package peermanagement

import "github.com/LeJamon/go-xrpl/internal/peermanagement/message"

// peerTxQueueMax is rippled reduce_relay::kMaxTxQueueSize. The queue is
// drained in txQueueMaxEntriesPerFrame-sized batches, so a large backlog makes
// progress on every timer tick instead of repeatedly starving its tail.
const peerTxQueueMax = 10_000

// addTxQueue records a transaction for the next reduce-relay announcement.
// Hashes are de-duplicated per peer. When the cap is reached, one frame is
// drained before admitting the new hash, matching rippled's addTxQueue
// behaviour.
func (p *Peer) addTxQueue(hash [32]byte) {
	if hash == ([32]byte{}) {
		return
	}
	for {
		p.txQueueMu.Lock()
		if p.txQueueSet == nil {
			p.txQueueSet = make(map[[32]byte]struct{})
		}
		if _, exists := p.txQueueSet[hash]; exists {
			p.txQueueMu.Unlock()
			return
		}
		if len(p.txQueue) < peerTxQueueMax {
			p.txQueue = append(p.txQueue, hash)
			p.txQueueSet[hash] = struct{}{}
			p.txQueueMu.Unlock()
			return
		}
		p.txQueueMu.Unlock()

		// sendTxQueue removes hashes only after Send successfully admits the
		// frame. If admission fails, retaining the full queue is safer than
		// evicting a transaction that has not yet been announced.
		if err := p.sendTxQueue(); err != nil {
			return
		}
	}
}

// removeTxQueue forgets a hash after this peer confirms it already has the
// transaction. Removing by value preserves FIFO order for the remaining
// deferred announcements.
func (p *Peer) removeTxQueue(hash [32]byte) {
	p.txQueueMu.Lock()
	defer p.txQueueMu.Unlock()
	if _, exists := p.txQueueSet[hash]; !exists {
		return
	}
	delete(p.txQueueSet, hash)
	for i, queued := range p.txQueue {
		if queued != hash {
			continue
		}
		copy(p.txQueue[i:], p.txQueue[i+1:])
		p.txQueue[len(p.txQueue)-1] = [32]byte{}
		p.txQueue = p.txQueue[:len(p.txQueue)-1]
		return
	}
}

func (p *Peer) txQueueLen() int {
	p.txQueueMu.Lock()
	defer p.txQueueMu.Unlock()
	return len(p.txQueue)
}

// sendTxQueue emits one queued hash batch. The queue is committed only after
// the frame is accepted by Peer.Send, so a bounded-queue failure leaves the
// hashes available for the next timer tick.
func (p *Peer) sendTxQueue() error {
	p.txQueueMu.Lock()
	defer p.txQueueMu.Unlock()

	n := len(p.txQueue)
	if n > txQueueMaxEntriesPerFrame {
		n = txQueueMaxEntriesPerFrame
	}
	if n == 0 {
		return nil
	}
	wire := make([][]byte, n)
	for i, hash := range p.txQueue[:n] {
		wire[i] = append([]byte(nil), hash[:]...)
	}
	frame, err := message.EncodeFrame(&message.HaveTransactions{Hashes: wire})
	if err != nil {
		return err
	}
	if err := p.Send(frame); err != nil {
		return err
	}
	for _, hash := range p.txQueue[:n] {
		delete(p.txQueueSet, hash)
	}
	copy(p.txQueue, p.txQueue[n:])
	for i := len(p.txQueue) - n; i < len(p.txQueue); i++ {
		p.txQueue[i] = [32]byte{}
	}
	p.txQueue = p.txQueue[:len(p.txQueue)-n]
	return nil
}

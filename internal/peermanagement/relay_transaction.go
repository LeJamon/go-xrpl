package peermanagement

import (
	"bytes"
	"math/rand/v2"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
)

// peerTxReduceRelayEnabled reports whether a peer negotiated the
// tx-reduce-relay feature. Reads the peer's capabilities directly so it is
// safe to call while holding o.peersMu (unlike PeerSupports, which re-locks).
func peerTxReduceRelayEnabled(p *Peer) bool {
	caps := p.Capabilities()
	return caps != nil && caps.HasFeature(FeatureTxReduceRelay)
}

// RelayTransaction relays a peer-originated transaction frame to connected
// peers using rippled's reduce-relay peer selection (OverlayImpl::relay,
// OverlayImpl.cpp:1214-1294, with getActivePeers at 1062-1094).
//
// When tx-reduce-relay is disabled or the active peer count is at or below the
// minimum (TxReduceRelayMinPeers + peers without the feature), the full frame
// is sent to every candidate peer. Otherwise it is sent to all peers without
// the feature plus a TxRelayPercentage share of the enabled peers; the
// remaining enabled peers learn of the transaction via their per-peer
// TMHaveTransactions queue, drained by the periodic sendTxQueueAnnounce tick.
//
// except is the originating peer: go-xrpl's single-element toSkip. rippled also
// skips peers a HashRouter marks as already holding the tx, which go-xrpl does
// not track (see router.relayTransaction); suppressed is therefore reported as
// the single origin peer.
func (o *Overlay) RelayTransaction(except PeerID, frame []byte) {
	hash, ok := transactionHashFromFrame(frame)
	o.relayTransaction(except, hash, ok, frame)
}

// transactionHashFromFrame extracts the canonical transaction blob from a
// wire TMTransaction frame and derives the XRPL transaction ID. A false
// result means the caller must not suppress the full transaction: without a
// usable hash a HAVE_TRANSACTIONS announcement could never be fulfilled.
func transactionHashFromFrame(frame []byte) ([32]byte, bool) {
	header, payload, err := message.ReadMessage(bytes.NewReader(frame))
	if err != nil || header.MessageType != message.TypeTransaction {
		return [32]byte{}, false
	}
	decoded, err := message.Decode(message.TypeTransaction, payload)
	if err != nil {
		return [32]byte{}, false
	}
	txn, ok := decoded.(*message.Transaction)
	if !ok || len(txn.RawTransaction) == 0 {
		return [32]byte{}, false
	}
	return sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), txn.RawTransaction), true
}

func (o *Overlay) relayTransaction(except PeerID, hash [32]byte, hashOK bool, frame []byte) {
	// getActivePeers (OverlayImpl.cpp:1062-1094): total counts every active
	// peer; disabled counts peers without the feature; candidates are the
	// peers not in toSkip; enabledInSkip counts skipped peers that have the
	// feature (they already hold the tx, so they count toward the quota).
	var (
		total         uint64
		disabled      uint64
		enabledInSkip uint64
		candidates    []*Peer
	)
	for _, target := range o.connectedPeers() {
		id, peer := target.id, target.peer
		total++
		enabled := peerTxReduceRelayEnabled(peer)
		if !enabled {
			disabled++
		}
		if id == except {
			if enabled {
				enabledInSkip++
			}
			continue
		}
		candidates = append(candidates, peer)
	}

	result := fanoutResult{operation: "relay-transaction"}
	defer func() {
		o.logFanoutFailure(result.err())
	}()
	sendFull := func(p *Peer) bool {
		err := p.Send(frame)
		result.record(err)
		return err == nil
	}

	const suppressed = 1 // go-xrpl's toSkip is the single originating peer

	minPeers := uint64(o.cfg.TxReduceRelayMinPeers)
	if minPeers == 0 {
		minPeers = DefaultTxReduceRelayMinPeers
	}
	minRelay := minPeers + disabled

	// All-relay path: feature off, or too few peers to bother reducing
	// (OverlayImpl.cpp:1251-1259).
	if !o.cfg.EnableTxReduceRelay || total <= minRelay {
		for _, p := range candidates {
			sendFull(p)
		}
		if o.cfg.EnableTxReduceRelay || o.cfg.EnableTxReduceRelayMetrics {
			o.txm.addRelayPeers(total, suppressed, 0)
		}
		return
	}

	// More peers than the minimum: relay in full to every disabled peer and
	// to a TxRelayPercentage share of the enabled peers above the minimum
	// (OverlayImpl.cpp:1264-1293).
	pct := uint64(o.cfg.TxRelayPercentage)
	if pct == 0 {
		pct = DefaultTxRelayPercentage
	}
	enabledTarget := minPeers + (total-minRelay)*pct/100
	o.txm.addRelayPeers(enabledTarget, suppressed, disabled)

	if enabledTarget > enabledInSkip {
		rand.Shuffle(len(candidates), func(i, j int) {
			candidates[i], candidates[j] = candidates[j], candidates[i]
		})
	}

	enabledAndRelayed := enabledInSkip
	relayTransactionCandidates(candidates, enabledAndRelayed, enabledTarget, sendFull,
		func(peer *Peer) bool {
			return hashOK && peer.addTxQueue(hash)
		})
}

func relayTransactionCandidates(
	candidates []*Peer,
	enabledAndRelayed uint64,
	enabledTarget uint64,
	sendFull func(*Peer) bool,
	queue ...func(*Peer) bool,
) {
	for _, p := range candidates {
		switch {
		case !peerTxReduceRelayEnabled(p):
			sendFull(p) // always relay to peers without the feature
		case enabledAndRelayed < enabledTarget:
			if sendFull(p) {
				enabledAndRelayed++
			}
		default:
			// Remaining enabled peers learn of the tx via their per-peer
			// TMHaveTransactions queue.
			if len(queue) > 0 && queue[0] != nil {
				if !queue[0](p) {
					// A suppressed announcement is only safe when the hash was
					// retained. Fall back to the full frame if queue admission
					// fails or the frame did not contain a usable transaction ID.
					sendFull(p)
				}
			} else {
				// Keep the helper's standalone behaviour lossless for callers that
				// do not provide a queue implementation.
				sendFull(p)
			}
		}
	}
}

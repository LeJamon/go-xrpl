package peermanagement

import (
	"math/rand/v2"
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
	o.relayTransaction(except, [32]byte{}, frame)
}

// RelayTransactionWithHash is RelayTransaction with the transaction ID used
// to populate suppressed peers' lossless reduce-relay queues.
func (o *Overlay) RelayTransactionWithHash(except PeerID, hash [32]byte, frame []byte) {
	o.relayTransaction(except, hash, frame)
}

func (o *Overlay) relayTransaction(except PeerID, hash [32]byte, frame []byte) {
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
		func(peer *Peer) {
			if hash != ([32]byte{}) {
				peer.addTxQueue(hash)
			}
		})
}

func relayTransactionCandidates(
	candidates []*Peer,
	enabledAndRelayed uint64,
	enabledTarget uint64,
	sendFull func(*Peer) bool,
	queue ...func(*Peer),
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
				queue[0](p)
			}
		}
	}
}

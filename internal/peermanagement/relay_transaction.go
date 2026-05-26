package peermanagement

import (
	"context"
	"errors"
	"log/slog"
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
// remaining enabled peers learn of the transaction via the periodic
// TMHaveTransactions announce (sendTxQueueAnnounce) — goXRPL's analogue of
// rippled's per-peer addTxQueue, since that announce already gossips the
// open-ledger tx hashes to every tx-reduce-relay peer each second.
//
// except is the originating peer: goXRPL's single-element toSkip. rippled also
// skips peers a HashRouter marks as already holding the tx, which goXRPL does
// not track (see router.relayTransaction); suppressed is therefore reported as
// the single origin peer.
func (o *Overlay) RelayTransaction(except PeerID, frame []byte) {
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()

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
	for id, peer := range o.peers {
		if peer.State() != PeerStateConnected {
			continue
		}
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

	sendFull := func(p *Peer) {
		if err := p.Send(frame); err != nil {
			level := slog.LevelInfo
			if errors.Is(err, ErrSendBufferFull) {
				level = slog.LevelWarn
			}
			slog.Log(context.Background(), level, "relay-transaction send failed",
				"t", "Overlay", "frame_size", len(frame), "err", err.Error())
		}
	}

	const suppressed = 1 // goXRPL's toSkip is the single originating peer

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
	for _, p := range candidates {
		switch {
		case !peerTxReduceRelayEnabled(p):
			sendFull(p) // always relay to peers without the feature
		case enabledAndRelayed < enabledTarget:
			enabledAndRelayed++
			sendFull(p)
		default:
			// Remaining enabled peers learn of the tx via the periodic
			// TMHaveTransactions announce (rippled's addTxQueue analogue).
		}
	}
}

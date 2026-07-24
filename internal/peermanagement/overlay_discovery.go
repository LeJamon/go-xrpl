package peermanagement

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// discoveryLoop periodically attempts to connect to new peers.
func (o *Overlay) discoveryLoop(ctx context.Context) error {
	// Immediate first attempt on startup
	o.autoconnect(ctx)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			o.autoconnect(ctx)
		case <-o.autoconnectWake:
			o.autoconnect(ctx)
		}
	}
}

func (o *Overlay) shouldLogAllBootstrapSourcesUnviable(status bootstrapSourceStatus) bool {
	if !status.allUnviable() {
		o.bootstrapDiag.Store(0)
		return false
	}
	return o.bootstrapDiag.Swap(status.episode) != status.episode
}

func (o *Overlay) wakeAutoconnect() {
	select {
	case o.autoconnectWake <- struct{}{}:
	default:
	}
}

func (o *Overlay) prepareAutoconnect(count int) (int, bool, *bootstrapLease, bool) {
	cold := !o.bootstrap.isReady()
	if !cold {
		return count, false, nil, true
	}
	lease, ok := o.bootstrap.tryReserve()
	if !ok {
		return count, true, nil, true
	}
	return count, true, lease, true
}

// autoconnect attempts to connect to peers if we need more.
func (o *Overlay) autoconnect(ctx context.Context) {
	o.reconcileDiscoveryConnected()

	count := o.cfg.MaxOutbound - o.ordinaryOutboundCount()
	count = max(count, 0)
	if count == 0 {
		return
	}

	count, cold, lease, ok := o.prepareAutoconnect(count)
	if !ok {
		slog.Info("Autoconnect", "t", "Overlay", "candidates", 0, "needed", 0, "bootstrap", "waiting")
		return
	}

	addrs := o.discovery.selectPeersToConnect(count, cold)
	if len(addrs) == 0 {
		lease.release()
		if cold {
			status := o.discovery.bootstrapSourceSummary()
			state := "no-viable-source"
			if status.allUnviable() {
				state = "all-known-sources-unviable"
				if !o.shouldLogAllBootstrapSourcesUnviable(status) {
					return
				}
			} else {
				o.shouldLogAllBootstrapSourcesUnviable(status)
			}
			slog.Info("Autoconnect", "t", "Overlay", "candidates", 0, "needed", count,
				"bootstrap", state, "known_sources", status.known, "unviable_sources", status.unviable)
			return
		}
	}
	if cold {
		o.bootstrapDiag.Store(0)
	}
	slog.Info("Autoconnect", "t", "Overlay", "candidates", len(addrs), "needed", count)
	for i, addr := range addrs {
		select {
		case <-ctx.Done():
			lease.release()
			for _, pending := range addrs[i:] {
				o.discovery.finishConnectAttempt(pending, connectAttemptReleased)
			}
			return
		case o.outboundSem <- struct{}{}:
		}
		dialDone, ok := o.beginPeerStart()
		if !ok {
			<-o.outboundSem
			lease.release()
			for _, pending := range addrs[i:] {
				o.discovery.finishConnectAttempt(pending, connectAttemptReleased)
			}
			return
		}
		peerLease := lease
		lease = nil
		go func(a string, bootstrapLease *bootstrapLease, dialDone func()) {
			defer dialDone()
			defer func() { <-o.outboundSem }()
			err := o.connectReserved(a, bootstrapLease)
			result := connectAttemptSucceeded
			if err != nil {
				bootstrapLease.release()
				if bootstrapLease != nil && ctx.Err() == nil {
					o.wakeAutoconnect()
				}
				result = connectAttemptReleased
				if ctx.Err() == nil &&
					!errors.Is(err, ErrAlreadyConnected) &&
					!errors.Is(err, ErrMaxPeersReached) {
					result = connectAttemptFailed
				}
			}
			o.discovery.finishConnectAttempt(a, result)
			if err != nil {
				slog.Info("Peer connection failed", "t", "Overlay", "addr", a, "err", err)
			} else {
				slog.Info("Peer connected", "t", "Overlay", "addr", a)
			}
		}(addr, peerLease, dialDone)
	}
}

// maintenanceLoop performs periodic maintenance tasks.
func (o *Overlay) maintenanceLoop(ctx context.Context) error {
	// idleSweepTicker drives the reduce-relay idle-peer sweep (G2).
	// Cadence is Idled/2 (4s) so no relay peer stays referenced more
	// than ~1.5x the idle threshold before being evicted. Without
	// this sweep, r.slots only shrinks on explicit RemovePeer and
	// accumulates stale entries for validators we no longer see.
	idleSweepTicker := time.NewTicker(Idled / 2)
	defer idleSweepTicker.Stop()

	// endpointsTicker drives the periodic TMEndpoints emission. The
	// helper itself decides per-peer whether to actually emit.
	endpointsTicker := time.NewTicker(endpointsBroadcastInterval)
	defer endpointsTicker.Stop()

	// clusterTicker drives the periodic TMCluster gossip.
	// sendClusterUpdate early-returns when cluster is empty, so this
	// is essentially free for non-cluster deployments.
	clusterTicker := time.NewTicker(clusterBroadcastInterval)
	defer clusterTicker.Stop()

	// txQueueTicker drives the periodic TMHaveTransactions emission
	// for tx-reduce-relay. sendTxQueueAnnounce early-returns when
	// EnableTxReduceRelay is off, so this is free for the default
	// configuration.
	txQueueTicker := time.NewTicker(txQueueBroadcastInterval)
	defer txQueueTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-idleSweepTicker.C:
			if o.relay != nil {
				o.relay.deleteIdlePeers(now)
			}
		case <-endpointsTicker.C:
			o.sendEndpoints()
		case <-clusterTicker.C:
			o.sendClusterUpdate()
		case <-txQueueTicker.C:
			o.sendTxQueueAnnounce()
		}
	}
}

// handleSquelch is called by the relay system when a peer should be squelched
// or unsquelched for a given validator. It constructs a TMSquelch message and
// delivers it to the specific peer (unicast).
func (o *Overlay) handleSquelch(validator []byte, peerID PeerID, squelch bool, duration time.Duration) {
	peer, exists := o.getPeer(peerID)
	if !exists {
		return
	}

	msg := &message.Squelch{
		Squelch:         squelch,
		ValidatorPubKey: validator,
	}
	if squelch {
		// The wire carries the duration as seconds. Only set on
		// squelch=true — on un-squelch the peer ignores this field
		// per the XRPL reduce-relay protocol.
		msg.SquelchDuration = uint32(duration / time.Second)
	}

	frame, err := message.EncodeFrame(msg)
	if err != nil {
		slog.Warn("Squelch encode failed", "t", "Overlay", "peer", peerID, "err", err)
		return
	}

	if err := peer.Send(frame); err != nil {
		slog.Info("Squelch send failed", "t", "Overlay", "peer", peerID, "err", err)
	}
}

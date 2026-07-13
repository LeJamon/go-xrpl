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
		}
	}
}

// autoconnect attempts to connect to peers if we need more.
func (o *Overlay) autoconnect(ctx context.Context) {
	// Reconcile discovery's Connected view with the live overlay peer
	// set first. Without this, an event-bus race on disconnect can
	// leave fixed peers marked Connected=true in d.peers even after
	// their TCP connection ended — and SelectPeersToConnect filters
	// them out forever. Observed in iter23/24 soak: a single dropped
	// rippled connection on goxrpl-1 stranded the network sub-quorum
	// because Autoconnect reported `candidates=0 needed=N` indefinitely.
	o.reconcileDiscoveryConnected()

	if !o.discovery.NeedsMorePeers() {
		return
	}

	count := o.cfg.MaxOutbound - o.outboundCount()
	if count <= 0 {
		return
	}

	addrs := o.discovery.SelectPeersToConnect(count)
	slog.Info("Autoconnect", "t", "Overlay", "candidates", len(addrs), "needed", count)
	for i, addr := range addrs {
		select {
		case <-ctx.Done():
			for _, pending := range addrs[i:] {
				o.discovery.finishConnectAttempt(pending, connectAttemptReleased)
			}
			return
		case o.outboundSem <- struct{}{}:
		}
		go func(a string) {
			defer func() { <-o.outboundSem }()
			err := o.Connect(a)
			result := connectAttemptSucceeded
			if err != nil {
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
		}(addr)
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

	encoded, err := message.Encode(msg)
	if err != nil {
		slog.Warn("Squelch encode failed", "t", "Overlay", "peer", peerID, "err", err)
		return
	}
	frame, err := message.BuildWireMessage(message.TypeSquelch, encoded)
	if err != nil {
		slog.Warn("Squelch frame build failed", "t", "Overlay", "peer", peerID, "err", err)
		return
	}

	if err := peer.Send(frame); err != nil {
		slog.Info("Squelch send failed", "t", "Overlay", "peer", peerID, "err", err)
	}
}

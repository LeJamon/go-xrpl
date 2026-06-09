// Outbound peer-protocol emissions that aren't already covered by the
// per-peer Send wrappers in overlay.go. Specifically:
//   - TMHaveTransactionSet announce (rippled OverlayImpl::for_each →
//     PeerImp::send(mtHAVE_SET) in the post-LCL "we have this set"
//     path);
//   - Periodic TMCluster gossip (rippled NetworkOPs.cpp:1118-1162,
//     setClusterTimer at NetworkOPs.cpp:1006-1020, 10s cadence);
//   - Periodic TMHaveTransactions emission for the tx-reduce-relay
//     feature (rippled OverlayImpl::sendTxQueue at
//     OverlayImpl.cpp:1366-1373, fired from Timer::on_timer when
//     TX_REDUCE_RELAY_ENABLE is set).
//
// Each emitter is driven from the maintenance loop.

package peermanagement

import (
	"log/slog"
	"time"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/cluster"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// clusterBroadcastInterval matches rippled's setClusterTimer cadence
// of 10s (NetworkOPs.cpp:1013). Long enough that a single missed tick
// doesn't ripple, short enough that newly-joined cluster members hear
// us within the first close-cycle they participate in.
const clusterBroadcastInterval = 10 * time.Second

// txQueueBroadcastInterval drives the periodic tx-reduce-relay
// outbound emission. Matches rippled's OverlayImpl::Timer::on_timer
// at 1s (OverlayImpl.cpp:104-108). Rippled only emits when the
// per-peer txQueue_ has at least one entry; go-xrpl doesn't maintain
// per-peer queues, so the emit body itself early-returns on an empty
// open-ledger hashes snapshot (see sendTxQueueAnnounce) — same effect
// for the (common) empty-mempool case.
const txQueueBroadcastInterval = 1 * time.Second

// txQueueMaxEntriesPerFrame caps a single outbound TMHaveTransactions
// frame. Matches rippled reduce_relay::MAX_TX_QUEUE_SIZE (64). Beyond
// that, rippled rotates to a fresh frame; we drop the tail since
// go-xrpl has no per-peer cursor state.
const txQueueMaxEntriesPerFrame = 64

// buildFrame encodes msg and wraps it in a msgType wire frame.
// Failures are logged at debug level under opName, with any extra
// logAttrs appended, and return nil.
func buildFrame(msgType message.MessageType, msg message.Message, opName string, logAttrs ...any) []byte {
	encoded, err := message.Encode(msg)
	if err != nil {
		slog.Debug(opName+" encode failed",
			append([]any{"t", "Overlay"}, append(logAttrs, "err", err)...)...)
		return nil
	}
	frame, err := message.BuildWireMessage(msgType, encoded)
	if err != nil {
		slog.Debug(opName+" frame build failed",
			append([]any{"t", "Overlay"}, append(logAttrs, "err", err)...)...)
		return nil
	}
	return frame
}

// encodeAndSend builds a msgType wire frame from msg and sends it to
// peer, logging failures at debug level under opName.
func encodeAndSend(peer *Peer, msgType message.MessageType, msg message.Message, opName string) {
	frame := buildFrame(msgType, msg, opName, "peer", peer.ID())
	if frame == nil {
		return
	}
	if err := peer.Send(frame); err != nil {
		slog.Debug(opName+" send failed",
			"t", "Overlay", "peer", peer.ID(), "err", err)
	}
}

// BroadcastHaveTxSet announces that we hold a particular transaction
// set, mirroring rippled's post-consensus mtHAVE_SET emission. The
// consensus adaptor calls this once per BuildTxSet so peers acquiring
// the same set via mtHAVE_SET{tsNEED} can find a source without
// polling.
//
// Note: rippled also emits mtHAVE_SET in response to direct queries;
// that path is already covered inline in router.handleHaveSet. The
// outbound-on-build branch here is the previously-missing half.
func (o *Overlay) BroadcastHaveTxSet(setID [32]byte) {
	msg := &message.HaveTransactionSet{
		Status: message.TxSetStatusHave,
		Hash:   setID[:],
	}
	frame := buildFrame(message.TypeHaveSet, msg, "HaveTxSet announce")
	if frame == nil {
		return
	}
	_ = o.Broadcast(frame)
}

// sendClusterUpdate emits a single mtCLUSTER frame to every connected
// cluster-member peer. Mirrors rippled's NetworkOPsImp::processClusterTimer
// (NetworkOPs.cpp:1118-1162): refresh our own ClusterNode entry first
// so peers see our current load, then build the wire message from the
// registry and send to peers whose Peer::cluster() is true.
//
// Skips early when no cluster is configured (cluster.Size() == 0),
// matching rippled NetworkOPs.cpp:1121-1122.
func (o *Overlay) sendClusterUpdate() {
	if o.cluster == nil || o.cluster.Size() == 0 {
		return
	}

	// Refresh our own entry and gate the broadcast on the update's
	// "this is news" return value — mirrors rippled
	// NetworkOPs.cpp:1126-1139, where a stale reportTime returns
	// false and the broadcast is skipped (with "Too soon to send
	// cluster update" log). The self-entry's loadFee is sourced from
	// localLoadFeeProvider (LoadFeeTrack.GetLocalFee); 0 when unwired,
	// matching the pre-LoadFeeTrack behaviour.
	if len(o.localNodeIdentity) > 0 {
		var selfFee uint32
		if o.localLoadFeeProvider != nil {
			selfFee = o.localLoadFeeProvider()
		}
		if !o.cluster.Update(o.localNodeIdentity, "", selfFee, time.Now()) {
			return
		}
	}

	clusterMsg := &message.Cluster{
		ClusterNodes: make([]message.ClusterNode, 0, o.cluster.Size()),
	}
	o.cluster.ForEach(func(m cluster.Member) {
		encoded, err := addresscodec.EncodeNodePublicKey(m.Identity)
		if err != nil {
			return
		}
		clusterMsg.ClusterNodes = append(clusterMsg.ClusterNodes, message.ClusterNode{
			PublicKey:  encoded,
			ReportTime: uint32(m.ReportTime.Unix()),
			NodeLoad:   m.LoadFee,
			NodeName:   m.Name,
		})
	})
	if len(clusterMsg.ClusterNodes) == 0 {
		return
	}

	// Attach our resource-manager gossip so cluster peers fold our
	// per-source charge accounting into theirs. Mirrors rippled
	// NetworkOPs.cpp:1151-1157, which appends one TMLoadSource per
	// exportConsumers() item (name=address, cost=balance).
	if o.resourceManager != nil {
		for _, item := range o.resourceManager.ExportConsumers().Items {
			clusterMsg.LoadSources = append(clusterMsg.LoadSources, message.LoadSource{
				Name: item.Address,
				Cost: uint32(item.Balance),
			})
		}
	}

	frame := buildFrame(message.TypeCluster, clusterMsg, "TMCluster")
	if frame == nil {
		return
	}

	// Send only to cluster-member peers — rippled's send_if(...,
	// peer_in_cluster()) at NetworkOPs.cpp:1158-1160.
	o.peersMu.RLock()
	defer o.peersMu.RUnlock()
	for id, peer := range o.peers {
		if peer.State() != PeerStateConnected {
			continue
		}
		token := peer.RemotePublicKey()
		if token == nil {
			continue
		}
		if _, ok := o.cluster.Member(token.Bytes()); !ok {
			continue
		}
		if err := peer.Send(frame); err != nil {
			slog.Debug("TMCluster send failed",
				"t", "Overlay", "peer", id, "err", err)
		}
	}
}

// sendTxQueueAnnounce emits a periodic TMHaveTransactions frame to
// every tx-reduce-relay-negotiated peer. Mirrors rippled's
// OverlayImpl::sendTxQueue at OverlayImpl.cpp:1366-1373 except that
// rippled maintains per-peer txQueue_ accumulators (PeerImp.cpp:303-
// 320) while go-xrpl announces the open-ledger snapshot.
//
// Skipped entirely when EnableTxReduceRelay is off — that's the
// operator opt-in that gates whether we advertise the feature in
// handshake at all. Without the advertisement no peer will negotiate,
// so the gossip would land on deaf ears.
func (o *Overlay) sendTxQueueAnnounce() {
	if !o.cfg.EnableTxReduceRelay {
		return
	}
	provider := o.openLedgerHashesProviderSnapshot()
	if provider == nil {
		return
	}

	hashes := provider()
	if len(hashes) == 0 {
		return
	}
	if len(hashes) > txQueueMaxEntriesPerFrame {
		hashes = hashes[:txQueueMaxEntriesPerFrame]
	}

	wire := make([][]byte, 0, len(hashes))
	for _, h := range hashes {
		hh := h
		wire = append(wire, hh[:])
	}
	msg := &message.HaveTransactions{Hashes: wire}
	frame := buildFrame(message.TypeHaveTransactions, msg, "HaveTransactions")
	if frame == nil {
		return
	}

	o.peersMu.RLock()
	defer o.peersMu.RUnlock()
	for id, peer := range o.peers {
		if peer.State() != PeerStateConnected {
			continue
		}
		if !o.PeerSupports(id, FeatureTxReduceRelay) {
			continue
		}
		if err := peer.Send(frame); err != nil {
			slog.Debug("HaveTransactions send failed",
				"t", "Overlay", "peer", id, "err", err)
		}
	}
}

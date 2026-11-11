package peermanagement

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/protocol"
)

// handleEvent dispatches events to appropriate handlers.
func (o *Overlay) handleEvent(evt Event) {
	switch evt.Type {
	case EventPeerConnected:
		o.onPeerConnected(evt)
	case EventPeerDisconnected:
		o.onPeerDisconnected(evt)
	case EventPeerFailed:
		o.onPeerFailed(evt)
	case EventMessageReceived:
		o.onMessageReceived(evt)
	case EventLedgerResponse:
		o.onLedgerResponse(evt)
	}
}

func (o *Overlay) onPeerConnected(evt Event) {
	if cb := o.onPeerConnectSnapshot(); cb != nil {
		cb(evt.PeerID)
	}
}

func (o *Overlay) onPeerDisconnected(evt Event) {
	o.peerDisconnects.Add(1)
	o.cancelServePeer(evt.PeerID)
	o.relay.RemovePeer(evt.PeerID)
	// Fire the higher-layer disconnect callback so per-peer state in
	// consumers (router peerStates, adaptor peerLCLs) gets cleaned.
	// Without this the peer's last-reported ledger stays in the
	// engine's getNetworkLedger vote set indefinitely, biasing
	// consensus toward the view of a peer that's no longer here.
	if cb := o.onPeerDisconnectSnapshot(); cb != nil {
		cb(evt.PeerID)
	}
}

// SetPeerDisconnectCallback registers a callback fired after a peer is
// removed from the overlay. The callback runs on the event-loop
// goroutine so implementations MUST NOT block — push to a channel if
// meaningful work is needed. Passing nil clears the callback.
//
// This is the channel by which higher layers (e.g. the consensus
// router) are notified of disconnects so they can clean their own
// per-peer state. Prefer this over polling Peers().
func (o *Overlay) SetPeerDisconnectCallback(cb func(PeerID)) {
	o.providersMu.Lock()
	o.onPeerDisconnect = cb
	o.providersMu.Unlock()
}

func (o *Overlay) onPeerDisconnectSnapshot() func(PeerID) {
	o.providersMu.RLock()
	defer o.providersMu.RUnlock()
	return o.onPeerDisconnect
}

// SetPeerConnectCallback registers a callback fired after a peer's
// handshake has completed and the peer is in the overlay's peer map.
// Same blocking contract as SetPeerDisconnectCallback: runs on the
// event loop and MUST NOT block. Passing nil clears the callback.
//
// Used by the consensus router to send our local validator manifest
// to a freshly-connected peer (#372), so peers configured under
// validator-list publishing can resolve our signing key back to the
// trusted master.
func (o *Overlay) SetPeerConnectCallback(cb func(PeerID)) {
	o.providersMu.Lock()
	o.onPeerConnect = cb
	o.providersMu.Unlock()
}

func (o *Overlay) onPeerConnectSnapshot() func(PeerID) {
	o.providersMu.RLock()
	defer o.providersMu.RUnlock()
	return o.onPeerConnect
}

func (o *Overlay) onPeerFailed(evt Event) {
	if o.discovery.bootCache != nil {
		o.discovery.bootCache.MarkFailed(evt.Endpoint.String())
	}
}

// onMessageReceived dispatches one inbound message by its descriptor: the
// feature gate first, then the overlay's own handler, then forwarding to
// the descriptor's route.
func (o *Overlay) onMessageReceived(evt Event) {
	if evt.charge == nil {
		if peer, ok := o.getPeer(evt.PeerID); ok {
			evt.charge = newMessageCharge(peer, evt.MessageType.String())
		}
	}
	defer evt.release()
	desc := describeMessage(evt.MessageType)

	// Record transport metrics before dispatch, including TMTransactions
	// frames that the batch handler later rejects. Transaction-processing
	// metrics for the batch contents are recorded only after admission.
	if o.shouldRecordTxMetrics(evt.PeerID) {
		o.recordInboundTxMetric(evt.MessageType, evt.Payload, evt.WireSize)
	}

	if !o.admitGated(&evt, desc.gate) {
		return
	}
	o.handleLocally(evt)
	if desc.route != routeNone {
		o.forwardInbound(&evt, desc.route)
	}
}

// admitGated reports whether the peer satisfies gate, charging and
// dropping the message when it does not.
func (o *Overlay) admitGated(evt *Event, gate *featureGate) bool {
	if gate == nil {
		return true
	}
	if (!gate.localTxReduceRelay || o.cfg.EnableTxReduceRelay) && o.PeerSupports(evt.PeerID, gate.feature) {
		return true
	}
	slog.Debug("Message from peer without negotiated feature; dropping", "t", "Overlay",
		"type", evt.MessageType.String(), "feature", gate.feature.String(), "peer", evt.PeerID)
	if gate.immediate {
		o.IncPeerBadData(evt.PeerID, gate.reason)
	} else {
		o.selectMessageCharge(evt, resource.FeeMalformedRequest(), gate.reason)
	}
	return false
}

// handleLocally runs the overlay's own handler for the message types it
// processes itself; other types pass through untouched.
func (o *Overlay) handleLocally(evt Event) {
	switch evt.MessageType {
	case message.TypePing:
		if o.handlePing(evt) {
			o.acknowledgePeerBootstrapPing(evt.PeerID)
		}
	case message.TypeSquelch:
		if !o.PeerSupports(evt.PeerID, FeatureVpReduceRelay) {
			slog.Debug("TMSquelch from peer without vprr feature; accepting (matches rippled)",
				"t", "Overlay", "peer", evt.PeerID)
		}
		o.handleSquelchMessage(evt)
	case message.TypeStatusChange:
		// The router also needs this frame for its peer-LCL view and
		// initial-sync acquisition (issue #381); the descriptor forwards it.
		o.handleStatusChange(evt)
	case message.TypeReplayDeltaReq:
		o.dispatchReplayDeltaRequest(evt)
	case message.TypeProofPathReq:
		o.dispatchProofPathRequest(evt)
	case message.TypeCluster:
		o.handleClusterMessage(evt)
	case message.TypeGetObjects:
		o.handleGetObjectsMessage(evt)
	case message.TypeHaveTransactions:
		o.handleHaveTransactionsMessage(evt)
	case message.TypeTransactions:
		o.handleTransactionsBatchMessage(evt)
	case message.TypeEndpoints:
		o.handleEndpointsMessage(evt)
	}
}

func (o *Overlay) forwardInbound(evt *Event, route overlayRoute) {
	msgType := evt.MessageType
	slog.Debug("Message received", "t", "Overlay", "type", msgType.String(), "peer", evt.PeerID, "size", len(evt.Payload))
	msg := evt.inboundMessage()
	switch route {
	case routeTransaction:
		o.forwardTransaction(msg)
	case routeLedgerData:
		o.forwardLedgerData(msg)
	case routeConsensus:
		o.forwardConsensus(msg)
	case routeConsensusControl:
		o.forwardConsensusControl(msg)
	default:
		select {
		case o.messages <- msg:
		default:
			_ = msg.Close()
			o.droppedMessages.Add(1)
			slog.Warn("Message dropped: channel full", "t", "Overlay", "type", msgType.String())
			if msgType == message.TypeManifests {
				o.RejectPeerBootstrap(evt.PeerID)
			}
		}
	}
}

func (o *Overlay) shouldRecordTxMetrics(peerID PeerID) bool {
	return o.cfg.EnableTxReduceRelayMetrics ||
		(o.cfg.EnableTxReduceRelay && o.PeerSupports(peerID, FeatureTxReduceRelay))
}

func (o *Overlay) forwardConsensus(msg *InboundMessage) {
	if o.consensusMessages == nil {
		_ = msg.Close()
		return
	}
	select {
	case o.consensusMessages <- msg:
	case <-o.stopCh:
		_ = msg.Close()
	case <-o.runDone:
		_ = msg.Close()
	}
}

func (o *Overlay) forwardConsensusControl(msg *InboundMessage) {
	if o.consensusControlMessages == nil {
		_ = msg.Close()
		return
	}
	select {
	case o.consensusControlMessages <- msg:
	case <-o.stopCh:
		_ = msg.Close()
	case <-o.runDone:
		_ = msg.Close()
	}
}

// forwardTransaction hands an inbound TMTransaction frame to the tx
// lane. A full lane means the MaxTransactions ceiling is reached, so the
// frame is shed and counted (surfaced as jq_trans_overflow). The
// originating peer resends and reduce-relay re-delivers via other peers,
// so a shed frame is recoverable. Shared by the wire path
// (onMessageReceived) and the TMTransactions batch fanout.
func (o *Overlay) forwardTransaction(msg *InboundMessage) {
	select {
	case o.txMessages <- msg:
	default:
		_ = msg.Close()
		// Counted via droppedTransactions (jq_trans_overflow); log at Debug so a
		// shed storm under load cannot itself flood the single log writer.
		o.droppedTransactions.Add(1)
		slog.Debug("Transaction queue is full", "t", "Overlay",
			"pending", len(o.txMessages), "max", cap(o.txMessages), "peer", msg.PeerID)
	}
}

// DroppedTransactions returns the cumulative count of TMTransaction
// frames refused at the overlay → router boundary. Surfaced via
// server_info as jq_trans_overflow.
func (o *Overlay) DroppedTransactions() uint64 {
	return o.droppedTransactions.Load()
}

// forwardLedgerData hands an acquisition reply to the bounded ledgerData lane.
// Waiting for capacity propagates backpressure to the peer read loop instead
// of discarding a reply needed to complete catch-up.
func (o *Overlay) forwardLedgerData(msg *InboundMessage) {
	if o.ledgerData == nil {
		_ = msg.Close()
		return
	}
	select {
	case o.ledgerData <- msg:
	case <-o.stopCh:
		_ = msg.Close()
	case <-o.runDone:
		_ = msg.Close()
	}
}

// DroppedMessages returns the cumulative count of best-effort inbound
// messages shed because the downstream consumer channel was full.
func (o *Overlay) DroppedMessages() uint64 {
	return o.droppedMessages.Load()
}

// PingTimeoutDisconnects returns the cumulative count of peers torn
// down because they failed to answer pings within pingTimeout. A
// nonzero, growing value flags either a flaky network or peers that
// have stopped servicing the overlay protocol.
func (o *Overlay) PingTimeoutDisconnects() uint64 {
	return o.pingTimeoutDisconnects.Load()
}

// PeerDisconnects returns the cumulative count of peers torn down for
// any reason. Surfaced via server_info.peer_disconnects.
func (o *Overlay) PeerDisconnects() uint64 {
	return o.peerDisconnects.Load()
}

// PeerDisconnectsResources returns the count of peers torn down by a
// resource.Consumer charge exceeding the drop threshold. Surfaced via
// server_info.peer_disconnects_resources.
func (o *Overlay) PeerDisconnectsResources() uint64 {
	return o.peerDisconnectsCharges.Load()
}

func (o *Overlay) ResourceStats() resource.Stats {
	if o.resourceManager == nil {
		return resource.Stats{}
	}
	return o.resourceManager.Stats()
}

// DroppedEvents returns the cumulative count of events dropped
// because the event loop fell behind. Non-zero growth means handlers
// are slow enough that blocking sends would have deadlocked the read
// hot path — investigate handler latency before raising the buffer.
func (o *Overlay) DroppedEvents() uint64 {
	return o.droppedEvents.Load()
}

// dispatchLifecycle delivers a peer lifecycle event to the event loop.
// Unlike the lossy EventMessageReceived path, lifecycle events must not be
// dropped: a lost EventPeerDisconnected leaks router/relay per-peer state
// until the idle sweep. The send blocks until the event loop accepts it
// (lifecycle volume is tiny and bounded by peer count), bailing only when
// the overlay is shutting down so a stopped event loop can't wedge the
// caller. Every caller is a handshake / run-watcher / autoconnect
// goroutine — never the event loop itself — so a blocking send cannot
// self-deadlock.
func (o *Overlay) dispatchLifecycle(evt Event) {
	select {
	case o.lifecycle <- evt:
	case <-o.stopCh:
	}
}

func (o *Overlay) notePeerRunEnded(err error) {
	if errors.Is(err, ErrPingTimeout) {
		o.pingTimeoutDisconnects.Add(1)
	}
}

// DroppedLedgerResponses returns the cumulative count of ledger-sync
// responses dropped due to a full events channel (see
// LedgerSyncHandler.sendReplayDeltaResponse / sendProofPathResponse).
// Same shape as DroppedMessages but for the server-side response path;
// delegates to the handler's own counter.
func (o *Overlay) DroppedLedgerResponses() uint64 {
	if o.ledgerSync != nil {
		return o.ledgerSync.DroppedResponses()
	}
	return 0
}

// dispatchReplayDeltaRequest decodes an inbound mtREPLAY_DELTA_REQ frame and
// routes it to the local LedgerSyncHandler. Decode failures are logged and
// dropped silently — a malformed request from a peer should not crash the
// dispatch loop. The handler answers via the configured LedgerProvider, which
// is wired at startup by the consensus adaptor, which can import
// internal/ledger, which this package cannot.
func (o *Overlay) dispatchReplayDeltaRequest(evt Event) {
	decoded, err := message.Decode(message.TypeReplayDeltaReq, evt.Payload)
	if err != nil {
		slog.Debug("ReplayDeltaRequest decode failed", "t", "Overlay", "peer", evt.PeerID, "err", err)
		o.selectMessageChargeReason(&evt, "replay-delta-req-decode")
		return
	}
	req, ok := decoded.(*message.ReplayDeltaRequest)
	if !ok {
		return
	}
	o.submitRetainedServe(evt, resource.FeeModerateBurdenPeer(),
		func(ctx context.Context) {
			if err := o.ledgerSync.HandleMessage(ctx, evt.PeerID, req); err != nil &&
				!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				slog.Debug("ReplayDeltaRequest handler error", "t", "Overlay", "peer", evt.PeerID, "err", err)
			}
		})
}

// dispatchProofPathRequest decodes an inbound mtPROOF_PATH_REQ frame and
// routes it to the local LedgerSyncHandler. Decode failures are logged
// and dropped silently — a malformed request from a peer should not
// crash the dispatch loop. The handler answers via the configured
// LedgerProvider, which is wired at startup by the consensus adaptor and can
// import internal/ledger, which this package cannot.
func (o *Overlay) dispatchProofPathRequest(evt Event) {
	decoded, err := message.Decode(message.TypeProofPathReq, evt.Payload)
	if err != nil {
		slog.Debug("ProofPathRequest decode failed", "t", "Overlay", "peer", evt.PeerID, "err", err)
		o.selectMessageChargeReason(&evt, "proof-path-req-decode")
		return
	}
	req, ok := decoded.(*message.ProofPathRequest)
	if !ok {
		return
	}
	o.submitRetainedServe(evt, resource.FeeModerateBurdenPeer(),
		func(ctx context.Context) {
			if err := o.ledgerSync.HandleMessage(ctx, evt.PeerID, req); err != nil &&
				!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				slog.Debug("ProofPathRequest handler error", "t", "Overlay", "peer", evt.PeerID, "err", err)
			}
		})
}

func (o *Overlay) handleStatusChange(evt Event) {
	decoded, err := message.Decode(message.TypeStatusChange, evt.Payload)
	if err != nil {
		slog.Debug("StatusChange decode failed", "t", "Overlay", "peer", evt.PeerID, "err", err)
		return
	}
	sc, ok := decoded.(*message.StatusChange)
	if !ok {
		return
	}
	peer, exists := o.getPeer(evt.PeerID)
	if !exists {
		return
	}
	if !sc.HasNetworkTime() {
		sc.NetworkTime = uint64(protocol.RippleSeconds(time.Now()))
		sc.NetworkTimeSet = true
	}

	effectiveStatus := peer.applyStatusChange(sc)

	// lostSync returns before either the tracking check or the
	// publish runs, so a lostSync update never surfaces as a
	// peer_status WebSocket event.
	if sc.HasNewEvent() && sc.NewEvent == message.NodeEventLostSync {
		return
	}

	// The tracking check is gated on a fresh (<2 min) validated
	// ledger. The gate must NOT short-circuit the publish below,
	// which runs unconditionally for non-lostSync messages.
	if sc.HasLedgerSeq() {
		if provider := o.validLedgerProviderSnapshot(); provider != nil {
			if validSeq, age, ok := provider(); ok && validSeq != 0 && age < 2*time.Minute {
				peer.CheckTracking(sc.LedgerSeq, validSeq)
			}
		}
	}

	// Publish to peer_status subscribers.
	if pub := o.peerStatusPublisherSnapshot(); pub != nil {
		// Emit ledger_hash whenever the wire carried the field,
		// sourcing the value from the peer's post-apply closed-ledger
		// state. When the wire bytes were malformed, applyStatusChange
		// clears that storage and the all-zeros 64-char hex string is
		// emitted.
		var ledgerHash string
		if sc.HasLedgerHash() {
			if h, ok := peer.ClosedLedger(); ok {
				ledgerHash = strings.ToUpper(hex.EncodeToString(h[:]))
			} else {
				ledgerHash = strings.Repeat("0", 64)
			}
		}
		// Emit min/max only when both wire fields were present.
		// nil-on-absence keeps that paired gate without conflating
		// value 0 with "absent".
		var minSeq, maxSeq *uint32
		if sc.FirstSeq != nil && sc.LastSeq != nil {
			f, l := *sc.FirstSeq, *sc.LastSeq
			minSeq, maxSeq = &f, &l
		}
		var ledgerIndex *uint32
		if sc.HasLedgerSeq() {
			ls := sc.LedgerSeq
			ledgerIndex = &ls
		}
		var status, action string
		if sc.HasNewStatus() {
			status = peerStatusUpperName(effectiveStatus)
		}
		if sc.HasNewEvent() {
			action = peerStatusActionName(sc.NewEvent)
		}
		var date *uint32
		if sc.HasNetworkTime() {
			value := uint32(sc.NetworkTime)
			date = &value
		}
		pub(PeerStatusUpdate{
			Status:         status,
			Action:         action,
			LedgerIndex:    ledgerIndex,
			LedgerHash:     ledgerHash,
			Date:           date,
			LedgerIndexMin: minSeq,
			LedgerIndexMax: maxSeq,
		})
	}
}

// handleSquelchMessage processes an inbound TMSquelch from a peer and
// updates the per-peer validator squelch table.
func (o *Overlay) handleSquelchMessage(evt Event) {
	decoded, err := message.Decode(message.TypeSquelch, evt.Payload)
	if err != nil {
		slog.Debug("Squelch decode failed", "t", "Overlay", "peer", evt.PeerID, "err", err)
		o.selectMessageChargeReason(&evt, "squelch-malformed-pubkey")
		return
	}
	sq, ok := decoded.(*message.Squelch)
	if !ok {
		return
	}
	// Validator pubkey must be a 33-byte compressed secp256k1 point.
	// Silently dropping would let a peer spam bogus TMSquelch frames
	// without penalty, so charge bad data.
	if len(sq.ValidatorPubKey) != 33 {
		slog.Debug("Squelch malformed pubkey",
			"t", "Overlay", "peer", evt.PeerID, "len", len(sq.ValidatorPubKey))
		o.selectMessageChargeReason(&evt, "squelch-malformed-pubkey")
		return
	}

	// Drop any inbound squelch whose target pubkey is our own
	// validator — otherwise a peer could silence our own traffic on
	// the RelayFromValidator path (self-silencing DoS).
	if ownPubKey := o.localValidatorPubKey(); len(ownPubKey) == 33 && bytes.Equal(sq.ValidatorPubKey, ownPubKey) {
		slog.Debug("Squelch dropped: targets local validator",
			"t", "Overlay", "peer", evt.PeerID)
		return
	}

	peer, exists := o.getPeer(evt.PeerID)
	if !exists {
		return
	}

	if !sq.Squelch {
		peer.RemoveSquelch(sq.ValidatorPubKey)
		return
	}
	duration := time.Duration(sq.SquelchDuration) * time.Second
	if ok, reason := peer.addSquelch(sq.ValidatorPubKey, duration); !ok {
		o.selectMessageChargeReason(&evt, reason)
		slog.Debug("Squelch ignored: invalid duration", "t", "Overlay", "peer", evt.PeerID, "duration", sq.SquelchDuration)
	}
}

func (o *Overlay) handlePing(evt Event) bool {
	decoded, err := message.Decode(message.TypePing, evt.Payload)
	if err != nil {
		return false
	}
	ping, ok := decoded.(*message.Ping)
	if !ok {
		return false
	}

	switch ping.PType {
	case message.PingTypePing:
		o.selectMessageCharge(&evt, resource.FeeModerateBurdenPeer(), "ping request")
		pong := message.Ping{PType: message.PingTypePong}
		if ping.HasSeq() {
			pong.Seq = ping.Seq
			pong.SeqSet = true
		}
		wireMsg, err := message.EncodeFrame(&pong)
		if err != nil {
			return false
		}
		o.Send(evt.PeerID, wireMsg)
	case message.PingTypePong:
		if peer, exists := o.getPeer(evt.PeerID); exists && ping.HasSeq() {
			peer.OnPong(ping.Seq, time.Now())
		}
	default:
		return false
	}
	return true
}

// onLedgerResponse ships an already-wire-framed ledger-sync response
// (produced by LedgerSyncHandler.send*Response) to the requesting peer.
// The payload MUST be a full wire frame (6-byte header + protobuf body)
// — see sendReplayDeltaResponse for the contract. Shipping a bare
// protobuf here caused B to parse the first 6 body bytes as a garbage
// wire header and stall for the phantom payload, which was the
// post-handshake I/O regression fixed alongside this comment.
func (o *Overlay) onLedgerResponse(evt Event) {
	o.SendPriority(evt.PeerID, evt.Payload)
}

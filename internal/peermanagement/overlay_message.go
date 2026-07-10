package peermanagement

import (
	"bytes"
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
	// Only track outbound connections in discovery — inbound endpoints
	// use ephemeral source ports that aren't connectable.
	if !evt.Inbound {
		o.discovery.MarkConnected(evt.Endpoint.String(), evt.PeerID)
	}
	// Notify higher layers AFTER discovery state is updated so any work
	// they do (e.g. sending us-originated frames to the peer) sees a
	// fully-bookkept overlay. Mirrors the disconnect callback ordering.
	if cb := o.onPeerConnectSnapshot(); cb != nil {
		cb(evt.PeerID)
	}
}

func (o *Overlay) onPeerDisconnected(evt Event) {
	o.peerDisconnects.Add(1)
	o.discovery.MarkDisconnected(evt.PeerID)
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

func (o *Overlay) onMessageReceived(evt Event) {
	msgType := message.MessageType(evt.MessageType)

	// Record reduce-relay traffic metrics before dispatch: counted on
	// the inbound path, by message type and on-wire payload size, gated
	// on the negotiated tx-reduce-relay feature or the metrics-only
	// override.
	if o.cfg.EnableTxReduceRelayMetrics ||
		(o.cfg.EnableTxReduceRelay && o.PeerSupports(evt.PeerID, FeatureTxReduceRelay)) {
		o.recordInboundTxMetric(msgType, evt.Payload, evt.WireSize)
	}

	// Handle PING at transport level — respond with PONG immediately
	if msgType == message.TypePing {
		o.handlePing(evt)
		return
	}

	// Inbound TMSquelch is accepted UNCONDITIONALLY, matching rippled —
	// there is no per-peer gating on vpReduceRelay for incoming
	// squelches. Feature negotiation governs what WE SEND (we only
	// emit TMSquelch to peers who advertised reduce-relay), not what
	// we accept: a squelch directive is harmless when applied — it
	// only suppresses what we send next — and rejecting it creates an
	// attack surface where a hostile peer could advertise one
	// capability set to us and another to a neighbor to desync
	// squelch state.
	if msgType == message.TypeSquelch {
		if !o.PeerSupports(evt.PeerID, FeatureVpReduceRelay) {
			slog.Debug("TMSquelch from peer without vprr feature; accepting (matches rippled)",
				"t", "Overlay", "peer", evt.PeerID)
		}
		o.handleSquelchMessage(evt)
		return
	}

	// mtSTATUS_CHANGE refreshes Closed-/Previous-Ledger hints and is
	// then forwarded to the consensus
	// router. The overlay handler updates per-peer state +
	// peer_status WS publishing; the consensus router needs the same
	// frame to update its peer-LCL view (Adaptor.UpdatePeerLCL feeds
	// getNetworkLedger) and to drive initial-sync ledger acquisition
	// (startLedgerAcquisition / checkBehind). Splitting at the
	// overlay and dropping here would leave the router blind to peer
	// status — a fresh node would never leave OpModeDisconnected and
	// the engine's timerEntry would never advance (issue #381).
	if msgType == message.TypeStatusChange {
		o.handleStatusChange(evt)
		// fall through to the o.messages forward
	}

	// Serve mtREPLAY_DELTA_REQ from the local ledger sync handler.
	// Before dispatching we verify the peer actually negotiated
	// ledger-replay in its handshake; a peer sending these without the
	// feature is silently dropped and charged bad data.
	if msgType == message.TypeReplayDeltaReq {
		if !o.peerNegotiatedLedgerReplay(evt.PeerID) {
			slog.Debug("ReplayDeltaRequest from peer without ledgerreplay feature; dropping",
				"t", "Overlay", "peer", evt.PeerID)
			o.IncPeerBadData(evt.PeerID, "replay-delta-req-unnegotiated")
			return
		}
		o.dispatchReplayDeltaRequest(evt)
		return
	}

	// Serve mtPROOF_PATH_REQ from the local ledger sync handler. Same
	// handshake-negotiation gate as mtREPLAY_DELTA_REQ above — the
	// proof-path protocol is part of the ledger-replay feature bundle.
	if msgType == message.TypeProofPathReq {
		if !o.peerNegotiatedLedgerReplay(evt.PeerID) {
			slog.Debug("ProofPathRequest from peer without ledgerreplay feature; dropping",
				"t", "Overlay", "peer", evt.PeerID)
			o.IncPeerBadData(evt.PeerID, "proof-path-req-unnegotiated")
			return
		}
		o.dispatchProofPathRequest(evt)
		return
	}

	// Response-path feature gate. A peer that didn't negotiate
	// ledgerreplay in handshake shouldn't be sending us
	// TMReplayDeltaResponse or TMProofPathResponse unsolicited. Gate
	// BEFORE forwarding to the router so a non-negotiated peer can't
	// wedge the inbound acquisition state with bogus responses.
	if msgType == message.TypeReplayDeltaResponse {
		if !o.peerNegotiatedLedgerReplay(evt.PeerID) {
			slog.Debug("TMReplayDeltaResponse from peer without ledgerreplay feature; dropping",
				"t", "Overlay", "peer", evt.PeerID)
			o.IncPeerBadData(evt.PeerID, "replay-delta-resp-unnegotiated")
			return
		}
	}
	if msgType == message.TypeProofPathResponse {
		if !o.peerNegotiatedLedgerReplay(evt.PeerID) {
			slog.Debug("TMProofPathResponse from peer without ledgerreplay feature; dropping",
				"t", "Overlay", "peer", evt.PeerID)
			o.IncPeerBadData(evt.PeerID, "proof-path-resp-unnegotiated")
			return
		}
	}

	// mtREPLAY_DELTA_RESPONSE / mtPROOF_PATH_RESPONSE that pass the
	// feature gate above reach the consensus router via the overlay's
	// Messages() channel — like every other peer-originated consensus
	// frame (mtLEDGER_DATA, mtPROPOSE, mtVALIDATION). Transactions ride
	// the separate TxMessages() lane. The router owns the verification +
	// adoption state and is the only place that can drive it.

	// Transport-level messages with no consensus-router impact are
	// handled inline here and NOT forwarded to o.messages.
	switch msgType {
	case message.TypeCluster:
		o.handleClusterMessage(evt)
		return
	case message.TypeGetObjects:
		o.handleGetObjectsMessage(evt)
		return
	case message.TypeHaveTransactions:
		o.handleHaveTransactionsMessage(evt)
		return
	case message.TypeTransactions:
		o.handleTransactionsBatchMessage(evt)
		return
	case message.TypeEndpoints:
		o.handleEndpointsMessage(evt)
		return
	}

	slog.Debug("Message received", "t", "Overlay", "type", msgType.String(), "peer", evt.PeerID, "size", len(evt.Payload))

	// Transactions ride a dedicated lane so a tx flood can't crowd
	// consensus/acquisition frames out of the messages channel and get
	// us resource-disconnected for dropping mtLEDGER_DATA/mtPROPOSE/
	// mtVALIDATION (issue #1103).
	if msgType == message.TypeTransaction {
		o.forwardTransaction(&InboundMessage{
			PeerID:  evt.PeerID,
			Type:    evt.MessageType,
			Payload: evt.Payload,
		})
		return
	}

	// Acquisition replies ride a dedicated lane so a
	// serve/propose/validation flood on the shared messages channel can't
	// shed a reply this node explicitly requested and wedge catch-up. The
	// replay-delta / proof-path responses already passed their feature gate
	// above.
	switch msgType {
	case message.TypeLedgerData, message.TypeReplayDeltaResponse, message.TypeProofPathResponse:
		o.forwardLedgerData(&InboundMessage{
			PeerID:  evt.PeerID,
			Type:    evt.MessageType,
			Payload: evt.Payload,
		})
		return
	}

	// Forward consensus/acquisition frames. On back-pressure (channel
	// full), increment a visible counter rather than silently dropping —
	// the warn log alone is easy to miss at production log levels.
	select {
	case o.messages <- &InboundMessage{
		PeerID:  evt.PeerID,
		Type:    evt.MessageType,
		Payload: evt.Payload,
	}:
	default:
		o.droppedMessages.Add(1)
		slog.Warn("Message dropped: channel full", "t", "Overlay", "type", msgType.String())
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

// forwardLedgerData hands an acquisition reply to the dedicated ledgerData
// lane. The lane is generously sized, so this sheds only under extreme
// outstanding-request volume; a shed frame warns and bumps droppedLedgerData,
// and the acquisition's own retry timer re-requests the missing nodes, so it
// is recoverable. Losing a reply this node explicitly requested is notable
// (it can stall catch-up), so this warns rather than logs at debug.
func (o *Overlay) forwardLedgerData(msg *InboundMessage) {
	select {
	case o.ledgerData <- msg:
	default:
		o.droppedLedgerData.Add(1)
		slog.Warn("Ledger-data lane full", "t", "Overlay",
			"pending", len(o.ledgerData), "max", cap(o.ledgerData), "peer", msg.PeerID)
	}
}

// DroppedLedgerData returns the cumulative count of acquisition replies
// shed because the dedicated ledgerData lane was full.
func (o *Overlay) DroppedLedgerData() uint64 {
	return o.droppedLedgerData.Load()
}

// DroppedMessages returns the cumulative count of inbound messages the
// overlay had to drop because the downstream consumer channel was
// full. Surfaced via server_info/server_state for operators to detect
// consumer back-pressure — a nonzero and growing value indicates the
// router/engine can't keep up with network ingress.
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

func (o *Overlay) ResourceManager() *resource.Manager {
	return o.resourceManager
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
// is wired at startup by the consensus adaptor (see
// internal/consensus/adaptor.NewLedgerProvider) — that layer can import
// internal/ledger, which this package cannot.
func (o *Overlay) dispatchReplayDeltaRequest(evt Event) {
	decoded, err := message.Decode(message.TypeReplayDeltaReq, evt.Payload)
	if err != nil {
		slog.Debug("ReplayDeltaRequest decode failed", "t", "Overlay", "peer", evt.PeerID, "err", err)
		o.IncPeerBadData(evt.PeerID, "replay-delta-req-decode")
		return
	}
	req, ok := decoded.(*message.ReplayDeltaRequest)
	if !ok {
		return
	}
	if err := o.ledgerSync.HandleMessage(o.ctx, evt.PeerID, req); err != nil {
		slog.Debug("ReplayDeltaRequest handler error", "t", "Overlay", "peer", evt.PeerID, "err", err)
		if errors.Is(err, ErrPeerBadRequest) {
			o.IncPeerBadData(evt.PeerID, "replay-delta-req-bad")
		}
	}
}

// dispatchProofPathRequest decodes an inbound mtPROOF_PATH_REQ frame and
// routes it to the local LedgerSyncHandler. Decode failures are logged
// and dropped silently — a malformed request from a peer should not
// crash the dispatch loop. The handler answers via the configured
// LedgerProvider, which is wired at startup by the consensus adaptor
// (see internal/consensus/adaptor.NewLedgerProvider) — that layer can
// import internal/ledger, which this package cannot.
func (o *Overlay) dispatchProofPathRequest(evt Event) {
	decoded, err := message.Decode(message.TypeProofPathReq, evt.Payload)
	if err != nil {
		slog.Debug("ProofPathRequest decode failed", "t", "Overlay", "peer", evt.PeerID, "err", err)
		o.IncPeerBadData(evt.PeerID, "proof-path-req-decode")
		return
	}
	req, ok := decoded.(*message.ProofPathRequest)
	if !ok {
		return
	}
	if err := o.ledgerSync.HandleMessage(o.ctx, evt.PeerID, req); err != nil {
		slog.Debug("ProofPathRequest handler error", "t", "Overlay", "peer", evt.PeerID, "err", err)
		if errors.Is(err, ErrPeerBadRequest) {
			o.IncPeerBadData(evt.PeerID, "proof-path-req-bad")
		}
	}
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
	// Stamp the wire's networktime with the local clock when the peer
	// didn't include it, so the peer_status emit always carries a
	// `date`. Mutate sc so the auto-filled value is observable to
	// subscribers.
	if sc.NetworkTime == 0 {
		sc.NetworkTime = uint64(time.Now().Unix() - protocol.RippleEpochUnix)
	}

	effectiveStatus := peer.applyStatusChange(sc)

	// lostSync returns before either the tracking check or the
	// publish runs, so a lostSync update never surfaces as a
	// peer_status WebSocket event.
	if sc.NewEvent == message.NodeEventLostSync {
		return
	}

	// The tracking check is gated on a fresh (<2 min) validated
	// ledger. The gate must NOT short-circuit the publish below,
	// which runs unconditionally for non-lostSync messages.
	if sc.LedgerSeq != 0 {
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
		if len(sc.LedgerHash) > 0 {
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
		// The decoder loses proto-presence for ledger_seq (see
		// internal/peermanagement/proto/ripple.pb.go), so use 0 as
		// the absence proxy — XRPL ledger sequences start at the
		// genesis ledger 1, no real peer broadcasts has_ledgerseq=0.
		var ledgerIndex *uint32
		if sc.LedgerSeq != 0 {
			ls := sc.LedgerSeq
			ledgerIndex = &ls
		}
		// Date is always set thanks to the auto-fill above. Truncate
		// uint64 → uint32 to match the uint32 date rippled emits.
		dateVal := uint32(sc.NetworkTime)
		pub(PeerStatusUpdate{
			Status:         peerStatusUpperName(effectiveStatus),
			Action:         peerStatusActionName(sc.NewEvent),
			LedgerIndex:    ledgerIndex,
			LedgerHash:     ledgerHash,
			Date:           &dateVal,
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
		o.IncPeerBadData(evt.PeerID, "squelch-malformed-pubkey")
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
		o.IncPeerBadData(evt.PeerID, "squelch-malformed-pubkey")
		return
	}

	// Drop any inbound squelch whose target pubkey is our own
	// validator — otherwise a peer could silence our own traffic on
	// the RelayFromValidator path (self-silencing DoS). go-xrpl
	// additionally charges the sending peer a bad-data event so
	// repeated attempts feed the eviction threshold; rippled just
	// logs-and-returns there.
	if ownPubKey := o.localValidatorPubKey(); len(ownPubKey) == 33 && bytes.Equal(sq.ValidatorPubKey, ownPubKey) {
		slog.Debug("Squelch dropped: targets local validator",
			"t", "Overlay", "peer", evt.PeerID)
		o.IncPeerBadData(evt.PeerID, "squelch-targets-self")
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
	if !peer.AddSquelch(sq.ValidatorPubKey, duration) {
		slog.Debug("Squelch ignored: invalid duration", "t", "Overlay", "peer", evt.PeerID, "duration", sq.SquelchDuration)
	}
}

func (o *Overlay) handlePing(evt Event) {
	decoded, err := message.Decode(message.TypePing, evt.Payload)
	if err != nil {
		return
	}
	ping, ok := decoded.(*message.Ping)
	if !ok {
		return
	}

	switch ping.PType {
	case message.PingTypePing:
		pong := &message.Ping{
			PType:    message.PingTypePong,
			Seq:      ping.Seq,
			PingTime: ping.PingTime,
		}
		encoded, err := message.Encode(pong)
		if err != nil {
			return
		}
		wireMsg, err := message.BuildWireMessage(message.TypePing, encoded)
		if err != nil {
			return
		}
		o.Send(evt.PeerID, wireMsg)
	case message.PingTypePong:
		if peer, exists := o.getPeer(evt.PeerID); exists {
			peer.OnPong(ping.Seq, time.Now())
		}
	}
}

// onLedgerResponse ships an already-wire-framed ledger-sync response
// (produced by LedgerSyncHandler.send*Response) to the requesting peer.
// The payload MUST be a full wire frame (6-byte header + protobuf body)
// — see sendReplayDeltaResponse for the contract. Shipping a bare
// protobuf here caused B to parse the first 6 body bytes as a garbage
// wire header and stall for the phantom payload, which was the
// post-handshake I/O regression fixed alongside this comment.
func (o *Overlay) onLedgerResponse(evt Event) {
	o.Send(evt.PeerID, evt.Payload)
}

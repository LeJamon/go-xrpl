package adaptor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/inbound"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

// inboundReplayDeltaTickInterval drives the periodic check for an
// in-flight replay-delta acquisition that has timed out. Tick rate is
// well below the acquisition timeout so a stuck request is detected
// promptly without burning CPU on a fast-path message loop.
const inboundReplayDeltaTickInterval = 5 * time.Second

// peerLedgerState tracks the latest ledger info reported by a peer.
type peerLedgerState struct {
	LedgerSeq  uint32
	LedgerHash [32]byte
}

// Router reads inbound messages from the P2P overlay and dispatches
// them to the consensus engine and adaptor.
type Router struct {
	engine      consensus.Engine
	adaptor     *Adaptor
	modeManager *ModeManager
	inbox       <-chan *peermanagement.InboundMessage
	logger      *slog.Logger

	// Peer ledger tracking for catch-up detection
	peersMu    sync.RWMutex
	peerStates map[peermanagement.PeerID]*peerLedgerState

	// Active inbound ledger acquisition (nil when not acquiring).
	// Mutually exclusive with inboundReplayDelta — only one acquisition
	// strategy runs at a time per Router (single-goroutine handleMessage
	// keeps that invariant trivially).
	inboundLedger *inbound.Ledger

	// Active inbound replay-delta acquisition (nil when not acquiring).
	// Started by startReplayDeltaAcquisition; cleared on success, on
	// verification failure (which falls back to the legacy path), or on
	// timeout via maintenanceTick.
	inboundReplayDelta *inbound.ReplayDelta
}

// NewRouter creates a new Router.
func NewRouter(engine consensus.Engine, adaptor *Adaptor, modeManager *ModeManager, inbox <-chan *peermanagement.InboundMessage) *Router {
	return &Router{
		engine:      engine,
		adaptor:     adaptor,
		modeManager: modeManager,
		inbox:       inbox,
		logger:      slog.Default().With("component", "consensus-router"),
		peerStates:  make(map[peermanagement.PeerID]*peerLedgerState),
	}
}

// Run reads messages from the overlay and dispatches them.
// It blocks until the context is cancelled. A periodic maintenance tick
// also runs in this loop to time out stuck inbound replay-delta
// acquisitions and fall back to the legacy mtGET_LEDGER path.
func (r *Router) Run(ctx context.Context) {
	ticker := time.NewTicker(inboundReplayDeltaTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-r.inbox:
			if !ok {
				return
			}
			r.handleMessage(msg)
		case <-ticker.C:
			r.maintenanceTick()
		}
	}
}

// maintenanceTick runs out-of-band housekeeping: detect a replay-delta
// acquisition that has outlived its timeout, abandon it, and re-issue
// using the legacy header+state path. Sharing the message-loop goroutine
// keeps the single-writer invariant on inboundReplayDelta intact.
func (r *Router) maintenanceTick() {
	if r.inboundReplayDelta == nil {
		return
	}
	if !r.inboundReplayDelta.IsTimedOut() {
		return
	}
	seq := r.inboundReplayDelta.Seq()
	hash := r.inboundReplayDelta.Hash()
	peerID := r.inboundReplayDelta.PeerID()
	r.logger.Warn("replay delta acquisition timed out, falling back to legacy",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
	)
	r.inboundReplayDelta = nil
	r.startLedgerAcquisitionLegacy(seq, hash, peerID)
}

func (r *Router) handleMessage(msg *peermanagement.InboundMessage) {
	msgType := message.MessageType(msg.Type)

	switch msgType {
	case message.TypeProposeLedger:
		r.handleProposal(msg)
	case message.TypeValidation:
		r.handleValidation(msg)
	case message.TypeTransaction:
		r.handleTransaction(msg)
	case message.TypeHaveSet:
		r.handleHaveSet(msg)
	case message.TypeStatusChange:
		r.handleStatusChange(msg)
	case message.TypeGetLedger:
		r.handleGetLedger(msg)
	case message.TypeLedgerData:
		r.handleLedgerData(msg)
	case message.TypeReplayDeltaResponse:
		r.handleReplayDeltaResponse(msg)
	default:
		// Not a consensus message — ignore
	}
}

func (r *Router) handleProposal(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeProposeLedger, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode proposal", "error", err, "peer", msg.PeerID)
		return
	}
	proposeSet, ok := decoded.(*message.ProposeSet)
	if !ok {
		return
	}

	proposal := ProposalFromMessage(proposeSet)
	if err := r.engine.OnProposal(proposal); err != nil {
		r.logger.Debug("engine rejected proposal", "error", err, "peer", msg.PeerID)
	}
}

func (r *Router) handleValidation(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeValidation, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode validation", "error", err, "peer", msg.PeerID)
		return
	}
	val, ok := decoded.(*message.Validation)
	if !ok {
		return
	}

	validation, err := ValidationFromMessage(val)
	if err != nil {
		r.logger.Warn("failed to parse validation", "error", err, "peer", msg.PeerID)
		return
	}
	if err := r.engine.OnValidation(validation); err != nil {
		r.logger.Debug("engine rejected validation", "error", err, "peer", msg.PeerID)
	}
}

func (r *Router) handleTransaction(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeTransaction, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode transaction", "error", err, "peer", msg.PeerID)
		return
	}
	txMsg, ok := decoded.(*message.Transaction)
	if !ok {
		return
	}

	blob := TransactionFromMessage(txMsg)
	if len(blob) == 0 {
		return
	}

	// Add to the adaptor's pending transaction pool
	r.adaptor.AddPendingTx(blob)
}

func (r *Router) handleHaveSet(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeHaveSet, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode have_set", "error", err, "peer", msg.PeerID)
		return
	}
	hts, ok := decoded.(*message.HaveTransactionSet)
	if !ok {
		return
	}

	txSetID, status := HaveSetFromMessage(hts)

	switch status {
	case message.TxSetStatusHave:
		// Peer has a tx set we might need — if the engine is waiting for it,
		// we could request the full set. For now, just log.
		r.logger.Debug("peer has txset", "txset", txSetID, "peer", msg.PeerID)
	case message.TxSetStatusNeed:
		// Peer needs a tx set we might have — check cache and respond.
		if ts, ok := r.adaptor.txSetCache.Get(txSetID); ok {
			// We have it — notify the engine with the tx set data
			if err := r.engine.OnTxSet(ts.ID(), ts.Txs()); err != nil {
				r.logger.Debug("engine rejected txset", "error", err)
			}
		}
	}
}

func (r *Router) handleGetLedger(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeGetLedger, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode get_ledger", "error", err, "peer", msg.PeerID)
		return
	}
	req, ok := decoded.(*message.GetLedger)
	if !ok {
		return
	}

	r.logger.Debug("peer requests ledger",
		"peer", msg.PeerID,
		"itype", req.InfoType,
		"seq", req.LedgerSeq,
		"hash_len", len(req.LedgerHash),
	)

	// Only handle base (header) requests for now
	if req.InfoType != message.LedgerInfoBase {
		return
	}

	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}

	// Find the requested ledger
	var l *ledger.Ledger
	if len(req.LedgerHash) == 32 {
		var hash [32]byte
		copy(hash[:], req.LedgerHash)
		l, err = svc.GetLedgerByHash(hash)
	} else if req.LedgerSeq > 0 {
		l, err = svc.GetLedgerBySequence(req.LedgerSeq)
	} else {
		l = svc.GetClosedLedger()
	}
	if err != nil || l == nil {
		return
	}

	hash := l.Hash()
	resp := &message.LedgerData{
		LedgerHash: hash[:],
		LedgerSeq:  l.Sequence(),
		InfoType:   message.LedgerInfoBase,
		Nodes: []message.LedgerNode{
			{NodeData: l.SerializeHeader()},
		},
		RequestCookie: uint32(req.RequestCookie),
	}

	frame, err := encodeFrame(message.TypeLedgerData, resp)
	if err != nil {
		r.logger.Warn("failed to encode ledger_data response", "error", err)
		return
	}

	if err := r.adaptor.SendToPeer(uint64(msg.PeerID), frame); err != nil {
		r.logger.Debug("failed to send ledger_data to peer", "error", err, "peer", msg.PeerID)
	}
}

func (r *Router) handleStatusChange(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeStatusChange, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode status_change", "error", err, "peer", msg.PeerID)
		return
	}
	sc, ok := decoded.(*message.StatusChange)
	if !ok {
		return
	}

	r.logger.Info("peer status change",
		"peer", msg.PeerID,
		"status", sc.NewStatus,
		"event", sc.NewEvent,
		"ledger_seq", sc.LedgerSeq,
		"needs_sync", r.adaptor.NeedsInitialSync(),
	)

	// Track peer's reported ledger state
	if sc.LedgerSeq > 0 {
		var peerHash [32]byte
		if len(sc.LedgerHash) == 32 {
			copy(peerHash[:], sc.LedgerHash)
		}
		var parentHash [32]byte
		if len(sc.LedgerHashPrevious) == 32 {
			copy(parentHash[:], sc.LedgerHashPrevious)
		}

		r.peersMu.Lock()
		r.peerStates[msg.PeerID] = &peerLedgerState{
			LedgerSeq:  sc.LedgerSeq,
			LedgerHash: peerHash,
		}
		r.peersMu.Unlock()

		// During initial sync, fetch full ledger from peer (like rippled).
		// Don't adopt with synthetic headers — wait for real state data.
		if r.adaptor.NeedsInitialSync() && sc.LedgerSeq > 1 {
			r.startLedgerAcquisition(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
			return
		}

		// When in Full mode and significantly behind (gap > 2), acquire the
		// latest ledger from the peer but stay in Full mode so we keep
		// participating in consensus.
		if r.adaptor.GetOperatingMode() == consensus.OpModeFull && sc.LedgerSeq > 1 {
			svc := r.adaptor.LedgerService()
			if svc != nil {
				ourSeq := svc.GetClosedLedgerIndex()
				if sc.LedgerSeq > ourSeq+2 {
					r.logger.Warn("behind network while in Full mode, catching up",
						"our_seq", ourSeq,
						"peer_seq", sc.LedgerSeq,
						"gap", sc.LedgerSeq-ourSeq,
					)
					r.startLedgerAcquisition(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
					return
				}
			}
		}

		// While not in Full mode, keep acquiring from peers until
		// we're within 1 ledger of the network.
		if r.adaptor.GetOperatingMode() != consensus.OpModeFull && sc.LedgerSeq > 1 {
			svc := r.adaptor.LedgerService()
			if svc != nil {
				ourSeq := svc.GetClosedLedgerIndex()
				if sc.LedgerSeq > ourSeq+1 {
					r.startLedgerAcquisition(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
					return
				}
			}
		}

		// Check if we're behind and need to catch up
		r.checkBehind(sc.LedgerSeq, peerHash)
	}
}

// startLedgerAcquisition picks the best available ledger-acquisition
// strategy for the given target. When we have the parent ledger locally,
// the bandwidth-efficient replay-delta protocol is preferred (one request
// returns header + every tx blob); otherwise we fall back to the legacy
// mtGET_LEDGER header+state walk. Mirrors rippled's preference for
// LedgerDeltaAcquire over InboundLedger when the parent is available.
func (r *Router) startLedgerAcquisition(seq uint32, hash [32]byte, peerID uint64) {
	// Always defer to an in-flight replay-delta on the same hash to
	// avoid issuing a duplicate request.
	if r.inboundReplayDelta != nil && r.inboundReplayDelta.Hash() == hash {
		return
	}

	parent := r.adaptor.GetParentLedgerForReplay(seq)
	if parent != nil {
		if err := r.startReplayDeltaAcquisition(seq, hash, peerID, parent); err == nil {
			return
		}
		// Fall through to the legacy path on issue failure.
	}
	r.startLedgerAcquisitionLegacy(seq, hash, peerID)
}

// startReplayDeltaAcquisition issues a single mtREPLAY_DELTA_REQUEST and
// arms the InboundReplayDelta state machine to verify the matching
// response. Mirrors rippled's LedgerDeltaAcquire::trigger.
func (r *Router) startReplayDeltaAcquisition(seq uint32, hash [32]byte, peerID uint64, parent *ledger.Ledger) error {
	if r.inboundReplayDelta != nil {
		return errors.New("replay delta acquisition already in progress")
	}
	r.logger.Info("starting replay delta acquisition",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
	)
	r.inboundReplayDelta = inbound.NewReplayDelta(hash, peerID, parent, r.logger)
	if err := r.adaptor.RequestReplayDelta(peerID, hash); err != nil {
		r.logger.Warn("failed to request replay delta from peer", "error", err)
		r.inboundReplayDelta = nil
		return err
	}
	return nil
}

// startLedgerAcquisitionLegacy requests the full ledger (header + state
// tree) from a peer using the legacy mtGET_LEDGER protocol. This is the
// fallback path when the parent isn't locally available or replay-delta
// verification fails.
func (r *Router) startLedgerAcquisitionLegacy(seq uint32, hash [32]byte, peerID uint64) {
	// If already acquiring this exact hash, skip
	if r.inboundLedger != nil {
		if r.inboundLedger.Hash() == hash {
			return
		}
		// Acquiring a different (older) hash — abandon it for the newer one
		if r.inboundLedger.IsTimedOut() {
			r.logger.Info("inbound ledger: timed out, retrying with new peer",
				"old_seq", r.inboundLedger.Seq(),
				"new_seq", seq,
			)
		}
		r.inboundLedger = nil
	}

	r.logger.Info("starting ledger acquisition (legacy)",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
	)

	r.inboundLedger = inbound.New(hash, seq, peerID, r.logger)
	if err := r.adaptor.RequestLedgerBaseFromPeer(peerID, hash, seq); err != nil {
		r.logger.Warn("failed to request ledger base from peer", "error", err)
		r.inboundLedger = nil
	}
}

// handleReplayDeltaResponse verifies an inbound mtREPLAY_DELTA_RESPONSE
// against the active acquisition and adopts the resulting ledger. On
// verification failure the active acquisition is abandoned and the
// legacy path is started for the same target.
func (r *Router) handleReplayDeltaResponse(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeReplayDeltaResponse, msg.Payload)
	if err != nil {
		r.logger.Debug("failed to decode replay delta response", "error", err, "peer", msg.PeerID)
		return
	}
	resp, ok := decoded.(*message.ReplayDeltaResponse)
	if !ok || resp == nil {
		return
	}

	if r.inboundReplayDelta == nil {
		r.logger.Debug("replay delta response with no active acquisition", "peer", msg.PeerID)
		return
	}

	// Reject responses that don't match our active hash — they belong to
	// a different (or stale) acquisition.
	expected := r.inboundReplayDelta.Hash()
	if !bytes.Equal(resp.LedgerHash, expected[:]) {
		r.logger.Debug("replay delta response for non-active hash; dropping",
			"peer", msg.PeerID,
			"got", fmt.Sprintf("%x", resp.LedgerHash),
			"want", fmt.Sprintf("%x", expected[:8]),
		)
		return
	}

	if err := r.inboundReplayDelta.GotResponse(resp); err != nil {
		seq := r.inboundReplayDelta.Seq()
		hash := r.inboundReplayDelta.Hash()
		peerID := r.inboundReplayDelta.PeerID()
		r.inboundReplayDelta = nil
		r.logger.Warn("replay delta verification failed; falling back to legacy",
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"peer", peerID,
			"error", err,
		)
		r.startLedgerAcquisitionLegacy(seq, hash, peerID)
		return
	}

	// GotResponse verified the header hash and the tx-map root. Apply
	// re-derives the post-state by replaying every tx through the
	// engine against a mutable copy of the parent's state, then
	// verifies the resulting AccountHash matches the target header —
	// the only proof we have that our engine doesn't diverge from
	// rippled. Without this step the adopted ledger would carry the
	// parent's stale state map, breaking consensus on the next round.
	parent := r.inboundReplayDelta.Parent()
	engineCfg := r.adaptor.EngineConfigForReplay(parent)
	derived, err := r.inboundReplayDelta.Apply(engineCfg)
	if err != nil {
		seq := r.inboundReplayDelta.Seq()
		hash := r.inboundReplayDelta.Hash()
		peerID := r.inboundReplayDelta.PeerID()
		r.inboundReplayDelta = nil
		r.logger.Warn("replay delta apply failed; falling back to legacy",
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"peer", peerID,
			"error", err,
		)
		r.startLedgerAcquisitionLegacy(seq, hash, peerID)
		return
	}
	r.inboundReplayDelta = nil
	if err := r.adoptVerifiedLedger(derived); err != nil {
		r.logger.Warn("failed to adopt replay-delta ledger", "error", err)
	}
}

// adoptVerifiedLedger commits a ledger reconstructed from a verified
// replay delta. Mirrors completeInboundLedger's adoption logic: install
// state and tx maps via the ledger service, advance to Tracking if
// we're below it, and log the new tip.
func (r *Router) adoptVerifiedLedger(l *ledger.Ledger) error {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return errors.New("no ledger service")
	}
	hdr := l.Header()
	stateMap, err := l.StateMapSnapshot()
	if err != nil {
		return fmt.Errorf("snapshot state map: %w", err)
	}
	if err := svc.AdoptLedgerWithState(&hdr, stateMap); err != nil {
		return fmt.Errorf("adopt with state: %w", err)
	}
	if r.adaptor.GetOperatingMode() < consensus.OpModeTracking {
		r.adaptor.SetOperatingMode(consensus.OpModeTracking)
	}
	r.logger.Info("adopted ledger via replay delta",
		"seq", hdr.LedgerIndex,
		"hash", fmt.Sprintf("%x", hdr.Hash[:8]),
	)
	return nil
}

// checkBehind compares the peer's ledger seq to ours and handles catch-up.
// When we're within 1 ledger of the peer and not yet in Full mode,
// transitions to Full to start consensus.
func (r *Router) checkBehind(peerSeq uint32, peerHash [32]byte) {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}

	ourSeq := svc.GetClosedLedgerIndex()

	// If we're caught up (gap ≤ 1) and not yet Full, transition to Full
	// only if our LCL hash matches what the majority of peers report.
	if peerSeq <= ourSeq+1 {
		if r.adaptor.GetOperatingMode() == consensus.OpModeTracking {
			if r.ourLCLMatchesPeers() {
				r.logger.Info("caught up with network, transitioning to Full",
					"our_seq", ourSeq,
					"peer_seq", peerSeq,
				)
				r.adaptor.SetOperatingMode(consensus.OpModeFull)
			} else {
				r.logger.Info("caught up but LCL hash differs, staying in Tracking",
					"our_seq", ourSeq,
					"peer_seq", peerSeq,
				)
			}
		}
		return
	}

	r.logger.Info("behind network",
		"our_seq", ourSeq,
		"peer_seq", peerSeq,
		"gap", peerSeq-ourSeq,
	)

	// Request the peer's closed ledger by hash and sequence
	if err := r.adaptor.RequestLedgerByHashAndSeq(peerHash, peerSeq); err != nil {
		r.logger.Debug("failed to request ledger", "seq", peerSeq, "error", err)
	}
}

// ourLCLMatchesPeers checks if our closed ledger hash matches what the
// majority of tracked peers report. Returns true if we have no peer data
// (to avoid blocking startup).
func (r *Router) ourLCLMatchesPeers() bool {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return true
	}
	closedLedger := svc.GetClosedLedger()
	if closedLedger == nil {
		return true
	}
	ourHash := closedLedger.Hash()
	ourSeq := svc.GetClosedLedgerIndex()

	r.peersMu.RLock()
	defer r.peersMu.RUnlock()

	if len(r.peerStates) == 0 {
		return true
	}

	matching := 0
	total := 0
	for _, ps := range r.peerStates {
		if ps.LedgerSeq == ourSeq {
			total++
			if ps.LedgerHash == ourHash {
				matching++
			}
		}
	}

	// If no peers at our seq, allow transition (they may have advanced)
	if total == 0 {
		return true
	}

	return matching > total/2
}

func (r *Router) handleLedgerData(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeLedgerData, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode ledger_data", "error", err, "peer", msg.PeerID)
		return
	}
	ld, ok := decoded.(*message.LedgerData)
	if !ok {
		return
	}

	r.logger.Info("received ledger data",
		"peer", msg.PeerID,
		"seq", ld.LedgerSeq,
		"nodes", len(ld.Nodes),
		"itype", ld.InfoType,
		"has_inbound", r.inboundLedger != nil,
	)

	// Feed data to active inbound ledger acquisition
	if r.inboundLedger != nil {
		if r.handleInboundLedgerData(ld) {
			return
		}
		// If handleInboundLedgerData returned false (e.g. GotBase failed),
		// fall through to the legacy header-only adoption path
	}

	// During initial sync, try to adopt the ledger header from peers
	if ld.InfoType == message.LedgerInfoBase && len(ld.Nodes) > 0 && r.adaptor.NeedsInitialSync() {
		headerData := ld.Nodes[0].NodeData
		if err := r.adaptor.AdoptLedgerFromHeader(headerData); err != nil {
			r.logger.Debug("failed to adopt ledger header", "error", err, "peer", msg.PeerID)
		} else {
			r.logger.Info("adopted ledger from peer",
				"seq", ld.LedgerSeq,
				"peer", msg.PeerID,
			)
			return
		}
	}

	// Pass the ledger data to the consensus engine
	if len(ld.LedgerHash) == 32 {
		var ledgerID consensus.LedgerID
		copy(ledgerID[:], ld.LedgerHash)

		var payload []byte
		for _, node := range ld.Nodes {
			payload = append(payload, node.NodeData...)
		}

		if err := r.engine.OnLedger(ledgerID, payload); err != nil {
			r.logger.Debug("engine rejected ledger data", "error", err, "peer", msg.PeerID)
		}
	}
}

// handleInboundLedgerData feeds LedgerData to the active InboundLedger acquisition.
// Returns true if the data was consumed by the acquisition.
func (r *Router) handleInboundLedgerData(ld *message.LedgerData) bool {
	il := r.inboundLedger
	if il == nil {
		return false
	}

	// Verify the response is for our active acquisition
	if len(ld.LedgerHash) == 32 {
		expectedHash := il.Hash()
		var responseHash [32]byte
		copy(responseHash[:], ld.LedgerHash)
		if responseHash != expectedHash {
			return false // Not for us
		}
	}

	switch ld.InfoType {
	case message.LedgerInfoBase:
		// Phase 1: Got header + root nodes
		if len(ld.Nodes) < 2 {
			// Response doesn't include root nodes — can't do full acquisition.
			// Clear inbound and fall through to legacy adoption.
			r.logger.Debug("inbound ledger: response has < 2 nodes, falling back", "nodes", len(ld.Nodes))
			r.inboundLedger = nil
			return false
		}
		if err := il.GotBase(ld.Nodes); err != nil {
			r.logger.Warn("inbound ledger: GotBase failed, falling back", "error", err)
			r.inboundLedger = nil
			return false
		}

		if il.IsComplete() {
			r.completeInboundLedger()
			return true
		}

		// Request missing state nodes
		nodeIDs := il.NeedsMissingNodeIDs()
		if len(nodeIDs) > 0 {
			if err := r.adaptor.RequestStateNodes(il.PeerID(), il.Hash(), nodeIDs); err != nil {
				r.logger.Warn("inbound ledger: failed to request state nodes", "error", err)
			}
		}
		return true

	case message.LedgerInfoAsNode:
		// Phase 2: Got state tree nodes
		if err := il.GotStateNodes(ld.Nodes); err != nil {
			r.logger.Warn("inbound ledger: GotStateNodes failed", "error", err)
			return true
		}

		if il.IsComplete() {
			r.completeInboundLedger()
			return true
		}

		// Request more missing nodes if needed
		nodeIDs := il.NeedsMissingNodeIDs()
		if len(nodeIDs) > 0 {
			if err := r.adaptor.RequestStateNodes(il.PeerID(), il.Hash(), nodeIDs); err != nil {
				r.logger.Warn("inbound ledger: failed to request state nodes", "error", err)
			}
		}
		return true
	}

	return false
}

// completeInboundLedger finalizes an InboundLedger acquisition and adopts the ledger.
func (r *Router) completeInboundLedger() {
	il := r.inboundLedger
	r.inboundLedger = nil

	h, stateMap, err := il.Result()
	if err != nil {
		r.logger.Warn("inbound ledger: failed to get result", "error", err)
		return
	}

	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}

	if err := svc.AdoptLedgerWithState(h, stateMap); err != nil {
		r.logger.Warn("inbound ledger: failed to adopt with state", "error", err)
		return
	}

	// Only upgrade to Tracking if still in a lower mode.
	// Never demote from Full — that would break consensus participation.
	if r.adaptor.GetOperatingMode() < consensus.OpModeTracking {
		r.adaptor.SetOperatingMode(consensus.OpModeTracking)
	}
	r.logger.Info("adopted ledger with full state from peer",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
		"account_hash", fmt.Sprintf("%x", h.AccountHash[:8]),
	)
}

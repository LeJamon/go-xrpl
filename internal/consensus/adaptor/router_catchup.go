package adaptor

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

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

		// Surface the peer's reported LCL to the adaptor so the
		// engine's getNetworkLedger can consider it as a vote even
		// when no proposal has (yet) arrived from this peer.
		r.adaptor.UpdatePeerLCL(uint64(msg.PeerID), consensus.LedgerID(peerHash))

		// During initial sync, fetch the full ledger from the peer.
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

		// Hash-divergence catch-up. A late-join node (or a node whose
		// consensus ran in isolation while disconnected) can end up at
		// the same seq as its peers but with a different ledger hash.
		// The seq-based branches above don't fire because ourSeq ==
		// peerSeq; we need to detect that our LCL hash differs from the
		// peer's and acquire theirs. Only fire if we're NOT already
		// acquiring that hash (startLedgerAcquisition dedupes internally
		// via the replayer / acquisition-registry guards, but checking
		// here saves a lookup in the hot path).
		svc := r.adaptor.LedgerService()
		if svc != nil && sc.LedgerSeq > 1 && len(sc.LedgerHash) == 32 {
			closed := svc.GetClosedLedger()
			if closed != nil {
				ourSeq := closed.Sequence()
				ourHash := closed.Hash()
				if ourSeq == sc.LedgerSeq && ourHash != peerHash {
					r.logger.Warn("ledger hash divergence at same seq, acquiring peer's ledger",
						"seq", sc.LedgerSeq,
						"our_hash", fmt.Sprintf("%x", ourHash[:8]),
						"peer_hash", fmt.Sprintf("%x", peerHash[:8]),
						"peer", msg.PeerID,
					)
					r.startLedgerAcquisition(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
					return
				}
			}
		}

		r.checkBehind(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
	}
}

// startLedgerAcquisition picks the best available ledger-acquisition
// strategy for the given target. When we have the parent ledger locally
// and the peer advertises ledger-replay, the bandwidth-efficient
// replay-delta protocol is preferred (one request returns header + every
// tx blob); otherwise we fall back to the legacy mtGET_LEDGER
// header+state walk.
//
// This is currently the only driver of startReplayDeltaAcquisition: it
// handles a single target ledger per call. The Replayer coordinator
// supports concurrent acquisitions across many hashes, but the policy
// layer that walks a range (e.g., backward from a peer's tip via
// ParentHash) is a follow-up item.
func (r *Router) startLedgerAcquisition(seq uint32, hash [32]byte, peerID uint64) {
	// Unified dedup across BOTH acquisition paths. A prior fix only
	// checked r.replayer.Has(hash); that still allowed the cross-path
	// race where two status changes at the same seq with different
	// hashes armed both a replay-delta AND a legacy acquisition
	// simultaneously, with adoption order then deciding which won. The
	// single-point-of-truth check is a deliberate narrowing: a tighter
	// guarantee that the same hash can't acquire through both paths.
	if r.isAcquiring(hash) {
		return
	}

	parent := r.adaptor.GetParentLedgerForReplay(seq)
	if parent != nil && r.adaptor.PeerSupportsReplay(peerID) {
		if err := r.startReplayDeltaAcquisition(seq, hash, peerID, parent); err == nil {
			return
		}
		// Fall through to the legacy path on issue failure.
	}
	r.startLedgerAcquisitionLegacy(seq, hash, peerID)
}

// isAcquiring reports whether an acquisition — replay-delta or legacy
// — is currently in flight for the given ledger hash. Used as the
// single dedup entry point so a race between a replay-delta and a
// legacy acquisition for the same hash is impossible.
func (r *Router) isAcquiring(hash [32]byte) bool {
	if r.replayer.Has(hash) {
		return true
	}
	if r.fetchTracker.Find(hash) != nil {
		return true
	}
	return false
}

// startReplayDeltaAcquisition registers a new acquisition with the
// Replayer coordinator and issues the corresponding
// mtREPLAY_DELTA_REQUEST.
//
// Returns ErrAcquisitionExists if a request for the same hash is
// already in flight (caller should drop the duplicate), ErrCapacityFull
// if the coordinator is at cap (caller falls back to legacy), or the
// wire-send error if the request itself failed (coordinator slot is
// freed before returning so the caller can retry).
func (r *Router) startReplayDeltaAcquisition(seq uint32, hash [32]byte, peerID uint64, parent *ledger.Ledger) error {
	rd, err := r.replayer.Acquire(hash, peerID, parent)
	if err != nil {
		return err
	}
	_ = rd // retained in replayer; HandleResponse retrieves it on reply.
	r.logger.Info("starting replay delta acquisition",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
	)
	if err := r.adaptor.RequestReplayDelta(peerID, hash); err != nil {
		r.logger.Warn("failed to request replay delta from peer", "error", err)
		r.replayer.Abandon(hash)
		return err
	}
	return nil
}

// startLedgerAcquisitionLegacy requests the full ledger (header + state
// tree) from a peer using the legacy mtGET_LEDGER protocol. This is the
// fallback path when the parent isn't locally available or replay-delta
// verification fails.
//
// Callers that enter via startLedgerAcquisition already consult
// isAcquiring across both paths — but we still re-check here because
// maintenanceTick and the replay-delta fallback paths can enter
// directly, bypassing the unified entry point.
func (r *Router) startLedgerAcquisitionLegacy(seq uint32, hash [32]byte, peerID uint64) {
	// Safety net: if a replay-delta for the same hash is still
	// registered, don't start a legacy on top of it — one path is
	// always enough.
	if r.replayer.Has(hash) {
		return
	}

	_, created := r.fetchTracker.GetOrCreate(hash, func() *inbound.Ledger {
		return inbound.New(hash, seq, peerID, r.logger)
	})
	if !created {
		// Already acquiring this hash (consensus or a prior arm).
		return
	}

	r.logger.Info("starting ledger acquisition (legacy)",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
	)

	if err := r.adaptor.RequestLedgerBaseFromPeer(peerID, hash, seq); err != nil {
		r.logger.Warn("failed to request ledger base from peer", "error", err)
		r.fetchTracker.Remove(hash, false)
	}
}

// FetchInfo returns the inbound-ledger acquisition snapshot served by the
// fetch_info RPC. Safe to call from any goroutine.
func (r *Router) FetchInfo() map[string]any {
	return r.fetchTracker.Info()
}

// ClearFetchInfo resets the acquisition counters and recent-failure history,
// backing fetch_info's `clear` param.
func (r *Router) ClearFetchInfo() {
	r.fetchTracker.Clear()
}

// RequestLedger triggers (or joins) a generic acquisition of a ledger from
// peers, backing the ledger_request RPC. When hash is zero the target is
// resolved from the validated ledger's skip list, and a ReasonGeneric
// acquisition is started (or the in-flight one reused). started=true while
// an acquisition is in flight; (nil,false,false) when the target can't be
// resolved or no peer is available.
//
// reference distinguishes the two acquiring shapes: false when the
// snapshot is the target ledger itself; true when it is a 256-aligned
// reference ledger being fetched only to learn the target's hash.
//
// Safe to call from an RPC goroutine: the registry and each acquisition guard
// their own state.
func (r *Router) RequestLedger(hash [32]byte, seq uint32) (acquiring map[string]any, started, reference bool) {
	// Don't acquire history online-delete has reclaimed: rippled's
	// LedgerMaster::shouldAcquire refuses to fetch a missing ledger below
	// minimumOnline. Re-fetching it would only feed the rotator another
	// delete. Forward catch-up / validation acquisitions are above the
	// validated tip (≥ floor) so they never hit this gate.
	if seq != 0 && r.belowFloor(seq) {
		r.logger.Debug("ledger_request declined: below online-delete floor",
			"seq", seq, "floor", r.floor.MinimumOnline())
		return nil, false, false
	}
	if hash == ([32]byte{}) {
		if seq == 0 {
			return nil, false, false
		}
		svc := r.adaptor.LedgerService()
		if svc == nil {
			return nil, false, false
		}
		vl := svc.GetValidatedLedger()
		if vl == nil {
			return nil, false, false
		}
		h, ok, err := vl.HashOfSeq(seq)
		if err != nil {
			return nil, false, false
		}
		if !ok {
			// seq is past the rolling window and not 256-aligned, so its hash
			// isn't directly in the validated ledger. Resolve it through a
			// 256-aligned reference ledger whose hash IS enshrined in the skip
			// list.
			refIndex := getCandidateLedger(seq)
			refHash, refOK, err := vl.HashOfSeq(refIndex)
			if err != nil || !refOK {
				return nil, false, false
			}
			refLedger, err := svc.GetLedgerByHash(refHash)
			if err != nil || refLedger == nil {
				// We lack the reference ledger needed to learn the target's
				// hash — acquire it and report it as the in-flight reference.
				if snap, ok := r.startGenericAcquisition(refHash, refIndex); ok {
					return snap, true, true
				}
				return nil, false, false
			}
			h, ok, err = refLedger.HashOfSeq(seq)
			if err != nil || !ok {
				return nil, false, false
			}
		}
		hash = h
	}

	if snap, ok := r.startGenericAcquisition(hash, seq); ok {
		return snap, true, false
	}
	return nil, false, false
}

// startGenericAcquisition begins (or joins) a ReasonGeneric acquisition for
// hash, issuing a base fetch from a selected peer only when it creates a fresh
// one. Returns the acquisition snapshot, or ok=false when no peer is available
// or the initial fetch could not be issued. The fetchTracker's GetOrCreate is
// atomic, so a concurrent consensus catch-up arming the same hash is joined
// rather than duplicated.
func (r *Router) startGenericAcquisition(hash [32]byte, seq uint32) (map[string]any, bool) {
	if il := r.fetchTracker.Find(hash); il != nil {
		return inbound.AcquisitionJSON(il.Snapshot()), true
	}

	peerID, ok := r.selectAcquisitionPeer(seq)
	if !ok {
		return nil, false
	}

	il, created := r.fetchTracker.GetOrCreate(hash, func() *inbound.Ledger {
		return inbound.NewGeneric(hash, seq, peerID, r.logger)
	})
	if created {
		r.logger.Info("starting ledger acquisition (generic, ledger_request)",
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"peer", peerID,
		)
		if err := r.adaptor.RequestLedgerBaseFromPeer(peerID, hash, seq); err != nil {
			r.logger.Warn("ledger_request: failed to request ledger base", "error", err)
			r.fetchTracker.Remove(hash, false)
			return nil, false
		}
	}
	return inbound.AcquisitionJSON(il.Snapshot()), true
}

// getCandidateLedger rounds seq up to the next multiple of 256 — the nearest
// ancestor whose hash is enshrined in the historical skip list and is therefore
// easy to resolve, then close enough (within 256) to hold seq's hash in its own
// rolling list.
func getCandidateLedger(seq uint32) uint32 {
	return (seq + 255) &^ 255
}

// selectAcquisitionPeer picks a connected peer to fetch a ledger from,
// preferring one whose reported ledger is at or beyond the target sequence
// (and therefore likely to hold it). When seq is unknown (0) or no peer is far
// enough along, it falls back to any connected peer. Returns (0,false) when no
// peer has reported a ledger state.
func (r *Router) selectAcquisitionPeer(seq uint32) (uint64, bool) {
	r.peersMu.RLock()
	defer r.peersMu.RUnlock()

	var fallback uint64
	var haveFallback bool
	for pid, st := range r.peerStates {
		if !haveFallback {
			fallback, haveFallback = uint64(pid), true
		}
		if seq == 0 || st.LedgerSeq >= seq {
			return uint64(pid), true
		}
	}
	return fallback, haveFallback
}

// handleReplayDeltaResponse verifies an inbound mtREPLAY_DELTA_RESPONSE
// against its matching in-flight acquisition (routed by ledger hash)
// and adopts the resulting ledger. On verification or apply failure the
// acquisition is abandoned and the legacy path is started for the same
// target. Unsolicited/stale responses (no matching acquisition) are
// silently dropped — a normal race when a peer batch-forwards replies
// after we've already moved on.
func (r *Router) handleReplayDeltaResponse(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeReplayDeltaResponse, msg.Payload)
	if err != nil {
		r.logger.Debug("failed to decode replay delta response", "error", err, "peer", msg.PeerID)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "replay-delta-resp-decode")
		return
	}
	resp, ok := decoded.(*message.ReplayDeltaResponse)
	if !ok || resp == nil {
		return
	}

	// A delta acquired by an active LedgerReplayTask is owned by that
	// task and never re-enters the generic InboundLedger flow.
	if r.routeDeltaToActiveTask(resp) {
		return
	}

	rd, err := r.replayer.HandleResponse(resp)
	if errors.Is(err, inbound.ErrNoMatchingAcquisition) {
		// Stale or unsolicited — drop silently without charging the
		// peer. A misbehaving peer sending genuinely bogus data would
		// fail its ACTIVE acquisition's verifier (branch below), which
		// IS attributed via IncPeerBadData.
		r.logger.Debug("replay delta response with no matching acquisition",
			"peer", msg.PeerID)
		return
	}
	if err != nil {
		// Verification failed. rd is still registered in the Replayer so
		// we can read its provenance before abandoning the slot.
		seq := rd.Seq()
		hash := rd.Hash()
		peerID := rd.PeerID()
		r.replayer.Abandon(hash)
		r.logger.Warn("replay delta verification failed; falling back to legacy",
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"peer", peerID,
			"error", err,
		)
		r.adaptor.IncPeerBadData(peerID, "replay-delta-verify")
		r.startLedgerAcquisitionLegacy(seq, hash, peerID)
		return
	}

	// GotResponse verified the header hash and the tx-map root. Apply
	// re-derives the post-state by replaying every tx through the
	// engine against a mutable copy of the parent's state, then
	// verifies the resulting AccountHash matches the target header —
	// the only proof we have that our engine produced the right state.
	// Without this step the adopted ledger would carry the parent's
	// stale state map, breaking consensus on the next round.
	parent := rd.Parent()
	engineCfg := r.adaptor.EngineConfigForReplay(parent)
	derived, err := rd.Apply(engineCfg)
	if err != nil {
		seq := rd.Seq()
		hash := rd.Hash()
		peerID := rd.PeerID()
		r.replayer.Abandon(hash)
		// DO NOT charge the peer here. GotResponse already verified the
		// peer's header hash and tx-map root; a subsequent Apply failure
		// means OUR engine produced a divergent AccountHash — an engine
		// bug, not peer misbehavior. Charging here would wrongly evict
		// honest peers for our bugs.
		r.logger.Error("ENGINE DIVERGENCE: replay delta apply failed; falling back to legacy",
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"peer", peerID,
			"error", err,
		)
		r.startLedgerAcquisitionLegacy(seq, hash, peerID)
		return
	}
	peerID := rd.PeerID()
	r.replayer.Complete(rd.Hash())
	if err := r.adoptVerifiedLedger(derived, peerID); err != nil {
		r.logger.Warn("failed to adopt replay-delta ledger", "error", err)
	}
}

// adoptVerifiedLedger commits a ledger reconstructed from a verified replay
// delta, installing the peer-provided tx-blob tree alongside the state map.
// Routes through SubmitHeldAdoption so out-of-order arrivals are stashed by
// awaited parent seq; on stash we arm a backward-chain acquisition for the
// parent.
func (r *Router) adoptVerifiedLedger(l *ledger.Ledger, peerID uint64) error {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return errors.New("no ledger service")
	}
	hdr := l.Header()
	stateMap, err := l.StateMapSnapshot()
	if err != nil {
		return fmt.Errorf("snapshot state map: %w", err)
	}
	// Pass the verified tx map through so the adopted ledger carries
	// real transactions — without this, tx/tx_history/account_tx RPCs
	// can't answer for replay-delta-adopted ledgers and we can't
	// re-serve the replay-delta to other peers.
	txMap, err := l.TxMapSnapshot()
	if err != nil {
		return fmt.Errorf("snapshot tx map: %w", err)
	}
	// context.TODO: adoptVerifiedLedger is reached from a peer-message
	// handler stack that does not currently carry a context. Threading
	// one through the message-dispatch chain is tracked separately from
	// this issue (#185).
	res, err := svc.SubmitHeldAdoption(context.TODO(), &hdr, stateMap, txMap)
	if err != nil {
		return fmt.Errorf("adopt with state: %w", err)
	}
	if r.adaptor.GetOperatingMode() < consensus.OpModeTracking {
		r.adaptor.SetOperatingMode(consensus.OpModeTracking)
	}
	r.logger.Info("adopted ledger via replay delta",
		"seq", hdr.LedgerIndex,
		"hash", fmt.Sprintf("%x", hdr.Hash[:8]),
	)
	// Notify the consensus engine so it can flip out of
	// ModeWrongLedger via Engine.OnLedger. Without this, the engine
	// remains stuck in wrongLedger indefinitely after a successful
	// inbound acquisition.
	if r.engine != nil {
		if err := r.engine.OnLedger(consensus.LedgerID(hdr.Hash), nil); err != nil {
			r.logger.Debug("engine rejected adopted ledger", "error", err, "seq", hdr.LedgerIndex)
		}
	}
	if res.Stashed {
		r.armParentAcquisition(svc, res.ParentSeq, res.ParentHash, peerID)
	}
	return nil
}

// maybeAcquireFromValidation arms inbound acquisition for a ledger attested
// by a single TRUSTED validation, before the hash reaches quorum. It is the
// non-quorum counterpart to armValidationStashAcquisition, acquiring the
// ledger on EVERY trusted current validation when we don't already have it
// — quorum is not required. With only the quorum-gated path, a node below
// quorum (3 of 4 trusted validators on the network tip) never fetched that
// tip and stalled in the wrongLedger chase loop.
//
// This only ACQUIRES. Advancing validatedLedger still flows through the
// quorum gate (onFullyValidated → SetValidatedLedger), so a sub-quorum
// fetch cannot move our validated tip and carries no state-divergence
// risk; it just makes the ledger locally available so the node can rejoin
// consensus on the network's chain instead of holding no position. The
// deliberately-kept peer-LCL trusted-backing gate and the quorum gate are
// untouched.
func (r *Router) maybeAcquireFromValidation(v *consensus.Validation, originPeer uint64) {
	if v == nil || v.LedgerSeq == 0 {
		return
	}
	// Only trusted validators steer chain selection.
	if !r.adaptor.IsTrusted(v.NodeID) {
		return
	}
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}
	// Gate on the VALIDATED tip, never the closed/built tip — same rationale
	// as armValidationStashAcquisition: a node that ran its closed chain
	// ahead would otherwise skip the acquire and stay stuck on the wrong
	// chain.
	if v.LedgerSeq <= svc.GetValidatedLedgerIndex() {
		return
	}
	hash := [32]byte(v.LedgerID)
	// Already have it (built or adopted) — nothing to fetch.
	if l, err := svc.GetLedgerByHash(hash); err == nil && l != nil {
		return
	}
	// startLedgerAcquisition dedupes via isAcquiring, but checking here keeps
	// the hot path quiet when many validations name the same hash.
	if r.isAcquiring(hash) {
		return
	}
	r.logger.Info("acquiring ledger from trusted validation",
		"t", "consensus",
		"event", "validation-acquire",
		"seq", v.LedgerSeq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", originPeer,
	)
	r.startLedgerAcquisition(v.LedgerSeq, hash, originPeer)
}

// armValidationStashAcquisition arms inbound acquisition for a (seq, hash)
// that SetValidatedLedger stashed. Prefers a peer advertising LCL >= seq,
// falls back to any tracked peer.
func (r *Router) armValidationStashAcquisition(seq uint32, hash [32]byte) {
	defer func() {
		if rv := recover(); rv != nil {
			r.logger.Error("armValidationStashAcquisition panic recovered",
				"seq", seq,
				"hash", fmt.Sprintf("%x", hash[:8]),
				"panic", rv,
			)
		}
	}()
	if seq == 0 {
		return
	}
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}
	// Skip only when seq is at or below the last *validated* ledger.
	// Gating on the closed-ledger index instead silently swallowed
	// recovery for a node that had run ahead on a private chain: when the
	// validation tracker observed quorum on canonical seq=N with a
	// different hash than our local seq=N, the acquire was skipped because
	// closedSeq >> validatedSeq, leaving us stuck on the private chain
	// forever.
	if seq <= svc.GetValidatedLedgerIndex() {
		return
	}

	// Walk peers in ID order so the chosen peer (and the emitted log)
	// is reproducible across runs. Any peer with the hash can serve it.
	r.peersMu.RLock()
	peerIDs := make([]peermanagement.PeerID, 0, len(r.peerStates))
	for pid := range r.peerStates {
		peerIDs = append(peerIDs, pid)
	}
	slices.Sort(peerIDs)
	var (
		preferredPeerID uint64
		fallbackPeerID  uint64
	)
	for _, pid := range peerIDs {
		st := r.peerStates[pid]
		if fallbackPeerID == 0 {
			fallbackPeerID = uint64(pid)
		}
		if st != nil && st.LedgerSeq >= seq {
			preferredPeerID = uint64(pid)
			break
		}
	}
	r.peersMu.RUnlock()
	if preferredPeerID == 0 {
		preferredPeerID = fallbackPeerID
	}
	if preferredPeerID == 0 {
		return
	}

	r.logger.Info("arming acquisition for stashed validation",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"preferred_peer", preferredPeerID,
	)
	r.startLedgerAcquisition(seq, hash, preferredPeerID)
}

// armParentAcquisition fires a backward-chain acquisition for the parent of
// a stashed held-adoption candidate. Skips at-or-below closed (already
// adopted or fork-dropped).
func (r *Router) armParentAcquisition(svc *service.Service, parentSeq uint32, parentHash [32]byte, preferredPeerID uint64) {
	if parentSeq == 0 {
		return
	}
	if parentSeq <= svc.GetClosedLedgerIndex() {
		return
	}
	r.logger.Info("arming backward-chain acquisition for stashed held-adoption parent",
		"parent_seq", parentSeq,
		"parent_hash", fmt.Sprintf("%x", parentHash[:8]),
		"preferred_peer", preferredPeerID,
	)
	r.startLedgerAcquisition(parentSeq, parentHash, preferredPeerID)
}

// checkBehind decides what to do based on how far behind a peer
// reports. Two outcomes:
//
//   - peerSeq <= ourSeq+1: we're caught up. If still in Tracking and
//     our LCL hash matches peers' majority, transition to Full.
//     Otherwise stay in Tracking — the hash-mismatch branch in
//     handleStatusChange will have already fired the right acquisition.
//   - peerSeq > ourSeq+1: we're behind by more than one ledger. Arm a
//     single acquisition for the peer's tip. Subsequent status changes
//     from peers will chain more acquisitions forward as we adopt each
//     ledger and ourSeq advances.
//
// Only one acquisition fires per call. A faster "range walk" that
// issues concurrent requests for every seq between ourLCL+1 and
// peerSeq would need the intermediate ledger hashes, which we don't
// know until each acquired header reveals its ParentHash; we rely on
// forward status gossip instead. Replayer already supports concurrent
// in-flight acquisitions, so switching to backward-walk later is a
// localized change in this function.
func (r *Router) checkBehind(peerSeq uint32, peerHash [32]byte, peerID uint64) {
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

	r.logger.Info("behind network, acquiring peer tip",
		"our_seq", ourSeq,
		"peer_seq", peerSeq,
		"gap", peerSeq-ourSeq,
		"peer", peerID,
	)

	// Arm a real acquisition instead of broadcasting a bare
	// mtGET_LEDGER. RequestLedgerByHashAndSeq would broadcast the
	// request but never arm the InboundLedger state machine, so any
	// response would arrive with no active consumer and be dropped.
	// startLedgerAcquisition picks replay-delta or legacy per the
	// routing policy and both paths install their own state machines.
	r.startLedgerAcquisition(peerSeq, peerHash, peerID)
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
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "ledger-data-decode")
		return
	}
	ld, ok := decoded.(*message.LedgerData)
	if !ok {
		return
	}

	var il *inbound.Ledger
	if len(ld.LedgerHash) == 32 {
		var h [32]byte
		copy(h[:], ld.LedgerHash)
		il = r.fetchTracker.Find(h)
	}

	r.logger.Info("received ledger data",
		"peer", msg.PeerID,
		"seq", ld.LedgerSeq,
		"nodes", len(ld.Nodes),
		"itype", ld.InfoType,
		"has_inbound", il != nil,
	)

	// liTS_CANDIDATE response — feeds the engine via the tx-set path
	// (consensus-time only).
	if ld.InfoType == message.LedgerInfoTsCandidate {
		r.handleTxSetData(ld, uint64(msg.PeerID))
		return
	}

	if il != nil {
		if r.handleInboundLedgerData(il, ld) {
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

// handleInboundLedgerData feeds LedgerData to the given InboundLedger
// acquisition (already matched by hash in handleLedgerData). Returns true if
// the data was consumed by the acquisition.
func (r *Router) handleInboundLedgerData(il *inbound.Ledger, ld *message.LedgerData) bool {
	if il == nil {
		return false
	}

	switch ld.InfoType {
	case message.LedgerInfoBase:
		if len(ld.Nodes) < 2 {
			// Response doesn't include root nodes — can't do full acquisition.
			// Drop the acquisition and fall through to legacy adoption.
			r.logger.Debug("inbound ledger: response has < 2 nodes, falling back", "nodes", len(ld.Nodes))
			r.fetchTracker.Remove(il.Hash(), false)
			return false
		}
		if err := il.GotBase(ld.Nodes); err != nil {
			r.logger.Warn("inbound ledger: GotBase failed, falling back", "error", err)
			r.adaptor.IncPeerBadData(il.PeerID(), "ledger-data-base")
			r.fetchTracker.Remove(il.Hash(), false)
			return false
		}

		if il.IsComplete() {
			r.completeInboundLedger(il)
			return true
		}

		// Request the missing state and transaction nodes in parallel.
		r.requestMissingAcquisitionNodes(il)
		return true

	case message.LedgerInfoAsNode:
		if err := il.GotStateNodes(ld.Nodes); err != nil {
			r.logger.Warn("inbound ledger: GotStateNodes failed", "error", err)
			r.adaptor.IncPeerBadData(il.PeerID(), "ledger-data-state")
			return true
		}

		if il.IsComplete() {
			r.completeInboundLedger(il)
			return true
		}

		r.requestMissingAcquisitionNodes(il)
		return true

	case message.LedgerInfoTxNode:
		if err := il.GotTransactionNodes(ld.Nodes); err != nil {
			r.logger.Warn("inbound ledger: GotTransactionNodes failed", "error", err)
			r.adaptor.IncPeerBadData(il.PeerID(), "ledger-data-tx")
			return true
		}

		if il.IsComplete() {
			r.completeInboundLedger(il)
			return true
		}

		r.requestMissingAcquisitionNodes(il)
		return true
	}

	return false
}

// requestMissingAcquisitionNodes asks the source peer for the outstanding
// account-state and transaction tree nodes of the active acquisition,
// requesting both trees in parallel. Each call is a no-op for a tree
// already complete.
func (r *Router) requestMissingAcquisitionNodes(il *inbound.Ledger) {
	if nodeIDs := il.NeedsMissingNodeIDs(); len(nodeIDs) > 0 {
		if err := r.adaptor.RequestStateNodes(il.PeerID(), il.Hash(), nodeIDs); err != nil {
			r.logger.Warn("inbound ledger: failed to request state nodes", "error", err)
		}
	}
	if nodeIDs := il.NeedsMissingTxNodeIDs(); len(nodeIDs) > 0 {
		if err := r.adaptor.RequestTransactionNodes(il.PeerID(), il.Hash(), nodeIDs); err != nil {
			r.logger.Warn("inbound ledger: failed to request tx nodes", "error", err)
		}
	}
}

// completeInboundLedger finalizes an InboundLedger acquisition and adopts the
// ledger. A ReasonGeneric acquisition (RPC-driven, ledger_request) is persisted
// for querying but does not flip operating mode or notify consensus, so an
// arbitrary historical fetch can't disturb the active chain.
func (r *Router) completeInboundLedger(il *inbound.Ledger) {
	r.fetchTracker.Remove(il.Hash(), true)

	h, stateMap, txMap, err := il.Result()
	if err != nil {
		r.logger.Warn("inbound ledger: failed to get result", "error", err)
		return
	}
	peerID := il.PeerID()

	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}

	// The acquisition fetches the header, state map, and transaction map; txMap
	// is nil only when the ledger has no transactions (empty tx tree), in which
	// case the service installs the genesis-shaped empty tx map.
	//
	// Route through SubmitHeldAdoption so out-of-order catchup arrivals
	// either fast-path (parent already present) or stash for cascade when the
	// awaited parent lands. Legacy mtGET_LEDGER is sequential at the wire level
	// today, but nothing in the protocol forbids interleaving — the held-queue
	// is the correct seam regardless.
	// context.TODO: same as adoptVerifiedLedger — reached from a peer-message
	// handler stack with no plumbed context. See note there.
	res, err := svc.SubmitHeldAdoption(context.TODO(), h, stateMap, txMap)
	if err != nil {
		r.logger.Warn("inbound ledger: failed to adopt with state", "error", err)
		return
	}

	// A generic (RPC-driven) acquisition is now persisted and queryable; it
	// must not advance operating mode or feed consensus.
	if il.Reason() == inbound.ReasonGeneric {
		r.logger.Info("acquired ledger (generic) with full state from peer",
			"seq", h.LedgerIndex,
			"hash", fmt.Sprintf("%x", h.Hash[:8]),
		)
		if res.Stashed {
			r.armParentAcquisition(svc, res.ParentSeq, res.ParentHash, peerID)
		}
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
	// Notify the consensus engine so it can flip out of
	// ModeWrongLedger via Engine.OnLedger, mirroring the replay-delta
	// path in adoptVerifiedLedger.
	if r.engine != nil {
		if err := r.engine.OnLedger(consensus.LedgerID(h.Hash), nil); err != nil {
			r.logger.Debug("engine rejected adopted ledger", "error", err, "seq", h.LedgerIndex)
		}
	}
	if res.Stashed {
		r.armParentAcquisition(svc, res.ParentSeq, res.ParentHash, peerID)
	}
}

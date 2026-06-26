package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/skiplist"
	"github.com/LeJamon/go-xrpl/shamap"
)

// AcceptLedger closes the current open ledger and creates a new one.
// This is the main mechanism for advancing ledgers in standalone mode.
// It corresponds to the "ledger_accept" RPC command.
//
// When pending transactions exist, they are sorted using CanonicalTXSet ordering
// and re-applied from a fresh copy of the LCL, matching rippled's behavior.
// Reference: rippled NetworkOPs::acceptLedgerTransaction / CanonicalTXSet
func (s *Service) AcceptLedger(ctx context.Context) (uint32, error) {
	return s.AcceptLedgerAt(ctx, time.Time{})
}

// AcceptLedgerAt is AcceptLedger with an explicit close_time. A zero
// time.Time falls back to time.Now(). Differential / replay tests use
// an explicit value to keep close_time byte-identical between
// implementations.
func (s *Service) AcceptLedgerAt(ctx context.Context, explicitCloseTime time.Time) (uint32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.config.Standalone {
		return 0, ErrNotStandalone
	}

	if s.openLedger == nil {
		return 0, ErrNoOpenLedger
	}

	if s.closedLedger == nil {
		return 0, ErrNoClosedLedger
	}

	closeTime := explicitCloseTime
	if closeTime.IsZero() {
		closeTime = time.Now()
	}

	// If there are pending transactions, re-apply them in canonical order
	// on a fresh ledger built from the LCL. This matches rippled's behavior
	// where open ledger transactions are re-ordered via CanonicalTXSet.
	var retriableTxs []openledger.PendingTx
	if len(s.pendingTxs) > 0 {
		built, err := s.buildClosedLedgerLocked(s.pendingTxs, closeTime, s.config.Standalone)
		if err != nil {
			return 0, err
		}
		retriableTxs = built
	}

	// Reset pending transactions
	s.pendingTxs = nil

	// Close the current open ledger
	if err := s.openLedger.Close(closeTime, 0); err != nil {
		return 0, fmt.Errorf("failed to close ledger: %w", err)
	}

	// In standalone mode, immediately validate
	if err := s.openLedger.SetValidated(); err != nil {
		return 0, fmt.Errorf("failed to validate ledger: %w", err)
	}

	// Persist the closed ledger to storage backends (nodestore and/or relational DB).
	// persistLedger has internal nil guards for each backend.
	//
	// Match rippled: LedgerMaster::setFullLedger -> pendSaveValidated
	// discards the bool return and the chain advance proceeds regardless
	// (rippled/src/xrpld/app/ledger/detail/LedgerMaster.cpp:831,972).
	// Treating SQL persistence failure as fatal here would diverge from
	// rippled and risk forks on transient relational-DB issues.
	if err := s.persistLedger(ctx, s.openLedger); err != nil {
		s.logger.Error("failed to persist closed ledger; chain advance continues",
			"seq", s.openLedger.Sequence(), "err", err)
	}

	// Store the closed ledger in memory cache
	closedSeq := s.openLedger.Sequence()
	closedLedgerHash := s.openLedger.Hash()
	s.closedLedger = s.openLedger
	s.validatedLedger = s.openLedger
	s.putHistoryLocked(s.openLedger)
	s.evictOldHistoryLocked(closedSeq)

	// Standalone validates immediately; fold the validated ledger into the
	// amendment table (enabled set + majority projection + block detection).
	s.syncAmendmentTable(s.validatedLedger)

	// Standalone already promotes to validated above, so any stashed
	// validation at this seq is redundant — but drain it so the entry
	// doesn't linger and accidentally match a later re-close at the
	// same seq. No-op when nothing is stashed.
	s.drainPendingLedgerValidationLocked(closedSeq, s.closedLedger)

	// Collect transaction results for event callbacks/hooks
	var txResults []TransactionResultEvent
	if s.eventCallback != nil || (s.hooks != nil && (s.hooks.OnLedgerClosed != nil || s.hooks.OnTransaction != nil)) {
		txResults = s.collectTransactionResults(s.closedLedger, closedSeq, closedLedgerHash)
	}

	ledgerInfo, validatedLedgers, err := s.advanceToNewOpenLedgerLocked(closedSeq, closedLedgerHash, retriableTxs)
	if err != nil {
		return 0, err
	}

	// Fire structured event hooks for the newly-closed ledger. In the
	// standalone path the ledger is already validated (line above sets
	// s.validatedLedger), so the legacy eventCallback fires immediately
	// rather than being stashed for SetValidatedLedger to drain.
	s.fireLedgerClosedHooksLocked(ledgerInfo, txResults, closeTime, validatedLedgers)

	// Fire legacy event callback for backward compatibility
	if s.eventCallback != nil {
		event := &LedgerAcceptedEvent{
			LedgerInfo:         ledgerInfo,
			TransactionResults: txResults,
		}

		// Call callback in a goroutine to not block ledger operations
		callback := s.eventCallback
		go callback(event)
	}

	s.logger.Info("Ledger accepted",
		"sequence", closedSeq,
		"hash", fmt.Sprintf("%x", closedLedgerHash[:8]),
		"txs", len(txResults),
	)

	return closedSeq, nil
}

// buildClosedLedgerLocked canonically sorts pending, re-applies it onto a
// fresh ledger built from s.closedLedger, hoists every committed tx into
// s.txIndex, and installs the result as s.openLedger. It returns the txs
// left in retry state after the build passes. Shared by the standalone
// (AcceptLedgerAt) and consensus (AcceptConsensusResult) close paths, which
// differ only in their pending source and whether signature verification is
// skipped. Caller must hold s.mu.
func (s *Service) buildClosedLedgerLocked(pending []pendingTx, closeTime time.Time, skipSigVerify bool) ([]openledger.PendingTx, error) {
	// Salt = SHAMap root of the tx set, matching rippled's consensus-build
	// convention; the local pending pool plays the same role in standalone.
	canonicalSort(pending, computeSalt(pending))

	freshLedger, err := ledger.NewOpen(s.closedLedger, closeTime)
	if err != nil {
		return nil, fmt.Errorf("failed to create fresh ledger for close: %w", err)
	}

	baseFee, reserveBase, reserveIncrement := readFeesFromLedger(s.closedLedger)
	applyCfg := openledger.ApplyConfig{
		BaseFee:                   baseFee,
		ReserveBase:               reserveBase,
		ReserveIncrement:          reserveIncrement,
		LedgerSequence:            freshLedger.Sequence(),
		NetworkID:                 s.config.NetworkID,
		ParentCloseTime:           parentCloseTimeRippleEpoch(s.closedLedger),
		Logger:                    s.config.Logger,
		SkipSignatureVerification: skipSigVerify,
		// BuildLedger semantics: tec under certainRetry holds for retry and
		// commits on the final non-retry pass.
		Mode: openledger.BuildLedgerMode,
		// Pull amendments from the parent ledger so amendment-gated behaviour
		// (SLE threading and others) matches the on-chain rule set rather than
		// the all-amendments-on default.
		Rules: rulesFromLedger(s.closedLedger, s.logger),
	}

	var retriableTxs []openledger.PendingTx
	if err := openledger.ApplyTxs(freshLedger, pending, &retriableTxs, applyCfg); err != nil {
		return nil, fmt.Errorf("openledger.ApplyTxs: %w", err)
	}

	// Track every committed tx (tesSUCCESS and tec) by the ledger seq.
	_ = freshLedger.ForEachTransaction(func(txHash [32]byte, _ []byte) bool {
		s.txIndex[txHash] = freshLedger.Sequence()
		return true
	})

	s.openLedger = freshLedger
	return retriableTxs, nil
}

// advanceToNewOpenLedgerLocked opens a fresh ledger on top of the just-closed
// s.closedLedger, refreshes the open-ledger fee metrics, and rebuilds the
// persistent open-ledger view — replaying any retriable txs onto it. It
// returns the closed ledger's info and the validated-range string used for
// hook dispatch. Shared tail of the standalone and consensus close paths.
// Caller must hold s.mu.
func (s *Service) advanceToNewOpenLedgerLocked(closedSeq uint32, closedLedgerHash [32]byte, retriableTxs []openledger.PendingTx) (*LedgerInfo, string, error) {
	newOpen, err := ledger.NewOpen(s.closedLedger, time.Now())
	if err != nil {
		return nil, "", fmt.Errorf("failed to create new open ledger: %w", err)
	}
	s.openLedger = newOpen

	// Refresh fee metrics from the just-closed ledger so the next Accept's
	// modifier sees the right open-ledger fee level.
	s.processClosedLedgerLocked()

	// LCL transition: replay the prior view's txs onto the new open ledger via
	// Accept. retriesFirst is driven by retriableTxs — txs the build pass left
	// in retry state are precisely the ones that need a retries-first replay
	// against the new open view. This is a superset of rippled's disputed set;
	// cleanly-applied txs that get redundantly replayed are harmless (Accept's
	// parent-skip guard short-circuits).
	s.acceptOpenLedgerViewLocked(closedSeq, retriableTxs, len(retriableTxs) > 0)

	ledgerInfo := &LedgerInfo{
		Sequence:   closedSeq,
		Hash:       closedLedgerHash,
		ParentHash: s.closedLedger.ParentHash(),
		CloseTime:  s.closedLedger.CloseTime(),
		TotalDrops: s.closedLedger.TotalDrops(),
		Validated:  s.closedLedger.IsValidated(),
		Closed:     s.closedLedger.IsClosed(),
	}
	return ledgerInfo, s.getValidatedLedgersRange(), nil
}

// installAdoptedLedgerLocked writes adopted into ledgerHistory[seq] under
// the validated-precedence rule — mirrors LedgerHistory::insert(ledger,
// validated) at LedgerHistory.cpp:55-74. Returns the canonical entry;
// callers must use the return as s.closedLedger to keep history and
// closed-reference consistent. Holds s.mu write.
func (s *Service) installAdoptedLedgerLocked(seq uint32, adopted *ledger.Ledger) *ledger.Ledger {
	if existing, ok := s.ledgerHistory[seq]; ok {
		existingHash := existing.Hash()
		newHash := adopted.Hash()
		if existingHash != newHash && existing.IsValidated() && !adopted.IsValidated() {
			s.logger.Warn("adopt skip: validated entry already present",
				"seq", seq,
				"existing_hash", fmt.Sprintf("%x", existingHash[:8]),
				"adopt_hash", fmt.Sprintf("%x", newHash[:8]),
			)
			return existing
		}
	}
	s.putHistoryLocked(adopted)
	return adopted
}

// fixMismatchLocked invalidates the tail of ledgerHistory when the
// adopted ledger does not chain to whatever we already have at
// `adopted.Sequence()-1`. Mirrors rippled's setFullLedger parent-hash
// sanity check + fixMismatch() call (LedgerMaster.cpp:749-801, 849-862).
//
// Trigger: prev := ledgerHistory[adoptedSeq-1] exists AND
// prev.Hash() != adopted.ParentHash(). When that happens:
//
//  1. Delete the prev-seq slot (wrong fork at adoptedSeq-1).
//  2. Delete every seq > adoptedSeq — those entries chained to the
//     now-discarded prev or to a sibling of `adopted`, and so their
//     parent lineage no longer resolves.
//  3. Purge s.txIndex / s.txPositionIndex entries for the removed
//     ledgers — otherwise `tx` / `transaction_entry` RPCs keep
//     resolving to a seq whose contents were discarded.
//  4. Clear s.closedLedger if it was pointing at an invalidated slot.
//     AdoptLedgerWithState reassigns closedLedger to `adopted` right
//     after this returns, so the clear is a defense-in-depth belt.
//  5. If the invalidated prev-seq entry was marked validated, log ERROR
//     — silently resetting a validated ledger would mask a serious
//     fork. We do NOT reset s.validatedLedger silently; operator
//     attention is required.
//
// Caller must hold s.mu (write lock). Called from AdoptLedgerWithState
// before the new entry is written. No-op on the happy path (parent
// chain matches or no prev entry exists), so the hot path is a single
// map lookup + hash compare.
//
// Scope note: rippled's fixMismatch walks the LedgerHashes skiplist
// backward further than the immediate parent and tries to "close the
// seam" by finding the deepest still-consistent ancestor. This Go
// implementation only invalidates the immediate prev-seq mismatch and
// the forward orphans — deeper history is left untouched. Rationale:
// the skiplist walk requires hashOfSeq reconstruction against the
// adopted state, which is deferred. The common case (single-ledger
// fork at the tip) is fully covered; multi-ledger divergences lower
// in history will be re-tripped on each subsequent adopt as they
// re-become the prev-seq.
func (s *Service) fixMismatchLocked(adopted *ledger.Ledger) {
	adoptedSeq := adopted.Sequence()
	if adoptedSeq == 0 {
		return
	}

	prev, havePrev := s.ledgerHistory[adoptedSeq-1]
	if !havePrev {
		// No prev-seq entry to mismatch against — nothing to do.
		return
	}
	if prev.Hash() == adopted.ParentHash() {
		// Happy path: the adopted ledger chains correctly.
		return
	}

	// Mismatch. Collect the set of seqs to purge:
	//   (a) the mismatched prev-seq itself,
	//   (b) every seq strictly greater than adoptedSeq (orphaned
	//       forward entries — their ancestry passes through prev-seq
	//       or a sibling of `adopted`, both now invalid).
	//
	// Note: seq == adoptedSeq is also purged implicitly because the
	// caller overwrites that slot with `adopted` right after we return.
	// We still collect any tx-index entries associated with it so
	// orphaned tx-hash lookups from the stale ledger don't linger.
	var toRemove []uint32
	toRemove = append(toRemove, adoptedSeq-1)
	if sameSeq, ok := s.ledgerHistory[adoptedSeq]; ok && sameSeq.Hash() != adopted.Hash() {
		toRemove = append(toRemove, adoptedSeq)
	}
	for seq := range s.ledgerHistory {
		if seq > adoptedSeq {
			toRemove = append(toRemove, seq)
		}
	}

	// Collect diagnostic info before mutation for the WARN log. A
	// fixMismatch hit is rare and operationally significant —
	// operators should be able to reconstruct exactly which history
	// slots were purged from a single log line.
	type purged struct {
		Seq       uint32
		Hash      string
		Validated bool
	}
	purgedDetails := make([]purged, 0, len(toRemove))
	validatedSeqPurged := uint32(0)
	validatedHashPurged := [32]byte{}
	hitValidated := false

	for _, seq := range toRemove {
		l, ok := s.ledgerHistory[seq]
		if !ok {
			continue
		}
		h := l.Hash()
		purgedDetails = append(purgedDetails, purged{
			Seq:       seq,
			Hash:      fmt.Sprintf("%x", h[:8]),
			Validated: l.IsValidated(),
		})
		if l.IsValidated() {
			hitValidated = true
			validatedSeqPurged = seq
			validatedHashPurged = h
		}

		// Drop tx-index entries that resolve to this invalidated seq.
		// Iteration order over a Go map is randomized; that is fine
		// here because we mutate only entries whose value equals `seq`.
		for txHash, txSeq := range s.txIndex {
			if txSeq == seq {
				delete(s.txIndex, txHash)
				delete(s.txPositionIndex, txHash)
			}
		}

		s.deleteHistoryLocked(seq)
	}

	// Defense-in-depth: if closedLedger was pointing at one of the
	// purged slots, clear it. The caller (AdoptLedgerWithState) is
	// about to reassign closedLedger = adopted anyway, but clearing
	// here ensures any intermediate read (e.g., a deferred logger
	// access) does not dereference a ledger we just invalidated.
	if s.closedLedger != nil {
		closedSeq := s.closedLedger.Sequence()
		if _, purged := s.ledgerHistory[closedSeq]; !purged && closedSeq != adoptedSeq {
			// closedLedger points at a seq we removed from history.
			if closedSeq == adoptedSeq-1 || closedSeq > adoptedSeq {
				s.closedLedger = nil
			}
		}
	}

	// Validated-ledger handling: we do NOT silently reset it. A
	// validated ledger getting invalidated by a parent-hash mismatch
	// means the node previously quorum-validated a hash that the
	// peer-adopted chain now contradicts — a serious fork that
	// requires operator attention. Log ERROR and leave the pointer
	// in place; downstream consumers will observe the divergence
	// (e.g., validatedLedger > adoptedSeq) and either re-sync or
	// surface a visible alert.
	if hitValidated {
		s.logger.Error("fixMismatch purged a validated ledger — possible fork detected",
			"adopted_seq", adoptedSeq,
			"adopted_hash", fmt.Sprintf("%x", adopted.Hash()),
			"adopted_parent_hash", fmt.Sprintf("%x", adopted.ParentHash()),
			"prev_seq", adoptedSeq-1,
			"prev_hash", fmt.Sprintf("%x", prev.Hash()),
			"purged_validated_seq", validatedSeqPurged,
			"purged_validated_hash", fmt.Sprintf("%x", validatedHashPurged),
		)
	}

	adoptedHash := adopted.Hash()
	adoptedParent := adopted.ParentHash()
	prevHash := prev.Hash()
	s.logger.Warn("fixMismatch invalidated diverged history tail",
		"adopted_seq", adoptedSeq,
		"adopted_hash", fmt.Sprintf("%x", adoptedHash[:8]),
		"adopted_parent_hash", fmt.Sprintf("%x", adoptedParent[:8]),
		"stored_prev_hash", fmt.Sprintf("%x", prevHash[:8]),
		"purged_count", len(purgedDetails),
		"purged", purgedDetails,
	)
}

// AcceptConsensusResult closes the current open ledger using a consensus-agreed
// transaction set and close time. Unlike AcceptLedger (standalone), this method:
//   - Takes the already-agreed tx set and close time as parameters
//   - Does NOT require standalone mode
//   - Does NOT automatically validate (validation comes from the validation tracker)
//
// The parent parameter specifies which ledger to build on top of. When the
// consensus engine switches chains (wrong ledger detection), this may differ
// from s.closedLedger. The service resets its internal state accordingly.
//
// The multi-pass retry logic is the same as AcceptLedger to match rippled's
// BuildLedger behavior.
func (s *Service) AcceptConsensusResult(ctx context.Context, parent *ledger.Ledger, txBlobs [][]byte, closeTime time.Time, closeTimeCorrect bool) (uint32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closedLedger == nil {
		return 0, ErrNoClosedLedger
	}

	// If the parent differs from our closed ledger (chain switch via wrong
	// ledger detection), reset internal state to build on the correct chain.
	//
	// Sibling-fork case (issue #470): a same-seq parent with a different
	// hash is ALSO a chain switch. Skipping it leaves s.openLedger pinned
	// to the local alt's state map — subsequent close()s read the alt's
	// hashes from that map's LedgerHashes SLE and stamp them into the
	// next ledger, propagating the divergent chain in memory even after
	// the canonical sibling was adopted into ledgerHistory.
	if parent != nil && (parent.Sequence() != s.closedLedger.Sequence() || parent.Hash() != s.closedLedger.Hash()) {
		s.closedLedger = parent
		s.putHistoryLocked(parent)
		newOpen, err := ledger.NewOpen(parent, closeTime)
		if err != nil {
			return 0, fmt.Errorf("failed to create open ledger from parent: %w", err)
		}
		s.openLedger = newOpen
		// Chain switch is a clean reset, not an LCL transition: rebuild
		// the open-ledger view from scratch via New rather than Accept.
		if err := s.rebuildOpenLedgerViewLocked(); err != nil {
			return 0, err
		}
	}

	if s.openLedger == nil {
		return 0, ErrNoOpenLedger
	}

	var canonicalTxHashes []string
	var retriableTxs []openledger.PendingTx
	if len(txBlobs) > 0 {
		pending := make([]pendingTx, 0, len(txBlobs))
		for _, blob := range txBlobs {
			ptx, err := parsePendingTx(blob)
			if err != nil {
				continue
			}
			pending = append(pending, ptx)
		}

		built, err := s.buildClosedLedgerLocked(pending, closeTime, false)
		if err != nil {
			return 0, err
		}
		retriableTxs = built

		// buildClosedLedgerLocked sorts pending in place; the round-summary
		// log below reports that canonical order.
		canonicalTxHashes = make([]string, 0, len(pending))
		for _, ptx := range pending {
			canonicalTxHashes = append(canonicalTxHashes, fmt.Sprintf("%x", ptx.Hash[:8]))
		}
	}

	// Reset pending transactions
	s.pendingTxs = nil

	// Close the ledger with the consensus-agreed close time. Match
	// rippled's Ledger.cpp:367 — when consensus did not agree on
	// closeTime, set sLCF_NoConsensusTime so the hash matches what
	// rippled produces in the same case (Issue #361).
	var closeFlags uint8
	if !closeTimeCorrect {
		closeFlags = header.LCFNoConsensusTime
	}
	if err := s.openLedger.Close(closeTime, closeFlags); err != nil {
		return 0, fmt.Errorf("failed to close ledger: %w", err)
	}

	// Do NOT auto-validate — validation comes from the consensus validation tracker.

	// Persist. Match rippled's LedgerMaster::setFullLedger ->
	// pendSaveValidated: the bool return is discarded and the chain
	// advance proceeds regardless. Treating persist failure as fatal
	// here would diverge from rippled and risk forks on transient
	// relational-DB issues.
	// Reference: rippled/src/xrpld/app/ledger/detail/LedgerMaster.cpp:831,972
	if err := s.persistLedger(ctx, s.openLedger); err != nil {
		s.logger.Error("failed to persist consensus-closed ledger; chain advance continues",
			"seq", s.openLedger.Sequence(), "err", err)
	}

	closedSeq := s.openLedger.Sequence()
	closedLedgerHash := s.openLedger.Hash()

	// One line per locally-built ledger for diffing against rippled.
	{
		stateRoot, _ := s.openLedger.StateMapHash()
		txRoot, _ := s.openLedger.TxMapHash()
		parentHash := s.openLedger.ParentHash()
		s.logger.Info("local-built ledger round-summary",
			"t", "consensus-build",
			"event", "round-summary",
			"seq", closedSeq,
			"hash", fmt.Sprintf("%x", closedLedgerHash[:8]),
			"parent_hash", fmt.Sprintf("%x", parentHash[:8]),
			"close_time", closeTime.UTC().Format(time.RFC3339Nano),
			"close_time_correct", closeTimeCorrect,
			"close_flags", closeFlags,
			"state_root", fmt.Sprintf("%x", stateRoot[:8]),
			"tx_root", fmt.Sprintf("%x", txRoot[:8]),
			"total_drops", s.openLedger.TotalDrops(),
			"tx_count", len(txBlobs),
			"tx_hashes", canonicalTxHashes,
		)
	}

	// Mirror LedgerHistory::insert(ledger, validated) at
	// LedgerHistory.cpp:55-74 — validated entry wins for the by-seq
	// map. closedLedger reflects the local build so divergence is
	// observable via server_info/ledger_closed.
	if existing, ok := s.ledgerHistory[closedSeq]; ok && existing.Hash() != closedLedgerHash && existing.IsValidated() {
		existingHash := existing.Hash()
		s.logger.Warn("local consensus close diverges from validated ledger; preserving validated in history, keeping local-build as closedLedger reference",
			"seq", closedSeq,
			"local_hash", fmt.Sprintf("%x", closedLedgerHash[:8]),
			"validated_hash", fmt.Sprintf("%x", existingHash[:8]),
		)
		s.closedLedger = s.openLedger
	} else {
		s.closedLedger = s.openLedger
		s.putHistoryLocked(s.openLedger)
	}

	// Drain any validation that arrived before this close (validation
	// tracker leading the consensus close). Fail-safe on expired/mismatch.
	// Capture the return: when drain returns true, the adopted ledger was
	// promoted to validated in-line from the pre-stashed (seq, hash)
	// notification — no later SetValidatedLedger will arrive to fire the
	// legacy eventCallback, so we must fire it inline below (and skip
	// the hash-keyed stash, which would never be drained).
	promotedByDrain := s.drainPendingLedgerValidationLocked(closedSeq, s.closedLedger)

	// Collect transaction results for event callbacks/hooks
	var txResults []TransactionResultEvent
	if s.eventCallback != nil || (s.hooks != nil && (s.hooks.OnLedgerClosed != nil || s.hooks.OnTransaction != nil)) {
		txResults = s.collectTransactionResults(s.closedLedger, closedSeq, closedLedgerHash)
	}

	ledgerInfo, validatedLedgers, err := s.advanceToNewOpenLedgerLocked(closedSeq, closedLedgerHash, retriableTxs)
	if err != nil {
		return 0, err
	}

	// Same hook dispatch as the standalone and peer-adopt paths. The helper
	// reads each tx's real position from s.txPositionIndex (populated by
	// collectTransactionResults above) rather than reporting index 0 to every
	// `transaction` stream subscriber.
	s.fireLedgerClosedHooksLocked(ledgerInfo, txResults, closeTime, validatedLedgers)

	// In the consensus path we do NOT fire eventCallback at close time —
	// the ledger isn't yet validated. Stash the event keyed by hash so
	// SetValidatedLedger can fire it once trusted-validation quorum is
	// reached, keeping WebSocket ledgerClosed events in lockstep with
	// server_info.validated_ledger. Rippled publishes both from the
	// same quorum-gated point (pubLedger / checkAccept).
	//
	// Validation-first race exception: when the drain above promoted
	// validatedLedger in-line, the trusted validation has ALREADY arrived
	// (pre-stashed by an earlier SetValidatedLedger call). No future
	// SetValidatedLedger will land for this hash, so stashing the event
	// would orphan it forever — WebSocket `ledgerClosed` + `transaction`
	// subscribers (wired through SetEventCallback) would miss the ledger.
	// Fire the callback inline instead, matching SetValidatedLedger's own
	// drain-then-dispatch shape.
	if s.eventCallback != nil {
		event := &LedgerAcceptedEvent{
			LedgerInfo:         ledgerInfo,
			TransactionResults: txResults,
		}
		if promotedByDrain {
			// Fire on a goroutine so subscriber callbacks can't reach
			// back into s.mu (which is still held via the deferred
			// Unlock) and deadlock the service.
			callback := s.eventCallback
			go callback(event)
		} else {
			s.stashPendingValidationLocked(closedLedgerHash, event)
		}
	}

	s.logger.Info("Consensus ledger accepted",
		"sequence", closedSeq,
		"hash", fmt.Sprintf("%x", closedLedgerHash[:8]),
		"txs", len(txResults),
	)

	return closedSeq, nil
}

// SetValidatedLedger marks a ledger as validated by consensus and fires
// any stashed eventCallback for that ledger. Called by the consensus
// adaptor when the validation tracker confirms a ledger has received
// trusted-validation quorum.
//
// The expectedHash guards against fork scenarios where peers validated
// a hash different from the one we closed locally at that seq — in that
// case our local ledger is on the wrong fork and must NOT be flipped
// to validated. Matches rippled's checkAccept() which works off the
// validated ledger pointer (hash + seq), not seq alone.
func (s *Service) SetValidatedLedger(seq uint32, expectedHash [32]byte) {
	s.mu.Lock()
	l, ok := s.ledgerHistory[seq]
	// Mirrors LedgerMaster::checkAccept(hash, seq) at LedgerMaster.cpp:
	// 904-918 — hash-keyed in rippled; our seq-keyed map splits into
	// "no entry" or "entry-with-different-hash" (same-height fork).
	// Both stash and arm acquisition.
	if !ok || l.Hash() != expectedHash {
		s.stashPendingLedgerValidationLocked(seq, expectedHash)
		// Capture handler under lock; fire when seq > the last
		// VALIDATED seq (mirrors rippled LedgerMaster::checkAccept's
		// `if (seq < mValidLedgerSeq) return` gate at
		// LedgerMaster.cpp:883). Gating on closedSeq instead silently
		// blocked recovery when a node ran ahead on a private chain:
		// quorum on a divergent canonical seq=N would stash but the
		// arming handler refused to fire because closedSeq >> N.
		var (
			handler func(uint32, [32]byte)
			fire    bool
		)
		if s.onPendingValidationStashed != nil {
			validatedSeq := uint32(0)
			if s.validatedLedger != nil {
				validatedSeq = s.validatedLedger.Sequence()
			}
			if seq > validatedSeq {
				handler = s.onPendingValidationStashed
				fire = true
			}
		}
		s.mu.Unlock()
		if fire {
			go handler(seq, expectedHash)
		}
		return
	}
	_ = l.SetValidated()
	s.validatedLedger = l
	s.evictOldHistoryLocked(seq)

	// Sweep the held local pool against the just-validated ledger.
	// Mirrors LedgerMaster::setValidLedger → app_.getOPs().updateLocalTx(*l)
	// at LedgerMaster.cpp:283. Sweeping here (not on every consensus close)
	// avoids dropping held txs against a ledger consensus later abandons.
	pool := s.localTxs
	event := s.drainPendingValidationLocked(expectedHash)
	callback := s.eventCallback
	s.mu.Unlock()

	// Fold the just-validated ledger into the amendment table outside the lock
	// (the table has its own mutex). Mirrors LedgerMaster::doValidatedLedger.
	s.syncAmendmentTable(l)

	if pool != nil {
		pool.Sweep(l)
	}

	if event != nil && callback != nil {
		go callback(event)
	}
}

// SubmitHeldAdoptionResult describes the disposition of a candidate
// ledger passed to SubmitHeldAdoption. When Stashed is true the caller
// should arm a backward acquisition for (ParentSeq, ParentHash) — without
// that, the stash entry will age out at heldAdoptionTTL (issue #397).
type SubmitHeldAdoptionResult struct {
	// Adopted means the awaited parent was already in history at the
	// expected hash and the candidate was fast-pathed into the adopt.
	Adopted bool

	// Stashed means the candidate is parked in the held-adoption stash
	// pending cascade-promotion at the parent seq.
	Stashed bool

	// ParentSeq, ParentHash describe the awaited parent. Set whenever
	// h.LedgerIndex > 1, regardless of outcome.
	ParentSeq  uint32
	ParentHash [32]byte
}

// SubmitHeldAdoption routes a fetched replay-delta either to immediate
// adoption (when the awaited parent seq is already in history and its
// hash matches the supplied ParentHash) or to the held-orphan stash
// (keyed by the awaited parent seq = h.LedgerIndex - 1). Stashed
// entries are cascade-adopted later, from inside AdoptLedgerWithState
// at the parent seq, when the adopted hash matches ParentHash.
//
// Safe to call concurrently. Nil header or nil stateMap is rejected;
// nil txMap is allowed (legacy catchup path — AdoptLedgerWithState
// falls back to the genesis-shaped empty tx map).
//
// Mirrors rippled's tryAdvance cascade shape, flattened to single-hop
// (see comment on heldAdoptions for the scope trade-off).
func (s *Service) SubmitHeldAdoption(ctx context.Context, h *header.LedgerHeader, stateMap *shamap.SHAMap, txMap *shamap.SHAMap) (SubmitHeldAdoptionResult, error) {
	if h == nil {
		return SubmitHeldAdoptionResult{}, errors.New("SubmitHeldAdoption: nil header")
	}
	if stateMap == nil {
		return SubmitHeldAdoptionResult{}, errors.New("SubmitHeldAdoption: nil state map")
	}

	res := SubmitHeldAdoptionResult{}
	if h.LedgerIndex > 1 {
		res.ParentSeq = h.LedgerIndex - 1
		res.ParentHash = h.ParentHash
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Evict stale entries on every submission so an operator that
	// repeatedly submits orphans doesn't keep a stale entry alive.
	s.evictExpiredHeldAdoptionsLocked()

	// Fast path: if the awaited parent is already in history at the
	// expected hash, adopt immediately rather than stashing for a
	// cascade that will never re-fire. Genesis (seq 1) has no parent,
	// so the fast path is skipped for seq <= 1; the adopt itself will
	// error downstream if anything is wrong.
	if h.LedgerIndex > 1 {
		parentSeq := h.LedgerIndex - 1
		if parent, ok := s.ledgerHistory[parentSeq]; ok {
			parentHash := parent.Hash()
			if parentHash == h.ParentHash {
				if err := s.adoptLedgerWithStateLocked(ctx, h, stateMap, txMap, 0); err != nil {
					return res, err
				}
				res.Adopted = true
				return res, nil
			}
			// Parent seq present on a different fork — stash; cascade
			// will adopt when the awaited parent arrives and
			// fixMismatchLocked clears the mismatched tail
			// (LedgerMaster.cpp:749-801 setFullLedger pattern). Never
			// pre-emptively delete without a verified anchor.
			s.logger.Info("SubmitHeldAdoption divergent-parent submission stashed",
				"seq", h.LedgerIndex,
				"parent_seq", parentSeq,
				"parent_have", fmt.Sprintf("%x", parentHash[:8]),
				"parent_want", fmt.Sprintf("%x", h.ParentHash[:8]),
			)
		}
	}

	// Parent not yet present — stash.
	s.heldAdoptions[h.LedgerIndex-1] = &pendingAdopt{
		header:   h,
		stateMap: stateMap,
		txMap:    txMap,
		at:       time.Now(),
	}
	res.Stashed = true
	return res, nil
}

// cascadeHeldAdoptionsLocked promotes a held child whose awaited parent
// seq (h.LedgerIndex for the child's key) just finished adopting. If the
// held entry's ParentHash matches the adopted hash, it is removed from
// the stash and adopted via adoptLedgerWithStateLocked — which itself
// re-invokes cascadeHeldAdoptionsLocked, giving a bounded recursive
// walk through any chain of pre-stashed orphans.
//
// Entries older than heldAdoptionTTL are evicted on every call (not
// just on the matched key) so a pathological peer that seeds a stash
// full of stale forks can't defer eviction forever.
//
// Caller must hold s.mu (write).
func (s *Service) cascadeHeldAdoptionsLocked(ctx context.Context, adopted *ledger.Ledger, depth int) {
	// Purge stale entries first so a single adopt sweeps them all out.
	s.evictExpiredHeldAdoptionsLocked()

	if depth >= heldAdoptionCascadeMax {
		s.logger.Warn("cascadeHeldAdoptions: hit recursion cap — refusing further promotion",
			"cap", heldAdoptionCascadeMax,
			"seq", adopted.Sequence(),
		)
		return
	}

	parentSeq := adopted.Sequence()
	held, ok := s.heldAdoptions[parentSeq]
	if !ok {
		return
	}
	delete(s.heldAdoptions, parentSeq)

	adoptedHash := adopted.Hash()
	if held.header.ParentHash != adoptedHash {
		// The held orphan expected a different parent hash at this seq
		// — it was on a divergent fork. Drop it rather than adopting
		// onto the wrong chain.
		s.logger.Warn("cascadeHeldAdoptions: dropping fork-mismatched held entry",
			"seq", held.header.LedgerIndex,
			"parent_have", fmt.Sprintf("%x", adoptedHash[:8]),
			"parent_want", fmt.Sprintf("%x", held.header.ParentHash[:8]),
		)
		return
	}

	s.logger.Info("cascadeHeldAdoptions: promoting held orphan",
		"seq", held.header.LedgerIndex,
		"hash", fmt.Sprintf("%x", held.header.Hash[:8]),
		"depth", depth+1,
	)
	if err := s.adoptLedgerWithStateLocked(ctx, held.header, held.stateMap, held.txMap, depth+1); err != nil {
		// Adoption of the held entry failed (e.g. persistence error on
		// the cascade hop). Log and stop — the outer adopt already
		// succeeded, so we do not surface the cascade error upwards.
		s.logger.Error("cascadeHeldAdoptions: held-entry adopt failed",
			"seq", held.header.LedgerIndex,
			"err", err,
		)
	}
}

// NeedsInitialSync returns true if the node hasn't yet adopted a ledger from peers.
func (s *Service) NeedsInitialSync() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.needsInitialSync
}

// AdoptLedgerHeader adopts a peer's ledger header as our closed ledger.
// Used during initial sync: the node fetches the network's current ledger
// header and starts tracking from there.
// The state map is reused from genesis (valid as long as no transactions
// have changed the state — true for empty ledger sequences).
func (s *Service) AdoptLedgerHeader(h *header.LedgerHeader) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.needsInitialSync {
		return errors.New("not in initial sync mode")
	}

	if s.genesisLedger == nil {
		return errors.New("no genesis ledger available")
	}

	// Snapshot the genesis state map for the adopted ledger
	stateMap, err := s.genesisLedger.StateMapSnapshot()
	if err != nil {
		return fmt.Errorf("failed to snapshot genesis state: %w", err)
	}

	// Update LedgerHashes skiplist so state matches rippled
	if err := skiplist.UpdateOnMap(stateMap, h.LedgerIndex, h.ParentHash); err != nil {
		s.logger.Warn("failed to update skip list during adoption", "error", err)
	}

	// Create empty tx map
	txMap, err := s.genesisLedger.TxMapSnapshot()
	if err != nil {
		return fmt.Errorf("failed to snapshot genesis tx map: %w", err)
	}

	// Create the adopted ledger from the peer's header.
	adopted := ledger.NewFromHeader(*h, stateMap, txMap, drops.Fees{})

	// Update service state. The adopted ledger becomes our closed
	// ledger and joins history, but we do NOT mark it validated —
	// we haven't yet received trusted-validation quorum for this
	// hash ourselves. Matches rippled's sync behavior: a freshly
	// adopted ledger is merely a starting point for tracking;
	// validated_ledger advances later, when the first consensus
	// round whose outcome we can quorum-validate completes.
	//
	// validatedLedger stays at whatever it was before adoption
	// (typically genesis for a first-time sync) until the
	// ValidationTracker fires OnLedgerFullyValidated. Source
	// closedLedger from the install helper's return so the
	// validated-precedence skip keeps closedLedger canonical.
	s.closedLedger = s.installAdoptedLedgerLocked(h.LedgerIndex, adopted)

	// Create new open ledger on top
	openLedger, err := ledger.NewOpen(s.closedLedger, time.Now())
	if err != nil {
		return fmt.Errorf("failed to create open ledger: %w", err)
	}
	s.openLedger = openLedger
	s.needsInitialSync = false

	// Adopt-from-peer is a fresh start, not an LCL transition — rebuild
	// the open-ledger view via New rather than Accept (no prior
	// node-local current view applies to the freshly adopted closed).
	if err := s.rebuildOpenLedgerViewLocked(); err != nil {
		return err
	}

	s.logger.Info("Adopted ledger from peer",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
	)

	return nil
}

// ReAdoptLedgerHeader re-adopts a peer's ledger header while catching up.
// Unlike AdoptLedgerHeader, this works after needsInitialSync has been cleared.
// Used during the catch-up phase when we're still behind the network.
func (s *Service) ReAdoptLedgerHeader(h *header.LedgerHeader) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.genesisLedger == nil {
		return errors.New("no genesis ledger available")
	}

	// Only allow re-adoption if the new sequence is ahead of our current
	if s.closedLedger != nil && h.LedgerIndex <= s.closedLedger.Sequence() {
		return fmt.Errorf("re-adopt seq %d not ahead of current %d", h.LedgerIndex, s.closedLedger.Sequence())
	}

	// Snapshot from the closed ledger so the skiplist accumulates across re-adoptions
	source := s.closedLedger
	if source == nil {
		source = s.genesisLedger
	}
	stateMap, err := source.StateMapSnapshot()
	if err != nil {
		return fmt.Errorf("failed to snapshot state: %w", err)
	}

	// Update LedgerHashes skiplist so state matches rippled
	if err := skiplist.UpdateOnMap(stateMap, h.LedgerIndex, h.ParentHash); err != nil {
		s.logger.Warn("failed to update skip list during re-adoption", "error", err)
	}

	txMap, err := s.genesisLedger.TxMapSnapshot()
	if err != nil {
		return fmt.Errorf("failed to snapshot genesis tx map: %w", err)
	}

	adopted := ledger.NewFromHeader(*h, stateMap, txMap, drops.Fees{})

	// Advance closedLedger to the peer's tip, but do NOT advance
	// validatedLedger here — peers serve us ledgers they themselves
	// closed, and "closed" is not "validated". Rippled's LedgerMaster
	// distinguishes the two, and server_info.validated_ledger is only
	// set after trusted-validation quorum lands. Leaving validatedLedger
	// alone lets the quorum gate in SetValidatedLedger do its job.
	s.closedLedger = s.installAdoptedLedgerLocked(h.LedgerIndex, adopted)

	// Create new open ledger on top
	openLedger, err := ledger.NewOpen(s.closedLedger, time.Now())
	if err != nil {
		return fmt.Errorf("failed to create open ledger: %w", err)
	}
	s.openLedger = openLedger
	s.pendingTxs = nil

	// Re-adopt: fresh start on the peer's tip — rebuild via New.
	if err := s.rebuildOpenLedgerViewLocked(); err != nil {
		return err
	}

	s.logger.Info("Re-adopted ledger from peer",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
	)

	return nil
}

// AdoptLedgerWithState adopts a ledger using a fully-fetched state map from a peer.
// Unlike AdoptLedgerHeader which reuses genesis state, this uses the real state tree
// fetched via the TMGetLedger/TMLedgerData protocol.
//
// txMap is the verified transaction SHAMap when arriving via the
// replay-delta path (rippled LedgerDeltaAcquire installs the peer-
// provided tx-blob tree at LedgerDeltaAcquire.cpp:209). Pass nil for
// header-only state catchup, in which case we reuse genesis's empty
// tx map — matches pre-replay-delta behavior. Dropping the peer-
// provided tx map on replay-delta adoption (the pre-R5.1 bug) left
// `tx`, `tx_history`, `account_tx`, `transaction_entry` RPCs unable
// to answer queries against adopted ledgers, and prevented re-serving
// replay-delta requests for those ledgers to other peers.
func (s *Service) AdoptLedgerWithState(ctx context.Context, h *header.LedgerHeader, stateMap *shamap.SHAMap, txMap *shamap.SHAMap) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adoptLedgerWithStateLocked(ctx, h, stateMap, txMap, 0)
}

// adoptLedgerWithStateLocked is the lock-free core of AdoptLedgerWithState.
// Caller must hold s.mu (write). `cascadeDepth` is the current recursion
// depth of the held-orphan cascade (F6); the public entrypoints pass 0
// and the cascade helper recurses with depth+1 until heldAdoptionCascadeMax.
func (s *Service) adoptLedgerWithStateLocked(
	ctx context.Context,
	h *header.LedgerHeader,
	stateMap *shamap.SHAMap,
	txMap *shamap.SHAMap,
	cascadeDepth int,
) error {
	if s.genesisLedger == nil {
		return errors.New("no genesis ledger available")
	}

	// Use the caller-supplied tx map when available (replay-delta
	// adoption path); fall back to an empty genesis-shaped tx map for
	// the header-only state catchup path that has no per-ledger tx
	// content to install.
	if txMap == nil {
		empty, err := s.genesisLedger.TxMapSnapshot()
		if err != nil {
			return fmt.Errorf("failed to snapshot empty tx map: %w", err)
		}
		txMap = empty
	}

	adopted := ledger.NewFromHeader(*h, stateMap, txMap, drops.Fees{})

	// F5: before installing the adopted ledger into history, check
	// whether it chains to whatever we already have at seq-1. If the
	// parent-hash doesn't match, we're on a divergent fork relative to
	// what the peer served — invalidate the tail (prev-seq + every
	// orphaned forward entry) so subsequent RPCs don't resolve against
	// stale state. Mirrors rippled LedgerMaster::setFullLedger's
	// parent-hash sanity check and fixMismatch() call at
	// LedgerMaster.cpp:849-862.
	s.fixMismatchLocked(adopted)

	// Install into ledgerHistory[seq]; only ADVANCE closedLedger on
	// strict seq increase. Backward-chain cascade fills must not
	// regress the closed-reference pointer.
	canonical := s.installAdoptedLedgerLocked(h.LedgerIndex, adopted)
	advanced := false
	if s.closedLedger == nil || canonical.Sequence() > s.closedLedger.Sequence() {
		s.closedLedger = canonical
		advanced = true
	} else if canonical.Sequence() == s.closedLedger.Sequence() && canonical.Hash() != s.closedLedger.Hash() {
		// Sibling-fork resolution (issue #470): a same-seq adoption with a
		// different hash means the peer's chain replaces our locally-built
		// alt at this tip. closedLedger must point at the adopted entry,
		// otherwise subsequent local builds keep snapshotting the alt's
		// state map (whose LedgerHashes SLE encodes the alt-chain's hashes
		// for ancestors) and emit divergent ledgers forever.
		s.closedLedger = canonical
		advanced = true
	}
	s.needsInitialSync = false

	// Install-skipped: validated entry already at this seq with a
	// different hash. Skip persist/drain/collect/hooks — those ran
	// for the canonical entry.
	if canonical != adopted {
		openLedger, err := ledger.NewOpen(canonical, time.Now())
		if err != nil {
			return fmt.Errorf("failed to create open ledger after adopt-skip: %w", err)
		}
		s.openLedger = openLedger
		if advanced {
			if err := s.rebuildOpenLedgerViewLocked(); err != nil {
				return err
			}
		}
		canonicalHash := canonical.Hash()
		s.logger.Info("Adopted ledger from peer (skip: validated entry kept)",
			"seq", h.LedgerIndex,
			"adopt_hash", fmt.Sprintf("%x", h.Hash[:8]),
			"canonical_hash", fmt.Sprintf("%x", canonicalHash[:8]),
		)
		return nil
	}

	// If a trusted validation for this seq arrived before we got here
	// (validation tracker leading the adopt loop), drain the stash and
	// promote on match. The drain is fail-safe: expired or
	// hash-mismatched entries are deleted without promoting. Capture the
	// return: when drain returns true, the hash-keyed eventCallback stash
	// below must be skipped and the callback fired inline — see the
	// comment at the callback-dispatch block for the full rationale.
	promotedByDrain := s.drainPendingLedgerValidationLocked(h.LedgerIndex, adopted)

	// Persist the adopted ledger exactly as the local close path does so
	// tx/account_tx/tx_history/transaction_entry RPCs can answer queries
	// against it. Matches LedgerMaster::setFullLedger -> pendSaveValidated.
	if err := s.persistLedger(ctx, adopted); err != nil {
		// Degrade gracefully: the in-memory state is still correct and the
		// next consensus close will re-try persistence. Log loudly because
		// a persistent failure breaks tx RPCs silently.
		s.logger.Error("Failed to persist adopted ledger", "seq", h.LedgerIndex, "err", err)
	}

	// Populate the in-memory tx-index and capture per-tx event records
	// so hooks.OnTransaction + stream subscribers see every adopted tx.
	// collectTransactionResults walks the tx map and writes to s.txIndex
	// + s.txPositionIndex as a side effect AND returns the per-tx
	// TransactionResultEvent slice that hook dispatch needs.
	txResults := s.collectTransactionResults(adopted, h.LedgerIndex, h.Hash)

	// Rebuild openLedger only on forward adoption — backward-fills must
	// not regress the engine's open view. Per-seq persist/hooks fire below
	// regardless.
	if advanced {
		openLedger, err := ledger.NewOpen(adopted, time.Now())
		if err != nil {
			return fmt.Errorf("failed to create open ledger: %w", err)
		}
		s.openLedger = openLedger
		// Forward-advance adopt = fresh start on the peer's tip.
		// Rebuild via New so the persistent view re-anchors on adopted.
		if err := s.rebuildOpenLedgerViewLocked(); err != nil {
			return err
		}
	}

	// Fire hooks.OnLedgerClosed + hooks.OnTransaction so WebSocket
	// `ledger` and `transactions` stream subscribers see peer-adopted
	// ledgers. Without this, the streams silently skip every ledger
	// the node catches up to — an observable divergence from rippled,
	// whose pubLedger path fires for both consensus-closed and sync-
	// adopted ledgers.
	ledgerInfo := &LedgerInfo{
		Sequence:   h.LedgerIndex,
		Hash:       h.Hash,
		ParentHash: adopted.ParentHash(),
		CloseTime:  adopted.CloseTime(),
		TotalDrops: adopted.TotalDrops(),
		Validated:  adopted.IsValidated(),
		Closed:     adopted.IsClosed(),
	}
	validatedLedgers := s.getValidatedLedgersRange()
	// Peer-adopted ledgers carry a close time from the adopted header,
	// not from local consensus — use adopted.CloseTime() so downstream
	// subscribers see the network-agreed close time (matches the Header
	// field that was just populated by NewFromHeader).
	s.fireLedgerClosedHooksLocked(ledgerInfo, txResults, adopted.CloseTime(), validatedLedgers)

	// The legacy eventCallback is meant to fire on *validated*, not
	// *closed*. Peer-adopted ledgers advance s.closedLedger but not
	// s.validatedLedger (the quorum gate at SetValidatedLedger owns
	// that transition). Stash the event keyed by hash so the next
	// SetValidatedLedger(seq, hash) for this ledger drains it —
	// the exact same pattern AcceptConsensusResult uses.
	//
	// Validation-first race exception: when the F4 drain above promoted
	// validatedLedger in-line from a pre-stashed (seq, hash) notification,
	// no future SetValidatedLedger will arrive for this hash. Stashing
	// here would orphan the event forever — WebSocket `ledgerClosed` +
	// `transaction` subscribers (wired through SetEventCallback) would
	// silently miss the ledger. Fire the callback inline instead, matching
	// SetValidatedLedger's own drain-then-dispatch shape. Skipping the
	// stash also prevents a double-fire if a late-duplicate
	// SetValidatedLedger arrives for the same hash.
	if s.eventCallback != nil {
		event := &LedgerAcceptedEvent{
			LedgerInfo:         ledgerInfo,
			TransactionResults: txResults,
		}
		if promotedByDrain {
			// Fire on a goroutine so subscriber callbacks can't reach
			// back into s.mu (still held via the caller's defer) and
			// deadlock the service.
			callback := s.eventCallback
			go callback(event)
		} else {
			s.stashPendingValidationLocked(h.Hash, event)
		}
	}

	s.logger.Info("Adopted ledger with full state from peer",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
		"account_hash", fmt.Sprintf("%x", h.AccountHash[:8]),
	)

	// F6: cascade any held adoption that was waiting on this ledger to
	// land. Out-of-order replay-delta completions (seq N+2 arriving
	// before seq N+1) otherwise stall until the inbound loop happens to
	// re-request them. Also evicts entries older than heldAdoptionTTL so
	// the stash doesn't accumulate stale forks across adopt calls.
	s.cascadeHeldAdoptionsLocked(ctx, adopted, cascadeDepth)

	return nil
}

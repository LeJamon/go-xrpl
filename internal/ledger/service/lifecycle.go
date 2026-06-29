package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/skiplist"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

// AcceptLedger closes the open ledger and opens a new one (the ledger_accept RPC;
// standalone only). Pending txs are re-applied in CanonicalTXSet order on a fresh
// copy of the LCL.
func (s *Service) AcceptLedger(ctx context.Context) (uint32, error) {
	return s.AcceptLedgerAt(ctx, time.Time{})
}

// AcceptLedgerAt is AcceptLedger with an explicit close_time (zero → time.Now()),
// used by replay tests to keep close_time byte-identical.
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

	// Re-apply pending in canonical order on a fresh ledger built from the LCL.
	var retriableTxs []openledger.PendingTx
	if len(s.pendingTxs) > 0 {
		built, err := s.buildClosedLedgerLocked(s.pendingTxs, closeTime, s.config.Standalone)
		if err != nil {
			return 0, err
		}
		retriableTxs = built
	} else {
		// flag-ledger close with no pending txs still needs the NegativeUNL transition
		if err := s.applyFlagLedgerNegativeUNL(s.openLedger); err != nil {
			return 0, err
		}
	}

	s.pendingTxs = nil

	if err := s.openLedger.Close(closeTime, 0); err != nil {
		return 0, fmt.Errorf("failed to close ledger: %w", err)
	}

	// Standalone validates immediately.
	if err := s.openLedger.SetValidated(); err != nil {
		return 0, fmt.Errorf("failed to validate ledger: %w", err)
	}

	// Persist best-effort: a persistence failure must not be fatal — treating it
	// so would diverge from rippled and risk forks on transient DB issues.
	if err := s.persistLedger(ctx, s.openLedger); err != nil {
		s.logger.Error("failed to persist closed ledger; chain advance continues",
			"seq", s.openLedger.Sequence(), "err", err)
	}

	closedSeq := s.openLedger.Sequence()
	closedLedgerHash := s.openLedger.Hash()
	s.closedLedger = s.openLedger
	s.validatedLedger = s.openLedger
	s.putHistoryLocked(s.openLedger)
	s.evictOldHistoryLocked(closedSeq)

	// Fold the validated ledger into the amendment table.
	s.syncAmendmentTable(s.validatedLedger)

	// Drain any stashed validation at this seq so it can't match a later
	// re-close (redundant here since standalone already validated). No-op if none.
	s.drainPendingLedgerValidationLocked(closedSeq, s.closedLedger)

	var txResults []TransactionResultEvent
	if s.eventCallback != nil || (s.hooks != nil && (s.hooks.OnLedgerClosed != nil || s.hooks.OnTransaction != nil)) {
		txResults = s.collectTransactionResults(s.closedLedger, closedSeq, closedLedgerHash)
	}

	ledgerInfo, validatedLedgers, err := s.advanceToNewOpenLedgerLocked(closedSeq, closedLedgerHash, retriableTxs)
	if err != nil {
		return 0, err
	}

	// Fire structured hooks; standalone is already validated so the legacy
	// eventCallback fires immediately rather than being stashed.
	s.fireLedgerClosedHooksLocked(ledgerInfo, txResults, closeTime, validatedLedgers)

	if s.eventCallback != nil {
		event := &LedgerAcceptedEvent{
			LedgerInfo:         ledgerInfo,
			TransactionResults: txResults,
		}

		// Goroutine so the callback can't block ledger ops.
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

// buildClosedLedgerLocked canonically sorts pending, re-applies it onto a fresh
// ledger from s.closedLedger, hoists committed txs into s.txIndex, and installs
// the result as s.openLedger, returning the txs left in retry state. Shared by
// the standalone and consensus close paths. Caller must hold s.mu.
// applyFlagLedgerNegativeUNL applies the pending NegativeUNL transition on a
// flag ledger when featureNegativeUNL is enabled; skipping it on the local
// close path forks account_hash from the network. Caller must hold s.mu.
func (s *Service) applyFlagLedgerNegativeUNL(l *ledger.Ledger) error {
	if !protocol.IsFlagLedger(l.Sequence()) {
		return nil
	}
	rules := rulesFromLedger(s.closedLedger, s.logger)
	if rules == nil || !rules.Enabled(amendment.FeatureNegativeUNL) {
		return nil
	}
	if err := l.UpdateNegativeUNL(); err != nil {
		return fmt.Errorf("flag-ledger updateNegativeUNL: %w", err)
	}
	return nil
}

func (s *Service) buildClosedLedgerLocked(pending []pendingTx, closeTime time.Time, skipSigVerify bool) ([]openledger.PendingTx, error) {
	// Salt = SHAMap root of the tx set (rippled consensus-build convention).
	canonicalSort(pending, computeSalt(pending))

	freshLedger, err := ledger.NewOpen(s.closedLedger, closeTime)
	if err != nil {
		return nil, fmt.Errorf("failed to create fresh ledger for close: %w", err)
	}

	// On a flag ledger the NegativeUNL transition must be applied before any txs.
	if err := s.applyFlagLedgerNegativeUNL(freshLedger); err != nil {
		return nil, err
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
		// tec under certainRetry holds for retry, commits on the final pass.
		Mode: openledger.BuildLedgerMode,
		// Amendments from the parent ledger, not the all-on default.
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

// advanceToNewOpenLedgerLocked opens a fresh ledger on s.closedLedger, refreshes
// fee metrics, and rebuilds the open view (replaying retriableTxs), returning the
// closed ledger info and validated-range string. Shared close-path tail. Caller
// must hold s.mu.
func (s *Service) advanceToNewOpenLedgerLocked(closedSeq uint32, closedLedgerHash [32]byte, retriableTxs []openledger.PendingTx) (*LedgerInfo, string, error) {
	newOpen, err := ledger.NewOpen(s.closedLedger, time.Now())
	if err != nil {
		return nil, "", fmt.Errorf("failed to create new open ledger: %w", err)
	}
	s.openLedger = newOpen

	// Refresh fee metrics so the next Accept sees the right open-ledger fee level.
	s.processClosedLedgerLocked()

	// LCL transition: replay the prior view's txs via Accept, retries-first for
	// the build pass's retry set (a harmless superset of rippled's disputed set).
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

// installAdoptedLedgerLocked writes adopted into ledgerHistory[seq] under the
// validated-precedence rule and returns the canonical entry; callers must use the
// return as s.closedLedger to keep history and closed-reference consistent.
// Caller must hold s.mu (write).
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

// fixMismatchLocked invalidates the tail of ledgerHistory when adopted does not
// chain to the entry at adopted.Sequence()-1. On mismatch it purges the prev-seq
// slot and every seq > adoptedSeq (orphaned forward entries), drops their
// tx-index entries, and clears s.closedLedger if it pointed at a purged slot. A
// purged *validated* entry is logged at ERROR rather than silently reset — it
// signals a fork needing operator attention. Caller must hold s.mu (write); no-op
// on the happy path (parent chain matches or no prev entry).
//
// Scope: only the immediate prev-seq mismatch and forward orphans are
// invalidated; deeper history is left to be re-tripped on later adopts.
func (s *Service) fixMismatchLocked(adopted *ledger.Ledger) {
	adoptedSeq := adopted.Sequence()
	if adoptedSeq == 0 {
		return
	}

	prev, havePrev := s.ledgerHistory[adoptedSeq-1]
	if !havePrev {
		return
	}
	if prev.Hash() == adopted.ParentHash() {
		// Happy path: adopted chains correctly.
		return
	}

	// Purge: the mismatched prev-seq, the same-seq alt (caller overwrites it
	// anyway, but its tx-index must go), and every seq > adoptedSeq (orphans).
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

	// Collect purge diagnostics before mutation for the WARN log.
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

		// Drop tx-index entries resolving to this invalidated seq.
		for txHash, txSeq := range s.txIndex {
			if txSeq == seq {
				delete(s.txIndex, txHash)
				delete(s.txPositionIndex, txHash)
			}
		}

		s.deleteHistoryLocked(seq)
	}

	// Defense-in-depth: clear closedLedger if it pointed at a purged slot
	// (the caller reassigns it to adopted anyway).
	if s.closedLedger != nil {
		closedSeq := s.closedLedger.Sequence()
		if _, purged := s.ledgerHistory[closedSeq]; !purged && closedSeq != adoptedSeq {
			if closedSeq == adoptedSeq-1 || closedSeq > adoptedSeq {
				s.closedLedger = nil
			}
		}
	}

	// Never silently reset validatedLedger: a purged validated entry means a
	// quorum-validated hash now contradicted — log ERROR, leave the pointer for
	// downstream divergence handling.
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

// AcceptConsensusResult closes the open ledger from a consensus-agreed tx set and
// close time. Unlike AcceptLedger it takes the agreed set/time as parameters,
// doesn't require standalone, and does NOT auto-validate (the validation tracker
// does). parent is the ledger to build on; when consensus switches chains it may
// differ from s.closedLedger, and the service resets state accordingly.
func (s *Service) AcceptConsensusResult(ctx context.Context, parent *ledger.Ledger, txBlobs [][]byte, closeTime time.Time, closeTimeCorrect bool) (uint32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closedLedger == nil {
		return 0, ErrNoClosedLedger
	}

	// Parent differs from our closed ledger → chain switch; reset state.
	// A same-seq parent with a different hash is also a switch (issue #470):
	// otherwise s.openLedger stays pinned to the local alt's state map and
	// keeps stamping the divergent chain into later ledgers.
	if parent != nil && (parent.Sequence() != s.closedLedger.Sequence() || parent.Hash() != s.closedLedger.Hash()) {
		s.closedLedger = parent
		s.putHistoryLocked(parent)
		newOpen, err := ledger.NewOpen(parent, closeTime)
		if err != nil {
			return 0, fmt.Errorf("failed to create open ledger from parent: %w", err)
		}
		s.openLedger = newOpen
		// Chain switch is a clean reset: rebuild via New, not Accept.
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

		// pending is now in canonical order for the round-summary log.
		canonicalTxHashes = make([]string, 0, len(pending))
		for _, ptx := range pending {
			canonicalTxHashes = append(canonicalTxHashes, fmt.Sprintf("%x", ptx.Hash[:8]))
		}
	} else {
		// empty consensus tx set still needs the flag-ledger NegativeUNL transition
		if err := s.applyFlagLedgerNegativeUNL(s.openLedger); err != nil {
			return 0, err
		}
	}

	s.pendingTxs = nil

	// Close at the consensus close time; set NoConsensusTime when consensus
	// didn't agree, so the hash matches rippled (issue #361).
	var closeFlags uint8
	if !closeTimeCorrect {
		closeFlags = header.LCFNoConsensusTime
	}
	if err := s.openLedger.Close(closeTime, closeFlags); err != nil {
		return 0, fmt.Errorf("failed to close ledger: %w", err)
	}

	// Do NOT auto-validate — validation comes from the consensus validation tracker.

	// Persist best-effort: a persistence failure must not be fatal (would
	// diverge from rippled and risk forks on transient DB issues).
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

	// Validated entry wins the by-seq map; closedLedger still reflects the local
	// build so divergence is observable via server_info/ledger_closed.
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

	// Drain a validation that arrived before this close (tracker leading the
	// close); fail-safe on expired/mismatch. A true return means it was promoted
	// inline, so the eventCallback must fire inline below (no later
	// SetValidatedLedger will arrive to drain a hash-keyed stash).
	promotedByDrain := s.drainPendingLedgerValidationLocked(closedSeq, s.closedLedger)

	var txResults []TransactionResultEvent
	if s.eventCallback != nil || (s.hooks != nil && (s.hooks.OnLedgerClosed != nil || s.hooks.OnTransaction != nil)) {
		txResults = s.collectTransactionResults(s.closedLedger, closedSeq, closedLedgerHash)
	}

	ledgerInfo, validatedLedgers, err := s.advanceToNewOpenLedgerLocked(closedSeq, closedLedgerHash, retriableTxs)
	if err != nil {
		return 0, err
	}

	// Same hook dispatch as the other close paths; positions come from
	// s.txPositionIndex, not a constant 0.
	s.fireLedgerClosedHooksLocked(ledgerInfo, txResults, closeTime, validatedLedgers)

	// Consensus close isn't validated yet: stash the event by hash for
	// SetValidatedLedger to fire at quorum, keeping ledgerClosed in lockstep
	// with validated_ledger. Exception: if the drain above already promoted
	// inline, no SetValidatedLedger will arrive — fire inline instead of
	// orphaning the event.
	if s.eventCallback != nil {
		event := &LedgerAcceptedEvent{
			LedgerInfo:         ledgerInfo,
			TransactionResults: txResults,
		}
		if promotedByDrain {
			// Goroutine: subscriber callbacks must not re-enter s.mu (held).
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

// SetValidatedLedger marks a ledger validated by consensus and fires any stashed
// eventCallback. expectedHash guards against forks: if peers validated a
// different hash than we closed at this seq, our ledger is on the wrong fork and
// must NOT be flipped to validated.
func (s *Service) SetValidatedLedger(seq uint32, expectedHash [32]byte) {
	s.mu.Lock()
	l, ok := s.ledgerHistory[seq]
	// rippled checkAccept is hash-keyed; our seq-keyed map splits into "no entry"
	// or "different-hash" (same-height fork) — both stash and arm acquisition.
	if !ok || l.Hash() != expectedHash {
		s.stashPendingLedgerValidationLocked(seq, expectedHash)
		// Capture handler under lock; fire when seq > last validated seq.
		// Gating on closedSeq instead wedged recovery when the node ran ahead
		// on a private chain (closedSeq >> the divergent canonical seq).
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

	// Sweep the held local pool against the just-validated ledger (not every
	// close — consensus may abandon a closed ledger).
	pool := s.localTxs
	event := s.drainPendingValidationLocked(expectedHash)
	callback := s.eventCallback
	s.mu.Unlock()

	// Fold into the amendment table outside the lock (it has its own mutex).
	s.syncAmendmentTable(l)

	if pool != nil {
		pool.Sweep(l)
	}

	if event != nil && callback != nil {
		go callback(event)
	}
}

// SubmitHeldAdoptionResult describes the disposition of a candidate ledger. When
// Stashed, the caller must arm a backward acquisition for (ParentSeq, ParentHash)
// or the entry ages out at heldAdoptionTTL (issue #397).
type SubmitHeldAdoptionResult struct {
	// Adopted: the awaited parent was already in history at the expected hash.
	Adopted bool

	// Stashed: parked in the held-adoption stash pending cascade at the parent seq.
	Stashed bool

	// ParentSeq, ParentHash describe the awaited parent. Set whenever
	// h.LedgerIndex > 1, regardless of outcome.
	ParentSeq  uint32
	ParentHash [32]byte
}

// SubmitHeldAdoption routes a fetched replay-delta either to immediate adoption
// (awaited parent already in history at the matching hash) or to the held-orphan
// stash keyed by the awaited parent seq, cascade-adopted later from
// AdoptLedgerWithState. Safe to call concurrently. Nil header/stateMap rejected;
// nil txMap is allowed (legacy catchup → empty genesis-shaped tx map).
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

	// Evict stale entries on every submission.
	s.evictExpiredHeldAdoptionsLocked()

	// Fast path: parent already in history at the expected hash → adopt now
	// (skipped for seq <= 1, which has no parent).
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
			// Parent present on a different fork — stash; cascade adopts once the
			// awaited parent arrives and fixMismatchLocked clears the tail.
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

// cascadeHeldAdoptionsLocked promotes the held child awaiting the just-adopted
// seq when its ParentHash matches the adopted hash, recursing through any chain
// of pre-stashed orphans (bounded by heldAdoptionCascadeMax). Evicts entries
// older than heldAdoptionTTL on every call. Caller must hold s.mu (write).
func (s *Service) cascadeHeldAdoptionsLocked(ctx context.Context, adopted *ledger.Ledger, depth int) {
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
		// Held orphan expected a different parent hash — divergent fork, drop it.
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
		// Cascade-hop adopt failed; log and stop — the outer adopt succeeded.
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

// AdoptLedgerHeader adopts a peer's ledger header as our closed ledger during
// initial sync. The state map is reused from genesis (valid only while no txs
// have changed state).
func (s *Service) AdoptLedgerHeader(h *header.LedgerHeader) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.needsInitialSync {
		return errors.New("not in initial sync mode")
	}

	if s.genesisLedger == nil {
		return errors.New("no genesis ledger available")
	}

	stateMap, err := s.genesisLedger.StateMapSnapshot()
	if err != nil {
		return fmt.Errorf("failed to snapshot genesis state: %w", err)
	}

	// Update LedgerHashes skiplist so state matches rippled
	if err := skiplist.UpdateOnMap(stateMap, h.LedgerIndex, h.ParentHash); err != nil {
		s.logger.Warn("failed to update skip list during adoption", "error", err)
	}

	txMap, err := s.genesisLedger.TxMapSnapshot()
	if err != nil {
		return fmt.Errorf("failed to snapshot genesis tx map: %w", err)
	}

	adopted := ledger.NewFromHeader(*h, stateMap, txMap, drops.Fees{})

	// Adopted becomes closedLedger and joins history but is NOT marked validated
	// (no quorum yet); validatedLedger advances later via SetValidatedLedger.
	// Source closedLedger from the install helper so validated-precedence holds.
	s.closedLedger = s.installAdoptedLedgerLocked(h.LedgerIndex, adopted)

	openLedger, err := ledger.NewOpen(s.closedLedger, time.Now())
	if err != nil {
		return fmt.Errorf("failed to create open ledger: %w", err)
	}
	s.openLedger = openLedger
	s.needsInitialSync = false

	// Adopt-from-peer is a fresh start: rebuild the open view via New, not Accept.
	if err := s.rebuildOpenLedgerViewLocked(); err != nil {
		return err
	}

	s.logger.Info("Adopted ledger from peer",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
	)

	return nil
}

// ReAdoptLedgerHeader re-adopts a peer's header during catch-up — like
// AdoptLedgerHeader but after needsInitialSync is cleared.
func (s *Service) ReAdoptLedgerHeader(h *header.LedgerHeader) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.genesisLedger == nil {
		return errors.New("no genesis ledger available")
	}

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

	// Advance closedLedger to the peer's tip but NOT validatedLedger: "closed"
	// is not "validated"; the quorum gate in SetValidatedLedger owns that.
	s.closedLedger = s.installAdoptedLedgerLocked(h.LedgerIndex, adopted)

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

// AdoptLedgerWithState adopts a ledger using a fully-fetched state map from a
// peer (unlike AdoptLedgerHeader, which reuses genesis state). txMap is the
// verified tx SHAMap on the replay-delta path; pass nil for header-only state
// catchup (reuses genesis's empty tx map). A nil txMap on replay-delta adoption
// would leave tx/account_tx/transaction_entry RPCs unable to answer against the
// adopted ledger.
func (s *Service) AdoptLedgerWithState(ctx context.Context, h *header.LedgerHeader, stateMap *shamap.SHAMap, txMap *shamap.SHAMap) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adoptLedgerWithStateLocked(ctx, h, stateMap, txMap, 0)
}

// adoptLedgerWithStateLocked is the lock-held core of AdoptLedgerWithState.
// Caller must hold s.mu (write). cascadeDepth is the held-orphan cascade depth;
// public entrypoints pass 0, the cascade recurses to heldAdoptionCascadeMax.
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

	// Caller-supplied tx map (replay-delta) or an empty genesis-shaped one
	// (header-only catchup).
	if txMap == nil {
		empty, err := s.genesisLedger.TxMapSnapshot()
		if err != nil {
			return fmt.Errorf("failed to snapshot empty tx map: %w", err)
		}
		txMap = empty
	}

	adopted := ledger.NewFromHeader(*h, stateMap, txMap, drops.Fees{})

	// Invalidate the history tail if adopted doesn't chain to our seq-1 entry
	// (divergent fork) so RPCs don't resolve stale state. See fixMismatchLocked.
	s.fixMismatchLocked(adopted)

	// Install into history; advance closedLedger only on strict seq increase
	// (backward cascade fills must not regress it).
	canonical := s.installAdoptedLedgerLocked(h.LedgerIndex, adopted)
	advanced := false
	if s.closedLedger == nil || canonical.Sequence() > s.closedLedger.Sequence() {
		s.closedLedger = canonical
		advanced = true
	} else if canonical.Sequence() == s.closedLedger.Sequence() && canonical.Hash() != s.closedLedger.Hash() {
		// Sibling-fork (issue #470): a same-seq adopt with a different hash
		// replaces our local alt; closedLedger must point at adopted or later
		// builds keep snapshotting the alt's state map and diverge forever.
		s.closedLedger = canonical
		advanced = true
	}
	s.needsInitialSync = false

	// Install skipped (validated entry already at this seq, different hash):
	// persist/drain/collect/hooks already ran for the canonical entry.
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

	// Drain a validation that arrived before this adopt; fail-safe on
	// expired/mismatch. A true return means promoted inline → fire the callback
	// inline below instead of stashing (see the callback block).
	promotedByDrain := s.drainPendingLedgerValidationLocked(h.LedgerIndex, adopted)

	// Persist the adopted ledger so tx/account_tx/transaction_entry RPCs can
	// answer against it.
	if err := s.persistLedger(ctx, adopted); err != nil {
		// Degrade gracefully; the next close retries. Log loudly — a failure
		// breaks tx RPCs silently.
		s.logger.Error("Failed to persist adopted ledger", "seq", h.LedgerIndex, "err", err)
	}

	// Populate the tx-index and capture per-tx event records (side effect +
	// return) so hooks and stream subscribers see every adopted tx.
	txResults := s.collectTransactionResults(adopted, h.LedgerIndex, h.Hash)

	// Rebuild openLedger only on forward adoption (backward-fills must not
	// regress the open view).
	if advanced {
		openLedger, err := ledger.NewOpen(adopted, time.Now())
		if err != nil {
			return fmt.Errorf("failed to create open ledger: %w", err)
		}
		s.openLedger = openLedger
		// Forward adopt = fresh start: rebuild via New on adopted.
		if err := s.rebuildOpenLedgerViewLocked(); err != nil {
			return err
		}
	}

	// Fire hooks so `ledger`/`transactions` subscribers see peer-adopted ledgers
	// (else the streams silently skip every catch-up ledger).
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
	// Use the adopted header's close time (network-agreed), not a local one.
	s.fireLedgerClosedHooksLocked(ledgerInfo, txResults, adopted.CloseTime(), validatedLedgers)

	// eventCallback fires on *validated*, not *closed*; peer-adopt advances
	// closedLedger only. Stash by hash for the next SetValidatedLedger to drain.
	// Exception: if the drain above promoted inline, no SetValidatedLedger will
	// arrive — fire inline instead of orphaning the event (and avoid a
	// double-fire on a late-duplicate SetValidatedLedger).
	if s.eventCallback != nil {
		event := &LedgerAcceptedEvent{
			LedgerInfo:         ledgerInfo,
			TransactionResults: txResults,
		}
		if promotedByDrain {
			// Goroutine: subscriber callbacks must not re-enter s.mu (held).
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

	// Cascade any held adoption awaiting this ledger (out-of-order replay-delta
	// completions otherwise stall); also evicts stale held entries.
	s.cascadeHeldAdoptionsLocked(ctx, adopted, cascadeDepth)

	return nil
}

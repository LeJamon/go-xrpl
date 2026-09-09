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
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

// AcceptLedger closes the open ledger and opens a new one (the ledger_accept RPC;
// standalone only). Pending txs are re-applied in CanonicalTXSet order on a fresh
// copy of the LCL.
func (s *Service) AcceptLedger(ctx context.Context) (uint32, error) {
	return s.acceptLedgerAt(ctx, time.Time{})
}

// acceptLedgerAt lets replay tests keep close_time byte-identical without
// exposing deterministic clock control through the RPC service or wire.
func (s *Service) acceptLedgerAt(ctx context.Context, explicitCloseTime time.Time) (uint32, error) {
	if err := s.lockOpenLedgerIfRunning(openLedgerConsensus); err != nil {
		return 0, err
	}
	defer s.openLedgerMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyComponent.mu.Lock()
	defer s.historyComponent.mu.Unlock()

	if !s.config.Standalone {
		return 0, svcerr.ErrNotStandalone
	}

	if s.openLedger == nil {
		return 0, svcerr.ErrNoOpenLedger
	}

	if s.closedLedger == nil {
		return 0, svcerr.ErrNoClosedLedger
	}

	closeTime := explicitCloseTime
	if closeTime.IsZero() {
		closeTime = time.Now()
	}

	// Re-apply pending in canonical order on a fresh ledger built from the LCL.
	var retriableTxs []openledger.PendingTx
	closed, replayed, err := s.applyStartupReplayLocked()
	if err != nil {
		return 0, err
	}
	if replayed {
		retriableTxs = append(retriableTxs, s.pendingTxs...)
	} else if len(s.pendingTxs) == 0 {
		closed, err = s.openLedger.MutableSnapshotUnflushed()
		if err != nil {
			return 0, fmt.Errorf("snapshot open ledger for close: %w", err)
		}
		if err := s.applyFlagLedgerNegativeUNL(closed); err != nil {
			return 0, err
		}
	} else {
		closed, retriableTxs, err = s.buildClosedLedgerLocked(s.pendingTxs, closeTime, s.config.Standalone)
		if err != nil {
			return 0, err
		}
	}

	if !replayed {
		if err := closed.Close(closeTime, 0); err != nil {
			return 0, fmt.Errorf("failed to close ledger: %w", err)
		}
	}

	// Standalone validates immediately.
	if !closed.IsValidated() {
		if err := closed.SetValidated(); err != nil {
			return 0, fmt.Errorf("failed to validate ledger: %w", err)
		}
	}
	closedSeq := closed.Sequence()
	closedLedgerHash := closed.Hash()
	stagedResults, err := stageTransactionResults(closed, closedSeq, closedLedgerHash)
	if err != nil {
		return 0, fmt.Errorf("collect transaction results: %w", err)
	}
	newOpen, err := s.prepareNewOpenLedgerLocked(closed, retriableTxs)
	if err != nil {
		return 0, err
	}
	s.pendingTxs = nil
	if replayed {
		s.startupReplay = nil
	}

	// Persist best-effort: a persistence failure must not be fatal — treating it
	// so would diverge from rippled and risk forks on transient DB issues.
	if err := s.persistLedger(ctx, closed); err != nil {
		s.logger.Error("failed to persist closed ledger; chain advance continues",
			"seq", closed.Sequence(), "err", err)
	}

	s.closedLedger = closed
	s.validatedLedger = closed
	s.validatedSignTime = closed.CloseTime()
	s.putHistoryLocked(closed)
	s.evictOldHistoryLocked(closedSeq)
	s.openLedger = newOpen
	s.tickLoadFeeLocked()

	// Fold the validated ledger into the amendment table.
	s.syncTable(s.validatedLedger)

	s.commitTransactionResultsLocked(stagedResults)
	var txResults []TransactionResultEvent
	if s.hasEventSink() {
		txResults = stagedResults.results
	}

	s.dispatchLedgerEvent(&LedgerAcceptedEvent{
		LedgerInfo:         ledgerInfo(closed),
		Ledger:             s.closedLedger,
		TransactionResults: txResults,
	})

	s.logger.Info("Ledger accepted",
		"sequence", closedSeq,
		"hash", fmt.Sprintf("%x", closedLedgerHash[:8]),
		"txs", len(txResults),
	)

	return closedSeq, nil
}

// applyFlagLedgerNegativeUNL applies the pending NegativeUNL transition on a
// flag ledger; skipping it on the local close path forks account_hash from the
// network.
func (s *Service) applyFlagLedgerNegativeUNL(l *ledger.Ledger) error {
	if !protocol.IsFlagLedger(l.Sequence()) {
		return nil
	}
	if err := l.UpdateNegativeUNL(); err != nil {
		return fmt.Errorf("flag-ledger updateNegativeUNL: %w", err)
	}
	return nil
}

func (s *Service) buildClosedLedgerLocked(pending []openledger.PendingTx, closeTime time.Time, skipSigVerify bool) (*ledger.Ledger, []openledger.PendingTx, error) {
	salt, err := openledger.ComputeSalt(pending)
	if err != nil {
		return nil, nil, err
	}
	return s.buildClosedLedger(s.closedLedger, pending, salt, closeTime, skipSigVerify, nil)
}

func (s *Service) buildClosedLedger(parent *ledger.Ledger, pending []openledger.PendingTx, salt [32]byte, closeTime time.Time, skipSigVerify bool, applyDuration *time.Duration) (*ledger.Ledger, []openledger.PendingTx, error) {
	openledger.CanonicalSort(pending, salt)

	freshLedger, err := ledger.NewOpenForBuild(parent, closeTime)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create fresh ledger for close: %w", err)
	}

	// On a flag ledger the NegativeUNL transition must be applied before any txs.
	if err := s.applyFlagLedgerNegativeUNL(freshLedger); err != nil {
		return nil, nil, err
	}

	baseFee, reserveBase, reserveIncrement := readFeesFromLedger(parent)
	applyCfg := openledger.ApplyConfig{
		BaseFee:                   baseFee,
		ReserveBase:               reserveBase,
		ReserveIncrement:          reserveIncrement,
		LedgerSequence:            freshLedger.Sequence(),
		NetworkID:                 s.config.NetworkID,
		ParentCloseTime:           parentCloseTimeRippleEpoch(parent),
		ApplicationCloseTime:      protocol.ToRippleTime(freshLedger.CloseTime()),
		ApplicationCloseTimeSet:   true,
		Logger:                    s.config.Logger,
		SkipSignatureVerification: skipSigVerify,
		// tec under certainRetry holds for retry, commits on the final pass.
		Mode: openledger.BuildLedgerMode,
		// Amendments from the parent ledger, not the all-on default.
		Rules: rulesFromLedger(parent, s.logger),
	}

	applyStarted := time.Now()
	if applyDuration != nil {
		defer func() { *applyDuration = time.Since(applyStarted) }()
	}
	var retriableTxs []openledger.PendingTx
	if err := openledger.ApplyTxs(freshLedger, pending, &retriableTxs, applyCfg); err != nil {
		return nil, nil, fmt.Errorf("openledger.ApplyTxs: %w", err)
	}
	return freshLedger, retriableTxs, nil
}

func (s *Service) prepareNewOpenLedgerLocked(closed *ledger.Ledger, retriableTxs []openledger.PendingTx) (*ledger.Ledger, error) {
	newOpen, err := ledger.NewOpen(closed, time.Now())
	if err != nil {
		return nil, fmt.Errorf("failed to create new open ledger: %w", err)
	}
	if err := s.acceptStandaloneOpenLedgerLocked(closed, retriableTxs); err != nil {
		return nil, err
	}
	return newOpen, nil
}

func ledgerInfo(closed *ledger.Ledger) *LedgerInfo {
	return &LedgerInfo{
		Sequence:  closed.Sequence(),
		Hash:      closed.Hash(),
		CloseTime: closed.CloseTime(),
		Validated: closed.IsValidated(),
	}
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

	// A below-tip backfill the canonical entry above chains to: the entries
	// above are NOT orphans of this adopt — purge only the fork ledger below.
	if next, ok := s.ledgerHistory[adoptedSeq+1]; ok && next.ParentHash() == adopted.Hash() {
		staleHash := prev.Hash()
		if prev.IsValidated() {
			s.logger.Error("history backfill contradicts a validated ledger — possible fork",
				"seq", adoptedSeq-1,
				"hash", fmt.Sprintf("%x", staleHash),
			)
		}
		for txHash, txSeq := range s.txIndex {
			if txSeq == adoptedSeq-1 {
				delete(s.txIndex, txHash)
				delete(s.txPositionIndex, txHash)
			}
		}
		s.invalidateCompleteLedger(adoptedSeq - 1)
		s.dropValidationCandidateLocked(adoptedSeq - 1)
		s.drainPendingValidationLocked(staleHash)
		s.deleteHistoryLocked(adoptedSeq - 1)
		s.logger.Warn("history backfill replaced a stale fork ledger below it",
			"seq", adoptedSeq-1,
			"stale_hash", fmt.Sprintf("%x", staleHash[:8]),
			"adopted_seq", adoptedSeq,
		)
		return
	}

	// Purge: the mismatched prev-seq, the same-seq alt (caller overwrites it
	// anyway, but its tx-index must go), and every seq > adoptedSeq (orphans).
	s.invalidateCompleteLedgerRange(adoptedSeq-1, ^uint32(0))
	s.dropValidationCandidateRangeLocked(adoptedSeq-1, adoptedSeq, adopted.Hash())
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
// does). An ordinary result must build on the current closed ledger.
// disputedBlobs are the round's disputed txs we voted NO on (peer-proposed,
// excluded from the agreed set); they get first crack at the new open ledger,
// ahead of the TxQ.
func (s *Service) AcceptConsensusResult(ctx context.Context, parent *ledger.Ledger, txBlobs, disputedBlobs [][]byte, closeTime time.Time, closeTimeCorrect bool) (uint32, error) {
	return s.acceptConsensusResult(ctx, parent, txBlobs, disputedBlobs, closeTime, closeTimeCorrect)
}

// SwitchToPreferredLedger installs the complete ledger selected by consensus as
// the canonical closed-ledger frontier before the recovery round starts.
func (s *Service) SwitchToPreferredLedger(parent *ledger.Ledger) error {
	return s.switchToPreferredLedger(parent, nil)
}

func (s *Service) switchToPreferredLedger(parent *ledger.Ledger, beforeLock func()) error {
	if beforeLock != nil {
		beforeLock()
	}
	if err := s.lockOpenLedgerIfRunning(openLedgerPreferredSwitch); err != nil {
		return err
	}
	s.mu.Lock()
	s.historyComponent.mu.Lock()
	previousValidated := s.validatedLedger
	defer func() {
		notification := s.validatedLedgerNotificationLocked(previousValidated)
		s.historyComponent.mu.Unlock()
		s.mu.Unlock()
		s.openLedgerMu.Unlock()
		notification.notify()
	}()

	if s.closedLedger == nil {
		return svcerr.ErrNoClosedLedger
	}
	if parent == nil || !parent.IsClosed() {
		return ErrPreferredChainSwitch
	}

	parentHash := parent.Hash()
	replacingProvisional := s.networkLedgerState == networkLedgerFastLoadProvisional &&
		parent.Sequence() == s.closedLedger.Sequence() && parentHash != s.closedLedger.Hash()
	if s.validatedLedger != nil {
		validatedSeq := s.validatedLedger.Sequence()
		if parent.Sequence() < validatedSeq {
			return ErrPreferredChainSwitch
		}
		if parent.Sequence() == validatedSeq && parentHash != s.validatedLedger.Hash() {
			if s.networkLedgerState != networkLedgerFastLoadProvisional {
				return ErrPreferredChainSwitch
			}
			replacingProvisional = true
		}
	}
	if parent.Sequence() == s.closedLedger.Sequence() && parentHash == s.closedLedger.Hash() {
		s.completeInitialSyncLocked()
		return nil
	}

	newOpen, err := ledger.NewOpen(parent, time.Now())
	if err != nil {
		return fmt.Errorf("failed to create open ledger after preferred chain switch: %w", err)
	}

	stagedResults, err := stageTransactionResults(parent, parent.Sequence(), parentHash)
	if err != nil {
		return fmt.Errorf("collect transaction results: %w", err)
	}
	if err := s.acceptPreferredOpenLedgerLocked(parent); err != nil {
		return err
	}

	s.fixMismatchLocked(parent)
	s.purgeConflictingHistoryLocked(parent)
	s.putHistoryLocked(parent)
	s.cachePersistedLedgerLocked(parent)
	s.closedLedger = parent
	s.openLedger = newOpen
	s.tickLoadFeeLocked()
	if replacingProvisional {
		s.ledgerEventMu.Lock()
		s.ledgerEventHaveFrontier = parent.Sequence() != 0
		if s.ledgerEventHaveFrontier {
			s.ledgerEventFrontierSeq = parent.Sequence() - 1
			s.ledgerEventFrontierHash = parent.ParentHash()
		}
		for seq := range s.ledgerEventCandidates {
			delete(s.ledgerEventCandidates, seq)
		}
		s.ledgerEventMu.Unlock()
	}
	s.completeInitialSyncLocked()
	if parent.IsValidated() {
		s.confirmFastLoadLocked(parent.Sequence(), parentHash)
	}
	s.commitTransactionResultsLocked(stagedResults)
	if parent.IsValidated() {
		s.evictOldHistoryLocked(parent.Sequence())
		s.enqueuePersist(parent)
	} else {
		s.retainValidationCandidateLocked(parent)
		if s.hasEventSink() {
			s.stashPendingValidationLocked(parentHash, &LedgerAcceptedEvent{
				LedgerInfo:         ledgerInfo(parent),
				Ledger:             parent,
				TransactionResults: stagedResults.results,
			})
		}
	}

	s.logger.Warn("Switched canonical closed ledger",
		"seq", parent.Sequence(),
		"hash", fmt.Sprintf("%x", parentHash[:8]),
	)
	return nil
}

func (s *Service) purgeConflictingHistoryLocked(parent *ledger.Ledger) {
	parentSeq := parent.Sequence()
	parentHash := parent.Hash()
	s.dropValidationCandidateRangeLocked(parentSeq, parentSeq, parentHash)
	if s.closedLedger != nil && s.closedLedger.Sequence() >= parentSeq && s.closedLedger.Hash() != parentHash {
		s.cachePersistedLedgerLocked(s.closedLedger)
	}
	if parentSeq != ^uint32(0) {
		s.invalidateCompleteLedgerRange(parentSeq+1, ^uint32(0))
	}
	removed := make(map[uint32]struct{})
	for seq, existing := range s.ledgerHistory {
		if seq < parentSeq || (seq == parentSeq && existing.Hash() == parentHash) {
			continue
		}
		removed[seq] = struct{}{}
		if seq == parentSeq {
			s.invalidateCompleteLedgerHash(seq, existing.Hash())
		}
		s.deleteHistoryLocked(seq)
	}
	for txHash, txSeq := range s.txIndex {
		if _, ok := removed[txSeq]; ok {
			delete(s.txIndex, txHash)
			delete(s.txPositionIndex, txHash)
		}
	}
}

// SetValidatedLedger marks a ledger validated by consensus and fires any stashed
// event sink. expectedHash guards against forks: if peers validated a
// different hash than we closed at this seq, our ledger is on the wrong fork and
// must NOT be flipped to validated.
func (s *Service) SetValidatedLedger(seq uint32, expectedHash [32]byte) {
	s.SetValidatedLedgerAt(seq, expectedHash, time.Time{})
}

func (s *Service) validatedLedgerEventLocked(l *ledger.Ledger) *LedgerAcceptedEvent {
	s.drainValidationCandidateLocked(l.Sequence(), l.Hash())
	event := s.drainPendingValidationLocked(l.Hash())
	if event == nil {
		return &LedgerAcceptedEvent{
			LedgerInfo: &LedgerInfo{
				Sequence:  l.Sequence(),
				Hash:      l.Hash(),
				CloseTime: l.CloseTime(),
				Validated: true,
			},
			Ledger: l,
		}
	}
	if event.LedgerInfo != nil {
		event.LedgerInfo.Validated = true
	}
	event.Ledger = l
	for i := range event.TransactionResults {
		event.TransactionResults[i].Validated = true
	}
	return event
}

// SetValidatedLedgerAt marks a ledger validated using the trusted-validation
// signing-time median. A zero signing time falls back to the ledger close time.
func (s *Service) SetValidatedLedgerAt(seq uint32, expectedHash [32]byte, signTime time.Time) {
	s.setValidatedLedgerAt(seq, expectedHash, signTime, false)
}

// PromoteStoredValidatedLedgerAt installs and validates a hash-stored acquired
// ledger without changing the closed or open ledger frontiers. The caller must
// establish live trusted-validation quorum before invoking it.
func (s *Service) PromoteStoredValidatedLedgerAt(seq uint32, expectedHash [32]byte, signTime time.Time) {
	s.setValidatedLedgerAt(seq, expectedHash, signTime, true)
}

func (s *Service) setValidatedLedgerAt(seq uint32, expectedHash [32]byte, signTime time.Time, allowStored bool) {
	if !s.beginValidatedLedgerUpdate() {
		return
	}
	updateFinished := false
	defer func() {
		if !updateFinished {
			s.validationWG.Done()
		}
	}()
	var (
		previousValidated  *ledger.Ledger
		l                  *ledger.Ledger
		fromStored         bool
		loadedStored       *ledger.Ledger
		verifiedTipHash    [32]byte
		historicalVerified bool
		gateHeld           bool
	)
	defer func() {
		if gateHeld {
			s.openLedgerMu.Unlock()
		}
	}()
	acquireGateAndRetry := func() error {
		s.historyComponent.mu.Unlock()
		s.mu.Unlock()
		if err := s.lockOpenLedgerIfRunning(openLedgerValidation); err != nil {
			return err
		}
		gateHeld = true
		return nil
	}
	unlockForLookup := func() {
		s.historyComponent.mu.Unlock()
		s.mu.Unlock()
		if gateHeld {
			s.openLedgerMu.Unlock()
			gateHeld = false
		}
	}
	for {
		s.mu.Lock()
		s.historyComponent.mu.Lock()
		previousValidated = s.validatedLedger
		l, fromStored = s.ledgerHistory[seq], false
		if l != nil {
			if l.Hash() == expectedHash {
				if !gateHeld {
					if err := acquireGateAndRetry(); err != nil {
						return
					}
					continue
				}
				break
			}
			if !allowStored {
				s.historyComponent.mu.Unlock()
				s.mu.Unlock()
				return
			}
		}
		candidate := s.validationCandidates[seq]
		if !allowStored {
			if previousValidated == nil {
				s.historyComponent.mu.Unlock()
				s.mu.Unlock()
				return
			}
			if seq > previousValidated.Sequence() {
				if candidate != nil && candidate.Hash() == expectedHash {
					l = candidate
					if !gateHeld {
						if err := acquireGateAndRetry(); err != nil {
							return
						}
						continue
					}
					break
				}
				s.historyComponent.mu.Unlock()
				s.mu.Unlock()
				return
			}
			tipHash := previousValidated.Hash()
			if !historicalVerified || verifiedTipHash != tipHash {
				tip := previousValidated
				unlockForLookup()
				canonicalHash, ok, err := tip.HashOfSeq(seq)
				if err != nil || !ok || canonicalHash != expectedHash {
					return
				}
				verifiedTipHash = tipHash
				historicalVerified = true
				continue
			}
			if candidate != nil && candidate.Hash() == expectedHash {
				l = candidate
				if !gateHeld {
					if err := acquireGateAndRetry(); err != nil {
						return
					}
					continue
				}
				break
			}
		} else if candidate != nil && candidate.Hash() == expectedHash {
			l = candidate
			fromStored = true
			if !gateHeld {
				if err := acquireGateAndRetry(); err != nil {
					return
				}
				continue
			}
			break
		}
		fromStored = allowStored
		if cached := s.persistedLedgers[expectedHash]; cached != nil && cached.Sequence() == seq {
			l = cached
			if !gateHeld {
				if err := acquireGateAndRetry(); err != nil {
					return
				}
				continue
			}
			break
		}
		if loadedStored != nil {
			l = loadedStored
			if !gateHeld {
				if err := acquireGateAndRetry(); err != nil {
					return
				}
				continue
			}
			break
		}
		unlockForLookup()

		var err error
		loadedStored, err = s.loadStoredLedgerByHash(context.Background(), expectedHash)
		if err != nil {
			s.logger.Warn("failed to load stored ledger for validation",
				"seq", seq,
				"hash", fmt.Sprintf("%x", expectedHash[:8]),
				"error", err,
			)
			return
		}
		if loadedStored == nil || loadedStored.Sequence() != seq {
			s.logger.Warn("stored ledger unavailable for validation",
				"seq", seq,
				"hash", fmt.Sprintf("%x", expectedHash[:8]),
			)
			return
		}
	}
	replaceProvisional := s.networkLedgerState == networkLedgerFastLoadProvisional &&
		s.validatedLedger != nil && seq == s.validatedLedger.Sequence() &&
		expectedHash != s.validatedLedger.Hash()
	sameValidatedTip := s.validatedLedger != nil && seq == s.validatedLedger.Sequence() &&
		expectedHash == s.validatedLedger.Hash()
	provisionalWorkingTip := s.networkLedgerState == networkLedgerFastLoadProvisional &&
		s.closedLedger != nil && seq == s.closedLedger.Sequence() && expectedHash == s.closedLedger.Hash()
	if sameValidatedTip && !provisionalWorkingTip {
		s.historyComponent.mu.Unlock()
		s.mu.Unlock()
		return
	}
	if s.validatedLedger != nil && seq <= s.validatedLedger.Sequence() && !replaceProvisional {
		if allowStored && !sameValidatedTip {
			s.historyComponent.mu.Unlock()
			s.mu.Unlock()
			return
		}
		if !fromStored {
			_ = l.SetValidated()
			s.confirmFastLoadLocked(seq, expectedHash)
			s.enqueueValidatedHistoryPersist(l)
			s.dispatchLedgerEvent(s.validatedLedgerEventLocked(l))
		}
		s.historyComponent.mu.Unlock()
		s.mu.Unlock()
		return
	}
	var stagedResults *stagedTransactionResults
	if fromStored {
		var err error
		stagedResults, err = stageTransactionResults(l, seq, expectedHash)
		if err != nil {
			s.logger.Error("failed to collect acquired validated ledger transaction results",
				"seq", seq,
				"hash", fmt.Sprintf("%x", expectedHash[:8]),
				"error", err,
			)
			s.historyComponent.mu.Unlock()
			s.mu.Unlock()
			return
		}
	}
	_ = l.SetValidated()
	if fromStored {
		s.purgeConflictingHistoryLocked(l)
		s.putHistoryLocked(l)
		s.commitTransactionResultsLocked(stagedResults)
	} else {
		s.confirmFastLoadLocked(seq, expectedHash)
	}
	s.validatedLedger = l
	if signTime.IsZero() {
		signTime = l.CloseTime()
	}
	s.validatedSignTime = signTime
	if !fromStored && s.networkLedgerState == networkLedgerFastLoadProvisional {
		s.networkLedgerState = networkLedgerReady
		s.clearFastLoadBaseLocked()
	}
	s.evictOldHistoryLocked(seq)

	// Sweep the held local pool against the just-validated ledger (not every
	// close — consensus may abandon a closed ledger).
	pool := s.localTxs
	event := s.validatedLedgerEventLocked(l)
	if fromStored {
		event.TransactionResults = stagedResults.results
		for i := range event.TransactionResults {
			event.TransactionResults[i].Validated = true
		}
	}
	s.enqueuePersist(l)
	notification := s.validatedLedgerNotificationLocked(previousValidated)
	s.dispatchLedgerEvent(event)
	s.historyComponent.mu.Unlock()
	s.mu.Unlock()
	s.openLedgerMu.Unlock()
	gateHeld = false

	// Fold into the amendment table outside the lock (it has its own mutex).
	s.syncTable(l)

	if pool != nil {
		if err := pool.Sweep(l); err != nil {
			s.logger.Warn("failed to sweep local transactions", "ledger_seq", l.Sequence(), "err", err)
		}
	}

	s.validationWG.Done()
	updateFinished = true
	notification.notify()
}

func (s *Service) completeInitialSyncLocked() {
	if s.networkLedgerState == networkLedgerNeeded {
		s.networkLedgerState = networkLedgerReady
	}
}

func (s *Service) confirmFastLoadLocked(seq uint32, hash [32]byte) {
	if s.networkLedgerState != networkLedgerFastLoadProvisional || s.validatedLedger == nil {
		return
	}
	if seq == s.validatedLedger.Sequence() && hash == s.validatedLedger.Hash() {
		s.networkLedgerState = networkLedgerReady
		s.clearFastLoadBaseLocked()
	}
}

// NeedsInitialSync reports whether explicit network-ledger acquisition is active.
func (s *Service) NeedsInitialSync() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.networkLedgerState == networkLedgerNeeded
}

// IsFastLoadProvisional reports whether FastLoad startup still awaits trusted
// network confirmation or replacement.
func (s *Service) IsFastLoadProvisional() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.networkLedgerState == networkLedgerFastLoadProvisional
}

// StoreLedgerWithState makes a fully-fetched ledger available by hash without
// changing the node's closed/open ledger frontier. Consensus may later select
// the stored ledger as its preferred parent.
func (s *Service) StoreLedgerWithState(ctx context.Context, h *header.LedgerHeader, stateMap *shamap.SHAMap, txMap *shamap.SHAMap) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyComponent.mu.Lock()
	defer s.historyComponent.mu.Unlock()
	return s.storeLedgerWithStateLocked(ctx, h, stateMap, txMap)
}

// BootstrapLedgerWithState stores an acquired ledger and reports whether the
// node still needs an initial network-ledger switch. Consensus owns that switch.
func (s *Service) BootstrapLedgerWithState(ctx context.Context, h *header.LedgerHeader, stateMap *shamap.SHAMap, txMap *shamap.SHAMap) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyComponent.mu.Lock()
	defer s.historyComponent.mu.Unlock()
	initialCandidate := s.networkLedgerState != networkLedgerReady
	return initialCandidate, s.storeLedgerWithStateLocked(ctx, h, stateMap, txMap)
}

// IngestHistoricalLedgerWithState installs an acquired ledger into validated
// history without changing the node's current ledger frontiers.
func (s *Service) IngestHistoricalLedgerWithState(ctx context.Context, h *header.LedgerHeader, stateMap *shamap.SHAMap, txMap *shamap.SHAMap) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyComponent.mu.Lock()
	defer s.historyComponent.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	historical, err := s.ledgerWithStateLocked(h, stateMap, txMap)
	if err != nil {
		return err
	}

	seq := historical.Sequence()
	hash := historical.Hash()
	if s.closedLedger == nil || seq >= s.closedLedger.Sequence() {
		return fmt.Errorf("historical ledger %d is not below the closed ledger frontier", seq)
	}
	existing, existingAtSeq := s.ledgerHistory[seq]
	sameHistoricalLedger := existingAtSeq && existing.Hash() == hash
	child, childExists := s.ledgerHistory[seq+1]
	if childExists && child.ParentHash() != hash {
		return fmt.Errorf("historical ledger %d does not connect to canonical child", seq)
	}
	if sameHistoricalLedger {
		historical = existing
	} else if !childExists {
		return fmt.Errorf("historical ledger %d has no canonical child", seq)
	}
	if !historical.IsValidated() {
		if err := historical.SetValidated(); err != nil {
			return fmt.Errorf("failed to validate historical ledger: %w", err)
		}
	}
	stagedResults, err := stageTransactionResults(historical, seq, hash)
	if err != nil {
		return fmt.Errorf("collect historical transaction results: %w", err)
	}

	var replacedTxHashes [][32]byte
	replacedHash := [32]byte{}
	replacing := existingAtSeq && existing.Hash() != hash
	if replacing {
		replacedHash = existing.Hash()
		if err := existing.ForEachTransaction(func(txHash [32]byte, _ []byte) bool {
			replacedTxHashes = append(replacedTxHashes, txHash)
			return true
		}); err != nil {
			return fmt.Errorf("collect replaced historical transaction hashes: %w", err)
		}
	}

	if replacing {
		for _, txHash := range replacedTxHashes {
			delete(s.txIndex, txHash)
			delete(s.txPositionIndex, txHash)
		}
		s.invalidateCompleteLedgerHash(seq, replacedHash)
	}

	s.putHistoryLocked(historical)
	s.cachePersistedLedgerLocked(historical)
	s.commitTransactionResultsLocked(stagedResults)
	s.enqueueValidatedHistoryPersist(historical)

	s.logger.Info("Ingested historical ledger",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
	)
	return nil
}

func (s *Service) ledgerWithStateLocked(h *header.LedgerHeader, stateMap *shamap.SHAMap, txMap *shamap.SHAMap) (*ledger.Ledger, error) {
	if s.genesisLedger == nil {
		return nil, errors.New("no genesis ledger available")
	}
	if h == nil {
		return nil, errors.New("nil ledger header")
	}
	if stateMap == nil {
		return nil, errors.New("nil ledger state map")
	}
	if calculated := header.CalculateHash(*h); calculated != h.Hash {
		return nil, fmt.Errorf("acquired ledger header hash mismatch: got %x, want %x", calculated, h.Hash)
	}
	stateHash, err := stateMap.Hash()
	if err != nil {
		return nil, fmt.Errorf("failed to calculate acquired state map hash: %w", err)
	}
	if stateHash != h.AccountHash {
		return nil, fmt.Errorf("acquired state map root mismatch: got %x, want %x", stateHash, h.AccountHash)
	}
	if txMap == nil {
		if h.TxHash != ([32]byte{}) {
			return nil, errors.New("nil transaction map for non-empty transaction root")
		}
		empty, err := s.genesisLedger.TxMapSnapshot()
		if err != nil {
			return nil, fmt.Errorf("failed to snapshot empty tx map: %w", err)
		}
		txMap = empty
	}
	txHash, err := txMap.Hash()
	if err != nil {
		return nil, fmt.Errorf("failed to calculate acquired transaction map hash: %w", err)
	}
	if txHash != h.TxHash {
		return nil, fmt.Errorf("acquired transaction map root mismatch: got %x, want %x", txHash, h.TxHash)
	}

	acquired, err := ledger.NewFromHeader(*h, stateMap, txMap, drops.Fees{})
	if err != nil {
		return nil, fmt.Errorf("failed to construct acquired ledger: %w", err)
	}
	return acquired, nil
}

func (s *Service) storeLedgerWithStateLocked(ctx context.Context, h *header.LedgerHeader, stateMap *shamap.SHAMap, txMap *shamap.SHAMap) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stored, err := s.ledgerWithStateLocked(h, stateMap, txMap)
	if err != nil {
		return err
	}

	s.cachePersistedLedgerLocked(stored)
	s.enqueueNodePersist(stored)

	storedHash := stored.Hash()
	s.logger.Info("Stored acquired ledger",
		"seq", stored.Sequence(),
		"hash", fmt.Sprintf("%x", storedHash[:8]),
	)
	return nil
}

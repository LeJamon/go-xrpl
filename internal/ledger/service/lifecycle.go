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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyComponent.mu.Lock()
	defer s.historyComponent.mu.Unlock()

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

// buildClosedLedgerLocked canonically sorts pending and re-applies it onto a
// fresh ledger from s.closedLedger without publishing it.
// applyFlagLedgerNegativeUNL applies the pending NegativeUNL transition on a
// flag ledger; skipping it on the local close path forks account_hash from the
// network. Caller must hold s.mu.
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
	// Salt = SHAMap root of the tx set (rippled consensus-build convention).
	salt, err := openledger.ComputeSalt(pending)
	if err != nil {
		return nil, nil, err
	}
	openledger.CanonicalSort(pending, salt)

	freshLedger, err := ledger.NewOpenForBuild(s.closedLedger, closeTime)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create fresh ledger for close: %w", err)
	}

	// On a flag ledger the NegativeUNL transition must be applied before any txs.
	if err := s.applyFlagLedgerNegativeUNL(freshLedger); err != nil {
		return nil, nil, err
	}

	baseFee, reserveBase, reserveIncrement := readFeesFromLedger(s.closedLedger)
	applyCfg := openledger.ApplyConfig{
		BaseFee:                   baseFee,
		ReserveBase:               reserveBase,
		ReserveIncrement:          reserveIncrement,
		LedgerSequence:            freshLedger.Sequence(),
		NetworkID:                 s.config.NetworkID,
		ParentCloseTime:           parentCloseTimeRippleEpoch(s.closedLedger),
		ApplicationCloseTime:      protocol.ToRippleTime(freshLedger.CloseTime()),
		ApplicationCloseTimeSet:   true,
		Logger:                    s.config.Logger,
		SkipSignatureVerification: skipSigVerify,
		// tec under certainRetry holds for retry, commits on the final pass.
		Mode: openledger.BuildLedgerMode,
		// Amendments from the parent ledger, not the all-on default.
		Rules: rulesFromLedger(s.closedLedger, s.logger),
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
	// LCL transition: replay the prior view's txs via Accept, retries-first for
	// retriableTxs (disputed we-voted-NO txs plus the build pass's leftovers).
	// rippled gates retries-first on disputes alone, but its retry loop drains
	// the shared retriable set during the current-view replay either way;
	// ApplyTxs never re-applies pre-existing retries, so the first pass must
	// run whenever the set is non-empty or the leftovers would be dropped.
	if err := s.acceptOpenLedgerViewLocked(closed, retriableTxs, len(retriableTxs) > 0); err != nil {
		return nil, err
	}
	return newOpen, nil
}

func ledgerInfo(closed *ledger.Ledger) *LedgerInfo {
	return &LedgerInfo{
		Sequence:   closed.Sequence(),
		Hash:       closed.Hash(),
		ParentHash: closed.ParentHash(),
		CloseTime:  closed.CloseTime(),
		TotalDrops: closed.TotalDrops(),
		Validated:  closed.IsValidated(),
		Closed:     closed.IsClosed(),
	}
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
	s.mu.Lock()
	s.historyComponent.mu.Lock()
	previousValidated := s.validatedLedger
	defer func() {
		notification := s.validatedLedgerNotificationLocked(previousValidated)
		s.historyComponent.mu.Unlock()
		s.mu.Unlock()
		notification.notify()
	}()

	if s.closedLedger == nil {
		return ErrNoClosedLedger
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
	if err := s.acceptOpenLedgerViewLocked(parent, nil, false); err != nil {
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
	} else if s.hasEventSink() {
		s.stashPendingValidationLocked(parentHash, &LedgerAcceptedEvent{
			LedgerInfo:         ledgerInfo(parent),
			Ledger:             parent,
			TransactionResults: stagedResults.results,
		})
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

func (s *Service) acceptConsensusResult(
	ctx context.Context,
	parent *ledger.Ledger,
	txBlobs, disputedBlobs [][]byte,
	closeTime time.Time,
	closeTimeCorrect bool,
) (uint32, error) {
	s.mu.Lock()
	previousValidated := s.validatedLedger
	defer s.unlockAndNotifyValidatedLedger(previousValidated)
	s.historyComponent.mu.Lock()
	defer s.historyComponent.mu.Unlock()

	if s.closedLedger == nil {
		return 0, ErrNoClosedLedger
	}

	parentDiffers := parent != nil && (parent.Sequence() != s.closedLedger.Sequence() || parent.Hash() != s.closedLedger.Hash())
	if parentDiffers {
		return 0, fmt.Errorf("%w: closed=%d/%x parent=%d/%x",
			ErrConsensusParentMismatch,
			s.closedLedger.Sequence(), s.closedLedger.Hash(),
			parent.Sequence(), parent.Hash(),
		)
	}

	if s.openLedger == nil {
		return 0, ErrNoOpenLedger
	}

	// ALWAYS rebuild the closed ledger fresh from the parent with exactly the
	// agreed set — including the EMPTY set (rippled buildLCL). Closing the
	// ingress open ledger directly leaks its node-local tx map into the header,
	// so an empty round carries a non-zero per-node tx_root and forks validators
	// with differing pending traffic (a zero-tx ledger must have tx_root=0).
	var canonicalTxHashes []string
	pending := make([]openledger.PendingTx, 0, len(txBlobs))
	for _, blob := range txBlobs {
		ptx, err := openledger.ParsePendingTx(blob)
		if err != nil {
			continue
		}
		pending = append(pending, ptx)
	}
	closed, replayed, err := s.applyStartupReplayLocked()
	if err != nil {
		return 0, err
	}
	var retriableTxs []openledger.PendingTx
	if !replayed {
		closed, retriableTxs, err = s.buildClosedLedgerLocked(pending, closeTime, false)
		if err != nil {
			return 0, err
		}
	} else {
		salt, saltErr := openledger.ComputeSalt(pending)
		if saltErr != nil {
			return 0, saltErr
		}
		openledger.CanonicalSort(pending, salt)
		retriableTxs = append(retriableTxs, pending...)
		closeTime = closed.CloseTime()
		closeTimeCorrect = closed.Header().CloseFlags&header.LCFNoConsensusTime == 0
	}

	// Pseudo-txs can't succeed in a later ledger; malformed blobs are
	// dropped. The merged set is re-sorted with the agreed set's SHAMap root
	// as salt, matching the canonical order rippled's retriable set applies in.
	if len(disputedBlobs) > 0 {
		seen := make(map[[32]byte]struct{}, len(retriableTxs))
		for _, ptx := range retriableTxs {
			seen[ptx.Hash] = struct{}{}
		}
		added := false
		for _, blob := range disputedBlobs {
			ptx, perr := openledger.ParsePendingTx(blob)
			if perr != nil {
				continue
			}
			if ptx.Parsed.TxType().IsPseudoTransaction() {
				continue
			}
			if _, dup := seen[ptx.Hash]; dup {
				continue
			}
			seen[ptx.Hash] = struct{}{}
			retriableTxs = append(retriableTxs, ptx)
			added = true
		}
		if added {
			salt, saltErr := openledger.ComputeSalt(pending)
			if saltErr != nil {
				return 0, saltErr
			}
			openledger.CanonicalSort(retriableTxs, salt)
		}
	}

	// pending is now in canonical order for the round-summary log.
	canonicalTxHashes = make([]string, 0, len(pending))
	for _, ptx := range pending {
		canonicalTxHashes = append(canonicalTxHashes, fmt.Sprintf("%x", ptx.Hash[:8]))
	}

	// Close at the consensus close time; set NoConsensusTime when consensus
	// didn't agree, so the hash matches rippled (issue #361).
	var closeFlags uint8
	if !closeTimeCorrect {
		closeFlags = header.LCFNoConsensusTime
	}
	if !replayed {
		if err := closed.Close(closeTime, closeFlags); err != nil {
			return 0, fmt.Errorf("failed to close ledger: %w", err)
		}
	} else {
		closeFlags = closed.Header().CloseFlags
	}

	// Do NOT auto-validate — validation comes from the consensus validation tracker.

	closedSeq := closed.Sequence()
	closedLedgerHash := closed.Hash()
	stagedResults, err := stageTransactionResults(closed, closedSeq, closedLedgerHash)
	if err != nil {
		return 0, fmt.Errorf("collect transaction results: %w", err)
	}
	// One line per locally-built ledger for diffing against rippled.
	{
		stateRoot, _ := closed.StateMapHash()
		txRoot, _ := closed.TxMapHash()
		parentHash := closed.ParentHash()
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
			"total_drops", closed.TotalDrops(),
			"tx_count", closed.TxCount(),
			"tx_hashes", canonicalTxHashes,
		)
	}
	newOpen, err := s.prepareNewOpenLedgerLocked(closed, retriableTxs)
	if err != nil {
		return 0, err
	}
	s.pendingTxs = nil
	if replayed {
		s.startupReplay = nil
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
		s.closedLedger = closed
	} else {
		s.closedLedger = closed
		s.putHistoryLocked(closed)
	}

	s.commitTransactionResultsLocked(stagedResults)
	var txResults []TransactionResultEvent
	if s.hasEventSink() {
		txResults = stagedResults.results
	}
	if s.closedLedger.IsValidated() {
		s.enqueueValidatedHistoryPersist(s.closedLedger)
	} else {
		s.enqueueNodePersist(s.closedLedger)
	}

	s.openLedger = newOpen
	s.tickLoadFeeLocked()

	// Consensus close isn't validated yet: stash the event by hash for
	// SetValidatedLedger to fire at quorum, keeping ledgerClosed in lockstep
	// with validated_ledger.
	event := &LedgerAcceptedEvent{
		LedgerInfo:         ledgerInfo(closed),
		Ledger:             s.closedLedger,
		TransactionResults: txResults,
	}
	if s.hasEventSink() {
		s.stashPendingValidationLocked(closedLedgerHash, event)
	}

	s.logger.Info("Consensus ledger accepted",
		"sequence", closedSeq,
		"hash", fmt.Sprintf("%x", closedLedgerHash[:8]),
		"txs", len(txResults),
	)

	return closedSeq, nil
}

// SetValidatedLedger marks a ledger validated by consensus and fires any stashed
// event sink. expectedHash guards against forks: if peers validated a
// different hash than we closed at this seq, our ledger is on the wrong fork and
// must NOT be flipped to validated.
func (s *Service) SetValidatedLedger(seq uint32, expectedHash [32]byte) {
	s.SetValidatedLedgerAt(seq, expectedHash, time.Time{})
}

func (s *Service) validatedLedgerEventLocked(l *ledger.Ledger) *LedgerAcceptedEvent {
	event := s.drainPendingValidationLocked(l.Hash())
	if event == nil {
		return &LedgerAcceptedEvent{
			LedgerInfo: &LedgerInfo{
				Sequence:   l.Sequence(),
				Hash:       l.Hash(),
				ParentHash: l.ParentHash(),
				CloseTime:  l.CloseTime(),
				TotalDrops: l.TotalDrops(),
				Validated:  true,
				Closed:     l.IsClosed(),
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
	s.mu.Lock()
	s.historyComponent.mu.Lock()
	previousValidated := s.validatedLedger
	l, inHistory := s.ledgerHistory[seq]
	fromStored := !inHistory || l.Hash() != expectedHash
	if fromStored {
		if !allowStored {
			s.historyComponent.mu.Unlock()
			s.mu.Unlock()
			return
		}
		var ok bool
		l, ok = s.persistedLedgers[expectedHash]
		if !ok || l.Sequence() != seq {
			s.historyComponent.mu.Unlock()
			s.mu.Unlock()
			return
		}
	}
	replaceProvisional := s.networkLedgerState == networkLedgerFastLoadProvisional &&
		s.validatedLedger != nil && seq == s.validatedLedger.Sequence() &&
		expectedHash != s.validatedLedger.Hash()
	if s.validatedLedger != nil && seq <= s.validatedLedger.Sequence() && !replaceProvisional {
		if allowStored {
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

	// Fold into the amendment table outside the lock (it has its own mutex).
	s.syncTable(l)

	if pool != nil {
		if err := pool.Sweep(l); err != nil {
			s.logger.Warn("failed to sweep local transactions", "ledger_seq", l.Sequence(), "err", err)
		}
	}

	notification.notify()
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
	previousValidated := s.validatedLedger
	defer s.unlockAndNotifyValidatedLedger(previousValidated)
	s.historyComponent.mu.Lock()
	defer s.historyComponent.mu.Unlock()

	if h.LedgerIndex == 0 {
		return res, errors.New("SubmitHeldAdoption: ledger sequence must be non-zero")
	}
	if _, err := s.ledgerWithStateLocked(h, stateMap, txMap); err != nil {
		return res, fmt.Errorf("SubmitHeldAdoption: validate candidate: %w", err)
	}

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

// AdoptLedgerWithState adopts a ledger using a fully-fetched state map from a
// peer. txMap is the verified tx SHAMap on the replay-delta path; pass nil for
// state-only catchup of a ledger whose transaction root is empty.
func (s *Service) AdoptLedgerWithState(ctx context.Context, h *header.LedgerHeader, stateMap *shamap.SHAMap, txMap *shamap.SHAMap) error {
	s.mu.Lock()
	previousValidated := s.validatedLedger
	defer s.unlockAndNotifyValidatedLedger(previousValidated)
	s.historyComponent.mu.Lock()
	defer s.historyComponent.mu.Unlock()
	return s.adoptLedgerWithStateLocked(ctx, h, stateMap, txMap, 0)
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
	adopted, err := s.ledgerWithStateLocked(h, stateMap, txMap)
	if err != nil {
		return err
	}
	stagedResults, err := stageTransactionResults(adopted, h.LedgerIndex, h.Hash)
	if err != nil {
		return fmt.Errorf("collect adopted transaction results: %w", err)
	}

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
	s.completeInitialSyncLocked()

	// The canonical entry has already completed persistence, validation
	// draining, and event collection.
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

	s.commitTransactionResultsLocked(stagedResults)
	txResults := stagedResults.results
	if adopted.IsValidated() {
		s.enqueueValidatedHistoryPersist(adopted)
	} else {
		s.enqueueNodePersist(adopted)
	}

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

	// Forward adoption publishes catch-up ledgers. Below-tip history backfills
	// remain silent so streams never run backward.
	if advanced {
		ledgerInfo := &LedgerInfo{
			Sequence:   h.LedgerIndex,
			Hash:       h.Hash,
			ParentHash: adopted.ParentHash(),
			CloseTime:  adopted.CloseTime(),
			TotalDrops: adopted.TotalDrops(),
			Validated:  adopted.IsValidated(),
			Closed:     adopted.IsClosed(),
		}
		// The event sink fires on *validated*, not *closed*; peer-adopt advances
		// closedLedger only. Stash by hash for the next SetValidatedLedger to
		// drain.
		event := &LedgerAcceptedEvent{
			LedgerInfo:         ledgerInfo,
			Ledger:             adopted,
			TransactionResults: txResults,
		}
		if s.hasEventSink() {
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

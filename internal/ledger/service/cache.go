package service

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger"
)

func (s *historyComponent) ledgerBySequence(seq uint32) *ledger.Ledger {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ledgerHistory[seq]
}

func (s *historyComponent) hashesByRange(minSeq, maxSeq uint32) map[uint32][32]byte {
	hashes := make(map[uint32][32]byte)
	if minSeq > maxSeq {
		return hashes
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for seq := minSeq; ; seq++ {
		if l := s.ledgerHistory[seq]; l != nil {
			hashes[seq] = l.Hash()
		}
		if seq == maxSeq {
			break
		}
	}
	return hashes
}

func (s *Service) ensureCompleteLedgerStateLocked() {
	if s.completedLedgers == nil {
		s.completedLedgers = newCompleteLedgerSet()
	}
	if s.completeLedgerTokens == nil {
		s.completeLedgerTokens = make(map[uint32]uint64)
	}
	if s.completeLedgerHashes == nil {
		s.completeLedgerHashes = make(map[uint32][32]byte)
	}
}

func (s *Service) beginValidatedPersistence(seq uint32, hash [32]byte) uint64 {
	s.completeMu.Lock()
	defer s.completeMu.Unlock()
	s.ensureCompleteLedgerStateLocked()
	s.nextCompleteLedgerToken++
	token := s.nextCompleteLedgerToken
	s.completeLedgerTokens[seq] = token
	s.completeLedgerHashes[seq] = hash
	if seq >= s.completeLedgerFloor {
		s.completedLedgers.add(seq)
	}
	return token
}

func (s *Service) recordValidatedPersistence(seq uint32, token uint64, success bool) {
	s.completeMu.Lock()
	defer s.completeMu.Unlock()
	s.ensureCompleteLedgerStateLocked()
	if s.completeLedgerTokens[seq] != token {
		return
	}
	delete(s.completeLedgerTokens, seq)
	if !success || seq < s.completeLedgerFloor {
		delete(s.completeLedgerHashes, seq)
		s.completedLedgers.remove(seq)
		return
	}
	s.completedLedgers.add(seq)
}

// invalidateCompleteLedger removes seq and prevents already-queued persistence
// work for the invalidated fork from adding it back.
func (s *Service) invalidateCompleteLedger(seq uint32) {
	s.persistMu.Lock()
	if job := s.validatedPersistJobs[seq]; job != nil {
		job.canceled.Store(true)
	}
	delete(s.validatedPersistJobs, seq)
	s.completeMu.Lock()
	s.ensureCompleteLedgerStateLocked()
	delete(s.completeLedgerTokens, seq)
	delete(s.completeLedgerHashes, seq)
	s.completedLedgers.remove(seq)
	s.completeMu.Unlock()
	s.persistMu.Unlock()
	s.invalidatePersistedValidatedTip(seq, seq)
}

func (s *Service) invalidateCompleteLedgerHash(seq uint32, hash [32]byte) {
	s.persistMu.Lock()
	job := s.validatedPersistJobs[seq]
	hasReplacement := job != nil && job.l != nil && job.l.Hash() != hash
	if job != nil && !hasReplacement {
		job.canceled.Store(true)
		delete(s.validatedPersistJobs, seq)
	}
	s.completeMu.Lock()
	s.ensureCompleteLedgerStateLocked()
	if s.completeLedgerHashes[seq] == hash {
		delete(s.completeLedgerTokens, seq)
		delete(s.completeLedgerHashes, seq)
		s.completedLedgers.remove(seq)
	}
	s.completeMu.Unlock()
	s.persistMu.Unlock()
	s.invalidatePersistedValidatedTipHash(seq, hash)
}

func (s *Service) invalidateCompleteLedgerRange(start, end uint32) {
	if start > end {
		return
	}
	s.persistMu.Lock()
	for seq, job := range s.validatedPersistJobs {
		if seq >= start && seq <= end {
			job.canceled.Store(true)
			delete(s.validatedPersistJobs, seq)
		}
	}
	s.completeMu.Lock()
	s.ensureCompleteLedgerStateLocked()
	for seq := range s.completeLedgerTokens {
		if seq >= start && seq <= end {
			delete(s.completeLedgerTokens, seq)
		}
	}
	for seq := range s.completeLedgerHashes {
		if seq >= start && seq <= end {
			delete(s.completeLedgerHashes, seq)
		}
	}
	s.completedLedgers.removeRange(start, end)
	s.completeMu.Unlock()
	s.persistMu.Unlock()
	s.invalidatePersistedValidatedTip(start, end)
}

func (s *Service) clampCompleteLedgers(floor uint32) {
	if floor == 0 {
		return
	}
	s.persistMu.Lock()
	s.completeMu.Lock()
	defer s.persistMu.Unlock()
	defer s.completeMu.Unlock()
	s.ensureCompleteLedgerStateLocked()
	if floor <= s.completeLedgerFloor {
		return
	}
	s.completeLedgerFloor = floor
	for seq, job := range s.validatedPersistJobs {
		if seq < floor {
			job.canceled.Store(true)
			delete(s.validatedPersistJobs, seq)
		}
	}
	for seq := range s.completeLedgerTokens {
		if seq < floor {
			delete(s.completeLedgerTokens, seq)
		}
	}
	for seq := range s.completeLedgerHashes {
		if seq < floor {
			delete(s.completeLedgerHashes, seq)
		}
	}
	s.completedLedgers.removeRange(0, floor-1)
}

func (s *Service) hasCompleteLedger(l *ledger.Ledger) bool {
	if l == nil {
		return false
	}
	s.completeMu.RLock()
	defer s.completeMu.RUnlock()
	if s.completedLedgers == nil || !s.completedLedgers.contains(l.Sequence()) {
		return false
	}
	return s.completeLedgerHashes[l.Sequence()] == l.Hash()
}

// HasCompleteLedger reports whether seq belongs to the completed-ledger set.
func (s *Service) HasCompleteLedger(seq uint32) bool {
	s.completeMu.RLock()
	defer s.completeMu.RUnlock()
	return s.completedLedgers != nil && s.completedLedgers.contains(seq)
}

func (s *Service) completeLedgersString() string {
	s.completeMu.RLock()
	defer s.completeMu.RUnlock()
	if s.completedLedgers == nil {
		return "empty"
	}
	return s.completedLedgers.String()
}

func (s *Service) completeLedgerEvictionStatus(seq uint32) (tracked, durable bool) {
	s.completeMu.RLock()
	defer s.completeMu.RUnlock()
	_, hasHash := s.completeLedgerHashes[seq]
	_, pending := s.completeLedgerTokens[seq]
	complete := s.completedLedgers != nil && s.completedLedgers.contains(seq)
	tracked = complete || hasHash || pending
	durable = s.nodeStore != nil &&
		s.shamapFamily != nil &&
		complete &&
		hasHash &&
		!pending
	return tracked, durable
}

// evictOldHistoryLocked drops ledgerHistory + tx-index entries outside the
// configured cache window. Caller holds Service.mu and historyComponent.mu.
func (s *Service) evictOldHistoryLocked(latestValidatedSeq uint32) {
	window := s.ledgerCacheSize()
	if latestValidatedSeq <= window {
		return
	}
	cutoff := latestValidatedSeq - window
	for seq, l := range s.ledgerHistory {
		if seq > cutoff {
			continue
		}
		if tracked, durable := s.completeLedgerEvictionStatus(seq); tracked && !durable {
			s.invalidateCompleteLedger(seq)
		}
		_ = l.ForEachTransaction(func(txHash [32]byte, _ []byte) bool {
			delete(s.txIndex, txHash)
			delete(s.txPositionIndex, txHash)
			return true
		})
		s.deleteHistoryLocked(seq)
	}
}

// putHistoryLocked installs l into ledgerHistory, keeping the by-hash index in
// sync. Caller holds historyComponent.mu.
func (s *historyComponent) putHistoryLocked(l *ledger.Ledger) {
	seq := l.Sequence()
	if old, ok := s.ledgerHistory[seq]; ok {
		delete(s.ledgerByHash, old.Hash())
	}
	s.ledgerHistory[seq] = l
	s.ledgerByHash[l.Hash()] = seq
}

// deleteHistoryLocked removes seq from ledgerHistory and the by-hash index.
// Caller holds historyComponent.mu.
func (s *historyComponent) deleteHistoryLocked(seq uint32) {
	if old, ok := s.ledgerHistory[seq]; ok {
		delete(s.ledgerByHash, old.Hash())
		delete(s.ledgerHistory, seq)
	}
}

func (s *Service) cachePersistedLedgerLocked(l *ledger.Ledger) {
	hash := l.Hash()
	if existing, ok := s.persistedLedgers[hash]; ok {
		if existing.IsValidated() && !l.IsValidated() {
			return
		}
		s.persistedLedgers[hash] = l
		return
	}
	s.persistedLedgers[hash] = l
	s.persistedLedgerFIFO = append(s.persistedLedgerFIFO, hash)
	if len(s.persistedLedgerFIFO) <= int(s.ledgerCacheSize()) {
		return
	}
	oldest := s.persistedLedgerFIFO[0]
	s.persistedLedgerFIFO = s.persistedLedgerFIFO[1:]
	delete(s.persistedLedgers, oldest)
}

// caps the pending-validation stash so a node that never reaches quorum can't
// leak memory; 256 ≈ 13min at 3s close, enough to cover catch-up (issue #395)
const pendingValidationMaxLen = 256

// stashPendingValidationLocked stashes an accepted event by hash for later
// event sink dispatch on full validation, LRU-evicting at the cap.
// Caller must hold s.mu.
func (s *Service) stashPendingValidationLocked(hash [32]byte, event *LedgerAcceptedEvent) {
	if _, exists := s.pendingValidation[hash]; !exists {
		s.pendingValidationOrder = append(s.pendingValidationOrder, hash)
	}
	s.pendingValidation[hash] = event

	for len(s.pendingValidationOrder) > pendingValidationMaxLen {
		oldest := s.pendingValidationOrder[0]
		s.pendingValidationOrder = s.pendingValidationOrder[1:]
		// Cap-eviction drops an event that may later reach quorum (no rippled
		// equivalent); warn so a stuck-validation issue is visible.
		if s.logger != nil {
			s.logger.Warn("pendingValidation LRU drop — event lost for this ledger hash",
				"hash", fmt.Sprintf("%x", oldest[:8]),
				"cap", pendingValidationMaxLen,
			)
		}
		delete(s.pendingValidation, oldest)
	}
}

func (s *Service) retainValidationCandidateLocked(l *ledger.Ledger) {
	seq := l.Sequence()
	if _, exists := s.validationCandidates[seq]; !exists {
		s.validationCandidateOrder = append(s.validationCandidateOrder, seq)
	}
	s.validationCandidates[seq] = l
	for len(s.validationCandidateOrder) > pendingValidationMaxLen {
		oldest := s.validationCandidateOrder[0]
		s.validationCandidateOrder = s.validationCandidateOrder[1:]
		delete(s.validationCandidates, oldest)
	}
}

func (s *Service) drainValidationCandidateLocked(seq uint32, hash [32]byte) {
	l := s.validationCandidates[seq]
	if l == nil || l.Hash() != hash {
		return
	}
	s.dropValidationCandidateLocked(seq)
}

func (s *Service) dropValidationCandidateLocked(seq uint32) {
	delete(s.validationCandidates, seq)
	for i, candidateSeq := range s.validationCandidateOrder {
		if candidateSeq == seq {
			s.validationCandidateOrder = append(
				s.validationCandidateOrder[:i],
				s.validationCandidateOrder[i+1:]...,
			)
			break
		}
	}
}

func (s *Service) dropValidationCandidateRangeLocked(first, keepSeq uint32, keepHash [32]byte) {
	for seq, candidate := range s.validationCandidates {
		if seq < first || (seq == keepSeq && candidate.Hash() == keepHash) {
			continue
		}
		s.dropValidationCandidateLocked(seq)
		s.drainPendingValidationLocked(candidate.Hash())
	}
}

// drainPendingValidationLocked removes and returns the stashed event for hash,
// or nil. Caller must hold s.mu.
func (s *Service) drainPendingValidationLocked(hash [32]byte) *LedgerAcceptedEvent {
	event, ok := s.pendingValidation[hash]
	if !ok {
		return nil
	}
	delete(s.pendingValidation, hash)
	for i, h := range s.pendingValidationOrder {
		if h == hash {
			s.pendingValidationOrder = append(s.pendingValidationOrder[:i], s.pendingValidationOrder[i+1:]...)
			break
		}
	}
	return event
}

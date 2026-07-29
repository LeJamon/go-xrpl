package service

import (
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
)

// caps in-memory ledgerHistory + tx-index to a window of recent validated
// ledgers; older seqs fall through to the relational DB
const historyWindow = 256

const persistedLedgerCacheSize = historyWindow

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

// evictOldHistoryLocked drops ledgerHistory + tx-index entries older than the
// historyWindow. Caller holds Service.mu and historyComponent.mu.
func (s *Service) evictOldHistoryLocked(latestValidatedSeq uint32) {
	if latestValidatedSeq <= historyWindow {
		return
	}
	cutoff := latestValidatedSeq - historyWindow
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

func (s *historyComponent) cachePersistedLedgerLocked(l *ledger.Ledger) {
	hash := l.Hash()
	if _, ok := s.persistedLedgers[hash]; ok {
		return
	}
	s.persistedLedgers[hash] = l
	s.persistedLedgerFIFO = append(s.persistedLedgerFIFO, hash)
	if len(s.persistedLedgerFIFO) <= persistedLedgerCacheSize {
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

// pendingValidationEntry records a trusted-validation notification for a seq not
// yet in ledgerHistory. `at` TTL-guards it: a stale entry is discarded on drain
// rather than promoting a fork.
type pendingValidationEntry struct {
	expectedHash [32]byte
	signTime     time.Time
	at           time.Time
}

// how long a stashed validation stays promotable on later adopt/close;
// 10min covers deep-gap catch-up (one backward hop per peer round-trip)
const pendingValidationTTL = 10 * time.Minute

// HasPendingLedgerValidation reports whether seq and hash still have a
// non-expired trusted-validation notification awaiting canonical selection.
func (s *Service) HasPendingLedgerValidation(seq uint32, hash [32]byte) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.pendingLedgerValidations[seq]
	return ok && entry.expectedHash == hash && time.Since(entry.at) < pendingValidationTTL
}

// stashPendingLedgerValidationLocked stores a (seq, expectedHash, at) entry
// drained when ledgerHistory[seq] lands, LRU-evicting at the cap.
// Caller must hold s.mu.
func (s *Service) stashPendingLedgerValidationLocked(seq uint32, expectedHash [32]byte, signTime time.Time) {
	if _, exists := s.pendingLedgerValidations[seq]; !exists {
		s.pendingLedgerValidationsOrder = append(s.pendingLedgerValidationsOrder, seq)
	}
	s.pendingLedgerValidations[seq] = pendingValidationEntry{
		expectedHash: expectedHash,
		signTime:     signTime,
		at:           time.Now(),
	}

	for len(s.pendingLedgerValidationsOrder) > pendingValidationMaxLen {
		oldest := s.pendingLedgerValidationsOrder[0]
		s.pendingLedgerValidationsOrder = s.pendingLedgerValidationsOrder[1:]
		// Cap-eviction drops a validation that may later adopt at this seq;
		// warn so a stuck-validation issue is visible.
		if s.logger != nil {
			s.logger.Warn("pendingLedgerValidations LRU drop — validation lost for this seq",
				"seq", oldest,
				"cap", pendingValidationMaxLen,
			)
		}
		delete(s.pendingLedgerValidations, oldest)
	}
}

// drainPendingLedgerValidationLocked removes any stashed validation at seq and,
// on hash match within pendingValidationTTL and a successful current-quorum
// recheck, promotes adopted to validated and returns true. Rejected entries are
// deleted so a later adopt cannot match a stale notification. Caller must hold
// s.mu.
func (s *Service) drainPendingLedgerValidationLocked(seq uint32, adopted *ledger.Ledger) bool {
	entry, ok := s.pendingLedgerValidations[seq]
	if !ok {
		return false
	}
	delete(s.pendingLedgerValidations, seq)
	for i, q := range s.pendingLedgerValidationsOrder {
		if q == seq {
			s.pendingLedgerValidationsOrder = append(s.pendingLedgerValidationsOrder[:i], s.pendingLedgerValidationsOrder[i+1:]...)
			break
		}
	}

	if time.Since(entry.at) >= pendingValidationTTL {
		// Expired: too old to trust; a fresh SetValidatedLedger re-stashes
		// if still current.
		return false
	}
	if adopted.Hash() != entry.expectedHash {
		// Fork: peers validated a different hash at this seq — don't promote.
		return false
	}

	signTime := entry.signTime
	if s.pendingValidationResolver != nil && !s.config.Standalone {
		var accepted bool
		signTime, accepted = s.pendingValidationResolver(seq, entry.expectedHash)
		if !accepted {
			return false
		}
	}

	replaceProvisional := s.networkLedgerState == networkLedgerFastLoadProvisional &&
		s.validatedLedger != nil &&
		seq == s.validatedLedger.Sequence() &&
		adopted.Hash() != s.validatedLedger.Hash()

	// The validated tip is monotonic (rippled LedgerMaster::setFullLedger,
	// LedgerMaster.cpp:948): a below-tip match marks the ledger validated but
	// must not rewind the pointer. The sole same-height exception replaces a
	// provisional fast-loaded ledger after the current trusted quorum recheck.
	if s.validatedLedger != nil && seq <= s.validatedLedger.Sequence() && !replaceProvisional {
		_ = adopted.SetValidated()
		return false
	}

	if signTime.IsZero() {
		signTime = adopted.CloseTime()
	}
	_ = adopted.SetValidated()
	s.validatedLedger = adopted
	s.validatedSignTime = signTime
	if s.networkLedgerState == networkLedgerFastLoadProvisional {
		s.networkLedgerState = networkLedgerReady
	}
	s.evictOldHistoryLocked(seq)
	return true
}

// pendingAdopt is a held replay-delta adoption awaiting its parent seq, carrying
// everything AdoptLedgerWithState needs to apply it without refetch.
type pendingAdopt struct {
	header   *header.LedgerHeader
	stateMap *shamap.SHAMap
	txMap    *shamap.SHAMap
	at       time.Time
}

// must outlast a multi-ledger fork catch-up (60s wedged a node)
const heldAdoptionTTL = 5 * time.Minute

// DoS guard on cascade recursion depth; real cascades are 1-2 hops, a malicious
// orphan chain could otherwise drive arbitrary stack depth
const heldAdoptionCascadeMax = 256

// evictExpiredHeldAdoptionsLocked removes held entries older than
// heldAdoptionTTL. Caller must hold s.mu.
func (s *Service) evictExpiredHeldAdoptionsLocked() {
	if len(s.heldAdoptions) == 0 {
		return
	}
	now := time.Now()
	for key, held := range s.heldAdoptions {
		if now.Sub(held.at) >= heldAdoptionTTL {
			s.logger.Warn("heldAdoption TTL eviction",
				"parent_seq", key,
				"child_seq", held.header.LedgerIndex,
				"age", now.Sub(held.at),
			)
			delete(s.heldAdoptions, key)
		}
	}
}

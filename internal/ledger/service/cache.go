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

// evictOldHistoryLocked drops ledgerHistory + tx-index entries older than the
// historyWindow. Caller must hold s.mu.
func (s *Service) evictOldHistoryLocked(latestValidatedSeq uint32) {
	if latestValidatedSeq <= historyWindow {
		return
	}
	cutoff := latestValidatedSeq - historyWindow
	for seq, l := range s.ledgerHistory {
		if seq > cutoff {
			continue
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
// sync. Caller must hold s.mu.
func (s *Service) putHistoryLocked(l *ledger.Ledger) {
	seq := l.Sequence()
	if old, ok := s.ledgerHistory[seq]; ok {
		delete(s.ledgerByHash, old.Hash())
	}
	s.ledgerHistory[seq] = l
	s.ledgerByHash[l.Hash()] = seq
}

// deleteHistoryLocked removes seq from ledgerHistory and the by-hash
// index. Caller must hold s.mu.
func (s *Service) deleteHistoryLocked(seq uint32) {
	if old, ok := s.ledgerHistory[seq]; ok {
		delete(s.ledgerByHash, old.Hash())
		delete(s.ledgerHistory, seq)
	}
}

// caps the pending-validation stash so a node that never reaches quorum can't
// leak memory; 256 ≈ 13min at 3s close, enough to cover catch-up (issue #395)
const pendingValidationMaxLen = 256

// stashPendingValidationLocked stashes an accepted event by hash for later
// eventCallback dispatch on full validation, LRU-evicting at the cap.
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
	at           time.Time
}

// how long a stashed validation stays promotable on later adopt/close;
// 10min covers deep-gap catch-up (one backward hop per peer round-trip)
const pendingValidationTTL = 10 * time.Minute

// stashPendingLedgerValidationLocked stores a (seq, expectedHash, at) entry
// drained when ledgerHistory[seq] lands, LRU-evicting at the cap.
// Caller must hold s.mu.
func (s *Service) stashPendingLedgerValidationLocked(seq uint32, expectedHash [32]byte) {
	if _, exists := s.pendingLedgerValidations[seq]; !exists {
		s.pendingLedgerValidationsOrder = append(s.pendingLedgerValidationsOrder, seq)
	}
	s.pendingLedgerValidations[seq] = pendingValidationEntry{
		expectedHash: expectedHash,
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
// on hash match within pendingValidationTTL, promotes adopted to validated and
// returns true. Expired/mismatched entries are deleted (else a later adopt at the
// same seq could match a stale notification). Caller must hold s.mu.
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

	_ = adopted.SetValidated()
	s.validatedLedger = adopted
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

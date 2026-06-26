package service

import (
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
)

// historyWindow caps the in-memory ledgerHistory + tx-index caches to
// a sliding window of recent validated ledgers. Mirrors rippled's
// default ledger-cache capacity (SizedItem::ledgerSize "large" tier =
// 256, see rippled/src/xrpld/core/detail/Config.cpp). Range-style RPC
// lookups for older sequences fall through to the relational DB; hash-
// based GetTransaction lookups beyond the window currently return
// "not found" until a DB fallback lands.
const historyWindow = 256

// evictOldHistoryLocked drops ledgerHistory entries (and their
// associated tx-index entries) with seq <= latestValidatedSeq -
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

// putHistoryLocked installs l into ledgerHistory and keeps the by-hash
// index in sync, dropping a replaced same-sequence entry's hash. Caller
// must hold s.mu.
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

// pendingValidationMaxLen caps the pending-validation stash so a node
// that never reaches quorum (misconfigured UNL, network partition) can't
// leak memory. At 3s ledger close, 256 entries ≈ 13 minutes — large
// enough to cover extended catch-up without evicting in-flight quorum
// notifications (issue #395).
const pendingValidationMaxLen = 256

// stashPendingValidationLocked stores an accepted event keyed by hash
// for later eventCallback dispatch once the ledger is fully validated.
// LRU-evicts the oldest entry if the stash would exceed its cap.
// Caller must hold s.mu.
func (s *Service) stashPendingValidationLocked(hash [32]byte, event *LedgerAcceptedEvent) {
	if _, exists := s.pendingValidation[hash]; !exists {
		s.pendingValidationOrder = append(s.pendingValidationOrder, hash)
	}
	s.pendingValidation[hash] = event

	for len(s.pendingValidationOrder) > pendingValidationMaxLen {
		oldest := s.pendingValidationOrder[0]
		s.pendingValidationOrder = s.pendingValidationOrder[1:]
		// Silently losing the oldest pending event when the cap is hit
		// means a LedgerAcceptedEvent never fires for that hash even if
		// it later reaches quorum — a failure mode that doesn't exist
		// in rippled. Log via the service's configured logger at warn
		// level so an operator noticing a stuck-validation issue can
		// see it; keep the cap in place so a node that never reaches
		// quorum (bad UNL, partition) can't leak memory.
		if s.logger != nil {
			s.logger.Warn("pendingValidation LRU drop — event lost for this ledger hash",
				"hash", fmt.Sprintf("%x", oldest[:8]),
				"cap", pendingValidationMaxLen,
			)
		}
		delete(s.pendingValidation, oldest)
	}
}

// drainPendingValidationLocked removes and returns the stashed event
// for the given hash, or nil if none exists. Caller must hold s.mu.
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

// pendingValidationEntry records a trusted-validation notification that
// arrived for a ledger sequence not yet present in ledgerHistory. The
// `at` timestamp TTL-guards the entry: if the adopt/close path races
// far enough behind the validation tracker that quorum gossip has gone
// stale, the entry is discarded on drain rather than silently promoting.
type pendingValidationEntry struct {
	expectedHash [32]byte
	at           time.Time
}

// pendingValidationTTL bounds how long a stashed validation is
// considered fresh enough to promote on later adopt/close. The
// 10-minute window covers deep-gap catchup, where backward-chain
// adoption walks one hop per peer round-trip — "validation arrived
// for seq N" to "ledger at seq N adopted" can take several minutes.
// pendingValidationMaxLen=256 already bounds memory and the on-drain
// hash check guarantees fork safety, so a generous TTL is safe.
const pendingValidationTTL = 10 * time.Minute

// stashPendingLedgerValidationLocked stores a (seq, expectedHash, at) entry
// for later drain when ledgerHistory[seq] is populated. LRU-evicts the
// oldest entry if the stash would exceed pendingValidationMaxLen.
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
		// Silently losing the oldest pending validation when the cap is
		// hit means a ledger that later adopts at this seq won't be
		// promoted to validated by this (already-delivered) quorum
		// notification. Log via the service's configured logger at warn
		// level so an operator noticing a stuck-validation issue can see
		// it; keep the cap in place so a node where adoption never
		// catches up (disconnected peer, partition) can't leak memory.
		if s.logger != nil {
			s.logger.Warn("pendingLedgerValidations LRU drop — validation lost for this seq",
				"seq", oldest,
				"cap", pendingValidationMaxLen,
			)
		}
		delete(s.pendingLedgerValidations, oldest)
	}
}

// drainPendingLedgerValidationLocked checks for a stashed validation at
// the given seq and, if present, removes it. If the entry matches the
// adopted hash AND has not exceeded pendingValidationTTL, the adopted
// ledger is promoted to validated and the promotion is reflected in
// s.validatedLedger. Returns true when a promotion occurred so callers
// can log / emit events accordingly. Caller must hold s.mu.
//
// Expired or hash-mismatched entries are always deleted — leaving them
// in place would let a later adopt at the same seq accidentally match
// a stale notification.
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
		// Expired: gossip is too old to trust. A fresh SetValidatedLedger
		// call will re-stash / re-promote if the validation is still
		// current on the trusted-validation tracker's side.
		return false
	}
	if adopted.Hash() != entry.expectedHash {
		// Fork signal: peers validated a different hash at this seq
		// than the one we just adopted. Refuse to promote; the adopted
		// ledger is on the wrong fork from the quorum's perspective.
		return false
	}

	_ = adopted.SetValidated()
	s.validatedLedger = adopted
	s.evictOldHistoryLocked(seq)
	return true
}

// pendingAdopt is the payload of a held replay-delta adoption waiting
// for its parent seq to land. Carries the exact inputs
// AdoptLedgerWithState needs so the cascade can apply the held ledger
// without re-fetching anything.
type pendingAdopt struct {
	header   *header.LedgerHeader
	stateMap *shamap.SHAMap
	txMap    *shamap.SHAMap
	at       time.Time
}

// heldAdoptionTTL bounds how long a held adoption is kept before
// eviction. 5 minutes accommodates a long backward-chain catch-up
// from a divergent local fork — a goxrpl-1 enclave run reproduced
// a wedged node where a 30-ledger fork couldn't recover because
// intermediate held entries TTL-evicted at 60s while the cascade
// was still walking back to a common ancestor. The window is bounded
// to keep a stale fork / disconnected-peer response from lingering
// indefinitely and re-firing against an unrelated adopted ledger.
const heldAdoptionTTL = 5 * time.Minute

// heldAdoptionCascadeMax caps the cascade recursion depth. Real-world
// cascades are 1-2 hops deep (replay-delta is single-ledger-per-
// request). The cap is purely a DoS guard: a malicious peer-stream that
// seeded a deep chain of held orphans pre-adoption would otherwise
// push arbitrary stack depth into the adopt path. 256 is two orders of
// magnitude above any legitimate cascade length.
const heldAdoptionCascadeMax = 256

// evictExpiredHeldAdoptionsLocked removes held entries whose `at`
// timestamp is older than heldAdoptionTTL. Caller must hold s.mu.
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

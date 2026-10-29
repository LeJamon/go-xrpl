package rcl

import "github.com/LeJamon/go-xrpl/internal/consensus"

// Proposals and partial validations steer recovery but do not prove a fork.
func (e *Engine) authenticatedQuorumConflictLocked(ledger consensus.Ledger) (consensus.LedgerID, bool) {
	if ledger == nil || e.validationTracker == nil {
		return consensus.LedgerID{}, false
	}
	snapshot := e.validationTracker.QuorumValidatedLedgers(ledger.Seq())
	return e.authenticatedQuorumConflictForCandidate(snapshot, ledger.ID(), ledger.Seq(), ledger)
}

func (e *Engine) authenticatedQuorumConflict(ledgerID consensus.LedgerID, seq uint32) (consensus.LedgerID, bool) {
	if e.validationTracker == nil {
		return consensus.LedgerID{}, false
	}
	var candidate consensus.Ledger
	if ledgerID != (consensus.LedgerID{}) {
		candidate, _ = e.adaptor.GetLedger(ledgerID)
	}
	snapshot := e.validationTracker.QuorumValidatedLedgers(seq)
	return e.authenticatedQuorumConflictForCandidate(snapshot, ledgerID, seq, candidate)
}

func (e *Engine) authenticatedQuorumConflictForCandidate(
	snapshot consensus.ValidationQuorumSnapshot,
	candidateID consensus.LedgerID,
	seq uint32,
	candidate consensus.Ledger,
) (consensus.LedgerID, bool) {
	for _, id := range snapshot.LedgerIDs {
		if id != candidateID {
			return id, true
		}
	}

	retainedID := snapshot.RetainedLedgerID
	retainedSeq := snapshot.RetainedLedgerSeq
	if retainedSeq == 0 || seq < retainedSeq {
		return consensus.LedgerID{}, false
	}
	if candidateID == (consensus.LedgerID{}) {
		return retainedID, true
	}
	if seq == retainedSeq {
		if candidateID != retainedID {
			return retainedID, true
		}
		return consensus.LedgerID{}, false
	}
	if candidate == nil {
		return consensus.LedgerID{}, false
	}
	hash, ok := e.candidateHashAtSeq(candidate, retainedSeq)
	if ok && hash != retainedID {
		return retainedID, true
	}
	return consensus.LedgerID{}, false
}

// candidateHashAtSeq resolves a candidate's ancestry without requiring a
// service lookup in the publication guard. Production ledgers expose their
// embedded skip-list resolver; its unavailable result is authoritative. Test
// and alternate ledgers without that capability fall back to known parent
// IDs. An unavailable ancestor is deliberately reported as unknown so
// compatibility remains permissive, matching rippled's nullopt behavior.
func (e *Engine) candidateHashAtSeq(candidate consensus.Ledger, targetSeq uint32) (consensus.LedgerID, bool) {
	if candidate == nil || candidate.Seq() < targetSeq {
		return consensus.LedgerID{}, false
	}
	if candidate.Seq() == targetSeq {
		return candidate.ID(), true
	}
	if resolver, ok := candidate.(consensus.LedgerHashOfSeq); ok {
		hash, available, err := resolver.HashOfSeq(targetSeq)
		return hash, err == nil && available
	}

	current := candidate
	for current != nil && current.Seq() > targetSeq {
		parentID := current.ParentID()
		if parentID == (consensus.LedgerID{}) {
			return consensus.LedgerID{}, false
		}
		if current.Seq() == targetSeq+1 {
			return parentID, true
		}
		parent, err := e.adaptor.GetLedger(parentID)
		if err != nil || parent == nil {
			return consensus.LedgerID{}, false
		}
		if parent.ID() != parentID || parent.Seq()+1 != current.Seq() {
			return consensus.LedgerID{}, false
		}
		current = parent
	}
	if current == nil || current.Seq() != targetSeq {
		return consensus.LedgerID{}, false
	}
	return current.ID(), true
}

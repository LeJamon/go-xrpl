package rcl

import "github.com/LeJamon/go-xrpl/internal/consensus"

// Proposals and partial validations steer recovery but do not prove a fork.
func (e *Engine) authenticatedQuorumConflictLocked(ledger consensus.Ledger) (consensus.LedgerID, bool) {
	if ledger == nil {
		return consensus.LedgerID{}, false
	}
	return e.authenticatedQuorumConflict(ledger.ID(), ledger.Seq())
}

func (e *Engine) authenticatedQuorumConflict(ledgerID consensus.LedgerID, seq uint32) (consensus.LedgerID, bool) {
	if e.validationTracker == nil || e.adaptor.IsQuorumUnavailable() {
		return consensus.LedgerID{}, false
	}
	ids, _, available := e.validationTracker.QuorumValidatedLedgers(seq)
	if !available {
		return consensus.LedgerID{}, false
	}
	for _, id := range ids {
		if id != ledgerID {
			return id, true
		}
	}
	return consensus.LedgerID{}, false
}

package rcl

import (
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/ledgertrie"
)

// This file holds ValidationTracker query methods that exist only to
// support tests. Keeping them out of validations.go keeps the production
// API surface limited to what the engine actually calls.

// SetTrusted updates the trusted set without changing the test tracker's
// quorum. Production code uses SetTrustedAndQuorum so the two values always
// install atomically.
func (vt *ValidationTracker) SetTrusted(nodes []consensus.NodeID) {
	vt.mu.Lock()
	vt.setTrustedLocked(nodes)
	vt.mu.Unlock()
	vt.checkAcquired()
}

// SetQuorum updates the quorum requirement for tests. Production code sets
// quorum together with the trusted set via SetTrustedAndQuorum.
func (vt *ValidationTracker) SetQuorum(quorum int) {
	vt.mu.Lock()
	vt.quorum = quorum
	vt.mu.Unlock()
}

// TrustedSupport returns trusted, non-negative-UNL validators supporting the
// ledger or one of its descendants. It mirrors the old test-facing query;
// production callers use the finality and preferred-ledger APIs directly.
func (vt *ValidationTracker) TrustedSupport(ledgerID consensus.LedgerID) int {
	for attempt := 0; attempt < 2; attempt++ {
		vt.checkAcquired()

		vt.mu.RLock()
		trie := vt.trie
		ancestry := vt.ancestry
		vt.mu.RUnlock()

		if trie == nil || ancestry == nil {
			return vt.TrustedValidationCount(ledgerID)
		}

		resolved, ok := resolveAncestry(ancestry, ledgerID, nil)
		if !ok || resolved.retryable {
			return vt.TrustedValidationCount(ledgerID)
		}

		vt.mu.Lock()
		if vt.trie != trie {
			ledgerVals, exists := vt.validations[ledgerID]
			if !exists {
				vt.mu.Unlock()
				return 0
			}
			ledgerVals.touch(vt.now())
			count := vt.countTrustedExcludingNegUNLLocked(ledgerVals.vals)
			vt.mu.Unlock()
			return count
		}
		var support int
		if safeTrieCall("BranchSupport", func() {
			support = vt.branchSupportExcludingNegUNLLocked(resolved.ledger)
		}) {
			vt.resetTrieLocked()
			vt.mu.Unlock()
			continue
		}
		vt.mu.Unlock()
		return support
	}
	return vt.TrustedValidationCount(ledgerID)
}

func (vt *ValidationTracker) branchSupportExcludingNegUNLLocked(lgr ledgertrie.Ledger) int {
	if !isValidAncestryLedger(lgr) {
		panic("invalid target ledger")
	}
	targetSeq := lgr.Seq()
	targetID := lgr.ID()
	count := 0
	for nodeID, tip := range vt.trieTips {
		if vt.negUNL[nodeID] {
			continue
		}
		if !isValidAncestryLedger(tip) {
			panic("invalid trie tip")
		}
		if tip.Seq() >= targetSeq && tip.Ancestor(targetSeq) == targetID {
			count++
		}
	}
	return count
}

// GetValidationCount returns the count of validations for a ledger.
func (vt *ValidationTracker) GetValidationCount(ledgerID consensus.LedgerID) int {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	ledgerVals, exists := vt.validations[ledgerID]
	if !exists {
		return 0
	}
	return len(ledgerVals.vals)
}

// Clear removes all tracked validations.
func (vt *ValidationTracker) Clear() {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	vt.validations = make(map[consensus.LedgerID]*ledgerValidations)
	vt.byNode = make(map[consensus.NodeID]*consensus.Validation)
	vt.fired = make(map[consensus.LedgerID]struct{})
	vt.rebuildTrieLocked()
}

// ValidationStats summarizes tracked validations.
type ValidationStats struct {
	TotalValidations   int
	TrustedValidations int
	ValidatorsActive   int
	LedgersTracked     int
}

// GetStats returns current validation statistics.
func (vt *ValidationTracker) GetStats() ValidationStats {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	totalValidations := 0
	trustedValidations := 0

	for _, ledgerVals := range vt.validations {
		for nodeID := range ledgerVals.vals {
			totalValidations++
			if vt.trusted[nodeID] {
				trustedValidations++
			}
		}
	}

	return ValidationStats{
		TotalValidations:   totalValidations,
		TrustedValidations: trustedValidations,
		ValidatorsActive:   len(vt.byNode),
		LedgersTracked:     len(vt.validations),
	}
}

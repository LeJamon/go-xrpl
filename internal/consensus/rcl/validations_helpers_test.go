package rcl

import (
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/ledgertrie"
)

// This file holds ValidationTracker query methods that exist only to
// support tests. Keeping them out of validations.go keeps the production
// API surface limited to what the engine actually calls.

// setTrusted updates the trusted set without changing the test tracker's
// quorum. Production code uses SetTrustedAndQuorum so the two values always
// install atomically.
func (vt *ValidationTracker) setTrusted(nodes []consensus.NodeID) {
	vt.mu.Lock()
	vt.setTrustedLocked(nodes)
	vt.recheckFinalityLocked()
	vt.mu.Unlock()
	vt.drainFinality()
	vt.checkAcquired()
}

// setQuorum updates the quorum requirement for tests. Production code sets
// quorum together with the trusted set via SetTrustedAndQuorum.
func (vt *ValidationTracker) setQuorum(quorum int) {
	vt.mu.Lock()
	vt.quorum = quorum
	vt.recheckFinalityLocked()
	vt.mu.Unlock()
	vt.drainFinality()
}

// trustedSupport returns trusted, non-negative-UNL validators supporting the
// ledger or one of its descendants. It mirrors the old test-facing query;
// production callers use the finality and preferred-ledger APIs directly.
func (vt *ValidationTracker) trustedSupport(ledgerID consensus.LedgerID) int {
	for attempt := 0; attempt < 2; attempt++ {
		vt.checkAcquired()

		vt.mu.RLock()
		trie := vt.trie
		ancestry := vt.ancestry
		vt.mu.RUnlock()

		if trie == nil || ancestry == nil {
			return vt.trustedValidationCount(ledgerID)
		}

		resolved, ok := resolveAncestry(ancestry, ledgerID, nil)
		if !ok || resolved.retryable {
			return vt.trustedValidationCount(ledgerID)
		}

		vt.mu.Lock()
		if vt.trie != trie {
			ledgerVals, exists := vt.validations[ledgerID]
			if !exists {
				vt.mu.Unlock()
				return 0
			}
			ledgerVals.touch(vt.now())
			count := countTrustedExcludingNegUNLLocked(ledgerVals.vals, vt.trusted, vt.negUNL, nil)
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
	return vt.trustedValidationCount(ledgerID)
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

// getValidationCount returns the count of validations for a ledger.
func (vt *ValidationTracker) getValidationCount(ledgerID consensus.LedgerID) int {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	ledgerVals, exists := vt.validations[ledgerID]
	if !exists {
		return 0
	}
	return len(ledgerVals.vals)
}

// Clear removes all tracked validations.
// validationStats summarizes tracked validations.
type validationStats struct {
	TotalValidations   int
	TrustedValidations int
	ValidatorsActive   int
	LedgersTracked     int
}

// getStats returns current validation statistics.
func (vt *ValidationTracker) getStats() validationStats {
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

	return validationStats{
		TotalValidations:   totalValidations,
		TrustedValidations: trustedValidations,
		ValidatorsActive:   len(vt.byNode),
		LedgersTracked:     len(vt.validations),
	}
}

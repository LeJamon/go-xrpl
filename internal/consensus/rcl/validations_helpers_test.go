package rcl

import "github.com/LeJamon/go-xrpl/internal/consensus"

// This file holds ValidationTracker query methods that exist only to
// support tests. Keeping them out of validations.go keeps the production
// API surface limited to what the engine actually calls.

// GetValidationCount returns the count of validations for a ledger.
func (vt *ValidationTracker) GetValidationCount(ledgerID consensus.LedgerID) int {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	ledgerVals, exists := vt.validations[ledgerID]
	if !exists {
		return 0
	}
	return len(ledgerVals)
}

// GetCurrentValidators returns nodes that have recently validated.
// Uses the injected clock (vt.now) so tests and production share the
// same network-adjusted time source that Add() uses for its isCurrent
// check — keeps "recent" consistent across both reads and writes.
func (vt *ValidationTracker) GetCurrentValidators() []consensus.NodeID {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	cutoff := vt.now().Add(-vt.freshness)
	var result []consensus.NodeID

	for nodeID, v := range vt.byNode {
		if v.SignTime.After(cutoff) {
			result = append(result, nodeID)
		}
	}
	return result
}

// Clear removes all tracked validations.
func (vt *ValidationTracker) Clear() {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	vt.validations = make(map[consensus.LedgerID]map[consensus.NodeID]*consensus.Validation)
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
		for nodeID := range ledgerVals {
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

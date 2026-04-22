package rcl

import (
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// ValidationTracker tracks validations and determines ledger finality.
type ValidationTracker struct {
	mu sync.RWMutex

	// validations maps ledger ID to validations for that ledger
	validations map[consensus.LedgerID]map[consensus.NodeID]*consensus.Validation

	// byNode maps node ID to their latest validation
	byNode map[consensus.NodeID]*consensus.Validation

	// trusted is the set of trusted validators
	trusted map[consensus.NodeID]bool

	// negUNL is the set of validators disabled via the negative-UNL
	// mechanism. They contribute to the UNL size accounting but do NOT
	// count toward the full-validation quorum for a ledger — matching
	// rippled's LedgerMaster.cpp:886,952 where negative-UNL entries
	// are filtered before the quorum comparison.
	negUNL map[consensus.NodeID]bool

	// quorum is the number of validations needed for finality
	quorum int

	// freshness is how long validations are considered fresh
	freshness time.Duration

	// fired records ledgers we've already reported as fully validated,
	// so the callback fires exactly once per ledger even if more
	// validations keep arriving after the threshold is crossed.
	fired map[consensus.LedgerID]struct{}

	// minSeq is the sequence floor for accepting new validations.
	// Validations with LedgerSeq < minSeq are rejected in Add(). The
	// gate prevents a flood of far-stale validations from a broken peer
	// from inflating memory or tripping old-ledger quorum firings.
	// Caller (the engine) advances minSeq as ledgers accept.
	minSeq uint32

	// callbacks
	onFullyValidated func(ledgerID consensus.LedgerID, ledgerSeq uint32)
}

// NewValidationTracker creates a new validation tracker.
func NewValidationTracker(quorum int, freshness time.Duration) *ValidationTracker {
	return &ValidationTracker{
		validations: make(map[consensus.LedgerID]map[consensus.NodeID]*consensus.Validation),
		byNode:      make(map[consensus.NodeID]*consensus.Validation),
		trusted:     make(map[consensus.NodeID]bool),
		negUNL:      make(map[consensus.NodeID]bool),
		quorum:      quorum,
		freshness:   freshness,
		fired:       make(map[consensus.LedgerID]struct{}),
	}
}

// SetTrusted updates the set of trusted validators.
func (vt *ValidationTracker) SetTrusted(nodes []consensus.NodeID) {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	vt.trusted = make(map[consensus.NodeID]bool)
	for _, node := range nodes {
		vt.trusted[node] = true
	}
}

// SetQuorum updates the quorum requirement.
func (vt *ValidationTracker) SetQuorum(quorum int) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	vt.quorum = quorum
}

// SetNegativeUNL replaces the current negative-UNL set. Validators on
// the negative-UNL are still considered trusted for message acceptance
// but are excluded from the quorum count in checkFullValidation —
// matching rippled's behavior of disabling temporarily-offline
// validators without removing them from the config. Pass nil or an
// empty slice to clear the negUNL.
func (vt *ValidationTracker) SetNegativeUNL(nodes []consensus.NodeID) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	vt.negUNL = make(map[consensus.NodeID]bool, len(nodes))
	for _, n := range nodes {
		vt.negUNL[n] = true
	}
}

// SetMinSeq advances the sequence floor below which incoming
// validations are rejected. Called by the engine after a ledger is
// accepted to discard far-stale validations without holding them in
// memory. Passing a value <= current minSeq is a no-op.
func (vt *ValidationTracker) SetMinSeq(seq uint32) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	if seq > vt.minSeq {
		vt.minSeq = seq
	}
}

// SetFullyValidatedCallback sets the callback for when a ledger is fully validated.
// Fired once per ledger the first time trusted-validation count crosses the quorum
// threshold. Seq is passed alongside the ledger ID so the callee can look up or
// stamp the ledger without a secondary map lookup.
func (vt *ValidationTracker) SetFullyValidatedCallback(fn func(ledgerID consensus.LedgerID, ledgerSeq uint32)) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	vt.onFullyValidated = fn
}

// Add adds a validation to the tracker.
// Returns true if this is a new validation (not duplicate).
//
// Inbound filters match rippled's LedgerMaster.cpp:886,952:
//   - Only Full validations count toward quorum. Partial validations
//     (Full=false) indicate a node that hasn't applied the ledger
//     yet and can't attest to its state root. We drop them entirely.
//   - Validations with seq below minSeq are rejected. Once a ledger
//     accepts, validations for seqs many rounds back are noise that
//     can never retroactively become quorum; keeping them in memory
//     wastes work on every checkFullValidation pass.
//   - Per-node newer-seq-only rule: a node's latest validation
//     supersedes any earlier one. Same as before.
func (vt *ValidationTracker) Add(validation *consensus.Validation) bool {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	// Reject partial validations — a node signaling Full=false has not
	// fully applied the ledger and its signature doesn't anchor the
	// state root. Rippled's LedgerMaster.cpp:886 filters the same way.
	if !validation.Full {
		return false
	}

	// Reject far-stale validations below the sequence floor.
	if vt.minSeq > 0 && validation.LedgerSeq < vt.minSeq {
		return false
	}

	// Check if this is a newer validation from this node
	existing, hasExisting := vt.byNode[validation.NodeID]
	if hasExisting {
		if validation.LedgerSeq <= existing.LedgerSeq {
			return false // Not newer, ignore
		}
	}

	// Update by-node tracking
	vt.byNode[validation.NodeID] = validation

	// Add to ledger validations
	ledgerVals, exists := vt.validations[validation.LedgerID]
	if !exists {
		ledgerVals = make(map[consensus.NodeID]*consensus.Validation)
		vt.validations[validation.LedgerID] = ledgerVals
	}
	ledgerVals[validation.NodeID] = validation

	// Check for full validation
	vt.checkFullValidation(validation.LedgerID)

	return true
}

// checkFullValidation checks if a ledger has reached full validation.
// Fires the callback exactly once per ledger — the first time trusted
// count crosses the quorum threshold. Subsequent adds for the same
// ledger are ignored to avoid repeatedly flipping server_info's
// validated_ledger on every late-arriving peer validation.
//
// Zero-quorum edge case (empty UNL): requires at least one tracked
// validation for the ledger before firing, so we don't spuriously
// promote a ledger hash we haven't seen any validator sign.
//
// Negative-UNL filter: a validator on the negUNL is trusted for
// message acceptance but excluded from the quorum count here, matching
// rippled's LedgerMaster.cpp:952. Same-quorum with a validator
// temporarily disabled shouldn't require one MORE validation to finalize.
func (vt *ValidationTracker) checkFullValidation(ledgerID consensus.LedgerID) {
	if vt.onFullyValidated == nil {
		return
	}
	if _, done := vt.fired[ledgerID]; done {
		return
	}
	ledgerVals, exists := vt.validations[ledgerID]
	if !exists || len(ledgerVals) == 0 {
		return
	}

	trustedCount := 0
	var sampleSeq uint32
	for nodeID, v := range ledgerVals {
		if sampleSeq == 0 {
			sampleSeq = v.LedgerSeq
		}
		if vt.trusted[nodeID] && !vt.negUNL[nodeID] {
			trustedCount++
		}
	}

	if trustedCount >= vt.quorum {
		vt.fired[ledgerID] = struct{}{}
		vt.onFullyValidated(ledgerID, sampleSeq)
	}
}

// GetValidations returns all validations for a ledger.
func (vt *ValidationTracker) GetValidations(ledgerID consensus.LedgerID) []*consensus.Validation {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	ledgerVals, exists := vt.validations[ledgerID]
	if !exists {
		return nil
	}

	result := make([]*consensus.Validation, 0, len(ledgerVals))
	for _, v := range ledgerVals {
		result = append(result, v)
	}
	return result
}

// GetTrustedValidations returns trusted validations for a ledger.
func (vt *ValidationTracker) GetTrustedValidations(ledgerID consensus.LedgerID) []*consensus.Validation {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	ledgerVals, exists := vt.validations[ledgerID]
	if !exists {
		return nil
	}

	var result []*consensus.Validation
	for nodeID, v := range ledgerVals {
		if vt.trusted[nodeID] {
			result = append(result, v)
		}
	}
	return result
}

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

// GetTrustedValidationCount returns the count of trusted validations.
func (vt *ValidationTracker) GetTrustedValidationCount(ledgerID consensus.LedgerID) int {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	ledgerVals, exists := vt.validations[ledgerID]
	if !exists {
		return 0
	}

	count := 0
	for nodeID := range ledgerVals {
		if vt.trusted[nodeID] {
			count++
		}
	}
	return count
}

// GetTrustedSupport is an alias for GetTrustedValidationCount named for
// the LedgerTrie-style preference heuristic used in checkLedger. Rippled
// picks a network ledger via vals.getPreferred() (RCLConsensus.cpp:301)
// which returns the ledger with the most validation SUPPORT on its
// ancestor chain — we approximate "support" with a flat trusted-count
// at the exact ledger ID. Good enough when trusted validators broadly
// agree; a full LedgerTrie port is a follow-up item.
func (vt *ValidationTracker) GetTrustedSupport(ledgerID consensus.LedgerID) int {
	return vt.GetTrustedValidationCount(ledgerID)
}

// IsFullyValidated returns true if the ledger has reached full validation.
func (vt *ValidationTracker) IsFullyValidated(ledgerID consensus.LedgerID) bool {
	return vt.GetTrustedValidationCount(ledgerID) >= vt.quorum
}

// GetLatestValidation returns the latest validation from a node.
func (vt *ValidationTracker) GetLatestValidation(nodeID consensus.NodeID) *consensus.Validation {
	vt.mu.RLock()
	defer vt.mu.RUnlock()
	return vt.byNode[nodeID]
}

// GetCurrentValidators returns nodes that have recently validated.
func (vt *ValidationTracker) GetCurrentValidators() []consensus.NodeID {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	cutoff := time.Now().Add(-vt.freshness)
	var result []consensus.NodeID

	for nodeID, v := range vt.byNode {
		if v.SignTime.After(cutoff) {
			result = append(result, nodeID)
		}
	}
	return result
}

// ExpireOld removes old validations.
func (vt *ValidationTracker) ExpireOld(minSeq uint32) {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	for ledgerID, ledgerVals := range vt.validations {
		for _, v := range ledgerVals {
			if v.LedgerSeq < minSeq {
				delete(vt.validations, ledgerID)
				delete(vt.fired, ledgerID)
			}
			break
		}
	}
}

// Clear removes all tracked validations.
func (vt *ValidationTracker) Clear() {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	vt.validations = make(map[consensus.LedgerID]map[consensus.NodeID]*consensus.Validation)
	vt.byNode = make(map[consensus.NodeID]*consensus.Validation)
	vt.fired = make(map[consensus.LedgerID]struct{})
}

// Stats returns statistics about tracked validations.
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

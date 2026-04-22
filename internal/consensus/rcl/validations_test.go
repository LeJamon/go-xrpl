package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

func TestValidationTracker_Add(t *testing.T) {
	vt := NewValidationTracker(3, 5*time.Minute)

	node1 := consensus.NodeID{1}
	node2 := consensus.NodeID{2}
	ledger1 := consensus.LedgerID{1}

	v1 := &consensus.Validation{
		LedgerID:  ledger1,
		LedgerSeq: 100,
		NodeID:    node1,
		SignTime:  time.Now(),
		Full:      true,
	}

	v2 := &consensus.Validation{
		LedgerID:  ledger1,
		LedgerSeq: 100,
		NodeID:    node2,
		SignTime:  time.Now(),
		Full:      true,
	}

	// Add first validation
	if !vt.Add(v1) {
		t.Error("First validation should be added")
	}

	// Add second validation
	if !vt.Add(v2) {
		t.Error("Second validation should be added")
	}

	// Count should be 2
	if vt.GetValidationCount(ledger1) != 2 {
		t.Errorf("Expected 2 validations, got %d", vt.GetValidationCount(ledger1))
	}

	// Adding same validation should return false
	if vt.Add(v1) {
		t.Error("Duplicate validation should not be added")
	}
}

func TestValidationTracker_TrustedValidations(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)

	node1 := consensus.NodeID{1}
	node2 := consensus.NodeID{2}
	node3 := consensus.NodeID{3}
	ledger1 := consensus.LedgerID{1}

	// Set trusted nodes
	vt.SetTrusted([]consensus.NodeID{node1, node2})

	// Add validations
	vt.Add(&consensus.Validation{LedgerID: ledger1, LedgerSeq: 100, NodeID: node1, SignTime: time.Now(), Full: true})
	vt.Add(&consensus.Validation{LedgerID: ledger1, LedgerSeq: 100, NodeID: node2, SignTime: time.Now(), Full: true})
	vt.Add(&consensus.Validation{LedgerID: ledger1, LedgerSeq: 100, NodeID: node3, SignTime: time.Now(), Full: true})

	// Total should be 3
	if vt.GetValidationCount(ledger1) != 3 {
		t.Errorf("Expected 3 total validations, got %d", vt.GetValidationCount(ledger1))
	}

	// Trusted should be 2
	if vt.GetTrustedValidationCount(ledger1) != 2 {
		t.Errorf("Expected 2 trusted validations, got %d", vt.GetTrustedValidationCount(ledger1))
	}
}

func TestValidationTracker_FullyValidated(t *testing.T) {
	quorum := 3
	vt := NewValidationTracker(quorum, 5*time.Minute)

	nodes := []consensus.NodeID{{1}, {2}, {3}, {4}}
	vt.SetTrusted(nodes)

	ledger1 := consensus.LedgerID{1}
	var fullyValidatedLedger consensus.LedgerID
	var fullyValidatedSeq uint32
	var fireCount int

	vt.SetFullyValidatedCallback(func(id consensus.LedgerID, seq uint32) {
		fullyValidatedLedger = id
		fullyValidatedSeq = seq
		fireCount++
	})

	// Add validations one by one
	for i := 0; i < quorum-1; i++ {
		vt.Add(&consensus.Validation{
			LedgerID:  ledger1,
			LedgerSeq: 100,
			NodeID:    nodes[i],
			SignTime:  time.Now(),
			Full:      true,
		})
	}

	// Should not be fully validated yet
	if vt.IsFullyValidated(ledger1) {
		t.Error("Should not be fully validated with less than quorum")
	}

	// Add one more to reach quorum
	vt.Add(&consensus.Validation{
		LedgerID:  ledger1,
		LedgerSeq: 100,
		NodeID:    nodes[quorum-1],
		SignTime:  time.Now(),
		Full:      true,
	})

	// Should be fully validated now
	if !vt.IsFullyValidated(ledger1) {
		t.Error("Should be fully validated with quorum")
	}

	// Callback should have been called with the right ledger + seq
	if fullyValidatedLedger != ledger1 {
		t.Error("Fully validated callback should have been called")
	}
	if fullyValidatedSeq != 100 {
		t.Errorf("Expected callback seq 100, got %d", fullyValidatedSeq)
	}
	if fireCount != 1 {
		t.Errorf("Callback must fire exactly once on threshold crossing, got %d", fireCount)
	}

	// Additional validations past the threshold must NOT re-fire the callback.
	extraNode := nodes[quorum]
	vt.Add(&consensus.Validation{
		LedgerID:  ledger1,
		LedgerSeq: 100,
		NodeID:    extraNode,
		SignTime:  time.Now(),
		Full:      true,
	})
	if fireCount != 1 {
		t.Errorf("Callback must be idempotent, re-fired (count=%d)", fireCount)
	}
}

func TestValidationTracker_NewerValidation(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)

	node1 := consensus.NodeID{1}
	ledger1 := consensus.LedgerID{1}
	ledger2 := consensus.LedgerID{2}

	// Add validation for ledger 1
	vt.Add(&consensus.Validation{
		LedgerID:  ledger1,
		LedgerSeq: 100,
		NodeID:    node1,
		SignTime:  time.Now(),
		Full:      true,
	})

	// Add newer validation for ledger 2
	if !vt.Add(&consensus.Validation{
		LedgerID:  ledger2,
		LedgerSeq: 101,
		NodeID:    node1,
		SignTime:  time.Now(),
		Full:      true,
	}) {
		t.Error("Newer validation should be added")
	}

	// Latest validation should be for ledger 2
	latest := vt.GetLatestValidation(node1)
	if latest.LedgerID != ledger2 {
		t.Error("Latest validation should be for ledger 2")
	}

	// Old validation should not be added
	if vt.Add(&consensus.Validation{
		LedgerID:  ledger1,
		LedgerSeq: 100,
		NodeID:    node1,
		SignTime:  time.Now(),
		Full:      true,
	}) {
		t.Error("Older validation should not be added")
	}
}

func TestValidationTracker_Stats(t *testing.T) {
	vt := NewValidationTracker(2, 5*time.Minute)

	nodes := []consensus.NodeID{{1}, {2}, {3}}
	vt.SetTrusted(nodes[:2])

	ledger1 := consensus.LedgerID{1}
	ledger2 := consensus.LedgerID{2}

	// Add validations
	vt.Add(&consensus.Validation{LedgerID: ledger1, LedgerSeq: 100, NodeID: nodes[0], SignTime: time.Now(), Full: true})
	vt.Add(&consensus.Validation{LedgerID: ledger1, LedgerSeq: 100, NodeID: nodes[1], SignTime: time.Now(), Full: true})
	vt.Add(&consensus.Validation{LedgerID: ledger2, LedgerSeq: 101, NodeID: nodes[2], SignTime: time.Now(), Full: true})

	stats := vt.GetStats()

	if stats.TotalValidations != 3 {
		t.Errorf("Expected 3 total validations, got %d", stats.TotalValidations)
	}

	if stats.TrustedValidations != 2 {
		t.Errorf("Expected 2 trusted validations, got %d", stats.TrustedValidations)
	}

	if stats.ValidatorsActive != 3 {
		t.Errorf("Expected 3 active validators, got %d", stats.ValidatorsActive)
	}

	if stats.LedgersTracked != 2 {
		t.Errorf("Expected 2 ledgers tracked, got %d", stats.LedgersTracked)
	}
}

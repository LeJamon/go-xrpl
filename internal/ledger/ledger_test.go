package ledger

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
)

// newParentAt builds a closed parent ledger with the requested
// (seq, resolution, closeAgree) — synthesized from genesis and a
// chain of no-op Close()s so we get valid hashes/maps for free.
// Returns a closed parent ready to be passed into NewOpen.
//
// The seq argument is the parent's LedgerIndex. Because rippled's
// bin-step predicate fires on the CHILD's seq (child = parent+1),
// the parent seq must be chosen so parent+1 matches the desired
// step boundary.
func newParentAt(t *testing.T, parentSeq uint32, resolution uint32, closeAgree bool) *Ledger {
	t.Helper()

	res, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("genesis.Create: %v", err)
	}
	parent := FromGenesis(res.Header, res.StateMap, res.TxMap, drops.Fees{})

	// Walk forward until we hit parentSeq. Genesis is seq 1.
	for parent.Sequence() < parentSeq {
		child, err := NewOpen(parent, parent.CloseTime().Add(10*time.Second))
		if err != nil {
			t.Fatalf("NewOpen at seq %d: %v", parent.Sequence()+1, err)
		}
		if err := child.Close(parent.CloseTime().Add(10*time.Second), 0); err != nil {
			t.Fatalf("Close at seq %d: %v", child.Sequence(), err)
		}
		parent = child
	}

	// Stamp the header fields the test actually cares about. We do
	// this directly rather than re-closing because we need a
	// specific resolution + closeAgree pair, and the bin-step
	// algorithm would otherwise interfere.
	h := parent.header
	h.CloseTimeResolution = resolution
	if closeAgree {
		h.CloseFlags &^= header.LCFNoConsensusTime
	} else {
		h.CloseFlags |= header.LCFNoConsensusTime
	}
	parent.header = h
	return parent
}

// TestLedger_Close_UsesDynamicResolution exercises the primary
// integration path: when a child ledger is opened on a parent at
// resolution=30s with previousAgree=true and the child's seq lands
// on the increase-every boundary (seq%8 == 0), the child's header
// must bind to the FINER bin (20s), not inherit 30s statically.
//
// Before task 3.1, NewOpen copied parent.header.CloseTimeResolution
// verbatim — this test would have observed 30s.
func TestLedger_Close_UsesDynamicResolution(t *testing.T) {
	// parent at seq=7 so child=8 (seq % 8 == 0).
	parent := newParentAt(t, 7, 30, true)

	closeTime := parent.CloseTime().Add(10 * time.Second)
	child, err := NewOpen(parent, closeTime)
	if err != nil {
		t.Fatalf("NewOpen: %v", err)
	}

	if got, want := child.Sequence(), uint32(8); got != want {
		t.Fatalf("child seq: got %d want %d", got, want)
	}
	if got, want := child.CloseTimeResolution(), uint32(20); got != want {
		t.Errorf("child CloseTimeResolution: got %d want %d (expected step 30→20 at seq=8 agree=true)", got, want)
	}
}

// TestLedger_Close_DynamicResolution_Disagree verifies the
// symmetric path: when the parent's CloseFlags carry
// sLCF_NoConsensusTime (previousAgree=false) and the child seq is on
// the decrease-every boundary (always, since seqMod 1 == 0), the
// child must step COARSER.
func TestLedger_Close_DynamicResolution_Disagree(t *testing.T) {
	// parent seq=4, previousAgree=false → child seq=5 steps coarser.
	parent := newParentAt(t, 4, 30, false)

	child, err := NewOpen(parent, parent.CloseTime().Add(10*time.Second))
	if err != nil {
		t.Fatalf("NewOpen: %v", err)
	}
	if got, want := child.CloseTimeResolution(), uint32(60); got != want {
		t.Errorf("disagree child resolution: got %d want %d", got, want)
	}
}

// TestLedger_Close_DynamicResolution_NoStep covers the "stay" case:
// parent resolution is preserved on rounds where neither predicate
// fires (previousAgree=true but seq is not on the increase-every
// boundary).
func TestLedger_Close_DynamicResolution_NoStep(t *testing.T) {
	// parent seq=6 → child seq=7; 7 % 8 != 0; agree=true;
	// expect no step.
	parent := newParentAt(t, 6, 30, true)

	child, err := NewOpen(parent, parent.CloseTime().Add(10*time.Second))
	if err != nil {
		t.Fatalf("NewOpen: %v", err)
	}
	if got, want := child.CloseTimeResolution(), uint32(30); got != want {
		t.Errorf("no-step child resolution: got %d want %d", got, want)
	}
}

// TestLedger_Close_DynamicResolution_AgreeSaturatedAtFinest covers
// the saturation edge: parent already at 10s (finest), agree=true,
// seq=8. rippled stops stepping at the begin() of the bin array, so
// the child resolution should remain 10s.
func TestLedger_Close_DynamicResolution_AgreeSaturatedAtFinest(t *testing.T) {
	parent := newParentAt(t, 7, 10, true)

	child, err := NewOpen(parent, parent.CloseTime().Add(10*time.Second))
	if err != nil {
		t.Fatalf("NewOpen: %v", err)
	}
	if got, want := child.CloseTimeResolution(), uint32(10); got != want {
		t.Errorf("saturated finest: got %d want %d", got, want)
	}
}

// TestLedger_Close_DynamicResolution_DisagreeSaturatedAtCoarsest
// covers the symmetric edge: parent at 120s (coarsest), disagree,
// any seq → no coarser step available.
func TestLedger_Close_DynamicResolution_DisagreeSaturatedAtCoarsest(t *testing.T) {
	parent := newParentAt(t, 3, 120, false)

	child, err := NewOpen(parent, parent.CloseTime().Add(10*time.Second))
	if err != nil {
		t.Fatalf("NewOpen: %v", err)
	}
	if got, want := child.CloseTimeResolution(), uint32(120); got != want {
		t.Errorf("saturated coarsest: got %d want %d", got, want)
	}
}

package service

import "testing"

// newSeededService returns a started service whose in-memory history is exactly
// [lo, hi], with the validated tip pinned to validated.
func newSeededService(t *testing.T, lo, hi, validated uint32) *Service {
	t.Helper()
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	seedHistory(t, svc, lo, hi)
	svc.validatedLedger = svc.ledgerHistory[validated]
	if svc.validatedLedger == nil {
		t.Fatalf("validated seq %d not in seeded history [%d,%d]", validated, lo, hi)
	}
	return svc
}

// TestAdvertisedLedgerRange_NoValidated verifies that with no validated ledger
// the node advertises an empty range instead of claiming the whole chain.
func TestAdvertisedLedgerRange_NoValidated(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	seedHistory(t, svc, 10, 100)
	svc.validatedLedger = nil

	if first, last, ok := svc.AdvertisedLedgerRange(); ok || first != 0 || last != 0 {
		t.Fatalf("AdvertisedLedgerRange = (%d, %d, %v), want (0, 0, false)", first, last, ok)
	}
}

// TestAdvertisedLedgerRange_HistoryToValidatedTip verifies the low end comes
// from retained history and the high end from the validated tip, not the
// broader in-memory window.
func TestAdvertisedLedgerRange_HistoryToValidatedTip(t *testing.T) {
	// History runs to 100 but only 80 is validated: last must be 80, not 100.
	svc := newSeededService(t, 10, 100, 80)

	first, last, ok := svc.AdvertisedLedgerRange()
	if !ok || first != 10 || last != 80 {
		t.Fatalf("AdvertisedLedgerRange = (%d, %d, %v), want (10, 80, true)", first, last, ok)
	}
}

// TestAdvertisedLedgerRange_ClampedToFloor verifies the online-delete floor
// raises the advertised low end to the earliest durably held ledger.
func TestAdvertisedLedgerRange_ClampedToFloor(t *testing.T) {
	svc := newSeededService(t, 10, 100, 100)

	// No floor: full retained range up to the validated tip.
	if first, last, ok := svc.AdvertisedLedgerRange(); !ok || first != 10 || last != 100 {
		t.Fatalf("unclamped = (%d, %d, %v), want (10, 100, true)", first, last, ok)
	}

	// Floor at 50 raises the low end; the tip is unchanged.
	svc.SetMinimumOnlineFunc(func() uint32 { return 50 })
	if first, last, ok := svc.AdvertisedLedgerRange(); !ok || first != 50 || last != 100 {
		t.Fatalf("clamped = (%d, %d, %v), want (50, 100, true)", first, last, ok)
	}

	// A floor below the window (or zero, no rotation yet) leaves it untouched.
	svc.SetMinimumOnlineFunc(func() uint32 { return 5 })
	if first, last, ok := svc.AdvertisedLedgerRange(); !ok || first != 10 || last != 100 {
		t.Fatalf("floor below window = (%d, %d, %v), want (10, 100, true)", first, last, ok)
	}
	svc.SetMinimumOnlineFunc(func() uint32 { return 0 })
	if first, last, ok := svc.AdvertisedLedgerRange(); !ok || first != 10 || last != 100 {
		t.Fatalf("zero floor = (%d, %d, %v), want (10, 100, true)", first, last, ok)
	}
}

// TestAdvertisedLedgerRange_FloorAboveTip verifies that when the floor exceeds
// the validated tip nothing durable remains, so the range is empty rather than
// inverted.
func TestAdvertisedLedgerRange_FloorAboveTip(t *testing.T) {
	svc := newSeededService(t, 10, 100, 100)
	svc.SetMinimumOnlineFunc(func() uint32 { return 200 })

	if first, last, ok := svc.AdvertisedLedgerRange(); ok || first != 0 || last != 0 {
		t.Fatalf("AdvertisedLedgerRange = (%d, %d, %v), want (0, 0, false)", first, last, ok)
	}
}

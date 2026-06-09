package feetrack

import (
	"testing"
)

// TestScaleFeeLoad_Identity: with a fresh tracker the factor is LoadBase,
// so ScaleFeeLoad returns the input fee unchanged (including the 0
// short-circuit).
func TestScaleFeeLoad_Identity(t *testing.T) {
	tr := New()
	cases := []uint64{0, 1, 10, 10000, 1<<32 + 7}
	for _, fee := range cases {
		got, err := ScaleFeeLoad(fee, tr, false)
		if err != nil {
			t.Fatalf("ScaleFeeLoad(%d) err: %v", fee, err)
		}
		if got != fee {
			t.Fatalf("ScaleFeeLoad(%d) = %d; want identity", fee, got)
		}
	}
}

// TestScaleFeeLoad_NilTracker keeps fee handling safe when no tracker
// is wired (services starting up, simulate paths during construction).
func TestScaleFeeLoad_NilTracker(t *testing.T) {
	got, err := ScaleFeeLoad(123, nil, false)
	if err != nil || got != 123 {
		t.Fatalf("nil tracker = (%d,%v); want (123,nil)", got, err)
	}
}

// TestRaiseLowerLocalFee pins the hysteresis: the first raise only arms
// raiseCount, the second actually bumps the factor; lower decays back to
// LoadBase and clears raiseCount.
func TestRaiseLowerLocalFee(t *testing.T) {
	tr := New()
	if changed := tr.RaiseLocalFee(); changed {
		t.Fatal("first raise must not change fee yet (raiseCount latch)")
	}
	if tr.GetLocalFee() != LoadBase {
		t.Fatalf("local fee after first raise = %d; want %d", tr.GetLocalFee(), LoadBase)
	}
	if changed := tr.RaiseLocalFee(); !changed {
		t.Fatal("second raise must lift local fee above LoadBase")
	}
	want := LoadBase + LoadBase/FeeIncFraction
	if tr.GetLocalFee() != want {
		t.Fatalf("local fee after second raise = %d; want %d", tr.GetLocalFee(), want)
	}
	if !tr.IsLoadedLocal() {
		t.Fatal("IsLoadedLocal must be true once localFee != LoadBase")
	}

	// Drive it back down. LowerLocalFee should clear the raise count
	// and decay; running enough cycles must clamp at LoadBase, not
	// below it.
	for range 10 {
		tr.LowerLocalFee()
	}
	if tr.GetLocalFee() != LoadBase {
		t.Fatalf("local fee after lower cycles = %d; want %d", tr.GetLocalFee(), LoadBase)
	}
	if tr.IsLoadedLocal() {
		t.Fatal("IsLoadedLocal must be false once fee returns to LoadBase")
	}
}

// TestScaleFeeLoad_Loaded checks scaling under a raised local fee.
// After two raises, local = LoadBase + LoadBase/4 = 320; ScaleFeeLoad
// applies fee * 320 / 256 = fee * 5/4.
func TestScaleFeeLoad_Loaded(t *testing.T) {
	tr := New()
	tr.RaiseLocalFee()
	tr.RaiseLocalFee()
	got, err := ScaleFeeLoad(1000, tr, false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1250 {
		t.Fatalf("ScaleFeeLoad(1000) under 5/4 load = %d; want 1250", got)
	}
}

// TestScaleFeeLoad_UnlimitedBranch pins the unlimited carve-out: when
// only local load is elevated and stays under 4x remote, an unlimited
// caller pays the remote-rate factor.
func TestScaleFeeLoad_UnlimitedBranch(t *testing.T) {
	tr := New()
	tr.RaiseLocalFee()
	tr.RaiseLocalFee() // local = 320, remote = 256
	got, err := ScaleFeeLoad(1000, tr, true)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1000 {
		t.Fatalf("unlimited caller under moderate local load = %d; want identity 1000", got)
	}

	// Drive local above 4x remote. Each raise multiplies by 5/4, so
	// after >=7 raises beyond the latch local >= 4*remote and the
	// privileged carve-out drops away.
	for range 8 {
		tr.RaiseLocalFee()
	}
	if tr.GetLocalFee() < 4*tr.GetRemoteFee() {
		t.Fatalf("setup failed: local %d not >= 4*remote %d", tr.GetLocalFee(), tr.GetRemoteFee())
	}
	got, err = ScaleFeeLoad(1000, tr, true)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got <= 1000 {
		t.Fatalf("unlimited caller beyond 4x remote should pay scaled fee, got %d", got)
	}
}

// TestScaleFeeLoad_Overflow protects the multiplication: a huge fee
// times a large factor must surface ErrOverflow rather than silently
// truncate.
func TestScaleFeeLoad_Overflow(t *testing.T) {
	tr := New()
	for range 80 {
		tr.RaiseLocalFee()
	}
	_, err := ScaleFeeLoad(^uint64(0), tr, false)
	if err == nil {
		t.Fatal("expected ErrOverflow on max-fee max-factor; got nil")
	}
}

// TestLoadFactorAggregates pins max(cluster, local, remote) and the
// (max(local,remote), max(remote,cluster)) pair consumed by ScaleFeeLoad.
func TestLoadFactorAggregates(t *testing.T) {
	tr := New()
	tr.SetRemoteFee(400)
	tr.SetClusterFee(300)
	tr.RaiseLocalFee()
	tr.RaiseLocalFee() // local = max(local, remote=400) * 5/4 = 500

	if tr.GetLocalFee() != 500 {
		t.Fatalf("local after raise with remote=400: got %d, want 500", tr.GetLocalFee())
	}
	if lf := tr.GetLoadFactor(); lf != 500 {
		t.Fatalf("load factor = %d; want max(cluster=300, local=500, remote=400) = 500", lf)
	}
	feeFactor, remFee := tr.GetScalingFactors()
	if feeFactor != 500 || remFee != 400 {
		t.Fatalf("scaling factors = (%d,%d); want (500,400)", feeFactor, remFee)
	}
}

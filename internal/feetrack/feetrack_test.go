package feetrack

import (
	"errors"
	"math"
	"testing"
)

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

func TestScaleFeeLoad_NilTracker(t *testing.T) {
	got, err := ScaleFeeLoad(123, nil, false)
	if err != nil || got != 123 {
		t.Fatalf("nil tracker = (%d,%v); want (123,nil)", got, err)
	}
	if _, err := ScaleFeeLoad(uint64(math.MaxInt64)+1, nil, false); !errors.Is(err, ErrOverflow) {
		t.Fatalf("nil tracker MaxInt64+1 error = %v, want ErrOverflow", err)
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
	if tr.LocalFee() != LoadBase {
		t.Fatalf("local fee after first raise = %d; want %d", tr.LocalFee(), LoadBase)
	}
	if changed := tr.RaiseLocalFee(); !changed {
		t.Fatal("second raise must lift local fee above LoadBase")
	}
	want := LoadBase + LoadBase/feeIncFraction
	if tr.LocalFee() != want {
		t.Fatalf("local fee after second raise = %d; want %d", tr.LocalFee(), want)
	}
	if !tr.IsLoadedLocal() {
		t.Fatal("IsLoadedLocal must be true once localFee != LoadBase")
	}

	// Repeated decay must clamp at LoadBase.
	for range 10 {
		tr.LowerLocalFee()
	}
	if tr.LocalFee() != LoadBase {
		t.Fatalf("local fee after lower cycles = %d; want %d", tr.LocalFee(), LoadBase)
	}
	if tr.IsLoadedLocal() {
		t.Fatal("IsLoadedLocal must be false once fee returns to LoadBase")
	}
}

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
	got, err = ScaleFeeLoad(1, tr, false)
	if err != nil {
		t.Fatalf("ScaleFeeLoad truncation: %v", err)
	}
	if got != 1 {
		t.Fatalf("truncated scaled fee = %d; want 1", got)
	}
}

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

	// The privileged carve-out ends at four times the remote factor.
	for range 8 {
		tr.RaiseLocalFee()
	}
	if tr.LocalFee() < 4*tr.RemoteFee() {
		t.Fatalf("setup failed: local %d not >= 4*remote %d", tr.LocalFee(), tr.RemoteFee())
	}
	got, err = ScaleFeeLoad(1000, tr, true)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got <= 1000 {
		t.Fatalf("unlimited caller beyond 4x remote should pay scaled fee, got %d", got)
	}
}

func TestScaleFeeLoad_Overflow(t *testing.T) {
	tr := New()
	for range 80 {
		tr.RaiseLocalFee()
	}
	_, err := ScaleFeeLoad(^uint64(0), tr, false)
	if !errors.Is(err, ErrOverflow) {
		t.Fatalf("max-fee max-factor error = %v, want ErrOverflow", err)
	}
}

func TestScaleFeeLoadSignedBoundary(t *testing.T) {
	tr := New()

	got, err := ScaleFeeLoad(math.MaxInt64, tr, false)
	if err != nil || got != math.MaxInt64 {
		t.Fatalf("MaxInt64 scale = (%d, %v), want (%d, nil)", got, err, uint64(math.MaxInt64))
	}
	if _, err := ScaleFeeLoad(uint64(math.MaxInt64)+1, tr, false); !errors.Is(err, ErrOverflow) {
		t.Fatalf("MaxInt64+1 error = %v, want ErrOverflow", err)
	}

	tr.SetRemoteFee(512)
	fee := uint64(math.MaxInt64 / 2)
	got, err = ScaleFeeLoad(fee, tr, false)
	if err != nil || got != uint64(math.MaxInt64)-1 {
		t.Fatalf("wide product scale = (%d, %v), want (%d, nil)", got, err, uint64(math.MaxInt64)-1)
	}

	tr.SetRemoteFee(feeMax)
	if _, err := ScaleFeeLoad(10_000_000_000_000, tr, false); !errors.Is(err, ErrOverflow) {
		t.Fatalf("signed XRP overflow error = %v, want ErrOverflow", err)
	}
}

func TestScaleFeeLoadAllocations(t *testing.T) {
	tr := New()
	tr.SetRemoteFee(512)
	var (
		got uint64
		err error
	)
	allocs := testing.AllocsPerRun(1000, func() {
		got, err = ScaleFeeLoad(1_000_000, tr, false)
	})
	if err != nil || got != 2_000_000 {
		t.Fatalf("scale = (%d, %v), want (2000000, nil)", got, err)
	}
	if allocs != 0 {
		t.Fatalf("ScaleFeeLoad allocations = %v, want 0", allocs)
	}
}

func TestIsLoadedCluster(t *testing.T) {
	tr := New()
	if tr.IsLoadedCluster() {
		t.Fatal("fresh tracker must not be cluster-loaded")
	}

	tr.RaiseLocalFee()
	if !tr.IsLoadedCluster() {
		t.Fatal("armed first raise must report cluster load")
	}
	tr.LowerLocalFee()
	if tr.IsLoadedCluster() {
		t.Fatal("lowering the armed tracker must clear cluster load")
	}

	tr.SetClusterFee(LoadBase + 1)
	if !tr.IsLoadedCluster() {
		t.Fatal("cluster-only fee divergence must report cluster load")
	}
	tr.SetClusterFee(LoadBase)
	if tr.IsLoadedCluster() {
		t.Fatal("normal cluster fee must clear cluster load")
	}
	tr.SetClusterFee(0)
	if !tr.IsLoadedCluster() {
		t.Fatal("zero cluster fee is distinct from the normal factor")
	}
}

func TestRaiseLocalFeeClampsRemoteOverflow(t *testing.T) {
	tr := New()
	tr.SetRemoteFee(3_435_973_837)
	tr.RaiseLocalFee()
	tr.RaiseLocalFee()
	if got := tr.LocalFee(); got != feeMax {
		t.Fatalf("local fee after overflowing remote raise = %d, want %d", got, feeMax)
	}
}

func TestLoadFactorAggregates(t *testing.T) {
	tr := New()
	tr.SetRemoteFee(400)
	tr.SetClusterFee(300)
	tr.RaiseLocalFee()
	tr.RaiseLocalFee() // local = max(local, remote=400) * 5/4 = 500

	if tr.LocalFee() != 500 {
		t.Fatalf("local after raise with remote=400: got %d, want 500", tr.LocalFee())
	}
	if lf := tr.LoadFactor(); lf != 500 {
		t.Fatalf("load factor = %d; want max(cluster=300, local=500, remote=400) = 500", lf)
	}
	feeFactor, remFee := tr.scalingFactors()
	if feeFactor != 500 || remFee != 400 {
		t.Fatalf("scaling factors = (%d,%d); want (500,400)", feeFactor, remFee)
	}
}

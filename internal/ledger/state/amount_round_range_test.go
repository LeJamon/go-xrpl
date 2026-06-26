package state

import "testing"

// recoverPanic runs f and returns the recovered panic value, or nil.
func recoverPanic(f func()) (rec any) {
	defer func() { rec = recover() }()
	f()
	return nil
}

const outOfRangeMsg = "Native currency amount out of range"

// TestCanonicalizeDropsMaxNative locks the cMaxNativeN (10^17) boundary on the
// native canonicalize helpers, mirroring STAmount::canonicalize. A magnitude of
// exactly MaxNativeDrops is in range; one drop over, or a scale-up offset above
// 17, is rejected — both for the round-away-from-zero (CanonicalizeDrops{,Strict})
// and the no-round (canonicalizeDropsNoRound) tails.
func TestCanonicalizeDropsMaxNative(t *testing.T) {
	// 1e16 scaled up by one decade lands exactly on cMaxNativeN: in range.
	if got := CanonicalizeDrops(10_000_000_000_000_000, 1); got != maxNativeDrops {
		t.Fatalf("CanonicalizeDrops at cMaxNativeN = %d, want %d", got, maxNativeDrops)
	}
	if got := CanonicalizeDropsStrict(10_000_000_000_000_000, 1, true); got != maxNativeDrops {
		t.Fatalf("CanonicalizeDropsStrict at cMaxNativeN = %d, want %d", got, maxNativeDrops)
	}

	// One drop over the ceiling must be rejected, not returned.
	cases := []struct {
		name string
		f    func() int64
	}{
		{"CanonicalizeDrops over ceiling", func() int64 { return CanonicalizeDrops(10_000_000_000_000_001, 1) }},
		{"CanonicalizeDrops offset>17", func() int64 { return CanonicalizeDrops(1, 18) }},
		{"CanonicalizeDropsStrict over ceiling", func() int64 { return CanonicalizeDropsStrict(10_000_000_000_000_001, 1, false) }},
		{"CanonicalizeDropsStrict offset>17", func() int64 { return CanonicalizeDropsStrict(1, 18, true) }},
		{"canonicalizeDropsNoRound truncate over ceiling", func() int64 { return canonicalizeDropsNoRound(20_000_000_000_000_000, 1, true) }},
		{"canonicalizeDropsNoRound offset>17", func() int64 { return canonicalizeDropsNoRound(1, 18, true) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := recoverPanic(func() { tc.f() })
			if rec == nil {
				t.Fatalf("%s: expected panic, got none", tc.name)
			}
			if msg, _ := rec.(string); msg != outOfRangeMsg {
				t.Fatalf("%s: panic = %q, want %q", tc.name, rec, outOfRangeMsg)
			}
		})
	}

	// A negative magnitude exactly at the ceiling is still in range.
	if got := CanonicalizeDrops(-10_000_000_000_000_000, 1); got != -maxNativeDrops {
		t.Fatalf("CanonicalizeDrops negative cMaxNativeN = %d, want %d", got, -maxNativeDrops)
	}
}

// TestNativeRoundDropsOutOfRange checks the end-to-end mul/div native round
// entry points: an out-of-range native result panics instead of returning an
// out-of-range (or int64-wrapped) drops value, while a balance-scale result is
// returned unchanged.
func TestNativeRoundDropsOutOfRange(t *testing.T) {
	const iss = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	// IOU(10^15) * XRP(10^6): the native product is ~3.9e18 drops, far above
	// cMaxNativeN. Both rounding directions must reject.
	for _, ru := range []bool{false, true} {
		rec := recoverPanic(func() {
			MulRoundNative(
				NewIssuedAmountFromValue(1_000_000_000_000_000, 0, "USD", iss),
				NewXRPAmountFromInt(1_000_000),
				ru,
			)
		})
		if msg, _ := rec.(string); msg != outOfRangeMsg {
			t.Fatalf("MulRoundNative out-of-range (ru=%v): panic = %v, want %q", ru, rec, outOfRangeMsg)
		}
	}

	// A division whose normalized offset exceeds 17 is rejected by the pre-check.
	rec := recoverPanic(func() {
		DivRoundNative(
			NewIssuedAmountFromValue(9_999_999_999_999_999, 80, "USD", iss),
			NewIssuedAmountFromValue(1_000_000_000_000_000, 0, "USD", iss),
			false,
		)
	})
	if msg, _ := rec.(string); msg != outOfRangeMsg {
		t.Fatalf("DivRoundNative offset>17: panic = %v, want %q", rec, outOfRangeMsg)
	}

	// An in-range native division must still return its drops value untouched.
	if got := DivRoundNative(NewXRPAmountFromInt(100_000_000_000), NewXRPAmountFromInt(7), false); got != 14_285_714_285 {
		t.Fatalf("DivRoundNative in-range = %d, want 14285714285", got)
	}
	// And an in-range generic (non native×native) multiply.
	if got := MulRoundNative(NewIssuedAmountFromValue(-2_500_000_000_000_000, -10, "USD", iss), NewXRPAmountFromInt(-123_456_789), false); got != 30_864_197_250_000 {
		t.Fatalf("MulRoundNative in-range = %d, want 30864197250000", got)
	}
}

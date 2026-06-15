package state

import (
	"strings"
	"testing"
)

// Issue #950 covers the two faithfulness gaps in the native (XRP-output)
// mul/div round path that the reachable offer/quality callers never hit (they
// always pass roundUp=true with positive operands, landing in the addSlop
// branch): the no-round branch's rounding mode, and the native×native overflow
// guard in mulRoundImpl. The grids below deliberately exercise those unreachable
// inputs.

// iouValue builds an issued amount whose mantissa/exponent are already in the
// normalized [10^15, 10^16) band, so construction never rounds and the result is
// independent of the switchover flag under test.
func iouValue(mantissa int64, exponent int) Amount {
	return NewIssuedAmountFromValue(mantissa, exponent, "USD", "rIssuerForTests")
}

// TestIssue950_MulRoundNativeNoRound exercises Gap 1 for MulRoundNative: in the
// not-rounding-away-from-zero branch rippled's STAmount::canonicalize builds the
// native result through Number, rounding the discarded fraction to-nearest
// (banker's) once the Number switchover is on, and truncating before it.
func TestIssue950_MulRoundNativeNoRound(t *testing.T) {
	oneDrop := NewXRPAmountFromInt(1)

	cases := []struct {
		name        string
		v2          Amount
		roundUp     bool
		wantNearest int64
		wantTrunc   int64
	}{
		// roundUp=false, positive operands → resultNegative == roundUp.
		{"5.5_half_to_even_up", iouValue(5_500_000_000_000_000, -15), false, 6, 5},
		{"5.7_above_half", iouValue(5_700_000_000_000_000, -15), false, 6, 5},
		{"5.3_below_half", iouValue(5_300_000_000_000_000, -15), false, 5, 5},
		{"3.5_half_to_even_up", iouValue(3_500_000_000_000_000, -15), false, 4, 3},
		{"2.5_half_to_even_down", iouValue(2_500_000_000_000_000, -15), false, 2, 2},
		// roundUp=true with a negative result also lands in the no-round branch
		// (addSlop == false). The magnitude is rounded to-nearest, then negated.
		{"neg_5.5_round_up", iouValue(-5_500_000_000_000_000, -15), true, -6, -5},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev := GetNumberSwitchover()
			defer SetNumberSwitchover(prev)

			SetNumberSwitchover(true)
			if got := MulRoundNative(oneDrop, tc.v2, tc.roundUp); got != tc.wantNearest {
				t.Errorf("switchover on: MulRoundNative = %d drops, want %d", got, tc.wantNearest)
			}

			SetNumberSwitchover(false)
			if got := MulRoundNative(oneDrop, tc.v2, tc.roundUp); got != tc.wantTrunc {
				t.Errorf("switchover off: MulRoundNative = %d drops, want %d", got, tc.wantTrunc)
			}
		})
	}
}

// TestIssue950_DivRoundNativeNoRound exercises Gap 1 for DivRoundNative.
func TestIssue950_DivRoundNativeNoRound(t *testing.T) {
	cases := []struct {
		name        string
		numDrops    int64
		den         Amount
		wantNearest int64
		wantTrunc   int64
	}{
		{"11_div_2_is_5.5", 11, iouValue(2_000_000_000_000_000, -15), 6, 5},
		{"23_div_2_is_11.5", 23, iouValue(2_000_000_000_000_000, -15), 12, 11},
		{"7_div_2_is_3.5", 7, iouValue(2_000_000_000_000_000, -15), 4, 3},
		{"5_div_2_is_2.5", 5, iouValue(2_000_000_000_000_000, -15), 2, 2},
		{"27_div_10_is_2.7", 27, iouValue(1_000_000_000_000_000, -14), 3, 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev := GetNumberSwitchover()
			defer SetNumberSwitchover(prev)
			num := NewXRPAmountFromInt(tc.numDrops)

			SetNumberSwitchover(true)
			if got := DivRoundNative(num, tc.den, false); got != tc.wantNearest {
				t.Errorf("switchover on: DivRoundNative = %d drops, want %d", got, tc.wantNearest)
			}

			SetNumberSwitchover(false)
			if got := DivRoundNative(num, tc.den, false); got != tc.wantTrunc {
				t.Errorf("switchover off: DivRoundNative = %d drops, want %d", got, tc.wantTrunc)
			}
		})
	}
}

// TestIssue950_MulRoundNativeNativeOverflow exercises Gap 2: when both operands
// are native XRP, MulRoundNative takes mulRoundImpl's native×native fast path,
// returning the product of the drop values and panicking on either overflow
// bound before the multiply.
func TestIssue950_MulRoundNativeNativeOverflow(t *testing.T) {
	t.Run("product_no_overflow", func(t *testing.T) {
		if got := MulRoundNative(NewXRPAmountFromInt(3), NewXRPAmountFromInt(4), true); got != 12 {
			t.Errorf("3 * 4 drops = %d, want 12", got)
		}
		// minV == 3e9 is at the first bound but not past it; (maxV>>32) == 0.
		if got := MulRoundNative(NewXRPAmountFromInt(3_000_000_000), NewXRPAmountFromInt(1), true); got != 3_000_000_000 {
			t.Errorf("3e9 * 1 drops = %d, want 3000000000", got)
		}
	})

	t.Run("first_bound_minV_gt_sqrt_cMaxNative", func(t *testing.T) {
		assertNativeOverflow(t, func() {
			MulRoundNative(NewXRPAmountFromInt(3_000_000_001), NewXRPAmountFromInt(3_000_000_001), true)
		})
	})

	t.Run("second_bound_maxV_shift_times_minV", func(t *testing.T) {
		// minV = 3e9 (<= first bound), maxV = 2^32 → (maxV>>32)*minV = 3e9 > 2095475792.
		assertNativeOverflow(t, func() {
			MulRoundNative(NewXRPAmountFromInt(3_000_000_000), NewXRPAmountFromInt(4_294_967_296), true)
		})
	})
}

// TestIssue950_NativeRoundDropsStrictNoRoundTruncates guards the strict no-round
// native path: unlike the non-strict variant (Gap 1), the strict variant installs
// a Number towards-zero/downward guard in rippled, so it must keep truncating even
// post-switchover. This is the path the fixReducedOffersV1 offer crossing relies
// on; rounding it to-nearest produced offers with worsened rates.
func TestIssue950_NativeRoundDropsStrictNoRoundTruncates(t *testing.T) {
	prev := GetNumberSwitchover()
	defer SetNumberSwitchover(prev)
	SetNumberSwitchover(true)

	const amount uint64 = 55_000_000_000_000_000 // 5.5e16, offset -16 → 5.5 drops
	const offset = -16

	// addSlop == false (no-round branch), positive result.
	if got := NativeRoundDrops(amount, offset, false, false, false, true); got != 5 {
		t.Errorf("strict no-round: got %d, want 5 (truncation)", got)
	}
	if got := NativeRoundDrops(amount, offset, false, false, false, false); got != 6 {
		t.Errorf("non-strict no-round: got %d, want 6 (to-nearest)", got)
	}
}

func assertNativeOverflow(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on native value overflow, got none")
		}
		if msg, ok := r.(string); !ok || !strings.Contains(msg, "Native value overflow") {
			t.Fatalf("expected \"Native value overflow\" panic, got %v", r)
		}
	}()
	fn()
}

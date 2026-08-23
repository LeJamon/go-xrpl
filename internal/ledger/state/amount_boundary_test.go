package state

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestAmountMulRatioRejectsZeroDenominatorForIOU(t *testing.T) {
	amount := NewIssuedAmountFromValue(1_000_000_000_000_000, -15, "USD", "rIssuer")
	assertPanicsWith(t, "division by zero", func() {
		amount.MulRatio(1, 0, false)
	})
}

func TestAmountMulRatioRejectsNativeOverflow(t *testing.T) {
	amount := NewXRPAmountFromInt(math.MaxInt64)
	assertPanicsWith(t, "XRP mulRatio overflow", func() {
		amount.MulRatio(math.MaxUint32, 1, false)
	})
}

func TestAmountDivRoundsNegativeNativeAwayFromZero(t *testing.T) {
	got := NewXRPAmountFromInt(-5).Div(NewXRPAmountFromInt(2), true)
	if got.Drops() != -3 {
		t.Fatalf("-5 / 2 rounded away from zero = %d, want -3", got.Drops())
	}
}

func TestAmountCompareCheckedRejectsDifferentAssets(t *testing.T) {
	xrp := NewXRPAmountFromInt(1)
	usd := NewIssuedAmountFromValue(1, 0, "USD", "rIssuer")
	if _, err := xrp.CompareChecked(usd); err == nil {
		t.Fatal("CompareChecked(XRP, USD) succeeded")
	}

	eur := NewIssuedAmountFromValue(1, 0, "EUR", "rIssuer")
	if _, err := usd.CompareChecked(eur); err == nil {
		t.Fatal("CompareChecked(USD, EUR) succeeded")
	}
	assertPanicsWith(t, "different assets", func() {
		xrp.Compare(usd)
	})
}

func TestAmountMulWithNumberContextRejectsNativeOverflow(t *testing.T) {
	ctx := NewNumberContext(MantissaScaleSmall, false)
	assertPanicsWith(t, "Native value overflow", func() {
		NewXRPAmountFromInt(math.MaxInt64).MulWithNumberContext(NewXRPAmountFromInt(2), ctx, false, RoundToNearest)
	})
}

func TestAmountMulWithNumberContextNativeProtocolBounds(t *testing.T) {
	maxNative := NewXRPAmountFromInt(int64(MaxNativeDrops))
	oneNative := NewXRPAmountFromInt(1)
	oneIOU := NewIssuedAmountFromValue(MinMantissa, -15, "USD", "rIssuer")
	ctx := NewNumberContext(MantissaScaleSmall, false)

	if got := maxNative.MulWithNumberContext(oneNative, ctx, false, RoundToNearest); got.Drops() != int64(MaxNativeDrops) {
		t.Fatalf("native max * 1 = %d, want %d", got.Drops(), MaxNativeDrops)
	}
	if got := maxNative.MulWithNumberContext(oneIOU, ctx, false, RoundToNearest); got.Drops() != int64(MaxNativeDrops) {
		t.Fatalf("native max * IOU(1) = %d, want %d", got.Drops(), MaxNativeDrops)
	}

	assertPanicsWith(t, "Native currency amount out of range", func() {
		maxNative.MulWithNumberContext(NewXRPAmountFromInt(2), ctx, false, RoundToNearest)
	})
	assertPanicsWith(t, "Native value overflow", func() {
		NewXRPAmountFromInt(-1).MulWithNumberContext(oneNative, ctx, false, RoundToNearest)
	})
	assertPanicsWith(t, "Native value overflow", func() {
		NewXRPAmountFromInt(-1).MulWithNumberContext(NewXRPAmountFromInt(-1), ctx, false, RoundToNearest)
	})
	assertPanicsWith(t, "Native currency amount out of range", func() {
		maxNative.MulWithNumberContext(
			NewIssuedAmountFromValue(2*MinMantissa, -15, "USD", "rIssuer"),
			ctx,
			false,
			RoundToNearest,
		)
	})
	if got := oneNative.MulWithNumberContext(
		NewIssuedAmountFromValue(-MinMantissa, -15, "USD", "rIssuer"),
		ctx,
		false,
		RoundToNearest,
	); got.Drops() != -1 {
		t.Fatalf("native 1 * IOU(-1) = %d, want -1", got.Drops())
	}
	if got := oneNative.Div(
		NewIssuedAmountFromValue(-MinMantissa, -15, "USD", "rIssuer"),
		false,
	); got.Drops() != -1 {
		t.Fatalf("native 1 / IOU(-1) = %d, want -1", got.Drops())
	}
}

func TestIssuedAmountFloatRejectsNonFiniteValues(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		assertPanicsWith(t, "issued amount must be finite", func() {
			NewIssuedAmountFromFloat64(value, "USD", "rIssuer")
		})
	}
}

func TestIssuedAmountDecimalRejectsEmptyMantissa(t *testing.T) {
	for _, value := range []string{"", "+", "-", ".5", "1.", "e1", "+e1"} {
		if _, err := NewIssuedAmountFromDecimalString(value, "USD", "rIssuer"); err == nil {
			t.Errorf("NewIssuedAmountFromDecimalString(%q) succeeded", value)
		}
	}
}

func assertPanicsWith(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		got := recover()
		if got == nil {
			t.Fatalf("expected panic containing %q", want)
		}
		if !strings.Contains(fmt.Sprint(got), want) {
			t.Fatalf("panic = %q, want substring %q", got, want)
		}
	}()
	fn()
}

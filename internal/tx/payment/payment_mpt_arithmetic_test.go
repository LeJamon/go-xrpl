package payment

import (
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

func TestMPTMultiplyProtocolRounding(t *testing.T) {
	small := state.NewNumberContext(state.MantissaScaleSmall, false)
	large := state.NewNumberContext(state.MantissaScaleLarge, true)

	tests := []struct {
		name    string
		amount  uint64
		rate    uint64
		context state.NumberContext
		want    uint64
	}{
		{name: "parity max", amount: math.MaxInt64, rate: mptRateOne, context: small, want: math.MaxInt64},
		{name: "parity max large scale", amount: math.MaxInt64, rate: mptRateOne, context: large, want: math.MaxInt64},
		{name: "below 1e15", amount: 999_999_999_999_999, rate: 1_100_000_000, context: small, want: 1_099_999_999_999_999},
		{name: "at 1e15", amount: 1_000_000_000_000_000, rate: 1_100_000_000, context: small, want: 1_100_000_000_000_000},
		{name: "above 1e15", amount: 1_000_000_000_000_001, rate: 1_100_000_000, context: small, want: 1_100_000_000_000_001},
		{name: "issue small scale", amount: 10_000_000_000_000_001, rate: 1_100_000_000, context: small, want: 11_000_000_000_000_000},
		{name: "issue large scale", amount: 10_000_000_000_000_001, rate: 1_100_000_000, context: large, want: 11_000_000_000_000_001},
		{name: "small scale even input tie", amount: 10_000_000_000_000_005, rate: 1_100_000_000, context: small, want: 11_000_000_000_000_000},
		{name: "large scale output tie rounds up", amount: 10_000_000_000_000_005, rate: 1_100_000_000, context: large, want: 11_000_000_000_000_006},
		{name: "small scale odd input tie", amount: 10_000_000_000_000_015, rate: 1_100_000_000, context: small, want: 11_000_000_000_000_020},
		{name: "large scale output tie rounds down", amount: 10_000_000_000_000_015, rate: 1_100_000_000, context: large, want: 11_000_000_000_000_016},
		{name: "one times one point five", amount: 1, rate: 1_500_000_000, context: small, want: 2},
		{name: "three times one point five", amount: 3, rate: 1_500_000_000, context: small, want: 4},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := mptMultiply(test.amount, test.rate, test.context); got != test.want {
				t.Fatalf("mptMultiply(%d, %d) = %d, want %d", test.amount, test.rate, got, test.want)
			}
		})
	}

	for _, context := range []state.NumberContext{small, large} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("mptMultiply(math.MaxInt64, 1.1) did not panic for scale %v", context.Scale())
				}
			}()
			_ = mptMultiply(math.MaxInt64, 1_100_000_000, context)
		}()
	}
}

func TestMPTDivideLegacyRounding(t *testing.T) {
	tests := []struct {
		name   string
		amount uint64
		rate   uint64
		want   uint64
	}{
		{name: "parity max", amount: math.MaxInt64, rate: mptRateOne, want: math.MaxInt64},
		{name: "one half", amount: 1, rate: 2_000_000_000, want: 1},
		{name: "three halves", amount: 3, rate: 2_000_000_000, want: 2},
		{name: "five halves", amount: 5, rate: 2_000_000_000, want: 3},
		{name: "issue boundary", amount: 11_000_000_000_000_000, rate: 1_100_000_000, want: 10_000_000_000_000_000},
		{name: "issue boundary plus one", amount: 11_000_000_000_000_001, rate: 1_100_000_000, want: 10_000_000_000_000_001},
		{name: "largest muldiv result", amount: 202_914_184_810_805_067, rate: 1_100_000_000, want: 184_467_440_737_095_516},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := mptDivide(test.amount, test.rate); got != test.want {
				t.Fatalf("mptDivide(%d, %d) = %d, want %d", test.amount, test.rate, got, test.want)
			}
		})
	}

	for _, amount := range []uint64{202_914_184_810_805_068, math.MaxInt64} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("mptDivide(%d, 1.1) did not panic", amount)
				}
			}()
			_ = mptDivide(amount, 1_100_000_000)
		}()
	}
}

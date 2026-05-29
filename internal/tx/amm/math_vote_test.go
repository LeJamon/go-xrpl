package amm

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

// TestNumberDivToInt64_RoundHalfToEven verifies that the vote-weight / trading-fee
// quotient conversion matches rippled's static_cast<std::int64_t>(Number{...}),
// which uses round-half-to-even — NOT float64 truncation toward zero. A divergence
// here forks consensus because the result is serialized into the AMM ledger object.
// Reference: rippled AMMVote.cpp:137-139,164-168,209.
func TestNumberDivToInt64_RoundHalfToEven(t *testing.T) {
	amt := func(v float64) state.Amount {
		return state.NewIssuedAmountFromFloat64(v, "", "")
	}

	cases := []struct {
		n, d float64
		want int64
	}{
		{15, 2, 8},  // 7.5 -> 8 (truncation would yield 7)
		{35, 10, 4}, // 3.5 -> 4 (truncation would yield 3)
		{25, 10, 2}, // 2.5 -> 2 (round half to even; truncation also 2)
		{45, 10, 4}, // 4.5 -> 4 (round half to even; truncation would yield 4)
		{55, 10, 6}, // 5.5 -> 6 (round half to even; truncation would yield 5)
		{14, 4, 4},  // 3.5 -> 4 (round half to even)
		{10, 4, 2},  // 2.5 -> 2 (round half to even)
		{7, 2, 4},   // 3.5 -> 4
		{20, 8, 2},  // 2.5 -> 2
		{0, 5, 0},   // zero numerator
		{5, 0, 0},   // zero denominator
	}

	for _, c := range cases {
		got := numberDivToInt64(amt(c.n), amt(c.d))
		if got != c.want {
			t.Errorf("numberDivToInt64(%v, %v) = %d, want %d", c.n, c.d, got, c.want)
		}
	}
}

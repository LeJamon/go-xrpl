// Package quality_test tests theoretical quality calculations for payment paths.
// Reference: rippled/src/test/app/TheoreticalQuality_test.cpp
package quality_test

import (
	"math"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx/payment"
	"github.com/stretchr/testify/require"
)

// toQuality creates a Quality representing mantissa * 10^exponent.
// Mirrors rippled TheoreticalQuality_test.cpp toQuality() helper.
func toQuality(mantissa uint64, exponent int) payment.Quality {
	return payment.QualityFromMantissaExp(mantissa, exponent)
}

// TestRelativeQualityDistance tests the relative distance metric between qualities.
// Reference: rippled TheoreticalQuality_test.cpp testRelativeQDistance()
func TestRelativeQualityDistance(t *testing.T) {
	t.Run("equal qualities have zero distance", func(t *testing.T) {
		d := payment.RelativeDistance(toQuality(100, 0), toQuality(100, 0))
		require.Equal(t, 0.0, d)
	})

	t.Run("10x difference gives distance 9", func(t *testing.T) {
		// relativeDistance(100, 1000) = (1000-100)/100 = 9
		d := payment.RelativeDistance(toQuality(100, 0), toQuality(100, 1))
		require.InDelta(t, 9.0, d, 1e-10)
	})

	t.Run("10% difference gives distance 0.1", func(t *testing.T) {
		// relativeDistance(100, 110) = (110-100)/100 = 0.1
		d := payment.RelativeDistance(toQuality(100, 0), toQuality(110, 0))
		require.InDelta(t, 0.1, d, 1e-10)
	})

	t.Run("same ratio at high exponent", func(t *testing.T) {
		// relativeDistance(100*10^90, 110*10^90) = 0.1
		d := payment.RelativeDistance(toQuality(100, 90), toQuality(110, 90))
		require.InDelta(t, 0.1, d, 1e-10)
	})

	t.Run("order of magnitude difference at high exponent", func(t *testing.T) {
		// relativeDistance(100*10^90, 110*10^91) = 10
		d := payment.RelativeDistance(toQuality(100, 90), toQuality(110, 91))
		require.InDelta(t, 10.0, d, 1e-6)
	})

	t.Run("huge exponent difference", func(t *testing.T) {
		// relativeDistance(100, 100*10^90) = 1e90
		d := payment.RelativeDistance(toQuality(100, 0), toQuality(100, 90))
		require.InDelta(t, 1e90, d, 1e80)
	})

	t.Run("larger mantissa in smaller value", func(t *testing.T) {
		// relativeDistance(102, 101*10^90) >= 1e89
		// If values did not compare correctly, result would be negative.
		d := payment.RelativeDistance(toQuality(102, 0), toQuality(101, 90))
		require.GreaterOrEqual(t, d, 1e89)
	})
}

// TestDirectStepQuality tests that theoretical quality upper bounds match actual
// flow quality for direct payment paths (alice → bob → carol → dan).
// Reference: rippled TheoreticalQuality_test.cpp testDirectStep()
//
// This test uses 250 random iterations varying trust line quality in/out,
// debt direction, and transfer rates across 4 accounts. It calls internal
// path engine functions (toStrands, qualityUpperBound, flow) directly on
// the ledger state rather than through transaction submission.
//
// Not portable as a behavior test because it requires:
// - Trust line QualityIn/QualityOut setup (not exposed via standard TrustSet helpers)
// - Direct access to internal payment path functions (toStrands, GetStrandQuality, flow)
// - PaymentSandbox wrapping the ledger view for strand execution
func TestDirectStepQuality(t *testing.T) {
	t.Skip("Internal path engine test: requires direct access to toStrands/qualityUpperBound/flow (not accessible from behavior test layer)")
}

// TestBookStepQuality tests that theoretical quality upper bounds match actual
// flow quality for payment paths through offer books.
// Reference: rippled TheoreticalQuality_test.cpp testBookStep()
//
// This test uses 100 random iterations with a payment path:
// alice (USD/bob) → bob → (USD/bob)|(EUR/carol) → carol → dan
// with random transfer rates, quality in/out, and debt directions.
//
// Not portable for the same reasons as TestDirectStepQuality.
func TestBookStepQuality(t *testing.T) {
	t.Skip("Internal path engine test: requires direct access to toStrands/qualityUpperBound/flow (not accessible from behavior test layer)")
}

// TestQualityComposition verifies basic quality composition (multiplication).
// This supplements the theoretical quality tests by verifying that Quality.Compose
// correctly combines step qualities.
func TestQualityComposition(t *testing.T) {
	// Identity composition: q * 1.0 = q
	t.Run("identity composition", func(t *testing.T) {
		q := toQuality(150, 0)
		identity := toQuality(1, 0)
		composed := q.Compose(identity)
		d := payment.RelativeDistance(q, composed)
		require.LessOrEqual(t, d, 1e-7, "composing with identity should preserve quality")
	})

	// Commutative: a * b should equal b * a
	t.Run("commutative", func(t *testing.T) {
		a := toQuality(120, 0)
		b := toQuality(150, 0)
		ab := a.Compose(b)
		ba := b.Compose(a)
		d := payment.RelativeDistance(ab, ba)
		require.LessOrEqual(t, d, 1e-7, "quality composition should be commutative")
	})

	// Known values: 2.0 * 3.0 = 6.0
	t.Run("known multiplication", func(t *testing.T) {
		two := toQuality(2, 0)
		three := toQuality(3, 0)
		six := toQuality(6, 0)
		composed := two.Compose(three)
		d := payment.RelativeDistance(composed, six)
		require.LessOrEqual(t, d, 1e-7, "2 * 3 should compose to 6")
	})

	// Large exponents: 10^45 * 10^45 = 10^90
	t.Run("large exponent composition", func(t *testing.T) {
		a := toQuality(1, 45)
		composed := a.Compose(a)
		expected := toQuality(1, 90)
		d := payment.RelativeDistance(composed, expected)
		require.LessOrEqual(t, d, 1e-7, "10^45 * 10^45 should compose to 10^90")
	})
}

// TestQualityOrdering verifies quality comparison semantics.
func TestQualityOrdering(t *testing.T) {
	t.Run("lower value is better", func(t *testing.T) {
		better := toQuality(1, 0) // rate of 1.0
		worse := toQuality(2, 0)  // rate of 2.0
		require.True(t, better.BetterThan(worse))
		require.True(t, worse.WorseThan(better))
		require.False(t, better.WorseThan(worse))
	})

	t.Run("equal qualities", func(t *testing.T) {
		q1 := toQuality(100, 0)
		q2 := toQuality(100, 0)
		require.False(t, q1.BetterThan(q2))
		require.False(t, q1.WorseThan(q2))
		require.Equal(t, 0, q1.Compare(q2))
	})

	t.Run("exponent dominates mantissa", func(t *testing.T) {
		// 999 * 10^0 < 1 * 10^3 = 1000 in quality value
		small := toQuality(999, 0)
		large := toQuality(1, 3)
		// Both represent ~same value but different encoding
		d := payment.RelativeDistance(small, large)
		require.LessOrEqual(t, d, 0.002, "999 and 1000 should be very close")
	})
}

// TestRelativeDistanceSymmetry verifies relativeDistance(a,b) == relativeDistance(b,a).
func TestRelativeDistanceSymmetry(t *testing.T) {
	cases := [][2]payment.Quality{
		{toQuality(100, 0), toQuality(200, 0)},
		{toQuality(1, 10), toQuality(1, 20)},
		{toQuality(999, 0), toQuality(1, 3)},
	}

	for _, tc := range cases {
		d1 := payment.RelativeDistance(tc[0], tc[1])
		d2 := payment.RelativeDistance(tc[1], tc[0])
		require.InDelta(t, d1, d2, 1e-10, "relativeDistance should be symmetric")
		require.False(t, math.IsNaN(d1), "distance should not be NaN")
		require.GreaterOrEqual(t, d1, 0.0, "distance should be non-negative")
	}
}

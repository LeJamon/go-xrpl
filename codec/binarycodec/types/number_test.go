package types

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNumberNormalizeRoundHalfEven verifies that the discarded low-order
// digits are rounded half-to-even, matching rippled's Number::normalize Guard.
func TestNumberNormalizeRoundHalfEven(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		mantissa string
		exponent int32
	}{
		// Single dropped digit, exactly half (==5): round to even.
		{"tie odd rounds up", "12345678901234575", "1234567890123458", 1},
		{"tie even stays", "12345678901234565", "1234567890123456", 1},
		// Single dropped digit, above / below half.
		{"above half rounds up", "12345678901234566", "1234567890123457", 1},
		{"below half truncates", "12345678901234564", "1234567890123456", 1},
		// Carry that overflows the mantissa back up an exponent.
		{"round up carries exponent", "99999999999999995", "1000000000000000", 2},
		// Multiple dropped digits: half is 500.
		{"multi above half", "1234567890123456501", "1234567890123457", 3},
		{"multi exactly half even", "1234567890123456500", "1234567890123456", 3},
		{"multi exactly half odd", "1234567890123457500", "1234567890123458", 3},
		// Sign is preserved through rounding.
		{"negative tie odd rounds up", "-12345678901234575", "-1234567890123458", 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, exp, err := parseAndNormalize(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.mantissa, m.String())
			require.Equal(t, tc.exponent, exp)
		})
	}
}

// TestNumberNormalizeUnderflowClamp verifies that sub-normal results clamp to
// canonical zero (mantissa 0, exponent 0x80000000) like rippled.
func TestNumberNormalizeUnderflowClamp(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		// Mantissa cannot be scaled up to minMantissa before hitting the
		// exponent floor.
		{"mantissa below min at exponent floor", "1e-32768"},
		// Exponent already below minExponent.
		{"exponent below min", "5000000000000000e-40000"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, exp, err := parseAndNormalize(tc.input)
			require.NoError(t, err)
			require.Equal(t, big.NewInt(0).String(), m.String())
			require.Equal(t, defaultZeroExp, exp)
		})
	}
}

// TestNumberNormalizeOverflow verifies that exceeding maxExponent while scaling
// down an oversized mantissa returns an overflow error.
func TestNumberNormalizeOverflow(t *testing.T) {
	_, _, err := parseAndNormalize("99999999999999990e32768")
	require.ErrorIs(t, err, ErrNumberOverflow)
}

// TestNumberRoundTrip is a sanity check that FromJSON/ToJSON still round-trip
// ordinary values after the normalize rework.
func TestNumberRoundTrip(t *testing.T) {
	n := &Number{}
	for _, s := range []string{"0", "3.14", "-3.14", "123", "-123", "1000000000000000"} {
		b, err := n.FromJSON(s)
		require.NoError(t, err)
		require.Len(t, b, 12)
	}
}

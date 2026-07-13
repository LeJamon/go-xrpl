package types

import (
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
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
		{"tie odd rounds up", "12345678901234567895", "1234567890123456790", 1},
		{"tie even stays", "12345678901234567885", "1234567890123456788", 1},
		// Single dropped digit, above / below half.
		{"above half rounds up", "12345678901234567896", "1234567890123456790", 1},
		{"below half truncates", "12345678901234567894", "1234567890123456789", 1},
		// Carry that overflows the mantissa back up an exponent.
		{"round up carries exponent", "99999999999999999995", "1000000000000000000", 2},
		// Multiple dropped digits: half is 500.
		{"multi above half", "1234567890123456789501", "1234567890123456790", 3},
		{"multi exactly half even", "1234567890123456788500", "1234567890123456788", 3},
		{"multi exactly half odd", "1234567890123456789500", "1234567890123456790", 3},
		// Sign is preserved through rounding.
		{"negative tie odd rounds up", "-12345678901234567895", "-1234567890123456790", 1},
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
	_, _, err := parseAndNormalize("99999999999999999990e32768")
	require.ErrorIs(t, err, ErrNumberOverflow)
}

// TestNumberRoundTrip checks that FromJSON/ToJSON round-trip ordinary values.
func TestNumberRoundTrip(t *testing.T) {
	n := &Number{}
	for _, s := range []string{"0", "3.14", "-3.14", "123", "-123", "1000000000000000"} {
		b, err := n.FromJSON(s)
		require.NoError(t, err)
		require.Len(t, b, 12)
	}
}

func TestNumberLargeScaleWireParity(t *testing.T) {
	n := &Number{}
	b, err := n.FromJSON("500")
	require.NoError(t, err)
	require.Equal(t, int64(5_000_000_000_000_000_000), int64(binary.BigEndian.Uint64(b[:8])))
	require.Equal(t, int32(-16), int32(binary.BigEndian.Uint32(b[8:])))

	decoded, err := n.ToJSON(serdes.NewBinaryParser(b, nil))
	require.NoError(t, err)
	require.Equal(t, "500", decoded)

	b, err = n.FromJSON("9223372036854775808")
	require.NoError(t, err)
	require.Equal(t, int64(922337203685477581), int64(binary.BigEndian.Uint64(b[:8])))
	require.Equal(t, int32(1), int32(binary.BigEndian.Uint32(b[8:])))
}

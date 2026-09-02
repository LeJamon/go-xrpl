package types

import (
	"encoding/binary"
	"encoding/json"
	"errors"
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
			m, ok := new(big.Int).SetString(tc.input, 10)
			require.True(t, ok)
			m, exp, err := normalize(m, 0)
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
		name     string
		mantissa string
		exponent int32
	}{
		// Mantissa cannot be scaled up to minMantissa before hitting the
		// exponent floor.
		{"mantissa below min at exponent floor", "1", -32768},
		// Exponent already below minExponent.
		{"exponent below min", "5000000000000000", -40000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input, ok := new(big.Int).SetString(tc.mantissa, 10)
			require.True(t, ok)
			m, exp, err := normalize(input, tc.exponent)
			require.NoError(t, err)
			require.Equal(t, big.NewInt(0).String(), m.String())
			require.Equal(t, defaultZeroExp, exp)
		})
	}
}

// TestNumberNormalizeOverflow verifies that exceeding maxExponent while scaling
// down an oversized mantissa returns an overflow error.
func TestNumberNormalizeOverflow(t *testing.T) {
	m, ok := new(big.Int).SetString("99999999999999999990", 10)
	require.True(t, ok)
	_, _, err := normalize(m, 32768)
	require.ErrorIs(t, err, ErrNumberOverflow)
}

func TestNumberJSONInputBoundary(t *testing.T) {
	n := &Number{}

	for _, value := range []any{
		"+1",
		"-0.0e80",
		"9223372036854775807",
		int32(-42),
		int64(4294967295),
		uint32(42),
		float64(42),
		json.Number("-2147483648"),
		json.Number("4294967295"),
	} {
		if _, err := n.FromJSON(value); err != nil {
			t.Fatalf("FromJSON(%v [%T]): %v", value, value, err)
		}
	}

	for _, value := range []any{
		".1",
		"1.",
		"junk1junk",
		"001",
		"000.0",
		"18446744073709551616",
		"12345678901234567895",
		"9223372036854775808",
		"-9223372036854775808",
		"18446744073709551615",
		"-18446744073709551615",
		"1e-32768",
		"1e2147483648",
		"1e-2147483648",
		int64(-2147483649),
		uint64(4294967296),
		json.Number("1.0"),
		json.Number("1e2"),
		1.5,
	} {
		if _, err := n.FromJSON(value); !errors.Is(err, ErrInvalidNumber) {
			t.Fatalf("FromJSON(%v [%T]) error = %v, want ErrInvalidNumber", value, value, err)
		}
	}
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

	b, err = n.FromJSON("9223372036854775800")
	require.NoError(t, err)
	require.Equal(t, int64(9223372036854775800), int64(binary.BigEndian.Uint64(b[:8])))
	require.Equal(t, int32(0), int32(binary.BigEndian.Uint32(b[8:])))
}

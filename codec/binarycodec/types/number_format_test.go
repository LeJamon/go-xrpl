package types

import (
	"math/big"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/stretchr/testify/require"
)

// TestFormatNumberText ports rippled 3.1.0's to_string(Number) cases for both
// mantissa scales (rangeLog 15 = small, 18 = large). Inputs are the normalized
// (internal mantissa, exponent) values a Number holds after construction.
func TestFormatNumberText(t *testing.T) {
	t.Parallel()
	bi := func(s string) *big.Int { v, _ := new(big.Int).SetString(s, 10); return v }

	cases := []struct {
		mantissa string
		exponent int32
		rangeLog int
		want     string
	}{
		// The live 3.1.0 change: trailing zeros shift out of the scientific
		// mantissa into the exponent, for both scales.
		{"1000000000000000", -45, 15, "1e-30"},
		{"-1000000000000000", -45, 15, "-1e-30"},
		{"1000000000000000000", -48, 18, "1e-30"},

		// Common cases (identical for both scales), given their small-scale
		// normalized form.
		{"2000000000000000", -35, 15, "2e-20"},
		{"-2000000000000000", -35, 15, "-2e-20"},
		{"2500000000000000", -13, 15, "250"},
		{"2500000000000000", -17, 15, "0.025"},

		// Small-scale limits.
		{"1000000000000000", -32768, 15, "1e-32753"},
		{"9999999999999999", 32768, 15, "9999999999999999e32768"},
		{"-9999999999999999", 32768, 15, "-9999999999999999e32768"},

		// Large-scale limits and int64-boundary values (internal mantissa may
		// exceed int64, hence big.Int).
		{"1000000000000000000", -32768, 18, "1e-32750"},
		{"9223372036854775807", 32768, 18, "9223372036854775807e32768"},
		{"-9223372036854775807", 32768, 18, "-9223372036854775807e32768"},
		{"9999999999999999990", 0, 18, "9999999999999999990"},
		{"9223372036854775810", 0, 18, "9223372036854775810"},
	}
	for _, tc := range cases {
		got := formatNumberText(bi(tc.mantissa), tc.exponent, tc.rangeLog)
		require.Equalf(t, tc.want, got, "formatNumberText(%s, %d, L=%d)", tc.mantissa, tc.exponent, tc.rangeLog)
	}
}

// TestNumberToJSON_ScientificShift verifies the codec's decode path applies the
// new scientific formatting end to end (encode → decode → JSON string).
func TestNumberToJSON_ScientificShift(t *testing.T) {
	t.Parallel()
	n := &Number{}
	for _, tc := range []struct{ in, want string }{
		{"2e-20", "2e-20"},
		{"-2e-20", "-2e-20"},
		{"0.025", "0.025"},
		{"250", "250"},
		{"0", "0"},
		{"2e20", "2e20"},
	} {
		b, err := n.FromJSON(tc.in)
		require.NoError(t, err)
		out, err := n.ToJSON(serdes.NewBinaryParser(b, nil))
		require.NoError(t, err)
		require.Equalf(t, tc.want, out, "round-trip %q", tc.in)
	}
}

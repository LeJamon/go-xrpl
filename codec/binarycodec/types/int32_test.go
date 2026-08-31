package types

import (
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInt32FromJSON(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  []byte
	}{
		{name: "int32 minimum", input: int32(math.MinInt32), want: []byte{0x80, 0, 0, 0}},
		{name: "int32 maximum", input: int32(math.MaxInt32), want: []byte{0x7f, 0xff, 0xff, 0xff}},
		{name: "int minimum", input: int(math.MinInt32), want: []byte{0x80, 0, 0, 0}},
		{name: "int maximum", input: int(math.MaxInt32), want: []byte{0x7f, 0xff, 0xff, 0xff}},
		{name: "int64 minimum", input: int64(math.MinInt32), want: []byte{0x80, 0, 0, 0}},
		{name: "int64 maximum", input: int64(math.MaxInt32), want: []byte{0x7f, 0xff, 0xff, 0xff}},
		{name: "integral float64 minimum", input: float64(math.MinInt32), want: []byte{0x80, 0, 0, 0}},
		{name: "integral float64 maximum", input: float64(math.MaxInt32), want: []byte{0x7f, 0xff, 0xff, 0xff}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := (&Int32{}).FromJSON(test.input)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestInt32FromJSONRejectsInvalidValues(t *testing.T) {
	type testCase struct {
		name  string
		input any
	}
	tests := []testCase{
		{name: "unsupported type", input: true},
		{name: "int64 above maximum", input: int64(math.MaxInt32) + 1},
		{name: "int64 below minimum", input: int64(math.MinInt32) - 1},
		{name: "int64 wrapping to zero", input: int64(1) << 32},
		{name: "float64 above maximum", input: float64(math.MaxInt32) + 1},
		{name: "float64 below minimum", input: float64(math.MinInt32) - 1},
		{name: "fractional float64", input: 1.5},
		{name: "NaN", input: math.NaN()},
		{name: "positive infinity", input: math.Inf(1)},
		{name: "negative infinity", input: math.Inf(-1)},
	}
	if strconv.IntSize == 64 {
		aboveMax := int64(math.MaxInt32) + 1
		belowMin := int64(math.MinInt32) - 1
		tests = append(tests,
			testCase{name: "int above maximum", input: int(aboveMax)},
			testCase{name: "int below minimum", input: int(belowMin)},
		)
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := (&Int32{}).FromJSON(test.input)
			require.ErrorIs(t, err, ErrInvalidInt32)
			require.Nil(t, got)
		})
	}
}

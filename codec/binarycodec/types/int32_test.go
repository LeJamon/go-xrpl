package types

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInt32FromJSON(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  []byte
	}{
		{"minimum integer", int64(math.MinInt32), []byte{0x80, 0, 0, 0}},
		{"maximum unsigned integer", uint32(math.MaxInt32), []byte{0x7f, 0xff, 0xff, 0xff}},
		{"negative decimal string", "-1", []byte{0xff, 0xff, 0xff, 0xff}},
		{"positive decimal string", "+42", []byte{0, 0, 0, 42}},
		{"JSON number", json.Number("-2147483648"), []byte{0x80, 0, 0, 0}},
		{"integral float", float64(12), []byte{0, 0, 0, 12}},
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
	for _, input := range []any{
		int64(math.MinInt32) - 1,
		uint64(math.MaxInt32) + 1,
		float64(1.5),
		math.NaN(),
		math.Inf(1),
		"2147483648",
		"1.0",
		json.Number("-2147483649"),
		true,
	} {
		_, err := (&Int32{}).FromJSON(input)
		require.ErrorIs(t, err, ErrInvalidInt32, "%v", input)
	}
}

package serdes

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
)

// nolint
func BenchmarkEncode(b *testing.B) {

	tt := []struct {
		input string
	}{
		{
			input: "LedgerEntry",
		},
		{
			input: "yurt",
		},
	}

	codec := NewFieldIDCodec(definitions.Get())
	for _, test := range tt {
		b.Run(fmt.Sprintf("input_name_%v", test.input), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				codec.Encode(test.input)
			}
		})
	}
}

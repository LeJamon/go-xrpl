package tx

import (
	"fmt"
	"math"
	"testing"
)

func TestUint64ToUpperHex(t *testing.T) {
	cases := []uint64{0, 1, 9, 10, 15, 16, 255, 256, 0xABCDEF, math.MaxUint32, math.MaxUint64}
	for _, v := range cases {
		got := uint64ToUpperHex(v)
		want := fmt.Sprintf("%X", v)
		if got != want {
			t.Errorf("uint64ToUpperHex(%d) = %q, want %q", v, got, want)
		}
	}
}

func BenchmarkUint64ToUpperHex(b *testing.B) {
	const v uint64 = 0xDEADBEEFCAFEBABE
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = uint64ToUpperHex(v)
	}
}

func BenchmarkFmtSprintfHex(b *testing.B) {
	const v uint64 = 0xDEADBEEFCAFEBABE
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = fmt.Sprintf("%X", v)
	}
}

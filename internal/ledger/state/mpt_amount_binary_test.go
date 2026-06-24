package state

import (
	"encoding/binary"
	"math"
	"testing"
)

// mptAmountBlob builds a 33-byte MPT amount: 1-byte header, 8-byte big-endian
// magnitude, 24-byte issuance ID (left zero). header 0x60 = positive MPT,
// 0x20 = negative MPT.
func mptAmountBlob(header byte, magnitude uint64) []byte {
	b := make([]byte, 33)
	b[0] = header
	binary.BigEndian.PutUint64(b[1:9], magnitude)
	return b
}

// TestParseMPTAmountBinary_RejectsOverMaxInt64 guards GHSA-j5cw-qr86-mmv7: an
// MPT magnitude >= 2^63 must be rejected, not silently wrapped through
// num.Int64() into a negative value. rippled bounds MPT amounts at
// maxMPTokenAmount = 2^63-1.
func TestParseMPTAmountBinary_RejectsOverMaxInt64(t *testing.T) {
	cases := []struct {
		name      string
		header    byte
		magnitude uint64
		wantErr   bool
		wantRaw   int64
	}{
		// Exact advisory reproducer: magnitude 2^63 wraps to -2^63.
		{"positive_two_pow_63", 0x60, 1 << 63, true, 0},
		{"positive_max_uint64", 0x60, math.MaxUint64, true, 0},
		{"negative_two_pow_63", 0x20, 1 << 63, true, 0},
		{"positive_max_int64_ok", 0x60, math.MaxInt64, false, math.MaxInt64},
		{"positive_small_ok", 0x60, 1234567890, false, 1234567890},
		{"negative_small_ok", 0x20, 1234567890, false, -1234567890},
		{"zero_ok", 0x60, 0, false, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			amt, err := ParseMPTAmountBinary(mptAmountBlob(tc.header, tc.magnitude))
			if tc.wantErr {
				if err == nil {
					raw, _ := amt.MPTRaw()
					t.Fatalf("expected out-of-range error for magnitude %d, got mptRaw=%d", tc.magnitude, raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			raw, ok := amt.MPTRaw()
			if !ok {
				t.Fatal("expected MPTRaw to be set")
			}
			if raw != tc.wantRaw {
				t.Errorf("MPTRaw = %d, want %d", raw, tc.wantRaw)
			}
		})
	}
}

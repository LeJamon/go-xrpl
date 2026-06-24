package ledgerfields

import (
	"encoding/binary"
	"math"
	"testing"
)

// mptStreamBlob builds a 33-byte MPT amount: 1-byte header, 8-byte big-endian
// magnitude, 24-byte issuance ID (left zero). header 0x60 = positive MPT,
// 0x20 = negative MPT.
func mptStreamBlob(header byte, magnitude uint64) []byte {
	b := make([]byte, 33)
	b[0] = header
	binary.BigEndian.PutUint64(b[1:9], magnitude)
	return b
}

// TestReadMPTAmount_RejectsOverMaxInt64 guards GHSA-j5cw-qr86-mmv7 on the
// inline streaming decoder: an MPT mantissa >= 2^63 must error rather than
// surface as a bogus decimal string. rippled bounds MPT amounts at
// maxMPTokenAmount = 2^63-1.
func TestReadMPTAmount_RejectsOverMaxInt64(t *testing.T) {
	cases := []struct {
		name      string
		header    byte
		magnitude uint64
		wantErr   bool
		wantValue string
	}{
		{"positive_two_pow_63", 0x60, 1 << 63, true, ""},
		{"positive_max_uint64", 0x60, math.MaxUint64, true, ""},
		{"negative_two_pow_63", 0x20, 1 << 63, true, ""},
		{"positive_max_int64_ok", 0x60, math.MaxInt64, false, "9223372036854775807"},
		{"positive_small_ok", 0x60, 1234567890, false, "1234567890"},
		{"negative_small_ok", 0x20, 1234567890, false, "-1234567890"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sr := newStreamReader(mptStreamBlob(tc.header, tc.magnitude))
			got, err := sr.readMPTAmount()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected out-of-range error for magnitude %d, got %#v", tc.magnitude, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got["value"] != tc.wantValue {
				t.Errorf("value = %v, want %s", got["value"], tc.wantValue)
			}
		})
	}
}

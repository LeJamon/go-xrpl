package invariants

import (
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func TestHasInvalidAmount_JSON(t *testing.T) {
	mpt := func(v string) map[string]any {
		return map[string]any{"value": v, "mpt_issuance_id": "00000000000000000000000000000000000000000000000000"}
	}
	iou := func(v string) map[string]any {
		return map[string]any{"value": v, "currency": "USD", "issuer": "rISSUER"}
	}
	tests := []struct {
		name   string
		fields map[string]any
		want   bool
	}{
		{"native at cap", map[string]any{"Amount": "100000000000000000"}, false},
		{"native over cap", map[string]any{"Amount": "100000000000000001"}, true},
		{"native negative over cap", map[string]any{"Amount": "-200000000000000000"}, true},
		{"native small", map[string]any{"Amount": "1000000"}, false},
		{"iou never invalid", map[string]any{"Amount": iou("99999999999999999999")}, false},
		{"mpt negative", map[string]any{"Amount": mpt("-1")}, true},
		{"mpt at cap", map[string]any{"Amount": mpt("9223372036854775807")}, false},
		{"mpt over cap", map[string]any{"Amount": mpt("9223372036854775808")}, true},
		{"non-amount field ignored", map[string]any{"Account": "100000000000000001"}, false},
		{"nested array object bad amount", map[string]any{
			"RawTransactions": []any{
				map[string]any{"RawTransaction": map[string]any{"Amount": "100000000000000001"}},
			},
		}, true},
		{"nested array object good amount", map[string]any{
			"RawTransactions": []any{
				map[string]any{"RawTransaction": map[string]any{"Amount": "1000000"}},
			},
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasInvalidAmount(tt.fields); got != tt.want {
				t.Fatalf("HasInvalidAmount(%v) = %v, want %v", tt.fields, got, tt.want)
			}
		})
	}
}

func nativeAmountBytes(drops uint64, positive bool) []byte {
	v := drops & 0x3FFF_FFFF_FFFF_FFFF
	if positive {
		v |= 0x4000_0000_0000_0000
	}
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func mptAmountBytes(mag uint64, positive bool) []byte {
	b := make([]byte, 33)
	b[0] = 0x20
	if positive {
		b[0] |= 0x40
	}
	binary.BigEndian.PutUint64(b[1:9], mag)
	return b
}

func TestAmountBytesInvalid(t *testing.T) {
	tests := []struct {
		name string
		v    []byte
		want bool
	}{
		{"native at cap", nativeAmountBytes(maxNativeN, true), false},
		{"native over cap", nativeAmountBytes(maxNativeN+1, true), true},
		{"iou 48 bytes", append([]byte{0x80}, make([]byte, 47)...), false},
		{"mpt at cap", mptAmountBytes(maxMPTAmount, true), false},
		{"mpt over cap", mptAmountBytes(maxMPTAmount+1, true), true},
		{"mpt negative", mptAmountBytes(1, false), true},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := amountBytesInvalid(tt.v); got != tt.want {
				t.Fatalf("amountBytesInvalid = %v, want %v", got, tt.want)
			}
		})
	}
}

// balanceFieldObject builds a minimal serialized STObject carrying a single
// sfBalance (Amount, field code 2) with the given native drops. The high field
// nibble 0x6 is Amount; the low nibble 0x2 is the Balance field code.
func balanceFieldObject(drops uint64) []byte {
	out := make([]byte, 0, 9)
	out = append(out, 0x62)
	return append(out, nativeAmountBytes(drops, true)...)
}

func TestHasInvalidAmountBinary(t *testing.T) {
	if hasInvalidAmountBinary(balanceFieldObject(maxNativeN)) {
		t.Fatalf("native at cap should be valid")
	}
	if !hasInvalidAmountBinary(balanceFieldObject(maxNativeN + 1)) {
		t.Fatalf("native over cap should be invalid")
	}
}

func TestCheckValidAmounts_Gating(t *testing.T) {
	badEntry := []InvariantEntry{{
		EntryType: entry.TypeAccountRoot,
		After:     balanceFieldObject(maxNativeN + 1),
	}}

	// Pre-amendment: the condition is only logged, never fatal.
	if v := checkValidAmounts(badEntry, amendment.EmptyRules()); v != nil {
		t.Fatalf("pre-amendment must not fail, got %v", v)
	}

	on := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_2_0})
	if v := checkValidAmounts(badEntry, on); v == nil {
		t.Fatalf("post-amendment must flag a non-canonical amount")
	}

	// A canonical entry passes even with the amendment on.
	goodEntry := []InvariantEntry{{EntryType: entry.TypeAccountRoot, After: balanceFieldObject(maxNativeN)}}
	if v := checkValidAmounts(goodEntry, on); v != nil {
		t.Fatalf("canonical entry must pass, got %v", v)
	}

	// Deleted entries are never scanned (rippled visitEntry skips them).
	del := []InvariantEntry{{EntryType: entry.TypeAccountRoot, IsDelete: true, Before: balanceFieldObject(maxNativeN + 1)}}
	if v := checkValidAmounts(del, on); v != nil {
		t.Fatalf("deleted entry must be skipped, got %v", v)
	}
}

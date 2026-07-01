package invariants

import "testing"

// TestIsXRPCurrency pins the exact badCurrency boundary: only the all-zero
// (native) currency and rippled's badCurrency() — bytes 12-14 "XRP", every
// other byte zero (UintTypes.cpp:132-137) — count as XRP. A currency whose
// bytes 12-14 merely spell "XRP" beside non-zero bytes is a valid IOU
// (mainnet "Wellgistics XRP", ledger 99342512).
func TestIsXRPCurrency(t *testing.T) {
	tests := []struct {
		name string
		curr string
		want bool
	}{
		{"empty string", "", true},
		{"literal XRP", "XRP", true},
		{"three NUL bytes", "\x00\x00\x00", true},
		{"all-zero hex", "0000000000000000000000000000000000000000", true},
		{"badCurrency hex", "0000000000000000000000005852500000000000", true},
		{"Wellgistics XRP mainnet IOU", "57656C6C67697374696373205852500000000000", false},
		{"Wellgistics XRP lowercase hex", "57656c6c67697374696373205852500000000000", false},
		{"XRP at 12-14 with non-zero suffix", "0000000000000000000000005852500000000001", false},
		{"XRP at 12-14 with non-zero prefix byte", "0100000000000000000000005852500000000000", false},
		{"noCurrency sentinel", "1", false},
		{"ISO USD", "USD", false},
		{"40 chars but invalid hex", "ZZ00000000000000000000005852500000000000", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isXRPCurrency(tt.curr); got != tt.want {
				t.Errorf("isXRPCurrency(%q) = %v, want %v", tt.curr, got, tt.want)
			}
		})
	}
}

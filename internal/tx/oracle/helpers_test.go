package oracle

import "testing"

func TestCurrencyCodeForms(t *testing.T) {
	usd := [20]byte{}
	copy(usd[12:], "USD")

	tests := []struct {
		name  string
		asset string
		want  [20]byte
	}{
		{"xrp", "XRP", [20]byte{}},
		{"noCurrency sentinel", "1", [20]byte{19: 0x01}},
		{"noCurrency hex", "0000000000000000000000000000000000000001", [20]byte{19: 0x01}},
		{"iso", "USD", usd},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := currencyCode(tt.asset); got != tt.want {
				t.Fatalf("currencyCode(%q) = %x, want %x", tt.asset, got, tt.want)
			}
		})
	}
}

// The binary codec renders the noCurrency code as "1" while the SLE parser
// renders the same bytes as 40-char hex; both must reduce to the same Currency,
// and that Currency (0x00…01) must sort after XRP (all-zero) — matching rippled's
// std::pair<Currency,Currency> byte order. The pre-fix code had no "1" case, so
// it collapsed the sentinel onto the all-zero code and tied it with XRP.
func TestCurrencyOrderKeyNoCurrencyAfterXRP(t *testing.T) {
	if currencyCode("1") != currencyCode("0000000000000000000000000000000000000001") {
		t.Fatal(`currencyCode("1") must equal its 40-char hex form`)
	}
	if !(currencyOrderKey("XRP", "USD") < currencyOrderKey("1", "USD")) {
		t.Fatal("XRP must order before the noCurrency sentinel")
	}
}

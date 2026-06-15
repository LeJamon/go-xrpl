package keylet

import "testing"

// The handlers, ledger/state, subscription, and amm packages all route their
// currency validation through keylet (ParseCurrency / IsValidCurrencyCode /
// CurrencyBytes) and the NoCurrency / BadCurrency sentinels. These tests pin the
// reserved values and the validate-and-encode contract so a future change to the
// canonical rules can't silently diverge from rippled's to_currency.

func TestCurrencySentinels(t *testing.T) {
	if NoCurrency != ([20]byte{19: 0x01}) {
		t.Errorf("NoCurrency = %x, want trailing 0x01", NoCurrency)
	}
	if BadCurrency != ([20]byte{12: 'X', 13: 'R', 14: 'P'}) {
		t.Errorf("BadCurrency = %x, want bytes 12-14 = XRP", BadCurrency)
	}
	// to_currency yields NoCurrency for any malformed code.
	if got := CurrencyBytes("!!"); got != NoCurrency {
		t.Errorf("CurrencyBytes(malformed) = %x, want NoCurrency", got)
	}
}

func TestIsValidCurrencyCode(t *testing.T) {
	valid := []string{
		"",    // native
		"XRP", // native
		"USD", // ISO
		"xrp", // lowercase ISO (not the native short-circuit)
		"$%^", // ISO special chars
		"0123456789012345678901234567890123456789", // 40 hex
		"5852500000000000000000000000000000000000", // 40 hex spelling a reserved value (still well-formed)
	}
	for _, c := range valid {
		if !IsValidCurrencyCode(c) {
			t.Errorf("IsValidCurrencyCode(%q) = false, want true", c)
		}
	}

	invalid := []string{
		"US",     // too short
		"USDD",   // too long
		"US D",   // 3 chars but space is outside isoCharSet
		"US\x01", // non-printable
		"123456789012345678901234567890123456789",   // 39 hex
		"0123456789012345678901234567890123456789Z", // 41 chars
		"00000000000000000000000000000000000000zz",  // 40 chars but not hex
	}
	for _, c := range invalid {
		if IsValidCurrencyCode(c) {
			t.Errorf("IsValidCurrencyCode(%q) = true, want false", c)
		}
	}
}

func TestParseCurrency(t *testing.T) {
	// Reserved sentinels expressed as 40-hex are well-formed but rejected.
	const noCurrencyHex = "0000000000000000000000000000000000000001"
	const badCurrencyHex = "0000000000000000000000005852500000000000"

	rejected := []string{
		noCurrencyHex,
		badCurrencyHex,
		"US",   // malformed length
		"US D", // malformed char
		"00000000000000000000000000000000000000zz", // malformed hex
	}
	for _, c := range rejected {
		if _, err := ParseCurrency(c); err == nil {
			t.Errorf("ParseCurrency(%q) err = nil, want error", c)
		}
	}

	// For every accepted code ParseCurrency must agree byte-for-byte with the
	// canonical encoder, since callers used to re-derive both halves.
	accepted := []string{"", "XRP", "USD", "$%^", "0123456789012345678901234567890123456789"}
	for _, c := range accepted {
		got, err := ParseCurrency(c)
		if err != nil {
			t.Errorf("ParseCurrency(%q) err = %v, want nil", c, err)
			continue
		}
		if want := CurrencyBytes(c); got != want {
			t.Errorf("ParseCurrency(%q) = %x, CurrencyBytes = %x", c, got, want)
		}
	}
}

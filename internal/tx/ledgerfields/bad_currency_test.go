package ledgerfields

import (
	"bytes"
	"strings"
	"testing"
)

func rawBadCurrencyRippleState(t *testing.T) []byte {
	t.Helper()

	amount := func(issuer string) map[string]any {
		return map[string]any{"value": "0", "currency": "USD", "issuer": issuer}
	}
	entry := &RippleState{}
	entry.SetFlags(0)
	entry.SetBalance(amount("rrrrrrrrrrrrrrrrrrrrBZbvji"))
	entry.SetLowLimit(amount(fxAccount))
	entry.SetHighLimit(amount(fxAccount))
	entry.SetPreviousTxnID(strings.Repeat("01", 32))
	entry.SetPreviousTxnLgrSeq(1)
	encoded, err := entry.Encode()
	if err != nil {
		t.Fatalf("Encode RippleState: %v", err)
	}

	if count := bytes.Count(encoded, usdCurrencyBytes); count != 3 {
		t.Fatalf("USD currency payload count = %d, want 3", count)
	}
	return bytes.ReplaceAll(encoded, usdCurrencyBytes, badCurrencyBytes)
}

func TestRippleStateDecodeAllowsBinaryBadCurrency(t *testing.T) {
	raw := rawBadCurrencyRippleState(t)

	amountOffset := bytes.Index(raw, badCurrencyBytes) - 8
	if amountOffset < 0 {
		t.Fatal("badCurrency amount not found")
	}
	if _, err := newStreamReader(raw[amountOffset:]).readAmountAny(); err == nil {
		t.Fatal("generic amount decoder accepted badCurrency")
	}

	var decoded RippleState
	if err := decoded.Decode(raw); err != nil {
		t.Fatalf("Decode RippleState: %v", err)
	}
	for name, value := range map[string]any{
		"Balance": decoded.Balance, "LowLimit": decoded.LowLimit, "HighLimit": decoded.HighLimit,
	} {
		amount, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("%s decoded as %T, want amount map", name, value)
		}
		if amount["currency"] != badCurrencyHex {
			t.Errorf("%s currency = %v, want %s", name, amount["currency"], badCurrencyHex)
		}
	}

	roundTrip, err := decoded.Encode()
	if err != nil {
		t.Fatalf("Encode decoded RippleState: %v", err)
	}
	if !bytes.Equal(roundTrip, raw) {
		t.Fatal("binary badCurrency RippleState did not round-trip")
	}

	fresh := &RippleState{}
	fresh.SetFlags(0)
	badAmount := map[string]any{"value": "0", "currency": badCurrencyHex, "issuer": fxAccount}
	fresh.SetBalance(badAmount)
	fresh.SetLowLimit(badAmount)
	fresh.SetHighLimit(badAmount)
	if _, err := fresh.Encode(); err == nil {
		t.Fatal("fresh RippleState writer accepted badCurrency")
	}
}

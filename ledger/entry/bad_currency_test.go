package entry

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func TestReservedCurrencyLedgerEntryRoundTrip(t *testing.T) {
	noCurrency := [20]byte{19: 1}
	for _, tc := range []struct {
		entry, currency string
		count           int
	}{
		{"AMM", fxCurrUSD, 1}, // Asset2
		{"AMM", "039C99CD9AB0B70B32ECDA51EAAE471625608EA2", 2}, // LPTokenBalance and AuctionSlot.Price
		{"Bridge", fxCurrUSD, 1},                               // XChainBridge.IssuingChainIssue
		{"Oracle", fxCurrUSD, 1},                               // PriceDataSeries[].PriceData.BaseAsset
	} {
		t.Run(tc.entry+"/"+tc.currency, func(t *testing.T) {
			data := fixtureBlob(t, tc.entry)
			currency, err := hex.DecodeString(tc.currency)
			if err != nil {
				t.Fatal(err)
			}
			if count := bytes.Count(data, currency); count != tc.count {
				t.Fatalf("fixture contains %d currency fields, want %d", count, tc.count)
			}
			wire := bytes.ReplaceAll(data, currency, noCurrency[:])
			e := NewByName(tc.entry)
			if err := e.Decode(wire); err != nil {
				t.Fatal(err)
			}
			requireEncodeParity(t, e)
			got, err := e.(encodeIface).Encode()
			if err != nil || !bytes.Equal(got, wire) {
				t.Fatalf("Encode() = %X, %v; want %X", got, err, wire)
			}
		})
	}
}

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
	generic, err := newStreamReader(raw[amountOffset:]).readAmountAny()
	if err != nil {
		t.Fatalf("generic amount decoder: %v", err)
	}
	genericAmount, ok := generic.(map[string]any)
	if !ok || genericAmount["currency"] != badCurrencyHex {
		t.Fatalf("generic amount = %#v, want badCurrency", generic)
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
	freshData, err := fresh.Encode()
	if err != nil {
		t.Fatalf("encode fresh badCurrency RippleState: %v", err)
	}
	if count := bytes.Count(freshData, badCurrencyBytes); count != 3 {
		t.Fatalf("fresh badCurrency payload count = %d, want 3", count)
	}
}

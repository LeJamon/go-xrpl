package state

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

func TestDecodeLedgerAmountTypedValues(t *testing.T) {
	cases := []struct {
		name       string
		value      any
		wantValue  string
		wantCurr   string
		wantIssuer string
		wantMPT    string
		wantRaw    int64
	}{
		{
			name:      "xrp",
			value:     ledgerfields.AmountValue{Value: "100000000000000000"},
			wantValue: "100000000000000000",
		},
		{
			name:       "iou",
			value:      ledgerfields.AmountValue{Value: "1234567.89", Currency: "USD", Issuer: testIssuer},
			wantValue:  "1234567.89",
			wantCurr:   "USD",
			wantIssuer: testIssuer,
		},
		{
			name:       "no currency sentinel",
			value:      map[string]any{"value": "1.25", "currency": "1", "issuer": testIssuer},
			wantValue:  "1.25",
			wantCurr:   "1",
			wantIssuer: testIssuer,
		},
		{
			name:      "mpt",
			value:     ledgerfields.AmountValue{Value: "-9223372036854775807", MPTIssuanceID: strings.ToLower(testMPTID)},
			wantValue: "-9223372036854775807",
			wantMPT:   testMPTID,
			wantRaw:   -9223372036854775807,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeLedgerAmount("test.amount", tt.value)
			if err != nil {
				t.Fatalf("decodeLedgerAmount: %v", err)
			}
			if got.Value() != tt.wantValue {
				t.Fatalf("value = %q, want %q", got.Value(), tt.wantValue)
			}
			if got.Currency != tt.wantCurr {
				t.Fatalf("currency = %q, want %q", got.Currency, tt.wantCurr)
			}
			if got.Issuer != tt.wantIssuer {
				t.Fatalf("issuer = %q, want %q", got.Issuer, tt.wantIssuer)
			}
			if tt.wantMPT == "" {
				if got.IsMPT() {
					t.Fatalf("IsMPT = true, want false")
				}
				return
			}
			if !got.IsMPT() || got.MPTIssuanceID() != tt.wantMPT {
				t.Fatalf("MPT issuance ID = %q, want %q", got.MPTIssuanceID(), tt.wantMPT)
			}
			raw, ok := got.MPTRaw()
			if !ok || raw != tt.wantRaw {
				t.Fatalf("MPTRaw = %d/%v, want %d/true", raw, ok, tt.wantRaw)
			}
		})
	}
}

func TestDecodeLedgerAmountPreservesDecodedIOURounding(t *testing.T) {
	value := map[string]any{
		"value":    "18446744073709551615",
		"currency": "USD",
		"issuer":   testIssuer,
	}
	got, err := decodeLedgerAmount("Offer.TakerPays", value)
	if err != nil {
		t.Fatalf("decodeLedgerAmount: %v", err)
	}
	if got.Value() != "1844674407370955e4" {
		t.Fatalf("value = %q, want 1844674407370955e4", got.Value())
	}
}

func TestDecodeLedgerAmountRejectsMalformedTypedValues(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{
			name:  "native overflow",
			value: ledgerfields.AmountValue{Value: "100000000000000001"},
			want:  "Native currency amount out of range",
		},
		{
			name:  "fractional mpt",
			value: ledgerfields.AmountValue{Value: "1.5", MPTIssuanceID: testMPTID},
			want:  "XRP and MPT must be specified as integral amount.",
		},
		{
			name:  "mpt overflow",
			value: map[string]any{"value": "9223372036854775808", "mpt_issuance_id": testMPTID},
			want:  "MPT amount out of range",
		},
		{
			name:  "empty mpt issuance ID",
			value: map[string]any{"value": "1", "mpt_issuance_id": ""},
			want:  "MPT issuance ID is empty",
		},
		{
			name:  "issued amount missing issuer",
			value: ledgerfields.AmountValue{Value: "1", Currency: "USD"},
			want:  "Invalid Asset's Json specification",
		},
		{
			name:  "unsupported decoded type",
			value: uint32(1),
			want:  "amount has unsupported type uint32",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeLedgerAmount("Check.SendMax", tt.value); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestDecodeLedgerAmountMatchesExistingArithmetic(t *testing.T) {
	iou := func(value, currency, issuer string) any {
		return map[string]any{"value": value, "currency": currency, "issuer": issuer}
	}
	mpt := func(value string) any {
		return map[string]any{"value": value, "mpt_issuance_id": testMPTID}
	}
	cases := map[string]any{
		"native zero": "0", "native negative": "-100000000000000000",
		"native maximum": "100000000000000000", "native overflow": "100000000000000001",
		"iou zero":                iou("0", "USD", testIssuer),
		"iou minimum":             iou("1e-81", "USD", testIssuer),
		"iou underflow":           iou("1e-82", "USD", testIssuer),
		"iou maximum":             iou("9.999999999999999e95", "USD", testIssuer),
		"iou overflow":            iou("1e96", "USD", testIssuer),
		"iou even rounding":       iou("10000000000000005", "USD", testIssuer),
		"iou odd rounding":        iou("10000000000000015", "USD", testIssuer),
		"iou negative rounding":   iou("-10000000000000015", "USD", testIssuer),
		"hex currency and issuer": iou("3.14", strings.Repeat("A1", 20), strings.Repeat("01", 20)),
		"account one issuer":      iou("1", "USD", AccountOneAddress),
		"bad currency sentinel":   iou("1", badCurrencyHex, testIssuer),
		"mpt zero":                mpt("0"), "mpt maximum": mpt("9223372036854775807"),
		"mpt negative": mpt("-9223372036854775807"), "mpt overflow": mpt("9223372036854775808"),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			want, wantErr := AmountFromJSON(raw)
			got, gotErr := decodeLedgerAmount("Amount", value)
			if (wantErr == nil) != (gotErr == nil) {
				t.Fatalf("errors differ: prior %v, direct %v", wantErr, gotErr)
			}
			if wantErr == nil && !reflect.DeepEqual(want, got) {
				t.Fatalf("amount differs: prior %#v, direct %#v", want, got)
			}
		})
	}
}

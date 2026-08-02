package handlers

import (
	"encoding/json"
	"reflect"
	"testing"

	binarycodecdefs "github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
)

func TestSerializedFieldParseMessageLeafValidation(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		value   any
		message string
	}{
		{
			name:    "UInt32 bad type",
			field:   "Sequence",
			value:   []any{},
			message: "Field 'tx_json.Sequence' has bad type.",
		},
		{
			name:    "UInt32 invalid string data",
			field:   "Sequence",
			value:   "4294967296",
			message: "Field 'tx_json.Sequence' has invalid data.",
		},
		{
			name:    "UInt8 out of range",
			field:   "CloseResolution",
			value:   json.Number("256"),
			message: "Field 'tx_json.CloseResolution' is out of range.",
		},
		{
			name:    "transaction type does not accept ledger entry name",
			field:   "TransactionType",
			value:   "AccountRoot",
			message: "Field 'tx_json.TransactionType' has invalid data.",
		},
		{
			name:    "UInt16 string does not accept leading plus",
			field:   "SignerWeight",
			value:   "+1",
			message: "Field 'tx_json.SignerWeight' has invalid data.",
		},
		{
			name:    "hash bad type",
			field:   "InvoiceID",
			value:   []any{},
			message: "Field 'tx_json.InvoiceID' has bad type.",
		},
		{
			name:    "hash invalid data",
			field:   "InvoiceID",
			value:   "not-hex",
			message: "Field 'tx_json.InvoiceID' has invalid data.",
		},
		{
			name:    "blob bad type",
			field:   "Domain",
			value:   []any{},
			message: "Field 'tx_json.Domain' has bad type.",
		},
		{
			name:    "blob invalid data",
			field:   "Domain",
			value:   "GG",
			message: "Field 'tx_json.Domain' has invalid data.",
		},
		{
			name:    "account bad type",
			field:   "Destination",
			value:   []any{},
			message: "Field 'tx_json.Destination' has bad type.",
		},
		{
			name:    "account invalid data",
			field:   "Destination",
			value:   "not-an-account",
			message: "Field 'tx_json.Destination' has invalid data.",
		},
		{
			name:    "amount invalid data",
			field:   "Amount",
			value:   []any{},
			message: "Field 'tx_json.Amount' has invalid data.",
		},
		{
			name:  "numeric amount",
			field: "Fee",
			value: json.Number("50"),
		},
		{
			name:    "numeric amount outside JSON uint range",
			field:   "Fee",
			value:   json.Number("4294967296"),
			message: "Field 'tx_json.Fee' has invalid data.",
		},
		{
			name:    "numeric amount exponent",
			field:   "Fee",
			value:   json.Number("1e2"),
			message: "Field 'tx_json.Fee' has invalid data.",
		},
		{
			name:  "string amount integral exponent",
			field: "Fee",
			value: "1e2",
		},
		{
			name:    "string amount leading zero",
			field:   "Fee",
			value:   "010",
			message: "Field 'tx_json.Fee' has invalid data.",
		},
	}

	defs := binarycodecdefs.Get()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			object := map[string]any{test.field: test.value}
			if got := serializedFieldParseMessage(object, "tx_json", defs); got != test.message {
				t.Fatalf("message = %q, want %q", got, test.message)
			}
		})
	}
}

func TestSerializedFieldParseMessageCanonicalizesNativeAmount(t *testing.T) {
	object := map[string]any{"Fee": "1e2"}
	if message := serializedFieldParseMessage(object, "tx_json", binarycodecdefs.Get()); message != "" {
		t.Fatalf("message = %q, want success", message)
	}
	if got := object["Fee"]; got != "100" {
		t.Fatalf("Fee = %#v, want canonical drops string", got)
	}
}

func TestSerializedFieldParseMessageCanonicalizesIssuedAmountString(t *testing.T) {
	object := map[string]any{"Amount": "1/USD/rMRxj8jED6ZCjtjgFxB4cz1MGVNtYqCEyS"}
	if message := serializedFieldParseMessage(object, "tx_json", binarycodecdefs.Get()); message != "" {
		t.Fatalf("message = %q, want success", message)
	}
	want := map[string]any{
		"value":    "1",
		"currency": "USD",
		"issuer":   "rMRxj8jED6ZCjtjgFxB4cz1MGVNtYqCEyS",
	}
	if got := object["Amount"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("Amount = %#v, want %#v", got, want)
	}
}

func TestSerializedFieldParseMessageInnerTemplate(t *testing.T) {
	object := map[string]any{
		"Signers": []any{
			map[string]any{
				"Signer": map[string]any{
					"SigningPubKey": "",
					"TxnSignature":  "",
				},
			},
		},
	}
	want := "Error at 'tx_json.Signers.[0].Signer'. Object 'Signer' contents did not meet requirements for that type."
	if got := serializedFieldParseMessage(object, "tx_json", binarycodecdefs.Get()); got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

func TestSerializedFieldParseMessageRejectsInvalidArrayWrappers(t *testing.T) {
	tests := []struct {
		name    string
		wrapper map[string]any
		want    string
	}{
		{
			name: "multiple wrappers",
			wrapper: map[string]any{
				"Memo":   map[string]any{},
				"Signer": map[string]any{},
			},
			want: "Field 'tx_json.Memos[0]' must be an object with a single key/object value.",
		},
		{
			name:    "non STObject wrapper",
			wrapper: map[string]any{"Account": map[string]any{}},
			want: "Item 'tx_json.Memos.[0].Account' at index 0 is not an object.  " +
				"Arrays may only contain objects.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			object := map[string]any{"Memos": []any{test.wrapper}}
			if got := serializedFieldParseMessage(object, "tx_json", binarycodecdefs.Get()); got != test.want {
				t.Fatalf("message = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSerializedFieldParseMessageCanonicalizesInnerDiscardable(t *testing.T) {
	entry := map[string]any{
		"Account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"hash":         "0000000000000000000000000000000000000000000000000000000000000000",
		"SignerWeight": json.Number("1"),
	}
	object := map[string]any{
		"SignerEntries": []any{map[string]any{"SignerEntry": entry}},
	}
	if message := serializedFieldParseMessage(object, "tx_json", binarycodecdefs.Get()); message != "" {
		t.Fatalf("message = %q, want success", message)
	}
	if _, ok := entry["hash"]; ok {
		t.Fatal("discardable hash was not removed")
	}
}

func TestSerializedFieldParseMessageUsesSharedDefaultStyle(t *testing.T) {
	object := map[string]any{
		"PriceDataSeries": []any{map[string]any{
			"PriceData": map[string]any{
				"BaseAsset":  "XRP",
				"QuoteAsset": "USD",
				"Scale":      json.Number("0"),
			},
		}},
	}
	want := "Error at 'tx_json.PriceDataSeries.[0].PriceData'. " +
		"Object 'PriceData' contents did not meet requirements for that type."
	if got := serializedFieldParseMessage(object, "tx_json", binarycodecdefs.Get()); got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

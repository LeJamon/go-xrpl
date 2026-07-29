package handlers

import (
	"encoding/json"
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

func TestSerializedFieldParseMessageSponsorSignatureTemplate(t *testing.T) {
	object := map[string]any{
		"SponsorSignature": map[string]any{
			"SigningPubKey": "",
			"Sequence":      1,
		},
	}
	want := "Object 'SponsorSignature' contents did not meet requirements for that type."
	if got := serializedFieldParseMessage(object, "tx_json", binarycodecdefs.Get()); got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

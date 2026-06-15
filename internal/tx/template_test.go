package tx

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// encodeTx encodes a field map into a binary transaction blob.
func encodeTx(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	hexStr, err := binarycodec.Encode(fields)
	if err != nil {
		t.Fatalf("binarycodec.Encode(%v): %v", fields, err)
	}
	blob, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	return blob
}

const (
	testAccount     = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	testDestination = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
)

// baseCommon returns the common fields every transaction carries, so a tx blob
// can be built by adding only the type-specific fields under test.
func baseCommon(txType string) map[string]any {
	return map[string]any{
		"TransactionType": txType,
		"Account":         testAccount,
		"Sequence":        uint32(1),
		"Fee":             "10",
		"SigningPubKey":   "",
	}
}

// TestParseFromBinary_DisallowedField verifies that a binary transaction
// carrying a codec-known field that is not in its type's template is rejected
// at parse, before it can be applied — matching rippled's STTx template
// application ("found in disallowed location").
func TestParseFromBinary_DisallowedField(t *testing.T) {
	cases := []struct {
		name          string
		txType        string
		disallowedKey string
		disallowedVal any
	}{
		{
			name:          "Payment carrying NFTokenID",
			txType:        "Payment",
			disallowedKey: "NFTokenID",
			disallowedVal: "00000000000000000000000000000000000000000000000000000000DEADBEEF",
		},
		{
			name:          "OfferCreate carrying Destination",
			txType:        "OfferCreate",
			disallowedKey: "Destination",
			disallowedVal: testDestination,
		},
		{
			name:          "AccountSet carrying Amount",
			txType:        "AccountSet",
			disallowedKey: "Amount",
			disallowedVal: "1000000",
		},
		{
			name:          "TrustSet carrying TakerPays",
			txType:        "TrustSet",
			disallowedKey: "TakerPays",
			disallowedVal: "1000000",
		},
		{
			name:          "EscrowFinish carrying Destination",
			txType:        "EscrowFinish",
			disallowedKey: "Destination",
			disallowedVal: testDestination,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := baseCommon(tc.txType)
			fields[tc.disallowedKey] = tc.disallowedVal
			blob := encodeTx(t, fields)

			_, err := ParseFromBinary(blob)
			if err == nil {
				t.Fatalf("expected parse to reject %s carrying %s, got nil error",
					tc.txType, tc.disallowedKey)
			}
			re, ok := ter.AsResultError(err)
			if !ok {
				t.Fatalf("expected a ResultError, got %T: %v", err, err)
			}
			if re.Code != ter.TemMALFORMED {
				t.Fatalf("expected TemMALFORMED, got %v: %v", re.Code, err)
			}
		})
	}
}

// TestParseFromBinary_AllowedFields verifies that transactions carrying only
// their own template fields plus the common fields parse successfully.
func TestParseFromBinary_AllowedFields(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]any
	}{
		{
			name: "Payment with template fields",
			fields: func() map[string]any {
				f := baseCommon("Payment")
				f["Destination"] = testDestination
				f["Amount"] = "1000000"
				f["DestinationTag"] = uint32(42)
				return f
			}(),
		},
		{
			name: "OfferCreate with template fields",
			fields: func() map[string]any {
				f := baseCommon("OfferCreate")
				f["TakerPays"] = "1000000"
				f["TakerGets"] = "2000000"
				f["Expiration"] = uint32(100)
				return f
			}(),
		},
		{
			name: "Payment with common optional fields",
			fields: func() map[string]any {
				f := baseCommon("Payment")
				f["Destination"] = testDestination
				f["Amount"] = "1000000"
				f["SourceTag"] = uint32(7)
				f["LastLedgerSequence"] = uint32(500)
				f["Memos"] = []any{}
				return f
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blob := encodeTx(t, tc.fields)
			if _, err := ParseFromBinary(blob); err != nil {
				t.Fatalf("expected parse to succeed, got: %v", err)
			}
		})
	}
}

// TestCheckTemplate_CommonFieldsAllowedForEveryType verifies that the field
// allowlist never rejects a transaction that carries only common fields,
// across every transaction type in the table. This guards against an omission
// in a type's template that would spuriously reject otherwise-valid traffic.
func TestCheckTemplate_CommonFieldsAllowedForEveryType(t *testing.T) {
	present := make(map[string]bool, len(commonFields))
	for name := range commonFields {
		present[name] = true
	}
	for txType := range txTemplates {
		if err := checkTemplate(txType, present); err != nil {
			t.Errorf("common fields must be allowed for %s, got: %v", txType, err)
		}
	}
}

// TestCheckTemplate_PseudoTransactions verifies the pseudo-transaction templates
// accept their own fields and reject foreign ones, so pseudo-tx handling is not
// broken by the allowlist.
func TestCheckTemplate_PseudoTransactions(t *testing.T) {
	t.Run("EnableAmendment allowed", func(t *testing.T) {
		present := map[string]bool{
			"TransactionType": true,
			"Account":         true,
			"Sequence":        true,
			"Fee":             true,
			"SigningPubKey":   true,
			"LedgerSequence":  true,
			"Amendment":       true,
		}
		if err := checkTemplate(TypeAmendment, present); err != nil {
			t.Fatalf("EnableAmendment fields must be allowed, got: %v", err)
		}
	})

	t.Run("SetFee rejects foreign field", func(t *testing.T) {
		present := map[string]bool{
			"TransactionType": true,
			"Account":         true,
			"BaseFeeDrops":    true,
			"Amount":          true, // not in SetFee template
		}
		err := checkTemplate(TypeFee, present)
		if err == nil {
			t.Fatal("SetFee carrying Amount must be rejected")
		}
		if re, ok := ter.AsResultError(err); !ok || re.Code != ter.TemMALFORMED {
			t.Fatalf("expected TemMALFORMED, got: %v", err)
		}
	})

	t.Run("UNLModify allowed", func(t *testing.T) {
		present := map[string]bool{
			"TransactionType":    true,
			"Account":            true,
			"UNLModifyDisabling": true,
			"LedgerSequence":     true,
			"UNLModifyValidator": true,
		}
		if err := checkTemplate(TypeUNLModify, present); err != nil {
			t.Fatalf("UNLModify fields must be allowed, got: %v", err)
		}
	})
}

// TestCheckTemplate_UnknownTypeNotEnforced verifies that an unrecognized
// transaction type is passed through without template enforcement (type
// resolution is the registry's responsibility).
func TestCheckTemplate_UnknownTypeNotEnforced(t *testing.T) {
	if err := checkTemplate(TypeInvalid, map[string]bool{"Anything": true}); err != nil {
		t.Fatalf("unknown type must not be template-enforced, got: %v", err)
	}
}

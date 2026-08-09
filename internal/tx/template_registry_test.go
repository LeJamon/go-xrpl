package tx_test

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
)

func TestConfidentialTypeRegistration(t *testing.T) {
	all.RegisterAll()
	for _, txType := range []tx.Type{
		tx.TypeConfidentialMPTConvert,
		tx.TypeConfidentialMPTMergeInbox,
		tx.TypeConfidentialMPTConvertBack,
		tx.TypeConfidentialMPTSend,
		tx.TypeConfidentialMPTClawback,
	} {
		if _, err := tx.NewFromType(txType); err != nil {
			t.Fatalf("NewFromType(%s) error = %v", txType, err)
		}
	}
}

func TestConfidentialRequiredWireFields(t *testing.T) {
	all.RegisterAll()
	tests := []string{
		`{"Account":"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK","TransactionType":"ConfidentialMPTConvert","MPTokenIssuanceID":"00000001ABCDEF0123456789ABCDEF0123456789ABCDEF12","HolderEncryptedAmount":"00","IssuerEncryptedAmount":"00","BlindingFactor":"00"}`,
		`{"Account":"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK","TransactionType":"ConfidentialMPTConvert","MPTokenIssuanceID":"00000001ABCDEF0123456789ABCDEF0123456789ABCDEF12","MPTAmount":"0","HolderEncryptedAmount":"00","IssuerEncryptedAmount":"00"}`,
		`{"Account":"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK","TransactionType":"ConfidentialMPTConvertBack","MPTokenIssuanceID":"00000001ABCDEF0123456789ABCDEF0123456789ABCDEF12","HolderEncryptedAmount":"00","IssuerEncryptedAmount":"00","BlindingFactor":"00","ZKProof":"00","BalanceCommitment":"00"}`,
	}
	for i, data := range tests {
		transaction, err := tx.ParseJSON([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		err = transaction.Validate()
		result, ok := ter.AsResultError(err)
		if !ok || result.Code != ter.TemMALFORMED {
			t.Fatalf("case %d %T Validate() = %v, want temMALFORMED", i, transaction, err)
		}
	}
}

// TestParseFromBinary_EveryRegisteredTypeAcceptsCommonFields verifies that the
// per-type field allowlist never rejects a transaction carrying only common
// fields, for every transaction type registered with the engine. A registered
// type missing a template entry, or a template that fails to admit the common
// fields, would surface here as a spurious parse rejection.
func TestParseFromBinary_EveryRegisteredTypeAcceptsCommonFields(t *testing.T) {
	all.RegisterAll()

	types := tx.SupportedTypes()
	if len(types) == 0 {
		t.Fatal("no transaction types registered")
	}
	formats := tx.FormatTemplates()

	for _, txType := range types {
		t.Run(txType.String(), func(t *testing.T) {
			if _, ok := formats[txType.String()]; !ok {
				t.Fatalf("registered transaction type %s has no template", txType)
			}
			fields := map[string]any{
				"TransactionType": txType.String(),
				"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"Sequence":        uint32(1),
				"Fee":             "10",
				"SigningPubKey":   "",
			}
			hexStr, err := binarycodec.Encode(fields)
			if err != nil {
				t.Fatalf("encode %s: %v", txType, err)
			}
			blob, err := hex.DecodeString(hexStr)
			if err != nil {
				t.Fatalf("hex decode: %v", err)
			}

			// The minimal common-fields blob may still be rejected by the
			// type's own Validate() for missing required fields, but it must
			// never be rejected by the template allowlist for a disallowed
			// field.
			_, err = tx.ParseFromBinary(blob)
			if err != nil && strings.Contains(err.Error(), "found in disallowed location") {
				t.Fatalf("common-fields blob spuriously rejected for %s: %v", txType, err)
			}
		})
	}
}

func TestParseAndPrepareVaultCreateWithScale(t *testing.T) {
	all.RegisterAll()

	hexStr, err := binarycodec.Encode(map[string]any{
		"TransactionType": "VaultCreate",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Sequence":        uint32(1),
		"Fee":             "10",
		"SigningPubKey":   "",
		"Asset": map[string]any{
			"currency": "USD",
			"issuer":   "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		},
		"Scale": uint8(9),
	})
	if err != nil {
		t.Fatalf("encode VaultCreate: %v", err)
	}
	blob, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("hex decode: %v", err)
	}

	parsed, err := txengine.ParseAndPrepare(blob)
	if err != nil {
		t.Fatalf("ParseAndPrepare: %v", err)
	}
	create, ok := parsed.Transaction.(*vault.VaultCreate)
	if !ok {
		t.Fatalf("transaction type = %T, want *vault.VaultCreate", parsed.Transaction)
	}
	if create.Scale == nil || *create.Scale != 9 {
		t.Fatalf("Scale = %v, want 9", create.Scale)
	}
	if !bytes.Equal(create.GetRawBytes(), blob) || !bytes.Equal(parsed.RawBlob, blob) {
		t.Fatal("ParseAndPrepare did not preserve the serialized transaction")
	}
}

func TestParseFromBinaryDynamicMPTFields(t *testing.T) {
	all.RegisterAll()

	cases := []struct {
		name   string
		fields map[string]any
	}{
		{
			name: "MPTokenIssuanceCreate ImmutableFlags",
			fields: map[string]any{
				"TransactionType": "MPTokenIssuanceCreate",
				"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"Sequence":        uint32(1),
				"Fee":             "10",
				"SigningPubKey":   "",
				"ImmutableFlags":  uint32(1),
			},
		},
		{
			name: "MPTokenIssuanceSet mutation fields",
			fields: map[string]any{
				"TransactionType":   "MPTokenIssuanceSet",
				"Account":           "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"Sequence":          uint32(1),
				"Fee":               "10",
				"SigningPubKey":     "",
				"MPTokenIssuanceID": "000000000000000000000000000000000000000000000001",
				"MPTokenMetadata":   "AA",
				"TransferFee":       uint16(1),
				"ImmutableFlags":    uint32(1),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hexStr, err := binarycodec.Encode(tc.fields)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			blob, err := hex.DecodeString(hexStr)
			if err != nil {
				t.Fatalf("hex decode: %v", err)
			}
			if _, err := tx.ParseFromBinary(blob); err != nil {
				t.Fatalf("ParseFromBinary: %v", err)
			}
		})
	}
}

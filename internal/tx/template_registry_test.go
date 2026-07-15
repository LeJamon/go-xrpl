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
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
)

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

	for _, txType := range types {
		t.Run(txType.String(), func(t *testing.T) {
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
			if err != nil && strings.Contains(err.Error(), "is not allowed for transaction type") {
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

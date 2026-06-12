package tx_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
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

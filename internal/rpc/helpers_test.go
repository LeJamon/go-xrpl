package rpc

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// InjectDeliveredAmount Tests
// Based on rippled src/test/rpc/DeliveredAmount_test.cpp and the
// RPC::insertDeliveredAmount / getDeliveredAmount logic in DeliveredAmount.cpp.
// The synthetic field is snake_case "delivered_amount"; the real serialized
// metadata field is PascalCase "DeliveredAmount" and is never altered.

// TestDeliveredAmountIneligibleTypeSkipped verifies that transaction types
// outside {Payment, CheckCash, AccountDelete} get no delivered_amount
// (canHaveDeliveredAmount, DeliveredAmount.cpp:93-95).
func TestDeliveredAmountIneligibleTypeSkipped(t *testing.T) {
	tests := []struct {
		name   string
		txType string
	}{
		{"OfferCreate", "OfferCreate"},
		{"OfferCancel", "OfferCancel"},
		{"TrustSet", "TrustSet"},
		{"AccountSet", "AccountSet"},
		{"EscrowCreate", "EscrowCreate"},
		{"NFTokenMint", "NFTokenMint"},
		{"SignerListSet", "SignerListSet"},
		{"empty string", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			txJSON := map[string]interface{}{
				"TransactionType": tc.txType,
				"Amount":          "1000000",
			}
			meta := map[string]interface{}{
				"TransactionResult": "tesSUCCESS",
			}

			handlers.InjectDeliveredAmount(txJSON, meta)

			_, has := meta["delivered_amount"]
			assert.False(t, has,
				"ineligible tx type %q should not get delivered_amount", tc.txType)
		})
	}
}

// TestDeliveredAmountEligibleTypes verifies the three eligible types each get a
// delivered_amount (here via the Amount fallback). AccountDelete carries no
// Amount field, so it falls through to "unavailable".
func TestDeliveredAmountEligibleTypes(t *testing.T) {
	t.Run("Payment", func(t *testing.T) {
		meta := map[string]interface{}{"TransactionResult": "tesSUCCESS"}
		handlers.InjectDeliveredAmount(
			map[string]interface{}{"TransactionType": "Payment", "Amount": "1000000"}, meta)
		assert.Equal(t, "1000000", meta["delivered_amount"])
	})

	t.Run("CheckCash", func(t *testing.T) {
		meta := map[string]interface{}{"TransactionResult": "tesSUCCESS"}
		handlers.InjectDeliveredAmount(
			map[string]interface{}{"TransactionType": "CheckCash", "Amount": "2000000"}, meta)
		assert.Equal(t, "2000000", meta["delivered_amount"])
	})

	t.Run("AccountDelete with no Amount falls back to unavailable", func(t *testing.T) {
		meta := map[string]interface{}{"TransactionResult": "tesSUCCESS"}
		handlers.InjectDeliveredAmount(
			map[string]interface{}{"TransactionType": "AccountDelete"}, meta)
		assert.Equal(t, "unavailable", meta["delivered_amount"])
	})
}

// TestDeliveredAmountFailedTxSkipped verifies that a non-tesSUCCESS result
// produces no delivered_amount (DeliveredAmount.cpp:101-103).
func TestDeliveredAmountFailedTxSkipped(t *testing.T) {
	for _, res := range []string{"tecUNFUNDED_PAYMENT", "tecPATH_PARTIAL", "tefPAST_SEQ"} {
		t.Run(res, func(t *testing.T) {
			meta := map[string]interface{}{"TransactionResult": res}
			handlers.InjectDeliveredAmount(
				map[string]interface{}{"TransactionType": "Payment", "Amount": "1000000"}, meta)
			_, has := meta["delivered_amount"]
			assert.False(t, has, "failed tx (%s) must not get delivered_amount", res)
		})
	}
}

// TestDeliveredAmountFromRealField verifies that the real (PascalCase)
// DeliveredAmount metadata field is copied to the synthetic snake_case
// delivered_amount and left in place (the partial-payment case).
func TestDeliveredAmountFromRealField(t *testing.T) {
	t.Run("XRP drops", func(t *testing.T) {
		txJSON := map[string]interface{}{"TransactionType": "Payment", "Amount": "5000000"}
		meta := map[string]interface{}{
			"TransactionResult": "tesSUCCESS",
			"DeliveredAmount":   "3000000",
		}

		handlers.InjectDeliveredAmount(txJSON, meta)

		assert.Equal(t, "3000000", meta["delivered_amount"],
			"delivered_amount should come from the real DeliveredAmount field, not Amount")
		assert.Equal(t, "3000000", meta["DeliveredAmount"],
			"the real DeliveredAmount field must be left untouched")
	})

	t.Run("IOU", func(t *testing.T) {
		iou := map[string]interface{}{
			"value":    "100",
			"currency": "USD",
			"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		}
		txJSON := map[string]interface{}{
			"TransactionType": "Payment",
			"Amount": map[string]interface{}{
				"value":    "500",
				"currency": "USD",
				"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
		}
		meta := map[string]interface{}{
			"TransactionResult": "tesSUCCESS",
			"DeliveredAmount":   iou,
		}

		handlers.InjectDeliveredAmount(txJSON, meta)

		delivered := meta["delivered_amount"].(map[string]interface{})
		assert.Equal(t, "100", delivered["value"])
	})
}

// TestDeliveredAmountFallbackToAmount verifies that with no real DeliveredAmount
// field, the tx Amount is used (the full-delivery case; the ledger-index /
// close-time gate always holds for ledgers goXRPL serves).
func TestDeliveredAmountFallbackToAmount(t *testing.T) {
	t.Run("XRP drops", func(t *testing.T) {
		txJSON := map[string]interface{}{"TransactionType": "Payment", "Amount": "50000000"}
		meta := map[string]interface{}{"TransactionResult": "tesSUCCESS"}

		handlers.InjectDeliveredAmount(txJSON, meta)

		assert.Equal(t, "50000000", meta["delivered_amount"])
		_, hasReal := meta["DeliveredAmount"]
		assert.False(t, hasReal, "the synthetic fallback must not invent a PascalCase field")
	})

	t.Run("IOU", func(t *testing.T) {
		iou := map[string]interface{}{
			"value":    "250.75",
			"currency": "USD",
			"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		}
		txJSON := map[string]interface{}{"TransactionType": "Payment", "Amount": iou}
		meta := map[string]interface{}{"TransactionResult": "tesSUCCESS"}

		handlers.InjectDeliveredAmount(txJSON, meta)

		delivered := meta["delivered_amount"].(map[string]interface{})
		assert.Equal(t, "250.75", delivered["value"])
		assert.Equal(t, "USD", delivered["currency"])
		assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", delivered["issuer"])
	})
}

// TestDeliveredAmountUnavailable verifies that an eligible, successful tx with
// neither a real DeliveredAmount field nor an Amount field yields the literal
// "unavailable" (DeliveredAmount.cpp:153-158).
func TestDeliveredAmountUnavailable(t *testing.T) {
	txJSON := map[string]interface{}{"TransactionType": "Payment"} // no Amount
	meta := map[string]interface{}{"TransactionResult": "tesSUCCESS"}

	handlers.InjectDeliveredAmount(txJSON, meta)

	assert.Equal(t, "unavailable", meta["delivered_amount"])
}

// TestDeliveredAmountIdempotent verifies that a pre-existing snake_case
// delivered_amount (e.g. the engine's simulate metadata carrying a real
// partial-payment value) is preserved rather than clobbered by the Amount
// fallback.
func TestDeliveredAmountIdempotent(t *testing.T) {
	txJSON := map[string]interface{}{"TransactionType": "Payment", "Amount": "9999999"}
	meta := map[string]interface{}{
		"TransactionResult": "tesSUCCESS",
		"delivered_amount":  "1234567",
	}

	handlers.InjectDeliveredAmount(txJSON, meta)

	assert.Equal(t, "1234567", meta["delivered_amount"],
		"an existing delivered_amount must not be overwritten by the Amount fallback")
}

// TestDeliveredAmountNilMeta verifies that nil meta does not panic.
func TestDeliveredAmountNilMeta(t *testing.T) {
	txJSON := map[string]interface{}{"TransactionType": "Payment", "Amount": "1000000"}
	require.NotPanics(t, func() {
		handlers.InjectDeliveredAmount(txJSON, nil)
	})
}

// TestDeliveredAmountMissingTransactionType verifies that a tx with no
// TransactionType is treated as ineligible.
func TestDeliveredAmountMissingTransactionType(t *testing.T) {
	txJSON := map[string]interface{}{"Amount": "1000000"} // no TransactionType
	meta := map[string]interface{}{"TransactionResult": "tesSUCCESS"}

	handlers.InjectDeliveredAmount(txJSON, meta)

	_, has := meta["delivered_amount"]
	assert.False(t, has, "missing TransactionType should yield no delivered_amount")
}

// FormatLedgerHash Tests

// TestFormatLedgerHashValidHash verifies that a 32-byte hash is formatted
// as a lowercase 64-character hex string.
func TestFormatLedgerHashValidHash(t *testing.T) {
	hash := [32]byte{
		0x4B, 0xC5, 0x0C, 0x9B, 0x0D, 0x85, 0x15, 0xD3,
		0xEA, 0xAE, 0x1E, 0x74, 0xB2, 0x9A, 0x95, 0x80,
		0x43, 0x46, 0xC4, 0x91, 0xEE, 0x1A, 0x95, 0xBF,
		0x25, 0xE4, 0xAA, 0xB8, 0x54, 0xA6, 0xA6, 0x52,
	}

	result := handlers.FormatLedgerHash(hash)

	assert.Equal(t, "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652", result)
	assert.Len(t, result, 64, "Hash hex string should be 64 characters")
}

// TestFormatLedgerHashZeroHash verifies that a zero hash formats correctly.
func TestFormatLedgerHashZeroHash(t *testing.T) {
	var hash [32]byte // all zeros

	result := handlers.FormatLedgerHash(hash)

	expected := "0000000000000000000000000000000000000000000000000000000000000000"
	assert.Equal(t, expected, result, "Zero hash should format as 64 zeroes")
	assert.Len(t, result, 64)
}

// TestFormatLedgerHashAllOnes verifies formatting of a hash with all bytes 0xFF.
func TestFormatLedgerHashAllOnes(t *testing.T) {
	var hash [32]byte
	for i := range hash {
		hash[i] = 0xFF
	}

	result := handlers.FormatLedgerHash(hash)

	expected := "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"
	assert.Equal(t, expected, result)
}

// TestFormatLedgerHashDeterministic verifies that the same input always
// produces the same output.
func TestFormatLedgerHashDeterministic(t *testing.T) {
	hash := [32]byte{0x01, 0x02, 0x03, 0x04}

	result1 := handlers.FormatLedgerHash(hash)
	result2 := handlers.FormatLedgerHash(hash)

	assert.Equal(t, result1, result2, "Same input should produce same output")
}

// ResolveLedgerIndex Tests (tested indirectly via TransactionEntryMethod)
// The resolveTargetLedger method is unexported on TransactionEntryMethod,
// so we test it indirectly through the handler's behavior with different
// ledger_index values passed via parameters.
// Note: Direct tests for resolveTargetLedger would require the handlers
// package. Ledger index resolution is tested indirectly through handler
// tests like TestAccountInfoLedgerSpecification and
// TestAccountInfoLedgerIndexFormats in account_info_test.go.

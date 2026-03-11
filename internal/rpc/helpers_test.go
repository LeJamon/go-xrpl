package rpc

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// InjectDeliveredAmount Tests
// Based on rippled src/test/rpc/DeliveredAmount_test.cpp
// =============================================================================

// TestDeliveredAmountNonPaymentSkipped verifies that non-Payment transactions
// are skipped entirely (no DeliveredAmount is added to meta).
func TestDeliveredAmountNonPaymentSkipped(t *testing.T) {
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

			_, hasDeliveredAmount := meta["DeliveredAmount"]
			assert.False(t, hasDeliveredAmount,
				"Non-Payment tx type %q should not get DeliveredAmount", tc.txType)
		})
	}
}

// TestDeliveredAmountExistingDeliveredAmountNotOverridden verifies that if
// DeliveredAmount is already present in meta, it is not overridden.
func TestDeliveredAmountExistingDeliveredAmountNotOverridden(t *testing.T) {
	t.Run("XRP drops DeliveredAmount preserved", func(t *testing.T) {
		txJSON := map[string]interface{}{
			"TransactionType": "Payment",
			"Amount":          "5000000",
		}
		meta := map[string]interface{}{
			"TransactionResult": "tesSUCCESS",
			"DeliveredAmount":   "3000000",
		}

		handlers.InjectDeliveredAmount(txJSON, meta)

		assert.Equal(t, "3000000", meta["DeliveredAmount"],
			"Existing DeliveredAmount should not be overridden")
	})

	t.Run("IOU DeliveredAmount preserved", func(t *testing.T) {
		iouAmount := map[string]interface{}{
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
			"DeliveredAmount":   iouAmount,
		}

		handlers.InjectDeliveredAmount(txJSON, meta)

		delivered := meta["DeliveredAmount"].(map[string]interface{})
		assert.Equal(t, "100", delivered["value"],
			"Existing IOU DeliveredAmount should not be overridden")
	})
}

// TestDeliveredAmountPromotedFromDeliveredAmountField verifies that
// delivered_amount (lowercase, from meta) is promoted to DeliveredAmount.
func TestDeliveredAmountPromotedFromDeliveredAmountField(t *testing.T) {
	t.Run("XRP drops promoted", func(t *testing.T) {
		txJSON := map[string]interface{}{
			"TransactionType": "Payment",
			"Amount":          "5000000",
		}
		meta := map[string]interface{}{
			"TransactionResult": "tesSUCCESS",
			"delivered_amount":  "2000000",
		}

		handlers.InjectDeliveredAmount(txJSON, meta)

		assert.Equal(t, "2000000", meta["DeliveredAmount"],
			"delivered_amount should be promoted to DeliveredAmount")
		// delivered_amount should still be present (not removed)
		assert.Equal(t, "2000000", meta["delivered_amount"],
			"delivered_amount should remain in meta")
	})

	t.Run("IOU promoted", func(t *testing.T) {
		iouDA := map[string]interface{}{
			"value":    "75.5",
			"currency": "EUR",
			"issuer":   "rPyfep3gcLzkosKC9XiE77Y8LJUBS1test",
		}
		txJSON := map[string]interface{}{
			"TransactionType": "Payment",
			"Amount": map[string]interface{}{
				"value":    "100",
				"currency": "EUR",
				"issuer":   "rPyfep3gcLzkosKC9XiE77Y8LJUBS1test",
			},
		}
		meta := map[string]interface{}{
			"TransactionResult": "tesSUCCESS",
			"delivered_amount":  iouDA,
		}

		handlers.InjectDeliveredAmount(txJSON, meta)

		delivered := meta["DeliveredAmount"].(map[string]interface{})
		assert.Equal(t, "75.5", delivered["value"],
			"IOU delivered_amount should be promoted to DeliveredAmount")
	})

	t.Run("delivered_amount takes precedence over Amount fallback", func(t *testing.T) {
		txJSON := map[string]interface{}{
			"TransactionType": "Payment",
			"Amount":          "9999999",
		}
		meta := map[string]interface{}{
			"TransactionResult": "tesSUCCESS",
			"delivered_amount":  "1234567",
		}

		handlers.InjectDeliveredAmount(txJSON, meta)

		assert.Equal(t, "1234567", meta["DeliveredAmount"],
			"delivered_amount should take precedence over Amount fallback")
	})
}

// TestDeliveredAmountFallbackToAmountXRP verifies that when no DeliveredAmount
// or delivered_amount exists in meta, the Amount field from the tx is used.
func TestDeliveredAmountFallbackToAmountXRP(t *testing.T) {
	txJSON := map[string]interface{}{
		"TransactionType": "Payment",
		"Amount":          "50000000",
	}
	meta := map[string]interface{}{
		"TransactionResult": "tesSUCCESS",
	}

	handlers.InjectDeliveredAmount(txJSON, meta)

	assert.Equal(t, "50000000", meta["DeliveredAmount"],
		"Amount field (XRP drops string) should be used as fallback DeliveredAmount")
}

// TestDeliveredAmountFallbackToAmountIOU verifies fallback to Amount for IOU.
func TestDeliveredAmountFallbackToAmountIOU(t *testing.T) {
	iouAmount := map[string]interface{}{
		"value":    "250.75",
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	}
	txJSON := map[string]interface{}{
		"TransactionType": "Payment",
		"Amount":          iouAmount,
	}
	meta := map[string]interface{}{
		"TransactionResult": "tesSUCCESS",
	}

	handlers.InjectDeliveredAmount(txJSON, meta)

	delivered := meta["DeliveredAmount"].(map[string]interface{})
	assert.Equal(t, "250.75", delivered["value"],
		"Amount IOU value should be used as fallback DeliveredAmount")
	assert.Equal(t, "USD", delivered["currency"])
	assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", delivered["issuer"])
}

// TestDeliveredAmountNilMeta verifies that nil meta does not panic.
func TestDeliveredAmountNilMeta(t *testing.T) {
	txJSON := map[string]interface{}{
		"TransactionType": "Payment",
		"Amount":          "1000000",
	}

	// Should not panic
	require.NotPanics(t, func() {
		handlers.InjectDeliveredAmount(txJSON, nil)
	})
}

// TestDeliveredAmountEmptyMeta verifies that empty meta does not panic
// and correctly adds DeliveredAmount from Amount fallback.
func TestDeliveredAmountEmptyMeta(t *testing.T) {
	txJSON := map[string]interface{}{
		"TransactionType": "Payment",
		"Amount":          "1000000",
	}
	meta := map[string]interface{}{}

	require.NotPanics(t, func() {
		handlers.InjectDeliveredAmount(txJSON, meta)
	})

	assert.Equal(t, "1000000", meta["DeliveredAmount"],
		"Empty meta should get DeliveredAmount from Amount fallback")
}

// TestDeliveredAmountNoAmountField verifies behavior when Payment has no Amount.
func TestDeliveredAmountNoAmountField(t *testing.T) {
	txJSON := map[string]interface{}{
		"TransactionType": "Payment",
		// No Amount field
	}
	meta := map[string]interface{}{
		"TransactionResult": "tesSUCCESS",
	}

	require.NotPanics(t, func() {
		handlers.InjectDeliveredAmount(txJSON, meta)
	})

	_, hasDeliveredAmount := meta["DeliveredAmount"]
	assert.False(t, hasDeliveredAmount,
		"No DeliveredAmount should be set when Amount is missing from tx")
}

// TestDeliveredAmountMissingTransactionType verifies that a tx with no
// TransactionType field is treated as non-Payment (skipped).
func TestDeliveredAmountMissingTransactionType(t *testing.T) {
	txJSON := map[string]interface{}{
		"Amount": "1000000",
		// No TransactionType field
	}
	meta := map[string]interface{}{
		"TransactionResult": "tesSUCCESS",
	}

	handlers.InjectDeliveredAmount(txJSON, meta)

	_, hasDeliveredAmount := meta["DeliveredAmount"]
	assert.False(t, hasDeliveredAmount,
		"Missing TransactionType should result in no DeliveredAmount")
}

// TestDeliveredAmountPriorityOrder verifies the full priority chain:
// 1. Existing DeliveredAmount in meta -> keep it
// 2. delivered_amount in meta -> promote to DeliveredAmount
// 3. Amount in tx -> use as fallback
func TestDeliveredAmountPriorityOrder(t *testing.T) {
	t.Run("DeliveredAmount wins over delivered_amount and Amount", func(t *testing.T) {
		txJSON := map[string]interface{}{
			"TransactionType": "Payment",
			"Amount":          "9999",
		}
		meta := map[string]interface{}{
			"DeliveredAmount": "1111",
			"delivered_amount": "2222",
		}

		handlers.InjectDeliveredAmount(txJSON, meta)

		assert.Equal(t, "1111", meta["DeliveredAmount"],
			"Existing DeliveredAmount should win")
	})

	t.Run("delivered_amount wins over Amount", func(t *testing.T) {
		txJSON := map[string]interface{}{
			"TransactionType": "Payment",
			"Amount":          "9999",
		}
		meta := map[string]interface{}{
			"delivered_amount": "2222",
		}

		handlers.InjectDeliveredAmount(txJSON, meta)

		assert.Equal(t, "2222", meta["DeliveredAmount"],
			"delivered_amount should win over Amount fallback")
	})

	t.Run("Amount used as last resort", func(t *testing.T) {
		txJSON := map[string]interface{}{
			"TransactionType": "Payment",
			"Amount":          "9999",
		}
		meta := map[string]interface{}{}

		handlers.InjectDeliveredAmount(txJSON, meta)

		assert.Equal(t, "9999", meta["DeliveredAmount"],
			"Amount should be used as last resort")
	})
}

// =============================================================================
// FormatLedgerHash Tests
// =============================================================================

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

	assert.Equal(t, "4bc50c9b0d8515d3eaae1e74b29a95804346c491ee1a95bf25e4aab854a6a652", result)
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

	expected := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
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

// =============================================================================
// ResolveLedgerIndex Tests (tested indirectly via TransactionEntryMethod)
// The resolveTargetLedger method is unexported on TransactionEntryMethod,
// so we test it indirectly through the handler's behavior with different
// ledger_index values passed via parameters.
// =============================================================================
// Note: Direct tests for resolveTargetLedger would require the handlers
// package. Ledger index resolution is tested indirectly through handler
// tests like TestAccountInfoLedgerSpecification and
// TestAccountInfoLedgerIndexFormats in account_info_test.go.

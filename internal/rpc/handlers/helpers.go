package handlers

import (
	"encoding/hex"
)

// FormatLedgerHash formats a 32-byte hash as hex string
func FormatLedgerHash(hash [32]byte) string {
	return hex.EncodeToString(hash[:])
}

// InjectDeliveredAmount adds DeliveredAmount to metadata for Payment transactions.
// If meta has a "DeliveredAmount" field already, it is left as-is.
// If meta has a "delivered_amount" field, it is promoted to "DeliveredAmount".
// Otherwise, for Payment transactions, the Amount field from the transaction
// is used as a fallback for "DeliveredAmount".
// Non-Payment transactions and nil meta are no-ops.
func InjectDeliveredAmount(txJSON map[string]interface{}, meta map[string]interface{}) {
	txType, _ := txJSON["TransactionType"].(string)
	if txType != "Payment" {
		return
	}
	if meta == nil {
		return
	}

	// If DeliveredAmount already present in metadata, use it
	if _, ok := meta["DeliveredAmount"]; ok {
		return
	}

	// If delivered_amount is present, promote to DeliveredAmount
	if da, ok := meta["delivered_amount"]; ok {
		meta["DeliveredAmount"] = da
		return
	}

	// Fallback: use Amount from transaction as DeliveredAmount
	if amount, ok := txJSON["Amount"]; ok {
		meta["DeliveredAmount"] = amount
	}
}

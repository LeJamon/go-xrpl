package testing

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// TxResult represents the result of applying a transaction.
type TxResult struct {
	// Code is the transaction engine result code (e.g., "tesSUCCESS").
	Code string

	// Success indicates whether the transaction was successfully applied.
	Success bool

	// Message provides additional details about the result.
	Message string

	// Metadata contains the transaction metadata (AffectedNodes, etc.).
	Metadata *tx.Metadata
}

// tesSUCCESS is the result code for a successful transaction.
const tesSUCCESS = "tesSUCCESS"

// IsSuccess returns true if the result code indicates success.
func (r TxResult) IsSuccess() bool {
	return r.Code == tesSUCCESS
}

// IsClaimed returns true if the result code indicates the fee was claimed but
// the transaction was not applied (tec codes).
func (r TxResult) IsClaimed() bool {
	if len(r.Code) < 3 {
		return false
	}
	return r.Code[:3] == "tec"
}

// IsRetry returns true if the result code indicates a retry is possible.
func (r TxResult) IsRetry() bool {
	if len(r.Code) < 3 {
		return false
	}
	return r.Code[:3] == "ter"
}

// IsMalformed returns true if the result code indicates the transaction is malformed.
func (r TxResult) IsMalformed() bool {
	if len(r.Code) < 3 {
		return false
	}
	return r.Code[:3] == "tem"
}

// IsFailed returns true if the result code indicates a failure.
func (r TxResult) IsFailed() bool {
	if len(r.Code) < 3 {
		return false
	}
	return r.Code[:3] == "tef"
}

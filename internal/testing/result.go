package jtx

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// TxResult represents the result of applying a transaction.
type TxResult struct {
	// Result is the typed transaction engine result.
	Result ter.Result

	// Code is the transaction engine result code (e.g., "tesSUCCESS").
	Code string

	// Success indicates whether the transaction was successfully applied.
	Success bool

	// Applied reports whether the engine committed the transaction or a
	// fee-claiming recovery result.
	Applied bool

	// Fee is the number of drops actually charged and committed.
	Fee uint64

	// Queued reports that TxQ retained the transaction for later application.
	Queued bool

	// ApplyInvoked and InvariantsChecked expose engine reachability to tests.
	ApplyInvoked      bool
	InvariantsChecked bool

	// Message provides additional details about the result.
	Message string

	// Metadata contains the transaction metadata (AffectedNodes, etc.).
	Metadata *tx.Metadata

	AppliedInnerTransactions []tx.AppliedInnerTransaction
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

package jtx

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// TxResult represents the result of applying a transaction.
type TxResult struct {
	Result ter.Result

	// Code is the transaction engine result code (e.g., "tesSUCCESS").
	Code string

	// Success indicates whether the result is tesSUCCESS.
	Success bool

	// Applied indicates whether the transaction and its fee effects were committed.
	Applied bool

	Queued bool

	// Fee is the amount charged in drops. It is zero for queued and rejected transactions.
	Fee uint64

	// Message provides additional details about the result.
	Message string

	// Metadata contains the transaction metadata (AffectedNodes, etc.).
	Metadata *tx.Metadata

	AppliedInnerTransactions []tx.AppliedInnerTransaction
}

func txResultFromApply(result tx.ApplyResult) TxResult {
	return TxResult{
		Result:                   result.Result,
		Code:                     result.Result.String(),
		Success:                  result.Result.IsSuccess(),
		Applied:                  result.Applied,
		Fee:                      result.Fee,
		Message:                  result.Message,
		Metadata:                 result.Metadata,
		AppliedInnerTransactions: result.AppliedInnerTransactions,
	}
}

func txResultFromTER(result ter.Result, queued bool) TxResult {
	return TxResult{
		Result:  result,
		Code:    result.String(),
		Success: result.IsSuccess(),
		Queued:  queued,
		Message: result.Message(),
	}
}

// IsSuccess returns true if the result code indicates success.
func (r TxResult) IsSuccess() bool {
	return r.Result.IsSuccess()
}

// IsClaimed returns true if the result code indicates the fee was claimed but
// the transaction was not applied (tec codes).
func (r TxResult) IsClaimed() bool {
	return r.Result.IsTec()
}

// IsRetry returns true if the result code indicates a retry is possible.
func (r TxResult) IsRetry() bool {
	return r.Result.IsTer()
}

// IsMalformed returns true if the result code indicates the transaction is malformed.
func (r TxResult) IsMalformed() bool {
	return r.Result.IsTem()
}

// IsFailed returns true if the result code indicates a failure.
func (r TxResult) IsFailed() bool {
	return r.Result.IsTef()
}

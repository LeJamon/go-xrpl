package tx

import "strconv"

// calculateFee calculates the fee for a transaction
// For multi-signed transactions, the minimum required fee is baseFee * (1 + numSigners)
func (e *Engine) calculateFee(tx Transaction) uint64 {
	common := tx.GetCommon()
	if common.Fee != "" {
		fee, err := strconv.ParseUint(common.Fee, 10, 64)
		if err == nil {
			return fee
		}
	}
	// If no fee specified, use base fee (adjusted for multi-sig if applicable)
	baseFee := e.config.BaseFee
	if IsMultiSigned(tx) {
		numSigners := len(common.Signers)
		return CalculateMultiSigFee(baseFee, numSigners)
	}
	return baseFee
}

// parseTxDeclaredFee extracts the fee declared in the transaction itself.
// This is the fee the user authorized, as opposed to the fee actually charged.
// If the transaction doesn't explicitly set a Fee field (e.g., the test env
// auto-computes it), fallback is returned instead.
// Reference: rippled InvariantCheck.cpp TransactionFeeCheck — tx.getFieldAmount(sfFee).xrp()
func parseTxDeclaredFee(tx Transaction, fallback uint64) uint64 {
	common := tx.GetCommon()
	if common.Fee != "" {
		if fee, err := strconv.ParseUint(common.Fee, 10, 64); err == nil {
			return fee
		}
	}
	// In rippled, sfFee is always present on the transaction. In the Go test env,
	// the fee may be auto-computed by the engine. Use the engine-computed fee as
	// the declared fee in this case, since the engine authorized it on behalf
	// of the test.
	return fallback
}

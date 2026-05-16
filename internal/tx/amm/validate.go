package amm

import (
	"github.com/LeJamon/goXRPLd/internal/tx"
)

// validateAMMAmount validates an AMM amount.
// Reference: rippled invalidAMMAmount() in AMMCore.cpp:102-103 — temBAD_AMOUNT
// when amount is zero (with validZero=false) or negative.
func validateAMMAmount(amt tx.Amount) error {
	if amt.IsZero() || amt.IsNegative() {
		return tx.Errorf(tx.TemBAD_AMOUNT, "amount must be positive")
	}
	return nil
}

// validateAMMAmountWithPair validates an AMM amount including optional asset pair matching.
// If pair is provided, the amount's issue must match either asset.
// If validZero is true, zero amounts are allowed.
// Returns:
// - "temBAD_AMM_TOKENS" if amount's issue doesn't match the asset pair
// - "temBAD_AMOUNT" if amount is negative or zero (when validZero is false)
// - "" on success
func validateAMMAmountWithPair(amt tx.Amount, asset1, asset2 *tx.Asset, validZero bool) string {
	// Check if amount's issue matches either asset in the pair
	if asset1 != nil && asset2 != nil {
		if !matchesAsset(&amt, *asset1) && !matchesAsset(&amt, *asset2) {
			return "temBAD_AMM_TOKENS"
		}
	}

	// Check amount value
	if amt.IsNegative() {
		return "temBAD_AMOUNT"
	}
	if !validZero && amt.IsZero() {
		return "temBAD_AMOUNT"
	}

	return ""
}

// validateAssetPair validates an AMM asset pair.
// Reference: rippled AMMCore.cpp invalidAMMAssetPair()
// - Assets must not be the same issue
// - XRP assets (empty currency) are valid
func validateAssetPair(asset1, asset2 tx.Asset) error {
	if matchesAssetByIssue(asset1, asset2) {
		return tx.Errorf(tx.TemBAD_AMM_TOKENS, "asset pair has same issue")
	}
	return nil
}

// ammErrCodeToResult maps a string error code from validateAMMAmountWithPair
// to its corresponding tx.Result constant.
func ammErrCodeToResult(code string) tx.Result {
	switch code {
	case "temBAD_AMM_TOKENS":
		return tx.TemBAD_AMM_TOKENS
	case "temBAD_AMOUNT":
		return tx.TemBAD_AMOUNT
	default:
		return tx.TemMALFORMED
	}
}

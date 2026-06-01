package amm

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// matchesAssetByIssue checks if two Assets represent the same issue.
// Handles XRP being represented as either "" or "XRP" for currency.
func matchesAssetByIssue(a, b tx.Asset) bool {
	aIsXRP := a.Currency == "" || a.Currency == "XRP"
	bIsXRP := b.Currency == "" || b.Currency == "XRP"
	if aIsXRP && bIsXRP {
		return true
	}
	return a.Currency == b.Currency && a.Issuer == b.Issuer
}

// matchesAsset checks if an Amount matches an Asset
// Handles XRP being represented as either "" or "XRP" for currency
func matchesAsset(amt *tx.Amount, asset tx.Asset) bool {
	if amt == nil {
		return false
	}
	// Check if both are XRP (currency empty or "XRP", no issuer)
	amtIsXRP := amt.IsNative() || amt.Currency == "" || amt.Currency == "XRP"
	assetIsXRP := asset.Currency == "" || asset.Currency == "XRP"
	if amtIsXRP && assetIsXRP {
		return true
	}
	// For IOUs, compare currency and issuer
	return amt.Currency == asset.Currency && amt.Issuer == asset.Issuer
}

// zeroAmount returns a zero amount for the given asset
func zeroAmount(asset tx.Asset) tx.Amount {
	if asset.Currency == "" || asset.Currency == "XRP" {
		return state.NewXRPAmountFromInt(0)
	}
	return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
}

// compareAccountIDs compares two account IDs lexicographically.
func compareAccountIDs(a, b [20]byte) int {
	return state.CompareAccountIDs(a, b)
}

// encodeAccountID encodes a 20-byte account ID to an XRPL address string.
func encodeAccountID(accountID [20]byte) (string, error) {
	return state.EncodeAccountID(accountID)
}

// EncodeAccountID converts a 20-byte account ID to an r-address string.
// Exported for use in test helpers.
func EncodeAccountID(accountID [20]byte) (string, error) {
	return state.EncodeAccountID(accountID)
}

// minAmountIOU returns the smaller of two amounts compared in IOU space.
func minAmountIOU(a, b tx.Amount) tx.Amount {
	if toIOUForCalc(a).Compare(toIOUForCalc(b)) < 0 {
		return a
	}
	return b
}

// maxAmount returns the larger of two amounts.
// Assumes both amounts are of the same type (both XRP or same IOU).
func maxAmount(a, b tx.Amount) tx.Amount {
	if a.Compare(b) > 0 {
		return a
	}
	return b
}

// isGreater returns true if a > b
func isGreater(a, b tx.Amount) bool {
	return a.Compare(b) > 0
}

// isGreaterOrEqual returns true if a >= b
func isGreaterOrEqual(a, b tx.Amount) bool {
	return a.Compare(b) >= 0
}

// isLessOrEqual returns true if a <= b
func isLessOrEqual(a, b tx.Amount) bool {
	return a.Compare(b) <= 0
}

// withinRelativeDistance checks if two amounts are within relative distance dist.
// Returns true if calc == req, or (max - min) / max < dist.
// Reference: rippled AMMHelpers.h withinRelativeDistance
func withinRelativeDistance(calc, req, dist tx.Amount) bool {
	calcIOU := toIOUForCalc(calc)
	reqIOU := toIOUForCalc(req)

	if calcIOU.Compare(reqIOU) == 0 {
		return true
	}

	var minAmt, maxAmt tx.Amount
	if calcIOU.Compare(reqIOU) < 0 {
		minAmt = calcIOU
		maxAmt = reqIOU
	} else {
		minAmt = reqIOU
		maxAmt = calcIOU
	}

	diff, _ := maxAmt.Sub(minAmt)
	ratio := numberDiv(diff, maxAmt)
	return ratio.Compare(dist) < 0
}

// isOnlyLiquidityProvider checks if the given account is the sole LP in the AMM.
// Simplified approach: if the LP's token balance equals the AMM's total LP token
// balance (within tolerance), they must be the only LP.
// Reference: rippled AMMUtils.cpp isOnlyLiquidityProvider (lines 386-466)
func isOnlyLiquidityProvider(lpTokens tx.Amount, lptBalance tx.Amount) bool {
	lpIOU := toIOUForCalc(lpTokens)
	totalIOU := toIOUForCalc(lptBalance)
	// If LP holds all tokens, they are the only provider.
	// Use withinRelativeDistance to handle rounding differences.
	tolerance := state.NewIssuedAmountFromValue(1, -3, "", "") // 0.001
	return withinRelativeDistance(lpIOU, totalIOU, tolerance)
}

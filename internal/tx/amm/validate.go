package amm

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// badCurrencyBytes is the 160-bit currency rippled rejects via badCurrency():
// the standard-form encoding of the letters "XRP" (bytes 12-14 = 'X','R','P').
// rippled UintTypes.cpp:135 — Currency(0x5852500000000000).
var badCurrencyBytes = [20]byte{12: 'X', 13: 'R', 14: 'P'}

// invalidAMMAsset mirrors rippled invalidAMMAsset() (AMMCore.cpp:65-77): an
// asset whose currency is the bad "XRP" 160-bit code is temBAD_CURRENCY, an XRP
// asset paired with a non-zero issuer is temBAD_ISSUER, and — when a pair is
// supplied — an asset matching neither member is temBAD_AMM_TOKENS.
func invalidAMMAsset(asset tx.Asset, pair *[2]tx.Asset) tx.Result {
	if keylet.CurrencyBytes(asset.Currency) == badCurrencyBytes {
		return tx.TemBAD_CURRENCY
	}
	isXRP := isXRPAsset(asset)
	if isXRP && asset.Issuer != "" {
		return tx.TemBAD_ISSUER
	}
	if pair != nil && !matchesAssetByIssue(asset, pair[0]) && !matchesAssetByIssue(asset, pair[1]) {
		return tx.TemBAD_AMM_TOKENS
	}
	return tx.TesSUCCESS
}

// amountAsset returns the issue (currency + issuer) of an amount.
func amountAsset(amt tx.Amount) tx.Asset {
	return tx.Asset{Currency: amt.Currency, Issuer: amt.Issuer}
}

// validateAMMAmount mirrors rippled invalidAMMAmount() with no pair: asset
// validity first, then temBAD_AMOUNT when the value is negative or zero.
func validateAMMAmount(amt tx.Amount) error {
	return validateAMMAmountWithPair(amt, nil, nil, false)
}

// validateAMMAmountWithPair validates an AMM amount, optionally requiring its
// issue to match one member of the (asset1, asset2) pair. It returns the asset
// check's exact tem code (temBAD_CURRENCY / temBAD_ISSUER / temBAD_AMM_TOKENS),
// or temBAD_AMOUNT when the value is negative or — unless validZero is set —
// zero. Reference: rippled invalidAMMAmount() (AMMCore.cpp:94-105).
func validateAMMAmountWithPair(amt tx.Amount, asset1, asset2 *tx.Asset, validZero bool) error {
	var pair *[2]tx.Asset
	if asset1 != nil && asset2 != nil {
		pair = &[2]tx.Asset{*asset1, *asset2}
	}
	if res := invalidAMMAsset(amountAsset(amt), pair); res != tx.TesSUCCESS {
		return tx.Errorf(res, "invalid amount asset")
	}
	if amt.IsNegative() || (!validZero && amt.IsZero()) {
		return tx.Errorf(tx.TemBAD_AMOUNT, "amount must be positive")
	}
	return nil
}

// validateAssetPair mirrors rippled invalidAMMAssetPair() (AMMCore.cpp:79-92):
// the two assets must not be the same issue, and each must be a valid AMM asset.
func validateAssetPair(asset1, asset2 tx.Asset) error {
	if matchesAssetByIssue(asset1, asset2) {
		return tx.Errorf(tx.TemBAD_AMM_TOKENS, "asset pair has same issue")
	}
	if res := invalidAMMAsset(asset1, nil); res != tx.TesSUCCESS {
		return tx.Errorf(res, "invalid asset")
	}
	if res := invalidAMMAsset(asset2, nil); res != tx.TesSUCCESS {
		return tx.Errorf(res, "invalid asset2")
	}
	return nil
}

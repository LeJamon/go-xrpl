package amm

import (
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

// ammAccountHolds returns the amount held by the AMM account for a specific issue.
// For XRP: reads from the AMM account's AccountRoot.Balance
// For IOU: reads from the trustline between AMM account and issuer
// Reference: rippled AMMUtils.cpp ammAccountHolds
func ammAccountHolds(view tx.LedgerView, ammAccountID [20]byte, asset tx.Asset) tx.Amount {
	if asset.Currency == "" || asset.Currency == "XRP" {
		// XRP: read from AccountRoot
		accountKey := keylet.Account(ammAccountID)
		data, err := view.Read(accountKey)
		if err != nil || data == nil {
			return state.NewXRPAmountFromInt(0)
		}
		account, err := state.ParseAccountRoot(data)
		if err != nil {
			return state.NewXRPAmountFromInt(0)
		}
		return state.NewXRPAmountFromInt(int64(account.Balance))
	}

	// IOU: read from trustline
	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
	}

	trustLineKey := keylet.Line(ammAccountID, issuerID, asset.Currency)
	data, err := view.Read(trustLineKey)
	if err != nil || data == nil {
		return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
	}

	rs, err := state.ParseRippleState(data)
	if err != nil {
		return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
	}

	// Determine balance based on canonical ordering
	// Balance is stored from low account's perspective (positive = low owes high)
	// For AMM: if AMM is low, positive balance means AMM holds tokens
	ammIsLow := state.CompareAccountIDsForLine(ammAccountID, issuerID) < 0
	balance := rs.Balance
	if !ammIsLow {
		balance = balance.Negate()
	}

	// Return absolute balance with proper currency/issuer
	if balance.Signum() <= 0 {
		return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
	}

	return state.NewIssuedAmountFromValue(balance.Mantissa(), balance.Exponent(), asset.Currency, asset.Issuer)
}

// ammPoolHolds returns the balances of both assets in the AMM pool.
// Reference: rippled AMMUtils.cpp ammPoolHolds
func ammPoolHolds(view tx.LedgerView, ammAccountID [20]byte, asset1, asset2 tx.Asset, fhZeroIfFrozen bool) (tx.Amount, tx.Amount) {
	balance1 := ammAccountHolds(view, ammAccountID, asset1)
	balance2 := ammAccountHolds(view, ammAccountID, asset2)

	// Check for frozen assets if requested
	if fhZeroIfFrozen {
		if tx.IsGlobalFrozen(view, asset1.Issuer) || tx.IsIndividualFrozen(view, ammAccountID, asset1) {
			balance1 = zeroAmount(asset1)
		}
		if tx.IsGlobalFrozen(view, asset2.Issuer) || tx.IsIndividualFrozen(view, ammAccountID, asset2) {
			balance2 = zeroAmount(asset2)
		}
	}

	return balance1, balance2
}

// AMMHolds returns the pool balances and LP token balance for an AMM.
// This is the main function to get current AMM state.
// Reference: rippled AMMUtils.cpp ammHolds
func AMMHolds(view tx.LedgerView, amm *AMMData, fhZeroIfFrozen bool) (asset1Balance, asset2Balance, lptBalance tx.Amount) {
	// Get pool balances from actual state
	asset1Balance, asset2Balance = ammPoolHolds(view, amm.Account, amm.Asset, amm.Asset2, fhZeroIfFrozen)

	// LP token balance is stored in the AMM entry
	lptBalance = amm.LPTokenBalance

	return asset1Balance, asset2Balance, lptBalance
}

// IsAMMEmpty returns true if the AMM has no LP tokens outstanding.
// An empty AMM can be deleted or reinitialized on deposit.
// Reference: rippled checks lpTokens == 0 for empty AMM
func IsAMMEmpty(amm *AMMData) bool {
	return amm.LPTokenBalance.IsZero()
}

// ammLPHolds returns the LP token balance held by an account for an AMM.
// LP tokens are stored in a trustline between the LP account and the AMM account.
// Reference: rippled AMMUtils.cpp ammLPHolds lines 113-160
func ammLPHolds(view tx.LedgerView, amm *AMMData, lpAccountID [20]byte) tx.Amount {
	// LP token currency is derived from the two asset currencies
	lptCurrency := GenerateAMMLPTCurrency(amm.Asset.Currency, amm.Asset2.Currency)
	ammAccountID := amm.Account

	// Read the trustline between LP account and AMM account
	trustLineKey := keylet.Line(lpAccountID, ammAccountID, lptCurrency)
	data, err := view.Read(trustLineKey)
	if err != nil || data == nil {
		// No trustline = no LP tokens held
		ammAccountAddr, _ := state.EncodeAccountID(ammAccountID)
		return state.NewIssuedAmountFromValue(0, -100, lptCurrency, ammAccountAddr)
	}

	// Parse the trustline
	rs, err := state.ParseRippleState(data)
	if err != nil {
		ammAccountAddr, _ := state.EncodeAccountID(ammAccountID)
		return state.NewIssuedAmountFromValue(0, -100, lptCurrency, ammAccountAddr)
	}

	// Determine balance based on canonical ordering
	// Balance is stored from low account's perspective (positive = low owes high)
	// For LP tokens: if LP is low, positive balance means LP holds tokens
	lpIsLow := state.CompareAccountIDsForLine(lpAccountID, ammAccountID) < 0
	balance := rs.Balance
	if !lpIsLow {
		balance = balance.Negate()
	}

	// Return balance with proper issuer (AMM account)
	ammAccountAddr, _ := state.EncodeAccountID(ammAccountID)
	if balance.Signum() <= 0 {
		return state.NewIssuedAmountFromValue(0, -100, lptCurrency, ammAccountAddr)
	}

	return state.NewIssuedAmountFromValue(balance.Mantissa(), balance.Exponent(), lptCurrency, ammAccountAddr)
}

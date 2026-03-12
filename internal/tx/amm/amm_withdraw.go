package amm

import (
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

func init() {
	tx.Register(tx.TypeAMMWithdraw, func() tx.Transaction {
		return &AMMWithdraw{BaseTx: *tx.NewBaseTx(tx.TypeAMMWithdraw, "")}
	})
}

// AMMWithdraw withdraws assets from an AMM.
type AMMWithdraw struct {
	tx.BaseTx

	// Asset identifies the first asset of the AMM (required)
	Asset tx.Asset `json:"Asset" xrpl:"Asset,asset"`

	// Asset2 identifies the second asset of the AMM (required)
	Asset2 tx.Asset `json:"Asset2" xrpl:"Asset2,asset"`

	// Amount is the amount of first asset to withdraw (optional)
	Amount *tx.Amount `json:"Amount,omitempty" xrpl:"Amount,omitempty,amount"`

	// Amount2 is the amount of second asset to withdraw (optional)
	Amount2 *tx.Amount `json:"Amount2,omitempty" xrpl:"Amount2,omitempty,amount"`

	// EPrice is the effective price limit (optional)
	EPrice *tx.Amount `json:"EPrice,omitempty" xrpl:"EPrice,omitempty,amount"`

	// LPTokenIn is the LP tokens to burn (optional)
	LPTokenIn *tx.Amount `json:"LPTokenIn,omitempty" xrpl:"LPTokenIn,omitempty,amount"`
}

// NewAMMWithdraw creates a new AMMWithdraw transaction
func NewAMMWithdraw(account string, asset, asset2 tx.Asset) *AMMWithdraw {
	return &AMMWithdraw{
		BaseTx: *tx.NewBaseTx(tx.TypeAMMWithdraw, account),
		Asset:  asset,
		Asset2: asset2,
	}
}

// TxType returns the transaction type
func (a *AMMWithdraw) TxType() tx.Type {
	return tx.TypeAMMWithdraw
}

// GetAMMAsset returns the first asset of the AMM (Asset field).
// Implements ammAssetProvider for the ValidAMM invariant checker.
func (a *AMMWithdraw) GetAMMAsset() tx.Asset {
	return a.Asset
}

// GetAMMAsset2 returns the second asset of the AMM (Asset2 field).
// Implements ammAssetProvider for the ValidAMM invariant checker.
func (a *AMMWithdraw) GetAMMAsset2() tx.Asset {
	return a.Asset2
}

// Validate validates the AMMWithdraw transaction
// Reference: rippled AMMWithdraw.cpp preflight
func (a *AMMWithdraw) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	// Check flags
	if a.GetFlags()&tfAMMWithdrawMask != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "invalid flags for AMMWithdraw")
	}

	if a.Asset.Currency == "" {
		return tx.Errorf(tx.TemMALFORMED, "Asset is required")
	}

	if a.Asset2.Currency == "" {
		return tx.Errorf(tx.TemMALFORMED, "Asset2 is required")
	}

	flags := a.GetFlags()

	// Withdrawal sub-transaction flags (exactly one must be set)
	tfWithdrawSubTx := tfLPToken | tfWithdrawAll | tfOneAssetWithdrawAll | tfSingleAsset | tfTwoAsset | tfOneAssetLPToken | tfLimitLPToken
	subTxFlags := flags & tfWithdrawSubTx

	// Count number of mode flags set using popcount
	flagCount := 0
	for f := subTxFlags; f != 0; f &= f - 1 {
		flagCount++
	}
	if flagCount != 1 {
		return tx.Errorf(tx.TemMALFORMED, "exactly one withdraw mode flag must be set")
	}

	// Validate field requirements for each mode
	hasAmount := a.Amount != nil
	hasAmount2 := a.Amount2 != nil
	hasEPrice := a.EPrice != nil
	hasLPTokenIn := a.LPTokenIn != nil

	if flags&tfLPToken != 0 {
		// LPToken mode: LPTokenIn required, no amount/amount2/ePrice
		if !hasLPTokenIn || hasAmount || hasAmount2 || hasEPrice {
			return tx.Errorf(tx.TemMALFORMED, "tfLPToken requires LPTokenIn only")
		}
	} else if flags&tfWithdrawAll != 0 {
		// WithdrawAll mode: no fields needed
		if hasLPTokenIn || hasAmount || hasAmount2 || hasEPrice {
			return tx.Errorf(tx.TemMALFORMED, "tfWithdrawAll requires no amount fields")
		}
	} else if flags&tfOneAssetWithdrawAll != 0 {
		// OneAssetWithdrawAll mode: Amount required (identifies which asset)
		if !hasAmount || hasLPTokenIn || hasAmount2 || hasEPrice {
			return tx.Errorf(tx.TemMALFORMED, "tfOneAssetWithdrawAll requires Amount only")
		}
	} else if flags&tfSingleAsset != 0 {
		// SingleAsset mode: Amount required
		if !hasAmount || hasLPTokenIn || hasAmount2 || hasEPrice {
			return tx.Errorf(tx.TemMALFORMED, "tfSingleAsset requires Amount only")
		}
	} else if flags&tfTwoAsset != 0 {
		// TwoAsset mode: Amount and Amount2 required
		if !hasAmount || !hasAmount2 || hasLPTokenIn || hasEPrice {
			return tx.Errorf(tx.TemMALFORMED, "tfTwoAsset requires Amount and Amount2")
		}
	} else if flags&tfOneAssetLPToken != 0 {
		// OneAssetLPToken mode: Amount and LPTokenIn required
		if !hasAmount || !hasLPTokenIn || hasAmount2 || hasEPrice {
			return tx.Errorf(tx.TemMALFORMED, "tfOneAssetLPToken requires Amount and LPTokenIn")
		}
	} else if flags&tfLimitLPToken != 0 {
		// LimitLPToken mode: Amount and EPrice required
		if !hasAmount || !hasEPrice || hasLPTokenIn || hasAmount2 {
			return tx.Errorf(tx.TemMALFORMED, "tfLimitLPToken requires Amount and EPrice")
		}
	}

	// Amount and Amount2 cannot have the same issue if both present
	if hasAmount && hasAmount2 {
		if a.Amount.Currency == a.Amount2.Currency && a.Amount.Issuer == a.Amount2.Issuer {
			return tx.Errorf(tx.TemBAD_AMM_TOKENS, "Amount and Amount2 cannot have the same issue")
		}
	}

	// Validate LPTokenIn is positive
	if hasLPTokenIn {
		if a.LPTokenIn.IsZero() || a.LPTokenIn.IsNegative() {
			return tx.Errorf(tx.TemBAD_AMM_TOKENS, "invalid LPTokenIn")
		}
	}

	// Validate amounts if provided
	// For tfOneAssetWithdrawAll, tfOneAssetLPToken, and when EPrice is present, zero amounts are allowed
	// (the amount is used to identify which asset, not the actual amount)
	validZeroAmount := (flags&(tfOneAssetWithdrawAll|tfOneAssetLPToken) != 0) || hasEPrice

	if hasAmount {
		if errCode := validateAMMAmountWithPair(*a.Amount, &a.Asset, &a.Asset2, validZeroAmount); errCode != "" {
			return tx.Errorf(ammErrCodeToResult(errCode), "invalid Amount")
		}
	}
	if hasAmount2 {
		if errCode := validateAMMAmountWithPair(*a.Amount2, &a.Asset, &a.Asset2, false); errCode != "" {
			return tx.Errorf(ammErrCodeToResult(errCode), "invalid Amount2")
		}
	}
	if hasEPrice {
		if err := validateAMMAmount(*a.EPrice); err != nil {
			return tx.Errorf(tx.TemBAD_AMOUNT, "invalid EPrice - %s", err.Error())
		}
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (a *AMMWithdraw) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(a)
}

// RequiredAmendments returns the amendments required for this transaction type
func (a *AMMWithdraw) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureAMM, amendment.FeatureFixUniversalNumber}
}

// Apply applies the AMMWithdraw transaction to ledger state.
// Reference: rippled AMMWithdraw.cpp applyGuts
func (a *AMMWithdraw) Apply(ctx *tx.ApplyContext) tx.Result {
	accountID := ctx.AccountID

	// Find the AMM
	ammKey := computeAMMKeylet(a.Asset, a.Asset2)

	ammRawData, err := ctx.View.Read(ammKey)
	if err != nil || ammRawData == nil {
		return TerNO_AMM
	}

	// Parse AMM data
	amm, err := parseAMMData(ammRawData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Get AMM account from the stored AMM data
	ammAccountID := amm.Account
	ammAccountKey := keylet.Account(ammAccountID)
	ammAccountData, err := ctx.View.Read(ammAccountKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	ammAccount, err := state.ParseAccountRoot(ammAccountData)
	if err != nil {
		return tx.TefINTERNAL
	}

	flags := a.GetFlags()
	tfee := amm.TradingFee

	// Preclaim: EPrice issue must match LP token issue (rippled AMMWithdraw.cpp:273-278)
	if a.EPrice != nil {
		lptCurrency := GenerateAMMLPTCurrency(amm.Asset.Currency, amm.Asset2.Currency)
		ammAccountAddr := ComputeAMMAccountAddress(amm.Asset, amm.Asset2)
		if a.EPrice.Currency != lptCurrency || a.EPrice.Issuer != ammAccountAddr {
			return tx.TemBAD_AMM_TOKENS
		}
	}

	// Amendment checks
	fixV1_3 := ctx.Rules().Enabled(amendment.FeatureFixAMMv1_3)
	isWithdrawAll := (flags & (tfWithdrawAll | tfOneAssetWithdrawAll)) != 0

	// Get amounts from transaction - use Amount type directly
	amount1 := zeroAmount(a.Asset)
	amount2 := zeroAmount(a.Asset2)
	lpTokensRequested := zeroAmount(tx.Asset{}) // LP tokens

	if a.Amount != nil {
		amount1 = *a.Amount
	}
	if a.Amount2 != nil {
		amount2 = *a.Amount2
	}
	if a.LPTokenIn != nil {
		lpTokensRequested = *a.LPTokenIn
	}

	// Get current AMM balances from actual state (not stored in AMM entry)
	assetBalance1, assetBalance2, lptBalance := AMMHolds(ctx.View, amm, false)

	// Reorder balances to match the transaction's asset ordering.
	// AMMHolds returns in amm.Asset / amm.Asset2 order, but the transaction
	// may specify assets in a different order. rippled's ammHolds() reorders
	// based on optional issue hints; we do it explicitly here.
	if !matchesAssetByIssue(amm.Asset, a.Asset) {
		assetBalance1, assetBalance2 = assetBalance2, assetBalance1
	}

	if lptBalance.IsZero() {
		return tx.TecAMM_EMPTY
	}

	// Get withdrawer's LP token balance from trustline
	lpTokensHeld := ammLPHolds(ctx.View, amm, accountID)
	if lpTokensHeld.IsZero() {
		return tx.TecAMM_BALANCE
	}

	// For tfWithdrawAll / tfOneAssetWithdrawAll, lpTokensWithdraw = lpTokensHeld
	lpTokensWithdraw := lpTokensRequested
	if isWithdrawAll {
		lpTokensWithdraw = lpTokensHeld
	}

	// Due to rounding, the LPTokenBalance of the last LP
	// might not match the LP's trustline balance.
	// Reference: rippled AMMWithdraw.cpp:311-317
	if ctx.Rules().Enabled(amendment.FeatureFixAMMv1_1) {
		if result := verifyAndAdjustLPTokenBalance(lpTokensHeld, amm); result != tx.TesSUCCESS {
			return result
		}
		// Refresh lptBalance since verifyAndAdjustLPTokenBalance may have modified amm.LPTokenBalance
		lptBalance = amm.LPTokenBalance
	}

	// Result amounts
	var lpTokensToRedeem tx.Amount
	var withdrawAmount1, withdrawAmount2 tx.Amount

	// Track single-asset withdrawal info for adjustAmountsByLPTokens calling convention.
	// rippled's singleWithdraw/singleWithdrawTokens/singleWithdrawEPrice all call
	// withdraw() with (amountBalance=withdrawn_asset_balance, amountWithdraw, nullopt).
	// We must replicate this: pass the withdrawn asset's balance (not always assetBalance1)
	// and nil for amount2 to enter the "single trade" path in adjustAmountsByLPTokens.
	var withdrawAssetBalance tx.Amount   // pool balance for the withdrawn asset
	isSingleAssetWithdraw := false       // true if only one asset is being withdrawn
	singleWithdrawIsAsset2 := false      // true if the single withdrawal is for asset2

	// Handle different withdrawal modes
	// Reference: rippled AMMWithdraw.cpp applyGuts()
	switch {
	case flags&tfLPToken != 0, flags&tfWithdrawAll != 0:
		// Proportional withdrawal (equalWithdrawTokens)
		// Reference: rippled AMMWithdraw.cpp equalWithdrawTokens()
		if lpTokensWithdraw.IsZero() || lptBalance.IsZero() {
			return tx.TecAMM_INVALID_TOKENS
		}
		if isGreater(toIOUForCalc(lpTokensWithdraw), toIOUForCalc(lpTokensHeld)) {
			return tx.TecAMM_INVALID_TOKENS
		}

		// Withdrawing all tokens in the pool
		if toIOUForCalc(lpTokensWithdraw).Compare(toIOUForCalc(lptBalance)) == 0 {
			withdrawAmount1 = assetBalance1
			withdrawAmount2 = assetBalance2
			lpTokensToRedeem = lpTokensWithdraw
		} else {
			// adjustLPTokensIn
			tokensAdj := lpTokensWithdraw
			if fixV1_3 && !isWithdrawAll {
				tokensAdj = adjustLPTokens(lptBalance, lpTokensWithdraw, false)
				if tokensAdj.IsZero() {
					return tx.TecAMM_INVALID_TOKENS
				}
			}

			// frac = tokensAdj / lptBalance
			frac := numberDiv(toIOUForCalc(tokensAdj), toIOUForCalc(lptBalance))
			withdrawAmount1 = getRoundedAsset(fixV1_3, assetBalance1, frac, false)
			withdrawAmount2 = getRoundedAsset(fixV1_3, assetBalance2, frac, false)

			// LP is making equal withdrawal by tokens but the requested amount
			// of LP tokens is likely too small and results in one-sided pool
			// withdrawal due to round off.
			if withdrawAmount1.IsZero() || withdrawAmount2.IsZero() {
				return tx.TecAMM_FAILED
			}
			lpTokensToRedeem = tokensAdj
		}

	case flags&tfOneAssetWithdrawAll != 0:
		// Withdraw all LP tokens as a single asset (singleWithdrawTokens)
		// Reference: rippled routes tfOneAssetWithdrawAll to singleWithdrawTokens()
		if lpTokensHeld.IsZero() {
			return tx.TecAMM_INVALID_TOKENS
		}
		isWithdrawAsset1 := matchesAsset(a.Amount, a.Asset)

		// adjustLPTokensIn - for WithdrawAll, skip adjustment
		tokensAdj := lpTokensHeld
		// tokens are not adjusted for withdrawAll

		isSingleAssetWithdraw = true
		var assetBalance tx.Amount
		if isWithdrawAsset1 {
			assetBalance = assetBalance1
			withdrawAssetBalance = assetBalance1
		} else {
			assetBalance = assetBalance2
			withdrawAssetBalance = assetBalance2
			singleWithdrawIsAsset2 = true
		}

		// the adjusted tokens are factored in
		amountWithdraw := ammAssetOut(assetBalance, lptBalance, tokensAdj, tfee, fixV1_3)
		// For OneAssetWithdrawAll, amount==zero or amountWithdraw >= amount
		if !amount1.IsZero() && toIOUForCalc(amountWithdraw).Compare(toIOUForCalc(amount1)) < 0 {
			return tx.TecAMM_FAILED
		}

		if isWithdrawAsset1 {
			withdrawAmount1 = amountWithdraw
			withdrawAmount2 = zeroAmount(a.Asset2)
		} else {
			withdrawAmount1 = zeroAmount(a.Asset)
			withdrawAmount2 = amountWithdraw
		}
		lpTokensToRedeem = tokensAdj

	case flags&tfSingleAsset != 0:
		// Single asset withdrawal (singleWithdraw)
		// Reference: rippled AMMWithdraw.cpp singleWithdraw()
		if amount1.IsZero() {
			return tx.TemMALFORMED
		}
		isWithdrawAsset1 := matchesAsset(a.Amount, a.Asset)

		isSingleAssetWithdraw = true
		var assetBalance, withdrawAmt tx.Amount
		if isWithdrawAsset1 {
			assetBalance = assetBalance1
			withdrawAssetBalance = assetBalance1
			withdrawAmt = amount1
		} else {
			assetBalance = assetBalance2
			withdrawAssetBalance = assetBalance2
			singleWithdrawIsAsset2 = true
			withdrawAmt = amount1
		}

		// adjustLPTokensIn
		tokens := calcLPTokensIn(assetBalance, withdrawAmt, lptBalance, tfee, fixV1_3)
		if fixV1_3 {
			tokens = adjustLPTokens(lptBalance, tokens, false)
		}
		if tokens.IsZero() {
			if fixV1_3 {
				return tx.TecAMM_INVALID_TOKENS
			}
			return tx.TecAMM_INVALID_TOKENS
		}
		// factor in the adjusted tokens
		tokensAdj, amountWithdrawAdj := adjustAssetOutByTokens(fixV1_3, assetBalance, withdrawAmt, lptBalance, tokens, tfee)
		if fixV1_3 && tokensAdj.IsZero() {
			return tx.TecAMM_INVALID_TOKENS
		}

		if isWithdrawAsset1 {
			withdrawAmount1 = amountWithdrawAdj
			withdrawAmount2 = zeroAmount(a.Asset2)
		} else {
			withdrawAmount1 = zeroAmount(a.Asset)
			withdrawAmount2 = amountWithdrawAdj
		}
		lpTokensToRedeem = tokensAdj

	case flags&tfTwoAsset != 0:
		// Two asset withdrawal with limits (equalWithdrawLimit)
		// Reference: rippled AMMWithdraw.cpp equalWithdrawLimit()
		if amount1.IsZero() || amount2.IsZero() {
			return tx.TemMALFORMED
		}

		frac := numberDiv(toIOUForCalc(amount1), toIOUForCalc(assetBalance1))
		amount2Withdraw := getRoundedAsset(fixV1_3, assetBalance2, frac, false)
		tokensAdj := getRoundedLPTokens(fixV1_3, lptBalance, frac, false)
		if fixV1_3 && tokensAdj.IsZero() {
			return tx.TecAMM_INVALID_TOKENS
		}
		// factor in the adjusted tokens
		frac = adjustFracByTokens(fixV1_3, lptBalance, tokensAdj, frac)
		amount2Withdraw = getRoundedAsset(fixV1_3, assetBalance2, frac, false)

		if toIOUForCalc(amount2Withdraw).Compare(toIOUForCalc(amount2)) <= 0 {
			withdrawAmount1 = amount1
			withdrawAmount2 = amount2Withdraw
			lpTokensToRedeem = tokensAdj
		} else {
			frac = numberDiv(toIOUForCalc(amount2), toIOUForCalc(assetBalance2))
			amountWithdraw := getRoundedAsset(fixV1_3, assetBalance1, frac, false)
			tokensAdj = getRoundedLPTokens(fixV1_3, lptBalance, frac, false)
			if fixV1_3 && tokensAdj.IsZero() {
				return tx.TecAMM_INVALID_TOKENS
			}
			frac = adjustFracByTokens(fixV1_3, lptBalance, tokensAdj, frac)
			amountWithdraw = getRoundedAsset(fixV1_3, assetBalance1, frac, false)

			if fixV1_3 && toIOUForCalc(amountWithdraw).Compare(toIOUForCalc(amount1)) > 0 {
				return tx.TecAMM_FAILED
			}

			withdrawAmount1 = amountWithdraw
			withdrawAmount2 = amount2
			lpTokensToRedeem = tokensAdj
		}

	case flags&tfOneAssetLPToken != 0:
		// Single asset withdrawal for specific LP tokens (singleWithdrawTokens)
		// Reference: rippled AMMWithdraw.cpp singleWithdrawTokens()
		if lpTokensRequested.IsZero() {
			return tx.TecAMM_INVALID_TOKENS
		}
		if isGreater(toIOUForCalc(lpTokensRequested), toIOUForCalc(lpTokensHeld)) ||
			isGreater(toIOUForCalc(lpTokensRequested), toIOUForCalc(lptBalance)) {
			return tx.TecAMM_INVALID_TOKENS
		}
		isWithdrawAsset1 := matchesAsset(a.Amount, a.Asset)

		isSingleAssetWithdraw = true
		var assetBalance tx.Amount
		if isWithdrawAsset1 {
			assetBalance = assetBalance1
			withdrawAssetBalance = assetBalance1
		} else {
			assetBalance = assetBalance2
			withdrawAssetBalance = assetBalance2
			singleWithdrawIsAsset2 = true
		}

		// adjustLPTokensIn
		tokensAdj := lpTokensRequested
		if fixV1_3 {
			tokensAdj = adjustLPTokens(lptBalance, lpTokensRequested, false)
			if tokensAdj.IsZero() {
				return tx.TecAMM_INVALID_TOKENS
			}
		}

		// the adjusted tokens are factored in
		amountWithdraw := ammAssetOut(assetBalance, lptBalance, tokensAdj, tfee, fixV1_3)
		if amount1.IsZero() || toIOUForCalc(amountWithdraw).Compare(toIOUForCalc(amount1)) >= 0 {
			if isWithdrawAsset1 {
				withdrawAmount1 = amountWithdraw
				withdrawAmount2 = zeroAmount(a.Asset2)
			} else {
				withdrawAmount1 = zeroAmount(a.Asset)
				withdrawAmount2 = amountWithdraw
			}
			lpTokensToRedeem = tokensAdj
		} else {
			return tx.TecAMM_FAILED
		}

	case flags&tfLimitLPToken != 0:
		// Single asset withdrawal with effective price limit (singleWithdrawEPrice)
		// Reference: rippled AMMWithdraw.cpp singleWithdrawEPrice()
		if amount1.IsZero() || a.EPrice == nil || a.EPrice.IsZero() {
			return tx.TemMALFORMED
		}

		isWithdrawAsset1 := matchesAsset(a.Amount, a.Asset)
		isSingleAssetWithdraw = true
		var assetBalance tx.Amount
		if isWithdrawAsset1 {
			assetBalance = assetBalance1
			withdrawAssetBalance = assetBalance1
		} else {
			assetBalance = assetBalance2
			withdrawAssetBalance = assetBalance2
			singleWithdrawIsAsset2 = true
		}

		ePrice := *a.EPrice
		assetBalIOU := toIOUForCalc(assetBalance)
		lptBalIOU := toIOUForCalc(lptBalance)
		ePriceIOU := toIOUForCalc(ePrice)

		// t = T*(T + A*E*(f - 2))/(T*f - A*E)
		ae := assetBalIOU.Mul(ePriceIOU, false)
		f := getFee(tfee)
		two := numAmount(2)
		fMinus2, _ := f.Sub(two)
		aeFMinus2 := ae.Mul(fMinus2, false)
		tPlusAE, _ := lptBalIOU.Add(aeFMinus2)
		tf := lptBalIOU.Mul(f, false)
		tfMinusAE, _ := tf.Sub(ae)

		tokensAdj := getRoundedLPTokensCb(fixV1_3,
			func() tx.Amount { return numberDiv(lptBalIOU.Mul(tPlusAE, false), tfMinusAE) },
			lptBalance,
			func() tx.Amount { return numberDiv(tPlusAE, tfMinusAE) },
			false)

		if tokensAdj.IsZero() || tokensAdj.IsNegative() {
			if fixV1_3 {
				return tx.TecAMM_INVALID_TOKENS
			}
			return tx.TecAMM_FAILED
		}

		tokensAdjIOU := toIOUForCalc(tokensAdj)
		amountWithdraw := getRoundedAssetCb(fixV1_3,
			func() tx.Amount { return numberDiv(tokensAdjIOU, ePriceIOU) },
			amount1,
			func() tx.Amount { return numberDiv(tokensAdjIOU, ePriceIOU) },
			false)

		if amount1.IsZero() || toIOUForCalc(amountWithdraw).Compare(toIOUForCalc(amount1)) >= 0 {
			if isWithdrawAsset1 {
				withdrawAmount1 = amountWithdraw
				withdrawAmount2 = zeroAmount(a.Asset2)
			} else {
				withdrawAmount1 = zeroAmount(a.Asset)
				withdrawAmount2 = amountWithdraw
			}
			lpTokensToRedeem = tokensAdj
		} else {
			return tx.TecAMM_FAILED
		}

	default:
		return tx.TemMALFORMED
	}

	if lpTokensToRedeem.IsZero() {
		return tx.TecAMM_INVALID_TOKENS
	}

	// Run adjustAmountsByLPTokens for withdrawal (non-withdrawAll modes)
	// Reference: rippled AMMWithdraw.cpp withdraw() calls adjustAmountsByLPTokens
	// Single-asset modes call withdraw(amountBalance, amountWithdraw, nullopt, ...)
	// where amountBalance is the withdrawn asset's balance. Two-asset modes call
	// withdraw(amountBalance, amount, amount2, ...) with both amounts.
	if !isWithdrawAll {
		fixV1_1 := ctx.Rules().Enabled(amendment.FeatureFixAMMv1_1)
		if isSingleAssetWithdraw {
			var withdrawAmt tx.Amount
			if singleWithdrawIsAsset2 {
				withdrawAmt = withdrawAmount2
			} else {
				withdrawAmt = withdrawAmount1
			}
			adjAmt, _, adjTokens := adjustAmountsByLPTokens(
				withdrawAssetBalance, withdrawAmt, nil, lptBalance, lpTokensToRedeem, tfee, false, fixV1_3, fixV1_1)
			lpTokensToRedeem = adjTokens
			if singleWithdrawIsAsset2 {
				withdrawAmount2 = adjAmt
			} else {
				withdrawAmount1 = adjAmt
			}
		} else {
			var amount2Ptr *tx.Amount
			if !withdrawAmount2.IsZero() {
				amount2Ptr = &withdrawAmount2
			}
			withdrawAmount1, amount2Ptr, lpTokensToRedeem = adjustAmountsByLPTokens(
				assetBalance1, withdrawAmount1, amount2Ptr, lptBalance, lpTokensToRedeem, tfee, false, fixV1_3, fixV1_1)
			if amount2Ptr != nil {
				withdrawAmount2 = *amount2Ptr
			}
		}
	}

	// Verify LP tokens
	if lpTokensToRedeem.IsZero() || isGreater(toIOUForCalc(lpTokensToRedeem), toIOUForCalc(lpTokensHeld)) {
		return tx.TecAMM_INVALID_TOKENS
	}

	// Verify withdrawal doesn't exceed balances
	if isGreater(toIOUForCalc(withdrawAmount1), toIOUForCalc(assetBalance1)) {
		return tx.TecAMM_BALANCE
	}
	if isGreater(toIOUForCalc(withdrawAmount2), toIOUForCalc(assetBalance2)) {
		return tx.TecAMM_BALANCE
	}

	// Per rippled: Cannot withdraw one side of the pool while leaving the other
	w1EqualsB1 := toIOUForCalc(withdrawAmount1).Compare(toIOUForCalc(assetBalance1)) == 0
	w2EqualsB2 := toIOUForCalc(withdrawAmount2).Compare(toIOUForCalc(assetBalance2)) == 0
	if (w1EqualsB1 && !w2EqualsB2) || (w2EqualsB2 && !w1EqualsB1) {
		return tx.TecAMM_BALANCE
	}

	// May happen if withdrawing an amount close to one side of the pool
	if toIOUForCalc(lpTokensToRedeem).Compare(toIOUForCalc(lptBalance)) == 0 &&
		(!w1EqualsB1 || !w2EqualsB2) {
		return tx.TecAMM_BALANCE
	}

	// Transfer assets from AMM to withdrawer
	isXRP1 := a.Asset.Currency == "" || a.Asset.Currency == "XRP"
	isXRP2 := a.Asset2.Currency == "" || a.Asset2.Currency == "XRP"

	if isXRP1 && !withdrawAmount1.IsZero() {
		// Convert to drops, handling IOU representation from calculations
		drops := uint64(iouToDrops(withdrawAmount1))
		ammAccount.Balance -= drops
		ctx.Account.Balance += drops
	}
	if isXRP2 && !withdrawAmount2.IsZero() {
		// Convert to drops, handling IOU representation from calculations
		drops := uint64(iouToDrops(withdrawAmount2))
		ammAccount.Balance -= drops
		ctx.Account.Balance += drops
	}

	// For IOU transfers: check reserve if trust line creation is needed,
	// then transfer tokens.
	// Reference: rippled AMMWithdraw.cpp lines 581-647
	enabledFixAMMv1_2 := ctx.Rules().Enabled(amendment.FeatureFixAMMv1_2)

	if !isXRP1 && !withdrawAmount1.IsZero() {
		issuerID, err := state.DecodeAccountID(a.Asset.Issuer)
		if err != nil {
			return tx.TefINTERNAL
		}
		if result := withdrawIOUToAccount(ctx, accountID, issuerID, ammAccountID, a.Asset, withdrawAmount1, enabledFixAMMv1_2); result != tx.TesSUCCESS {
			return result
		}
	}
	if !isXRP2 && !withdrawAmount2.IsZero() {
		issuerID, err := state.DecodeAccountID(a.Asset2.Issuer)
		if err != nil {
			return tx.TefINTERNAL
		}
		if result := withdrawIOUToAccount(ctx, accountID, issuerID, ammAccountID, a.Asset2, withdrawAmount2, enabledFixAMMv1_2); result != tx.TesSUCCESS {
			return result
		}
	}

	// Redeem LP tokens: debit withdrawer's trust line, then reduce AMM LP balance.
	// Reference: rippled AMMWithdraw.cpp — redeemIOU(account_, lpTokensActual, lpTokens.issue())
	if !lpTokensToRedeem.IsZero() {
		lptCurrency := GenerateAMMLPTCurrency(amm.Asset.Currency, amm.Asset2.Currency)
		ammAccountAddr, _ := state.EncodeAccountID(amm.Account)
		redeemAmt := state.NewIssuedAmountFromValue(
			lpTokensToRedeem.Mantissa(), lpTokensToRedeem.Exponent(), lptCurrency, ammAccountAddr)
		if r := redeemLPTokens(ctx.View, accountID, amm.Account, redeemAmt); r != tx.TesSUCCESS {
			return r
		}
	}
	newLPBalance, err := amm.LPTokenBalance.Sub(lpTokensToRedeem)
	if err != nil {
		return tx.TefINTERNAL
	}
	// NOTE: Asset balances are NOT stored in AMM entry
	// They are updated by the balance transfers above:
	// - XRP: via ammAccount.Balance -= drops
	// - IOU: via trustline updates (createOrUpdateAMMTrustline)

	// Check if AMM should be deleted (empty) or updated
	// Reference: rippled AMMWithdraw.cpp deleteAMMAccountIfEmpty (line 718)
	deleteResult := deleteAMMAccountIfEmpty(ctx.View, ammKey, ammAccountKey,
		newLPBalance, a.Asset, a.Asset2, amm, ammAccount)
	if deleteResult != tx.TesSUCCESS && deleteResult != tx.TecINCOMPLETE {
		return deleteResult
	}

	accountKey := keylet.Account(accountID)
	accountBytes, err := state.SerializeAccountRoot(ctx.Account)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(accountKey, accountBytes); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// withdrawIOUToAccount handles IOU transfer from AMM to withdrawer, including
// reserve check and trust line creation when needed.
// Reference: rippled AMMWithdraw.cpp sufficientReserve (lines 581-603) +
// accountSend (lines 609-646)
func withdrawIOUToAccount(
	ctx *tx.ApplyContext,
	accountID, issuerID, ammAccountID [20]byte,
	asset tx.Asset,
	amount tx.Amount,
	enabledFixAMMv1_2 bool,
) tx.Result {
	// Check if withdrawer already has a trust line for this IOU.
	trustLineKey := keylet.Line(accountID, issuerID, asset.Currency)
	trustLineExists, err := ctx.View.Exists(trustLineKey)
	if err != nil {
		return tx.TefINTERNAL
	}

	if !trustLineExists {
		// Reserve check: with fixAMMv1_2, verify the withdrawer has enough
		// reserve for the new trust line before creating it.
		// Reference: rippled AMMWithdraw.cpp lines 583-601
		if enabledFixAMMv1_2 {
			ownerCount := ctx.Account.OwnerCount
			// See also SetTrust::doApply(): ownerCount < 2 → no reserve needed
			if ownerCount >= 2 {
				reserve := ctx.AccountReserve(ownerCount + 1)
				if ctx.Account.Balance < reserve {
					return tx.TecINSUFFICIENT_RESERVE
				}
			}
		}

		// Create trust line for the withdrawer.
		// Reference: rippled uses accountSend → rippleCredit → trustCreate
		if result := createWithdrawTrustLine(ctx, accountID, issuerID, asset, amount, trustLineKey); result != tx.TesSUCCESS {
			return result
		}
	} else {
		// Trust line exists — just credit the withdrawer's balance.
		if err := updateTrustlineBalanceInView(accountID, issuerID, asset.Currency, amount, ctx.View); err != nil {
			return tx.TefINTERNAL
		}
	}

	// Debit AMM's trust line (negative delta)
	if err := createOrUpdateAMMTrustline(ammAccountID, asset, amount.Negate(), ctx.View); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// createWithdrawTrustLine creates a new trust line between withdrawer and
// issuer, setting the initial balance to the withdraw amount.
// Reference: rippled trustCreate via accountSend path
func createWithdrawTrustLine(
	ctx *tx.ApplyContext,
	accountID, issuerID [20]byte,
	asset tx.Asset,
	amount tx.Amount,
	trustLineKey keylet.Keylet,
) tx.Result {
	// Determine low/high accounts
	accountIsLow := keylet.IsLowAccount(accountID, issuerID)
	var lowAccountID, highAccountID [20]byte
	if accountIsLow {
		lowAccountID = accountID
		highAccountID = issuerID
	} else {
		lowAccountID = issuerID
		highAccountID = accountID
	}

	lowAccountStr, err := state.EncodeAccountID(lowAccountID)
	if err != nil {
		return tx.TefINTERNAL
	}
	highAccountStr, err := state.EncodeAccountID(highAccountID)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Set balance: the withdrawer (receiver) gets the tokens.
	// Convention: positive balance = LOW account holds tokens.
	// When receiver (account) is LOW → positive balance
	// When receiver (account) is HIGH → negative balance
	var balance tx.Amount
	if accountIsLow {
		balance = state.NewIssuedAmountFromValue(
			amount.Mantissa(), amount.Exponent(),
			asset.Currency, state.AccountOneAddress,
		)
	} else {
		negated := amount.Negate()
		balance = state.NewIssuedAmountFromValue(
			negated.Mantissa(), negated.Exponent(),
			asset.Currency, state.AccountOneAddress,
		)
	}

	// Flags: receiver gets reserve flag + NoRipple per DefaultRipple setting
	// Reference: rippled trustCreate
	var flags uint32
	if accountIsLow {
		flags |= state.LsfLowReserve
	} else {
		flags |= state.LsfHighReserve
	}

	// Set NoRipple based on DefaultRipple for each side
	acctData, err := ctx.View.Read(keylet.Account(accountID))
	if err != nil || acctData == nil {
		return tx.TefINTERNAL
	}
	acct, err := state.ParseAccountRoot(acctData)
	if err != nil {
		return tx.TefINTERNAL
	}
	if (acct.Flags & state.LsfDefaultRipple) == 0 {
		if accountIsLow {
			flags |= state.LsfLowNoRipple
		} else {
			flags |= state.LsfHighNoRipple
		}
	}

	issuerAcctData, err := ctx.View.Read(keylet.Account(issuerID))
	if err != nil || issuerAcctData == nil {
		return tx.TefINTERNAL
	}
	issuerAcct, err := state.ParseAccountRoot(issuerAcctData)
	if err != nil {
		return tx.TefINTERNAL
	}
	if (issuerAcct.Flags & state.LsfDefaultRipple) == 0 {
		if !accountIsLow {
			flags |= state.LsfLowNoRipple
		} else {
			flags |= state.LsfHighNoRipple
		}
	}

	rs := &state.RippleState{
		Balance:   balance,
		LowLimit:  tx.NewIssuedAmount(0, -100, asset.Currency, lowAccountStr),
		HighLimit: tx.NewIssuedAmount(0, -100, asset.Currency, highAccountStr),
		Flags:     flags,
	}

	// Insert into both owner directories
	lowDirKey := keylet.OwnerDir(lowAccountID)
	lowDirResult, err := state.DirInsert(ctx.View, lowDirKey, trustLineKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = lowAccountID
	})
	if err != nil {
		return tx.TefINTERNAL
	}
	rs.LowNode = lowDirResult.Page

	highDirKey := keylet.OwnerDir(highAccountID)
	highDirResult, err := state.DirInsert(ctx.View, highDirKey, trustLineKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = highAccountID
	})
	if err != nil {
		return tx.TefINTERNAL
	}
	rs.HighNode = highDirResult.Page

	// Serialize and insert
	rsBytes, err := state.SerializeRippleState(rs)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Insert(trustLineKey, rsBytes); err != nil {
		return tx.TefINTERNAL
	}

	// Increment withdrawer's owner count for the new trust line
	ctx.Account.OwnerCount++

	return tx.TesSUCCESS
}

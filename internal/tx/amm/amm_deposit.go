package amm

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// AMMDeposit deposits assets into an AMM.
type AMMDeposit struct {
	tx.BaseTx

	// Asset identifies the first asset of the AMM (required)
	Asset tx.Asset `json:"Asset" xrpl:"Asset,asset"`

	// Asset2 identifies the second asset of the AMM (required)
	Asset2 tx.Asset `json:"Asset2" xrpl:"Asset2,asset"`

	// Amount is the amount of first asset to deposit (optional)
	Amount *tx.Amount `json:"Amount,omitempty" xrpl:"Amount,omitempty,amount"`

	// Amount2 is the amount of second asset to deposit (optional)
	Amount2 *tx.Amount `json:"Amount2,omitempty" xrpl:"Amount2,omitempty,amount"`

	// EPrice is the effective price limit (optional)
	EPrice *tx.Amount `json:"EPrice,omitempty" xrpl:"EPrice,omitempty,amount"`

	// LPTokenOut is the LP tokens to receive (optional)
	LPTokenOut *tx.Amount `json:"LPTokenOut,omitempty" xrpl:"LPTokenOut,omitempty,amount"`

	// TradingFee is the trading fee for tfTwoAssetIfEmpty mode (optional)
	// Only used when depositing into an empty AMM
	TradingFee uint16 `json:"TradingFee,omitempty" xrpl:"TradingFee,omitempty"`
}

// NewAMMDeposit creates a new AMMDeposit transaction
func NewAMMDeposit(account string, asset, asset2 tx.Asset) *AMMDeposit {
	return &AMMDeposit{
		BaseTx: *tx.NewBaseTx(tx.TypeAMMDeposit, account),
		Asset:  asset,
		Asset2: asset2,
	}
}

func (a *AMMDeposit) TxType() tx.Type {
	return tx.TypeAMMDeposit
}

// GetAMMAsset returns the first asset of the AMM (Asset field).
// Implements ammAssetProvider for the ValidAMM invariant checker.
func (a *AMMDeposit) GetAMMAsset() tx.Asset {
	return a.Asset
}

// GetAMMAsset2 returns the second asset of the AMM (Asset2 field).
// Implements ammAssetProvider for the ValidAMM invariant checker.
func (a *AMMDeposit) GetAMMAsset2() tx.Asset {
	return a.Asset2
}

// Reference: rippled AMMDeposit.cpp preflight lines 32-162
func (a *AMMDeposit) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	flags := a.GetFlags()

	// Check for invalid flags
	// Reference: rippled AMMDeposit.cpp line 42-46
	if flags&tfAMMDepositMask != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "invalid flags for AMMDeposit")
	}

	// Must have exactly one deposit mode flag set
	// Reference: rippled AMMDeposit.cpp line 60-64 - std::popcount(flags & tfDepositSubTx) != 1
	depositModeFlags := flags & tfDepositSubTx
	flagCount := 0
	for f := depositModeFlags; f != 0; f &= f - 1 {
		flagCount++
	}
	if flagCount != 1 {
		return tx.Errorf(tx.TemMALFORMED, "must specify exactly one deposit mode flag")
	}

	// Validate flag-specific field combinations
	// Reference: rippled AMMDeposit.cpp lines 65-98
	hasAmount := a.Amount != nil
	hasAmount2 := a.Amount2 != nil
	hasEPrice := a.EPrice != nil
	hasLPTokens := a.LPTokenOut != nil
	hasTradingFee := a.TradingFee > 0

	if flags&tfLPToken != 0 {
		// tfLPToken: LPTokenOut required, [Amount, Amount2] optional but must be both or neither, no EPrice, no TradingFee
		if !hasLPTokens || hasEPrice || (hasAmount && !hasAmount2) || (!hasAmount && hasAmount2) || hasTradingFee {
			return tx.Errorf(tx.TemMALFORMED, "tfLPToken requires LPTokenOut, optional Amount+Amount2 pair")
		}
	} else if flags&tfSingleAsset != 0 {
		// tfSingleAsset: Amount required, no Amount2, no EPrice, no TradingFee
		if !hasAmount || hasAmount2 || hasEPrice || hasTradingFee {
			return tx.Errorf(tx.TemMALFORMED, "tfSingleAsset requires Amount only")
		}
	} else if flags&tfTwoAsset != 0 {
		// tfTwoAsset: Amount and Amount2 required, no EPrice, no TradingFee
		if !hasAmount || !hasAmount2 || hasEPrice || hasTradingFee {
			return tx.Errorf(tx.TemMALFORMED, "tfTwoAsset requires Amount and Amount2")
		}
	} else if flags&tfOneAssetLPToken != 0 {
		// tfOneAssetLPToken: Amount and LPTokenOut required, no Amount2, no EPrice, no TradingFee
		if !hasAmount || !hasLPTokens || hasAmount2 || hasEPrice || hasTradingFee {
			return tx.Errorf(tx.TemMALFORMED, "tfOneAssetLPToken requires Amount and LPTokenOut")
		}
	} else if flags&tfLimitLPToken != 0 {
		// tfLimitLPToken: Amount and EPrice required, no LPTokens, no Amount2, no TradingFee
		if !hasAmount || !hasEPrice || hasLPTokens || hasAmount2 || hasTradingFee {
			return tx.Errorf(tx.TemMALFORMED, "tfLimitLPToken requires Amount and EPrice")
		}
	} else if flags&tfTwoAssetIfEmpty != 0 {
		// tfTwoAssetIfEmpty: Amount and Amount2 required, no EPrice, no LPTokens
		if !hasAmount || !hasAmount2 || hasEPrice || hasLPTokens {
			return tx.Errorf(tx.TemMALFORMED, "tfTwoAssetIfEmpty requires Amount and Amount2")
		}
	}

	// Validate asset pair
	// Reference: rippled AMMDeposit.cpp lines 100-106
	if err := validateAssetPair(a.Asset, a.Asset2); err != nil {
		return err
	}

	// Amount and Amount2 cannot have the same issue
	// Reference: rippled AMMDeposit.cpp lines 108-113
	if hasAmount && hasAmount2 {
		if a.Amount.Currency == a.Amount2.Currency && a.Amount.Issuer == a.Amount2.Issuer {
			return tx.Errorf(tx.TemBAD_AMM_TOKENS, "Amount and Amount2 have same issue")
		}
	}

	// Validate LPTokenOut if provided
	// Reference: rippled AMMDeposit.cpp lines 115-119
	if hasLPTokens {
		if a.LPTokenOut.IsZero() || a.LPTokenOut.IsNegative() {
			return tx.Errorf(tx.TemBAD_AMM_TOKENS, "invalid LPTokens")
		}
	}

	// Validate Amount if provided. validZero is true only when EPrice is present.
	if hasAmount {
		if err := validateAMMAmountWithPair(*a.Amount, &a.Asset, &a.Asset2, hasEPrice); err != nil {
			return err
		}
	}

	// Validate Amount2 if provided
	if hasAmount2 {
		if err := validateAMMAmountWithPair(*a.Amount2, &a.Asset, &a.Asset2, false); err != nil {
			return err
		}
	}

	// Validate EPrice if provided — must match Amount's issue
	if hasAmount && hasEPrice {
		amtIssue := tx.Asset{Currency: a.Amount.Currency, Issuer: a.Amount.Issuer}
		if err := validateAMMAmountWithPair(*a.EPrice, &amtIssue, &amtIssue, false); err != nil {
			return err
		}
	}

	// Validate TradingFee if provided
	// Reference: rippled AMMDeposit.cpp lines 156-160
	if a.TradingFee > tradingFeeThreshold {
		return tx.Errorf(tx.TemBAD_FEE, "TradingFee must be 0-1000")
	}

	return nil
}

func (a *AMMDeposit) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(a)
}

func (a *AMMDeposit) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureAMM, amendment.FeatureFixUniversalNumber}
}

// Preclaim performs the stateful deposit validation against the unmodified
// view: AMM existence and pool sanity, authorization/freeze, per-amount
// funding (including the LP-token-trustline reserve), and the LPTokenOut issue.
// The view's source-account balance is already the pre-fee balance.
// Reference: rippled AMMDeposit.cpp preclaim
func (a *AMMDeposit) Preclaim(view tx.LedgerView, config tx.EngineConfig) tx.Result {
	accountID, err := state.DecodeAccountID(a.Account)
	if err != nil {
		return tx.TemBAD_SRC_ACCOUNT
	}
	account, result := readAccount(view, accountID)
	if result != tx.TesSUCCESS {
		return result
	}

	amm, _, result := readAMM(view, a.Asset, a.Asset2)
	if result != tx.TesSUCCESS {
		return result
	}
	ammAccountID := amm.Account

	flags := a.GetFlags()

	assetBalance1, assetBalance2, lptBalance := AMMHolds(view, amm, false)
	if !matchesAssetByIssue(amm.Asset, a.Asset) {
		assetBalance1, assetBalance2 = assetBalance2, assetBalance1
	}

	if flags&tfTwoAssetIfEmpty != 0 {
		if !lptBalance.IsZero() {
			return tx.TecAMM_NOT_EMPTY
		}
		if !assetBalance1.IsZero() || !assetBalance2.IsZero() {
			return tx.TecINTERNAL
		}
	} else {
		if lptBalance.IsZero() {
			return tx.TecAMM_EMPTY
		}
		if assetBalance1.IsZero() || assetBalance1.IsNegative() ||
			assetBalance2.IsZero() || assetBalance2.IsNegative() ||
			lptBalance.IsNegative() {
			return tx.TecINTERNAL
		}
	}

	lptCurrency := GenerateAMMLPTCurrency(a.Asset.Currency, a.Asset2.Currency)
	lptKey := keylet.Line(accountID, ammAccountID, lptCurrency)
	lptExists, _ := view.Exists(lptKey)

	// Check authorization and freeze status for BOTH pool assets — only when
	// AMMClawback is enabled.
	// Reference: rippled AMMDeposit.cpp lines 244-273
	if config.GetRules().Enabled(amendment.FeatureAMMClawback) {
		if result := requireAuth(view, a.Asset, accountID); result != tx.TesSUCCESS {
			return result
		}
		if result := requireAuth(view, a.Asset2, accountID); result != tx.TesSUCCESS {
			return result
		}
		if isFrozen(view, accountID, a.Asset) || isFrozen(view, accountID, a.Asset2) {
			return tx.TecFROZEN
		}
	}

	checkAmount := func(amt *tx.Amount, checkBalance bool) tx.Result {
		if amt == nil {
			return tx.TesSUCCESS
		}
		amtAsset := tx.Asset{Currency: amt.Currency, Issuer: amt.Issuer}
		if result := requireAuth(view, amtAsset, accountID); result != tx.TesSUCCESS {
			return result
		}
		if isFrozen(view, ammAccountID, amtAsset) {
			return tx.TecFROZEN
		}
		if tx.IsIndividualFrozen(view, accountID, amtAsset) {
			return tx.TecFROZEN
		}
		if checkBalance {
			if isXRPAsset(amtAsset) {
				// XRP liquid balance including reserve for the LP trustline.
				// The view balance is the pre-fee balance.
				// Reference: rippled AMMDeposit.cpp preclaim balance lambda lines 220-231
				extraOwner := uint32(0)
				if !lptExists {
					extraOwner = 1
				}
				reserve := accountReserve(config, account.OwnerCount+extraOwner)
				xrpLiquid := int64(account.Balance) - int64(reserve)
				if xrpLiquid < amt.Drops() {
					if lptExists {
						return TecUNFUNDED_AMM
					}
					return TecINSUF_RESERVE_LINE
				}
			} else {
				issuerID, _ := state.DecodeAccountID(amtAsset.Issuer)
				if accountID != issuerID {
					depositorFunds := tx.AccountFunds(view, accountID, *amt, false, config.ReserveBase, config.ReserveIncrement)
					if depositorFunds.Compare(*amt) < 0 {
						return TecUNFUNDED_AMM
					}
				}
			}
		}
		return tx.TesSUCCESS
	}

	checkFreezeForAsset := func(asset tx.Asset) tx.Result {
		amt := zeroAmount(asset)
		return checkAmount(&amt, false)
	}

	if flags&tfLPToken != 0 {
		if r := checkFreezeForAsset(a.Asset); r != tx.TesSUCCESS {
			return r
		}
		if r := checkFreezeForAsset(a.Asset2); r != tx.TesSUCCESS {
			return r
		}
	} else {
		if r := checkAmount(a.Amount, true); r != tx.TesSUCCESS {
			return r
		}
		if r := checkAmount(a.Amount2, true); r != tx.TesSUCCESS {
			return r
		}
	}

	// LPTokenOut issue must match the AMM's LP token issue.
	// Reference: rippled AMMDeposit.cpp preclaim lines 343-349
	if a.LPTokenOut != nil {
		if a.LPTokenOut.Currency != amm.LPTokenBalance.Currency ||
			a.LPTokenOut.Issuer != amm.LPTokenBalance.Issuer {
			return tx.TemBAD_AMM_TOKENS
		}
	}

	// Reserve for the LP token trustline if the depositor holds no LP tokens.
	// Reference: rippled AMMDeposit.cpp preclaim lines 353-362
	if ammLPHolds(view, amm, accountID).IsZero() {
		if insufficientLPTokenReserve(account, config) {
			return TecINSUF_RESERVE_LINE
		}
	}

	return tx.TesSUCCESS
}

// Reference: rippled AMMDeposit.cpp applyGuts
func (a *AMMDeposit) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("amm deposit apply",
		"account", a.Account,
		"asset", a.Asset,
		"asset2", a.Asset2,
		"amount", a.Amount,
		"amount2", a.Amount2,
		"flags", a.GetFlags(),
	)

	accountID := ctx.AccountID

	// Re-derive the AMM, its pseudo-account, and the pool balances against the
	// (now fee-deducted) view. Preclaim has already validated state.
	// Reference: rippled AMMDeposit.cpp applyGuts re-peeks the AMM.
	loaded, result := loadAMM(ctx.View, a.Asset, a.Asset2, a.Asset)
	if result != tx.TesSUCCESS {
		return result
	}
	amm := loaded.Data
	ammKey := loaded.Key
	ammAccountID := loaded.AccountID
	ammAccountKey := loaded.AccountKey
	ammAccount := loaded.Account
	assetBalance1, assetBalance2, lptBalance := loaded.AssetBalance1, loaded.AssetBalance2, loaded.LPTokenBalance

	flags := a.GetFlags()
	ammAccountAddr, _ := encodeAccountID(ammAccountID)
	lptCurrency := GenerateAMMLPTCurrency(a.Asset.Currency, a.Asset2.Currency)
	lptExists, _ := ctx.View.Exists(keylet.Line(accountID, ammAccountID, lptCurrency))

	// =========================================================================
	// APPLY - Reference: rippled AMMDeposit.cpp applyGuts lines 367-480
	// =========================================================================

	// Get trading fee - use existing or from transaction for empty AMM.
	// When the pool is non-empty, check the auction slot for a discounted fee.
	// Reference: rippled AMMDeposit.cpp doApply() line 389-391
	var tfee uint16
	if lptBalance.IsZero() {
		if a.TradingFee > 0 {
			tfee = a.TradingFee
		}
	} else {
		tfee = getAccountTradingFee(amm, accountID, ctx.Config.ParentCloseTime)
	}

	// Amendment checks
	fixV1_3 := ctx.Rules().Enabled(amendment.FeatureFixAMMv1_3)
	fixV1_1 := ctx.Rules().Enabled(amendment.FeatureFixAMMv1_1)

	// Get amounts from transaction.
	// In rippled, ammHolds() reorders pool balances to match Amount/Amount2 issues,
	// so amountBalance always corresponds to sfAmount. In our code, assetBalance1/2
	// are in a.Asset/a.Asset2 order. We reorder the tx amounts to match that order.
	// Reference: rippled AMMDeposit.cpp applyGuts lines 379-388
	amount1 := zeroAmount(a.Asset)
	amount2 := zeroAmount(a.Asset2)
	lpTokensRequested := zeroAmount(tx.Asset{}) // LP tokens

	if a.Amount != nil {
		amount1 = *a.Amount
	}
	if a.Amount2 != nil {
		amount2 = *a.Amount2
	}
	if a.LPTokenOut != nil {
		lpTokensRequested = *a.LPTokenOut
	}

	// Reorder amount1/amount2 to match assetBalance1/assetBalance2 (a.Asset/a.Asset2 order).
	// If Amount's issue matches a.Asset2 (not a.Asset), swap the amounts so that
	// amount1 corresponds to a.Asset and amount2 corresponds to a.Asset2.
	// This matches rippled's ammHolds issue-hint reordering behavior.
	if a.Amount != nil && a.Amount2 != nil {
		amountIssue := tx.Asset{Currency: a.Amount.Currency, Issuer: a.Amount.Issuer}
		if matchesAssetByIssue(a.Asset2, amountIssue) && !matchesAssetByIssue(a.Asset, amountIssue) {
			amount1, amount2 = amount2, amount1
		}
	}

	// Result amounts - use tx.Amount for precision
	var lpTokensToIssue tx.Amount
	var depositAmount1, depositAmount2 tx.Amount

	// Track the deposit convention for adjustAmountsByLPTokens.
	// rippled's deposit() receives (amountBalance, amountDeposit, amount2Deposit):
	// - For single-asset: amountBalance = deposited asset's pool balance, amount2 = nullopt
	// - For two-asset: amountBalance = asset1 pool balance, amount2 = asset2 deposit
	// We track which asset is the "primary" deposit to reconstruct this at the end.
	var depositAssetBalance tx.Amount // pool balance for the primary deposited asset
	isSingleAssetDeposit := false     // true if only one asset is being deposited
	singleDepositIsAsset2 := false    // true if the single deposit is for asset2

	// For tfLPToken, the optional Amount/Amount2 are deposit minimums compared
	// against the POST-adjustment deposit amounts (after adjustAmountsByLPTokens),
	// mapped to asset1/asset2 order. Reference: rippled AMMDeposit.cpp deposit()
	// lines 536-565 — the comparison runs after adjustAmountsByLPTokens.
	var depositMin1, depositMin2 *tx.Amount

	// Handle different deposit modes
	// Reference: rippled AMMDeposit.cpp applyGuts()
	switch {
	case flags&tfLPToken != 0:
		// Proportional deposit for specified LP tokens (equalDepositTokens)
		// Reference: rippled AMMDeposit.cpp equalDepositTokens()
		if lpTokensRequested.IsZero() || lptBalance.IsZero() {
			return tx.TecAMM_INVALID_TOKENS
		}

		// adjustLPTokensOut
		tokensAdj := lpTokensRequested
		if fixV1_3 {
			tokensAdj = adjustLPTokens(lptBalance, lpTokensRequested, true)
			if tokensAdj.IsZero() {
				return tx.TecAMM_INVALID_TOKENS
			}
		}

		// frac = tokensAdj / lptBalance
		// Use stAmountDiv to match rippled's divide(STAmount, STAmount, Issue)
		// which adds +5 rounding, unlike Number division.
		// Reference: rippled AMMDeposit.cpp equalDepositTokens line 661
		frac := stAmountDiv(toIOUForCalc(tokensAdj), toIOUForCalc(lptBalance))
		// amounts factor in the adjusted tokens
		depositAmount1 = getRoundedAsset(fixV1_3, assetBalance1, frac, true)
		depositAmount2 = getRoundedAsset(fixV1_3, assetBalance2, frac, true)
		lpTokensToIssue = tokensAdj

		// When Amount/Amount2 are specified with tfLPToken they are deposit
		// minimums (depositMin/deposit2Min in rippled's equalDepositTokens), mapped
		// to asset1/asset2 order here. The comparison against the deposit amounts is
		// deferred until after adjustAmountsByLPTokens, matching rippled's deposit().
		depositMin1, depositMin2 = mapAmountsToAssetOrder(a.Amount, a.Amount2, a.Asset)

	case flags&tfSingleAsset != 0:
		// Single asset deposit (singleDeposit)
		// Reference: rippled AMMDeposit.cpp singleDeposit()
		isDepositForAsset1 := a.Amount != nil && matchesAsset(a.Amount, a.Asset)
		isDepositForAsset2 := a.Amount != nil && matchesAsset(a.Amount, a.Asset2)

		var assetBalance, depositAmt tx.Amount
		if isDepositForAsset1 {
			assetBalance = assetBalance1
			depositAmt = amount1
		} else if isDepositForAsset2 {
			assetBalance = assetBalance2
			depositAmt = amount1
		} else {
			return tx.TecAMM_INVALID_TOKENS
		}

		// adjustLPTokensOut
		tokens := lpTokensOut(assetBalance, depositAmt, lptBalance, tfee, fixV1_3)
		if fixV1_3 {
			tokens = adjustLPTokens(lptBalance, tokens, true)
		}
		if tokens.IsZero() {
			return tx.TecAMM_INVALID_TOKENS
		}
		// factor in the adjusted tokens
		tokensAdj, amountDepositAdj := adjustAssetInByTokens(fixV1_3, assetBalance, depositAmt, lptBalance, tokens, tfee)
		if fixV1_3 && tokensAdj.IsZero() {
			return tx.TecAMM_INVALID_TOKENS
		}
		lpTokensToIssue = tokensAdj
		isSingleAssetDeposit = true
		depositAssetBalance = assetBalance
		if isDepositForAsset1 {
			depositAmount1 = amountDepositAdj
			depositAmount2 = zeroAmount(a.Asset2)
		} else {
			singleDepositIsAsset2 = true
			depositAmount1 = zeroAmount(a.Asset)
			depositAmount2 = amountDepositAdj
		}

	case flags&tfTwoAsset != 0:
		// Two asset deposit with limits (equalDepositLimit)
		// Reference: rippled AMMDeposit.cpp equalDepositLimit()
		lpTokensDepositMin := a.LPTokenOut // optional minimum

		frac := numberDiv(toIOUForCalc(amount1), toIOUForCalc(assetBalance1))
		tokensAdj := getRoundedLPTokens(fixV1_3, lptBalance, frac, true)

		if tokensAdj.IsZero() {
			if fixV1_3 {
				return tx.TecAMM_INVALID_TOKENS
			}
			return tx.TecAMM_FAILED
		}
		// factor in the adjusted tokens
		frac = adjustFracByTokens(fixV1_3, lptBalance, tokensAdj, frac)
		amount2Deposit := getRoundedAsset(fixV1_3, assetBalance2, frac, true)

		if toIOUForCalc(amount2Deposit).Compare(toIOUForCalc(amount2)) <= 0 {
			depositAmount1 = amount1
			depositAmount2 = amount2Deposit
			lpTokensToIssue = tokensAdj
			// Check lpTokensDepositMin
			if lpTokensDepositMin != nil && toIOUForCalc(lpTokensToIssue).Compare(toIOUForCalc(*lpTokensDepositMin)) < 0 {
				return tx.TecAMM_FAILED
			}
		} else {
			// Try the other way
			frac = numberDiv(toIOUForCalc(amount2), toIOUForCalc(assetBalance2))
			tokensAdj = getRoundedLPTokens(fixV1_3, lptBalance, frac, true)

			if tokensAdj.IsZero() {
				if fixV1_3 {
					return tx.TecAMM_INVALID_TOKENS
				}
				return tx.TecAMM_FAILED
			}
			frac = adjustFracByTokens(fixV1_3, lptBalance, tokensAdj, frac)
			amountDeposit := getRoundedAsset(fixV1_3, assetBalance1, frac, true)

			if toIOUForCalc(amountDeposit).Compare(toIOUForCalc(amount1)) <= 0 {
				depositAmount1 = amountDeposit
				depositAmount2 = amount2
				lpTokensToIssue = tokensAdj
				if lpTokensDepositMin != nil && toIOUForCalc(lpTokensToIssue).Compare(toIOUForCalc(*lpTokensDepositMin)) < 0 {
					return tx.TecAMM_FAILED
				}
			} else {
				return tx.TecAMM_FAILED
			}
		}

	case flags&tfOneAssetLPToken != 0:
		// Single asset deposit for specific LP tokens (singleDepositTokens)
		// Reference: rippled AMMDeposit.cpp singleDepositTokens()
		isDepositForAsset1 := matchesAsset(a.Amount, a.Asset)
		isDepositForAsset2 := matchesAsset(a.Amount, a.Asset2)

		var assetBalance tx.Amount
		if isDepositForAsset1 {
			assetBalance = assetBalance1
		} else if isDepositForAsset2 {
			assetBalance = assetBalance2
		} else {
			return tx.TecAMM_INVALID_TOKENS
		}

		// adjustLPTokensOut
		tokensAdj := lpTokensRequested
		if fixV1_3 {
			tokensAdj = adjustLPTokens(lptBalance, lpTokensRequested, true)
			if tokensAdj.IsZero() {
				return tx.TecAMM_INVALID_TOKENS
			}
		}

		// the adjusted tokens are factored in
		amountDeposit := ammAssetIn(assetBalance, lptBalance, tokensAdj, tfee, fixV1_3)
		if isGreater(toIOUForCalc(amountDeposit), toIOUForCalc(amount1)) {
			return tx.TecAMM_FAILED
		}

		isSingleAssetDeposit = true
		depositAssetBalance = assetBalance
		if isDepositForAsset1 {
			depositAmount1 = amountDeposit
			depositAmount2 = zeroAmount(a.Asset2)
		} else {
			singleDepositIsAsset2 = true
			depositAmount2 = amountDeposit
			depositAmount1 = zeroAmount(a.Asset)
		}
		lpTokensToIssue = tokensAdj

	case flags&tfLimitLPToken != 0:
		// Single asset deposit with effective price limit (singleDepositEPrice)
		// Reference: rippled AMMDeposit.cpp singleDepositEPrice()
		isDepositForAsset1 := matchesAsset(a.Amount, a.Asset)
		isDepositForAsset2 := matchesAsset(a.Amount, a.Asset2)

		var assetBalance tx.Amount
		if isDepositForAsset1 {
			assetBalance = assetBalance1
		} else if isDepositForAsset2 {
			assetBalance = assetBalance2
		} else {
			return tx.TecAMM_INVALID_TOKENS
		}

		ePrice := *a.EPrice

		// If amount != 0, try direct deposit first
		if !amount1.IsZero() {
			tokens := lpTokensOut(assetBalance, amount1, lptBalance, tfee, fixV1_3)
			if fixV1_3 {
				tokens = adjustLPTokens(lptBalance, tokens, true)
			}
			if tokens.IsZero() || tokens.IsNegative() {
				// rippled returns here for both amendment states — it does NOT
				// fall through to the EPrice calculation when the direct deposit
				// yields no tokens. Reference: rippled AMMDeposit.cpp
				// singleDepositEPrice() lines 916-922.
				if fixV1_3 {
					return tx.TecAMM_INVALID_TOKENS
				}
				return tx.TecAMM_FAILED
			} else {
				// factor in the adjusted tokens
				tokensAdj, amountDepositAdj := adjustAssetInByTokens(fixV1_3, assetBalance, amount1, lptBalance, tokens, tfee)
				if fixV1_3 && tokensAdj.IsZero() {
					return tx.TecAMM_INVALID_TOKENS
				}
				// Check effective price: ep = amountDeposit / tokens
				ep := numberDiv(toIOUForCalc(amountDepositAdj), toIOUForCalc(tokensAdj))
				if ep.Compare(toIOUForCalc(ePrice)) <= 0 {
					lpTokensToIssue = tokensAdj
					isSingleAssetDeposit = true
					depositAssetBalance = assetBalance
					if isDepositForAsset1 {
						depositAmount1 = amountDepositAdj
						depositAmount2 = zeroAmount(a.Asset2)
					} else {
						singleDepositIsAsset2 = true
						depositAmount1 = zeroAmount(a.Asset)
						depositAmount2 = amountDepositAdj
					}
					break
				}
			}
		}

		// EPrice-based calculation
		// Reference: rippled AMMDeposit.cpp singleDepositEPrice() lines 961-1003
		assetBalIOU := toIOUForCalc(assetBalance)
		lptBalIOU := toIOUForCalc(lptBalance)
		ePriceIOU := toIOUForCalc(ePrice)

		f1 := feeMult(tfee)
		f2 := numberDiv(feeMultHalf(tfee), f1)
		// c = f1 * assetBalance / (ePrice * lptBalance)
		c := numberDiv(f1.Mul(assetBalIOU, false), ePriceIOU.Mul(lptBalIOU, false))
		// d = f1 + c * f2 - c
		d, _ := f1.Add(c.Mul(f2, false))
		dVal, _ := d.Sub(c)
		d = dVal
		// a1 = c*c
		a1 := c.Mul(c, false)
		// b1 = c*c*f2*f2 + 2*c - d*d
		ccf2f2 := c.Mul(c, false).Mul(f2, false).Mul(f2, false)
		twoC := numAmount(2).Mul(c, false)
		dd := d.Mul(d, false)
		b1Sum, _ := ccf2f2.Add(twoC)
		b1, _ := b1Sum.Sub(dd)
		// c1 = 2*c*f2*f2 + 1 - 2*d*f2
		twoCf2f2 := numAmount(2).Mul(c, false).Mul(f2, false).Mul(f2, false)
		twoDf2 := numAmount(2).Mul(d, false).Mul(f2, false)
		c1Sum, _ := twoCf2f2.Add(oneAmount())
		c1, _ := c1Sum.Sub(twoDf2)

		amountDeposit := getRoundedAssetCb(fixV1_3,
			func() tx.Amount { return f1.Mul(assetBalIOU, false).Mul(solveQuadraticEq(a1, b1, c1), false) },
			assetBalance,
			func(mode state.RoundingMode) tx.Amount {
				return f1.MulRounded(solveQuadraticEqRounded(a1, b1, c1, mode), false, mode)
			},
			true)
		if amountDeposit.IsZero() || amountDeposit.IsNegative() {
			return tx.TecAMM_FAILED
		}

		tokens := getRoundedLPTokensCb(fixV1_3,
			func() tx.Amount { return numberDiv(toIOUForCalc(amountDeposit), ePriceIOU) },
			lptBalance,
			func(mode state.RoundingMode) tx.Amount {
				return numberDivRounded(toIOUForCalc(amountDeposit), ePriceIOU, mode)
			},
			true)

		// factor in the adjusted tokens
		tokensAdj, amountDepositAdj := adjustAssetInByTokens(fixV1_3, assetBalance, amountDeposit, lptBalance, tokens, tfee)
		if fixV1_3 && tokensAdj.IsZero() {
			return tx.TecAMM_INVALID_TOKENS
		}

		lpTokensToIssue = tokensAdj
		isSingleAssetDeposit = true
		depositAssetBalance = assetBalance
		if isDepositForAsset1 {
			depositAmount1 = amountDepositAdj
			depositAmount2 = zeroAmount(a.Asset2)
		} else {
			singleDepositIsAsset2 = true
			depositAmount1 = zeroAmount(a.Asset)
			depositAmount2 = amountDepositAdj
		}

	case flags&tfTwoAssetIfEmpty != 0:
		// Deposit into empty AMM
		if !lptBalance.IsZero() {
			return tx.TecAMM_NOT_EMPTY
		}
		lpTokensToIssue = calculateLPTokens(amount1, amount2, fixV1_3)
		depositAmount1 = amount1
		depositAmount2 = amount2
		// Set trading fee if provided
		if a.TradingFee > 0 {
			amm.TradingFee = a.TradingFee
		}

	default:
		ctx.Log.Error("amm deposit: invalid options")
		return tx.TemMALFORMED
	}

	// Run adjustAmountsByLPTokens for deposit — matches rippled's deposit() wrapper.
	// rippled's deposit() receives (amountBalance, amountDeposit, amount2Deposit, ...):
	// - For single-asset: amountBalance = deposited asset's pool balance, amount2 = nullopt
	// - For two-asset: amountBalance = asset1 pool balance, amount2 = &asset2Deposit
	// Reference: rippled AMMDeposit.cpp deposit() line 538
	if flags&tfTwoAssetIfEmpty == 0 && !lptBalance.IsZero() {
		if isSingleAssetDeposit {
			// Single-asset deposit: pass (depositedAssetBalance, depositAmount, nil)
			// matching rippled's deposit(view, amm, amountBalance, amountDeposit, nullopt, ...)
			var depositAmt tx.Amount
			if singleDepositIsAsset2 {
				depositAmt = depositAmount2
			} else {
				depositAmt = depositAmount1
			}
			adjAmt, _, adjTokens := adjustAmountsByLPTokens(
				depositAssetBalance, depositAmt, nil, lptBalance, lpTokensToIssue, tfee, true, fixV1_3, fixV1_1)
			lpTokensToIssue = adjTokens
			if singleDepositIsAsset2 {
				depositAmount2 = adjAmt
			} else {
				depositAmount1 = adjAmt
			}
		} else {
			// Two-asset deposit: pass (assetBalance1, depositAmount1, &depositAmount2)
			var amount2Ptr *tx.Amount
			if !depositAmount2.IsZero() {
				amount2Ptr = &depositAmount2
			}
			depositAmount1, amount2Ptr, lpTokensToIssue = adjustAmountsByLPTokens(
				assetBalance1, depositAmount1, amount2Ptr, lptBalance, lpTokensToIssue, tfee, true, fixV1_3, fixV1_1)
			if amount2Ptr != nil {
				depositAmount2 = *amount2Ptr
			}
		}
	}

	if lpTokensToIssue.IsZero() {
		return tx.TecAMM_INVALID_TOKENS
	}

	// tfLPToken deposit minimums: compare the POST-adjustment deposit amounts
	// against the optional Amount/Amount2 minimums. rippled checks these after
	// adjustAmountsByLPTokens (and after the zero-tokens guard above).
	// Reference: rippled AMMDeposit.cpp deposit() lines 553-565
	if depositMin1 != nil && isGreater(toIOUForCalc(*depositMin1), toIOUForCalc(depositAmount1)) {
		return tx.TecAMM_FAILED
	}
	if depositMin2 != nil && isGreater(toIOUForCalc(*depositMin2), toIOUForCalc(depositAmount2)) {
		return tx.TecAMM_FAILED
	}

	// Check LP token deposit minimum: when LPTokenOut is provided with modes that
	// use it as a minimum (tfSingleAsset, tfTwoAsset), verify the computed LP tokens
	// meet the minimum. Already handled for tfLPToken (in equalDepositTokens) and
	// tfOneAssetLPToken/tfLimitLPToken (which derive amount from LPTokenOut).
	// Reference: rippled AMMDeposit.cpp deposit() lines 553-563
	if a.LPTokenOut != nil && (flags&(tfSingleAsset|tfTwoAsset) != 0) {
		if toIOUForCalc(lpTokensToIssue).Compare(toIOUForCalc(*a.LPTokenOut)) < 0 {
			return tx.TecAMM_FAILED
		}
	}

	// Check computed deposit amounts are positive (rippled's checkBalance lambda).
	// In rippled deposit(), checkBalance rejects amounts <= 0 (beast::zero).
	// For the primary deposit amount, checkBalance is always called.
	// For the second deposit amount, checkBalance is only called when amount2Deposit
	// is not nullopt (i.e., in two-asset modes like tfLPToken, tfTwoAsset).
	// In single-asset modes, amount2Deposit is nullopt and checkBalance is skipped.
	// Reference: rippled AMMDeposit.cpp deposit() lines 512-514, 566-572, 590-598
	isXRP1 := isXRPAsset(a.Asset)
	isXRP2 := isXRPAsset(a.Asset2)
	checkBalancePositive := func(amt tx.Amount, isXRP bool) tx.Result {
		if amt.IsNegative() || amt.IsZero() {
			return tx.TemBAD_AMOUNT
		}
		// For XRP, the IOU representation may be non-zero but convert to 0 drops.
		// rippled's checkBalance uses beast::zero comparison after Number → STAmount conversion.
		if isXRP && iouToDrops(amt) <= 0 {
			return tx.TemBAD_AMOUNT
		}
		return tx.TesSUCCESS
	}
	// Match rippled's deposit() checkBalance calling pattern:
	// - For single-asset deposits: only check the deposited asset
	//   (rippled passes amount2Deposit = nullopt for the non-deposited asset)
	// - For two-asset deposits (tfLPToken, tfTwoAsset, tfTwoAssetIfEmpty):
	//   check both amounts
	// Reference: rippled AMMDeposit.cpp deposit() lines 566-598
	if isSingleAssetDeposit {
		if singleDepositIsAsset2 {
			if r := checkBalancePositive(depositAmount2, isXRP2); r != tx.TesSUCCESS {
				return r
			}
		} else {
			if r := checkBalancePositive(depositAmount1, isXRP1); r != tx.TesSUCCESS {
				return r
			}
		}
	} else {
		if r := checkBalancePositive(depositAmount1, isXRP1); r != tx.TesSUCCESS {
			return r
		}
		if r := checkBalancePositive(depositAmount2, isXRP2); r != tx.TesSUCCESS {
			return r
		}
	}

	// Check IOU balances first
	// Reference: rippled preclaim checks accountHolds >= deposit for each IOU
	if !isXRP1 && !depositAmount1.IsZero() {
		// Check depositor has enough of asset1 (IOU)
		// Skip check if depositor is the issuer (they can issue unlimited)
		issuerID1, _ := state.DecodeAccountID(a.Asset.Issuer)
		if accountID != issuerID1 {
			depositorFunds := tx.AccountFunds(ctx.View, accountID, depositAmount1, false, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
			if depositorFunds.Compare(depositAmount1) < 0 {
				return TecUNFUNDED_AMM
			}
		}
	}
	if !isXRP2 && !depositAmount2.IsZero() {
		// Check depositor has enough of asset2 (IOU)
		issuerID2, _ := state.DecodeAccountID(a.Asset2.Issuer)
		if accountID != issuerID2 {
			depositorFunds := tx.AccountFunds(ctx.View, accountID, depositAmount2, false, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
			if depositorFunds.Compare(depositAmount2) < 0 {
				return TecUNFUNDED_AMM
			}
		}
	}

	// Calculate total XRP needed for deposit and verify it against the depositor's
	// LIQUID balance (balance minus reserve), not the raw balance. A deposit that
	// would dip into the account reserve must fail tecUNFUNDED_AMM. The reserve
	// accounts for the LP token trust line when the depositor doesn't already hold
	// one (matching rippled's xrpLiquid(view, account, !sle) at apply time).
	// Reference: rippled AMMDeposit.cpp deposit() checkBalance lines 512-525
	totalXRPNeeded := int64(0)
	if isXRP1 && !depositAmount1.IsZero() {
		totalXRPNeeded += depositAmount1.Drops()
	}
	if isXRP2 && !depositAmount2.IsZero() {
		totalXRPNeeded += depositAmount2.Drops()
	}
	if totalXRPNeeded > 0 {
		ownerCountAdj := uint32(0)
		if !lptExists {
			ownerCountAdj = 1
		}
		reserve := ctx.AccountReserve(ctx.Account.OwnerCount + ownerCountAdj)
		xrpLiquid := int64(ctx.Account.Balance) - int64(reserve)
		if xrpLiquid < totalXRPNeeded {
			return TecUNFUNDED_AMM
		}
	}

	// Transfer assets from depositor to AMM
	if isXRP1 && !depositAmount1.IsZero() {
		drops := uint64(depositAmount1.Drops())
		ctx.Account.Balance -= drops
		ammAccount.Balance += drops
	}
	if isXRP2 && !depositAmount2.IsZero() {
		drops := uint64(depositAmount2.Drops())
		ctx.Account.Balance -= drops
		ammAccount.Balance += drops
	}

	// For IOU transfers, update trust lines for BOTH depositor and AMM
	// Reference: rippled AMMDeposit.cpp - deposit handles token transfer via book::quality path
	if !isXRP1 && !depositAmount1.IsZero() {
		// Get issuer account ID
		issuerID, err := state.DecodeAccountID(a.Asset.Issuer)
		if err != nil {
			return tx.TefINTERNAL
		}
		// Deduct from depositor's trust line (negative delta)
		// Skip if depositor IS the issuer — issuers issue from thin air.
		// Reference: rippled uses accountSend() which handles this internally.
		if accountID != issuerID {
			if err := updateTrustlineBalanceInView(accountID, issuerID, a.Asset.Currency, depositAmount1.Negate(), ctx.View); err != nil {
				// Trust line update failed - may not have sufficient balance
				return TecUNFUNDED_AMM
			}
		}
		// Credit AMM's trust line (positive delta)
		if err := createOrUpdateAMMTrustline(ammAccountID, a.Asset, depositAmount1, ctx.View); err != nil {
			return TecNO_LINE
		}
	}
	if !isXRP2 && !depositAmount2.IsZero() {
		issuerID, err := state.DecodeAccountID(a.Asset2.Issuer)
		if err != nil {
			return tx.TefINTERNAL
		}
		// Skip if depositor IS the issuer
		if accountID != issuerID {
			if err := updateTrustlineBalanceInView(accountID, issuerID, a.Asset2.Currency, depositAmount2.Negate(), ctx.View); err != nil {
				return TecUNFUNDED_AMM
			}
		}
		// Credit AMM's trust line
		if err := createOrUpdateAMMTrustline(ammAccountID, a.Asset2, depositAmount2, ctx.View); err != nil {
			return TecNO_LINE
		}
	}

	// Issue LP tokens to depositor - update AMM LP token balance
	newLPBalance, err := amm.LPTokenBalance.Add(lpTokensToIssue)
	if err != nil {
		return tx.TefINTERNAL
	}
	amm.LPTokenBalance = newLPBalance

	// NOTE: Asset balances are NOT stored in AMM entry
	// They are updated by the balance transfers above:
	// - XRP: via ammAccount.Balance += drops
	// - IOU: via trustline updates (createOrUpdateAMMTrustline)

	// Update LP token trustline for depositor
	lptAsset := tx.Asset{Currency: lptCurrency, Issuer: ammAccountAddr}
	if err := createLPTokenTrustline(accountID, lptAsset, lpTokensToIssue, ctx.View); err != nil {
		return TecINSUF_RESERVE_LINE
	}

	// Increment owner count if we created a new LP token trustline.
	// Write through the view (not just ctx.Account) so that subsequent
	// operations that read the account from the view (e.g., redeemIOUWithCleanup
	// in AMMWithdraw, or updateTrustLine cleanup) see the updated OwnerCount.
	// Reference: rippled — adjustOwnerCount on the SLE which is immediately
	// visible through peek().
	if !lptExists {
		_ = tx.AdjustOwnerCount(ctx.View, accountID, 1)
		ctx.Account.OwnerCount++
	}

	// Initialize fee auction vote if depositing into empty AMM
	// Reference: rippled AMMDeposit.cpp lines 472-474
	if lptBalance.IsZero() {
		initializeFeeAuctionVote(amm, accountID, lptCurrency, ammAccountAddr, tfee, ctx.Config.ParentCloseTime)
	}

	// Persist updated AMM
	ammBytes, err := serializeAMMData(amm)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(ammKey, ammBytes); err != nil {
		return tx.TefINTERNAL
	}

	// Persist updated AMM account
	ammAccountBytes, err := state.SerializeAccountRoot(ammAccount)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(ammAccountKey, ammAccountBytes); err != nil {
		return tx.TefINTERNAL
	}

	// Persist updated depositor account
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

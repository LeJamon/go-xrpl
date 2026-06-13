package amm

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// AMMClawback claws back tokens from an AMM.
type AMMClawback struct {
	tx.BaseTx

	// Holder is the account holding LP tokens (required)
	Holder string `json:"Holder" xrpl:"Holder"`

	// Asset identifies the first asset of the AMM (required)
	Asset tx.Asset `json:"Asset" xrpl:"Asset,asset"`

	// Asset2 identifies the second asset of the AMM (required)
	Asset2 tx.Asset `json:"Asset2" xrpl:"Asset2,asset"`

	// Amount is the amount to claw back (optional)
	Amount *tx.Amount `json:"Amount,omitempty" xrpl:"Amount,omitempty,amount"`
}

// NewAMMClawback creates a new AMMClawback transaction
func NewAMMClawback(account, holder string, asset, asset2 tx.Asset) *AMMClawback {
	return &AMMClawback{
		BaseTx: *tx.NewBaseTx(tx.TypeAMMClawback, account),
		Holder: holder,
		Asset:  asset,
		Asset2: asset2,
	}
}

func (a *AMMClawback) TxType() tx.Type {
	return tx.TypeAMMClawback
}

// GetAMMAsset returns the first asset of the AMM (Asset field).
// Implements ammAssetProvider for the ValidAMM invariant checker.
func (a *AMMClawback) GetAMMAsset() tx.Asset {
	return a.Asset
}

// GetAMMAsset2 returns the second asset of the AMM (Asset2 field).
// Implements ammAssetProvider for the ValidAMM invariant checker.
func (a *AMMClawback) GetAMMAsset2() tx.Asset {
	return a.Asset2
}

// Reference: rippled AMMClawback.cpp preflight
func (a *AMMClawback) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	if a.GetFlags()&tfAMMClawbackMask != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "invalid flags for AMMClawback")
	}

	// Reference: rippled AMMClawback.cpp preflight lines 52-57
	if a.Holder == a.Common.Account {
		return tx.Errorf(tx.TemMALFORMED, "Holder cannot be the same as issuer")
	}

	// Reference: rippled AMMClawback.cpp preflight line 63
	if isXRPAsset(a.Asset) {
		return tx.Errorf(tx.TemMALFORMED, "Asset cannot be XRP")
	}

	// If tfClawTwoAssets is set, both assets must be issued by the same issuer
	// Reference: rippled AMMClawback.cpp preflight lines 66-72
	if a.GetFlags()&tfClawTwoAssets != 0 {
		if a.Asset.Issuer != a.Asset2.Issuer {
			return tx.Errorf(tx.TemINVALID_FLAG, "tfClawTwoAssets requires both assets to have the same issuer")
		}
	}

	// Reference: rippled AMMClawback.cpp preflight lines 74-79
	if a.Asset.Issuer != a.Common.Account {
		return tx.Errorf(tx.TemMALFORMED, "Asset issuer must match Account")
	}

	// Reference: rippled AMMClawback.cpp preflight lines 81-89
	if a.Amount != nil {
		if a.Amount.Currency != a.Asset.Currency || a.Amount.Issuer != a.Asset.Issuer {
			return tx.Errorf(tx.TemBAD_AMOUNT, "Amount issue must match Asset")
		}
		if a.Amount.IsZero() || a.Amount.IsNegative() {
			return tx.Errorf(tx.TemBAD_AMOUNT, "Amount must be positive")
		}
	}

	return nil
}

func (a *AMMClawback) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(a)
}

func (a *AMMClawback) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureAMM, amendment.FeatureFixUniversalNumber, amendment.FeatureAMMClawback}
}

// Preclaim requires the holder and AMM to exist and the issuer to permit
// clawback (lsfAllowTrustLineClawback set, lsfNoFreeze clear).
// Reference: rippled AMMClawback.cpp preclaim
func (a *AMMClawback) Preclaim(view tx.LedgerView, _ tx.EngineConfig) tx.Result {
	issuerData, err := view.Read(keylet.Account(getIssuerBytes(a.Common.Account)))
	if err != nil || issuerData == nil {
		return TerNO_ACCOUNT
	}
	issuer, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		return tx.TefINTERNAL
	}

	holderID, err := state.DecodeAccountID(a.Holder)
	if err != nil {
		return tx.TemINVALID
	}
	if exists, _ := view.Exists(keylet.Account(holderID)); !exists {
		return TerNO_ACCOUNT
	}

	if _, _, result := readAMM(view, a.Asset, a.Asset2); result != tx.TesSUCCESS {
		return result
	}

	if (issuer.Flags&state.LsfAllowTrustLineClawback) == 0 ||
		(issuer.Flags&state.LsfNoFreeze) != 0 {
		return tx.TecNO_PERMISSION
	}

	return tx.TesSUCCESS
}

// Rippled flow: AMMClawback delegates to AMMWithdraw infrastructure which
// performs accountSend (AMM -> holder) + redeemIOU (LP tokens), then
// rippleCredit (holder -> issuer) for the clawback. The net effect for
// clawed-back assets: AMM pool decreases, holder unchanged, issuer absorbs.
// For non-clawed asset2: AMM pool decreases, holder gains.
//
// Reference: rippled AMMClawback.cpp preclaim + applyGuts
func (a *AMMClawback) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("amm clawback apply",
		"account", a.Account,
		"holder", a.Holder,
		"asset", a.Asset,
		"amount", a.Amount,
	)

	issuerID := ctx.AccountID

	holderID, err := state.DecodeAccountID(a.Holder)
	if err != nil {
		return tx.TemINVALID
	}
	holderKey := keylet.Account(holderID)
	holderData, err := ctx.View.Read(holderKey)
	if err != nil || holderData == nil {
		return TerNO_ACCOUNT
	}
	holderAccount, err := state.ParseAccountRoot(holderData)
	if err != nil {
		return tx.TefINTERNAL
	}

	loaded, result := loadAMM(ctx.View, a.Asset, a.Asset2, a.Asset)
	if result != tx.TesSUCCESS {
		return result
	}
	amm := loaded.Data
	ammKey := loaded.Key
	ammAccountID := loaded.AccountID
	ammAccountKey := loaded.AccountKey
	ammAccount := loaded.Account
	assetBalance1, assetBalance2 := loaded.AssetBalance1, loaded.AssetBalance2

	// fixAMMClawbackRounding: retrieve LP token balance and adjust if needed.
	// verifyAndAdjustLPTokenBalance may mutate amm.LPTokenBalance, so read
	// lptAMMBalance from it afterwards.
	// Reference: rippled AMMClawback.cpp applyGuts lines 154-166
	if ctx.Rules().Enabled(amendment.FeatureFixAMMClawbackRounding) {
		lpTokenBalance := ammLPHolds(ctx.View, amm, holderID)
		if lpTokenBalance.IsZero() {
			return tx.TecAMM_BALANCE
		}
		if result := verifyAndAdjustLPTokenBalance(ctx.View, lpTokenBalance, amm, holderID); result != tx.TesSUCCESS {
			return result
		}
	}

	lptAMMBalance := amm.LPTokenBalance
	if lptAMMBalance.IsZero() {
		return tx.TecAMM_BALANCE
	}

	// Get holder's LP token balance from trustline
	// Reference: rippled AMMClawback.cpp applyGuts line 185
	holdLPTokens := ammLPHolds(ctx.View, amm, holderID)
	if holdLPTokens.IsZero() {
		return tx.TecAMM_BALANCE
	}

	flags := a.GetFlags()
	fixV1_3 := ctx.Rules().Enabled(amendment.FeatureFixAMMv1_3)

	var lpTokensToWithdraw tx.Amount
	var withdrawAmount1, withdrawAmount2 tx.Amount

	if a.Amount == nil {
		// No amount specified - withdraw all LP tokens the holder has.
		// Reference: rippled calls equalWithdrawTokens with WithdrawAll::Yes
		lpTokensToWithdraw = holdLPTokens

		if toIOUForCalc(holdLPTokens).Compare(toIOUForCalc(lptAMMBalance)) == 0 {
			// Holder has ALL LP tokens — withdraw everything
			withdrawAmount1 = assetBalance1
			withdrawAmount2 = assetBalance2
		} else {
			// Proportional withdrawal
			frac := numberDiv(toIOUForCalc(holdLPTokens), toIOUForCalc(lptAMMBalance))
			withdrawAmount1 = getRoundedAsset(fixV1_3, assetBalance1, frac, false)
			withdrawAmount2 = getRoundedAsset(fixV1_3, assetBalance2, frac, false)
		}
	} else {
		// Amount specified - calculate proportional withdrawal.
		// Reference: rippled AMMClawback.cpp equalWithdrawMatchingOneAmount
		clawAmount := *a.Amount

		if assetBalance1.IsZero() {
			return tx.TecAMM_BALANCE
		}
		frac := numberDiv(toIOUForCalc(clawAmount), toIOUForCalc(assetBalance1))

		// Calculate LP tokens needed
		lpTokensNeeded := lptAMMBalance.Mul(frac, false)

		if isGreater(lpTokensNeeded, holdLPTokens) {
			// Holder doesn't have enough LP tokens — clawback all they have.
			lpTokensToWithdraw = holdLPTokens
			if toIOUForCalc(holdLPTokens).Compare(toIOUForCalc(lptAMMBalance)) == 0 {
				withdrawAmount1 = assetBalance1
				withdrawAmount2 = assetBalance2
			} else {
				fallbackFrac := numberDiv(toIOUForCalc(holdLPTokens), toIOUForCalc(lptAMMBalance))
				withdrawAmount1 = getRoundedAsset(fixV1_3, assetBalance1, fallbackFrac, false)
				withdrawAmount2 = getRoundedAsset(fixV1_3, assetBalance2, fallbackFrac, false)
			}
		} else {
			// fixAMMClawbackRounding: use rounded tokens and adjusted fractions
			if ctx.Rules().Enabled(amendment.FeatureFixAMMClawbackRounding) {
				tokensAdj := getRoundedLPTokens(fixV1_3, lptAMMBalance, frac, false)
				if tokensAdj.IsZero() {
					return tx.TecAMM_INVALID_TOKENS
				}
				frac = adjustFracByTokens(fixV1_3, lptAMMBalance, tokensAdj, frac)
				amountRounded := getRoundedAsset(fixV1_3, assetBalance1, frac, false)
				amount2Rounded := getRoundedAsset(fixV1_3, assetBalance2, frac, false)
				lpTokensToWithdraw = tokensAdj
				withdrawAmount1 = amountRounded
				withdrawAmount2 = amount2Rounded
			} else {
				amount2Withdraw := assetBalance2.Mul(frac, false)
				lpTokensToWithdraw = lpTokensNeeded
				withdrawAmount1 = clawAmount
				withdrawAmount2 = amount2Withdraw
			}
		}
	}

	// Verify withdrawal amounts against the pool balances exactly as rippled's
	// AMMWithdraw::withdraw does — fail tecAMM_BALANCE rather than silently
	// clamping. The clawback math inherits these guards because rippled routes
	// the clawback through equalWithdrawTokens / withdraw.
	// Reference: rippled AMMWithdraw.cpp withdraw() lines 539-579
	w1EqualsB1 := toIOUForCalc(withdrawAmount1).Compare(toIOUForCalc(assetBalance1)) == 0
	w2EqualsB2 := toIOUForCalc(withdrawAmount2).Compare(toIOUForCalc(assetBalance2)) == 0
	// Cannot withdraw one side of the pool while leaving the other.
	if (w1EqualsB1 && !w2EqualsB2) || (w2EqualsB2 && !w1EqualsB1) {
		return tx.TecAMM_BALANCE
	}
	// Withdrawing all LP tokens must drain both sides exactly.
	if toIOUForCalc(lpTokensToWithdraw).Compare(toIOUForCalc(lptAMMBalance)) == 0 &&
		(!w1EqualsB1 || !w2EqualsB2) {
		return tx.TecAMM_BALANCE
	}
	// Cannot withdraw more than the pool holds.
	if isGreater(toIOUForCalc(withdrawAmount1), toIOUForCalc(assetBalance1)) ||
		isGreater(toIOUForCalc(withdrawAmount2), toIOUForCalc(assetBalance2)) {
		return tx.TecAMM_BALANCE
	}

	// Rippled flow per asset:
	//   1. accountSend(ammAccount, holder, amount): AMM->holder
	//      Internally: redeemIOU(ammAccount, amount) + rippleCredit(issuer, holder, amount)
	//   2. rippleCredit(holder, issuer, amount): holder->issuer (clawback)
	//      The net effect on holder's trustline: +amount - amount = 0
	//      The net effect on AMM's trustline: -amount
	//      The issuer absorbs (tokens destroyed).
	//
	// For non-clawed asset2:
	//   Only step 1: AMM->holder. AMM trustline decreases, holder trustline increases.
	isXRP1 := isXRPAsset(a.Asset)
	isXRP2 := isXRPAsset(a.Asset2)

	// Asset1 is ALWAYS clawed back (sent from AMM to issuer).
	// Net effect: debit AMM's trust line, tokens returned to issuer (destroyed).
	// Asset1 cannot be XRP (enforced in preflight).
	if !isXRP1 && !withdrawAmount1.IsZero() {
		if err := createOrUpdateAMMTrustline(ammAccountID, a.Asset, withdrawAmount1.Negate(), ctx.View); err != nil {
			return tx.TefINTERNAL
		}
	}

	// Asset2: depends on tfClawTwoAssets flag
	if flags&tfClawTwoAssets != 0 {
		// Clawback asset2 too. Same net effect as asset1.
		if !isXRP2 && !withdrawAmount2.IsZero() {
			if err := createOrUpdateAMMTrustline(ammAccountID, a.Asset2, withdrawAmount2.Negate(), ctx.View); err != nil {
				return tx.TefINTERNAL
			}
		} else if isXRP2 && !withdrawAmount2.IsZero() {
			// XRP clawback: AMM loses XRP, issuer gains
			drops := uint64(iouToDrops(withdrawAmount2))
			ammAccount.Balance -= drops
			ctx.Account.Balance += drops
		}
	} else {
		// NOT clawing asset2 — holder receives it.
		// Transfer from AMM to holder.
		if !isXRP2 && !withdrawAmount2.IsZero() {
			if err := createOrUpdateAMMTrustline(ammAccountID, a.Asset2, withdrawAmount2.Negate(), ctx.View); err != nil {
				return tx.TefINTERNAL
			}
			// Credit holder's trust line with asset2 issuer — BUT skip if
			// holder IS the issuer (the IOU is just returned/destroyed).
			// Reference: rippled rippleSendIOU line 1807: direct path when
			// sender or receiver is the issuer.
			issuer2ID, _ := state.DecodeAccountID(a.Asset2.Issuer)
			if holderID != issuer2ID {
				if err := updateTrustlineBalanceInView(holderID, issuer2ID, a.Asset2.Currency, withdrawAmount2, ctx.View); err != nil {
					return tx.TefINTERNAL
				}
			}
		} else if isXRP2 && !withdrawAmount2.IsZero() {
			// XRP: AMM sends to holder
			drops := uint64(iouToDrops(withdrawAmount2))
			ammAccount.Balance -= drops
			holderAccount.Balance += drops
		}
	}

	// Burn LP tokens: debit the holder's LP token trust line, may delete it.
	// Reference: rippled AMMWithdraw::withdraw calls redeemIOU(holder, lpTokens, lpIssue)
	if !lpTokensToWithdraw.IsZero() {
		lptCurrency := GenerateAMMLPTCurrency(amm.Asset.Currency, amm.Asset2.Currency)
		ammAccountAddr, _ := state.EncodeAccountID(amm.Account)
		redeemAmt := state.NewIssuedAmountFromValue(
			lpTokensToWithdraw.Mantissa(), lpTokensToWithdraw.Exponent(), lptCurrency, ammAccountAddr)
		if r := redeemIOUWithCleanup(ctx.View, holderID, amm.Account, redeemAmt); r != tx.TesSUCCESS {
			return r
		}
	}

	newLPBalance, err := lptAMMBalance.Sub(lpTokensToWithdraw)
	if err != nil {
		return tx.TefINTERNAL
	}

	deleteResult := deleteAMMAccountIfEmpty(ctx.View, ammKey, ammAccountKey,
		newLPBalance, a.Asset, a.Asset2, amm, ammAccount)
	if deleteResult != tx.TesSUCCESS && deleteResult != tx.TecINCOMPLETE {
		return deleteResult
	}

	// Persist updated AMM account XRP balance if AMM still exists
	if !newLPBalance.IsZero() || deleteResult == tx.TecINCOMPLETE {
		ammAccountBytes, err := state.SerializeAccountRoot(ammAccount)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(ammAccountKey, ammAccountBytes); err != nil {
			return tx.TefINTERNAL
		}
	}

	accountKey := keylet.Account(issuerID)
	accountBytes, err := state.SerializeAccountRoot(ctx.Account)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(accountKey, accountBytes); err != nil {
		return tx.TefINTERNAL
	}

	// Re-read holder account from view — redeemIOUWithCleanup may have
	// decremented OwnerCount when deleting the LP token trust line.
	// We must merge our local changes (XRP balance) with whatever
	// redeemIOUWithCleanup wrote.
	holderData2, err := ctx.View.Read(holderKey)
	if err != nil || holderData2 == nil {
		return tx.TefINTERNAL
	}
	holderAccount2, err := state.ParseAccountRoot(holderData2)
	if err != nil {
		return tx.TefINTERNAL
	}
	// Apply any XRP balance change from our local holderAccount to the
	// version that redeemIOUWithCleanup persisted.
	holderAccount2.Balance = holderAccount.Balance
	holderBytes, err := state.SerializeAccountRoot(holderAccount2)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(holderKey, holderBytes); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

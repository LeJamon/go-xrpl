package amm

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

func init() {
	tx.Register(tx.TypeAMMClawback, func() tx.Transaction {
		return &AMMClawback{BaseTx: *tx.NewBaseTx(tx.TypeAMMClawback, "")}
	})
}

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

// TxType returns the transaction type
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

// Validate validates the AMMClawback transaction
// Reference: rippled AMMClawback.cpp preflight
func (a *AMMClawback) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	// Check flags
	if a.GetFlags()&tfAMMClawbackMask != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "invalid flags for AMMClawback")
	}

	// Holder is required
	if a.Holder == "" {
		return tx.Errorf(tx.TemMALFORMED, "Holder is required")
	}

	// Holder cannot be the same as issuer (Account)
	if a.Holder == a.Common.Account {
		return tx.Errorf(tx.TemMALFORMED, "Holder cannot be the same as issuer")
	}

	// Validate asset pair
	if a.Asset.Currency == "" {
		return tx.Errorf(tx.TemMALFORMED, "Asset is required")
	}

	if a.Asset2.Currency == "" {
		return tx.Errorf(tx.TemMALFORMED, "Asset2 is required")
	}

	// Asset cannot be XRP (must be issued currency)
	if a.Asset.Currency == "XRP" || a.Asset.Currency == "" && a.Asset.Issuer == "" {
		return tx.Errorf(tx.TemMALFORMED, "Asset cannot be XRP")
	}

	// Asset issuer must match the transaction account (issuer)
	if a.Asset.Issuer != a.Common.Account {
		return tx.Errorf(tx.TemMALFORMED, "Asset issuer must match Account")
	}

	// If tfClawTwoAssets is set, both assets must be issued by the same issuer
	if a.GetFlags()&tfClawTwoAssets != 0 {
		if a.Asset.Issuer != a.Asset2.Issuer {
			return tx.Errorf(tx.TemINVALID_FLAG, "tfClawTwoAssets requires both assets to have the same issuer")
		}
	}

	// Validate Amount if provided
	if a.Amount != nil {
		// Amount must be positive
		if err := validateAMMAmount(*a.Amount); err != nil {
			return tx.Errorf(tx.TemBAD_AMOUNT, "invalid Amount - %s", err.Error())
		}
		// Amount's issue must match tx.Asset
		if a.Amount.Currency != a.Asset.Currency || a.Amount.Issuer != a.Asset.Issuer {
			return tx.Errorf(tx.TemBAD_AMOUNT, "Amount issue must match tx.Asset")
		}
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (a *AMMClawback) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(a)
}

// RequiredAmendments returns the amendments required for this transaction type
func (a *AMMClawback) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureAMM, amendment.FeatureFixUniversalNumber, amendment.FeatureAMMClawback}
}

// Apply applies the AMMClawback transaction to ledger state.
//
// Rippled flow: AMMClawback delegates to AMMWithdraw infrastructure which
// performs accountSend (AMM -> holder) + redeemIOU (LP tokens), then
// rippleCredit (holder -> issuer) for the clawback. The net effect for
// clawed-back assets: AMM pool decreases, holder unchanged, issuer absorbs.
// For non-clawed asset2: AMM pool decreases, holder gains.
//
// Reference: rippled AMMClawback.cpp preclaim + applyGuts
func (a *AMMClawback) Apply(ctx *tx.ApplyContext) tx.Result {
	issuerID := ctx.AccountID

	// Find the holder
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

	// Find the AMM
	ammKey := computeAMMKeylet(a.Asset, a.Asset2)
	ammRawData, err := ctx.View.Read(ammKey)
	if err != nil || ammRawData == nil {
		return TerNO_AMM
	}

	// Verify issuer has lsfAllowTrustLineClawback and NOT lsfNoFreeze
	if (ctx.Account.Flags & state.LsfAllowTrustLineClawback) == 0 {
		return tx.TecNO_PERMISSION
	}
	if (ctx.Account.Flags & state.LsfNoFreeze) != 0 {
		return tx.TecNO_PERMISSION
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

	// fixAMMClawbackRounding: retrieve LP token balance and adjust if needed
	// Reference: rippled AMMClawback.cpp applyGuts lines 154-166
	if ctx.Rules().Enabled(amendment.FeatureFixAMMClawbackRounding) {
		lpTokenBalance := ammLPHolds(ctx.View, amm, holderID)
		if lpTokenBalance.IsZero() {
			return tx.TecAMM_BALANCE
		}
		if result := verifyAndAdjustLPTokenBalance(lpTokenBalance, amm); result != tx.TesSUCCESS {
			return result
		}
	}

	// Get current AMM balances from actual state (not stored in AMM entry)
	// Reference: rippled ammHolds with issue hints — reorders to match tx asset ordering
	assetBalance1, assetBalance2, lptAMMBalance := AMMHolds(ctx.View, amm, false)

	// Reorder balances to match the transaction's asset ordering (a.Asset / a.Asset2).
	if !matchesAssetByIssue(amm.Asset, a.Asset) {
		assetBalance1, assetBalance2 = assetBalance2, assetBalance1
	}

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

	// Verify withdrawal amounts don't exceed balances
	if isGreater(toIOUForCalc(withdrawAmount1), toIOUForCalc(assetBalance1)) {
		withdrawAmount1 = assetBalance1
	}
	if isGreater(toIOUForCalc(withdrawAmount2), toIOUForCalc(assetBalance2)) {
		withdrawAmount2 = assetBalance2
	}

	// =========================================================================
	// ASSET TRANSFERS
	//
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
	// =========================================================================
	isXRP1 := a.Asset.Currency == "" || a.Asset.Currency == "XRP"
	isXRP2 := a.Asset2.Currency == "" || a.Asset2.Currency == "XRP"

	// Asset1 is ALWAYS clawed back (sent from AMM to issuer).
	// Net effect: debit AMM's trust line, tokens returned to issuer (destroyed).
	// Asset1 cannot be XRP (enforced in preflight).
	if !isXRP1 && !withdrawAmount1.IsZero() {
		// Debit AMM's trust line with issuer
		if err := createOrUpdateAMMTrustline(ammAccountID, a.Asset, withdrawAmount1.Negate(), ctx.View); err != nil {
			return tx.TefINTERNAL
		}
	}

	// Asset2: depends on tfClawTwoAssets flag
	if flags&tfClawTwoAssets != 0 {
		// Clawback asset2 too. Same net effect as asset1.
		if !isXRP2 && !withdrawAmount2.IsZero() {
			// Debit AMM's trust line with asset2 issuer
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
			// Debit AMM's trust line
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

	// =========================================================================
	// BURN LP TOKENS
	// Reference: rippled AMMWithdraw::withdraw calls redeemIOU(holder, lpTokens, lpIssue)
	// This debits the holder's LP token trust line and may delete it.
	// =========================================================================
	if !lpTokensToWithdraw.IsZero() {
		lptCurrency := GenerateAMMLPTCurrency(amm.Asset.Currency, amm.Asset2.Currency)
		ammAccountAddr, _ := state.EncodeAccountID(amm.Account)
		redeemAmt := state.NewIssuedAmountFromValue(
			lpTokensToWithdraw.Mantissa(), lpTokensToWithdraw.Exponent(), lptCurrency, ammAccountAddr)
		if r := redeemIOUWithCleanup(ctx.View, holderID, amm.Account, redeemAmt); r != tx.TesSUCCESS {
			return r
		}
	}

	// =========================================================================
	// UPDATE AMM ENTRY / DELETE IF EMPTY
	// =========================================================================
	newLPBalance, _ := lptAMMBalance.Sub(lpTokensToWithdraw)

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

	// Persist updated issuer account
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

// redeemIOUWithCleanup burns LP tokens from holder's trust line, potentially
// deleting the trust line if balance reaches zero (matching rippled's redeemIOU
// + updateTrustLine + trustDelete flow).
// Reference: rippled View.cpp redeemIOU (line 2288)
func redeemIOUWithCleanup(view tx.LedgerView, holderID, ammAccountID [20]byte, amount tx.Amount) tx.Result {
	if amount.IsZero() {
		return tx.TesSUCCESS
	}

	trustLineKey := keylet.Line(holderID, ammAccountID, amount.Currency)
	data, err := view.Read(trustLineKey)
	if err != nil || data == nil {
		return tx.TefINTERNAL // LP token trust line must exist
	}

	rs, err := state.ParseRippleState(data)
	if err != nil {
		return tx.TefINTERNAL
	}

	holderHigh := !keylet.IsLowAccount(holderID, ammAccountID)

	// Get balance in holder terms (positive = holder holds tokens)
	saBalance := rs.Balance
	if holderHigh {
		saBalance = saBalance.Negate()
	}

	saBefore := saBalance
	// Holder is redeeming (sending back to AMM/issuer), so balance decreases
	saBalance, _ = saBalance.Sub(amount)

	// Check trust line cleanup conditions
	// Reference: rippled View.cpp updateTrustLine (line 2135) + redeemIOU (line 2323)
	bDelete := false
	rsFlags := rs.Flags

	var holderReserveFlag, holderNoRippleFlag, holderFreezeFlag uint32
	var holderLimitIsZero, holderQInIsZero, holderQOutIsZero bool
	if !holderHigh {
		holderReserveFlag = state.LsfLowReserve
		holderNoRippleFlag = state.LsfLowNoRipple
		holderFreezeFlag = state.LsfLowFreeze
		holderLimitIsZero = rs.LowLimit.IsZero()
		holderQInIsZero = rs.LowQualityIn == 0
		holderQOutIsZero = rs.LowQualityOut == 0
	} else {
		holderReserveFlag = state.LsfHighReserve
		holderNoRippleFlag = state.LsfHighNoRipple
		holderFreezeFlag = state.LsfHighFreeze
		holderLimitIsZero = rs.HighLimit.IsZero()
		holderQInIsZero = rs.HighQualityIn == 0
		holderQOutIsZero = rs.HighQualityOut == 0
	}

	isPositive := !saBefore.IsZero() && !saBefore.IsNegative()
	isZeroOrNeg := saBalance.IsZero() || saBalance.IsNegative()
	hasReserve := (rsFlags & holderReserveFlag) != 0

	holderAccountData, _ := view.Read(keylet.Account(holderID))
	holderAccount, _ := state.ParseAccountRoot(holderAccountData)

	holderHasDefaultRipple := false
	if holderAccount != nil {
		holderHasDefaultRipple = (holderAccount.Flags & state.LsfDefaultRipple) != 0
	}
	holderHasNoRipple := (rsFlags & holderNoRippleFlag) != 0
	holderHasFreeze := (rsFlags & holderFreezeFlag) != 0

	if isPositive && isZeroOrNeg && hasReserve &&
		(holderHasNoRipple != holderHasDefaultRipple) &&
		!holderHasFreeze &&
		holderLimitIsZero &&
		holderQInIsZero &&
		holderQOutIsZero {

		// Decrement holder's owner count
		if holderAccount != nil && holderAccount.OwnerCount > 0 {
			holderAccount.OwnerCount--
			holderBytes, err := state.SerializeAccountRoot(holderAccount)
			if err != nil {
				return tx.TefINTERNAL
			}
			if err := view.Update(keylet.Account(holderID), holderBytes); err != nil {
				return tx.TefINTERNAL
			}
		}

		// Clear holder's reserve flag
		rsFlags &= ^holderReserveFlag

		// Check if line should be deleted
		var ammReserveFlag uint32
		if holderHigh {
			ammReserveFlag = state.LsfLowReserve
		} else {
			ammReserveFlag = state.LsfHighReserve
		}

		bDelete = saBalance.IsZero() && (rsFlags&ammReserveFlag) == 0
	}

	// Update balance
	finalBalance := saBalance
	if holderHigh {
		finalBalance = finalBalance.Negate()
	}
	rs.Balance = state.NewIssuedAmountFromValue(
		finalBalance.Mantissa(), finalBalance.Exponent(),
		rs.Balance.Currency, rs.Balance.Issuer)
	rs.Flags = rsFlags

	if bDelete {
		return trustDeleteRippleState(view, trustLineKey, rs, holderID, ammAccountID, holderHigh)
	}

	rsBytes, err := state.SerializeRippleState(rs)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := view.Update(trustLineKey, rsBytes); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// trustDeleteRippleState removes a trust line from both owner directories and erases it.
// Reference: rippled View.cpp trustDelete (line 1534)
func trustDeleteRippleState(view tx.LedgerView, lineKey keylet.Keylet, rs *state.RippleState, id1, id2 [20]byte, id1IsHigh bool) tx.Result {
	var lowID, highID [20]byte
	if id1IsHigh {
		lowID = id2
		highID = id1
	} else {
		lowID = id1
		highID = id2
	}

	// Remove from low account's owner directory
	lowDirKey := keylet.OwnerDir(lowID)
	_, err := state.DirRemove(view, lowDirKey, rs.LowNode, lineKey.Key, false)
	if err != nil {
		return tx.TefBAD_LEDGER
	}

	// Remove from high account's owner directory
	highDirKey := keylet.OwnerDir(highID)
	_, err = state.DirRemove(view, highDirKey, rs.HighNode, lineKey.Key, false)
	if err != nil {
		return tx.TefBAD_LEDGER
	}

	// Erase the trust line
	if err := view.Erase(lineKey); err != nil {
		return tx.TefBAD_LEDGER
	}

	return tx.TesSUCCESS
}

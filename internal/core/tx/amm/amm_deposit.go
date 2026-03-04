package amm

import (
	"errors"

	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeAMMDeposit, func() tx.Transaction {
		return &AMMDeposit{BaseTx: *tx.NewBaseTx(tx.TypeAMMDeposit, "")}
	})
}

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

// TxType returns the transaction type
func (a *AMMDeposit) TxType() tx.Type {
	return tx.TypeAMMDeposit
}

// Validate validates the AMMDeposit transaction
// Reference: rippled AMMDeposit.cpp preflight lines 32-162
func (a *AMMDeposit) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	flags := a.GetFlags()

	// Check for invalid flags
	// Reference: rippled AMMDeposit.cpp line 42-46
	if flags&tfAMMDepositMask != 0 {
		return errors.New("temINVALID_FLAG: invalid flags for AMMDeposit")
	}

	// Must have exactly one deposit mode flag set
	// Reference: rippled AMMDeposit.cpp line 60-64 - std::popcount(flags & tfDepositSubTx) != 1
	depositModeFlags := flags & tfDepositSubTx
	flagCount := 0
	for f := depositModeFlags; f != 0; f &= f - 1 {
		flagCount++
	}
	if flagCount != 1 {
		return errors.New("temMALFORMED: must specify exactly one deposit mode flag")
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
			return errors.New("temMALFORMED: tfLPToken requires LPTokenOut, optional Amount+Amount2 pair")
		}
	} else if flags&tfSingleAsset != 0 {
		// tfSingleAsset: Amount required, no Amount2, no EPrice, no TradingFee
		if !hasAmount || hasAmount2 || hasEPrice || hasTradingFee {
			return errors.New("temMALFORMED: tfSingleAsset requires Amount only")
		}
	} else if flags&tfTwoAsset != 0 {
		// tfTwoAsset: Amount and Amount2 required, no EPrice, no TradingFee
		if !hasAmount || !hasAmount2 || hasEPrice || hasTradingFee {
			return errors.New("temMALFORMED: tfTwoAsset requires Amount and Amount2")
		}
	} else if flags&tfOneAssetLPToken != 0 {
		// tfOneAssetLPToken: Amount and LPTokenOut required, no Amount2, no EPrice, no TradingFee
		if !hasAmount || !hasLPTokens || hasAmount2 || hasEPrice || hasTradingFee {
			return errors.New("temMALFORMED: tfOneAssetLPToken requires Amount and LPTokenOut")
		}
	} else if flags&tfLimitLPToken != 0 {
		// tfLimitLPToken: Amount and EPrice required, no LPTokens, no Amount2, no TradingFee
		if !hasAmount || !hasEPrice || hasLPTokens || hasAmount2 || hasTradingFee {
			return errors.New("temMALFORMED: tfLimitLPToken requires Amount and EPrice")
		}
	} else if flags&tfTwoAssetIfEmpty != 0 {
		// tfTwoAssetIfEmpty: Amount and Amount2 required, no EPrice, no LPTokens
		if !hasAmount || !hasAmount2 || hasEPrice || hasLPTokens {
			return errors.New("temMALFORMED: tfTwoAssetIfEmpty requires Amount and Amount2")
		}
	}

	// Validate asset pair
	// Reference: rippled AMMDeposit.cpp lines 100-106
	if a.Asset.Currency == "" && a.Asset.Issuer == "" {
		// XRP asset - OK
	} else if a.Asset.Currency == "" {
		return errors.New("temMALFORMED: Asset is invalid")
	}
	if a.Asset2.Currency == "" && a.Asset2.Issuer == "" {
		// XRP asset - OK
	} else if a.Asset2.Currency == "" {
		return errors.New("temMALFORMED: Asset2 is invalid")
	}

	// Amount and Amount2 cannot have the same issue
	// Reference: rippled AMMDeposit.cpp lines 108-113
	if hasAmount && hasAmount2 {
		if a.Amount.Currency == a.Amount2.Currency && a.Amount.Issuer == a.Amount2.Issuer {
			return errors.New("temBAD_AMM_TOKENS: Amount and Amount2 have same issue")
		}
	}

	// Validate LPTokenOut if provided
	// Reference: rippled AMMDeposit.cpp lines 115-119
	if hasLPTokens {
		if a.LPTokenOut.IsZero() || a.LPTokenOut.IsNegative() {
			return errors.New("temBAD_AMM_TOKENS: invalid LPTokens")
		}
	}

	// Validate Amount if provided
	// Reference: rippled AMMDeposit.cpp lines 121-131
	// validZero is true only when EPrice is present
	if hasAmount {
		if errCode := validateAMMAmountWithPair(*a.Amount, &a.Asset, &a.Asset2, hasEPrice); errCode != "" {
			if errCode == "temBAD_AMM_TOKENS" {
				return errors.New("temBAD_AMM_TOKENS: invalid Amount")
			}
			return errors.New("temBAD_AMOUNT: invalid Amount")
		}
	}

	// Validate Amount2 if provided
	// Reference: rippled AMMDeposit.cpp lines 133-141
	if hasAmount2 {
		if errCode := validateAMMAmountWithPair(*a.Amount2, &a.Asset, &a.Asset2, false); errCode != "" {
			if errCode == "temBAD_AMM_TOKENS" {
				return errors.New("temBAD_AMM_TOKENS: invalid Amount2")
			}
			return errors.New("temBAD_AMOUNT: invalid Amount2")
		}
	}

	// Validate EPrice if provided - must match Amount's issue
	// Reference: rippled AMMDeposit.cpp lines 144-154
	if hasAmount && hasEPrice {
		amtIssue := tx.Asset{Currency: a.Amount.Currency, Issuer: a.Amount.Issuer}
		if errCode := validateAMMAmountWithPair(*a.EPrice, &amtIssue, &amtIssue, false); errCode != "" {
			return errors.New("temBAD_AMOUNT: invalid EPrice")
		}
	}

	// Validate TradingFee if provided
	// Reference: rippled AMMDeposit.cpp lines 156-160
	if a.TradingFee > TRADING_FEE_THRESHOLD {
		return errors.New("temBAD_FEE: TradingFee must be 0-1000")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (a *AMMDeposit) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(a)
}

// RequiredAmendments returns the amendments required for this transaction type
func (a *AMMDeposit) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureAMM, amendment.FeatureFixUniversalNumber}
}

// Apply applies the AMMDeposit transaction to ledger state.
// Reference: rippled AMMDeposit.cpp preclaim + applyGuts
func (a *AMMDeposit) Apply(ctx *tx.ApplyContext) tx.Result {
	accountID := ctx.AccountID

	// =========================================================================
	// PRECLAIM CHECKS - Reference: rippled AMMDeposit.cpp preclaim lines 165-365
	// =========================================================================

	// Find the AMM
	// Reference: rippled AMMDeposit.cpp lines 170-176
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

	// Get AMM account
	ammAccountID := computeAMMAccountID(ammKey.Key)
	ammAccountKey := keylet.Account(ammAccountID)
	ammAccountData, err := ctx.View.Read(ammAccountKey)
	if err != nil {
		return tx.TefINTERNAL
	}
	ammAccount, err := sle.ParseAccountRoot(ammAccountData)
	if err != nil {
		return tx.TefINTERNAL
	}

	flags := a.GetFlags()

	// Get current AMM balances from actual state (not stored in AMM entry)
	// Reference: rippled ammHolds - reads from AccountRoot (XRP) and trustlines (IOU)
	assetBalance1, assetBalance2, lptBalance := AMMHolds(ctx.View, amm, false)

	// Check AMM state based on deposit mode
	// Reference: rippled AMMDeposit.cpp lines 188-213
	if flags&tfTwoAssetIfEmpty != 0 {
		// For tfTwoAssetIfEmpty, AMM must be empty
		if !lptBalance.IsZero() {
			return tx.TecAMM_NOT_EMPTY
		}
		if !assetBalance1.IsZero() || !assetBalance2.IsZero() {
			return tx.TecINTERNAL
		}
	} else {
		// For all other modes, AMM must NOT be empty
		// Reference: rippled AMMDeposit.cpp lines 202-203
		if lptBalance.IsZero() {
			return tx.TecAMM_BALANCE // tecAMM_EMPTY
		}
		if assetBalance1.IsZero() || assetBalance1.IsNegative() ||
			assetBalance2.IsZero() || assetBalance2.IsNegative() ||
			lptBalance.IsNegative() {
			return tx.TecINTERNAL
		}
	}

	// Check authorization and freeze status for assets
	// Reference: rippled AMMDeposit.cpp lines 244-273 (featureAMMClawback enabled)
	if result := requireAuth(ctx.View, a.Asset, accountID); result != tx.TesSUCCESS {
		return result
	}
	if result := requireAuth(ctx.View, a.Asset2, accountID); result != tx.TesSUCCESS {
		return result
	}

	// Check if assets are frozen
	if isFrozen(ctx.View, accountID, a.Asset) || isFrozen(ctx.View, accountID, a.Asset2) {
		return tx.TecFROZEN
	}

	// Check amounts if provided (authorization and freeze)
	// Reference: rippled AMMDeposit.cpp lines 279-341
	if a.Amount != nil && !(flags&tfLPToken != 0) {
		amtAsset := tx.Asset{Currency: a.Amount.Currency, Issuer: a.Amount.Issuer}
		if result := requireAuth(ctx.View, amtAsset, accountID); result != tx.TesSUCCESS {
			return result
		}
		// Check AMM account freeze
		if isFrozen(ctx.View, ammAccountID, amtAsset) {
			return tx.TecFROZEN
		}
		// Check individual freeze
		if tx.IsIndividualFrozen(ctx.View, accountID, amtAsset) {
			return tx.TecFROZEN
		}
	}
	if a.Amount2 != nil && !(flags&tfLPToken != 0) {
		amt2Asset := tx.Asset{Currency: a.Amount2.Currency, Issuer: a.Amount2.Issuer}
		if result := requireAuth(ctx.View, amt2Asset, accountID); result != tx.TesSUCCESS {
			return result
		}
		if isFrozen(ctx.View, ammAccountID, amt2Asset) {
			return tx.TecFROZEN
		}
		if tx.IsIndividualFrozen(ctx.View, accountID, amt2Asset) {
			return tx.TecFROZEN
		}
	}

	// Check LP token trustline reserve
	// Reference: rippled AMMDeposit.cpp lines 353-362
	ammAccountAddr, _ := encodeAccountID(ammAccountID)
	lptCurrency := generateAMMLPTCurrency(a.Asset.Currency, a.Asset2.Currency)
	lptKey := keylet.Line(accountID, ammAccountID, lptCurrency)
	lptExists, _ := ctx.View.Exists(lptKey)
	if !lptExists {
		// Account needs reserve for new LP token trustline
		xrpLiquid := xrpLiquidBalance(ctx.View, accountID, 1)
		if xrpLiquid <= 0 {
			return TecINSUF_RESERVE_LINE
		}
	}

	// =========================================================================
	// APPLY - Reference: rippled AMMDeposit.cpp applyGuts lines 367-480
	// =========================================================================

	// Get trading fee - use existing or from transaction for empty AMM
	tfee := amm.TradingFee
	if lptBalance.IsZero() && a.TradingFee > 0 {
		tfee = a.TradingFee
	}

	// Get amounts from transaction
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

	// Result amounts - use tx.Amount for precision
	var lpTokensToIssue tx.Amount
	var depositAmount1, depositAmount2 tx.Amount

	// Handle different deposit modes
	switch {
	case flags&tfLPToken != 0:
		// Proportional deposit for specified LP tokens
		// Equations 5 and 6: a = (t/T) * A, b = (t/T) * B
		if lpTokensRequested.IsZero() || lptBalance.IsZero() {
			return tx.TecAMM_INVALID_TOKENS
		}
		// Calculate proportional amounts using Amount arithmetic
		// depositAmount1 = assetBalance1 * (lpTokensRequested / lptBalance)
		depositAmount1 = proportionalAmount(assetBalance1, lpTokensRequested, lptBalance)
		depositAmount2 = proportionalAmount(assetBalance2, lpTokensRequested, lptBalance)
		lpTokensToIssue = lpTokensRequested

	case flags&tfSingleAsset != 0:
		// Single asset deposit - determine which asset is being deposited
		// by comparing the Amount's currency/issuer with Asset and Asset2
		isDepositForAsset1 := a.Amount != nil && matchesAsset(a.Amount, a.Asset)
		isDepositForAsset2 := a.Amount != nil && matchesAsset(a.Amount, a.Asset2)

		if isDepositForAsset1 {
			lpTokensToIssue = lpTokensOut(assetBalance1, amount1, lptBalance, tfee)
			if lpTokensToIssue.IsZero() {
				return tx.TecAMM_INVALID_TOKENS
			}
			depositAmount1 = amount1
			depositAmount2 = zeroAmount(a.Asset2)
		} else if isDepositForAsset2 {
			lpTokensToIssue = lpTokensOut(assetBalance2, amount1, lptBalance, tfee)
			if lpTokensToIssue.IsZero() {
				return tx.TecAMM_INVALID_TOKENS
			}
			depositAmount1 = zeroAmount(a.Asset)
			depositAmount2 = amount1 // amount1 contains the Amount field value
		} else {
			// Amount currency doesn't match either AMM asset
			return tx.TecAMM_INVALID_TOKENS
		}

	case flags&tfTwoAsset != 0:
		// Two asset deposit with limits
		// Reference: rippled AMMDeposit.cpp equalDepositLimit()
		// Calculate fractions: frac1 = amount1/assetBalance1, frac2 = amount2/assetBalance2
		// Use the smaller fraction to maintain ratio
		// Convert to IOU for precise division (XRP/XRP integer division loses fractional part)
		frac1 := toIOUForCalc(amount1).Div(toIOUForCalc(assetBalance1), false)
		frac2 := toIOUForCalc(amount2).Div(toIOUForCalc(assetBalance2), false)

		// Use the smaller fraction
		var frac tx.Amount
		if !assetBalance2.IsZero() && frac2.Compare(frac1) < 0 {
			frac = frac2
		} else {
			frac = frac1
		}

		// Calculate LP tokens to issue (always IOU)
		lpTokensToIssue = lptBalance.Mul(frac, false)

		// Calculate deposit amounts using the fraction
		// For XRP: convert to IOU, multiply, then convert back to drops
		if assetBalance1.IsNative() {
			iouResult := toIOUForCalc(assetBalance1).Mul(frac, false)
			// Convert IOU result back to XRP drops
			drops := int64(iouResult.Mantissa())
			for e := iouResult.Exponent(); e > 0; e-- {
				drops *= 10
			}
			for e := iouResult.Exponent(); e < 0; e++ {
				drops /= 10
			}
			depositAmount1 = sle.NewXRPAmountFromInt(drops)
		} else {
			depositAmount1 = assetBalance1.Mul(frac, false)
		}

		if assetBalance2.IsNative() {
			iouResult := toIOUForCalc(assetBalance2).Mul(frac, false)
			drops := int64(iouResult.Mantissa())
			for e := iouResult.Exponent(); e > 0; e-- {
				drops *= 10
			}
			for e := iouResult.Exponent(); e < 0; e++ {
				drops /= 10
			}
			depositAmount2 = sle.NewXRPAmountFromInt(drops)
		} else {
			depositAmount2 = assetBalance2.Mul(frac, false)
		}

	case flags&tfOneAssetLPToken != 0:
		// Single asset deposit for specific LP tokens
		isDepositForAsset1 := matchesAsset(a.Amount, a.Asset)
		isDepositForAsset2 := matchesAsset(a.Amount, a.Asset2)

		if isDepositForAsset1 {
			depositAmount1 = ammAssetIn(assetBalance1, lptBalance, lpTokensRequested, tfee)
			if isGreater(depositAmount1, amount1) {
				return tx.TecAMM_FAILED
			}
			depositAmount2 = zeroAmount(a.Asset2)
		} else if isDepositForAsset2 {
			depositAmount2 = ammAssetIn(assetBalance2, lptBalance, lpTokensRequested, tfee)
			if isGreater(depositAmount2, amount1) { // amount1 is the max from Amount field
				return tx.TecAMM_FAILED
			}
			depositAmount1 = zeroAmount(a.Asset)
		} else {
			return tx.TecAMM_INVALID_TOKENS
		}
		lpTokensToIssue = lpTokensRequested

	case flags&tfLimitLPToken != 0:
		// Single asset deposit with effective price limit
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

		lpTokensToIssue = lpTokensOut(assetBalance, amount1, lptBalance, tfee)
		if lpTokensToIssue.IsZero() {
			return tx.TecAMM_INVALID_TOKENS
		}

		// Check effective price: EP = amount / lpTokens
		// User provides max EP in EPrice, so we check: amount/lpTokens <= EPrice
		if a.EPrice != nil && !a.EPrice.IsZero() {
			// effectivePrice = amount1 / lpTokensToIssue
			effectivePrice := amount1.Div(lpTokensToIssue, false)
			if isGreater(effectivePrice, *a.EPrice) {
				return tx.TecAMM_FAILED
			}
		}

		if isDepositForAsset1 {
			depositAmount1 = amount1
			depositAmount2 = zeroAmount(a.Asset2)
		} else {
			depositAmount1 = zeroAmount(a.Asset)
			depositAmount2 = amount1
		}

	case flags&tfTwoAssetIfEmpty != 0:
		// Deposit into empty AMM
		if !lptBalance.IsZero() {
			return tx.TecAMM_NOT_EMPTY
		}
		lpTokensToIssue = calculateLPTokens(amount1, amount2)
		depositAmount1 = amount1
		depositAmount2 = amount2
		// Set trading fee if provided
		if a.TradingFee > 0 {
			amm.TradingFee = a.TradingFee
		}

	default:
		return tx.TemMALFORMED
	}

	if lpTokensToIssue.IsZero() {
		return tx.TecAMM_INVALID_TOKENS
	}

	// Check depositor has sufficient balance
	// Reference: rippled AMMDeposit.cpp preclaim - accountHolds check
	isXRP1 := a.Asset.Currency == "" || a.Asset.Currency == "XRP"
	isXRP2 := a.Asset2.Currency == "" || a.Asset2.Currency == "XRP"

	// Check IOU balances first
	// Reference: rippled preclaim checks accountHolds >= deposit for each IOU
	if !isXRP1 && !depositAmount1.IsZero() {
		// Check depositor has enough of asset1 (IOU)
		// Skip check if depositor is the issuer (they can issue unlimited)
		issuerID1, _ := sle.DecodeAccountID(a.Asset.Issuer)
		if accountID != issuerID1 {
			depositorFunds := tx.AccountFunds(ctx.View, accountID, depositAmount1, false, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
			if depositorFunds.Compare(depositAmount1) < 0 {
				return TecUNFUNDED_AMM
			}
		}
	}
	if !isXRP2 && !depositAmount2.IsZero() {
		// Check depositor has enough of asset2 (IOU)
		issuerID2, _ := sle.DecodeAccountID(a.Asset2.Issuer)
		if accountID != issuerID2 {
			depositorFunds := tx.AccountFunds(ctx.View, accountID, depositAmount2, false, ctx.Config.ReserveBase, ctx.Config.ReserveIncrement)
			if depositorFunds.Compare(depositAmount2) < 0 {
				return TecUNFUNDED_AMM
			}
		}
	}

	// Calculate total XRP needed for deposit
	totalXRPNeeded := int64(0)
	if isXRP1 && !depositAmount1.IsZero() {
		totalXRPNeeded += depositAmount1.Drops()
	}
	if isXRP2 && !depositAmount2.IsZero() {
		totalXRPNeeded += depositAmount2.Drops()
	}
	if totalXRPNeeded > 0 && int64(ctx.Account.Balance) < totalXRPNeeded {
		return TecUNFUNDED_AMM
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
		issuerID, err := sle.DecodeAccountID(a.Asset.Issuer)
		if err != nil {
			return tx.TefINTERNAL
		}
		// Deduct from depositor's trust line (negative delta)
		if err := updateTrustlineBalanceInView(accountID, issuerID, a.Asset.Currency, depositAmount1.Negate(), ctx.View); err != nil {
			// Trust line update failed - may not have sufficient balance
			return TecUNFUNDED_AMM
		}
		// Credit AMM's trust line (positive delta)
		if err := createOrUpdateAMMTrustline(ammAccountID, a.Asset, depositAmount1, ctx.View); err != nil {
			return TecNO_LINE
		}
	}
	if !isXRP2 && !depositAmount2.IsZero() {
		issuerID, err := sle.DecodeAccountID(a.Asset2.Issuer)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := updateTrustlineBalanceInView(accountID, issuerID, a.Asset2.Currency, depositAmount2.Negate(), ctx.View); err != nil {
			return TecUNFUNDED_AMM
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

	// Increment owner count if we created a new LP token trustline
	// Reference: rippled - the depositor pays reserve for the LP token trustline
	if !lptExists {
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
	ammAccountBytes, err := sle.SerializeAccountRoot(ammAccount)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(ammAccountKey, ammAccountBytes); err != nil {
		return tx.TefINTERNAL
	}

	// Persist updated depositor account
	accountKey := keylet.Account(accountID)
	accountBytes, err := sle.SerializeAccountRoot(ctx.Account)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(accountKey, accountBytes); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

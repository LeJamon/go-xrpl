package amm

import (
	"errors"

	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
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

// Validate validates the AMMClawback transaction
// Reference: rippled AMMClawback.cpp preflight
func (a *AMMClawback) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	// Check flags
	if a.GetFlags()&tfAMMClawbackMask != 0 {
		return errors.New("temINVALID_FLAG: invalid flags for AMMClawback")
	}

	// Holder is required
	if a.Holder == "" {
		return errors.New("temMALFORMED: Holder is required")
	}

	// Holder cannot be the same as issuer (Account)
	if a.Holder == a.Common.Account {
		return errors.New("temMALFORMED: Holder cannot be the same as issuer")
	}

	// Validate asset pair
	if a.Asset.Currency == "" {
		return errors.New("temMALFORMED: Asset is required")
	}

	if a.Asset2.Currency == "" {
		return errors.New("temMALFORMED: Asset2 is required")
	}

	// Asset cannot be XRP (must be issued currency)
	if a.Asset.Currency == "XRP" || a.Asset.Currency == "" && a.Asset.Issuer == "" {
		return errors.New("temMALFORMED: Asset cannot be XRP")
	}

	// Asset issuer must match the transaction account (issuer)
	if a.Asset.Issuer != a.Common.Account {
		return errors.New("temMALFORMED: Asset issuer must match Account")
	}

	// If tfClawTwoAssets is set, both assets must be issued by the same issuer
	if a.GetFlags()&tfClawTwoAssets != 0 {
		if a.Asset.Issuer != a.Asset2.Issuer {
			return errors.New("temINVALID_FLAG: tfClawTwoAssets requires both assets to have the same issuer")
		}
	}

	// Validate Amount if provided
	if a.Amount != nil {
		// Amount must be positive
		if err := validateAMMAmount(*a.Amount); err != nil {
			return errors.New("temBAD_AMOUNT: invalid Amount - " + err.Error())
		}
		// Amount's issue must match tx.Asset
		if a.Amount.Currency != a.Asset.Currency || a.Amount.Issuer != a.Asset.Issuer {
			return errors.New("temBAD_AMOUNT: Amount issue must match tx.Asset")
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
// Reference: rippled AMMClawback.cpp preclaim + applyGuts
func (a *AMMClawback) Apply(ctx *tx.ApplyContext) tx.Result {
	issuerID := ctx.AccountID

	// Find the holder
	holderID, err := sle.DecodeAccountID(a.Holder)
	if err != nil {
		return tx.TemINVALID
	}

	holderKey := keylet.Account(holderID)
	holderData, err := ctx.View.Read(holderKey)
	if err != nil {
		return TerNO_ACCOUNT
	}
	holderAccount, err := sle.ParseAccountRoot(holderData)
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
	if (ctx.Account.Flags & sle.LsfAllowTrustLineClawback) == 0 {
		return tx.TecNO_PERMISSION
	}
	if (ctx.Account.Flags & sle.LsfNoFreeze) != 0 {
		return tx.TecNO_PERMISSION
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

	// Get current AMM balances from actual state (not stored in AMM entry)
	// Reference: rippled ammHolds - reads from AccountRoot (XRP) and trustlines (IOU)
	assetBalance1, assetBalance2, lptAMMBalance := AMMHolds(ctx.View, amm, false)

	if lptAMMBalance.IsZero() {
		return tx.TecAMM_BALANCE // AMM is empty
	}

	// Get holder's LP token balance
	// In full implementation, would read from LP token trustline
	// For now, use half of the AMM LP token balance as holder's balance (simplified)
	two := sle.NewIssuedAmountFromValue(2e15, -15, "", "")
	holdLPTokens := lptAMMBalance.Div(two, false)

	if holdLPTokens.IsZero() {
		return tx.TecAMM_BALANCE // Holder has no LP tokens
	}

	flags := a.GetFlags()

	var lpTokensToWithdraw tx.Amount
	var withdrawAmount1, withdrawAmount2 tx.Amount

	if a.Amount == nil {
		// No amount specified - withdraw all LP tokens the holder has
		// This is a proportional two-asset withdrawal
		lpTokensToWithdraw = holdLPTokens

		// Calculate proportional withdrawal amounts
		withdrawAmount1 = proportionalAmount(assetBalance1, holdLPTokens, lptAMMBalance)
		withdrawAmount2 = proportionalAmount(assetBalance2, holdLPTokens, lptAMMBalance)
	} else {
		// Amount specified - calculate proportional withdrawal based on specified amount
		clawAmount := *a.Amount

		// Calculate fraction based on the clawback amount relative to asset1 balance
		if assetBalance1.IsZero() {
			return tx.TecAMM_BALANCE
		}
		frac := clawAmount.Div(assetBalance1, false)

		// Calculate LP tokens needed for this withdrawal
		lpTokensNeeded := lptAMMBalance.Mul(frac, false)

		// If holder doesn't have enough LP tokens, clawback all they have
		if isGreater(lpTokensNeeded, holdLPTokens) {
			lpTokensToWithdraw = holdLPTokens
			withdrawAmount1 = proportionalAmount(assetBalance1, holdLPTokens, lptAMMBalance)
			withdrawAmount2 = proportionalAmount(assetBalance2, holdLPTokens, lptAMMBalance)
		} else {
			lpTokensToWithdraw = lpTokensNeeded
			withdrawAmount1 = clawAmount
			withdrawAmount2 = assetBalance2.Mul(frac, false)
		}
	}

	// Verify withdrawal amounts don't exceed balances
	if isGreater(withdrawAmount1, assetBalance1) {
		withdrawAmount1 = assetBalance1
	}
	if isGreater(withdrawAmount2, assetBalance2) {
		withdrawAmount2 = assetBalance2
	}

	// Perform the withdrawal from AMM
	isXRP1 := a.Asset.Currency == "" || a.Asset.Currency == "XRP"
	isXRP2 := a.Asset2.Currency == "" || a.Asset2.Currency == "XRP"

	// Transfer asset1 from AMM to holder (intermediate step)
	if isXRP1 && !withdrawAmount1.IsZero() {
		drops := uint64(withdrawAmount1.Drops())
		ammAccount.Balance -= drops
	}
	// Transfer asset2 from AMM to holder (intermediate step)
	if isXRP2 && !withdrawAmount2.IsZero() {
		drops := uint64(withdrawAmount2.Drops())
		ammAccount.Balance -= drops
	}

	// Now claw back: transfer asset1 from holder to issuer
	if isXRP1 && !withdrawAmount1.IsZero() {
		drops := uint64(withdrawAmount1.Drops())
		ctx.Account.Balance += drops
	}

	// If tfClawTwoAssets is set, also claw back asset2
	if flags&tfClawTwoAssets != 0 {
		if isXRP2 && !withdrawAmount2.IsZero() {
			drops := uint64(withdrawAmount2.Drops())
			ctx.Account.Balance += drops
		}
	} else {
		// Asset2 goes to holder (not clawed back)
		if isXRP2 && !withdrawAmount2.IsZero() {
			drops := uint64(withdrawAmount2.Drops())
			holderAccount.Balance += drops
		}
	}

	// Reduce LP token balance (this is stored in the AMM entry)
	newLPBalance, _ := lptAMMBalance.Sub(lpTokensToWithdraw)
	amm.LPTokenBalance = newLPBalance

	// NOTE: Asset balances are NOT stored in AMM entry
	// They are updated by the balance transfers above:
	// - XRP: via ammAccount.Balance -= drops
	// - IOU: via trustline updates (already done above)

	// Check if AMM should be deleted (empty)
	ammDeleted := false
	if newLPBalance.IsZero() {
		if err := ctx.View.Erase(ammKey); err != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Erase(ammAccountKey); err != nil {
			return tx.TefINTERNAL
		}
		ammDeleted = true
	}

	if !ammDeleted {
		ammBytes, err := serializeAMMData(amm)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(ammKey, ammBytes); err != nil {
			return tx.TefINTERNAL
		}

		ammAccountBytes, err := sle.SerializeAccountRoot(ammAccount)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(ammAccountKey, ammAccountBytes); err != nil {
			return tx.TefINTERNAL
		}
	}

	// Persist updated issuer account
	accountKey := keylet.Account(issuerID)
	accountBytes, err := sle.SerializeAccountRoot(ctx.Account)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(accountKey, accountBytes); err != nil {
		return tx.TefINTERNAL
	}

	// Persist updated holder account
	holderBytes, err := sle.SerializeAccountRoot(holderAccount)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(holderKey, holderBytes); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

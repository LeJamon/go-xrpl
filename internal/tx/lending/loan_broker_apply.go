package lending

import (
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func toLargeForRules(n state.XRPLNumber, rules *amendment.Rules) lmath.N {
	return lmath.NumScaled(n.Mantissa(), n.Exponent(), lendingNumberScale(rules))
}

// amountAssetMatches reports whether amount denominates the given asset.
func amountAssetMatches(amount tx.Amount, asset tx.Asset) bool {
	if asset.IsMPT() {
		return amount.IsMPT() && strings.EqualFold(amount.MPTIssuanceID(), asset.MPTIssuanceID)
	}
	if asset.IsNative() {
		return amount.IsNative()
	}
	return !amount.IsNative() && !amount.IsMPT() &&
		amount.Currency == asset.Currency && amount.Issuer == asset.Issuer
}

// -------------------- LoanBrokerSet --------------------

func (l *LoanBrokerSet) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	number := func(value string) lmath.N { return lendNumForRules(value, config.RequireRules()) }
	accountID, err := state.DecodeAccountID(l.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	vaultID, ok := hashBytes(l.VaultID)
	if !ok {
		return ter.TemMALFORMED
	}
	vinfo, verr := vault.ReadVaultInfo(view, keylet.VaultByID(vaultID))
	if verr != nil {
		return ter.TefINTERNAL
	}
	if vinfo == nil {
		return ter.TecNO_ENTRY
	}
	asset := vinfo.Asset
	if accountID != vinfo.Owner {
		return ter.TecNO_PERMISSION
	}

	if l.LoanBrokerID != nil {
		brokerID, bok := hashBytes(*l.LoanBrokerID)
		if !bok {
			return ter.TemMALFORMED
		}
		b, berr := readLoanBroker(view, keylet.LoanBrokerByID(brokerID))
		if berr != nil {
			return ter.TefINTERNAL
		}
		if b == nil {
			return ter.TecNO_ENTRY
		}
		if b.VaultID != vaultID {
			return ter.TecNO_PERMISSION
		}
		if b.Owner != accountID {
			return ter.TecNO_PERMISSION
		}
		if l.DebtMaximum != nil {
			debtMax := number(*l.DebtMaximum)
			if debtMax.Signum() != 0 && debtMax.Cmp(number(b.DebtTotal)) < 0 {
				return ter.TecLIMIT_EXCEEDED
			}
		}
	} else {
		if res := vault.CanAddHolding(view, asset); res != ter.TesSUCCESS {
			return res
		}
		if res := tx.AssetFrozen(view, vinfo.Account, asset); res != ter.TesSUCCESS {
			return res
		}
	}

	if l.DebtMaximum != nil && !representableAsAsset(number(*l.DebtMaximum), asset) {
		return ter.TecPRECISION_LOSS
	}
	return ter.TesSUCCESS
}

func (l *LoanBrokerSet) Apply(ctx *tx.ApplyContext) ter.Result {
	number := func(value string) lmath.N { return lendNumForRules(value, ctx.Rules()) }
	accountID := ctx.AccountID
	vaultID, ok := hashBytes(l.VaultID)
	if !ok {
		return ter.TefINTERNAL
	}
	vinfo, verr := vault.ReadVaultInfo(ctx.View, keylet.VaultByID(vaultID))
	if verr != nil || vinfo == nil {
		return ter.TefBAD_LEDGER
	}
	asset := vinfo.Asset
	integral := assetIntegral(asset)

	if l.LoanBrokerID != nil {
		brokerID, _ := hashBytes(*l.LoanBrokerID)
		brokerKey := keylet.LoanBrokerByID(brokerID)
		b, berr := readLoanBroker(ctx.View, brokerKey)
		if berr != nil || b == nil {
			return ter.TefBAD_LEDGER
		}
		if l.Data != nil {
			b.Data = *l.Data
		}
		if l.DebtMaximum != nil {
			b.DebtMaximum = numStr(number(*l.DebtMaximum))
		}
		associateBrokerAsset(b, integral, ctx.Rules())
		return updateBroker(ctx, brokerKey, b)
	}

	sequence := l.GetCommon().SeqProxy()
	brokerKey := keylet.LoanBroker(accountID, sequence)
	if exists, _ := ctx.View.Exists(brokerKey); exists {
		return ter.TecDUPLICATE
	}

	ownerDir, err := state.DirInsert(ctx.View, keylet.OwnerDir(accountID), brokerKey.Key, false, func(d *state.DirectoryNode) {
		d.Owner = accountID
	})
	if err != nil {
		return ter.TecDIR_FULL
	}
	vaultDir, err := state.DirInsert(ctx.View, keylet.OwnerDir(vinfo.Account), brokerKey.Key, false, func(d *state.DirectoryNode) {
		d.Owner = vinfo.Account
	})
	if err != nil {
		return ter.TecDIR_FULL
	}

	newOwnerCount := ctx.Account.OwnerCount + 2
	if ctx.PriorBalance() < ctx.AccountReserve(newOwnerCount) {
		return ter.TecINSUFFICIENT_RESERVE
	}

	pseudoID, res := createLoanBrokerPseudoAccount(ctx, brokerKey.Key)
	if res != ter.TesSUCCESS {
		return res
	}
	lineDelta, res := vault.AddEmptyHolding(ctx, pseudoID, asset, ctx.PriorBalance())
	if res != ter.TesSUCCESS {
		return res
	}
	if res := adjustPseudoOwnerCount(ctx, pseudoID, lineDelta); res != ter.TesSUCCESS {
		return res
	}

	b := &loanBrokerData{
		Sequence:     sequence,
		OwnerNode:    ownerDir.Page,
		VaultNode:    vaultDir.Page,
		VaultID:      vaultID,
		Account:      pseudoID,
		Owner:        accountID,
		LoanSequence: 1,
	}
	if l.Data != nil {
		b.Data = *l.Data
	}
	if l.ManagementFeeRate != nil {
		b.ManagementFeeRate = *l.ManagementFeeRate
	}
	if l.DebtMaximum != nil {
		b.DebtMaximum = numStr(number(*l.DebtMaximum))
	}
	if l.CoverRateMinimum != nil {
		b.CoverRateMinimum = *l.CoverRateMinimum
	}
	if l.CoverRateLiquidation != nil {
		b.CoverRateLiquidation = *l.CoverRateLiquidation
	}
	associateBrokerAsset(b, integral, ctx.Rules())

	data, serr := serializeLoanBrokerForRules(b, ctx.Rules())
	if serr != nil {
		return ter.TefINTERNAL
	}
	if ierr := ctx.View.Insert(brokerKey, data); ierr != nil {
		return ter.TefINTERNAL
	}
	ctx.Account.OwnerCount = newOwnerCount
	return ter.TesSUCCESS
}

// updateBroker serializes and updates a broker entry.
func updateBroker(ctx *tx.ApplyContext, brokerKey keylet.Keylet, b *loanBrokerData) ter.Result {
	data, serr := serializeLoanBrokerForRules(b, ctx.Rules())
	if serr != nil {
		return ter.TefINTERNAL
	}
	if uerr := ctx.View.Update(brokerKey, data); uerr != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

// -------------------- LoanBrokerDelete --------------------

func (l *LoanBrokerDelete) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	rules := config.RequireRules()
	accountID, err := state.DecodeAccountID(l.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	brokerID, ok := hashBytes(l.LoanBrokerID)
	if !ok {
		return ter.TemMALFORMED
	}
	b, berr := readLoanBroker(view, keylet.LoanBrokerByID(brokerID))
	if berr != nil {
		return ter.TefINTERNAL
	}
	if b == nil {
		return ter.TecNO_ENTRY
	}
	if b.Owner != accountID {
		return ter.TecNO_PERMISSION
	}
	if b.OwnerCount != 0 {
		return ter.TecHAS_OBLIGATIONS
	}
	vinfo, verr := vault.ReadVaultLending(view, keylet.VaultByID(b.VaultID))
	if verr != nil || vinfo == nil {
		return ter.TefBAD_LEDGER
	}
	if debt := lendNumForRules(b.DebtTotal, rules); debt.Signum() != 0 {
		integral := assetIntegral(vinfo.Asset)
		scale := vaultScaleOfForRules(vinfo, integral, rules)
		if lmath.RoundAssetTowardsZero(lmath.Asset{Integral: integral}, debt, scale).Signum() != 0 {
			return ter.TecHAS_OBLIGATIONS
		}
	}
	if lendNumForRules(b.CoverAvailable, rules).Signum() > 0 {
		if res := mptutil.CheckDeepFrozen(view, b.Owner, vinfo.Asset); res != ter.TesSUCCESS {
			return res
		}
		if rules.FixCleanup3_2_0Enabled() {
			if res := tx.AssetFrozen(view, b.Account, vinfo.Asset); res != ter.TesSUCCESS {
				return res
			}
		}
	}
	return ter.TesSUCCESS
}

func (l *LoanBrokerDelete) Apply(ctx *tx.ApplyContext) ter.Result {
	accountID := ctx.AccountID
	brokerID, ok := hashBytes(l.LoanBrokerID)
	if !ok {
		return ter.TefINTERNAL
	}
	brokerKey := keylet.LoanBrokerByID(brokerID)
	b, berr := readLoanBroker(ctx.View, brokerKey)
	if berr != nil || b == nil {
		return ter.TefBAD_LEDGER
	}
	vinfo, verr := vault.ReadVaultInfo(ctx.View, keylet.VaultByID(b.VaultID))
	if verr != nil || vinfo == nil {
		return ter.TefBAD_LEDGER
	}
	asset := vinfo.Asset

	// Return remaining cover to the broker owner.
	cover := lendNumForRules(b.CoverAvailable, ctx.Rules())
	if cover.Signum() > 0 {
		if res := vault.SendAsset(ctx, b.Account, accountID, asset, cover); res != ter.TesSUCCESS {
			return res
		}
	}

	// Remove the pseudo-account's asset holding and destroy the pseudo-account.
	assetDelta, res := vault.RemoveAssetHolding(ctx, b.Account, asset)
	if res != ter.TesSUCCESS {
		return res
	}
	pseudo, res := vault.ApplyAssetHoldingOwnerCount(ctx.View, b.Account, assetDelta)
	if res != ter.TesSUCCESS {
		return res
	}
	pseudoData, serr := state.SerializeAccountRoot(pseudo)
	if serr != nil {
		return ter.TefINTERNAL
	}
	if uerr := ctx.View.Update(keylet.Account(b.Account), pseudoData); uerr != nil {
		return ter.TefINTERNAL
	}
	if pseudo.Balance != 0 || pseudo.OwnerCount != 0 {
		return ter.TecHAS_OBLIGATIONS
	}

	// dirRemove from owner dir and vault-pseudo dir, then erase the broker.
	if r, e := state.DirRemove(ctx.View, keylet.OwnerDir(accountID), b.OwnerNode, brokerKey.Key, false); e != nil || !r.Success {
		return ter.TefBAD_LEDGER
	}
	if r, e := state.DirRemove(ctx.View, keylet.OwnerDir(vinfo.Account), b.VaultNode, brokerKey.Key, false); e != nil || !r.Success {
		return ter.TefBAD_LEDGER
	}
	if e := ctx.View.Erase(keylet.Account(b.Account)); e != nil {
		return ter.TefINTERNAL
	}
	if e := ctx.View.Erase(brokerKey); e != nil {
		return ter.TefINTERNAL
	}
	if ctx.Account.OwnerCount >= 2 {
		ctx.Account.OwnerCount -= 2
	} else {
		ctx.Account.OwnerCount = 0
	}
	return ter.TesSUCCESS
}

// -------------------- LoanBrokerCoverDeposit --------------------

func (l *LoanBrokerCoverDeposit) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	accountID, err := state.DecodeAccountID(l.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	brokerID, ok := hashBytes(l.LoanBrokerID)
	if !ok {
		return ter.TemMALFORMED
	}
	b, berr := readLoanBroker(view, keylet.LoanBrokerByID(brokerID))
	if berr != nil {
		return ter.TefINTERNAL
	}
	if b == nil {
		return ter.TecNO_ENTRY
	}
	if b.Owner != accountID {
		return ter.TecNO_PERMISSION
	}
	vinfo, verr := vault.ReadVaultInfo(view, keylet.VaultByID(b.VaultID))
	if verr != nil || vinfo == nil {
		return ter.TefBAD_LEDGER
	}
	asset := vinfo.Asset
	if !amountAssetMatches(l.Amount, asset) {
		return ter.TecWRONG_ASSET
	}
	if res := mptutil.CanTransferAsset(view, asset, accountID, b.Account, false); res != ter.TesSUCCESS {
		return res
	}
	if config.RequireRules().Enabled(amendment.FeatureFixCleanup3_3_0) {
		if res := mptutil.CheckDepositFreeze(view, accountID, b.Account, asset); res != ter.TesSUCCESS {
			return res
		}
	} else {
		if res := tx.AssetFrozen(view, accountID, asset); res != ter.TesSUCCESS {
			return res
		}
		if res := tx.AssetFrozen(view, b.Account, asset); res != ter.TesSUCCESS {
			return res
		}
	}
	if res := mptutil.RequireAssetAuthAt(view, asset, accountID, mptutil.StrongAuth, config.ParentCloseTime); res != ter.TesSUCCESS {
		return res
	}
	amount, cres := roundCoverDeposit(config.RequireRules().FixCleanup3_2_0Enabled(),
		lendNumForRules(b.CoverAvailable, config.RequireRules()), amountToLendNumForRules(l.Amount, config.RequireRules()), assetIntegral(asset))
	if cres != ter.TesSUCCESS {
		return cres
	}
	holds, herr := vault.AccountHoldsFull(view, config, accountID, asset)
	if herr != nil {
		return ter.TefINTERNAL
	}
	if toLargeForRules(holds, config.RequireRules()).Cmp(amount) < 0 {
		return ter.TecINSUFFICIENT_FUNDS
	}
	return ter.TesSUCCESS
}

func (l *LoanBrokerCoverDeposit) Apply(ctx *tx.ApplyContext) ter.Result {
	accountID := ctx.AccountID
	brokerID, ok := hashBytes(l.LoanBrokerID)
	if !ok {
		return ter.TefINTERNAL
	}
	brokerKey := keylet.LoanBrokerByID(brokerID)
	b, berr := readLoanBroker(ctx.View, brokerKey)
	if berr != nil || b == nil {
		return ter.TecINTERNAL
	}
	vinfo, verr := vault.ReadVaultInfo(ctx.View, keylet.VaultByID(b.VaultID))
	if verr != nil || vinfo == nil {
		return ter.TecINTERNAL
	}
	asset := vinfo.Asset
	integral := assetIntegral(asset)
	amount, cres := roundCoverDeposit(ctx.Rules().FixCleanup3_2_0Enabled(),
		lendNumForRules(b.CoverAvailable, ctx.Rules()), amountToLendNumForRules(l.Amount, ctx.Rules()), integral)
	if cres != ter.TesSUCCESS {
		return ter.TecINTERNAL
	}

	if res := vault.SendAsset(ctx, accountID, b.Account, asset, amount); res != ter.TesSUCCESS {
		return res
	}
	b.CoverAvailable = numStr(lendNumForRules(b.CoverAvailable, ctx.Rules()).Add(amount))
	associateBrokerAsset(b, integral, ctx.Rules())
	return updateBroker(ctx, brokerKey, b)
}

// -------------------- LoanBrokerCoverWithdraw --------------------

func (l *LoanBrokerCoverWithdraw) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	accountID, err := state.DecodeAccountID(l.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	dstID := accountID
	if l.Destination != "" {
		id, derr := state.DecodeAccountID(l.Destination)
		if derr != nil {
			return ter.TemMALFORMED
		}
		dstID = id
	}
	if tx.IsPseudoAccountID(view, dstID) {
		return ter.TecPSEUDO_ACCOUNT
	}
	brokerID, ok := hashBytes(l.LoanBrokerID)
	if !ok {
		return ter.TemMALFORMED
	}
	b, berr := readLoanBroker(view, keylet.LoanBrokerByID(brokerID))
	if berr != nil {
		return ter.TefINTERNAL
	}
	if b == nil {
		return ter.TecNO_ENTRY
	}
	if b.Owner != accountID {
		return ter.TecNO_PERMISSION
	}
	vinfo, verr := vault.ReadVaultLending(view, keylet.VaultByID(b.VaultID))
	if verr != nil || vinfo == nil {
		return ter.TefBAD_LEDGER
	}
	asset := vinfo.Asset
	if !amountAssetMatches(l.Amount, asset) {
		return ter.TecWRONG_ASSET
	}
	fix320 := config.RequireRules().FixCleanup3_2_0Enabled()
	integral := assetIntegral(asset)
	if res := canApplyToBrokerCover(fix320, lendNumForRules(b.CoverAvailable, config.RequireRules()), amountToLendNumForRules(l.Amount, config.RequireRules()), integral); res != ter.TesSUCCESS {
		return res
	}
	if res := mptutil.CanTransferAsset(view, asset, b.Account, dstID, fix320); res != ter.TesSUCCESS {
		return res
	}
	if accountID != dstID {
		if res := vault.CanWithdraw(view, accountID, dstID, l.Amount, l.DestinationTag != nil, config.NumberContext()); res != ter.TesSUCCESS {
			return res
		}
	}
	authType := mptutil.WeakAuth
	if accountID != dstID {
		authType = mptutil.StrongAuth
	}
	if res := mptutil.RequireAssetAuthAt(view, asset, dstID, authType, config.ParentCloseTime); res != ter.TesSUCCESS {
		return res
	}
	if config.RequireRules().Enabled(amendment.FeatureFixCleanup3_3_0) {
		if res := mptutil.CheckWithdrawFreeze(view, b.Account, accountID, dstID, asset); res != ter.TesSUCCESS {
			return res
		}
	}

	amount := amountToLendNumForRules(l.Amount, config.RequireRules())
	coverAvail := lendNumForRules(b.CoverAvailable, config.RequireRules())
	debtTotal := lendNumForRules(b.DebtTotal, config.RequireRules())
	var minimumCover lmath.N
	if fix320 {
		minimumCover = minimumBrokerCover(debtTotal, b.CoverRateMinimum, vaultScaleOfForRules(vinfo, integral, config.RequireRules()), integral)
	} else {
		minimumCover = brokerCoverRateAtScale(debtTotal, b.CoverRateMinimum, debtTotal.AssetExponent(integral, state.RoundToNearest), integral)
	}
	if coverAvail.Cmp(amount) < 0 {
		return ter.TecINSUFFICIENT_FUNDS
	}
	if coverAvail.Sub(amount).Cmp(minimumCover) < 0 {
		return ter.TecINSUFFICIENT_FUNDS
	}
	holds, herr := vault.AccountHoldsFull(view, config, b.Account, asset)
	if herr != nil {
		return ter.TefINTERNAL
	}
	if toLargeForRules(holds, config.RequireRules()).Cmp(amount) < 0 {
		return ter.TecINSUFFICIENT_FUNDS
	}
	return ter.TesSUCCESS
}

func (l *LoanBrokerCoverWithdraw) Apply(ctx *tx.ApplyContext) ter.Result {
	accountID := ctx.AccountID
	dstID := accountID
	if l.Destination != "" {
		id, derr := state.DecodeAccountID(l.Destination)
		if derr != nil {
			return ter.TefINTERNAL
		}
		dstID = id
	}
	brokerID, ok := hashBytes(l.LoanBrokerID)
	if !ok {
		return ter.TefINTERNAL
	}
	brokerKey := keylet.LoanBrokerByID(brokerID)
	b, berr := readLoanBroker(ctx.View, brokerKey)
	if berr != nil || b == nil {
		return ter.TecINTERNAL
	}
	vinfo, verr := vault.ReadVaultInfo(ctx.View, keylet.VaultByID(b.VaultID))
	if verr != nil || vinfo == nil {
		return ter.TecINTERNAL
	}
	asset := vinfo.Asset
	amount := amountToLendNumForRules(l.Amount, ctx.Rules())

	b.CoverAvailable = numStr(lendNumForRules(b.CoverAvailable, ctx.Rules()).Sub(amount))
	associateBrokerAsset(b, assetIntegral(asset), ctx.Rules())
	if res := updateBroker(ctx, brokerKey, b); res != ter.TesSUCCESS {
		return res
	}

	// Ensure the destination can hold the asset when it is the submitter.
	if dstID == accountID {
		if _, res := vault.AddEmptyHolding(ctx, dstID, asset, ctx.PriorBalance()); res != ter.TesSUCCESS && res != ter.TecDUPLICATE {
			return res
		}
	}
	return vault.SendAsset(ctx, b.Account, dstID, asset, amount)
}

// -------------------- LoanBrokerCoverClawback --------------------

// determineBrokerID resolves the LoanBroker ID either from the field or from the
// Amount's issuer (a broker pseudo-account).
func (l *LoanBrokerCoverClawback) determineBrokerID(view tx.LedgerView) ([32]byte, ter.Result) {
	if l.LoanBrokerID != nil {
		id, ok := hashBytes(*l.LoanBrokerID)
		if !ok {
			return id, ter.TemMALFORMED
		}
		return id, ter.TesSUCCESS
	}
	if l.Amount == nil || l.Amount.IsMPT() || l.Amount.IsNative() {
		return [32]byte{}, ter.TecINTERNAL
	}
	issuerID, err := state.DecodeAccountID(l.Amount.Issuer)
	if err != nil {
		return [32]byte{}, ter.TefINTERNAL
	}
	ar, aerr := tx.ReadAccountRoot(view, issuerID)
	if aerr != nil {
		return [32]byte{}, ter.TefINTERNAL
	}
	if ar == nil {
		return [32]byte{}, ter.TecNO_ENTRY
	}
	if !ar.HasLoanBrokerID() {
		return [32]byte{}, ter.TecOBJECT_NOT_FOUND
	}
	return ar.LoanBrokerID, ter.TesSUCCESS
}

// clawAmount computes the amount that may be clawed: capped at
// CoverAvailable minus the minimum required cover, and at the requested Amount
// when present.
func (l *LoanBrokerCoverClawback) clawAmount(b *loanBrokerData, vinfo *vault.VaultLending, rules *amendment.Rules) (lmath.N, ter.Result) {
	fix320 := rules != nil && rules.FixCleanup3_2_0Enabled()
	integral := assetIntegral(vinfo.Asset)
	debtTotal := lendNumForRules(b.DebtTotal, rules)
	var minRequired lmath.N
	if fix320 {
		minRequired = minimumBrokerCover(debtTotal, b.CoverRateMinimum, vaultScaleOfForRules(vinfo, integral, rules), integral)
	} else {
		minRequired = brokerCoverRate(debtTotal, b.CoverRateMinimum)
	}
	maxClaw := lendNumForRules(b.CoverAvailable, rules).AddRounded(minRequired.Negate(), state.RoundDownward)
	if maxClaw.Signum() <= 0 {
		return lmath.NumScaled(0, 0, lendingNumberScale(rules)), ter.TecINSUFFICIENT_FUNDS
	}
	if l.Amount == nil || l.Amount.IsZero() {
		return maxClaw, ter.TesSUCCESS
	}
	req := amountToLendNumForRules(*l.Amount, rules)
	if req.Cmp(maxClaw) > 0 {
		return maxClaw, ter.TesSUCCESS
	}
	return req, ter.TesSUCCESS
}

func (l *LoanBrokerCoverClawback) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	accountID, err := state.DecodeAccountID(l.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	brokerID, res := l.determineBrokerID(view)
	if res != ter.TesSUCCESS {
		return res
	}
	b, berr := readLoanBroker(view, keylet.LoanBrokerByID(brokerID))
	if berr != nil {
		return ter.TefINTERNAL
	}
	if b == nil {
		return ter.TecNO_ENTRY
	}
	vinfo, verr := vault.ReadVaultLending(view, keylet.VaultByID(b.VaultID))
	if verr != nil || vinfo == nil {
		return ter.TefBAD_LEDGER
	}
	asset := vinfo.Asset
	if asset.IsNative() {
		return ter.TecNO_PERMISSION
	}
	var mptIssuanceID [24]byte
	if asset.IsMPT() {
		mptIssuanceID, err = mptutil.DecodeID(asset.MPTIssuanceID)
		if err != nil {
			return ter.TefINTERNAL
		}
		if mptutil.Issuer(mptIssuanceID) != accountID {
			return ter.TecNO_PERMISSION
		}
	} else if asset.Issuer != l.Account {
		return ter.TecNO_PERMISSION
	}
	if l.Amount != nil {
		if l.Amount.IsMPT() {
			if !amountAssetMatches(*l.Amount, asset) {
				return ter.TecWRONG_ASSET
			}
		} else {
			// The Amount's issuer may be the vault asset issuer (the submitter) or
			// the broker pseudo-account, which normalizes to the vault asset.
			pseudoAddr, _ := state.EncodeAccountID(b.Account)
			if l.Amount.Currency != asset.Currency ||
				(l.Amount.Issuer != asset.Issuer && l.Amount.Issuer != pseudoAddr) {
				return ter.TecWRONG_ASSET
			}
		}
	}
	fix320 := config.RequireRules().FixCleanup3_2_0Enabled()
	claw, cres := l.clawAmount(b, vinfo, config.RequireRules())
	if cres != ter.TesSUCCESS {
		return cres
	}
	if res := canApplyToBrokerCover(fix320, lendNumForRules(b.CoverAvailable, config.RequireRules()), claw, assetIntegral(asset)); res != ter.TesSUCCESS {
		return res
	}
	holds, herr := vault.AccountHoldsFull(view, config, b.Account, asset)
	if herr != nil {
		return ter.TefINTERNAL
	}
	if toLargeForRules(holds, config.RequireRules()).Cmp(claw) < 0 {
		return ter.TecINTERNAL
	}
	if asset.IsMPT() {
		issuance, _, result := mptutil.ReadIssuance(view, mptIssuanceID)
		if result != ter.TesSUCCESS {
			return result
		}
		if issuance.Flags&entry.LsfMPTCanClawback == 0 {
			return ter.TecNO_PERMISSION
		}
		if issuance.Issuer != accountID {
			return ter.TecINTERNAL
		}
		return ter.TesSUCCESS
	}
	issuerID, _ := state.DecodeAccountID(asset.Issuer)
	iar, ierr := tx.ReadAccountRoot(view, issuerID)
	if ierr != nil || iar == nil {
		return ter.TefBAD_LEDGER
	}
	if iar.Flags&state.LsfAllowTrustLineClawback == 0 || iar.Flags&state.LsfNoFreeze != 0 {
		return ter.TecNO_PERMISSION
	}
	return ter.TesSUCCESS
}

func (l *LoanBrokerCoverClawback) Apply(ctx *tx.ApplyContext) ter.Result {
	accountID := ctx.AccountID
	brokerID, res := l.determineBrokerID(ctx.View)
	if res != ter.TesSUCCESS {
		return ter.TecINTERNAL
	}
	brokerKey := keylet.LoanBrokerByID(brokerID)
	b, berr := readLoanBroker(ctx.View, brokerKey)
	if berr != nil || b == nil {
		return ter.TecINTERNAL
	}
	vinfo, verr := vault.ReadVaultLending(ctx.View, keylet.VaultByID(b.VaultID))
	if verr != nil || vinfo == nil {
		return ter.TecINTERNAL
	}
	asset := vinfo.Asset
	claw, cres := l.clawAmount(b, vinfo, ctx.Rules())
	if cres != ter.TesSUCCESS {
		return ter.TecINTERNAL
	}
	b.CoverAvailable = numStr(lendNumForRules(b.CoverAvailable, ctx.Rules()).Sub(claw))
	associateBrokerAsset(b, assetIntegral(asset), ctx.Rules())
	if res := updateBroker(ctx, brokerKey, b); res != ter.TesSUCCESS {
		return res
	}
	return vault.SendAsset(ctx, b.Account, accountID, asset, claw)
}

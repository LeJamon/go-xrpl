package lending

import (
	"strings"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
)

// toLarge re-expresses a spendable-balance Number in the large lending scale so
// it compares directly against cover amounts.
func toLarge(n state.XRPLNumber) lmath.N { return lmath.Num(n.Mantissa(), n.Exponent()) }

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

func (l *LoanBrokerSet) Preclaim(view tx.LedgerView, _ tx.EngineConfig) ter.Result {
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
			debtMax := lendNum(*l.DebtMaximum)
			if debtMax.Signum() != 0 && debtMax.Cmp(lendNum(b.DebtTotal)) < 0 {
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

	if l.DebtMaximum != nil && !representableAsAsset(lendNum(*l.DebtMaximum), asset) {
		return ter.TecPRECISION_LOSS
	}
	return ter.TesSUCCESS
}

func (l *LoanBrokerSet) Apply(ctx *tx.ApplyContext) ter.Result {
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
			b.DebtMaximum = numStr(lendNum(*l.DebtMaximum))
		}
		associateBrokerAsset(b, integral)
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
	lineDelta, res := vault.AddEmptyHolding(ctx, pseudoID, asset)
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
		b.DebtMaximum = numStr(lendNum(*l.DebtMaximum))
	}
	if l.CoverRateMinimum != nil {
		b.CoverRateMinimum = *l.CoverRateMinimum
	}
	if l.CoverRateLiquidation != nil {
		b.CoverRateLiquidation = *l.CoverRateLiquidation
	}
	associateBrokerAsset(b, integral)

	data, serr := serializeLoanBroker(b)
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
	data, serr := serializeLoanBroker(b)
	if serr != nil {
		return ter.TefINTERNAL
	}
	if uerr := ctx.View.Update(brokerKey, data); uerr != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

// -------------------- LoanBrokerDelete --------------------

func (l *LoanBrokerDelete) Preclaim(view tx.LedgerView, _ tx.EngineConfig) ter.Result {
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
	if lendNum(b.DebtTotal).Signum() != 0 {
		return ter.TecHAS_OBLIGATIONS
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
	cover := lendNum(b.CoverAvailable)
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
	pseudo, perr := tx.ReadAccountRoot(ctx.View, b.Account)
	if perr != nil || pseudo == nil {
		return ter.TefBAD_LEDGER
	}
	pseudo.OwnerCount = uint32(int32(pseudo.OwnerCount) + assetDelta)
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
	if res := tx.AssetFrozen(view, accountID, asset); res != ter.TesSUCCESS {
		return res
	}
	if res := tx.AssetFrozen(view, b.Account, asset); res != ter.TesSUCCESS {
		return res
	}
	if res := tx.RequireAuth(view, asset, accountID); res != ter.TesSUCCESS {
		return res
	}
	amount, cres := roundCoverDeposit(config.RequireRules().FixCleanup3_2_0Enabled(),
		lendNum(b.CoverAvailable), amountToLendNum(l.Amount), assetIntegral(asset))
	if cres != ter.TesSUCCESS {
		return cres
	}
	holds, herr := vault.AccountHoldsFull(view, config, accountID, asset)
	if herr != nil {
		return ter.TefINTERNAL
	}
	if toLarge(holds).Cmp(amount) < 0 {
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
		lendNum(b.CoverAvailable), amountToLendNum(l.Amount), integral)
	if cres != ter.TesSUCCESS {
		return ter.TecINTERNAL
	}

	if res := vault.SendAsset(ctx, accountID, b.Account, asset, amount); res != ter.TesSUCCESS {
		return res
	}
	b.CoverAvailable = numStr(lendNum(b.CoverAvailable).Add(amount))
	associateBrokerAsset(b, integral)
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
	if res := canApplyToBrokerCover(fix320, lendNum(b.CoverAvailable), amountToLendNum(l.Amount), integral); res != ter.TesSUCCESS {
		return res
	}
	if accountID != dstID {
		if res := vault.CanWithdraw(view, accountID, dstID, l.Amount, l.DestinationTag != nil); res != ter.TesSUCCESS {
			return res
		}
	}
	if res := tx.RequireAuth(view, asset, dstID); res != ter.TesSUCCESS {
		return res
	}

	amount := amountToLendNum(l.Amount)
	coverAvail := lendNum(b.CoverAvailable)
	debtTotal := lendNum(b.DebtTotal)
	var minimumCover lmath.N
	if fix320 {
		minimumCover = minimumBrokerCover(debtTotal, b.CoverRateMinimum, vaultScaleOf(vinfo, integral), integral)
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
	if toLarge(holds).Cmp(amount) < 0 {
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
	amount := amountToLendNum(l.Amount)

	b.CoverAvailable = numStr(lendNum(b.CoverAvailable).Sub(amount))
	associateBrokerAsset(b, assetIntegral(asset))
	if res := updateBroker(ctx, brokerKey, b); res != ter.TesSUCCESS {
		return res
	}

	// Ensure the destination can hold the asset when it is the submitter.
	if dstID == accountID {
		if _, res := vault.AddEmptyHolding(ctx, dstID, asset); res != ter.TesSUCCESS && res != ter.TecDUPLICATE {
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
func (l *LoanBrokerCoverClawback) clawAmount(b *loanBrokerData, vinfo *vault.VaultLending, fix320 bool) (lmath.N, ter.Result) {
	integral := assetIntegral(vinfo.Asset)
	debtTotal := lendNum(b.DebtTotal)
	var minRequired lmath.N
	if fix320 {
		minRequired = minimumBrokerCover(debtTotal, b.CoverRateMinimum, vaultScaleOf(vinfo, integral), integral)
	} else {
		minRequired = brokerCoverRate(debtTotal, b.CoverRateMinimum)
	}
	maxClaw := lendNum(b.CoverAvailable).AddRounded(minRequired.Negate(), state.RoundDownward)
	if maxClaw.Signum() <= 0 {
		return lmath.Zero(), ter.TecINSUFFICIENT_FUNDS
	}
	if l.Amount == nil || l.Amount.IsZero() {
		return maxClaw, ter.TesSUCCESS
	}
	req := amountToLendNum(*l.Amount)
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
	if asset.Issuer != l.Account {
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
	claw, cres := l.clawAmount(b, vinfo, fix320)
	if cres != ter.TesSUCCESS {
		return cres
	}
	if res := canApplyToBrokerCover(fix320, lendNum(b.CoverAvailable), claw, assetIntegral(asset)); res != ter.TesSUCCESS {
		return res
	}
	// Only IOU issuers with clawback enabled and no global freeze may claw.
	issuerID, _ := state.DecodeAccountID(asset.Issuer)
	iar, ierr := tx.ReadAccountRoot(view, issuerID)
	if ierr != nil || iar == nil {
		return ter.TefBAD_LEDGER
	}
	if iar.Flags&state.LsfAllowTrustLineClawback == 0 || iar.Flags&state.LsfNoFreeze != 0 {
		return ter.TecNO_PERMISSION
	}
	_ = accountID
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
	claw, cres := l.clawAmount(b, vinfo, ctx.Rules().FixCleanup3_2_0Enabled())
	if cres != ter.TesSUCCESS {
		return ter.TecINTERNAL
	}
	b.CoverAvailable = numStr(lendNum(b.CoverAvailable).Sub(claw))
	associateBrokerAsset(b, assetIntegral(asset))
	if res := updateBroker(ctx, brokerKey, b); res != ter.TesSUCCESS {
		return res
	}
	return vault.SendAsset(ctx, b.Account, accountID, asset, claw)
}

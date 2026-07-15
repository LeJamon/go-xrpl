package lending

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
)

// hasExpired reports whether exp has passed at the given close time.
func hasExpired(now, exp uint32) bool { return exp != 0 && now >= exp }

// updateLoan serializes and updates a loan entry.
func updateLoan(ctx *tx.ApplyContext, loanKey keylet.Keylet, l *loanData) ter.Result {
	data, serr := serializeLoanForRules(l, ctx.Rules())
	if serr != nil {
		return ter.TefINTERNAL
	}
	if uerr := ctx.View.Update(loanKey, data); uerr != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

func owedToVaultForRules(l *loanData, rules *amendment.Rules) lmath.N {
	return lendNumForRules(l.TotalValueOutstanding, rules).Sub(lendNumForRules(l.ManagementFeeOutstanding, rules))
}

func vaultScaleOfForRules(v *vault.VaultLending, integral bool, rules *amendment.Rules) int {
	return lendNumForRules(v.AssetsTotal, rules).AssetExponent(integral, state.RoundToNearest)
}

// -------------------- LoanDelete --------------------

func (l *LoanDelete) Preclaim(view tx.LedgerView, _ tx.EngineConfig) ter.Result {
	accountID, err := state.DecodeAccountID(l.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	loanID, ok := hashBytes(l.LoanID)
	if !ok {
		return ter.TemMALFORMED
	}
	loan, lerr := readLoan(view, keylet.LoanByID(loanID))
	if lerr != nil {
		return ter.TefINTERNAL
	}
	if loan == nil {
		return ter.TecNO_ENTRY
	}
	if loan.PaymentRemaining > 0 {
		return ter.TecHAS_OBLIGATIONS
	}
	b, berr := readLoanBroker(view, keylet.LoanBrokerByID(loan.LoanBrokerID))
	if berr != nil || b == nil {
		return ter.TecINTERNAL
	}
	if b.Owner != accountID && loan.Borrower != accountID {
		return ter.TecNO_PERMISSION
	}
	return ter.TesSUCCESS
}

func (l *LoanDelete) Apply(ctx *tx.ApplyContext) ter.Result {
	loanID, ok := hashBytes(l.LoanID)
	if !ok {
		return ter.TefINTERNAL
	}
	loanKey := keylet.LoanByID(loanID)
	loan, lerr := readLoan(ctx.View, loanKey)
	if lerr != nil || loan == nil {
		return ter.TefBAD_LEDGER
	}
	brokerKey := keylet.LoanBrokerByID(loan.LoanBrokerID)
	b, berr := readLoanBroker(ctx.View, brokerKey)
	if berr != nil || b == nil {
		return ter.TefBAD_LEDGER
	}
	vinfo, verr := vault.ReadVaultLending(ctx.View, keylet.VaultByID(b.VaultID))
	if verr != nil || vinfo == nil {
		return ter.TefBAD_LEDGER
	}
	integral := assetIntegral(vinfo.Asset)

	if r, e := state.DirRemove(ctx.View, keylet.OwnerDir(b.Account), loan.LoanBrokerNode, loanKey.Key, false); e != nil || !r.Success {
		return ter.TefBAD_LEDGER
	}
	if r, e := state.DirRemove(ctx.View, keylet.OwnerDir(loan.Borrower), loan.OwnerNode, loanKey.Key, false); e != nil || !r.Success {
		return ter.TefBAD_LEDGER
	}
	if e := ctx.View.Erase(loanKey); e != nil {
		return ter.TefINTERNAL
	}

	// Decrement the broker's outstanding-loan count; forgive residual dust debt
	// when the last loan is removed.
	if b.OwnerCount > 0 {
		b.OwnerCount--
	}
	if b.OwnerCount == 0 && lendNumForRules(b.DebtTotal, ctx.Rules()).Signum() != 0 {
		b.DebtTotal = ""
	}
	associateBrokerAsset(b, integral, ctx.Rules())
	if res := updateBroker(ctx, brokerKey, b); res != ter.TesSUCCESS {
		return res
	}

	// Decrement the borrower's owner count.
	if loan.Borrower == ctx.AccountID {
		if ctx.Account.OwnerCount > 0 {
			ctx.Account.OwnerCount--
		}
	} else if e := tx.AdjustOwnerCount(ctx.View, loan.Borrower, -1); e != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

// -------------------- LoanManage --------------------

func (l *LoanManage) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	accountID, err := state.DecodeAccountID(l.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	loanID, ok := hashBytes(l.LoanID)
	if !ok {
		return ter.TemMALFORMED
	}
	loan, lerr := readLoan(view, keylet.LoanByID(loanID))
	if lerr != nil {
		return ter.TefINTERNAL
	}
	if loan == nil {
		return ter.TecNO_ENTRY
	}
	flags := l.GetFlags()
	impaired := loan.Flags&LsfLoanImpaired != 0
	defaulted := loan.Flags&LsfLoanDefault != 0

	if defaulted {
		return ter.TecNO_PERMISSION
	}
	if impaired && flags&TfLoanImpair != 0 {
		return ter.TecNO_PERMISSION
	}
	if !impaired && !defaulted && flags&TfLoanUnimpair != 0 {
		return ter.TecNO_PERMISSION
	}
	if loan.PaymentRemaining == 0 {
		return ter.TecNO_PERMISSION
	}
	if flags&TfLoanDefault != 0 && !hasExpired(config.ParentCloseTime, loan.NextPaymentDueDate+loan.GracePeriod) {
		return ter.TecTOO_SOON
	}
	b, berr := readLoanBroker(view, keylet.LoanBrokerByID(loan.LoanBrokerID))
	if berr != nil || b == nil {
		return ter.TecINTERNAL
	}
	if b.Owner != accountID {
		return ter.TecNO_PERMISSION
	}
	return ter.TesSUCCESS
}

func (l *LoanManage) Apply(ctx *tx.ApplyContext) ter.Result {
	loanID, ok := hashBytes(l.LoanID)
	if !ok {
		return ter.TefINTERNAL
	}
	loanKey := keylet.LoanByID(loanID)
	loan, lerr := readLoan(ctx.View, loanKey)
	if lerr != nil || loan == nil {
		return ter.TefBAD_LEDGER
	}
	brokerKey := keylet.LoanBrokerByID(loan.LoanBrokerID)
	b, berr := readLoanBroker(ctx.View, brokerKey)
	if berr != nil || b == nil {
		return ter.TefBAD_LEDGER
	}
	vaultKey := keylet.VaultByID(b.VaultID)
	vinfo, verr := vault.ReadVaultLending(ctx.View, vaultKey)
	if verr != nil || vinfo == nil {
		return ter.TefBAD_LEDGER
	}

	flags := l.GetFlags()
	integral := assetIntegral(vinfo.Asset)
	var result ter.Result
	switch {
	case flags&TfLoanDefault != 0:
		result = l.defaultLoan(ctx, loanKey, loan, brokerKey, b, vaultKey, vinfo)
	case flags&TfLoanImpair != 0:
		result = l.impairLoan(ctx, loanKey, loan, vaultKey, vinfo)
	case flags&TfLoanUnimpair != 0:
		result = l.unimpairLoan(ctx, loanKey, loan, vaultKey, vinfo)
	default:
		result = ter.TesSUCCESS
	}

	// Post-fixCleanup3_1_3: round the NUMBER fields of the Loan, LoanBroker, and
	// Vault to the asset precision on every successful path. Pre-amendment only
	// the noop path did so.
	if result == ter.TesSUCCESS && ctx.Rules().Enabled(amendment.FeatureFixCleanup3_1_3) {
		if r := l.associateEntities(ctx, loanKey, brokerKey, vaultKey, integral); r != ter.TesSUCCESS {
			return r
		}
	}
	return result
}

// associateEntities rounds the NUMBER fields of the Loan, LoanBroker, and Vault
// to the vault asset's precision after a successful default/impair/unimpair, so
// no accounting field carries sub-precision dust (rippled associateAsset).
func (l *LoanManage) associateEntities(ctx *tx.ApplyContext, loanKey, brokerKey, vaultKey keylet.Keylet, integral bool) ter.Result {
	loan, err := readLoan(ctx.View, loanKey)
	if err != nil || loan == nil {
		return ter.TefBAD_LEDGER
	}
	associateLoanAsset(loan, integral, ctx.Rules())
	if r := updateLoan(ctx, loanKey, loan); r != ter.TesSUCCESS {
		return r
	}
	b, berr := readLoanBroker(ctx.View, brokerKey)
	if berr != nil || b == nil {
		return ter.TefBAD_LEDGER
	}
	associateBrokerAsset(b, integral, ctx.Rules())
	if r := updateBroker(ctx, brokerKey, b); r != ter.TesSUCCESS {
		return r
	}
	v, verr := vault.ReadVaultLending(ctx.View, vaultKey)
	if verr != nil || v == nil {
		return ter.TefBAD_LEDGER
	}
	associateVaultAsset(v, integral, ctx.Rules())
	return vault.UpdateVaultTotals(ctx, vaultKey, v.AssetsTotal, v.AssetsAvailable, v.LossUnrealized)
}

func (l *LoanManage) impairLoan(ctx *tx.ApplyContext, loanKey keylet.Keylet, loan *loanData, vaultKey keylet.Keylet, v *vault.VaultLending) ter.Result {
	asset := mathAsset(v.Asset)
	integral := asset.Integral
	scale := vaultScaleOfForRules(v, integral, ctx.Rules())
	loss := owedToVaultForRules(loan, ctx.Rules())
	newLoss := lmath.AdjustImprecise(asset, lendNumForRules(v.LossUnrealized, ctx.Rules()), loss, scale)
	// Loss cannot exceed the vault's committed-but-unavailable assets.
	committed := lendNumForRules(v.AssetsTotal, ctx.Rules()).Sub(lendNumForRules(v.AssetsAvailable, ctx.Rules()))
	if newLoss.Cmp(committed) > 0 {
		return ter.TecLIMIT_EXCEEDED
	}
	if res := vault.UpdateVaultTotals(ctx, vaultKey, v.AssetsTotal, v.AssetsAvailable, numStr(newLoss)); res != ter.TesSUCCESS {
		return res
	}
	loan.Flags |= LsfLoanImpaired
	if !hasExpired(ctx.Config.ParentCloseTime, loan.NextPaymentDueDate) {
		loan.NextPaymentDueDate = ctx.Config.ParentCloseTime
	}
	return updateLoan(ctx, loanKey, loan)
}

func (l *LoanManage) unimpairLoan(ctx *tx.ApplyContext, loanKey keylet.Keylet, loan *loanData, vaultKey keylet.Keylet, v *vault.VaultLending) ter.Result {
	asset := mathAsset(v.Asset)
	integral := asset.Integral
	scale := vaultScaleOfForRules(v, integral, ctx.Rules())
	loss := owedToVaultForRules(loan, ctx.Rules())
	if lendNumForRules(v.LossUnrealized, ctx.Rules()).Cmp(loss) < 0 {
		return ter.TefBAD_LEDGER
	}
	newLoss := lmath.AdjustImprecise(asset, lendNumForRules(v.LossUnrealized, ctx.Rules()), loss.Negate(), scale)
	if res := vault.UpdateVaultTotals(ctx, vaultKey, v.AssetsTotal, v.AssetsAvailable, numStr(newLoss)); res != ter.TesSUCCESS {
		return res
	}
	loan.Flags &^= LsfLoanImpaired
	prev := loan.PreviousPaymentDueDate
	if loan.StartDate > prev {
		prev = loan.StartDate
	}
	normalDue := prev + loan.PaymentInterval
	if !hasExpired(ctx.Config.ParentCloseTime, normalDue) {
		loan.NextPaymentDueDate = normalDue
	} else {
		loan.NextPaymentDueDate = ctx.Config.ParentCloseTime + loan.PaymentInterval
	}
	return updateLoan(ctx, loanKey, loan)
}

func (l *LoanManage) defaultLoan(ctx *tx.ApplyContext, loanKey keylet.Keylet, loan *loanData, brokerKey keylet.Keylet, b *loanBrokerData, vaultKey keylet.Keylet, v *vault.VaultLending) ter.Result {
	asset := mathAsset(v.Asset)
	integral := asset.Integral
	loanScale := int(loan.LoanScale)
	vaultScale := vaultScaleOfForRules(v, integral, ctx.Rules())
	debtTotal := lendNumForRules(b.DebtTotal, ctx.Rules())
	totalDefault := owedToVaultForRules(loan, ctx.Rules())

	// Liquidation cover: min(debtTotal * coverMin * coverLiq, totalDefault),
	// capped at the broker's available cover.
	minimumCover := lmath.TenthBipsOfValue(debtTotal, b.CoverRateMinimum)
	liq := lmath.TenthBipsOfValue(minimumCover, b.CoverRateLiquidation)
	if liq.Cmp(totalDefault) > 0 {
		liq = totalDefault
	}
	covered := lmath.RoundAssetNearest(asset, liq, loanScale)
	if covered.Cmp(lendNumForRules(b.CoverAvailable, ctx.Rules())) > 0 {
		covered = lendNumForRules(b.CoverAvailable, ctx.Rules())
	}
	vaultDefault := totalDefault.Sub(covered)

	// Vault accounting.
	if lendNumForRules(v.AssetsTotal, ctx.Rules()).Cmp(vaultDefault) < 0 {
		return ter.TefBAD_LEDGER
	}
	vaultDefaultRounded := lmath.RoundAssetDownward(asset, vaultDefault, vaultScale)
	newTotal := lendNumForRules(v.AssetsTotal, ctx.Rules()).Sub(vaultDefaultRounded)
	newAvailable := lendNumForRules(v.AssetsAvailable, ctx.Rules()).Add(covered)
	if newAvailable.Cmp(newTotal) > 0 {
		return ter.TecINTERNAL
	}
	newLoss := lendNumForRules(v.LossUnrealized, ctx.Rules())
	if loan.Flags&LsfLoanImpaired != 0 {
		if newLoss.Cmp(totalDefault) < 0 {
			return ter.TefBAD_LEDGER
		}
		newLoss = lmath.AdjustImprecise(asset, newLoss, totalDefault.Negate(), vaultScale)
	}
	if res := vault.UpdateVaultTotals(ctx, vaultKey, numStr(newTotal), numStr(newAvailable), numStr(newLoss)); res != ter.TesSUCCESS {
		return res
	}

	// Broker accounting.
	if lendNumForRules(b.CoverAvailable, ctx.Rules()).Cmp(covered) < 0 {
		return ter.TefBAD_LEDGER
	}
	b.DebtTotal = numStr(lmath.AdjustImprecise(asset, debtTotal, totalDefault.Negate(), vaultScale))
	b.CoverAvailable = numStr(lendNumForRules(b.CoverAvailable, ctx.Rules()).Sub(covered))
	if res := updateBroker(ctx, brokerKey, b); res != ter.TesSUCCESS {
		return res
	}

	// Loan is written off.
	loan.Flags |= LsfLoanDefault
	loan.TotalValueOutstanding = ""
	loan.PrincipalOutstanding = ""
	loan.ManagementFeeOutstanding = ""
	loan.PaymentRemaining = 0
	loan.NextPaymentDueDate = 0
	if res := updateLoan(ctx, loanKey, loan); res != ter.TesSUCCESS {
		return res
	}

	// Move the covered amount from the broker pseudo to the vault pseudo.
	if covered.Signum() > 0 {
		return vault.SendAsset(ctx, b.Account, v.Account, v.Asset, covered)
	}
	return ter.TesSUCCESS
}

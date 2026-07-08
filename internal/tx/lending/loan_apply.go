package lending

import (
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
	data, serr := serializeLoan(l)
	if serr != nil {
		return ter.TefINTERNAL
	}
	if uerr := ctx.View.Update(loanKey, data); uerr != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

// owedToVault is the amount owed to the vault (principal + interest, excluding
// the broker's management fee): TotalValueOutstanding - ManagementFeeOutstanding.
func owedToVault(l *loanData) lmath.N {
	return lendNum(l.TotalValueOutstanding).Sub(lendNum(l.ManagementFeeOutstanding))
}

// vaultScaleOf returns getAssetsTotalScale(vault): the exponent of the vault's
// asset total in its asset (0 for integral assets).
func vaultScaleOf(v *vault.VaultLending, integral bool) int {
	return lendNum(v.AssetsTotal).AssetExponent(integral, state.RoundToNearest)
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
	if b.OwnerCount == 0 && lendNum(b.DebtTotal).Signum() != 0 {
		b.DebtTotal = ""
	}
	associateBrokerAsset(b, integral)
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
	switch {
	case flags&TfLoanDefault != 0:
		return l.defaultLoan(ctx, loanKey, loan, brokerKey, b, vaultKey, vinfo)
	case flags&TfLoanImpair != 0:
		return l.impairLoan(ctx, loanKey, loan, vaultKey, vinfo)
	case flags&TfLoanUnimpair != 0:
		return l.unimpairLoan(ctx, loanKey, loan, vaultKey, vinfo)
	default:
		return ter.TesSUCCESS
	}
}

func (l *LoanManage) impairLoan(ctx *tx.ApplyContext, loanKey keylet.Keylet, loan *loanData, vaultKey keylet.Keylet, v *vault.VaultLending) ter.Result {
	asset := mathAsset(v.Asset)
	integral := asset.Integral
	scale := vaultScaleOf(v, integral)
	loss := owedToVault(loan)
	newLoss := lmath.AdjustImprecise(asset, lendNum(v.LossUnrealized), loss, scale)
	// Loss cannot exceed the vault's committed-but-unavailable assets.
	committed := lendNum(v.AssetsTotal).Sub(lendNum(v.AssetsAvailable))
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
	associateLoanAsset(loan, integral)
	return updateLoan(ctx, loanKey, loan)
}

func (l *LoanManage) unimpairLoan(ctx *tx.ApplyContext, loanKey keylet.Keylet, loan *loanData, vaultKey keylet.Keylet, v *vault.VaultLending) ter.Result {
	asset := mathAsset(v.Asset)
	integral := asset.Integral
	scale := vaultScaleOf(v, integral)
	loss := owedToVault(loan)
	if lendNum(v.LossUnrealized).Cmp(loss) < 0 {
		return ter.TefBAD_LEDGER
	}
	newLoss := lmath.AdjustImprecise(asset, lendNum(v.LossUnrealized), loss.Negate(), scale)
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
	associateLoanAsset(loan, integral)
	return updateLoan(ctx, loanKey, loan)
}

func (l *LoanManage) defaultLoan(ctx *tx.ApplyContext, loanKey keylet.Keylet, loan *loanData, brokerKey keylet.Keylet, b *loanBrokerData, vaultKey keylet.Keylet, v *vault.VaultLending) ter.Result {
	asset := mathAsset(v.Asset)
	integral := asset.Integral
	loanScale := int(loan.LoanScale)
	vaultScale := vaultScaleOf(v, integral)
	debtTotal := lendNum(b.DebtTotal)
	totalDefault := owedToVault(loan)

	// Liquidation cover: min(debtTotal * coverMin * coverLiq, totalDefault),
	// capped at the broker's available cover.
	minimumCover := lmath.TenthBipsOfValue(debtTotal, b.CoverRateMinimum)
	liq := lmath.TenthBipsOfValue(minimumCover, b.CoverRateLiquidation)
	if liq.Cmp(totalDefault) > 0 {
		liq = totalDefault
	}
	covered := lmath.RoundAssetNearest(asset, liq, loanScale)
	if covered.Cmp(lendNum(b.CoverAvailable)) > 0 {
		covered = lendNum(b.CoverAvailable)
	}
	vaultDefault := totalDefault.Sub(covered)

	// Vault accounting.
	if lendNum(v.AssetsTotal).Cmp(vaultDefault) < 0 {
		return ter.TefBAD_LEDGER
	}
	vaultDefaultRounded := lmath.RoundAssetDownward(asset, vaultDefault, vaultScale)
	newTotal := lendNum(v.AssetsTotal).Sub(vaultDefaultRounded)
	newAvailable := lendNum(v.AssetsAvailable).Add(covered)
	if newAvailable.Cmp(newTotal) > 0 {
		return ter.TecINTERNAL
	}
	newLoss := lendNum(v.LossUnrealized)
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
	if lendNum(b.CoverAvailable).Cmp(covered) < 0 {
		return ter.TefBAD_LEDGER
	}
	b.DebtTotal = numStr(lmath.AdjustImprecise(asset, debtTotal, totalDefault.Negate(), vaultScale))
	b.CoverAvailable = numStr(lendNum(b.CoverAvailable).Sub(covered))
	associateBrokerAsset(b, integral)
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
	associateLoanAsset(loan, integral)
	if res := updateLoan(ctx, loanKey, loan); res != ter.TesSUCCESS {
		return res
	}

	// Move the covered amount from the broker pseudo to the vault pseudo.
	if covered.Signum() > 0 {
		return vault.SendAsset(ctx, b.Account, v.Account, v.Asset, covered)
	}
	return ter.TesSUCCESS
}

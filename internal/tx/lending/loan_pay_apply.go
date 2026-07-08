package lending

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

// paymentType maps the LoanPay flags to the amortization payment path.
func (l *LoanPay) paymentType() lmath.LoanPaymentType {
	switch {
	case l.GetFlags()&TfLoanLatePayment != 0:
		return lmath.PaymentLate
	case l.GetFlags()&TfLoanFullPayment != 0:
		return lmath.PaymentFull
	case l.GetFlags()&TfLoanOverpayment != 0:
		return lmath.PaymentOverpayment
	default:
		return lmath.PaymentRegular
	}
}

// loanToAccount marshals a Loan entry into the amortization LoanAccount.
func loanToAccount(loan *loanData) *lmath.LoanAccount {
	return &lmath.LoanAccount{
		PrincipalOutstanding:     lendNum(loan.PrincipalOutstanding),
		TotalValueOutstanding:    lendNum(loan.TotalValueOutstanding),
		ManagementFeeOutstanding: lendNum(loan.ManagementFeeOutstanding),
		PeriodicPayment:          lendNum(loan.PeriodicPayment),
		PaymentRemaining:         loan.PaymentRemaining,
		PrevPaymentDueDate:       loan.PreviousPaymentDueDate,
		NextPaymentDueDate:       loan.NextPaymentDueDate,
		StartDate:                loan.StartDate,
		PaymentInterval:          loan.PaymentInterval,
		LoanScale:                int(loan.LoanScale),
		InterestRate:             loan.InterestRate,
		LateInterestRate:         loan.LateInterestRate,
		CloseInterestRate:        loan.CloseInterestRate,
		OverpaymentInterestRate:  loan.OverpaymentInterestRate,
		OverpaymentFee:           loan.OverpaymentFee,
		LoanServiceFee:           lendNum(loan.LoanServiceFee),
		LatePaymentFee:           lendNum(loan.LatePaymentFee),
		ClosePaymentFee:          lendNum(loan.ClosePaymentFee),
		HasOverpaymentFlag:       loan.Flags&LsfLoanOverpayment != 0,
	}
}

// accountToLoan writes the mutated LoanAccount back onto the Loan entry.
func accountToLoan(loan *loanData, acc *lmath.LoanAccount) {
	loan.PrincipalOutstanding = numStr(acc.PrincipalOutstanding)
	loan.TotalValueOutstanding = numStr(acc.TotalValueOutstanding)
	loan.ManagementFeeOutstanding = numStr(acc.ManagementFeeOutstanding)
	loan.PeriodicPayment = numStr(acc.PeriodicPayment)
	loan.PaymentRemaining = acc.PaymentRemaining
	loan.PreviousPaymentDueDate = acc.PrevPaymentDueDate
	loan.NextPaymentDueDate = acc.NextPaymentDueDate
}

// CalculateBaseFee estimates the number of combined payments and charges one base
// fee per loanPaymentsPerFeeIncrement (5). There is intentionally no 100-payment
// cap here (rippled documents that a LoanPay may be over-charged).
func (l *LoanPay) CalculateBaseFee(view tx.LedgerView, config tx.EngineConfig) uint64 {
	normal := config.BaseFee
	if l.GetFlags()&(TfLoanFullPayment|TfLoanLatePayment) != 0 {
		return normal
	}
	loanID, ok := hashBytes(l.LoanID)
	if !ok {
		return normal
	}
	loan, err := readLoan(view, keylet.LoanByID(loanID))
	if err != nil || loan == nil {
		return normal
	}
	if loan.PaymentRemaining <= protocol.LoanPaymentsPerFeeIncrement {
		return normal
	}
	if hasExpired(config.ParentCloseTime, loan.NextPaymentDueDate) {
		return normal
	}
	b, berr := readLoanBroker(view, keylet.LoanBrokerByID(loan.LoanBrokerID))
	if berr != nil || b == nil {
		return normal
	}
	vinfo, verr := vault.ReadVaultInfo(view, keylet.VaultByID(b.VaultID))
	if verr != nil || vinfo == nil {
		return normal
	}
	if !amountAssetMatches(l.Amount, vinfo.Asset) {
		return normal
	}
	mAsset := mathAsset(vinfo.Asset)
	scale := int(loan.LoanScale)
	regular := lmath.RoundAssetUpward(mAsset, lendNum(loan.PeriodicPayment), scale).Add(lendNum(loan.LoanServiceFee))
	if regular.Signum() <= 0 {
		return normal
	}
	mode := state.RoundDownward
	if l.GetFlags()&TfLoanOverpayment != 0 {
		mode = state.RoundUpward
	}
	est := amountToLendNum(l.Amount).DivRounded(regular, mode).ToInt64WithMode(state.RoundTowardsZero)
	if est < 0 {
		est = 0
	}
	feeIncrements := (est + protocol.LoanPaymentsPerFeeIncrement - 1) / protocol.LoanPaymentsPerFeeIncrement
	if feeIncrements < 1 {
		feeIncrements = 1
	}
	return uint64(feeIncrements) * normal
}

func (l *LoanPay) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
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
	if loan.Borrower != accountID {
		return ter.TecNO_PERMISSION
	}
	if l.GetFlags()&TfLoanOverpayment != 0 && loan.Flags&LsfLoanOverpayment == 0 {
		return ter.TemINVALID_FLAG
	}
	if loan.PaymentRemaining == 0 || lendNum(loan.PrincipalOutstanding).IsZero() {
		return ter.TecKILLED
	}
	b, berr := readLoanBroker(view, keylet.LoanBrokerByID(loan.LoanBrokerID))
	if berr != nil || b == nil {
		return ter.TefBAD_LEDGER
	}
	vinfo, verr := vault.ReadVaultInfo(view, keylet.VaultByID(b.VaultID))
	if verr != nil || vinfo == nil {
		return ter.TefBAD_LEDGER
	}
	asset := vinfo.Asset
	if !amountAssetMatches(l.Amount, asset) {
		return ter.TecWRONG_ASSET
	}
	if r := vault.AssetFrozen(view, accountID, asset); r != ter.TesSUCCESS {
		return r
	}
	if r := tx.RequireAuth(view, asset, accountID); r != ter.TesSUCCESS {
		return r
	}
	holds, herr := vault.AccountHoldsFull(view, config, accountID, asset)
	if herr != nil {
		return ter.TefINTERNAL
	}
	if toLarge(holds).Cmp(amountToLendNum(l.Amount)) < 0 {
		return ter.TecINSUFFICIENT_FUNDS
	}
	return ter.TesSUCCESS
}

func (l *LoanPay) Apply(ctx *tx.ApplyContext) ter.Result {
	accountID := ctx.AccountID
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
	asset := vinfo.Asset
	mAsset := mathAsset(asset)
	integral := mAsset.Integral
	loanScale := int(loan.LoanScale)

	// Where the broker's fee goes: to the owner while cover is above the minimum,
	// otherwise back into the first-loss cover pool.
	minCover := lmath.RoundAssetUpward(mAsset, lmath.TenthBipsOfValue(lendNum(b.DebtTotal), b.CoverRateMinimum), loanScale)
	sendFeeToOwner := lendNum(b.CoverAvailable).Cmp(minCover) >= 0 &&
		vault.AssetFrozen(ctx.View, b.Owner, asset) == ter.TesSUCCESS &&
		tx.RequireAuth(ctx.View, asset, b.Owner) == ter.TesSUCCESS
	brokerPayee := b.Account
	if sendFeeToOwner {
		brokerPayee = b.Owner
	}

	// Reverse any impairment before applying the payment.
	if loan.Flags&LsfLoanImpaired != 0 {
		if r := reverseImpairment(ctx, loan, vaultKey, vinfo, integral); r != ter.TesSUCCESS {
			return r
		}
		// Re-read the vault totals after the loss reversal.
		vinfo, verr = vault.ReadVaultLending(ctx.View, vaultKey)
		if verr != nil || vinfo == nil {
			return ter.TefBAD_LEDGER
		}
	}

	acc := loanToAccount(loan)
	parts, t := lmath.LoanMakePayment(mAsset, ctx.Config.ParentCloseTime, acc, uint32(b.ManagementFeeRate), amountToLendNum(l.Amount), l.paymentType())
	if t != ter.TesSUCCESS {
		return t
	}
	accountToLoan(loan, acc)
	associateLoanAsset(loan, integral)
	if r := updateLoan(ctx, loanKey, loan); r != ter.TesSUCCESS {
		return r
	}

	// Vault + broker accounting.
	vaultScale := vaultScaleOf(vinfo, integral)
	rawToVault := parts.PrincipalPaid.Add(parts.InterestPaid)
	toVaultRounded := lmath.RoundAssetDownward(mAsset, rawToVault, vaultScale)
	toVaultForDebt := rawToVault.Sub(parts.ValueChange)
	toBroker := parts.FeePaid

	b.DebtTotal = numStr(lmath.AdjustImprecise(mAsset, lendNum(b.DebtTotal), toVaultForDebt.Negate(), vaultScale))
	if !sendFeeToOwner {
		b.CoverAvailable = numStr(lendNum(b.CoverAvailable).Add(toBroker))
	}
	associateBrokerAsset(b, integral)
	if r := updateBroker(ctx, brokerKey, b); r != ter.TesSUCCESS {
		return r
	}

	newAvailable := lendNum(vinfo.AssetsAvailable).Add(toVaultRounded)
	newTotal := lendNum(vinfo.AssetsTotal).Add(parts.ValueChange)
	if newAvailable.Cmp(newTotal) > 0 {
		return ter.TecINTERNAL
	}
	if r := vault.UpdateVaultTotals(ctx, vaultKey, numStr(newTotal), numStr(newAvailable), vinfo.LossUnrealized); r != ter.TesSUCCESS {
		return r
	}

	// Auth + holdings for the receivers, then move the funds.
	if toVaultRounded.Signum() != 0 {
		if r := tx.RequireAuth(ctx.View, asset, vinfo.Account); r != ter.TesSUCCESS {
			return r
		}
		if r := vault.SendAsset(ctx, accountID, vinfo.Account, asset, toVaultRounded); r != ter.TesSUCCESS {
			return r
		}
	}
	if toBroker.Signum() != 0 {
		if brokerPayee == accountID {
			if _, r := vault.AddEmptyHolding(ctx, brokerPayee, asset); r != ter.TesSUCCESS && r != ter.TecDUPLICATE {
				return r
			}
		}
		if r := tx.RequireAuth(ctx.View, asset, brokerPayee); r != ter.TesSUCCESS {
			return r
		}
		if r := vault.SendAsset(ctx, accountID, brokerPayee, asset, toBroker); r != ter.TesSUCCESS {
			return r
		}
	}
	return ter.TesSUCCESS
}

// reverseImpairment clears an impaired loan's unrealized loss from the vault and
// clears the flag, restoring the normal payment schedule (rippled unimpairLoan,
// invoked at the top of LoanPay::doApply).
func reverseImpairment(ctx *tx.ApplyContext, loan *loanData, vaultKey keylet.Keylet, v *vault.VaultLending, integral bool) ter.Result {
	asset := lmath.Asset{Integral: integral}
	scale := vaultScaleOf(v, integral)
	loss := owedToVault(loan)
	if lendNum(v.LossUnrealized).Cmp(loss) < 0 {
		return ter.TefBAD_LEDGER
	}
	newLoss := lmath.AdjustImprecise(asset, lendNum(v.LossUnrealized), loss.Negate(), scale)
	if r := vault.UpdateVaultTotals(ctx, vaultKey, v.AssetsTotal, v.AssetsAvailable, numStr(newLoss)); r != ter.TesSUCCESS {
		return r
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
	return ter.TesSUCCESS
}

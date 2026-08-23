package lending

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
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
func loanToAccount(loan *loanData, rules *amendment.Rules) *lmath.LoanAccount {
	number := func(value string) lmath.N { return lendNumForRules(value, rules) }
	return &lmath.LoanAccount{
		PrincipalOutstanding:     number(loan.PrincipalOutstanding),
		TotalValueOutstanding:    number(loan.TotalValueOutstanding),
		ManagementFeeOutstanding: number(loan.ManagementFeeOutstanding),
		PeriodicPayment:          number(loan.PeriodicPayment),
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
		LoanServiceFee:           number(loan.LoanServiceFee),
		LatePaymentFee:           number(loan.LatePaymentFee),
		ClosePaymentFee:          number(loan.ClosePaymentFee),
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
// fee per loanPaymentsPerFeeIncrement (5). With fixCleanup3_1_3 the estimate is
// capped: the payment handler never processes more than
// loanMaximumPaymentsPerTransaction payments, so the fee never exceeds
// loanMaximumPaymentsPerTransaction / loanPaymentsPerFeeIncrement increments.
func (l *LoanPay) CalculateBaseFee(view tx.LedgerView, config tx.EngineConfig) uint64 {
	number := func(value string) lmath.N { return lendNumForRules(value, config.RequireRules()) }
	normal := sign.CalculateDefaultBaseFee(l, config)
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
	regular := lmath.RoundAssetUpward(mAsset, number(loan.PeriodicPayment), scale).Add(number(loan.LoanServiceFee))
	if regular.Signum() <= 0 {
		return normal
	}
	// Post-fixCleanup3_1_3: cap the estimate at the maximum number of payments the
	// handler will process, so a large Amount does not inflate the fee unboundedly.
	if config.RequireRules().Enabled(amendment.FeatureFixCleanup3_1_3) {
		threshold := regular.Mul(lmath.FromInt(int64(protocol.LoanMaximumPaymentsPerTransaction)))
		if amountToLendNumForRules(l.Amount, config.RequireRules()).Cmp(threshold) >= 0 {
			maxFeeIncrements := protocol.LoanMaximumPaymentsPerTransaction / protocol.LoanPaymentsPerFeeIncrement
			return uint64(maxFeeIncrements) * normal
		}
	}
	mode := state.RoundDownward
	if l.GetFlags()&TfLoanOverpayment != 0 {
		mode = state.RoundUpward
	}
	est := amountToLendNumForRules(l.Amount, config.RequireRules()).DivRounded(regular, mode).ToInt64WithMode(mode)
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
	number := func(value string) lmath.N { return lendNumForRules(value, config.RequireRules()) }
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
		if config.RequireRules().Enabled(amendment.FeatureFixCleanup3_1_3) {
			return ter.TecNO_PERMISSION
		}
		return ter.TemINVALID_FLAG
	}
	if loan.PaymentRemaining == 0 || number(loan.PrincipalOutstanding).IsZero() {
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
	if r := tx.AssetFrozen(view, accountID, asset); r != ter.TesSUCCESS {
		return r
	}
	if r := tx.RequireAuth(view, asset, accountID); r != ter.TesSUCCESS {
		return r
	}
	holds, herr := vault.AccountHoldsFull(view, config, accountID, asset)
	if herr != nil {
		return ter.TefINTERNAL
	}
	if toLargeForRules(holds, config.RequireRules()).Cmp(amountToLendNumForRules(l.Amount, config.RequireRules())) < 0 {
		return ter.TecINSUFFICIENT_FUNDS
	}
	return ter.TesSUCCESS
}

func (l *LoanPay) Apply(ctx *tx.ApplyContext) ter.Result {
	number := func(value string) lmath.N { return lendNumForRules(value, ctx.Rules()) }
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
	var minCover lmath.N
	if ctx.Rules().FixCleanup3_2_0Enabled() {
		minCover = minimumBrokerCover(number(b.DebtTotal), b.CoverRateMinimum, vaultScaleOfForRules(vinfo, integral, ctx.Rules()), integral)
	} else {
		minCover = brokerCoverRateAtScale(number(b.DebtTotal), b.CoverRateMinimum, loanScale, integral)
	}
	sendFeeToOwner := number(b.CoverAvailable).Cmp(minCover) >= 0 &&
		tx.AssetFrozen(ctx.View, b.Owner, asset) == ter.TesSUCCESS &&
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

	acc := loanToAccount(loan, ctx.Rules())
	parts, t := lmath.LoanMakePayment(mAsset, ctx.Config.ParentCloseTime, acc, uint32(b.ManagementFeeRate), amountToLendNumForRules(l.Amount, ctx.Rules()), l.paymentType(), ctx.Rules().Enabled(amendment.FeatureFixCleanup3_1_3), ctx.Rules().FixCleanup3_2_0Enabled())
	if t != ter.TesSUCCESS {
		return t
	}
	accountToLoan(loan, acc)
	associateLoanAsset(loan, integral, ctx.Rules())
	if r := updateLoan(ctx, loanKey, loan); r != ter.TesSUCCESS {
		return r
	}

	// Vault + broker accounting.
	vaultScale := vaultScaleOfForRules(vinfo, integral, ctx.Rules())
	rawToVault := parts.PrincipalPaid.Add(parts.InterestPaid)
	toVaultRounded := lmath.RoundAssetDownward(mAsset, rawToVault, vaultScale)
	toVaultForDebt := rawToVault.Sub(parts.ValueChange)
	toBroker := parts.FeePaid

	b.DebtTotal = numStr(lmath.AdjustImprecise(mAsset, number(b.DebtTotal), toVaultForDebt.Negate(), vaultScale))
	if !sendFeeToOwner {
		b.CoverAvailable = numStr(number(b.CoverAvailable).Add(toBroker))
	}
	associateBrokerAsset(b, integral, ctx.Rules())
	if r := updateBroker(ctx, brokerKey, b); r != ter.TesSUCCESS {
		return r
	}

	newAvailable := number(vinfo.AssetsAvailable).Add(toVaultRounded)
	newTotal := number(vinfo.AssetsTotal).Add(parts.ValueChange)
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
			if _, r := vault.AddEmptyHolding(ctx, brokerPayee, asset, ctx.PriorBalance()); r != ter.TesSUCCESS && r != ter.TecDUPLICATE {
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
	ctx.SyncSenderOwnerCount()
	return ter.TesSUCCESS
}

// reverseImpairment clears an impaired loan's unrealized loss from the vault and
// clears the flag, restoring the normal payment schedule (rippled unimpairLoan,
// invoked at the top of LoanPay::doApply).
func reverseImpairment(ctx *tx.ApplyContext, loan *loanData, vaultKey keylet.Keylet, v *vault.VaultLending, integral bool) ter.Result {
	asset := lmath.Asset{Integral: integral}
	scale := vaultScaleOfForRules(v, integral, ctx.Rules())
	loss := owedToVaultForRules(loan, ctx.Rules())
	if lendNumForRules(v.LossUnrealized, ctx.Rules()).Cmp(loss) < 0 {
		return ter.TefBAD_LEDGER
	}
	newLoss := lmath.AdjustImprecise(asset, lendNumForRules(v.LossUnrealized, ctx.Rules()), loss.Negate(), scale)
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

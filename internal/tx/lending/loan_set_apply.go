package lending

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
)

// resolveBorrower resolves the counterparty (default: broker owner) and the
// borrower. Exactly one of the submitter / counterparty must be the broker owner;
// the other is the borrower.
func (l *LoanSet) resolveBorrower(brokerOwner, account [20]byte) (borrower [20]byte, res ter.Result) {
	counterparty := brokerOwner
	if l.Counterparty != "" {
		id, err := state.DecodeAccountID(l.Counterparty)
		if err != nil {
			return borrower, ter.TemMALFORMED
		}
		counterparty = id
	}
	if account != brokerOwner && counterparty != brokerOwner {
		return borrower, ter.TecNO_PERMISSION
	}
	if counterparty == brokerOwner {
		return account, ter.TesSUCCESS
	}
	return counterparty, ter.TesSUCCESS
}

// loanSetValueFields is getValueFields() for LoanSet: the NUMBER fields that must
// be representable in / rounded to the vault asset.
func (l *LoanSet) loanSetValueFields() []string {
	fields := []string{l.PrincipalRequested}
	for _, f := range []*string{l.LoanOriginationFee, l.LoanServiceFee, l.LatePaymentFee, l.ClosePaymentFee} {
		if f != nil {
			fields = append(fields, *f)
		}
	}
	return fields
}

// CalculateBaseFee includes transaction and sponsor signers through the common
// fee, then adds the nested counterparty signers.
func (l *LoanSet) CalculateBaseFee(_ tx.LedgerView, config tx.EngineConfig) uint64 {
	normal := sign.CalculateDefaultBaseFee(l, config)
	cp := l.GetCommon().CounterpartySignature
	if cp == nil {
		return normal
	}
	signerCount := len(cp.Signers)
	if signerCount == 0 && cp.TxnSignature != "" {
		signerCount = 1
	}
	return normal + uint64(signerCount)*config.BaseFee
}

func (l *LoanSet) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	number := func(value string) lmath.N { return lendNumForRules(value, config.RequireRules()) }
	accountID, err := state.DecodeAccountID(l.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}

	// Schedule must fit in the 32-bit time field.
	now := config.CurrentCloseTime()
	timeAvailable := ^uint32(0) - now
	interval := valOr(l.PaymentInterval, minPaymentInterval)
	total := valOr(l.PaymentTotal, 1)
	grace := valOr(l.GracePeriod, defaultGracePeriod)
	if grace > timeAvailable || interval > timeAvailable || total > timeAvailable {
		return ter.TecKILLED
	}
	if interval == 0 || (timeAvailable-grace)/interval < total {
		return ter.TecKILLED
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
	borrower, res := l.resolveBorrower(b.Owner, accountID)
	if res != ter.TesSUCCESS {
		return res
	}
	if br, aerr := tx.ReadAccountRoot(view, borrower); aerr != nil || br == nil {
		return ter.TerNO_ACCOUNT
	}
	vinfo, verr := vault.ReadVaultLending(view, keylet.VaultByID(b.VaultID))
	if verr != nil || vinfo == nil {
		return ter.TefBAD_LEDGER
	}
	if number(vinfo.AssetsMaximum).Signum() != 0 && number(vinfo.AssetsTotal).Cmp(number(vinfo.AssetsMaximum)) >= 0 {
		return ter.TecLIMIT_EXCEEDED
	}
	asset := vinfo.Asset
	for _, f := range l.loanSetValueFields() {
		if !representableAsAsset(number(f), asset) {
			return ter.TecPRECISION_LOSS
		}
	}
	if r := vault.CanAddHolding(view, asset); r != ter.TesSUCCESS {
		return r
	}
	if r := tx.AssetFrozen(view, vinfo.Account, asset); r != ter.TesSUCCESS {
		return r
	}
	if r := mptutil.CheckDeepFrozen(view, b.Account, asset); r != ter.TesSUCCESS {
		return r
	}
	if r := tx.AssetFrozen(view, borrower, asset); r != ter.TesSUCCESS {
		return r
	}
	if r := mptutil.CheckDeepFrozen(view, b.Owner, asset); r != ter.TesSUCCESS {
		return r
	}
	return ter.TesSUCCESS
}

func (l *LoanSet) Apply(ctx *tx.ApplyContext) ter.Result {
	number := func(value string) lmath.N { return lendNumForRules(value, ctx.Rules()) }
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
	borrower, res := l.resolveBorrower(b.Owner, accountID)
	if res != ter.TesSUCCESS {
		return res
	}
	vaultKey := keylet.VaultByID(b.VaultID)
	vinfo, verr := vault.ReadVaultLending(ctx.View, vaultKey)
	if verr != nil || vinfo == nil {
		return ter.TefBAD_LEDGER
	}
	asset := vinfo.Asset
	mAsset := mathAsset(asset)
	integral := mAsset.Integral

	principal := number(l.PrincipalRequested)
	vaultAvailable := number(vinfo.AssetsAvailable)
	vaultTotal := number(vinfo.AssetsTotal)
	vaultScale := vaultScaleOfForRules(vinfo, integral, ctx.Rules())
	if vaultAvailable.Cmp(principal) < 0 {
		return ter.TecINSUFFICIENT_FUNDS
	}

	interestRate := valOr(l.InterestRate, 0)
	paymentInterval := valOr(l.PaymentInterval, minPaymentInterval)
	paymentTotal := valOr(l.PaymentTotal, 1)

	props := lmath.ComputeLoanProperties(ctx.Rules().FixCleanup3_2_0Enabled(), mAsset, principal, interestRate, paymentInterval, paymentTotal, uint32(b.ManagementFeeRate), vaultScale)
	loanState := lmath.ConstructLoanState(props.LoanState.ValueOutstanding, principal, props.LoanState.ManagementFeeDue)

	vaultMaximum := number(vinfo.AssetsMaximum)
	if vaultMaximum.Signum() != 0 && loanState.InterestDue.Cmp(vaultMaximum.Sub(vaultTotal)) > 0 {
		return ter.TecLIMIT_EXCEEDED
	}
	for _, f := range l.loanSetValueFields() {
		if !lmath.IsRounded(mAsset, number(f), props.LoanScale) {
			return ter.TecPRECISION_LOSS
		}
	}
	if t := lmath.CheckLoanGuards(mAsset, principal, interestRate != 0, paymentTotal, props); t != ter.TesSUCCESS {
		return t
	}
	if props.LoanState.ManagementFeeDue.Signum() < 0 || props.LoanState.ValueOutstanding.Signum() <= 0 || props.PeriodicPayment.Signum() <= 0 {
		return ter.TecINTERNAL
	}

	originationFee := lmath.NumScaled(0, 0, lendingNumberScale(ctx.Rules()))
	if l.LoanOriginationFee != nil {
		originationFee = number(*l.LoanOriginationFee)
	}
	loanToBorrower := principal.Sub(originationFee)

	newDebtDelta := principal.Add(loanState.InterestDue)
	newDebtTotal := number(b.DebtTotal).Add(newDebtDelta)
	if number(b.DebtMaximum).Signum() != 0 && number(b.DebtMaximum).Cmp(newDebtTotal) < 0 {
		return ter.TecLIMIT_EXCEEDED
	}
	var minCover lmath.N
	if ctx.Rules().FixCleanup3_2_0Enabled() {
		minCover = minimumBrokerCover(newDebtTotal, b.CoverRateMinimum, vaultScale, integral)
	} else {
		minCover = brokerCoverRate(newDebtTotal, b.CoverRateMinimum)
	}
	if number(b.CoverAvailable).Cmp(minCover) < 0 {
		return ter.TecINSUFFICIENT_FUNDS
	}

	// Borrower reserve for the new Loan object.
	borrowerReserveOK, res := l.chargeBorrower(ctx, borrower)
	if res != ter.TesSUCCESS {
		return res
	}
	if !borrowerReserveOK {
		return ter.TecINSUFFICIENT_RESERVE
	}
	borrowerPriorBalance, r := loanSetHoldingPriorBalance(ctx, borrower)
	if r != ter.TesSUCCESS {
		return r
	}
	borrowerHoldingDelta, r := vault.AddEmptyHolding(ctx, borrower, asset, borrowerPriorBalance)
	if r != ter.TesSUCCESS && r != ter.TecDUPLICATE {
		return r
	}
	if r := applyLoanSetHoldingOwnerCount(ctx, borrower, borrowerHoldingDelta); r != ter.TesSUCCESS {
		return r
	}
	if r := tx.RequireAuth(ctx.View, asset, borrower); r != ter.TesSUCCESS {
		return r
	}
	if originationFee.Signum() != 0 {
		ownerPriorBalance, r := loanSetHoldingPriorBalance(ctx, b.Owner)
		if r != ter.TesSUCCESS {
			return r
		}
		ownerHoldingDelta, r := vault.AddEmptyHolding(ctx, b.Owner, asset, ownerPriorBalance)
		if r != ter.TesSUCCESS && r != ter.TecDUPLICATE {
			return r
		}
		if r := applyLoanSetHoldingOwnerCount(ctx, b.Owner, ownerHoldingDelta); r != ter.TesSUCCESS {
			return r
		}
		if r := tx.RequireAuth(ctx.View, asset, b.Owner); r != ter.TesSUCCESS {
			return r
		}
	}

	// Disburse principal to the borrower and the origination fee to the owner.
	if r := vault.SendAsset(ctx, vinfo.Account, borrower, asset, loanToBorrower); r != ter.TesSUCCESS {
		return r
	}
	if originationFee.Signum() != 0 {
		if r := vault.SendAsset(ctx, vinfo.Account, b.Owner, asset, originationFee); r != ter.TesSUCCESS {
			return r
		}
	}

	startDate := ctx.Config.CurrentCloseTime()
	loanSeq := b.LoanSequence
	loanKey := keylet.Loan(brokerID, loanSeq)

	brokerDir, err := state.DirInsert(ctx.View, keylet.OwnerDir(b.Account), loanKey.Key, false, func(d *state.DirectoryNode) {
		d.Owner = b.Account
	})
	if err != nil {
		return ter.TecDIR_FULL
	}
	ownerDir, err := state.DirInsert(ctx.View, keylet.OwnerDir(borrower), loanKey.Key, false, func(d *state.DirectoryNode) {
		d.Owner = borrower
	})
	if err != nil {
		return ter.TecDIR_FULL
	}

	loan := &loanData{
		OwnerNode:                ownerDir.Page,
		LoanBrokerNode:           brokerDir.Page,
		LoanBrokerID:             brokerID,
		LoanSequence:             loanSeq,
		Borrower:                 borrower,
		StartDate:                startDate,
		PaymentInterval:          paymentInterval,
		GracePeriod:              valOr(l.GracePeriod, defaultGracePeriod),
		NextPaymentDueDate:       startDate + paymentInterval,
		PaymentRemaining:         paymentTotal,
		LoanScale:                int32(props.LoanScale),
		PrincipalOutstanding:     numStr(principal),
		PeriodicPayment:          numStr(props.PeriodicPayment),
		TotalValueOutstanding:    numStr(props.LoanState.ValueOutstanding),
		ManagementFeeOutstanding: numStr(props.LoanState.ManagementFeeDue),
		OverpaymentFee:           valOr(l.OverpaymentFee, 0),
		InterestRate:             interestRate,
		LateInterestRate:         valOr(l.LateInterestRate, 0),
		CloseInterestRate:        valOr(l.CloseInterestRate, 0),
		OverpaymentInterestRate:  valOr(l.OverpaymentInterestRate, 0),
	}
	if l.LoanOriginationFee != nil {
		loan.LoanOriginationFee = numStr(originationFee)
	}
	if l.LoanServiceFee != nil {
		loan.LoanServiceFee = numStr(number(*l.LoanServiceFee))
	}
	if l.LatePaymentFee != nil {
		loan.LatePaymentFee = numStr(number(*l.LatePaymentFee))
	}
	if l.ClosePaymentFee != nil {
		loan.ClosePaymentFee = numStr(number(*l.ClosePaymentFee))
	}
	if l.GetFlags()&TfLoanOverpayment != 0 {
		loan.Flags |= LsfLoanOverpayment
	}
	associateLoanAsset(loan, integral, ctx.Rules())

	// Vault: draw principal, book the interest into total value.
	newAvailable := vaultAvailable.Sub(principal)
	newTotal := vaultTotal.Add(loanState.InterestDue)
	if r := vault.UpdateVaultTotals(ctx, vaultKey, numStr(newTotal), numStr(newAvailable), vinfo.LossUnrealized); r != ter.TesSUCCESS {
		return r
	}

	// Broker: grow debt, count the loan, advance the loan sequence.
	b.DebtTotal = numStr(lmath.AdjustImprecise(mAsset, number(b.DebtTotal), newDebtDelta, vaultScale))
	b.OwnerCount++
	b.LoanSequence++
	if b.LoanSequence == 0 {
		return ter.TecINTERNAL
	}
	associateBrokerAsset(b, integral, ctx.Rules())
	if r := updateBroker(ctx, brokerKey, b); r != ter.TesSUCCESS {
		return r
	}

	loanBytes, serr := serializeLoanForRules(loan, ctx.Rules())
	if serr != nil {
		return ter.TefINTERNAL
	}
	if ierr := ctx.View.Insert(loanKey, loanBytes); ierr != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

// chargeBorrower increments the borrower's owner count for the new Loan object
// and verifies its reserve. Returns (reserveOK, result).
func (l *LoanSet) chargeBorrower(ctx *tx.ApplyContext, borrower [20]byte) (bool, ter.Result) {
	if borrower == ctx.AccountID {
		newCount := ctx.Account.OwnerCount + 1
		if ctx.PriorBalance() < ctx.AccountReserve(newCount) {
			return false, ter.TesSUCCESS
		}
		ctx.Account.OwnerCount = newCount
		return true, ter.TesSUCCESS
	}
	br, err := tx.ReadAccountRoot(ctx.View, borrower)
	if err != nil || br == nil {
		return false, ter.TefBAD_LEDGER
	}
	if br.Balance < ctx.AccountReserveFor(br, br.OwnerCount+1) {
		return false, ter.TesSUCCESS
	}
	if e := tx.AdjustOwnerCount(ctx.View, borrower, 1); e != nil {
		return false, ter.TefINTERNAL
	}
	return true, ter.TesSUCCESS
}

func applyLoanSetHoldingOwnerCount(ctx *tx.ApplyContext, accountID [20]byte, delta int32) ter.Result {
	if delta == 0 {
		return ter.TesSUCCESS
	}
	if accountID == ctx.AccountID {
		ctx.Account.OwnerCount = tx.ConfineOwnerCount(ctx.Account.OwnerCount, int(delta))
		return ter.TesSUCCESS
	}
	if err := tx.AdjustOwnerCount(ctx.View, accountID, int(delta)); err != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

func loanSetHoldingPriorBalance(ctx *tx.ApplyContext, accountID [20]byte) (uint64, ter.Result) {
	if accountID == ctx.AccountID {
		return ctx.PriorBalance(), ter.TesSUCCESS
	}
	account, err := tx.ReadAccountRoot(ctx.View, accountID)
	if err != nil || account == nil {
		return 0, ter.TefBAD_LEDGER
	}
	return account.Balance, ter.TesSUCCESS
}

// valOr returns *p or def when p is nil.
func valOr(p *uint32, def uint32) uint32 {
	if p == nil {
		return def
	}
	return *p
}

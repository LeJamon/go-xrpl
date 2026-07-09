package lending

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
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

func (l *LoanSet) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	accountID, err := state.DecodeAccountID(l.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}

	// Schedule must fit in the 32-bit time field.
	now := config.ParentCloseTime
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
	if br, aerr := vault.ReadAccountRoot(view, borrower); aerr != nil || br == nil {
		return ter.TerNO_ACCOUNT
	}
	vinfo, verr := vault.ReadVaultLending(view, keylet.VaultByID(b.VaultID))
	if verr != nil || vinfo == nil {
		return ter.TefBAD_LEDGER
	}
	if lendNum(vinfo.AssetsMaximum).Signum() != 0 && lendNum(vinfo.AssetsTotal).Cmp(lendNum(vinfo.AssetsMaximum)) >= 0 {
		return ter.TecLIMIT_EXCEEDED
	}
	asset := vinfo.Asset
	for _, f := range l.loanSetValueFields() {
		if !representableAsAsset(lendNum(f), asset) {
			return ter.TecPRECISION_LOSS
		}
	}
	if r := vault.CanAddHolding(view, asset); r != ter.TesSUCCESS {
		return r
	}
	if r := vault.AssetFrozen(view, vinfo.Account, asset); r != ter.TesSUCCESS {
		return r
	}
	if r := vault.AssetFrozen(view, borrower, asset); r != ter.TesSUCCESS {
		return r
	}
	return ter.TesSUCCESS
}

func (l *LoanSet) Apply(ctx *tx.ApplyContext) ter.Result {
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

	principal := lendNum(l.PrincipalRequested)
	vaultAvailable := lendNum(vinfo.AssetsAvailable)
	vaultTotal := lendNum(vinfo.AssetsTotal)
	vaultScale := vaultScaleOf(vinfo, integral)
	if vaultAvailable.Cmp(principal) < 0 {
		return ter.TecINSUFFICIENT_FUNDS
	}

	interestRate := valOr(l.InterestRate, 0)
	paymentInterval := valOr(l.PaymentInterval, minPaymentInterval)
	paymentTotal := valOr(l.PaymentTotal, 1)

	props := lmath.ComputeLoanProperties(ctx.Rules().FixCleanup3_2_0Enabled(), mAsset, principal, interestRate, paymentInterval, paymentTotal, uint32(b.ManagementFeeRate), vaultScale)
	loanState := lmath.ConstructLoanState(props.LoanState.ValueOutstanding, principal, props.LoanState.ManagementFeeDue)

	vaultMaximum := lendNum(vinfo.AssetsMaximum)
	if vaultMaximum.Signum() != 0 && loanState.InterestDue.Cmp(vaultMaximum.Sub(vaultTotal)) > 0 {
		return ter.TecLIMIT_EXCEEDED
	}
	for _, f := range l.loanSetValueFields() {
		if !lmath.IsRounded(mAsset, lendNum(f), props.LoanScale) {
			return ter.TecPRECISION_LOSS
		}
	}
	if t := lmath.CheckLoanGuards(mAsset, principal, interestRate != 0, paymentTotal, props); t != ter.TesSUCCESS {
		return t
	}
	if props.LoanState.ManagementFeeDue.Signum() < 0 || props.LoanState.ValueOutstanding.Signum() <= 0 || props.PeriodicPayment.Signum() <= 0 {
		return ter.TecINTERNAL
	}

	originationFee := lmath.Zero()
	if l.LoanOriginationFee != nil {
		originationFee = lendNum(*l.LoanOriginationFee)
	}
	loanToBorrower := principal.Sub(originationFee)

	newDebtDelta := principal.Add(loanState.InterestDue)
	newDebtTotal := lendNum(b.DebtTotal).Add(newDebtDelta)
	if lendNum(b.DebtMaximum).Signum() != 0 && lendNum(b.DebtMaximum).Cmp(newDebtTotal) < 0 {
		return ter.TecLIMIT_EXCEEDED
	}
	var minCover lmath.N
	if ctx.Rules().FixCleanup3_2_0Enabled() {
		minCover = minimumBrokerCover(newDebtTotal, b.CoverRateMinimum, vaultScale, integral)
	} else {
		minCover = brokerCoverRate(newDebtTotal, b.CoverRateMinimum)
	}
	if lendNum(b.CoverAvailable).Cmp(minCover) < 0 {
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
	if _, r := vault.AddEmptyHolding(ctx, borrower, asset); r != ter.TesSUCCESS && r != ter.TecDUPLICATE {
		return r
	}
	if r := tx.RequireAuth(ctx.View, asset, borrower); r != ter.TesSUCCESS {
		return r
	}
	if originationFee.Signum() != 0 {
		if _, r := vault.AddEmptyHolding(ctx, b.Owner, asset); r != ter.TesSUCCESS && r != ter.TecDUPLICATE {
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

	startDate := ctx.Config.ParentCloseTime
	loanSeq := b.LoanSequence
	loanKey := keylet.Loan(brokerID, loanSeq)

	brokerDir, err := state.DirInsert(ctx.View, keylet.OwnerDir(vinfo.Account), loanKey.Key, false, func(d *state.DirectoryNode) {
		d.Owner = vinfo.Account
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
		loan.LoanServiceFee = numStr(lendNum(*l.LoanServiceFee))
	}
	if l.LatePaymentFee != nil {
		loan.LatePaymentFee = numStr(lendNum(*l.LatePaymentFee))
	}
	if l.ClosePaymentFee != nil {
		loan.ClosePaymentFee = numStr(lendNum(*l.ClosePaymentFee))
	}
	if l.GetFlags()&TfLoanOverpayment != 0 {
		loan.Flags |= LsfLoanOverpayment
	}
	associateLoanAsset(loan, integral)

	// Vault: draw principal, book the interest into total value.
	newAvailable := vaultAvailable.Sub(principal)
	newTotal := vaultTotal.Add(loanState.InterestDue)
	if r := vault.UpdateVaultTotals(ctx, vaultKey, numStr(newTotal), numStr(newAvailable), vinfo.LossUnrealized); r != ter.TesSUCCESS {
		return r
	}

	// Broker: grow debt, count the loan, advance the loan sequence.
	b.DebtTotal = numStr(lmath.AdjustImprecise(mAsset, lendNum(b.DebtTotal), newDebtDelta, vaultScale))
	b.OwnerCount++
	b.LoanSequence++
	if b.LoanSequence == 0 {
		return ter.TecINTERNAL
	}
	associateBrokerAsset(b, integral)
	if r := updateBroker(ctx, brokerKey, b); r != ter.TesSUCCESS {
		return r
	}

	loanBytes, serr := serializeLoan(loan)
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
	br, err := vault.ReadAccountRoot(ctx.View, borrower)
	if err != nil || br == nil {
		return false, ter.TefBAD_LEDGER
	}
	if br.Balance < ctx.AccountReserve(br.OwnerCount+1) {
		return false, ter.TesSUCCESS
	}
	if e := tx.AdjustOwnerCount(ctx.View, borrower, 1); e != nil {
		return false, ter.TefINTERNAL
	}
	return true, ter.TesSUCCESS
}

// valOr returns *p or def when p is nil.
func valOr(p *uint32, def uint32) uint32 {
	if p == nil {
		return def
	}
	return *p
}

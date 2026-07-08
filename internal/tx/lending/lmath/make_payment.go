package lmath

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/protocol"
)

// LoanAccount holds the mutable loan values LoanMakePayment reads and updates,
// standing in for the Loan SLE proxies rippled mutates. The LoanPay transactor
// marshals the Loan ledger entry into this struct, calls LoanMakePayment, then
// writes the mutated fields back.
type LoanAccount struct {
	PrincipalOutstanding     N
	TotalValueOutstanding    N
	ManagementFeeOutstanding N
	PeriodicPayment          N
	PaymentRemaining         uint32
	PrevPaymentDueDate       uint32 // sfPreviousPaymentDueDate
	NextPaymentDueDate       uint32
	StartDate                uint32
	PaymentInterval          uint32
	LoanScale                int
	InterestRate             uint32
	LateInterestRate         uint32
	CloseInterestRate        uint32
	OverpaymentInterestRate  uint32
	OverpaymentFee           uint32 // rate, 1/10 bips
	LoanServiceFee           N
	LatePaymentFee           N
	ClosePaymentFee          N
	HasOverpaymentFlag       bool // lsfLoanOverpayment
}

// hasExpired reports whether exp has passed relative to now (rippled hasExpired:
// exp != 0 && parentCloseTime >= exp).
func hasExpired(now, exp uint32) bool { return exp != 0 && now >= exp }

// doPayment applies a payment's deltas to the loan and returns the parts paid
// (rippled detail::doPayment). now unused; the schedule advance uses the loan's
// own dates.
func doPayment(payment ExtendedPaymentComponents, loan *LoanAccount) LoanPaymentParts {
	if payment.SpecialCase == SpecialFinal {
		loan.PaymentRemaining = 0
		loan.PrevPaymentDueDate = loan.NextPaymentDueDate
		loan.NextPaymentDueDate = 0
		loan.PrincipalOutstanding = zeroN()
		loan.TotalValueOutstanding = zeroN()
		loan.ManagementFeeOutstanding = zeroN()
	} else {
		if payment.SpecialCase != SpecialExtra {
			loan.PaymentRemaining--
			loan.PrevPaymentDueDate = loan.NextPaymentDueDate
			loan.NextPaymentDueDate += loan.PaymentInterval
		}
		loan.PrincipalOutstanding = loan.PrincipalOutstanding.Sub(payment.TrackedPrincipalDelta)
		loan.TotalValueOutstanding = loan.TotalValueOutstanding.Sub(payment.TrackedValueDelta)
		loan.ManagementFeeOutstanding = loan.ManagementFeeOutstanding.Sub(payment.TrackedManagementFeeDelta)
	}
	return LoanPaymentParts{
		PrincipalPaid: payment.TrackedPrincipalDelta,
		InterestPaid:  payment.TrackedInterestPart().Add(payment.UntrackedInterest),
		ValueChange:   payment.UntrackedInterest,
		FeePaid:       payment.TrackedManagementFeeDelta.Add(payment.UntrackedManagementFee),
	}
}

// computeLatePayment builds the components for a late payment (rippled
// computeLatePayment). Returns the failing tec (tecTOO_SOON /
// tecINSUFFICIENT_PAYMENT) when the payment cannot be made.
func computeLatePayment(asset Asset, now uint32, principalOutstanding N, nextDueDate uint32, periodic ExtendedPaymentComponents, lateInterestRate uint32, loanScale int, latePaymentFee, amount N, managementFeeRate uint32) (ExtendedPaymentComponents, ter.Result) {
	if !hasExpired(now, nextDueDate) {
		return ExtendedPaymentComponents{}, ter.TecTOO_SOON
	}
	latePaymentInterest := loanLatePaymentInterest(principalOutstanding, lateInterestRate, now, nextDueDate)
	interest := roundToAssetNearest(asset, latePaymentInterest, loanScale)
	roundedLateInterest, roundedLateManagementFee := computeInterestAndFeeParts(asset, interest, managementFeeRate, loanScale)
	late := newExtended(periodic.PaymentComponents,
		periodic.UntrackedManagementFee.Add(latePaymentFee).Add(roundedLateManagementFee),
		periodic.UntrackedInterest.Add(roundedLateInterest))
	if amount.Cmp(late.TotalDue) < 0 {
		return ExtendedPaymentComponents{}, ter.TecINSUFFICIENT_PAYMENT
	}
	return late, ter.TesSUCCESS
}

// computeFullPayment builds the components for an early full payment (rippled
// computeFullPayment). Returns tecKILLED (last payment can't be full) or
// tecINSUFFICIENT_PAYMENT.
func computeFullPayment(asset Asset, now uint32, principalOutstanding, managementFeeOutstanding, periodicPayment N, paymentRemaining, prevPaymentDate, startDate, paymentInterval, closeInterestRate uint32, loanScale int, totalInterestOutstanding, periodicRate, closePaymentFee, amount N, managementFeeRate uint32) (ExtendedPaymentComponents, ter.Result) {
	if paymentRemaining <= 1 {
		return ExtendedPaymentComponents{}, ter.TecKILLED
	}
	theoreticalPrincipal := loanPrincipalFromPeriodicPayment(periodicPayment, periodicRate, paymentRemaining)
	fullPaymentInterest := ComputeFullPaymentInterest(theoreticalPrincipal, periodicRate, now, paymentInterval, prevPaymentDate, startDate, closeInterestRate)
	interest := roundToAsset(asset, fullPaymentInterest, loanScale, state.RoundDownward)
	roundedFullInterest, roundedFullManagementFee := computeInterestAndFeeParts(asset, interest, managementFeeRate, loanScale)
	full := newExtended(
		PaymentComponents{
			TrackedValueDelta:         principalOutstanding.Add(totalInterestOutstanding).Add(managementFeeOutstanding),
			TrackedPrincipalDelta:     principalOutstanding,
			TrackedManagementFeeDelta: managementFeeOutstanding,
			SpecialCase:               SpecialFinal,
		},
		closePaymentFee.Add(roundedFullManagementFee).Sub(managementFeeOutstanding),
		roundedFullInterest.Sub(totalInterestOutstanding))
	if amount.Cmp(full.TotalDue) < 0 {
		return ExtendedPaymentComponents{}, ter.TecINSUFFICIENT_PAYMENT
	}
	return full, ter.TesSUCCESS
}

// tryOverpayment re-amortizes the loan in a sandbox to validate an overpayment
// (rippled detail::tryOverpayment). ok=false with err==tesSUCCESS means the
// overpayment is silently ignored; err!=tesSUCCESS propagates.
func tryOverpayment(asset Asset, loanScale int, overpaymentComponents ExtendedPaymentComponents, roundedOldState LoanState, periodicPayment, periodicRate N, paymentRemaining uint32, managementFeeRate uint32) (parts LoanPaymentParts, props LoanProperties, ok bool, err ter.Result) {
	theoreticalState := computeTheoreticalLoanState(periodicPayment, periodicRate, paymentRemaining, managementFeeRate)
	errors := subStates(roundedOldState, theoreticalState)
	newTheoreticalPrincipal := maxN(theoreticalState.PrincipalOutstanding.Sub(overpaymentComponents.TrackedPrincipalDelta), zeroN())
	newLoanProperties := ComputeLoanPropertiesRate(asset, newTheoreticalPrincipal, periodicRate, paymentRemaining, managementFeeRate, loanScale)
	newTheoreticalState := addStateDeltas(
		computeTheoreticalLoanState(newLoanProperties.PeriodicPayment, periodicRate, paymentRemaining, managementFeeRate),
		errors)

	principalOutstanding := clampN(roundToAsset(asset, newTheoreticalState.PrincipalOutstanding, loanScale, state.RoundUpward), zeroN(), roundedOldState.PrincipalOutstanding)
	totalValueOutstanding := clampN(roundToAsset(asset, principalOutstanding.Add(newTheoreticalState.InterestOutstanding()), loanScale, state.RoundUpward), zeroN(), roundedOldState.ValueOutstanding)
	managementFeeOutstanding := clampN(roundToAssetNearest(asset, newTheoreticalState.ManagementFeeDue, loanScale), zeroN(), roundedOldState.ManagementFeeDue)

	roundedNewState := ConstructLoanState(totalValueOutstanding, principalOutstanding, managementFeeOutstanding)
	newLoanProperties.LoanState = roundedNewState

	if t := CheckLoanGuards(asset, principalOutstanding, roundedNewState.InterestOutstanding().Signum() != 0, paymentRemaining, newLoanProperties); t != ter.TesSUCCESS {
		return LoanPaymentParts{}, LoanProperties{}, false, ter.TesSUCCESS
	}
	if newLoanProperties.PeriodicPayment.Signum() <= 0 || newLoanProperties.LoanState.ValueOutstanding.Signum() <= 0 || newLoanProperties.LoanState.ManagementFeeDue.Signum() < 0 {
		return LoanPaymentParts{}, LoanProperties{}, false, ter.TesSUCCESS
	}

	deltas := subStates(roundedOldState, roundedNewState)
	valueChange := deltas.Interest.Negate()
	if gtZero(valueChange) {
		return LoanPaymentParts{}, LoanProperties{}, false, ter.TesSUCCESS
	}
	parts = LoanPaymentParts{
		PrincipalPaid: deltas.Principal,
		InterestPaid:  overpaymentComponents.UntrackedInterest,
		ValueChange:   valueChange.Add(overpaymentComponents.UntrackedInterest),
		FeePaid:       overpaymentComponents.UntrackedManagementFee.Add(overpaymentComponents.TrackedManagementFeeDelta),
	}
	return parts, newLoanProperties, true, ter.TesSUCCESS
}

// doOverpayment validates and commits an overpayment to the loan (rippled
// detail::doOverpayment).
func doOverpayment(asset Asset, loanScale int, overpaymentComponents ExtendedPaymentComponents, loan *LoanAccount, periodicRate N, paymentRemaining uint32, managementFeeRate uint32) (LoanPaymentParts, bool, ter.Result) {
	loanState := ConstructLoanState(loan.TotalValueOutstanding, loan.PrincipalOutstanding, loan.ManagementFeeOutstanding)
	parts, newProps, ok, err := tryOverpayment(asset, loanScale, overpaymentComponents, loanState, loan.PeriodicPayment, periodicRate, paymentRemaining, managementFeeRate)
	if !ok {
		return LoanPaymentParts{}, false, err
	}
	newRoundedLoanState := newProps.LoanState
	if loan.PrincipalOutstanding.Cmp(newRoundedLoanState.PrincipalOutstanding) <= 0 {
		return LoanPaymentParts{}, false, ter.TesSUCCESS
	}
	loan.TotalValueOutstanding = newRoundedLoanState.ValueOutstanding
	loan.PrincipalOutstanding = newRoundedLoanState.PrincipalOutstanding
	loan.ManagementFeeOutstanding = newRoundedLoanState.ManagementFeeDue
	loan.PeriodicPayment = newProps.PeriodicPayment
	return parts, true, ter.TesSUCCESS
}

// LoanMakePayment applies a payment to the loan and returns the breakdown of
// amounts paid, mutating loan in place (rippled loanMakePayment). now is the
// parent close time in Ripple-epoch seconds. A result other than tesSUCCESS is a
// failure; loan must not be persisted in that case.
func LoanMakePayment(asset Asset, now uint32, loan *LoanAccount, managementFeeRate uint32, amount N, paymentType LoanPaymentType) (LoanPaymentParts, ter.Result) {
	if loan.PaymentRemaining == 0 || loan.PrincipalOutstanding.IsZero() {
		return LoanPaymentParts{}, ter.TecKILLED
	}
	if loan.NextPaymentDueDate == 0 {
		return LoanPaymentParts{}, ter.TecINTERNAL
	}
	loanScale := loan.LoanScale
	periodicRate := LoanPeriodicRate(loan.InterestRate, loan.PaymentInterval)

	if paymentType != PaymentLate && hasExpired(now, loan.NextPaymentDueDate) {
		return LoanPaymentParts{}, ter.TecEXPIRED
	}

	if paymentType == PaymentFull {
		closePaymentFee := roundToAssetNearest(asset, loan.ClosePaymentFee, loanScale)
		roundedLoanState := ConstructLoanState(loan.TotalValueOutstanding, loan.PrincipalOutstanding, loan.ManagementFeeOutstanding)
		full, t := computeFullPayment(asset, now, loan.PrincipalOutstanding, loan.ManagementFeeOutstanding, loan.PeriodicPayment,
			loan.PaymentRemaining, loan.PrevPaymentDueDate, loan.StartDate, loan.PaymentInterval, loan.CloseInterestRate,
			loanScale, roundedLoanState.InterestDue, periodicRate, closePaymentFee, amount, managementFeeRate)
		if t != ter.TesSUCCESS {
			return LoanPaymentParts{}, t
		}
		return doPayment(full, loan), ter.TesSUCCESS
	}

	periodicOf := func() ExtendedPaymentComponents {
		return newExtended(computePaymentComponents(asset, loanScale, loan.TotalValueOutstanding, loan.PrincipalOutstanding,
			loan.ManagementFeeOutstanding, loan.PeriodicPayment, periodicRate, loan.PaymentRemaining, managementFeeRate),
			loan.LoanServiceFee, zeroN())
	}
	periodic := periodicOf()

	if paymentType == PaymentLate {
		late, t := computeLatePayment(asset, now, loan.PrincipalOutstanding, loan.NextPaymentDueDate, periodic,
			loan.LateInterestRate, loanScale, loan.LatePaymentFee, amount, managementFeeRate)
		if t != ter.TesSUCCESS {
			return LoanPaymentParts{}, t
		}
		return doPayment(late, loan), ter.TesSUCCESS
	}

	totalParts := newParts()
	totalPaid := zeroN()
	numPayments := 0
	for amount.Cmp(totalPaid.Add(periodic.TotalDue)) >= 0 && loan.PaymentRemaining > 0 && numPayments < protocol.LoanMaximumPaymentsPerTransaction {
		totalPaid = totalPaid.Add(periodic.TotalDue)
		p := doPayment(periodic, loan)
		totalParts.add(p)
		numPayments++
		if periodic.SpecialCase == SpecialFinal {
			break
		}
		periodic = periodicOf()
	}
	if numPayments == 0 {
		return LoanPaymentParts{}, ter.TecINSUFFICIENT_PAYMENT
	}

	if paymentType == PaymentOverpayment && loan.HasOverpaymentFlag && loan.PaymentRemaining > 0 &&
		totalPaid.Cmp(amount) < 0 && numPayments < protocol.LoanMaximumPaymentsPerTransaction {
		overpayment := minN(amount.Sub(totalPaid), loan.TotalValueOutstanding)
		overpaymentComponents := computeOverpaymentComponents(asset, loanScale, overpayment, loan.OverpaymentInterestRate, loan.OverpaymentFee, managementFeeRate)
		if gtZero(overpaymentComponents.TrackedPrincipalDelta) {
			oParts, ok, err := doOverpayment(asset, loanScale, overpaymentComponents, loan, periodicRate, loan.PaymentRemaining, managementFeeRate)
			if ok {
				totalParts.add(oParts)
			} else if err != ter.TesSUCCESS {
				return LoanPaymentParts{}, err
			}
		}
	}
	return totalParts, ter.TesSUCCESS
}

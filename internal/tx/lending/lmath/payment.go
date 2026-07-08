package lmath

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// minN / maxN / clampN are the Number analogues of std::min/max/clamp.
func minN(a, b N) N {
	if a.Cmp(b) <= 0 {
		return a
	}
	return b
}

func maxN(a, b N) N {
	if a.Cmp(b) >= 0 {
		return a
	}
	return b
}

func clampN(v, lo, hi N) N { return maxN(lo, minN(v, hi)) }

func isNeg(n N) bool  { return n.Signum() < 0 }
func gtZero(n N) bool { return n.Signum() > 0 }

// PaymentSpecialCase distinguishes a regular payment from the loan-closing final
// payment and from an extra (overpayment) beyond the schedule.
type PaymentSpecialCase int

const (
	SpecialNone PaymentSpecialCase = iota
	SpecialFinal
	SpecialExtra
)

// LoanPaymentType selects the payment path. regular/late/full are mutually
// exclusive; overpayment follows the regular path with extra work at the end.
type LoanPaymentType int

const (
	PaymentRegular LoanPaymentType = iota
	PaymentLate
	PaymentFull
	PaymentOverpayment
)

// LoanState captures the outstanding parts of a loan. interestOutstanding =
// interestDue + managementFeeDue = valueOutstanding - principalOutstanding.
type LoanState struct {
	ValueOutstanding     N
	PrincipalOutstanding N
	InterestDue          N
	ManagementFeeDue     N
}

// InterestOutstanding is the total interest (net interest plus management fee).
func (s LoanState) InterestOutstanding() N { return s.InterestDue.Add(s.ManagementFeeDue) }

// LoanProperties are the derived amortization properties of a loan.
type LoanProperties struct {
	PeriodicPayment       N
	LoanState             LoanState
	LoanScale             int
	FirstPaymentPrincipal N
}

// LoanStateDeltas is the per-component difference between two LoanStates.
type LoanStateDeltas struct {
	Principal     N
	Interest      N
	ManagementFee N
}

// Total sums the three deltas.
func (d LoanStateDeltas) Total() N { return d.Principal.Add(d.Interest).Add(d.ManagementFee) }

// nonNegative floors each delta at zero.
func (d *LoanStateDeltas) nonNegative() {
	if isNeg(d.Principal) {
		d.Principal = zeroN()
	}
	if isNeg(d.Interest) {
		d.Interest = zeroN()
	}
	if isNeg(d.ManagementFee) {
		d.ManagementFee = zeroN()
	}
}

// PaymentComponents are the tracked "delta" values a single payment removes from
// the loan object.
type PaymentComponents struct {
	TrackedValueDelta         N
	TrackedPrincipalDelta     N
	TrackedManagementFeeDelta N
	SpecialCase               PaymentSpecialCase
}

// TrackedInterestPart derives the tracked interest paid to the vault.
func (p PaymentComponents) TrackedInterestPart() N {
	return p.TrackedValueDelta.Sub(p.TrackedPrincipalDelta.Add(p.TrackedManagementFeeDelta))
}

// ExtendedPaymentComponents adds untracked fee/interest (paid directly, not part
// of the amortization schedule) and the total amount due from the borrower.
type ExtendedPaymentComponents struct {
	PaymentComponents
	UntrackedManagementFee N
	UntrackedInterest      N
	TotalDue               N
}

func newExtended(p PaymentComponents, fee, interest N) ExtendedPaymentComponents {
	return ExtendedPaymentComponents{
		PaymentComponents:      p,
		UntrackedManagementFee: fee,
		UntrackedInterest:      interest,
		TotalDue:               p.TrackedValueDelta.Add(interest).Add(fee),
	}
}

// LoanPaymentParts is the breakdown of a processed payment.
type LoanPaymentParts struct {
	PrincipalPaid N
	InterestPaid  N
	ValueChange   N
	FeePaid       N
}

func (p *LoanPaymentParts) add(o LoanPaymentParts) {
	p.PrincipalPaid = p.PrincipalPaid.Add(o.PrincipalPaid)
	p.InterestPaid = p.InterestPaid.Add(o.InterestPaid)
	p.ValueChange = p.ValueChange.Add(o.ValueChange)
	p.FeePaid = p.FeePaid.Add(o.FeePaid)
}

func newParts() LoanPaymentParts {
	return LoanPaymentParts{PrincipalPaid: zeroN(), InterestPaid: zeroN(), ValueChange: zeroN(), FeePaid: zeroN()}
}

// subStates returns the per-component delta lhs - rhs (rippled operator-).
func subStates(lhs, rhs LoanState) LoanStateDeltas {
	return LoanStateDeltas{
		Principal:     lhs.PrincipalOutstanding.Sub(rhs.PrincipalOutstanding),
		Interest:      lhs.InterestDue.Sub(rhs.InterestDue),
		ManagementFee: lhs.ManagementFeeDue.Sub(rhs.ManagementFeeDue),
	}
}

// addStateDeltas returns lhs + rhs.
func addStateDeltas(lhs LoanState, rhs LoanStateDeltas) LoanState {
	return LoanState{
		ValueOutstanding:     lhs.ValueOutstanding.Add(rhs.Total()),
		PrincipalOutstanding: lhs.PrincipalOutstanding.Add(rhs.Principal),
		InterestDue:          lhs.InterestDue.Add(rhs.Interest),
		ManagementFeeDue:     lhs.ManagementFeeDue.Add(rhs.ManagementFee),
	}
}

// ConstructLoanState builds a LoanState from the three tracked loan values,
// deriving interestDue (rippled constructLoanState).
func ConstructLoanState(totalValue, principal, managementFee N) LoanState {
	return LoanState{
		ValueOutstanding:     totalValue,
		PrincipalOutstanding: principal,
		InterestDue:          totalValue.Sub(principal).Sub(managementFee),
		ManagementFeeDue:     managementFee,
	}
}

// computeTheoreticalLoanState computes the full-precision loan state for a point
// in the amortization schedule (rippled computeTheoreticalLoanState, eqs. 30-33).
func computeTheoreticalLoanState(periodicPayment, periodicRate N, paymentRemaining uint32, managementFeeRate uint32) LoanState {
	if paymentRemaining == 0 {
		return LoanState{zeroN(), zeroN(), zeroN(), zeroN()}
	}
	totalValueOutstanding := periodicPayment.Mul(numU(paymentRemaining))
	principalOutstanding := loanPrincipalFromPeriodicPayment(periodicPayment, periodicRate, paymentRemaining)
	interestGross := totalValueOutstanding.Sub(principalOutstanding)
	managementFee := tenthBipsOfValue(interestGross, managementFeeRate)
	interestNet := interestGross.Sub(managementFee)
	return LoanState{
		ValueOutstanding:     totalValueOutstanding,
		PrincipalOutstanding: principalOutstanding,
		InterestDue:          interestNet,
		ManagementFeeDue:     managementFee,
	}
}

// ComputeLoanPropertiesRate derives a loan's properties from the periodic rate
// (rippled computeLoanProperties, the Number-rate overload).
func ComputeLoanPropertiesRate(asset Asset, principalOutstanding N, periodicRate N, paymentsRemaining uint32, managementFeeRate uint32, minimumScale int) LoanProperties {
	periodicPayment := loanPeriodicPayment(principalOutstanding, periodicRate, paymentsRemaining)

	// Guard block: round the total value upward when interest is charged,
	// to-nearest for a zero-rate loan. The scale is derived from that value.
	guardMode := state.RoundUpward
	if periodicRate.IsZero() {
		guardMode = state.RoundToNearest
	}
	product := periodicPayment.MulRounded(numU(paymentsRemaining), guardMode)
	loanScale := minimumScale
	if e := product.AssetExponent(asset.Integral, guardMode); e > loanScale {
		loanScale = e
	}
	totalValueOutstanding := roundToAsset(asset, product, loanScale, guardMode)

	roundedPrincipal := roundToAssetNearest(asset, principalOutstanding, loanScale)
	totalInterest := totalValueOutstanding.Sub(roundedPrincipal)
	feeOwed := computeManagementFee(asset, totalInterest, managementFeeRate, loanScale)

	startingState := computeTheoreticalLoanState(periodicPayment, periodicRate, paymentsRemaining, managementFeeRate)
	firstPaymentState := LoanState{zeroN(), zeroN(), zeroN(), zeroN()}
	if paymentsRemaining >= 1 {
		firstPaymentState = computeTheoreticalLoanState(periodicPayment, periodicRate, paymentsRemaining-1, managementFeeRate)
	}
	firstPaymentPrincipal := startingState.PrincipalOutstanding.Sub(firstPaymentState.PrincipalOutstanding)

	return LoanProperties{
		PeriodicPayment:       periodicPayment,
		LoanState:             ConstructLoanState(totalValueOutstanding, roundedPrincipal, feeOwed),
		LoanScale:             loanScale,
		FirstPaymentPrincipal: firstPaymentPrincipal,
	}
}

// ComputeLoanProperties derives a loan's properties from the annualized interest
// rate and payment interval (rippled computeLoanProperties, the rate overload).
func ComputeLoanProperties(asset Asset, principalOutstanding N, interestRate uint32, paymentInterval uint32, paymentsRemaining uint32, managementFeeRate uint32, minimumScale int) LoanProperties {
	periodicRate := LoanPeriodicRate(interestRate, paymentInterval)
	return ComputeLoanPropertiesRate(asset, principalOutstanding, periodicRate, paymentsRemaining, managementFeeRate, minimumScale)
}

// CheckLoanGuards validates that a loan can be amortized as specified (rippled
// checkLoanGuards). Returns tesSUCCESS or the failing tec.
func CheckLoanGuards(asset Asset, principalRequested N, expectInterest bool, paymentTotal uint32, properties LoanProperties) ter.Result {
	totalInterestOutstanding := properties.LoanState.ValueOutstanding.Sub(principalRequested)
	if expectInterest && totalInterestOutstanding.Signum() <= 0 {
		return ter.TecPRECISION_LOSS
	}
	if !expectInterest && totalInterestOutstanding.Signum() > 0 {
		return ter.TecINTERNAL
	}
	if properties.FirstPaymentPrincipal.Signum() <= 0 {
		return ter.TecPRECISION_LOSS
	}
	roundedPayment := roundPeriodicPayment(asset, properties.PeriodicPayment, properties.LoanScale)
	if roundedPayment.IsZero() {
		return ter.TecPRECISION_LOSS
	}
	// Guard 4: the loan must amortize in exactly paymentTotal payments. rippled
	// divides under Number::upward and truncates toward zero to an int64.
	computedPayments := properties.LoanState.ValueOutstanding.DivRounded(roundedPayment, state.RoundUpward).ToInt64WithMode(state.RoundTowardsZero)
	if computedPayments != int64(paymentTotal) {
		return ter.TecPRECISION_LOSS
	}
	return ter.TesSUCCESS
}

// computeOverpaymentComponents computes the payment components for an overpayment
// (rippled computeOverpaymentComponents, eqs. 20-22).
func computeOverpaymentComponents(asset Asset, loanScale int, overpayment N, overpaymentInterestRate, overpaymentFeeRate, managementFeeRate uint32) ExtendedPaymentComponents {
	overpaymentFee := roundToAssetNearest(asset, tenthBipsOfValue(overpayment, overpaymentFeeRate), loanScale)
	interest := roundToAssetNearest(asset, tenthBipsOfValue(overpayment, overpaymentInterestRate), loanScale)
	roundedInterest, roundedManagementFee := computeInterestAndFeeParts(asset, interest, managementFeeRate, loanScale)
	p := PaymentComponents{
		TrackedValueDelta:         overpayment.Sub(overpaymentFee),
		TrackedPrincipalDelta:     overpayment.Sub(roundedInterest).Sub(roundedManagementFee).Sub(overpaymentFee),
		TrackedManagementFeeDelta: roundedManagementFee,
		SpecialCase:               SpecialExtra,
	}
	return newExtended(p, overpaymentFee, roundedInterest)
}

// computePaymentComponents splits a scheduled periodic payment into principal,
// interest, and management-fee components, correcting accumulated rounding
// (rippled computePaymentComponents).
func computePaymentComponents(asset Asset, scale int, totalValueOutstanding, principalOutstanding, managementFeeOutstanding, periodicPayment, periodicRate N, paymentRemaining uint32, managementFeeRate uint32) PaymentComponents {
	roundedPeriodicPayment := roundPeriodicPayment(asset, periodicPayment, scale)

	if paymentRemaining == 1 || totalValueOutstanding.Cmp(roundedPeriodicPayment) <= 0 {
		return PaymentComponents{
			TrackedValueDelta:         totalValueOutstanding,
			TrackedPrincipalDelta:     principalOutstanding,
			TrackedManagementFeeDelta: managementFeeOutstanding,
			SpecialCase:               SpecialFinal,
		}
	}

	trueTarget := computeTheoreticalLoanState(periodicPayment, periodicRate, paymentRemaining-1, managementFeeRate)
	roundedTarget := LoanState{
		ValueOutstanding:     roundToAssetNearest(asset, trueTarget.ValueOutstanding, scale),
		PrincipalOutstanding: roundToAssetNearest(asset, trueTarget.PrincipalOutstanding, scale),
		InterestDue:          roundToAssetNearest(asset, trueTarget.InterestDue, scale),
		ManagementFeeDue:     roundToAssetNearest(asset, trueTarget.ManagementFeeDue, scale),
	}

	currentLedgerState := ConstructLoanState(totalValueOutstanding, principalOutstanding, managementFeeOutstanding)
	deltas := subStates(currentLedgerState, roundedTarget)
	deltas.nonNegative()

	deltas.Principal = minN(deltas.Principal, currentLedgerState.PrincipalOutstanding)
	deltas.Interest = minN(minN(deltas.Interest, maxN(zeroN(), roundedPeriodicPayment.Sub(deltas.Principal))), currentLedgerState.InterestDue)
	deltas.ManagementFee = minN(minN(deltas.ManagementFee, roundedPeriodicPayment.Sub(deltas.Principal.Add(deltas.Interest))), currentLedgerState.ManagementFeeDue)

	takeFrom := func(component, excess *N) {
		if gtZero(*excess) {
			part := minN(*component, *excess)
			*component = component.Sub(part)
			*excess = excess.Sub(part)
		}
	}
	addressExcess := func(d *LoanStateDeltas, excess *N) {
		takeFrom(&d.Interest, excess)
		takeFrom(&d.ManagementFee, excess)
		takeFrom(&d.Principal, excess)
	}

	totalOverpayment := deltas.Total().Sub(currentLedgerState.ValueOutstanding)
	if gtZero(totalOverpayment) {
		addressExcess(&deltas, &totalOverpayment)
	}

	shortage := roundedPeriodicPayment.Sub(deltas.Total())
	if isNeg(shortage) {
		excess := shortage.Negate()
		addressExcess(&deltas, &excess)
	}

	return PaymentComponents{
		TrackedValueDelta:         clampN(deltas.Total(), zeroN(), currentLedgerState.ValueOutstanding),
		TrackedPrincipalDelta:     clampN(deltas.Principal, zeroN(), currentLedgerState.PrincipalOutstanding),
		TrackedManagementFeeDelta: clampN(deltas.ManagementFee, zeroN(), currentLedgerState.ManagementFeeDue),
	}
}

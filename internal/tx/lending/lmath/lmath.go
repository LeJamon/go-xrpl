// Package lmath ports the XLS-66 lending amortization math from rippled 3.1.0
// (src/xrpld/app/misc/detail/LendingHelpers.cpp) onto the XRPLNumber
// foundation. Every value is an XRPLNumber in the large mantissa scale that a
// lending/SingleAssetVault transaction context installs (10^18..10^19-1).
//
// Rounding modes. rippled carries the active Number rounding mode in a
// thread_local; go-xrpl threads it explicitly. All ordinary arithmetic
// (Add/Sub/Mul/Div) uses round-to-nearest, matching rippled's default global
// mode. An explicit mode appears only where rippled installs a
// NumberRoundModeGuard: inside roundToAsset, in computeLoanProperties' total-value
// block, and in checkLoanGuards' payment-count division.
package lmath

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/protocol"
)

// Asset is the minimal view of a vault asset the amortization math needs: only
// whether it is integral (native XRP or MPT, rounded to whole units) or an IOU
// (rounded to a decimal scale). Currency/issuer never affect the numeric result.
type Asset struct {
	Integral bool
}

// N is a large-scale XRPLNumber, the type every lending computation runs on.
type N = state.XRPLNumber

// num builds a large-scale Number from mantissa/exponent.
func num(mantissa int64, exponent int) N {
	return state.NewXRPLNumberScaled(mantissa, exponent, state.MantissaScaleLarge, state.RoundToNearest)
}

// Num builds a large-scale lending Number from mantissa/exponent (exported for
// the transactor SLE seam).
func Num(mantissa int64, exponent int) N { return num(mantissa, exponent) }

// NumScaled builds a lending Number in the transaction-selected mantissa range.
func NumScaled(mantissa int64, exponent int, scale state.MantissaScale) N {
	return state.NewXRPLNumberScaled(mantissa, exponent, scale, state.RoundToNearest)
}

// Zero returns the large-scale lending zero.
func Zero() N { return zeroN() }

// FromInt builds a large-scale Number from a plain integer.
func FromInt(v int64) N { return num(v, 0) }

// FromDrops builds a large-scale Number from an XRP drops / MPT unit count.
func FromDrops(v int64) N { return num(v, 0) }

// FromDropsScaled builds an XRP drops or MPT unit count in the transaction-selected range.
func FromDropsScaled(v int64, scale state.MantissaScale) N { return NumScaled(v, 0, scale) }

// numU builds a large-scale Number from an unsigned integer.
func numU(v uint32) N { return num(int64(v), 0) }

func numLike(reference N, mantissa int64, exponent int) N {
	return NumScaled(mantissa, exponent, reference.MantissaScale())
}

func numULike(reference N, v uint32) N { return numLike(reference, int64(v), 0) }
func zeroLike(reference N) N           { return numLike(reference, 0, 0) }
func oneLike(reference N) N            { return numLike(reference, 1, 0) }

// zero and one in the lending scale.
func zeroN() N { return num(0, 0) }
func oneN() N  { return num(1, 0) }

// roundToAsset rounds value to the asset's precision at decimal scale under mode
// (rippled roundToAsset(asset, value, scale, rounding)).
func roundToAsset(asset Asset, value N, scale int, mode state.RoundingMode) N {
	return value.RoundToAssetScale(asset.Integral, scale, mode)
}

// roundToAssetNearest is roundToAsset with the default (nearest) mode.
func roundToAssetNearest(asset Asset, value N, scale int) N {
	return value.RoundToAssetScale(asset.Integral, scale, state.RoundToNearest)
}

// roundPeriodicPayment rounds a periodic payment consistently upward (rippled
// roundPeriodicPayment).
func roundPeriodicPayment(asset Asset, periodicPayment N, scale int) N {
	return roundToAsset(asset, periodicPayment, scale, state.RoundUpward)
}

// tenthBipsOfValue returns value * tenthBips / 100000 (rippled tenthBipsOfValue).
func tenthBipsOfValue(value N, tenthBips uint32) N {
	if tenthBips == 0 || value.IsZero() {
		return zeroLike(value)
	}
	return value.Mul(numULike(value, tenthBips)).Div(numULike(value, protocol.TenthBipsPerUnity))
}

// TenthBipsOfValue returns value * tenthBips / 100000 (exported for the cover /
// debt rate computations in the transactors).
func TenthBipsOfValue(value N, tenthBips uint32) N { return tenthBipsOfValue(value, tenthBips) }

// TenthBipsOfValueRounded is TenthBipsOfValue with an explicit rounding mode on
// the multiply and divide, mirroring rippled's NumberRoundModeGuard around the
// broker cover-rate computations.
func TenthBipsOfValueRounded(value N, tenthBips uint32, mode state.RoundingMode) N {
	if tenthBips == 0 || value.IsZero() {
		return zeroLike(value)
	}
	return value.MulRounded(numULike(value, tenthBips), mode).DivRounded(numULike(value, protocol.TenthBipsPerUnity), mode)
}

// RoundAssetUpward / RoundAssetDownward / RoundAssetNearest expose roundToAsset
// at a decimal scale under a fixed mode for the transactor cover/debt math.
func RoundAssetUpward(asset Asset, value N, scale int) N {
	return roundToAsset(asset, value, scale, state.RoundUpward)
}

func RoundAssetDownward(asset Asset, value N, scale int) N {
	return roundToAsset(asset, value, scale, state.RoundDownward)
}

func RoundAssetNearest(asset Asset, value N, scale int) N {
	return roundToAssetNearest(asset, value, scale)
}

// RoundAssetTowardsZero rounds value to the asset scale toward zero.
func RoundAssetTowardsZero(asset Asset, value N, scale int) N {
	return value.RoundToAssetScale(asset.Integral, scale, state.RoundTowardsZero)
}

// AdjustImprecise re-rounds value+adjustment to the vault scale and floors it at
// zero, avoiding accumulated dust (rippled adjustImpreciseNumber).
func AdjustImprecise(asset Asset, value, adjustment N, vaultScale int) N {
	v := roundToAssetNearest(asset, value.Add(adjustment), vaultScale)
	if v.Signum() < 0 {
		return zeroLike(value)
	}
	return v
}

// IsRounded reports whether value already sits at the asset's precision for
// scale: rounding down and up agree (rippled isRounded). The Loan* transactors
// use it for the tecPRECISION_LOSS field checks.
func IsRounded(asset Asset, value N, scale int) bool {
	down := roundToAsset(asset, value, scale, state.RoundDownward)
	up := roundToAsset(asset, value, scale, state.RoundUpward)
	return down.Equal(up)
}

// LoanPeriodicRate converts an annualized rate (1/10 bips) to a per-period rate,
// prorated by the payment interval in seconds (rippled loanPeriodicRate,
// XLS-66 eq. 1).
func LoanPeriodicRate(interestRate uint32, paymentInterval uint32) N {
	return tenthBipsOfValue(numU(paymentInterval), interestRate).Div(numU(protocol.SecondsInYear))
}

func loanPeriodicRateLike(reference N, interestRate uint32, paymentInterval uint32) N {
	return tenthBipsOfValue(numULike(reference, paymentInterval), interestRate).
		Div(numULike(reference, protocol.SecondsInYear))
}

// computeRaisedRate returns (1 + periodicRate)^paymentsRemaining (rippled
// computeRaisedRate, eq. 5).
func computeRaisedRate(periodicRate N, paymentsRemaining uint32) N {
	return oneLike(periodicRate).Add(periodicRate).Power(paymentsRemaining)
}

// computePowerMinusOne evaluates (1 + r)^n - 1 by summing the binomial
// expansion, a sum of positive terms for r >= 0 that avoids the catastrophic
// cancellation of the direct closed form at near-zero rates. The loop stops once
// a term falls below Number precision (rippled computePowerMinusOne).
func computePowerMinusOne(periodicRate N, paymentsRemaining uint32) N {
	if paymentsRemaining == 0 || periodicRate.IsZero() {
		return zeroLike(periodicRate)
	}
	term := numULike(periodicRate, paymentsRemaining).Mul(periodicRate)
	sum := term
	for k := uint32(1); k < paymentsRemaining; k++ {
		term = term.Mul(periodicRate).Mul(numULike(periodicRate, paymentsRemaining-k)).Div(numULike(periodicRate, k+1))
		next := sum.Add(term)
		if next.Equal(sum) {
			break
		}
		sum = next
	}
	return sum
}

// computePowerMinusOneHybrid evaluates (1 + r)^n - 1, routing the near-zero
// regime (r*n below 1e-9) through the binomial expansion and everything else
// through the faster closed form (rippled computePowerMinusOneHybrid).
func computePowerMinusOneHybrid(periodicRate N, paymentsRemaining uint32) N {
	if paymentsRemaining == 0 || periodicRate.IsZero() {
		return zeroLike(periodicRate)
	}
	if numULike(periodicRate, paymentsRemaining).Mul(periodicRate).Cmp(numLike(periodicRate, 1, -9)) >= 0 {
		return computeRaisedRate(periodicRate, paymentsRemaining).Sub(oneLike(periodicRate))
	}
	return computePowerMinusOne(periodicRate, paymentsRemaining)
}

// computePaymentFactor converts principal to a periodic payment amount (rippled
// computePaymentFactor, eq. 6). Post-fixCleanup3_2_0 the (1+r)^n - 1 denominator
// uses the hybrid evaluator, avoiding cancellation at near-zero rates.
func computePaymentFactor(fix320 bool, periodicRate N, paymentsRemaining uint32) N {
	if paymentsRemaining == 0 {
		return zeroLike(periodicRate)
	}
	if periodicRate.IsZero() {
		return oneLike(periodicRate).Div(numULike(periodicRate, paymentsRemaining))
	}
	if fix320 {
		raisedMinusOne := computePowerMinusOneHybrid(periodicRate, paymentsRemaining)
		return periodicRate.Mul(oneLike(periodicRate).Add(raisedMinusOne)).Div(raisedMinusOne)
	}
	raised := computeRaisedRate(periodicRate, paymentsRemaining)
	return periodicRate.Mul(raised).Div(raised.Sub(oneLike(periodicRate)))
}

// loanPeriodicPayment returns the standard amortized periodic payment (rippled
// loanPeriodicPayment, eq. 7).
func loanPeriodicPayment(fix320 bool, principalOutstanding N, periodicRate N, paymentsRemaining uint32) N {
	if principalOutstanding.IsZero() || paymentsRemaining == 0 {
		return zeroLike(principalOutstanding)
	}
	if periodicRate.IsZero() {
		return principalOutstanding.Div(numULike(principalOutstanding, paymentsRemaining))
	}
	return principalOutstanding.Mul(computePaymentFactor(fix320, periodicRate, paymentsRemaining))
}

// loanPrincipalFromPeriodicPayment reverse-computes principal from a periodic
// payment (rippled loanPrincipalFromPeriodicPayment, eq. 10).
func loanPrincipalFromPeriodicPayment(fix320 bool, periodicPayment N, periodicRate N, paymentsRemaining uint32) N {
	if paymentsRemaining == 0 {
		return zeroLike(periodicPayment)
	}
	if periodicRate.IsZero() {
		return periodicPayment.Mul(numULike(periodicPayment, paymentsRemaining))
	}
	return periodicPayment.Div(computePaymentFactor(fix320, periodicRate, paymentsRemaining))
}

// computeManagementFee returns the broker's fee on an interest amount, rounded
// down to the asset scale (rippled computeManagementFee, eq. 32).
func computeManagementFee(asset Asset, value N, managementFeeRate uint32, scale int) N {
	return roundToAsset(asset, tenthBipsOfValue(value, managementFeeRate), scale, state.RoundDownward)
}

// computeInterestAndFeeParts splits an interest amount into the net interest
// (to the vault) and the management fee (to the broker) (rippled
// computeInterestAndFeeParts, eq. 33).
func computeInterestAndFeeParts(asset Asset, interest N, managementFeeRate uint32, scale int) (interestPart, feePart N) {
	fee := computeManagementFee(asset, interest, managementFeeRate, scale)
	return interest.Sub(fee), fee
}

// loanLatePaymentInterest returns penalty interest accrued on an overdue payment
// (rippled loanLatePaymentInterest, eq. 16). now is the parent close time in
// seconds since the Ripple epoch.
func loanLatePaymentInterest(principalOutstanding N, lateInterestRate uint32, now uint32, nextPaymentDueDate uint32) N {
	if principalOutstanding.IsZero() || lateInterestRate == 0 {
		return zeroLike(principalOutstanding)
	}
	if now <= nextPaymentDueDate {
		return zeroLike(principalOutstanding)
	}
	secondsOverdue := now - nextPaymentDueDate
	rate := loanPeriodicRateLike(principalOutstanding, lateInterestRate, secondsOverdue)
	return principalOutstanding.Mul(rate)
}

// loanAccruedInterest returns interest accrued since the last payment (rippled
// loanAccruedInterest, eq. 27). Returns zero if the loan is paid ahead.
func loanAccruedInterest(principalOutstanding N, periodicRate N, now uint32, startDate uint32, prevPaymentDate uint32, paymentInterval uint32) N {
	if periodicRate.IsZero() || paymentInterval == 0 {
		return zeroLike(principalOutstanding)
	}
	lastPaymentDate := prevPaymentDate
	if startDate > lastPaymentDate {
		lastPaymentDate = startDate
	}
	if now <= lastPaymentDate {
		return zeroLike(principalOutstanding)
	}
	secondsSinceLastPayment := now - lastPaymentDate
	// Multiply before dividing to limit rounding error amplification.
	return principalOutstanding.Mul(periodicRate).Mul(numULike(principalOutstanding, secondsSinceLastPayment)).Div(numULike(principalOutstanding, paymentInterval))
}

// ComputeFullPaymentInterest returns accrued interest plus prepayment penalty
// for an early full payment (rippled computeFullPaymentInterest, eqs. 27-28).
func ComputeFullPaymentInterest(theoreticalPrincipalOutstanding N, periodicRate N, now uint32, paymentInterval uint32, prevPaymentDate uint32, startDate uint32, closeInterestRate uint32) N {
	accrued := loanAccruedInterest(theoreticalPrincipalOutstanding, periodicRate, now, startDate, prevPaymentDate, paymentInterval)
	var penalty N
	if closeInterestRate == 0 {
		penalty = zeroLike(theoreticalPrincipalOutstanding)
	} else {
		penalty = tenthBipsOfValue(theoreticalPrincipalOutstanding, closeInterestRate)
	}
	return accrued.Add(penalty)
}

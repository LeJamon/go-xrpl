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

// numU builds a large-scale Number from an unsigned integer.
func numU(v uint32) N { return num(int64(v), 0) }

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
		return zeroN()
	}
	return value.Mul(numU(tenthBips)).Div(numU(protocol.TenthBipsPerUnity))
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

// computeRaisedRate returns (1 + periodicRate)^paymentsRemaining (rippled
// computeRaisedRate, eq. 5).
func computeRaisedRate(periodicRate N, paymentsRemaining uint32) N {
	return oneN().Add(periodicRate).Power(paymentsRemaining)
}

// computePaymentFactor converts principal to a periodic payment amount (rippled
// computePaymentFactor, eq. 6).
func computePaymentFactor(periodicRate N, paymentsRemaining uint32) N {
	if paymentsRemaining == 0 {
		return zeroN()
	}
	if periodicRate.IsZero() {
		return oneN().Div(numU(paymentsRemaining))
	}
	raised := computeRaisedRate(periodicRate, paymentsRemaining)
	return periodicRate.Mul(raised).Div(raised.Sub(oneN()))
}

// loanPeriodicPayment returns the standard amortized periodic payment (rippled
// loanPeriodicPayment, eq. 7).
func loanPeriodicPayment(principalOutstanding N, periodicRate N, paymentsRemaining uint32) N {
	if principalOutstanding.IsZero() || paymentsRemaining == 0 {
		return zeroN()
	}
	if periodicRate.IsZero() {
		return principalOutstanding.Div(numU(paymentsRemaining))
	}
	return principalOutstanding.Mul(computePaymentFactor(periodicRate, paymentsRemaining))
}

// loanPrincipalFromPeriodicPayment reverse-computes principal from a periodic
// payment (rippled loanPrincipalFromPeriodicPayment, eq. 10).
func loanPrincipalFromPeriodicPayment(periodicPayment N, periodicRate N, paymentsRemaining uint32) N {
	if paymentsRemaining == 0 {
		return zeroN()
	}
	if periodicRate.IsZero() {
		return periodicPayment.Mul(numU(paymentsRemaining))
	}
	return periodicPayment.Div(computePaymentFactor(periodicRate, paymentsRemaining))
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
		return zeroN()
	}
	if now <= nextPaymentDueDate {
		return zeroN()
	}
	secondsOverdue := now - nextPaymentDueDate
	rate := LoanPeriodicRate(lateInterestRate, secondsOverdue)
	return principalOutstanding.Mul(rate)
}

// loanAccruedInterest returns interest accrued since the last payment (rippled
// loanAccruedInterest, eq. 27). Returns zero if the loan is paid ahead.
func loanAccruedInterest(principalOutstanding N, periodicRate N, now uint32, startDate uint32, prevPaymentDate uint32, paymentInterval uint32) N {
	if periodicRate.IsZero() || paymentInterval == 0 {
		return zeroN()
	}
	lastPaymentDate := prevPaymentDate
	if startDate > lastPaymentDate {
		lastPaymentDate = startDate
	}
	if now <= lastPaymentDate {
		return zeroN()
	}
	secondsSinceLastPayment := now - lastPaymentDate
	// Multiply before dividing to limit rounding error amplification.
	return principalOutstanding.Mul(periodicRate).Mul(numU(secondsSinceLastPayment)).Div(numU(paymentInterval))
}

// ComputeFullPaymentInterest returns accrued interest plus prepayment penalty
// for an early full payment (rippled computeFullPaymentInterest, eqs. 27-28).
func ComputeFullPaymentInterest(theoreticalPrincipalOutstanding N, periodicRate N, now uint32, paymentInterval uint32, prevPaymentDate uint32, startDate uint32, closeInterestRate uint32) N {
	accrued := loanAccruedInterest(theoreticalPrincipalOutstanding, periodicRate, now, startDate, prevPaymentDate, paymentInterval)
	var penalty N
	if closeInterestRate == 0 {
		penalty = zeroN()
	} else {
		penalty = tenthBipsOfValue(theoreticalPrincipalOutstanding, closeInterestRate)
	}
	return accrued.Add(penalty)
}

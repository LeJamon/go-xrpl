package lmath

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func init() { state.SetNumberSwitchover(true) }

// month is the 30-day payment interval used across the rippled golden vectors.
const month uint32 = 30 * 24 * 60 * 60

func eq(t *testing.T, name string, got, want N) {
	t.Helper()
	if !got.Equal(want) {
		t.Errorf("%s: got %s, want %s", name, got.String(), want.String())
	}
}

// Golden vectors transcribed from rippled 3.1.0 LendingHelpers_test.cpp.

func TestComputeRaisedRate(t *testing.T) {
	cases := []struct {
		name string
		rate N
		n    uint32
		want N
	}{
		{"zero payments", num(5, -2), 0, num(1, 0)},
		{"one payment", num(5, -2), 1, num(105, -2)},
		{"multiple payments", num(5, -2), 3, num(1157625, -6)},
		{"zero rate", num(0, 0), 5, num(1, 0)},
	}
	for _, tc := range cases {
		eq(t, tc.name, computeRaisedRate(tc.rate, tc.n), tc.want)
	}
}

func TestComputePaymentFactor(t *testing.T) {
	cases := []struct {
		name string
		rate N
		n    uint32
		want N
	}{
		{"zero rate", num(0, 0), 4, num(25, -2)},
		{"one payment", num(5, -2), 1, num(105, -2)},
		{"multiple payments", num(5, -2), 3, num(3672085646312450436, -19)},
		{"zero payments", num(5, -2), 0, num(0, 0)},
	}
	for _, tc := range cases {
		eq(t, tc.name, computePaymentFactor(false, tc.rate, tc.n), tc.want)
	}
}

func TestLoanPeriodicPayment(t *testing.T) {
	stdRate := LoanPeriodicRate(100_000, month)
	cases := []struct {
		name      string
		principal N
		rate      N
		n         uint32
		want      N
	}{
		{"zero principal", num(0, 0), num(5, -2), 5, num(0, 0)},
		{"zero payments", num(1000, 0), num(5, -2), 0, num(0, 0)},
		{"zero rate", num(1000, 0), num(0, 0), 4, num(250, 0)},
		{"standard", num(1000, 0), stdRate, 3, num(389569066396123265, -15)},
	}
	for _, tc := range cases {
		eq(t, tc.name, loanPeriodicPayment(false, tc.principal, tc.rate, tc.n), tc.want)
	}
}

func TestLoanPrincipalFromPeriodicPayment(t *testing.T) {
	stdRate := LoanPeriodicRate(100_000, month)
	cases := []struct {
		name    string
		payment N
		rate    N
		n       uint32
		want    N
	}{
		{"zero payment", num(0, 0), num(5, -2), 5, num(0, 0)},
		{"zero payments", num(1000, 0), num(5, -2), 0, num(0, 0)},
		{"zero rate", num(250, 0), num(0, 0), 4, num(1000, 0)},
		{"standard", num(389569066396123265, -15), stdRate, 3, num(1000, 0)},
	}
	for _, tc := range cases {
		eq(t, tc.name, loanPrincipalFromPeriodicPayment(false, tc.payment, tc.rate, tc.n), tc.want)
	}
}

func TestComputeInterestAndFeeParts(t *testing.T) {
	iou := Asset{Integral: false}
	const scale = 1
	cases := []struct {
		name         string
		interest     N
		rate         uint32
		wantInterest N
		wantFee      N
	}{
		{"zero interest", num(0, 0), 10_000, num(0, 0), num(0, 0)},
		{"zero fee rate", num(1000, 0), 0, num(1000, 0), num(0, 0)},
		{"10% fee", num(1000, 0), 10_000, num(900, 0), num(100, 0)},
	}
	for _, tc := range cases {
		gotI, gotF := computeInterestAndFeeParts(iou, tc.interest, tc.rate, scale)
		eq(t, tc.name+" interest", gotI, tc.wantInterest)
		eq(t, tc.name+" fee", gotF, tc.wantFee)
	}
}

func TestLoanLatePaymentInterest(t *testing.T) {
	cases := []struct {
		name      string
		principal N
		rate      uint32
		now       uint32
		due       uint32
		want      N
	}{
		{"on-time", num(1000, 0), 10_000, 3000, 3000, num(0, 0)},
		{"early", num(1000, 0), 10_000, 3000, 4000, num(0, 0)},
		{"no principal", num(0, 0), 10_000, 3000, 2000, num(0, 0)},
		{"no late rate", num(1000, 0), 0, 3000, 2000, num(0, 0)},
		{"late", num(1000, 0), 100_000, 3000, 2000, num(317097919837645865, -19)},
	}
	for _, tc := range cases {
		eq(t, tc.name, loanLatePaymentInterest(tc.principal, tc.rate, tc.now, tc.due), tc.want)
	}
}

func TestLoanAccruedInterest(t *testing.T) {
	cases := []struct {
		name      string
		principal N
		rate      N
		now       uint32
		start     uint32
		prev      uint32
		interval  uint32
		want      N
	}{
		{"zero principal", num(0, 0), num(5, -2), 3000, 2000, 2500, month, num(0, 0)},
		{"before start", num(1000, 0), num(5, -2), 1000, 2000, 1500, month, num(0, 0)},
		{"zero rate", num(1000, 0), num(0, 0), 3000, 2000, 2500, month, num(0, 0)},
		{"zero interval", num(1000, 0), num(5, -2), 3000, 2000, 2500, 0, num(0, 0)},
		{"standard", num(1000, 0), num(5, -2), 3000, 1000, 2000, month, num(1929012345679012346, -20)},
	}
	for _, tc := range cases {
		eq(t, tc.name, loanAccruedInterest(tc.principal, tc.rate, tc.now, tc.start, tc.prev, tc.interval), tc.want)
	}
}

func TestComputeFullPaymentInterest(t *testing.T) {
	cases := []struct {
		name      string
		principal N
		rate      N
		now       uint32
		interval  uint32
		prev      uint32
		start     uint32
		closeRate uint32
		want      N
	}{
		{"zero principal", num(0, 0), num(5, -2), 3000, month, 2000, 1000, 10_000, num(0, 0)},
		{"zero close rate", num(1000, 0), num(5, -2), 3000, month, 2000, 1000, 0, num(1929012345679012346, -20)},
		{"standard", num(1000, 0), num(5, -2), 3000, month, 2000, 1000, 10_000, num(1000192901234567901, -16)},
	}
	for _, tc := range cases {
		got := ComputeFullPaymentInterest(tc.principal, tc.rate, tc.now, tc.interval, tc.prev, tc.start, tc.closeRate)
		eq(t, tc.name, got, tc.want)
	}
}

func TestComputeOverpaymentComponents(t *testing.T) {
	iou := Asset{Integral: false}
	const scale = 1
	c := computeOverpaymentComponents(iou, scale, num(1000, 0), 10_000, 50_000, 10_000)
	eq(t, "untrackedManagementFee", c.UntrackedManagementFee, num(500, 0))
	eq(t, "untrackedInterest", c.UntrackedInterest, num(90, 0))
	eq(t, "trackedInterestPart", c.TrackedInterestPart(), num(90, 0))
	eq(t, "trackedManagementFeeDelta", c.TrackedManagementFeeDelta, num(10, 0))
	eq(t, "trackedPrincipalDelta", c.TrackedPrincipalDelta, num(400, 0))
	// gross interest = tracked fee + untracked interest = 100
	eq(t, "grossInterest", c.TrackedManagementFeeDelta.Add(c.UntrackedInterest), num(100, 0))
	// all parts sum to the overpayment
	sum := c.TrackedManagementFeeDelta.Add(c.UntrackedInterest).Add(c.TrackedPrincipalDelta).Add(c.UntrackedManagementFee)
	eq(t, "sum", sum, num(1000, 0))
}

// TestComputePaymentFactor_HybridBothStates pins the fixCleanup3_2_0 payment
// factor: above the near-zero threshold both amendment states agree, but for a
// rate so small that r*n < 1e-9 the post-amendment path routes (1+r)^n - 1
// through the binomial expansion, avoiding the catastrophic cancellation of the
// direct closed form, so the two states diverge.
func TestComputePaymentFactor_HybridBothStates(t *testing.T) {
	rate := num(5, -2)
	if off, on := computePaymentFactor(false, rate, 3), computePaymentFactor(true, rate, 3); !off.Equal(on) {
		t.Fatalf("above threshold: off=%s on=%s must agree", off.String(), on.String())
	}

	tiny := num(1, -12)
	const n = 100
	off := computePaymentFactor(false, tiny, n)
	on := computePaymentFactor(true, tiny, n)
	if off.Equal(on) {
		t.Fatalf("near-zero rate: states must diverge (off=%s on=%s)", off.String(), on.String())
	}
	if on.Signum() <= 0 {
		t.Fatalf("near-zero factor must stay positive, got %s", on.String())
	}
}

// TestComputePowerMinusOne checks the binomial evaluator: above the near-zero
// regime the hybrid matches the closed form, and degenerate inputs are zero.
func TestComputePowerMinusOne(t *testing.T) {
	rate := num(5, -2)
	eq(t, "hybrid matches closed form", computePowerMinusOneHybrid(rate, 4), computeRaisedRate(rate, 4).Sub(oneN()))
	if !computePowerMinusOne(num(5, -2), 0).IsZero() || !computePowerMinusOne(num(0, 0), 5).IsZero() {
		t.Fatalf("degenerate powerMinusOne must be zero")
	}
}

func TestCheckLoanGuardsUpwardPaymentCount(t *testing.T) {
	parse := func(t *testing.T, scale state.MantissaScale, value string) N {
		t.Helper()
		n, err := state.ParseXRPLNumber(value, scale, state.RoundToNearest)
		if err != nil {
			t.Fatalf("ParseXRPLNumber(%q): %v", value, err)
		}
		return n
	}

	vectors := []struct {
		name                string
		asset               Asset
		principal           string
		periodicPayment     string
		valueOutstanding    string
		paymentTotal        uint32
		towardsZeroPayments int64
		loanScale           int
	}{
		{"IOU twelve payments", Asset{}, "1000", "83.33364250408379297", "1000.003710049006", 12, 11, -12},
		{"XRP twelve payments", Asset{Integral: true}, "1000000", "83333.33848930448955", "1000001", 12, 11, 0},
		{"IOU three payments", Asset{}, "20", "6.721536094178039612", "20.164608282535", 3, 2, -12},
	}
	scales := []struct {
		name  string
		scale state.MantissaScale
	}{
		{"small", state.MantissaScaleSmall},
		{"large legacy", state.MantissaScaleLargeLegacy},
		{"large", state.MantissaScaleLarge},
	}

	for _, vector := range vectors {
		t.Run(vector.name, func(t *testing.T) {
			for _, scale := range scales {
				t.Run(scale.name, func(t *testing.T) {
					principal := parse(t, scale.scale, vector.principal)
					properties := LoanProperties{
						PeriodicPayment: parse(t, scale.scale, vector.periodicPayment),
						LoanState: LoanState{
							ValueOutstanding: parse(t, scale.scale, vector.valueOutstanding),
						},
						LoanScale:             vector.loanScale,
						FirstPaymentPrincipal: parse(t, scale.scale, "1"),
					}
					roundedPayment := roundPeriodicPayment(vector.asset, properties.PeriodicPayment, properties.LoanScale)
					quotient := properties.LoanState.ValueOutstanding.DivRounded(roundedPayment, state.RoundUpward)

					if got := quotient.ToInt64WithMode(state.RoundTowardsZero); got != vector.towardsZeroPayments {
						t.Fatalf("toward-zero payment count = %d, want %d", got, vector.towardsZeroPayments)
					}
					if got := quotient.ToInt64WithMode(state.RoundUpward); got != int64(vector.paymentTotal) {
						t.Fatalf("upward payment count = %d, want %d", got, vector.paymentTotal)
					}
					if got := CheckLoanGuards(vector.asset, principal, true, vector.paymentTotal, properties); got != ter.TesSUCCESS {
						t.Fatalf("CheckLoanGuards = %v, want tesSUCCESS", got)
					}
				})
			}
		})
	}

	t.Run("loan set inputs", func(t *testing.T) {
		principal := parse(t, state.MantissaScaleLarge, "1000")
		properties := ComputeLoanProperties(true, Asset{}, principal, 500, 3600, 12, 0, -12)
		eq(t, "periodic payment", properties.PeriodicPayment, parse(t, state.MantissaScaleLarge, "83.33364250408379297"))
		eq(t, "value outstanding", properties.LoanState.ValueOutstanding, parse(t, state.MantissaScaleLarge, "1000.003710049006"))
		if properties.LoanScale != -12 {
			t.Fatalf("loan scale = %d, want -12", properties.LoanScale)
		}
		if got := CheckLoanGuards(Asset{}, principal, true, 12, properties); got != ter.TesSUCCESS {
			t.Fatalf("CheckLoanGuards = %v, want tesSUCCESS", got)
		}
	})
}

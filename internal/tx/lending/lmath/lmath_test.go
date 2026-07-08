package lmath

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
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
		eq(t, tc.name, computePaymentFactor(tc.rate, tc.n), tc.want)
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
		eq(t, tc.name, loanPeriodicPayment(tc.principal, tc.rate, tc.n), tc.want)
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
		eq(t, tc.name, loanPrincipalFromPeriodicPayment(tc.payment, tc.rate, tc.n), tc.want)
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

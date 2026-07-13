package state

import "testing"

func TestUniversalAmountArithmeticIgnoresLegacySwitchover(t *testing.T) {
	previous := GetNumberSwitchover()
	SetNumberSwitchover(false)
	defer SetNumberSwitchover(previous)

	one := NewIssuedAmountFromValue(1000000000000000, -15, "USD", "rIssuer")
	small := NewIssuedAmountFromValue(6000000000000000, -31, "USD", "rIssuer")
	sum, err := one.AddUniversal(small)
	if err != nil {
		t.Fatalf("AddUniversal: %v", err)
	}
	if got := sum.Value(); got != "1.000000000000001" {
		t.Fatalf("AddUniversal = %s, want 1.000000000000001", got)
	}

	almostOne := NewIssuedAmountFromValue(9999999999999999, -16, "USD", "rIssuer")
	difference, err := one.SubUniversal(almostOne)
	if err != nil {
		t.Fatalf("SubUniversal: %v", err)
	}
	if got := difference.IOU().NumberString(); got != "1e-16" {
		t.Fatalf("SubUniversal = %s, want 1e-16", got)
	}
}

func TestDivideNoIssueIntegralAndIssuedCombinations(t *testing.T) {
	iou := func(value string) Amount {
		amount, err := NewIssuedAmountFromDecimalString(value, "USD", "rIssuer")
		if err != nil {
			t.Fatalf("parse IOU %s: %v", value, err)
		}
		return amount
	}
	mpt := func(value int64) Amount {
		return NewMPTAmountWithIssuanceID(value, "", "00000001AE123A8556F3CF91154711376AFB0F894F832B3D")
	}

	tests := []struct {
		name     string
		num, den Amount
		want     string
	}{
		{name: "XRP over IOU", num: NewXRPAmountFromInt(1), den: iou("3"), want: "0.3333333333333334"},
		{name: "IOU over MPT", num: iou("2"), den: mpt(3), want: "0.6666666666666667"},
		{name: "MPT over XRP", num: mpt(2), den: NewXRPAmountFromInt(3), want: "0.6666666666666667"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := DivideNoIssue(test.num, test.den)
			if got.IsNative() || got.IsMPT() {
				t.Fatalf("DivideNoIssue returned an integral amount")
			}
			if value := got.IOU().NumberString(); value != test.want {
				t.Fatalf("DivideNoIssue = %s, want %s", value, test.want)
			}
		})
	}
}

func TestIOUAmountNumberStringScientificCompaction(t *testing.T) {
	tests := []struct {
		name   string
		amount IOUAmountValue
		want   string
	}{
		{name: "large positive", amount: NewIOUAmountValue(1000000000000000, 5), want: "1e20"},
		{name: "large negative", amount: NewIOUAmountValue(-1200000000000000, 5), want: "-12e19"},
		{name: "large-scale exponent zero", amount: NewIOUAmountValue(9007199254740993, 0), want: "9007199254740993e0"},
		{name: "large-scale plain exponent", amount: NewIOUAmountValue(1000000000000000, 3), want: "1000000000000000000"},
		{name: "small scientific", amount: NewIOUAmountValue(1000000000000000, -26), want: "1e-11"},
		{name: "plain threshold", amount: NewIOUAmountValue(1000000000000000, -25), want: "0.0000000001"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.amount.NumberString(); got != test.want {
				t.Fatalf("NumberString = %s, want %s", got, test.want)
			}
		})
	}
}

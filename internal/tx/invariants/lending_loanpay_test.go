package invariants

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
)

func loanPayInvariantRules(v11, fix320, fix340 bool) *amendment.Rules {
	features := [][32]byte{amendment.FeatureLendingProtocol}
	if v11 {
		features = append(features, amendment.FeatureLendingProtocolV1_1)
	}
	if fix320 {
		features = append(features, amendment.FeatureFixCleanup3_2_0)
	}
	if fix340 {
		features = append(features, amendment.FeatureFixCleanup3_4_0)
	}
	return amendment.NewRules(features)
}

func loanPayFixture(t *testing.T, beforeOverrides, afterOverrides map[string]any) InvariantEntry {
	t.Helper()
	before := loanInvariantMap(0, 2)
	before["PrincipalOutstanding"] = "90"
	before["TotalValueOutstanding"] = "100"
	before["ManagementFeeOutstanding"] = "0"
	before["NextPaymentDueDate"] = uint32(1100)
	after := loanInvariantMap(0, 1)
	after["PrincipalOutstanding"] = "89"
	after["TotalValueOutstanding"] = "99"
	after["ManagementFeeOutstanding"] = "0"
	after["NextPaymentDueDate"] = uint32(1200)
	for field, value := range beforeOverrides {
		before[field] = value
	}
	for field, value := range afterOverrides {
		after[field] = value
	}
	return InvariantEntry{
		EntryType: entry.TypeLoan,
		Before:    mustEncode(t, before),
		After:     mustEncode(t, after),
	}
}

func loanPayViolation(t *testing.T, rules *amendment.Rules, change InvariantEntry) *InvariantViolation {
	t.Helper()
	return checkValidLoanForTx(
		vvTx{txType: protocol.TxTypeLoanPay},
		TesSUCCESS,
		[]InvariantEntry{change},
		nil,
		rules,
	)
}

func loanPayRuleCases() []struct {
	name   string
	rules  *amendment.Rules
	enable bool
} {
	return []struct {
		name   string
		rules  *amendment.Rules
		enable bool
	}{
		{name: "V11 off cleanup320 off cleanup340 off", rules: loanPayInvariantRules(false, false, false)},
		{name: "V11 off cleanup320 off cleanup340 on", rules: loanPayInvariantRules(false, false, true)},
		{name: "V11 off cleanup320 on cleanup340 off", rules: loanPayInvariantRules(false, true, false)},
		{name: "V11 off cleanup320 on cleanup340 on", rules: loanPayInvariantRules(false, true, true)},
		{name: "V11 on cleanup320 off cleanup340 off", rules: loanPayInvariantRules(true, false, false), enable: true},
		{name: "V11 on cleanup320 off cleanup340 on", rules: loanPayInvariantRules(true, false, true), enable: true},
		{name: "V11 on cleanup320 on cleanup340 off", rules: loanPayInvariantRules(true, true, false), enable: true},
		{name: "V11 on cleanup320 on cleanup340 on", rules: loanPayInvariantRules(true, true, true), enable: true},
	}
}

func TestValidLoan_LoanPayNonFinalPostconditions(t *testing.T) {
	cases := []struct {
		name            string
		beforeOverrides map[string]any
		afterOverrides  map[string]any
		want            string
	}{
		{
			name:           "principal must not increase",
			afterOverrides: map[string]any{"PrincipalOutstanding": "95"},
			want:           "must not increase PrincipalOutstanding",
		},
		{
			name:           "total value must not increase",
			afterOverrides: map[string]any{"TotalValueOutstanding": "101"},
			want:           "must not increase TotalValueOutstanding",
		},
		{
			name: "one balance must decrease",
			afterOverrides: map[string]any{
				"PrincipalOutstanding":  "90",
				"TotalValueOutstanding": "100",
			},
			want: "must decrease PrincipalOutstanding or TotalValueOutstanding",
		},
		{
			name:           "payment remaining must decrease",
			afterOverrides: map[string]any{"PaymentRemaining": uint32(2)},
			want:           "must decrease PaymentRemaining",
		},
		{
			name:           "due date must advance",
			afterOverrides: map[string]any{"NextPaymentDueDate": uint32(1100)},
			want:           "must advance NextPaymentDueDate",
		},
		{
			name:           "due date must not move backward",
			afterOverrides: map[string]any{"NextPaymentDueDate": uint32(1000)},
			want:           "must advance NextPaymentDueDate",
		},
		{
			name:           "due date must use whole intervals",
			afterOverrides: map[string]any{"NextPaymentDueDate": uint32(1150)},
			want:           "must advance NextPaymentDueDate",
		},
		{
			name:           "payment interval must be nonzero",
			afterOverrides: map[string]any{"PaymentInterval": uint32(0)},
			want:           "must advance NextPaymentDueDate",
		},
		{
			name:           "payment remaining must not increase",
			afterOverrides: map[string]any{"PaymentRemaining": uint32(3)},
			want:           "must decrease PaymentRemaining",
		},
	}

	for _, rulesCase := range loanPayRuleCases() {
		t.Run(rulesCase.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					change := loanPayFixture(t, tc.beforeOverrides, tc.afterOverrides)
					violation := loanPayViolation(t, rulesCase.rules, change)
					if !rulesCase.enable {
						if violation != nil {
							t.Fatalf("legacy LoanPay invariant violation = %v", violation)
						}
						return
					}
					if violation == nil || !strings.Contains(violation.Message, tc.want) {
						t.Fatalf("LoanPay violation = %v, want message containing %q", violation, tc.want)
					}
				})
			}
		})
	}
}

func TestValidLoan_LoanPayNonFinalAcceptsValidBalanceTransitions(t *testing.T) {
	cases := []struct {
		name           string
		afterOverrides map[string]any
	}{
		{
			name:           "unchanged principal with declining total",
			afterOverrides: map[string]any{"PrincipalOutstanding": "90"},
		},
		{
			name: "declining principal with unchanged total",
			afterOverrides: map[string]any{
				"PrincipalOutstanding":  "89",
				"TotalValueOutstanding": "100",
			},
		},
		{
			name:           "both balances decline",
			afterOverrides: map[string]any{"PrincipalOutstanding": "89", "TotalValueOutstanding": "99"},
		},
		{
			name:           "due advances by multiple intervals",
			afterOverrides: map[string]any{"PrincipalOutstanding": "90", "NextPaymentDueDate": uint32(1300)},
		},
	}
	for _, rulesCase := range loanPayRuleCases() {
		if !rulesCase.enable {
			continue
		}
		t.Run(rulesCase.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					change := loanPayFixture(t, nil, tc.afterOverrides)
					if violation := loanPayViolation(t, rulesCase.rules, change); violation != nil {
						t.Fatalf("valid non-final LoanPay rejected: %v", violation)
					}
				})
			}
		})
	}
}

func TestValidLoan_LoanPayPostconditionsRequireSuccessfulLoanPay(t *testing.T) {
	change := loanPayFixture(t, nil, map[string]any{"PrincipalOutstanding": "95"})
	rules := loanPayInvariantRules(true, true, true)
	if violation := checkValidLoanForTx(vvTx{txType: protocol.TxTypeLoanPay}, TecINCOMPLETE, []InvariantEntry{change}, nil, rules); violation != nil {
		t.Fatalf("non-successful LoanPay postcondition rejected: %v", violation)
	}
	if violation := checkValidLoanForTx(vvTx{txType: protocol.TxTypePayment}, TesSUCCESS, []InvariantEntry{change}, nil, rules); violation != nil {
		t.Fatalf("non-LoanPay postcondition rejected: %v", violation)
	}
}

func TestValidLoan_LoanPayFullRepaymentKeepsZeroBalancesAndDueDate(t *testing.T) {
	before := loanInvariantMap(0, 1)
	before["PrincipalOutstanding"] = "90"
	before["TotalValueOutstanding"] = "100"
	before["ManagementFeeOutstanding"] = "0"
	before["NextPaymentDueDate"] = uint32(1100)
	for _, rulesCase := range loanPayRuleCases() {
		if !rulesCase.enable {
			continue
		}
		t.Run(rulesCase.name, func(t *testing.T) {
			after := loanInvariantMap(0, 0)
			after["PrincipalOutstanding"] = "0"
			after["TotalValueOutstanding"] = "0"
			after["ManagementFeeOutstanding"] = "0"
			after["NextPaymentDueDate"] = uint32(0)
			change := InvariantEntry{
				EntryType: entry.TypeLoan,
				Before:    mustEncode(t, before),
				After:     mustEncode(t, after),
			}
			if violation := loanPayViolation(t, rulesCase.rules, change); violation != nil {
				t.Fatalf("valid full repayment rejected: %v", violation)
			}
		})
	}
}

func TestValidLoan_LoanPayFullRepaymentRequiresZeroBalancesAndDueDate(t *testing.T) {
	for _, tc := range []struct {
		name           string
		afterOverrides map[string]any
		want           string
	}{
		{name: "principal", afterOverrides: map[string]any{"PrincipalOutstanding": "1"}, want: "zero payments remaining"},
		{name: "total value", afterOverrides: map[string]any{"TotalValueOutstanding": "1"}, want: "zero payments remaining"},
		{name: "management fee", afterOverrides: map[string]any{"ManagementFeeOutstanding": "1"}, want: "zero payments remaining"},
		{name: "due date", afterOverrides: map[string]any{"NextPaymentDueDate": uint32(1200)}, want: "zero payments must have zero next payment due date"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := loanInvariantMap(0, 1)
			before["PrincipalOutstanding"] = "90"
			before["TotalValueOutstanding"] = "100"
			before["ManagementFeeOutstanding"] = "0"
			before["NextPaymentDueDate"] = uint32(1100)
			after := loanInvariantMap(0, 0)
			after["PrincipalOutstanding"] = "0"
			after["TotalValueOutstanding"] = "0"
			after["ManagementFeeOutstanding"] = "0"
			after["NextPaymentDueDate"] = uint32(0)
			for field, value := range tc.afterOverrides {
				after[field] = value
			}
			change := InvariantEntry{
				EntryType: entry.TypeLoan,
				Before:    mustEncode(t, before),
				After:     mustEncode(t, after),
			}
			violation := loanPayViolation(t, loanPayInvariantRules(true, true, true), change)
			if violation == nil || !strings.Contains(violation.Message, tc.want) {
				t.Fatalf("full repayment violation = %v, want message containing %q", violation, tc.want)
			}
		})
	}
}

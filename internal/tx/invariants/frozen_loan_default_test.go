package invariants

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
)

func loanDefaultFrozenLine(t *testing.T, currency, low, high string) *state.RippleState {
	t.Helper()
	balance, err := state.NewIssuedAmountFromDecimalString("5", currency, state.AccountOneAddress)
	if err != nil {
		t.Fatalf("create %s balance: %v", currency, err)
	}
	lowLimit, err := state.NewIssuedAmountFromDecimalString("100", currency, low)
	if err != nil {
		t.Fatalf("create low limit: %v", err)
	}
	highLimit, err := state.NewIssuedAmountFromDecimalString("100", currency, high)
	if err != nil {
		t.Fatalf("create high limit: %v", err)
	}
	return &state.RippleState{
		Balance:   balance,
		LowLimit:  lowLimit,
		HighLimit: highLimit,
		Flags:     state.LsfLowDeepFreeze,
	}
}

func TestLoanDefaultFreezeExemptionScope(t *testing.T) {
	exemption := &loanDefaultFreezeExemption{
		currency: "USD",
		issuer:   addrIssuer,
		broker:   addrHolderA,
		vault:    addrHolderB,
	}
	tests := []struct {
		name     string
		currency string
		low      string
		high     string
		allowed  bool
	}{
		{name: "broker line", currency: "USD", low: addrIssuer, high: addrHolderA, allowed: true},
		{name: "vault line", currency: "USD", low: addrIssuer, high: addrHolderB, allowed: true},
		{name: "unrelated currency", currency: "EUR", low: addrIssuer, high: addrHolderA},
		{name: "unrelated line", currency: "USD", low: addrHolderA, high: addrHolderB},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			line := loanDefaultFrozenLine(t, test.currency, test.low, test.high)
			violation := validateFrozenState(
				frozenBalanceChange{lineData: line, balanceChangeSign: -1},
				true,
				stubTx{txType: txcore.TypeLoanManage},
				true,
				false,
				exemption,
				addrIssuer,
			)
			if test.allowed && violation != nil {
				t.Fatalf("allowed default line rejected: %v", violation)
			}
			if !test.allowed && violation == nil {
				t.Fatal("unrelated frozen line bypassed the default exemption")
			}
		})
	}
}

func TestLoanDefaultFreezeExemptionRequiresView(t *testing.T) {
	if got := findLoanDefaultFreezeExemption(stubTx{txType: txcore.TypeLoanManage}, nil, amendment.AllSupportedRules()); got != nil {
		t.Fatal("nil view unexpectedly produced a default freeze exemption")
	}
}

package lending_test

// Pre-activation surface tests for the XLS-66 LendingProtocol transaction
// types. Mirrors the stub convention used for Vault/XChain: while
// LendingProtocol is off (SupportedNo), every Loan* transaction is rejected at
// preflight with temDISABLED; once force-enabled, the still-stubbed Apply
// returns tefINTERNAL. Reference: rippled PR #5270; full semantics in #1245.

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
)

// a non-zero 256-bit hash used for VaultID / LoanBrokerID / LoanID fields.
const fxHash = "1111111111111111111111111111111111111111111111111111111111111111"

// loanTxBuilders returns a fresh set of well-formed Loan* transactions keyed by
// name, so both the disabled and enabled cases exercise identical inputs.
func loanTxBuilders(account string) map[string]tx.Transaction {
	amt := tx.NewXRPAmount(1_000_000)
	return map[string]tx.Transaction{
		"LoanBrokerSet":           lending.NewLoanBrokerSet(account, fxHash),
		"LoanBrokerDelete":        lending.NewLoanBrokerDelete(account, fxHash),
		"LoanBrokerCoverDeposit":  lending.NewLoanBrokerCoverDeposit(account, fxHash, amt),
		"LoanBrokerCoverWithdraw": lending.NewLoanBrokerCoverWithdraw(account, fxHash, amt),
		"LoanBrokerCoverClawback": lending.NewLoanBrokerCoverClawback(account),
		"LoanSet":                 lending.NewLoanSet(account, fxHash, "1000"),
		"LoanDelete":              lending.NewLoanDelete(account, fxHash),
		"LoanManage":              lending.NewLoanManage(account, fxHash),
		"LoanPay":                 lending.NewLoanPay(account, fxHash, amt),
	}
}

// TestLoanTransactionsDisabled asserts each Loan* type is rejected with
// temDISABLED while LendingProtocol is off — the surface a 3.0.0 node exposes.
func TestLoanTransactionsDisabled(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	for name, txn := range loanTxBuilders(alice.Address) {
		t.Run(name, func(t *testing.T) {
			result := env.Submit(txn)
			if result.Code != jtx.TemDISABLED {
				t.Errorf("%s: expected temDISABLED while LendingProtocol off, got %s", name, result.Code)
			}
		})
	}
}

// TestLoanTransactionsEnabledStillStubbed asserts that once LendingProtocol is
// force-enabled the transactions parse and pass preflight but fail at the
// stubbed Apply with tefINTERNAL, guarding against the amendment being switched
// on before the real semantics (issue #1245) land.
func TestLoanTransactionsEnabledStillStubbed(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.EnableFeature("LendingProtocol")
	env.Close()

	if !env.FeatureEnabled("LendingProtocol") {
		t.Fatal("LendingProtocol should be enabled after EnableFeature + Close")
	}

	for name, txn := range loanTxBuilders(alice.Address) {
		t.Run(name, func(t *testing.T) {
			result := env.Submit(txn)
			if result.Code != "tefINTERNAL" {
				t.Errorf("%s: expected tefINTERNAL from stubbed Apply, got %s", name, result.Code)
			}
		})
	}
}

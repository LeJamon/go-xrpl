package testing

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
)

// Fund funds the specified accounts from the master account with 1000 XRP each.
// Use FundAmount to fund a specific amount.
func (e *TestEnv) Fund(accounts ...*Account) {
	e.t.Helper()

	for _, acc := range accounts {
		e.FundAmount(acc, uint64(XRP(1000)))
	}
}

// masterPayment sends amount drops of XRP from the master account to acc,
// failing the test if it is not applied. When sign is true the payment is signed
// with the master key (matching rippled's funded-account setup); callers that
// only need a balance top-up pass sign=false. what labels failures.
func (e *TestEnv) masterPayment(acc *Account, amount uint64, sign bool, what string) {
	e.t.Helper()
	master := e.accounts["master"]
	if master == nil {
		e.t.Fatal("Master account not found")
	}
	seq := e.Seq(master)
	p := payment.NewPayment(master.Address, acc.Address, tx.NewXRPAmount(int64(amount)))
	p.Fee = formatUint64(e.baseFee)
	p.Sequence = &seq
	if e.networkID > 1024 {
		p.NetworkID = &e.networkID
	}
	if sign && master.PublicKey != nil {
		e.SignWith(p, master)
	}
	if result := e.Submit(p); !result.Success {
		e.t.Fatalf("%s for %s failed: %s", what, acc.Name, result.Code)
	}
}

// FundAmount funds an account with a specific amount.
// Like rippled's test environment, this also enables DefaultRipple on the account.
// This is important for trust line behavior - without DefaultRipple, trust lines
// cannot be deleted when limit is set to 0 (the NoRipple state would be "non-default").
func (e *TestEnv) FundAmount(acc *Account, amount uint64) {
	e.t.Helper()

	// Register account
	e.accounts[acc.Name] = acc

	// Fund with extra to cover the AccountSet fee (for enabling DefaultRipple)
	// so the account ends up with the requested amount.
	e.masterPayment(acc, amount+e.baseFee, true, "fund account")

	// Enable DefaultRipple on the account (matching rippled's test environment)
	// This allows trust lines to be properly deleted when limits are set to 0.
	e.enableDefaultRipple(acc)
}

// Pay sends XRP from master to an already-funded account.
// This is useful for tests that need to top-up an account with additional XRP
// (e.g., to meet reserve requirements). Unlike FundAmount, this does not
// register the account or enable DefaultRipple.
func (e *TestEnv) Pay(acc *Account, drops uint64) {
	e.t.Helper()
	// Unlike FundAmount this is a bare top-up: no DefaultRipple, no signature.
	e.masterPayment(acc, drops, false, "pay")
}

// enableDefaultRipple enables the DefaultRipple flag on an account.
// This matches rippled's test environment behavior.
func (e *TestEnv) enableDefaultRipple(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	accountSet.EnableDefaultRipple()
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq
	if e.networkID > 1024 {
		accountSet.NetworkID = &e.networkID
	}

	if acc.PublicKey != nil {
		e.SignWith(accountSet, acc)
	}

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to enable DefaultRipple for account %s: %s", acc.Name, result.Code)
	}
}

// FundNoRipple funds accounts WITHOUT enabling DefaultRipple.
// Reference: rippled's noripple(accounts...) in Env.h
func (e *TestEnv) FundNoRipple(accounts ...*Account) {
	e.t.Helper()
	for _, acc := range accounts {
		e.FundAmountNoRipple(acc, uint64(XRP(1000)))
	}
}

// FundAmountNoRipple funds an account with a specific amount but does NOT enable DefaultRipple.
func (e *TestEnv) FundAmountNoRipple(acc *Account, amount uint64) {
	e.t.Helper()
	e.accounts[acc.Name] = acc
	e.masterPayment(acc, amount, true, "fund account (no ripple)")
}

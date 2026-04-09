package testing

import (
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/account"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
)

// Fund funds the specified accounts from the master account.
// Each account receives the specified amount or a default of 1000 XRP.
func (e *TestEnv) Fund(accounts ...*Account) {
	e.t.Helper()

	for _, acc := range accounts {
		e.FundAmount(acc, uint64(XRP(1000)))
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

	// Create a payment from master to the new account
	master := e.accounts["master"]
	if master == nil {
		e.t.Fatal("Master account not found")
	}

	// Fund with extra to cover the AccountSet fee (for enabling DefaultRipple)
	// This ensures the account ends up with the requested amount.
	totalFunding := amount + e.baseFee

	// Create payment transaction
	seq := e.Seq(master)
	p := payment.NewPayment(master.Address, acc.Address, tx.NewXRPAmount(int64(totalFunding)))
	p.Fee = formatUint64(e.baseFee)
	p.Sequence = &seq
	if e.networkID > 1024 {
		p.NetworkID = &e.networkID
	}

	// When replay-on-close is enabled, sign the transaction so its serialized
	// bytes are stable for canonical sort salt computation.
	if master.PublicKey != nil {
		p.SetFlags(tx.TfFullyCanonicalSig)
		e.SignWith(p, master)
	}

	// Submit the payment
	result := e.Submit(p)
	if !result.Success {
		e.t.Fatalf("Failed to fund account %s: %s", acc.Name, result.Code)
	}

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

	master := e.accounts["master"]
	if master == nil {
		e.t.Fatal("Master account not found")
	}

	seq := e.Seq(master)
	p := payment.NewPayment(master.Address, acc.Address, tx.NewXRPAmount(int64(drops)))
	p.Fee = formatUint64(e.baseFee)
	p.Sequence = &seq
	if e.networkID > 1024 {
		p.NetworkID = &e.networkID
	}

	result := e.Submit(p)
	if !result.Success {
		e.t.Fatalf("Failed to pay %d drops to %s: %s", drops, acc.Name, result.Code)
	}
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
		accountSet.SetFlags(accountSet.GetFlags() | tx.TfFullyCanonicalSig)
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

	master := e.accounts["master"]
	if master == nil {
		e.t.Fatal("Master account not found")
	}

	seq := e.Seq(master)
	pay := payment.NewPayment(master.Address, acc.Address, tx.NewXRPAmount(int64(amount)))
	pay.Fee = formatUint64(e.baseFee)
	pay.Sequence = &seq
	if e.networkID > 1024 {
		pay.NetworkID = &e.networkID
	}

	if master.PublicKey != nil {
		pay.SetFlags(tx.TfFullyCanonicalSig)
		e.SignWith(pay, master)
	}

	result := e.Submit(pay)
	if !result.Success {
		e.t.Fatalf("Failed to fund account %s (no ripple): %s", acc.Name, result.Code)
	}
}

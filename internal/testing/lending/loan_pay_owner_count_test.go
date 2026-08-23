package lending_test

import (
	"encoding/hex"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	accounttest "github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestLoanPayDeletingSourceTrustLinePersistsOwnerCount(t *testing.T) {
	f := newLoanSetAssetFixture(t, "IOU")
	jtx.RequireTxSuccess(t, f.env.Submit(accounttest.AccountSet(f.borrower).
		ClearFlag(accounttx.AccountSetFlagDefaultRipple).
		Build()))
	jtx.RequireTxSuccess(t, submitLoanSet(t, f, f.borrower, f.owner, false))

	loanKey := keylet.Loan(f.brokerKey, 1)
	loanID := strings.ToUpper(hex.EncodeToString(loanKey.Key[:]))
	lineKey := f.holdingKey(f.borrower)
	if !f.env.LedgerEntryExists(lineKey) {
		t.Fatal("borrower trust line missing before LoanPay")
	}
	if got := f.env.OwnerCount(f.borrower); got != 2 {
		t.Fatalf("borrower OwnerCount before LoanPay = %d, want 2", got)
	}

	result := f.env.Submit(lending.NewLoanPay(
		f.borrower.Address,
		loanID,
		tx.NewIssuedAmountFromFloat64(1_000, "USD", f.issuer.Address),
	))
	jtx.RequireTxSuccess(t, result)

	if f.env.LedgerEntryExists(lineKey) {
		t.Fatal("borrower trust line still exists after redeeming its full balance")
	}
	if got := f.env.OwnerCount(f.borrower); got != 1 {
		t.Fatalf("borrower OwnerCount after LoanPay = %d, want 1", got)
	}
	assertModifiedTransition(
		t,
		result.Metadata,
		keylet.Account(f.borrower.AccountID()),
		"AccountRoot",
		"OwnerCount",
		1,
		2,
	)
}

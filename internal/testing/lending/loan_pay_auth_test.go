package lending_test

import (
	"encoding/hex"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/keylet"
)

// LoanPay preclaim runs rippled's requireAuth(view, Asset, borrower) for every
// vault asset kind, so a borrower whose MPT authorization the issuer revoked
// cannot repay.
func TestLoanPayRequiresBorrowerMPTAuthorization(t *testing.T) {
	f := newLoanSetAssetFixture(t, "MPT", mpttest.TfMPTRequireAuth)
	f.createHolding(f.borrower)
	f.createHolding(f.owner)
	jtx.RequireTxSuccess(t, submitLoanSet(t, f, f.borrower, f.owner, false))

	loanKey := keylet.Loan(f.brokerKey, 1)
	pay := func() jtx.TxResult {
		return f.env.Submit(lending.NewLoanPay(f.borrower.Address, hex.EncodeToString(loanKey.Key[:]), f.amount(1000)))
	}

	f.token.Authorize(mpttest.AuthorizeOpts{Holder: f.borrower, Flags: mpttest.TfMPTUnauthorize})
	jtx.RequireTxFail(t, pay(), jtx.TecNO_AUTH)

	f.token.Authorize(mpttest.AuthorizeOpts{Holder: f.borrower})
	jtx.RequireTxSuccess(t, pay())
}

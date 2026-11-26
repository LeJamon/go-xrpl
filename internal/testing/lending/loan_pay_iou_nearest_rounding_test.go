package lending_test

import (
	"encoding/hex"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/keylet"
)

// The overpayment fee is 74980000001.4996 loan-scale units: the large-scale add lifts it to a .5 tie that rounds to even.
func TestLoanPayIOUOverpaymentFeeNearestRounding(t *testing.T) {
	f := newLoanSetAssetFixture(t, "IOU")
	f.createHolding(f.owner)

	loanSet := lending.NewLoanSet(f.borrower.Address, f.brokerID, "1000")
	payments := uint32(2)
	overpaymentFee := uint32(74_980)
	flags := lending.TfLoanOverpayment
	loanSet.PaymentTotal = &payments
	loanSet.OverpaymentFee = &overpaymentFee
	loanSet.GetCommon().Flags = &flags
	loanSet.Counterparty = f.owner.Address
	loanSet.GetCommon().Fee = "20"
	loanSet.GetCommon().SigningPubKey = strings.ToUpper(f.borrower.PublicKeyHex())
	signature, err := txsign.SignCounterpartyWithRules(
		loanSet,
		strings.ToUpper(f.owner.PublicKeyHex()),
		"00"+strings.ToUpper(f.owner.PrivateKeyHex()),
		f.env.Rules(),
	)
	if err != nil {
		t.Fatalf("sign LoanSet counterparty: %v", err)
	}
	loanSet.GetCommon().CounterpartySignature = signature
	jtx.RequireTxSuccess(t, f.env.Submit(loanSet))
	f.fundHolding(f.borrower, 100)

	loanKey := keylet.Loan(f.brokerKey, 1)
	before := decodeLendingEntry(t, f.env, loanKey)
	assertLendingField(t, "Loan", before, "LoanScale", -11)
	assertLendingField(t, "Loan", before, "PeriodicPayment", "500")

	loanID := strings.ToUpper(hex.EncodeToString(loanKey.Key[:]))
	pay := lending.NewLoanPay(f.borrower.Address, loanID, tx.NewIssuedAmount(50_100_000_000_002, -11, "USD", f.issuer.Address))
	payFlags := lending.TfLoanOverpayment
	pay.GetCommon().Flags = &payFlags
	jtx.RequireTxSuccess(t, f.env.Submit(pay))

	after := decodeLendingEntry(t, f.env, loanKey)
	for _, field := range []string{"PrincipalOutstanding", "TotalValueOutstanding", "PeriodicPayment"} {
		assertLendingField(t, "Loan", after, field, "499.7498")
	}
	if got := f.env.IOUBalance(f.owner, f.issuer, "USD").Value(); got != "0.74980000002" {
		t.Fatalf("broker owner USD balance = %s, want 0.74980000002", got)
	}
}

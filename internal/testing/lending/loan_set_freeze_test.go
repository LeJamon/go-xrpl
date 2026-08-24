package lending_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/keylet"
)

func loanBrokerPseudoAccount(t *testing.T, f *loanSetAssetFixture) [20]byte {
	t.Helper()
	data, err := f.env.LedgerEntry(keylet.LoanBrokerByID(f.brokerKey))
	if err != nil {
		t.Fatalf("read LoanBroker: %v", err)
	}
	fields, err := binarycodec.DecodeBytes(data)
	if err != nil {
		t.Fatalf("decode LoanBroker: %v", err)
	}
	address, ok := fields["Account"].(string)
	if !ok {
		t.Fatalf("LoanBroker Account = %#v, want address", fields["Account"])
	}
	account, err := state.DecodeAccountID(address)
	if err != nil {
		t.Fatalf("decode LoanBroker Account: %v", err)
	}
	return account
}

func deepFreezeLoanSetTrustLine(t *testing.T, f *loanSetAssetFixture, account [20]byte) {
	t.Helper()
	lineKey := keylet.Line(account, f.issuer.AccountID(), "USD")
	data, err := f.env.LedgerEntry(lineKey)
	if err != nil {
		t.Fatalf("read trust line: %v", err)
	}
	line, err := state.ParseRippleState(data)
	if err != nil {
		t.Fatalf("parse trust line: %v", err)
	}
	if state.CompareAccountIDs(f.issuer.AccountID(), account) > 0 {
		line.Flags |= state.LsfHighFreeze | state.LsfHighDeepFreeze
	} else {
		line.Flags |= state.LsfLowFreeze | state.LsfLowDeepFreeze
	}
	data, err = state.SerializeRippleState(line)
	if err != nil {
		t.Fatalf("serialize trust line: %v", err)
	}
	if err := f.env.Ledger().Update(lineKey, data); err != nil {
		t.Fatalf("update trust line: %v", err)
	}
}

func TestLoanSetBrokerDeepFreeze(t *testing.T) {
	tests := []struct {
		name   string
		target func(*testing.T, *loanSetAssetFixture) [20]byte
	}{
		{
			name: "owner",
			target: func(_ *testing.T, f *loanSetAssetFixture) [20]byte {
				return f.owner.AccountID()
			},
		},
		{
			name:   "pseudo-account",
			target: loanBrokerPseudoAccount,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newLoanSetAssetFixture(t, "IOU")
			f.createHolding(f.owner)
			deepFreezeLoanSetTrustLine(t, f, test.target(t, f))

			jtx.RequireTxClaimed(t, submitLoanSet(t, f, f.borrower, f.owner, false), jtx.TecFROZEN)
			if f.env.LedgerEntryExists(keylet.Loan(f.brokerKey, 1)) || f.env.LedgerEntryExists(f.holdingKey(f.borrower)) {
				t.Fatal("deep-frozen LoanSet committed loan-related state")
			}
		})
	}

	t.Run("unfrozen", func(t *testing.T) {
		f := newLoanSetAssetFixture(t, "IOU")
		f.createHolding(f.owner)
		jtx.RequireTxSuccess(t, submitLoanSet(t, f, f.borrower, f.owner, false))
		assertLoanSetOwnedObjects(t, f, f.borrower)
	})
}

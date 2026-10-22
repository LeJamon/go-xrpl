package lending_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/trustset"
	"github.com/LeJamon/go-xrpl/keylet"
)

func newCoverWithdrawFreezeFixture(t *testing.T, kind string, disable330 bool) *loanSetAssetFixture {
	t.Helper()
	env := newLendingEnv(t)
	if disable330 {
		env.DisableFeature("fixCleanup3_3_0")
		env.Close()
	}
	var mptCreateFlags []uint32
	if kind == "MPT" {
		mptCreateFlags = []uint32{mpttest.TfMPTCanLock}
	}
	f := newLoanSetAssetFixtureWithEnv(t, env, kind, false, mptCreateFlags...)
	f.createHolding(f.owner)
	f.createHolding(f.borrower)
	f.fundHolding(f.owner, 1_000)
	f.fundHolding(f.borrower, 1_000)
	jtx.RequireTxSuccess(t, f.env.Submit(lending.NewLoanBrokerCoverDeposit(
		f.owner.Address,
		f.brokerID,
		f.amount(100),
	)))
	return f
}

func assertCoverWithdrawFreezeFeeOnly(t *testing.T, f *loanSetAssetFixture, destination, expectedCode string) {
	t.Helper()
	var borrowerUnchanged func() bool
	if f.asset.IsMPT() {
		if !f.token.CheckMPTokenAmount(f.borrower, 1_000) {
			t.Fatalf("MPT borrower balance before withdrawal = not 1000")
		}
		borrowerUnchanged = func() bool { return f.token.CheckMPTokenAmount(f.borrower, 1_000) }
	} else {
		beforeBorrower := f.env.IOUBalance(f.borrower, f.issuer, "USD").Value()
		borrowerUnchanged = func() bool {
			return f.env.IOUBalance(f.borrower, f.issuer, "USD").Value() == beforeBorrower
		}
	}
	beforeBroker := decodeLendingEntry(t, f.env, keylet.LoanBrokerByID(f.brokerKey))
	beforeCover, ok := beforeBroker["CoverAvailable"].(string)
	if !ok {
		t.Fatalf("LoanBroker CoverAvailable = %v, want an issued amount", beforeBroker["CoverAvailable"])
	}
	beforeOwnerBalance := f.env.Balance(f.owner)
	beforeOwnerSequence := f.env.Seq(f.owner)

	withdraw := lending.NewLoanBrokerCoverWithdraw(f.owner.Address, f.brokerID, f.amount(10))
	withdraw.Destination = destination
	result := f.env.Submit(withdraw)
	jtx.RequireTxClaimed(t, result, expectedCode)
	if !result.Applied || result.Fee == 0 {
		t.Fatalf("freeze result applied/fee = %v/%d, want fee-only recovery", result.Applied, result.Fee)
	}
	if result.Metadata == nil || result.Metadata.TransactionResult.String() != result.Code {
		t.Fatalf("freeze metadata result = %v, want %s", result.Metadata, result.Code)
	}
	if len(result.Metadata.AffectedNodes) != 1 ||
		result.Metadata.AffectedNodes[0].NodeType != "ModifiedNode" ||
		result.Metadata.AffectedNodes[0].LedgerEntryType != "AccountRoot" {
		t.Fatalf("freeze metadata = %+v, want one AccountRoot modification", result.Metadata.AffectedNodes)
	}
	if !borrowerUnchanged() {
		t.Fatalf("frozen destination balance changed")
	}
	afterBroker := decodeLendingEntry(t, f.env, keylet.LoanBrokerByID(f.brokerKey))
	if got := afterBroker["CoverAvailable"]; got != beforeCover {
		t.Fatalf("broker cover = %v, want unchanged %s", got, beforeCover)
	}
	if got := f.env.Balance(f.owner); got != beforeOwnerBalance-result.Fee {
		t.Fatalf("owner balance = %d, want fee-only %d", got, beforeOwnerBalance-result.Fee)
	}
	if got := f.env.Seq(f.owner); got != beforeOwnerSequence+1 {
		t.Fatalf("owner sequence = %d, want %d", got, beforeOwnerSequence+1)
	}
}

func TestLoanBrokerCoverWithdrawFreezeOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		disable330 bool
		freeze     func(*testing.T, *loanSetAssetFixture)
	}{
		{
			name: "fixCleanup3_3_0 destination deep freeze",
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				freeze := trustset.NewTrustSet(
					f.issuer.Address,
					tx.NewIssuedAmountFromFloat64(0, "USD", f.borrower.Address),
				)
				freeze.SetFlags(trustset.TrustSetFlagSetFreeze | trustset.TrustSetFlagSetDeepFreeze)
				jtx.RequireTxSuccess(t, f.env.Submit(freeze))
			},
		},
		{
			name:       "legacy destination deep freeze",
			disable330: true,
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				freeze := trustset.NewTrustSet(
					f.issuer.Address,
					tx.NewIssuedAmountFromFloat64(0, "USD", f.borrower.Address),
				)
				freeze.SetFlags(trustset.TrustSetFlagSetFreeze | trustset.TrustSetFlagSetDeepFreeze)
				jtx.RequireTxSuccess(t, f.env.Submit(freeze))
			},
		},
		{
			name:       "legacy global freeze",
			disable330: true,
			freeze: func(_ *testing.T, f *loanSetAssetFixture) {
				f.env.EnableGlobalFreeze(f.issuer)
			},
		},
		{
			name:       "legacy pseudo-account freeze",
			disable330: true,
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				pseudo := loanBrokerPseudoAccount(t, f)
				freeze := trustset.NewTrustSet(
					f.issuer.Address,
					tx.NewIssuedAmountFromFloat64(0, "USD", state.EncodeAccountIDSafe(pseudo)),
				)
				freeze.SetFlags(trustset.TrustSetFlagSetFreeze)
				jtx.RequireTxSuccess(t, f.env.Submit(freeze))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newCoverWithdrawFreezeFixture(t, "IOU", test.disable330)
			if test.freeze != nil {
				test.freeze(t, f)
			}
			f.env.Close()
			assertCoverWithdrawFreezeFeeOnly(t, f, f.borrower.Address, jtx.TecFROZEN)
		})
	}
}

func TestLoanBrokerCoverWithdrawLegacyMPTFreezeOutcomes(t *testing.T) {
	tests := []struct {
		name string
		lock func(*testing.T, *loanSetAssetFixture)
	}{
		{
			name: "global lock",
			lock: func(_ *testing.T, f *loanSetAssetFixture) {
				f.token.Set(mpttest.SetOpts{Account: f.issuer, Flags: mpttest.TfMPTLock})
			},
		},
		{
			name: "pseudo-account lock",
			lock: func(t *testing.T, f *loanSetAssetFixture) {
				pseudo := loanBrokerPseudoAccount(t, f)
				pseudoAccount := jtx.NewAccountWithAddress("loan-broker-pseudo", state.EncodeAccountIDSafe(pseudo))
				f.token.Set(mpttest.SetOpts{Account: f.issuer, Holder: pseudoAccount, Flags: mpttest.TfMPTLock})
			},
		},
		{
			name: "destination lock",
			lock: func(_ *testing.T, f *loanSetAssetFixture) {
				f.token.Set(mpttest.SetOpts{Account: f.issuer, Holder: f.borrower, Flags: mpttest.TfMPTLock})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newCoverWithdrawFreezeFixture(t, "MPT", true)
			test.lock(t, f)
			f.env.Close()
			assertCoverWithdrawFreezeFeeOnly(t, f, f.borrower.Address, jtx.TecLOCKED)
		})
	}
}

func TestLoanBrokerCoverWithdrawLegacyMPTIssuerFreezeExemption(t *testing.T) {
	f := newCoverWithdrawFreezeFixture(t, "MPT", true)
	f.token.Set(mpttest.SetOpts{Account: f.issuer, Holder: f.owner, Flags: mpttest.TfMPTLock})
	f.env.Close()

	withdraw := lending.NewLoanBrokerCoverWithdraw(f.owner.Address, f.brokerID, f.amount(10))
	withdraw.Destination = f.issuer.Address
	jtx.RequireTxSuccess(t, f.env.Submit(withdraw))
}

func TestLoanBrokerCoverWithdrawIssuerFreezeExemption(t *testing.T) {
	for _, disable330 := range []bool{false, true} {
		name := "fixCleanup3_3_0"
		if disable330 {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			f := newCoverWithdrawFreezeFixture(t, "IOU", disable330)
			freeze := trustset.NewTrustSet(
				f.issuer.Address,
				tx.NewIssuedAmountFromFloat64(0, "USD", f.owner.Address),
			)
			freeze.SetFlags(trustset.TrustSetFlagSetFreeze)
			jtx.RequireTxSuccess(t, f.env.Submit(freeze))
			f.env.Close()

			withdraw := lending.NewLoanBrokerCoverWithdraw(f.owner.Address, f.brokerID, f.amount(10))
			withdraw.Destination = f.issuer.Address
			jtx.RequireTxSuccess(t, f.env.Submit(withdraw))
		})
	}
}

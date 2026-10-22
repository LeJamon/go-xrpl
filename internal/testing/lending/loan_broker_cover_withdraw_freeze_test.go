package lending_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
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
	} else {
		env.EnableFeature("fixCleanup3_3_0")
	}
	env.Close()

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

func assertCoverWithdrawFeatureState(t *testing.T, f *loanSetAssetFixture, wantFix330 bool) {
	t.Helper()
	rules := f.env.Rules()
	states := []struct {
		name string
		got  bool
		want bool
	}{
		{
			name: "fixCleanup3_3_0",
			got:  rules.Enabled(amendment.FeatureFixCleanup3_3_0),
			want: wantFix330,
		},
		{
			name: "fixCleanup3_4_0",
			got:  rules.Enabled(amendment.FeatureFixCleanup3_4_0),
			want: false,
		},
		{
			name: "LendingProtocolV1_1",
			got:  rules.Enabled(amendment.FeatureLendingProtocolV1_1),
			want: false,
		},
	}
	for _, state := range states {
		if state.got != state.want {
			t.Fatalf("%s state = %t, want %t after close", state.name, state.got, state.want)
		}
	}
}

func setIOUFreeze(t *testing.T, f *loanSetAssetFixture, account [20]byte, flags uint32) {
	t.Helper()
	freeze := trustset.NewTrustSet(
		f.issuer.Address,
		tx.NewIssuedAmountFromFloat64(0, "USD", state.EncodeAccountIDSafe(account)),
	)
	freeze.SetFlags(flags)
	jtx.RequireTxSuccess(t, f.env.Submit(freeze))
}

func assertCoverWithdrawFreezeFeeOnly(t *testing.T, f *loanSetAssetFixture, destination, expectedCode string) {
	t.Helper()
	var borrowerUnchanged func() bool
	var ownerUnchanged func() bool
	if f.asset.IsMPT() {
		if !f.token.CheckMPTokenAmount(f.borrower, 1_000) {
			t.Fatalf("MPT borrower balance before withdrawal = not 1000")
		}
		if !f.token.CheckMPTokenAmount(f.owner, 900) {
			t.Fatalf("MPT owner balance before withdrawal = not 900")
		}
		borrowerUnchanged = func() bool { return f.token.CheckMPTokenAmount(f.borrower, 1_000) }
		ownerUnchanged = func() bool { return f.token.CheckMPTokenAmount(f.owner, 900) }
	} else {
		beforeBorrower := f.env.IOUBalance(f.borrower, f.issuer, "USD").Value()
		beforeOwner := f.env.IOUBalance(f.owner, f.issuer, "USD").Value()
		borrowerUnchanged = func() bool {
			return f.env.IOUBalance(f.borrower, f.issuer, "USD").Value() == beforeBorrower
		}
		ownerUnchanged = func() bool {
			return f.env.IOUBalance(f.owner, f.issuer, "USD").Value() == beforeOwner
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
	if !ownerUnchanged() {
		t.Fatalf("frozen source balance changed")
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

func assertCoverWithdrawOutcome(t *testing.T, f *loanSetAssetFixture, destination, expectedCode string) {
	t.Helper()
	if expectedCode == jtx.TesSUCCESS {
		withdraw := lending.NewLoanBrokerCoverWithdraw(f.owner.Address, f.brokerID, f.amount(10))
		withdraw.Destination = destination
		jtx.RequireTxSuccess(t, f.env.Submit(withdraw))
		return
	}
	assertCoverWithdrawFreezeFeeOnly(t, f, destination, expectedCode)
}

type coverWithdrawFreezeCase struct {
	name        string
	destination func(*loanSetAssetFixture) string
	freeze      func(*testing.T, *loanSetAssetFixture)
	fixCode     string
	legacyCode  string
}

func runCoverWithdrawFreezeMatrix(t *testing.T, kind string, cases []coverWithdrawFreezeCase) {
	t.Helper()
	for _, test := range cases {
		for _, disable330 := range []bool{false, true} {
			mode := "fixCleanup3_3_0"
			if disable330 {
				mode = "legacy"
			}
			t.Run(test.name+"/"+mode, func(t *testing.T) {
				f := newCoverWithdrawFreezeFixture(t, kind, disable330)
				test.freeze(t, f)
				f.env.Close()
				assertCoverWithdrawFeatureState(t, f, !disable330)

				expectedCode := test.fixCode
				if disable330 {
					expectedCode = test.legacyCode
				}
				assertCoverWithdrawOutcome(t, f, test.destination(f), expectedCode)
			})
		}
	}
}

func coverWithdrawBorrower(f *loanSetAssetFixture) string {
	return f.borrower.Address
}

func coverWithdrawSelf(*loanSetAssetFixture) string {
	return ""
}

func coverWithdrawIssuer(f *loanSetAssetFixture) string {
	return f.issuer.Address
}

func TestLoanBrokerCoverWithdrawIOUFreezeOutcomes(t *testing.T) {
	runCoverWithdrawFreezeMatrix(t, "IOU", []coverWithdrawFreezeCase{
		{
			name:        "global freeze to destination",
			destination: coverWithdrawBorrower,
			freeze: func(_ *testing.T, f *loanSetAssetFixture) {
				f.env.EnableGlobalFreeze(f.issuer)
			},
			fixCode:    jtx.TecFROZEN,
			legacyCode: jtx.TecFROZEN,
		},
		{
			name:        "pseudo-account regular freeze to destination",
			destination: coverWithdrawBorrower,
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				setIOUFreeze(t, f, loanBrokerPseudoAccount(t, f), trustset.TrustSetFlagSetFreeze)
			},
			fixCode:    jtx.TecFROZEN,
			legacyCode: jtx.TecFROZEN,
		},
		{
			name:        "pseudo-account deep freeze to destination",
			destination: coverWithdrawBorrower,
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				setIOUFreeze(t, f, loanBrokerPseudoAccount(t, f),
					trustset.TrustSetFlagSetFreeze|trustset.TrustSetFlagSetDeepFreeze)
			},
			fixCode:    jtx.TecFROZEN,
			legacyCode: jtx.TecFROZEN,
		},
		{
			name:        "submitter regular freeze to destination",
			destination: coverWithdrawBorrower,
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				setIOUFreeze(t, f, f.owner.AccountID(), trustset.TrustSetFlagSetFreeze)
			},
			fixCode:    jtx.TecFROZEN,
			legacyCode: jtx.TesSUCCESS,
		},
		{
			name:        "submitter regular freeze to self",
			destination: coverWithdrawSelf,
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				setIOUFreeze(t, f, f.owner.AccountID(), trustset.TrustSetFlagSetFreeze)
			},
			fixCode:    jtx.TesSUCCESS,
			legacyCode: jtx.TesSUCCESS,
		},
		{
			name:        "submitter deep freeze to destination",
			destination: coverWithdrawBorrower,
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				setIOUFreeze(t, f, f.owner.AccountID(),
					trustset.TrustSetFlagSetFreeze|trustset.TrustSetFlagSetDeepFreeze)
			},
			fixCode:    jtx.TecFROZEN,
			legacyCode: jtx.TesSUCCESS,
		},
		{
			name:        "submitter deep freeze to self",
			destination: coverWithdrawSelf,
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				setIOUFreeze(t, f, f.owner.AccountID(),
					trustset.TrustSetFlagSetFreeze|trustset.TrustSetFlagSetDeepFreeze)
			},
			fixCode:    jtx.TecFROZEN,
			legacyCode: jtx.TecFROZEN,
		},
		{
			name:        "destination regular freeze",
			destination: coverWithdrawBorrower,
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				setIOUFreeze(t, f, f.borrower.AccountID(), trustset.TrustSetFlagSetFreeze)
			},
			fixCode:    jtx.TesSUCCESS,
			legacyCode: jtx.TesSUCCESS,
		},
		{
			name:        "destination deep freeze",
			destination: coverWithdrawBorrower,
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				setIOUFreeze(t, f, f.borrower.AccountID(),
					trustset.TrustSetFlagSetFreeze|trustset.TrustSetFlagSetDeepFreeze)
			},
			fixCode:    jtx.TecFROZEN,
			legacyCode: jtx.TecFROZEN,
		},
		{
			name:        "global freeze to issuer",
			destination: coverWithdrawIssuer,
			freeze: func(_ *testing.T, f *loanSetAssetFixture) {
				f.env.EnableGlobalFreeze(f.issuer)
			},
			fixCode:    jtx.TesSUCCESS,
			legacyCode: jtx.TecINSUFFICIENT_FUNDS,
		},
		{
			name:        "pseudo-account regular freeze to issuer",
			destination: coverWithdrawIssuer,
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				setIOUFreeze(t, f, loanBrokerPseudoAccount(t, f), trustset.TrustSetFlagSetFreeze)
			},
			fixCode:    jtx.TesSUCCESS,
			legacyCode: jtx.TecINSUFFICIENT_FUNDS,
		},
		{
			name:        "pseudo-account deep freeze to issuer",
			destination: coverWithdrawIssuer,
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				setIOUFreeze(t, f, loanBrokerPseudoAccount(t, f),
					trustset.TrustSetFlagSetFreeze|trustset.TrustSetFlagSetDeepFreeze)
			},
			fixCode:    jtx.TesSUCCESS,
			legacyCode: jtx.TecINSUFFICIENT_FUNDS,
		},
		{
			name:        "submitter regular freeze to issuer",
			destination: coverWithdrawIssuer,
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				setIOUFreeze(t, f, f.owner.AccountID(), trustset.TrustSetFlagSetFreeze)
			},
			fixCode:    jtx.TesSUCCESS,
			legacyCode: jtx.TesSUCCESS,
		},
		{
			name:        "submitter deep freeze to issuer",
			destination: coverWithdrawIssuer,
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				setIOUFreeze(t, f, f.owner.AccountID(),
					trustset.TrustSetFlagSetFreeze|trustset.TrustSetFlagSetDeepFreeze)
			},
			fixCode:    jtx.TesSUCCESS,
			legacyCode: jtx.TesSUCCESS,
		},
	})
}

func TestLoanBrokerCoverWithdrawMPTFreezeOutcomes(t *testing.T) {
	runCoverWithdrawFreezeMatrix(t, "MPT", []coverWithdrawFreezeCase{
		{
			name:        "global lock to destination",
			destination: coverWithdrawBorrower,
			freeze: func(_ *testing.T, f *loanSetAssetFixture) {
				f.token.Set(mpttest.SetOpts{Account: f.issuer, Flags: mpttest.TfMPTLock})
			},
			fixCode:    jtx.TecLOCKED,
			legacyCode: jtx.TecLOCKED,
		},
		{
			name:        "pseudo-account lock to destination",
			destination: coverWithdrawBorrower,
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				pseudo := loanBrokerPseudoAccount(t, f)
				pseudoAccount := jtx.NewAccountWithAddress("loan-broker-pseudo", state.EncodeAccountIDSafe(pseudo))
				f.token.Set(mpttest.SetOpts{
					Account: f.issuer,
					Holder:  pseudoAccount,
					Flags:   mpttest.TfMPTLock,
				})
			},
			fixCode:    jtx.TecLOCKED,
			legacyCode: jtx.TecLOCKED,
		},
		{
			name:        "submitter lock to destination",
			destination: coverWithdrawBorrower,
			freeze: func(_ *testing.T, f *loanSetAssetFixture) {
				f.token.Set(mpttest.SetOpts{Account: f.issuer, Holder: f.owner, Flags: mpttest.TfMPTLock})
			},
			fixCode:    jtx.TecLOCKED,
			legacyCode: jtx.TesSUCCESS,
		},
		{
			name:        "submitter lock to self",
			destination: coverWithdrawSelf,
			freeze: func(_ *testing.T, f *loanSetAssetFixture) {
				f.token.Set(mpttest.SetOpts{Account: f.issuer, Holder: f.owner, Flags: mpttest.TfMPTLock})
			},
			fixCode:    jtx.TecLOCKED,
			legacyCode: jtx.TecLOCKED,
		},
		{
			name:        "destination lock",
			destination: coverWithdrawBorrower,
			freeze: func(_ *testing.T, f *loanSetAssetFixture) {
				f.token.Set(mpttest.SetOpts{Account: f.issuer, Holder: f.borrower, Flags: mpttest.TfMPTLock})
			},
			fixCode:    jtx.TecLOCKED,
			legacyCode: jtx.TecLOCKED,
		},
		{
			name:        "global lock to issuer",
			destination: coverWithdrawIssuer,
			freeze: func(_ *testing.T, f *loanSetAssetFixture) {
				f.token.Set(mpttest.SetOpts{Account: f.issuer, Flags: mpttest.TfMPTLock})
			},
			fixCode:    jtx.TesSUCCESS,
			legacyCode: jtx.TecINSUFFICIENT_FUNDS,
		},
		{
			name:        "pseudo-account lock to issuer",
			destination: coverWithdrawIssuer,
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				pseudo := loanBrokerPseudoAccount(t, f)
				pseudoAccount := jtx.NewAccountWithAddress("loan-broker-pseudo", state.EncodeAccountIDSafe(pseudo))
				f.token.Set(mpttest.SetOpts{
					Account: f.issuer,
					Holder:  pseudoAccount,
					Flags:   mpttest.TfMPTLock,
				})
			},
			fixCode:    jtx.TesSUCCESS,
			legacyCode: jtx.TecINSUFFICIENT_FUNDS,
		},
		{
			name:        "submitter lock to issuer",
			destination: coverWithdrawIssuer,
			freeze: func(_ *testing.T, f *loanSetAssetFixture) {
				f.token.Set(mpttest.SetOpts{Account: f.issuer, Holder: f.owner, Flags: mpttest.TfMPTLock})
			},
			fixCode:    jtx.TesSUCCESS,
			legacyCode: jtx.TesSUCCESS,
		},
	})
}

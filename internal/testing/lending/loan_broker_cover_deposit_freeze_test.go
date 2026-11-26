package lending_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/trustset"
)

type coverDepositFreezeCase struct {
	name       string
	freeze     func(*testing.T, *loanSetAssetFixture)
	fixCode    string
	legacyCode string
}

func runCoverDepositFreezeMatrix(t *testing.T, kind string, cases []coverDepositFreezeCase) {
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
				result := f.env.Submit(lending.NewLoanBrokerCoverDeposit(f.owner.Address, f.brokerID, f.amount(1)))
				if expectedCode == jtx.TesSUCCESS {
					jtx.RequireTxSuccess(t, result)
				} else {
					jtx.RequireTxClaimed(t, result, expectedCode)
				}
			})
		}
	}
}

func TestLoanBrokerCoverDepositIOUFreezeOutcomes(t *testing.T) {
	runCoverDepositFreezeMatrix(t, "IOU", []coverDepositFreezeCase{
		{
			name: "global freeze",
			freeze: func(_ *testing.T, f *loanSetAssetFixture) {
				f.env.EnableGlobalFreeze(f.issuer)
			},
			fixCode:    jtx.TecFROZEN,
			legacyCode: jtx.TecFROZEN,
		},
		{
			name: "source regular freeze",
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				setIOUFreeze(t, f, f.owner.AccountID(), trustset.TrustSetFlagSetFreeze)
			},
			fixCode:    jtx.TecFROZEN,
			legacyCode: jtx.TecFROZEN,
		},
		{
			name: "source deep freeze",
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				setIOUFreeze(t, f, f.owner.AccountID(),
					trustset.TrustSetFlagSetFreeze|trustset.TrustSetFlagSetDeepFreeze)
			},
			fixCode:    jtx.TecFROZEN,
			legacyCode: jtx.TecFROZEN,
		},
		{
			name: "pseudo-account regular freeze",
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				setIOUFreeze(t, f, loanBrokerPseudoAccount(t, f), trustset.TrustSetFlagSetFreeze)
			},
			fixCode:    jtx.TecFROZEN,
			legacyCode: jtx.TesSUCCESS,
		},
		{
			name: "pseudo-account deep freeze",
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				setIOUFreeze(t, f, loanBrokerPseudoAccount(t, f),
					trustset.TrustSetFlagSetFreeze|trustset.TrustSetFlagSetDeepFreeze)
			},
			fixCode:    jtx.TecFROZEN,
			legacyCode: jtx.TecFROZEN,
		},
	})
}

func TestLoanBrokerCoverDepositMPTLockOutcomes(t *testing.T) {
	runCoverDepositFreezeMatrix(t, "MPT", []coverDepositFreezeCase{
		{
			name: "global lock",
			freeze: func(_ *testing.T, f *loanSetAssetFixture) {
				f.token.Set(mpttest.SetOpts{Account: f.issuer, Flags: mpttest.TfMPTLock})
			},
			fixCode:    jtx.TecLOCKED,
			legacyCode: jtx.TecLOCKED,
		},
		{
			name: "source lock",
			freeze: func(_ *testing.T, f *loanSetAssetFixture) {
				f.token.Set(mpttest.SetOpts{Account: f.issuer, Holder: f.owner, Flags: mpttest.TfMPTLock})
			},
			fixCode:    jtx.TecLOCKED,
			legacyCode: jtx.TecLOCKED,
		},
		{
			name: "pseudo-account lock",
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				pseudo := jtx.NewAccountWithAddress("loan-broker-pseudo",
					state.EncodeAccountIDSafe(loanBrokerPseudoAccount(t, f)))
				f.token.Set(mpttest.SetOpts{Account: f.issuer, Holder: pseudo, Flags: mpttest.TfMPTLock})
			},
			fixCode:    jtx.TecLOCKED,
			legacyCode: jtx.TecLOCKED,
		},
	})
}

package lending_test

import (
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/keylet"
)

const loanPayServiceFee = int64(100)

func setupLoanPayFeeRouting(t *testing.T, kind string, mptCreateFlags ...uint32) (*loanSetAssetFixture, string, *jtx.Account) {
	t.Helper()
	return setupLoanPayFeeRoutingWithFixture(t, newLoanSetAssetFixture(t, kind, mptCreateFlags...), mptCreateFlags...)
}

func setupCashLoanPayFeeRouting(t *testing.T, kind string, mptCreateFlags ...uint32) (*loanSetAssetFixture, string, *jtx.Account) {
	t.Helper()
	return setupLoanPayFeeRoutingWithFixture(t, newCashLoanSetAssetFixture(t, kind, mptCreateFlags...), mptCreateFlags...)
}

func setupLoanPayFeeRoutingWithFixture(t *testing.T, f *loanSetAssetFixture, _ ...uint32) (*loanSetAssetFixture, string, *jtx.Account) {
	t.Helper()
	f.createHolding(f.owner)

	loanSet := lending.NewLoanSet(f.borrower.Address, f.brokerID, "1000")
	loanSet.Counterparty = f.owner.Address
	serviceFee := "100"
	loanSet.LoanServiceFee = &serviceFee
	loanSet.GetCommon().Fee = "20"
	loanSet.GetCommon().SigningPubKey = strings.ToUpper(f.borrower.PublicKeyHex())
	signature, err := txsign.SignCounterpartyWithRules(
		loanSet,
		strings.ToUpper(f.owner.PublicKeyHex()),
		"00"+strings.ToUpper(f.owner.PrivateKeyHex()),
		f.env.Ledger().Rules(),
	)
	if err != nil {
		t.Fatalf("sign LoanSet counterparty: %v", err)
	}
	loanSet.GetCommon().CounterpartySignature = signature
	jtx.RequireTxSuccess(t, f.env.Submit(loanSet))
	f.fundHolding(f.borrower, loanPayServiceFee)

	loanKey := keylet.Loan(f.brokerKey, 1)
	loanID := strings.ToUpper(hex.EncodeToString(loanKey.Key[:]))
	pseudoID := loanBrokerPseudoAccount(t, f)
	pseudo := jtx.NewAccountWithAddress("broker-pseudo", state.EncodeAccountIDSafe(pseudoID))
	return f, loanID, pseudo
}

func loanPayFee(f *loanSetAssetFixture, loanID string) *lending.LoanPay {
	return lending.NewLoanPay(
		f.borrower.Address,
		loanID,
		f.amount(1000+loanPayServiceFee),
	)
}

func submitLoanPayFee(t *testing.T, f *loanSetAssetFixture, loanID string) {
	t.Helper()
	jtx.RequireTxSuccess(t, f.env.Submit(loanPayFee(f, loanID)))
}

func requireLoanPayFeeBalances(t *testing.T, f *loanSetAssetFixture, pseudo *jtx.Account, owner, cover int64) {
	t.Helper()
	if f.asset.IsMPT() {
		f.token.RequireMPTokenAmount(f.owner, owner)
		f.token.RequireMPTokenAmount(pseudo, cover)
	} else {
		jtx.RequireIOUBalance(t, f.env, f.owner, f.issuer, "USD", float64(owner))
		jtx.RequireIOUBalance(t, f.env, pseudo, f.issuer, "USD", float64(cover))
	}

	broker := decodeLendingEntry(t, f.env, keylet.LoanBrokerByID(f.brokerKey))
	if cover == 0 {
		if value, present := broker["CoverAvailable"]; present && value != "0" {
			t.Fatalf("LoanBroker CoverAvailable = %v, want zero", value)
		}
		return
	}
	assertLendingField(t, "LoanBroker", broker, "CoverAvailable", "100")
}

func TestLoanPayBrokerOwnerFreezeRouting(t *testing.T) {
	tests := []struct {
		name     string
		freeze   func(*testing.T, *loanSetAssetFixture)
		ownerFee int64
		coverFee int64
	}{
		{
			name: "ordinary freeze sends fee to owner",
			freeze: func(_ *testing.T, f *loanSetAssetFixture) {
				f.env.FreezeTrustLine(f.issuer, f.owner, "USD")
			},
			ownerFee: loanPayServiceFee,
		},
		{
			name: "deep freeze sends fee to cover",
			freeze: func(t *testing.T, f *loanSetAssetFixture) {
				deepFreezeLoanSetTrustLine(t, f, f.owner.AccountID())
			},
			coverFee: loanPayServiceFee,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f, loanID, pseudo := setupLoanPayFeeRouting(t, "IOU")
			test.freeze(t, f)
			submitLoanPayFee(t, f, loanID)
			requireLoanPayFeeBalances(t, f, pseudo, test.ownerFee, test.coverFee)
		})
	}
}

func TestLoanPayMissingBrokerOwnerHoldingRoutesFeeToCover(t *testing.T) {
	for _, kind := range []string{"IOU", "MPT"} {
		t.Run(kind, func(t *testing.T) {
			f, loanID, pseudo := setupLoanPayFeeRouting(t, kind)
			f.removeHolding(f.owner)
			if f.env.LedgerEntryExists(f.holdingKey(f.owner)) {
				t.Fatal("broker owner holding still exists before LoanPay")
			}

			submitLoanPayFee(t, f, loanID)
			if f.env.LedgerEntryExists(f.holdingKey(f.owner)) {
				t.Fatal("LoanPay recreated the broker owner holding")
			}
			requireLoanPayFeeBalances(t, f, pseudo, 0, loanPayServiceFee)
		})
	}
}

func TestLoanPayDeepFrozenBrokerOwnerAndPseudoFails(t *testing.T) {
	tests := []struct {
		name           string
		kind           string
		mptCreateFlags []uint32
		freeze         func(*testing.T, *loanSetAssetFixture, *jtx.Account)
		want           string
	}{
		{
			name: "IOU",
			kind: "IOU",
			freeze: func(t *testing.T, f *loanSetAssetFixture, pseudo *jtx.Account) {
				deepFreezeLoanSetTrustLine(t, f, f.owner.AccountID())
				deepFreezeLoanSetTrustLine(t, f, pseudo.AccountID())
			},
			want: jtx.TecFROZEN,
		},
		{
			name:           "MPT",
			kind:           "MPT",
			mptCreateFlags: []uint32{mpttest.TfMPTCanLock},
			freeze: func(_ *testing.T, f *loanSetAssetFixture, pseudo *jtx.Account) {
				f.token.Set(mpttest.SetOpts{Holder: f.owner, Flags: mpttest.TfMPTLock})
				f.token.Set(mpttest.SetOpts{Holder: pseudo, Flags: mpttest.TfMPTLock})
			},
			want: jtx.TecLOCKED,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f, loanID, pseudo := setupLoanPayFeeRouting(t, test.kind, test.mptCreateFlags...)
			test.freeze(t, f, pseudo)

			jtx.RequireTxClaimed(t, f.env.Submit(loanPayFee(f, loanID)), test.want)
			requireLoanPayFeeBalances(t, f, pseudo, 0, 0)
		})
	}
}

func TestCashLoanPayDeepFrozenRollback(t *testing.T) {
	for _, test := range []struct {
		kind           string
		mptCreateFlags []uint32
		freeze         func(*testing.T, *loanSetAssetFixture, *jtx.Account)
		want           string
	}{
		{
			kind: "IOU",
			freeze: func(t *testing.T, f *loanSetAssetFixture, pseudo *jtx.Account) {
				deepFreezeLoanSetTrustLine(t, f, f.owner.AccountID())
				deepFreezeLoanSetTrustLine(t, f, pseudo.AccountID())
			},
			want: jtx.TecFROZEN,
		},
		{
			kind:           "MPT",
			mptCreateFlags: []uint32{mpttest.TfMPTCanLock},
			freeze: func(_ *testing.T, f *loanSetAssetFixture, pseudo *jtx.Account) {
				f.token.Set(mpttest.SetOpts{Holder: f.owner, Flags: mpttest.TfMPTLock})
				f.token.Set(mpttest.SetOpts{Holder: pseudo, Flags: mpttest.TfMPTLock})
			},
			want: jtx.TecLOCKED,
		},
	} {
		t.Run(test.kind, func(t *testing.T) {
			f, loanID, pseudo := setupCashLoanPayFeeRouting(t, test.kind, test.mptCreateFlags...)
			test.freeze(t, f, pseudo)
			loanKey := keylet.Loan(f.brokerKey, 1)
			brokerKey := keylet.LoanBrokerByID(f.brokerKey)
			beforeLoan := decodeLendingEntry(t, f.env, loanKey)
			beforeBroker := decodeLendingEntry(t, f.env, brokerKey)
			beforeVault := decodeLendingEntry(t, f.env, f.vaultKey)

			jtx.RequireTxClaimed(t, f.env.Submit(loanPayFee(f, loanID)), test.want)
			if got := decodeLendingEntry(t, f.env, loanKey); !reflect.DeepEqual(got, beforeLoan) {
				t.Fatalf("Loan changed after failed cash LoanPay: before=%v after=%v", beforeLoan, got)
			}
			if got := decodeLendingEntry(t, f.env, brokerKey); !reflect.DeepEqual(got, beforeBroker) {
				t.Fatalf("LoanBroker changed after failed cash LoanPay: before=%v after=%v", beforeBroker, got)
			}
			if got := decodeLendingEntry(t, f.env, f.vaultKey); !reflect.DeepEqual(got, beforeVault) {
				t.Fatalf("Vault changed after failed cash LoanPay: before=%v after=%v", beforeVault, got)
			}
		})
	}
}

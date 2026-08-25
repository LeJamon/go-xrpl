package lending_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/keylet"
)

const loanPayServiceFee = int64(100)

func setupLoanPayFeeRouting(t *testing.T, kind string) (*loanSetAssetFixture, string, *jtx.Account) {
	t.Helper()
	f := newLoanSetAssetFixture(t, kind)
	f.createHolding(f.owner)

	loanSet := lending.NewLoanSet(f.borrower.Address, f.brokerID, "1000")
	loanSet.Counterparty = f.owner.Address
	serviceFee := "100"
	loanSet.LoanServiceFee = &serviceFee
	loanSet.GetCommon().Fee = "20"
	loanSet.GetCommon().SigningPubKey = strings.ToUpper(f.borrower.PublicKeyHex())
	signature, err := txsign.SignCounterparty(
		loanSet,
		strings.ToUpper(f.owner.PublicKeyHex()),
		"00"+strings.ToUpper(f.owner.PrivateKeyHex()),
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
	f, loanID, pseudo := setupLoanPayFeeRouting(t, "IOU")
	deepFreezeLoanSetTrustLine(t, f, f.owner.AccountID())
	deepFreezeLoanSetTrustLine(t, f, pseudo.AccountID())

	jtx.RequireTxClaimed(t, f.env.Submit(loanPayFee(f, loanID)), jtx.TecFROZEN)
	requireLoanPayFeeBalances(t, f, pseudo, 0, 0)
}

package lending_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	paytest "github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
)

type loanSetAssetFixture struct {
	env                     *jtx.TestEnv
	issuer, owner, borrower *jtx.Account
	asset                   tx.Asset
	brokerID                string
	brokerKey               [32]byte
	holdingKey              func(*jtx.Account) keylet.Keylet
	createHolding           func(*jtx.Account)
	removeHolding           func(*jtx.Account)
	fundHolding             func(*jtx.Account, int64)
	amount                  func(int64) tx.Amount
	token                   *mpttest.MPTTester
}

func newLoanSetAssetFixture(t *testing.T, kind string, mptCreateFlags ...uint32) *loanSetAssetFixture {
	t.Helper()
	env := newLendingEnv(t)
	issuer := jtx.NewAccount(kind + "-issuer")
	owner := jtx.NewAccount(kind + "-owner")
	depositor := jtx.NewAccount(kind + "-depositor")
	borrower := jtx.NewAccount(kind + "-borrower")
	for _, account := range []*jtx.Account{issuer, owner, depositor, borrower} {
		env.FundAmount(account, 10_000_000_000)
	}

	var asset tx.Asset
	var deposit tx.Amount
	var holdingKey func(*jtx.Account) keylet.Keylet
	var createHolding func(*jtx.Account)
	var removeHolding func(*jtx.Account)
	var fundHolding func(*jtx.Account, int64)
	var amount func(int64) tx.Amount
	var token *mpttest.MPTTester
	switch kind {
	case "IOU":
		asset = tx.Asset{Currency: "USD", Issuer: issuer.Address}
		limit := tx.NewIssuedAmountFromFloat64(100_000, "USD", issuer.Address)
		env.Trust(depositor, limit)
		env.PayIOU(issuer, depositor, issuer, "USD", 10_000)
		deposit = tx.NewIssuedAmountFromFloat64(10_000, "USD", issuer.Address)
		holdingKey = func(account *jtx.Account) keylet.Keylet {
			return keylet.Line(account.AccountID(), issuer.AccountID(), "USD")
		}
		createHolding = func(account *jtx.Account) { env.Trust(account, limit) }
		removeHolding = func(account *jtx.Account) {
			env.Trust(account, tx.NewIssuedAmountFromFloat64(0, "USD", issuer.Address))
		}
		fundHolding = func(account *jtx.Account, value int64) {
			env.Trust(account, limit)
			env.PayIOU(issuer, account, issuer, "USD", float64(value))
		}
		amount = func(value int64) tx.Amount {
			return tx.NewIssuedAmountFromFloat64(float64(value), "USD", issuer.Address)
		}
	case "MPT":
		token = mpttest.NewMPTTester(t, env, issuer, mpttest.MPTInit{Holders: []*jtx.Account{depositor}})
		flags := mpttest.TfMPTCanTransfer
		for _, extra := range mptCreateFlags {
			flags |= extra
		}
		token.Create(mpttest.CreateOpts{Flags: flags})
		authorize := func(account *jtx.Account) {
			token.Authorize(mpttest.AuthorizeOpts{Account: account})
			if flags&mpttest.TfMPTRequireAuth != 0 {
				token.Authorize(mpttest.AuthorizeOpts{Holder: account})
			}
		}
		authorize(depositor)
		token.Pay(issuer, depositor, 10_000)
		asset = tx.Asset{MPTIssuanceID: token.IssuanceID()}
		deposit = token.MPTAmount(10_000)
		idBytes, err := hex.DecodeString(token.IssuanceID())
		if err != nil {
			t.Fatalf("decode MPToken issuance ID: %v", err)
		}
		var issuanceID [24]byte
		copy(issuanceID[:], idBytes)
		holdingKey = func(account *jtx.Account) keylet.Keylet {
			return keylet.MPTokenByID(issuanceID, account.AccountID())
		}
		createHolding = authorize
		removeHolding = func(account *jtx.Account) {
			token.Authorize(mpttest.AuthorizeOpts{Account: account, Flags: mpttest.TfMPTUnauthorize})
		}
		fundHolding = func(account *jtx.Account, value int64) { token.Pay(issuer, account, value) }
		amount = token.MPTAmount
	default:
		t.Fatalf("unsupported asset kind %q", kind)
	}

	vaultSeq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, asset)
	create.Common.Fee = reserveIncrement
	jtx.RequireTxSuccess(t, env.Submit(create))
	vaultID := vaultID(owner, vaultSeq)
	jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(depositor.Address, vaultID, deposit)))

	brokerSeq := env.Seq(owner)
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vaultID)))
	brokerID := brokerID(owner, brokerSeq)
	decodedBrokerID, err := hex.DecodeString(brokerID)
	if err != nil {
		t.Fatalf("decode LoanBroker ID: %v", err)
	}
	var brokerKey [32]byte
	copy(brokerKey[:], decodedBrokerID)

	return &loanSetAssetFixture{
		env:           env,
		issuer:        issuer,
		owner:         owner,
		borrower:      borrower,
		asset:         asset,
		brokerID:      brokerID,
		brokerKey:     brokerKey,
		holdingKey:    holdingKey,
		createHolding: createHolding,
		removeHolding: removeHolding,
		fundHolding:   fundHolding,
		amount:        amount,
		token:         token,
	}
}

func submitLoanSet(t *testing.T, f *loanSetAssetFixture, submitter, counterparty *jtx.Account, originationFee bool) jtx.TxResult {
	t.Helper()
	loanSet := lending.NewLoanSet(submitter.Address, f.brokerID, "1000")
	loanSet.Counterparty = counterparty.Address
	loanSet.GetCommon().Fee = "20"
	loanSet.GetCommon().SigningPubKey = strings.ToUpper(submitter.PublicKeyHex())
	if originationFee {
		fee := "1"
		loanSet.LoanOriginationFee = &fee
	}
	signature, err := txsign.SignCounterparty(
		loanSet,
		strings.ToUpper(counterparty.PublicKeyHex()),
		"00"+strings.ToUpper(counterparty.PrivateKeyHex()),
	)
	if err != nil {
		t.Fatalf("sign LoanSet counterparty: %v", err)
	}
	loanSet.GetCommon().CounterpartySignature = signature
	return f.env.Submit(loanSet)
}

func ownerDirectoryContains(t *testing.T, env *jtx.TestEnv, owner [20]byte, entry keylet.Keylet) bool {
	t.Helper()
	found := false
	if err := state.DirForEach(env.Ledger(), keylet.OwnerDir(owner), func(item [32]byte) error {
		if item == entry.Key {
			found = true
		}
		return nil
	}); err != nil {
		t.Fatalf("read owner directory: %v", err)
	}
	return found
}

func assertLoanSetOwnedObjects(t *testing.T, f *loanSetAssetFixture, borrower *jtx.Account) {
	t.Helper()
	holding := f.holdingKey(borrower)
	loan := keylet.Loan(f.brokerKey, 1)
	for name, entry := range map[string]keylet.Keylet{"holding": holding, "Loan": loan} {
		if !f.env.LedgerEntryExists(entry) {
			t.Errorf("%s ledger entry missing", name)
		}
		if !ownerDirectoryContains(t, f.env, borrower.AccountID(), entry) {
			t.Errorf("%s missing from borrower owner directory", name)
		}
	}
}

func TestLoanSetHoldingOwnerCount(t *testing.T) {
	t.Run("IOU borrower submits with new holding", func(t *testing.T) {
		f := newLoanSetAssetFixture(t, "IOU")
		f.createHolding(f.owner)
		if got := f.env.OwnerCount(f.borrower); got != 0 {
			t.Fatalf("initial borrower OwnerCount = %d, want 0", got)
		}
		jtx.RequireTxSuccess(t, submitLoanSet(t, f, f.borrower, f.owner, false))
		if got := f.env.OwnerCount(f.borrower); got != 2 {
			t.Fatalf("borrower OwnerCount = %d, want 2", got)
		}
		assertLoanSetOwnedObjects(t, f, f.borrower)
	})

	t.Run("MPT broker owner submits for counterparty borrower", func(t *testing.T) {
		f := newLoanSetAssetFixture(t, "MPT")
		f.createHolding(f.owner)
		ownerCount := f.env.OwnerCount(f.owner)
		jtx.RequireTxSuccess(t, submitLoanSet(t, f, f.owner, f.borrower, false))
		if got := f.env.OwnerCount(f.borrower); got != 2 {
			t.Fatalf("borrower OwnerCount = %d, want 2", got)
		}
		if got := f.env.OwnerCount(f.owner); got != ownerCount {
			t.Fatalf("broker owner OwnerCount = %d, want unchanged %d", got, ownerCount)
		}
		assertLoanSetOwnedObjects(t, f, f.borrower)
	})

	for _, kind := range []string{"IOU", "MPT"} {
		t.Run(kind+" existing holding", func(t *testing.T) {
			f := newLoanSetAssetFixture(t, kind)
			f.createHolding(f.borrower)
			f.createHolding(f.owner)
			before := f.env.OwnerCount(f.borrower)
			jtx.RequireTxSuccess(t, submitLoanSet(t, f, f.borrower, f.owner, false))
			if got := f.env.OwnerCount(f.borrower); got != before+1 {
				t.Fatalf("borrower OwnerCount = %d, want %d", got, before+1)
			}
			assertLoanSetOwnedObjects(t, f, f.borrower)
		})
	}

	t.Run("IOU origination fee creates broker owner holding", func(t *testing.T) {
		kind := "IOU"
		f := newLoanSetAssetFixture(t, kind)
		ownerBefore := f.env.OwnerCount(f.owner)
		if f.env.LedgerEntryExists(f.holdingKey(f.owner)) {
			t.Fatal("broker owner unexpectedly has underlying holding before LoanSet")
		}
		jtx.RequireTxSuccess(t, submitLoanSet(t, f, f.borrower, f.owner, true))
		if got := f.env.OwnerCount(f.owner); got != ownerBefore+1 {
			t.Fatalf("broker owner OwnerCount = %d, want %d", got, ownerBefore+1)
		}
		if !ownerDirectoryContains(t, f.env, f.owner.AccountID(), f.holdingKey(f.owner)) {
			t.Error("underlying holding missing from broker owner directory")
		}
		if got := f.env.OwnerCount(f.borrower); got != 2 {
			t.Fatalf("borrower OwnerCount = %d, want 2", got)
		}
		assertLoanSetOwnedObjects(t, f, f.borrower)
	})
}

func TestLoanSetHoldingReserve(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want jtx.TxResultCode
	}{
		{kind: "IOU", want: jtx.TecNO_LINE_INSUF_RESERVE},
		{kind: "MPT", want: jtx.TecINSUFFICIENT_RESERVE},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			f := newLoanSetAssetFixture(t, tc.kind)
			f.createHolding(f.owner)
			before := f.env.OwnerCount(f.borrower)
			target := f.env.ReserveBase() + uint64(before+1)*f.env.ReserveIncrement()
			balance := f.env.Balance(f.borrower)
			jtx.RequireTxSuccess(t, f.env.Submit(paytest.Pay(f.borrower, f.issuer, balance-target-f.env.BaseFee()).Build()))

			jtx.RequireTxClaimed(t, submitLoanSet(t, f, f.borrower, f.owner, false), tc.want)
			if got := f.env.OwnerCount(f.borrower); got != before {
				t.Fatalf("borrower OwnerCount after failed LoanSet = %d, want %d", got, before)
			}
			if f.env.LedgerEntryExists(f.holdingKey(f.borrower)) || f.env.LedgerEntryExists(keylet.Loan(f.brokerKey, 1)) {
				t.Fatal("failed LoanSet committed holding or Loan")
			}

			f.env.Pay(f.borrower, f.env.ReserveIncrement()+20)
			jtx.RequireTxSuccess(t, submitLoanSet(t, f, f.borrower, f.owner, false))
			if got := f.env.OwnerCount(f.borrower); got != before+2 {
				t.Fatalf("borrower OwnerCount after top-up = %d, want %d", got, before+2)
			}
			assertLoanSetOwnedObjects(t, f, f.borrower)
		})
	}
}

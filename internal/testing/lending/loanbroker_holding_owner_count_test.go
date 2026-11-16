package lending_test

import (
	"encoding/hex"
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
)

type holdingOwnerCountFixture struct {
	env      *jtx.TestEnv
	owner    *jtx.Account
	amount   func(int64) tx.Amount
	holding  keylet.Keylet
	brokerID string
}

func newHoldingOwnerCountFixture(t *testing.T, kind string, fix340 bool) *holdingOwnerCountFixture {
	t.Helper()
	env := newLendingEnv(t)
	if fix340 {
		env.EnableFeature("fixCleanup3_4_0")
		env.Close()
	}
	issuer, owner := jtx.NewAccount(kind+"-holding-issuer"), jtx.NewAccount(kind+"-holding-owner")
	env.FundAmount(issuer, 100_000_000_000)
	env.FundAmount(owner, 100_000_000_000)

	f := &holdingOwnerCountFixture{env: env, owner: owner}
	var asset tx.Asset
	var removeHolding func()
	switch kind {
	case "IOU":
		asset = tx.Asset{Currency: "USD", Issuer: issuer.Address}
		f.amount = func(v int64) tx.Amount { return tx.NewIssuedAmountFromFloat64(float64(v), "USD", issuer.Address) }
		f.holding = keylet.Line(owner.AccountID(), issuer.AccountID(), "USD")
		env.Trust(owner, f.amount(15_000))
		env.PayIOU(issuer, owner, issuer, "USD", 15_000)
		removeHolding = func() { env.Trust(owner, f.amount(0)) }
	case "MPT":
		token := mpttest.NewMPTTester(t, env, issuer, mpttest.MPTInit{Holders: []*jtx.Account{owner}})
		token.Create(mpttest.CreateOpts{Flags: mpttest.TfMPTCanTransfer})
		token.Authorize(mpttest.AuthorizeOpts{Account: owner})
		token.Pay(issuer, owner, 15_000)
		raw, err := hex.DecodeString(token.IssuanceID())
		if err != nil || len(raw) != 24 {
			t.Fatalf("decode MPT issuance ID %q: %v", token.IssuanceID(), err)
		}
		var id [24]byte
		copy(id[:], raw)
		asset = tx.Asset{MPTIssuanceID: token.IssuanceID()}
		f.amount = token.MPTAmount
		f.holding = keylet.MPTokenByID(id, owner.AccountID())
		removeHolding = func() { token.Authorize(mpttest.AuthorizeOpts{Account: owner, Flags: mpttest.TfMPTUnauthorize}) }
	default:
		t.Fatalf("unsupported asset kind %q", kind)
	}

	vaultSeq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, asset)
	create.Common.Fee = reserveIncrement
	jtx.RequireTxSuccess(t, env.Submit(create))
	jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(owner.Address, vaultID(owner, vaultSeq), f.amount(10_000))))
	brokerSeq := env.Seq(owner)
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vaultID(owner, vaultSeq))))
	f.brokerID = brokerID(owner, brokerSeq)
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerCoverDeposit(owner.Address, f.brokerID, f.amount(5_000))))
	env.Close()

	removeHolding()
	env.Close()
	if env.LedgerEntryExists(f.holding) {
		t.Fatalf("%s owner holding not removed", kind)
	}
	return f
}

func TestLoanBrokerRecreatedHoldingOwnerCount(t *testing.T) {
	for _, fix340 := range []bool{false, true} {
		for _, kind := range []string{"IOU", "MPT"} {
			t.Run(fmt.Sprintf("CoverWithdraw/%s/fixCleanup3_4_0=%t", kind, fix340), func(t *testing.T) {
				f := newHoldingOwnerCountFixture(t, kind, fix340)
				before := f.env.OwnerCount(f.owner)
				jtx.RequireTxSuccess(t, f.env.Submit(lending.NewLoanBrokerCoverWithdraw(f.owner.Address, f.brokerID, f.amount(1_000))))
				f.env.Close()
				if !f.env.LedgerEntryExists(f.holding) {
					t.Fatal("owner holding not recreated")
				}
				if got := f.env.OwnerCount(f.owner); got != before+1 {
					t.Fatalf("owner OwnerCount = %d, want %d", got, before+1)
				}
			})
		}
		// An MPT payout to an owner without an MPToken fails with tecNO_AUTH, so only
		// an IOU payout recreates the holding.
		t.Run(fmt.Sprintf("Delete/IOU/fixCleanup3_4_0=%t", fix340), func(t *testing.T) {
			f := newHoldingOwnerCountFixture(t, "IOU", fix340)
			before := f.env.OwnerCount(f.owner)
			jtx.RequireTxSuccess(t, f.env.Submit(lending.NewLoanBrokerDelete(f.owner.Address, f.brokerID)))
			f.env.Close()
			if !f.env.LedgerEntryExists(f.holding) {
				t.Fatal("owner trust line not recreated")
			}
			if got := f.env.OwnerCount(f.owner); got != before-1 {
				t.Fatalf("owner OwnerCount = %d, want %d", got, before-1)
			}
		})
	}
}

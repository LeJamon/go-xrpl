package lending_test

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
)

type destinationTrustState int

const (
	destinationZeroLimit destinationTrustState = iota
	destinationReachedLimit
	destinationNearLimit
	destinationNoTrustLine
)

func (s destinationTrustState) String() string {
	return [...]string{"ZeroLimit", "ReachedLimit", "NearLimit", "NoTrustLine"}[s]
}

// destinationOrderedAgainst returns a deterministic account that sorts on the
// requested side of issuer, so both trust-line balance orientations are covered.
func destinationOrderedAgainst(t *testing.T, issuer *jtx.Account, wantLow bool) *jtx.Account {
	t.Helper()
	for i := range 64 {
		acc := jtx.NewAccount(fmt.Sprintf("destination-%d", i))
		if keylet.IsLowAccount(acc.AccountID(), issuer.AccountID()) == wantLow {
			return acc
		}
	}
	t.Fatalf("no destination account found with low=%t against issuer", wantLow)
	return nil
}

func setDestinationTrustState(t *testing.T, f *loanSetAssetFixture, dest *jtx.Account, s destinationTrustState) {
	t.Helper()
	limit := tx.NewIssuedAmountFromFloat64(1_000, "USD", f.issuer.Address)
	switch s {
	case destinationZeroLimit:
		trust := trustset.NewTrustSet(dest.Address, tx.NewIssuedAmountFromFloat64(0, "USD", f.issuer.Address))
		// A QualityIn keeps the zero-limit line from being auto-deleted.
		qualityIn := uint32(10_000_000)
		trust.QualityIn = &qualityIn
		jtx.RequireTxSuccess(t, f.env.Submit(trust))
	case destinationReachedLimit:
		f.env.Trust(dest, limit)
		f.env.PayIOU(f.issuer, dest, f.issuer, "USD", 1_000)
	case destinationNearLimit:
		f.env.Trust(dest, limit)
		f.env.PayIOU(f.issuer, dest, f.issuer, "USD", 950)
	case destinationNoTrustLine:
	}
	f.env.Close()
}

func TestWithdrawToDestinationEnforcesTrustLimit(t *testing.T) {
	states := []destinationTrustState{
		destinationZeroLimit,
		destinationReachedLimit,
		destinationNearLimit,
		destinationNoTrustLine,
	}
	for _, s := range states {
		for _, destIsLow := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/destination_low=%t", s, destIsLow), func(t *testing.T) {
				f := newLoanSetAssetFixture(t, "IOU")
				dest := destinationOrderedAgainst(t, f.issuer, destIsLow)
				f.env.FundAmount(dest, 1_000_000_000)
				setDestinationTrustState(t, f, dest, s)

				f.fundHolding(f.owner, 2_000)
				jtx.RequireTxSuccess(t, f.env.Submit(vault.NewVaultDeposit(f.owner.Address, f.vaultID, f.amount(1_000))))
				jtx.RequireTxSuccess(t, f.env.Submit(lending.NewLoanBrokerCoverDeposit(f.owner.Address, f.brokerID, f.amount(1_000))))
				f.env.Close()

				before := f.env.IOUBalance(dest, f.issuer, "USD").Value()

				withdraw := vault.NewVaultWithdraw(f.owner.Address, f.vaultID, f.amount(100))
				withdraw.Destination = dest.Address
				jtx.RequireTxClaimed(t, f.env.Submit(withdraw), jtx.TecNO_LINE)

				cover := lending.NewLoanBrokerCoverWithdraw(f.owner.Address, f.brokerID, f.amount(100))
				cover.Destination = dest.Address
				jtx.RequireTxClaimed(t, f.env.Submit(cover), jtx.TecNO_LINE)

				if got := f.env.IOUBalance(dest, f.issuer, "USD").Value(); got != before {
					t.Fatalf("destination balance = %s, want unchanged %s", got, before)
				}

				if s != destinationNearLimit {
					return
				}
				withdraw = vault.NewVaultWithdraw(f.owner.Address, f.vaultID, f.amount(25))
				withdraw.Destination = dest.Address
				jtx.RequireTxSuccess(t, f.env.Submit(withdraw))
				cover = lending.NewLoanBrokerCoverWithdraw(f.owner.Address, f.brokerID, f.amount(25))
				cover.Destination = dest.Address
				jtx.RequireTxSuccess(t, f.env.Submit(cover))
				if got := f.env.IOUBalance(dest, f.issuer, "USD").Value(); got != "1000" {
					t.Fatalf("destination balance = %s, want limit 1000", got)
				}
			})
		}
	}
}

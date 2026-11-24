package tx

import (
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestAccountFundsNoFreezeStrictRejectsReserveOverflow(t *testing.T) {
	var accountID [20]byte
	accountID[0] = 1
	account, err := state.EncodeAccountID(accountID)
	if err != nil {
		t.Fatalf("EncodeAccountID: %v", err)
	}
	data, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:    account,
		Balance:    1_000_000,
		OwnerCount: math.MaxUint32,
	})
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	view := newMockBaseView()
	view.data[keylet.Account(accountID).Key] = data

	if _, err := AccountFundsNoFreezeStrict(view, accountID, NewXRPAmount(1), 0, math.MaxUint64); err == nil {
		t.Fatal("AccountFundsNoFreezeStrict multiplication overflow error = nil")
	}

	data, err = state.SerializeAccountRoot(&state.AccountRoot{
		Account:    account,
		Balance:    1_000_000,
		OwnerCount: 1,
	})
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	view.data[keylet.Account(accountID).Key] = data
	if _, err := AccountFundsNoFreezeStrict(view, accountID, NewXRPAmount(1), math.MaxUint64, 1); err == nil {
		t.Fatal("AccountFundsNoFreezeStrict addition overflow error = nil")
	}
}

func TestXRPLiquidUsesSponsoredReserveCounts(t *testing.T) {
	var accountID [20]byte
	accountID[0] = 2
	accountAddress, err := state.EncodeAccountID(accountID)
	if err != nil {
		t.Fatalf("EncodeAccountID: %v", err)
	}
	account := &state.AccountRoot{
		Account:              accountAddress,
		Balance:              30,
		OwnerCount:           5,
		SponsoredOwnerCount:  2,
		SponsoringOwnerCount: 1,
	}
	data, err := state.SerializeAccountRoot(account)
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	view := newMockBaseView()
	view.rules = amendment.NewRules([][32]byte{amendment.FeatureSponsor})
	view.data[keylet.Account(accountID).Key] = data

	if got := XRPLiquidOf(nil, accountID, account, 0, 10, 2).Drops(); got != 12 {
		t.Fatalf("XRPLiquidOf(nil) = %d, want 12", got)
	}
	if got := XRPLiquid(view, accountID, 0, 10, 2).Drops(); got != 12 {
		t.Fatalf("XRPLiquid = %d, want 12", got)
	}
	if got := XRPLiquid(view, accountID, 1, 10, 2).Drops(); got != 10 {
		t.Fatalf("XRPLiquid(+1) = %d, want 10", got)
	}
}

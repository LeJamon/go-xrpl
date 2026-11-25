package tx

import (
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestMeetsReserveAfterDebit(t *testing.T) {
	for _, test := range []struct {
		balance, debit uint64
		reserve        drops.XRPAmount
		want           bool
	}{
		{11, 1, 10, true},
		{10, 1, 10, false},
		{1, 1, 0, true},
		{1, 2, 0, false},
		{1, 2, -1, true},
		{1, 3, -1, false},
		{0, 1 << 63, math.MinInt64, true},
		{0, 1<<63 + 1, math.MinInt64, false},
	} {
		if got := MeetsReserveAfterDebit(test.balance, test.debit, test.reserve); got != test.want {
			t.Errorf("balance %d less %d meets reserve %d: got %t, want %t", test.balance, test.debit, test.reserve, got, test.want)
		}
	}
}

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

	if _, err := AccountFundsNoFreezeStrict(view, accountID, NewXRPAmount(1), drops.Fees{Increment: math.MaxInt64}); err == nil {
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
	if _, err := AccountFundsNoFreezeStrict(view, accountID, NewXRPAmount(1), drops.Fees{Reserve: math.MaxInt64, Increment: 1}); err == nil {
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

	fees := drops.Fees{Reserve: 10, Increment: 2}
	if got := XRPLiquidOf(nil, accountID, account, 0, fees).Drops(); got != 12 {
		t.Fatalf("XRPLiquidOf(nil) = %d, want 12", got)
	}
	if got := XRPLiquid(view, accountID, 0, fees).Drops(); got != 12 {
		t.Fatalf("XRPLiquid = %d, want 12", got)
	}
	if got := XRPLiquid(view, accountID, 1, fees).Drops(); got != 10 {
		t.Fatalf("XRPLiquid(+1) = %d, want 10", got)
	}
}

func TestXRPLiquidSignedReserve(t *testing.T) {
	var accountID [20]byte
	accountID[0] = 3
	accountAddress, err := state.EncodeAccountID(accountID)
	if err != nil {
		t.Fatalf("EncodeAccountID: %v", err)
	}
	account := &state.AccountRoot{Account: accountAddress, Balance: 100}
	view := newMockBaseView()
	view.data[keylet.Account(accountID).Key], err = state.SerializeAccountRoot(account)
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	reserve := drops.XRPAmount(-10)
	config := EngineConfig{ReserveBase: uint64(reserve)}
	fees := config.Fees()
	if got := XRPLiquidOf(view, accountID, account, 0, fees).Drops(); got != 110 {
		t.Fatalf("XRPLiquidOf(negative reserve) = %d, want 110", got)
	}
	funds, err := AccountFundsNoFreezeStrict(view, accountID, NewXRPAmount(1), fees)
	if err != nil {
		t.Fatalf("AccountFundsNoFreezeStrict(negative reserve): %v", err)
	}
	if got := funds.Drops(); got != 110 {
		t.Fatalf("AccountFundsNoFreezeStrict(negative reserve) = %d, want 110", got)
	}
}

func TestXRPLiquidSignedReserveOverflowFailsClosed(t *testing.T) {
	var accountID [20]byte
	accountID[0] = 4
	accountAddress, err := state.EncodeAccountID(accountID)
	if err != nil {
		t.Fatalf("EncodeAccountID: %v", err)
	}
	account := &state.AccountRoot{Account: accountAddress, Balance: uint64(drops.MaxDrops), OwnerCount: 91}
	view := newMockBaseView()
	view.data[keylet.Account(accountID).Key], err = state.SerializeAccountRoot(account)
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	fees := drops.Fees{Reserve: -drops.MaxDrops, Increment: -drops.MaxDrops}
	if got := XRPLiquidOf(view, accountID, account, 0, fees).Drops(); got != 0 {
		t.Fatalf("XRPLiquidOf(overflow) = %d, want 0", got)
	}
	if _, err := AccountFundsNoFreezeStrict(view, accountID, NewXRPAmount(1), fees); err == nil {
		t.Fatal("AccountFundsNoFreezeStrict overflow error = nil")
	}
}

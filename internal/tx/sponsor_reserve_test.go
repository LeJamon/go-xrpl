package tx

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestSyncSenderSponsorCountsPreservesPendingOwnerChanges(t *testing.T) {
	var accountID [20]byte
	accountID[0] = 3
	accountAddress, err := state.EncodeAccountID(accountID)
	if err != nil {
		t.Fatalf("EncodeAccountID: %v", err)
	}
	viewAccount := &state.AccountRoot{
		Account:                accountAddress,
		OwnerCount:             5,
		SponsoredOwnerCount:    2,
		SponsoringOwnerCount:   3,
		SponsoringAccountCount: 1,
	}
	data, err := state.SerializeAccountRoot(viewAccount)
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	view := newMockBaseView()
	view.data[keylet.Account(accountID).Key] = data
	ctx := &ApplyContext{
		View:      view,
		AccountID: accountID,
		Account: &state.AccountRoot{
			Account:                accountAddress,
			OwnerCount:             7,
			SponsoredOwnerCount:    4,
			SponsoringOwnerCount:   6,
			SponsoringAccountCount: 2,
		},
	}

	ctx.SyncSenderSponsorCounts(accountAddress)

	if ctx.Account.OwnerCount != 7 || ctx.Account.SponsoredOwnerCount != 4 ||
		ctx.Account.SponsoringAccountCount != 2 {
		t.Fatalf("pending counters were overwritten: %+v", ctx.Account)
	}
	if ctx.Account.SponsoringOwnerCount != 3 {
		t.Fatalf("SponsoringOwnerCount = %d, want 3", ctx.Account.SponsoringOwnerCount)
	}
}

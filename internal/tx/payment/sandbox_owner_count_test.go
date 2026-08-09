package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestPaymentSandboxPreservesMaximumEffectiveOwnerCount(t *testing.T) {
	root := NewPaymentSandbox(newPaymentMockLedgerView())
	var owner, sponsor [20]byte
	owner[19] = 1
	sponsor[19] = 2

	ownerBefore := tx.OwnerCounts{OwnerCount: 1, SponsoredOwnerCount: 1}
	ownerAfter := tx.OwnerCounts{}
	sponsorBefore := tx.OwnerCounts{SponsoringOwnerCount: 1}
	sponsorAfter := tx.OwnerCounts{}
	root.AdjustOwnerCount(owner, ownerBefore, ownerAfter)
	root.AdjustOwnerCount(sponsor, sponsorBefore, sponsorAfter)

	if got := root.OwnerCountHook(owner, ownerAfter).Count(); got != 0 {
		t.Fatalf("sponsored owner effective count = %d, want 0", got)
	}
	if got := root.OwnerCountHook(sponsor, sponsorAfter).Count(); got != 1 {
		t.Fatalf("sponsor effective count = %d, want 1", got)
	}

	child := NewChildSandbox(root)
	child.AdjustOwnerCount(sponsor, sponsorAfter, tx.OwnerCounts{SponsoringOwnerCount: 2})
	if err := child.Apply(root); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := root.OwnerCountHook(sponsor, sponsorAfter).Count(); got != 2 {
		t.Fatalf("merged sponsor effective count = %d, want 2", got)
	}
}

func TestSponsoredDeletionHooksOwnerAndSponsorCounts(t *testing.T) {
	view := newPaymentMockLedgerView()
	var owner, sponsor [20]byte
	owner[19] = 1
	sponsor[19] = 2
	sponsorAddress := state.EncodeAccountIDSafe(sponsor)
	ownerData, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:             state.EncodeAccountIDSafe(owner),
		OwnerCount:          1,
		SponsoredOwnerCount: 1,
	})
	if err != nil {
		t.Fatalf("serialize owner: %v", err)
	}
	sponsorData, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:              sponsorAddress,
		SponsoringOwnerCount: 1,
	})
	if err != nil {
		t.Fatalf("serialize sponsor: %v", err)
	}
	view.data[keylet.Account(owner).Key] = ownerData
	view.data[keylet.Account(sponsor).Key] = sponsorData
	sandbox := NewPaymentSandbox(view)

	if result := tx.DecreaseOwnerCountOnView(sandbox, owner, sponsorAddress, 1); result != ter.TesSUCCESS {
		t.Fatalf("DecreaseOwnerCountOnView = %s", result)
	}
	ownerAfter, err := tx.ReadAccountRoot(sandbox, owner)
	if err != nil {
		t.Fatalf("read owner: %v", err)
	}
	sponsorAfter, err := tx.ReadAccountRoot(sandbox, sponsor)
	if err != nil {
		t.Fatalf("read sponsor: %v", err)
	}
	if got := sandbox.OwnerCountHook(owner, tx.NewOwnerCounts(ownerAfter)).Count(); got != 0 {
		t.Fatalf("owner hooked effective count = %d, want 0", got)
	}
	if got := sandbox.OwnerCountHook(sponsor, tx.NewOwnerCounts(sponsorAfter)).Count(); got != 1 {
		t.Fatalf("sponsor hooked effective count = %d, want 1", got)
	}
}

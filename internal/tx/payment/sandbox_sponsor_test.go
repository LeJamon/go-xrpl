package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
)

func TestOwnerCountsHookKeepsSponsoredReserveState(t *testing.T) {
	var account [20]byte
	account[0] = 1
	before := tx.OwnerCounts{Owner: 5, Sponsored: 1}
	after := tx.OwnerCounts{Owner: 4}
	sandbox := &PaymentSandbox{tab: newDeferredCredits()}

	sandbox.AdjustOwnerCounts(account, before, after)

	if got := sandbox.OwnerCountsHook(account, after); got != before {
		t.Fatalf("OwnerCountsHook = %+v, want %+v", got, before)
	}
}

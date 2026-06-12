// Regression test for the reserve-vs-prior-balance fee bug (issue #887). The
// reserve check in doApply must compare against the balance before the ACTUAL
// fee was deducted (rippled's mPriorBalance), not balance + base fee. When a tx
// pays a fee larger than the base fee, the old "balance + baseFee" form
// understated the prior balance and spuriously returned tecINSUFFICIENT_RESERVE
// at the reserve boundary.
// Reference: rippled CreateTicket.cpp doApply — `if (mPriorBalance < reserve)`.
package ticket_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/ticket"
)

func TestTicketCreate_ReserveUsesPriorBalanceWithActualFee(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")

	// Fund alice with exactly the reserve for one extra owned object (the ticket
	// being created). A freshly funded account owns 0 objects, so the required
	// reserve is accountReserve(1).
	reserve := accountReserve(env, 1)
	env.FundAmount(alice, reserve)
	env.Close()

	// Sanity: alice's true balance is exactly the boundary reserve.
	if got := env.Balance(alice); got != reserve {
		t.Fatalf("setup: expected alice balance == reserve (%d), got %d", reserve, got)
	}

	// Pay a fee well above the base fee. The prior balance equals the true
	// balance (reserve), which exactly meets the reserve — so the tx must
	// succeed. Under the old "balance + baseFee" form the prior balance would be
	// reserve-(fee-baseFee) < reserve and the tx would wrongly fail with
	// tecINSUFFICIENT_RESERVE.
	escalatedFee := int64(env.BaseFee() * 100)
	r := env.Submit(ticket.TicketCreate(alice, 1).Fee(escalatedFee).Build())
	jtx.RequireTxSuccess(t, r)
}

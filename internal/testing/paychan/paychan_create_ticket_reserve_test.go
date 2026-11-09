package paychan

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
)

// Without Sponsor, PaymentChannelCreate checks the reserve in preclaim, where
// a ticket the transaction consumes still counts toward OwnerCount.
func TestPaymentChannelCreateTicketCountsTowardReserve(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sponsor bool
		want    string
	}{
		{name: "without-sponsor", want: "tecINSUFFICIENT_RESERVE"},
		{name: "with-sponsor", sponsor: true, want: "tesSUCCESS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			if !tc.sponsor {
				env.DisableFeature("Sponsor")
			}
			alice := jtx.NewAccount("alice")
			bob := jtx.NewAccount("bob")
			env.FundAmount(bob, uint64(xrp(10000)))
			env.FundAmount(alice, env.ReserveBase()+env.ReserveIncrement()+1000+env.BaseFee())
			env.Close()
			ticket := env.CreateTickets(alice, 1)
			env.Close()

			// Balance is reserve(1)+1000: enough once the ticket is gone, short
			// of reserve(2) while it still counts.
			result := env.Submit(jtx.WithTicketSeq(ChannelCreate(alice, bob, drops(100), 3600, alice.PublicKeyHex()).Build(), ticket))
			if tc.want == "tesSUCCESS" {
				jtx.RequireTxSuccess(t, result)
			} else {
				jtx.RequireTxClaimed(t, result, tc.want)
			}
		})
	}
}

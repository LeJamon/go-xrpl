package oracle_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	oracletest "github.com/LeJamon/go-xrpl/internal/testing/oracle"
)

// OracleSet checks the reserve in preclaim, where a ticket the transaction
// consumes still counts toward OwnerCount.
func TestOracleSetTicketCountsTowardReserve(t *testing.T) {
	env := jtx.NewTestEnv(t)
	owner := jtx.NewAccount("owner")
	env.FundAmount(owner, env.ReserveBase()+env.ReserveIncrement()+1000+env.BaseFee())
	env.Close()
	ticket := env.CreateTickets(owner, 1)
	env.Close()

	set := oracletest.OracleSet(owner, 1, defaultLUT(env)).
		ProviderHex(32).AssetClassHex(8).
		AddPrice("XRP", "USD", 740, 1).Build()
	result := env.Submit(jtx.WithTicketSeq(set, ticket))
	jtx.RequireTxClaimed(t, result, "tecINSUFFICIENT_RESERVE")
}

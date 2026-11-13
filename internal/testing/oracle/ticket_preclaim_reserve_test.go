package oracle_test

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	oracletest "github.com/LeJamon/go-xrpl/internal/testing/oracle"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestOracleSetPreclaimReserve(t *testing.T) {
	for _, tc := range []struct {
		name               string
		oldPairs, newPairs int
	}{
		{"create", 0, 1},
		{"grow", 5, 6},
		{"shrink", 6, 5},
		{"update", 5, 5},
	} {
		for _, ticketed := range []bool{false, true} {
			for _, below := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/ticket=%v/below=%v", tc.name, ticketed, below), func(t *testing.T) {
					env := jtx.NewTestEnv(t)
					owner := jtx.NewAccount("owner")
					env.Fund(owner)
					env.Close()
					oracleKey := keylet.Oracle(owner.ID, 1)
					var original []byte
					var oldOwners uint32
					if tc.oldPairs > 0 {
						create := oracletest.OracleSet(owner, 1, defaultLUT(env)).ProviderHex(32).AssetClassHex(8)
						for _, quote := range []string{"USD", "EUR", "GBP", "CNY", "CAD", "AUD"}[:tc.oldPairs] {
							create.AddPrice("XRP", quote, 740, 1)
						}
						jtx.RequireTxSuccess(t, env.Submit(create.Build()))
						env.Close()
						var err error
						original, err = env.LedgerEntry(oracleKey)
						require.NoError(t, err)
						oldOwners = 1
						if tc.oldPairs > 5 {
							oldOwners = 2
						}
					}
					var ticketSeq uint32
					if ticketed {
						ticketSeq = env.CreateTickets(owner, 1)
						env.Close()
					}
					newOwners := uint32(1)
					if tc.newPairs > 5 {
						newOwners = 2
					}
					preclaimOwners := newOwners
					if ticketed {
						preclaimOwners++
					}
					balance := env.ReserveBase() + uint64(preclaimOwners)*env.ReserveIncrement()
					if below {
						balance--
					}
					accountKey := keylet.Account(owner.ID)
					data, err := env.LedgerEntry(accountKey)
					require.NoError(t, err)
					root, err := state.ParseAccountRoot(data)
					require.NoError(t, err)
					root.Balance = balance
					data, err = state.SerializeAccountRoot(root)
					require.NoError(t, err)
					require.NoError(t, env.Ledger().Update(accountKey, data))
					sequence := env.Seq(owner)

					update := oracletest.OracleSet(owner, 1, defaultLUT(env)).Fee(env.BaseFee())
					switch tc.name {
					case "create":
						update.ProviderHex(32).AssetClassHex(8).AddPrice("XRP", "USD", 740, 1)
					case "grow":
						update.AddPrice("XRP", "AUD", 740, 1)
					case "shrink":
						update.AddDelete("XRP", "AUD")
					case "update":
						update.AddPrice("XRP", "USD", 741, 1)
					}
					txn := update.Build()
					if ticketed {
						jtx.WithTicketSeq(txn, ticketSeq)
					}
					result := env.Submit(txn)
					if below {
						jtx.RequireTxFail(t, result, "tecINSUFFICIENT_RESERVE")
					} else {
						jtx.RequireTxSuccess(t, result)
					}
					require.True(t, result.Applied)
					require.Equal(t, env.BaseFee(), result.Fee)
					jtx.RequireBalance(t, env, owner, balance-result.Fee)
					if ticketed {
						jtx.RequireSequence(t, env, owner, sequence)
						jtx.RequireLedgerEntryNotExists(t, env, keylet.Ticket(owner.ID, ticketSeq))
					} else {
						jtx.RequireSequence(t, env, owner, sequence+1)
					}
					jtx.RequireTicketCount(t, env, owner, 0)
					if below {
						jtx.RequireOwnerCount(t, env, owner, oldOwners)
						if tc.oldPairs == 0 {
							jtx.RequireLedgerEntryNotExists(t, env, oracleKey)
						} else {
							after, err := env.LedgerEntry(oracleKey)
							require.NoError(t, err)
							require.Equal(t, original, after)
						}
					} else {
						jtx.RequireOwnerCount(t, env, owner, newOwners)
						require.Len(t, oraclePairs(t, env, owner, 1), tc.newPairs)
					}
				})
			}
		}
	}
}

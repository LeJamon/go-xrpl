package escrow_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	"github.com/LeJamon/go-xrpl/internal/tx"
	sponsortx "github.com/LeJamon/go-xrpl/internal/tx/sponsor"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestEscrowFinishDestinationIsReserveSponsor(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, submitterRole := range []string{"owner", "destination", "other"} {
			t.Run(fmt.Sprintf("cleanup=%t/submitter=%s", cleanup, submitterRole), func(t *testing.T) {
				env := jtx.NewTestEnv(t)
				env.EnableFeature("Sponsor")
				if cleanup {
					env.EnableFeature("fixCleanup3_4_0")
				} else {
					env.DisableFeature("fixCleanup3_4_0")
				}
				owner := jtx.NewAccount("owner")
				destination := jtx.NewAccount("destination")
				other := jtx.NewAccount("other")
				env.Fund(owner, destination, other)
				env.Close()

				remaining := int32(1)
				set := sponsortx.NewSponsorshipSet(destination.Address)
				set.Sponsee = owner.Address
				set.RemainingOwnerCountDelta = &remaining
				jtx.RequireTxSuccess(t, env.Submit(set))

				const amount = int64(1_000_000)
				sequence := env.Seq(owner)
				create := escrow.EscrowCreate(owner, destination, amount).
					FinishTime(env.Now().Add(time.Second)).Build()
				flags := uint32(tx.SpfSponsorReserve)
				create.Common.Sponsor = destination.Address
				create.Common.SponsorFlags = &flags
				jtx.RequireTxSuccess(t, env.Submit(create))
				env.Close()

				readAccount := func(account *jtx.Account) *state.AccountRoot {
					root, err := state.ReadAccountRoot(env.Ledger(), account.ID)
					require.NoError(t, err)
					require.NotNil(t, root)
					return root
				}
				require.Equal(t, uint32(1), readAccount(owner).SponsoredOwnerCount)
				require.Equal(t, uint32(1), readAccount(destination).SponsoringOwnerCount)
				submitters := map[string]*jtx.Account{"owner": owner, "destination": destination, "other": other}
				submitter := submitters[submitterRole]
				accounts := []*jtx.Account{owner, destination, other}
				balances := make(map[*jtx.Account]uint64, len(accounts))
				sequences := make(map[*jtx.Account]uint32, len(accounts))
				for _, account := range accounts {
					balances[account] = env.Balance(account)
					sequences[account] = env.Seq(account)
				}
				sponsorOwners := env.OwnerCount(destination)

				result := env.Submit(escrow.EscrowFinish(submitter, owner, sequence).Build())
				jtx.RequireTxSuccess(t, result)
				require.Equal(t, env.BaseFee(), result.Fee)
				require.Zero(t, readAccount(owner).OwnerCount)
				require.Zero(t, readAccount(owner).SponsoredOwnerCount)
				require.Zero(t, readAccount(destination).SponsoringOwnerCount)
				require.Equal(t, sponsorOwners, env.OwnerCount(destination))
				for _, account := range accounts {
					balance, seq := balances[account], sequences[account]
					if account == destination {
						balance += uint64(amount)
					}
					if account == submitter {
						balance -= result.Fee
						seq++
					}
					require.Equal(t, balance, env.Balance(account), account.Name)
					require.Equal(t, seq, env.Seq(account), account.Name)
				}
				escrowData, err := env.LedgerEntry(keylet.Escrow(owner.ID, sequence))
				require.NoError(t, err)
				require.Nil(t, escrowData)
				sponsorshipData, err := env.LedgerEntry(keylet.Sponsorship(destination.ID, owner.ID))
				require.NoError(t, err)
				sponsorship, err := state.ParseSponsorship(sponsorshipData)
				require.NoError(t, err)
				require.Zero(t, sponsorship.RemainingOwnerCount)

				require.NotNil(t, result.Metadata)
				deleted := metadata.FindNode(result.Metadata, "DeletedNode", "Escrow")
				require.NotNil(t, deleted)
				require.Equal(t, destination.Address, deleted.FinalFields["Sponsor"])
				for _, change := range []struct {
					account *jtx.Account
					field   string
				}{{owner, "SponsoredOwnerCount"}, {destination, "SponsoringOwnerCount"}} {
					node := metadata.FindNodeByAccount(result.Metadata, change.account.Address)
					require.NotNil(t, node)
					require.Equal(t, uint32(1), metadata.ToUint32(node.PreviousFields[change.field]))
					require.NotContains(t, node.FinalFields, change.field)
				}
			})
		}
	}
}

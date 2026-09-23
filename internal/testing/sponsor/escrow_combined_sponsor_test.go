package sponsor_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestEscrowFinishCombinedFeeAndReserveSponsor(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, test := range []struct {
			name        string
			prefunded   bool
			sameSponsor bool
		}{
			{name: "cosigned-different-sponsor"},
			{name: "prefunded-different-sponsor", prefunded: true},
			{name: "prefunded-same-sponsor", prefunded: true, sameSponsor: true},
		} {
			t.Run(fmt.Sprintf("cleanup=%t/%s", cleanup, test.name), func(t *testing.T) {
				env, owner, destination, issuer, originalSponsor, nextSponsor := newSponsoredIOUEnv(t, cleanup)
				sequence := env.Seq(owner)
				create := escrow.EscrowCreate(owner, destination, 0).
					IOUAmount(issuer.IOU("USD", 100)).FinishTime(env.Now().Add(time.Second)).Build()
				withReserveSponsor(create, originalSponsor)
				attachSponsorSignature(t, env, create, owner, originalSponsor)
				jtx.RequireTxSuccess(t, env.SubmitSigned(create))
				env.Close()

				if test.sameSponsor {
					nextSponsor = originalSponsor
				}
				fee := env.BaseFee()
				if test.prefunded {
					remaining := int32(1)
					jtx.RequireTxSuccess(t, setSponsorship(env, nextSponsor, destination, int64(2*fee), &remaining))
				}
				required := reserveForOneMoreObject(t, env, nextSponsor, test.sameSponsor)
				funding := required
				if !test.prefunded {
					funding += fee
				}
				setAccountBalance(t, env, nextSponsor, funding-1)
				escrowKey := keylet.Escrow(owner.ID, sequence)
				before, err := env.LedgerEntry(escrowKey)
				require.NoError(t, err)
				before = append([]byte(nil), before...)
				sourceBalance, sourceSequence := env.Balance(destination), env.Seq(destination)

				finish := func() jtx.TxResult {
					txn := escrow.EscrowFinish(destination, owner, sequence).Build()
					txn.Common.Sponsor = nextSponsor.Address
					flags := tx.SpfSponsorFee | tx.SpfSponsorReserve
					txn.Common.SponsorFlags = &flags
					if test.prefunded {
						return env.Submit(txn)
					}
					attachSponsorSignature(t, env, txn, destination, nextSponsor)
					return env.SubmitSigned(txn)
				}
				failed := finish()
				jtx.RequireTxClaimed(t, failed, "tecNO_LINE_INSUF_RESERVE")
				require.Equal(t, fee, failed.Fee)
				require.Equal(t, sourceBalance, env.Balance(destination))
				require.Equal(t, sourceSequence+1, env.Seq(destination))
				after, err := env.LedgerEntry(escrowKey)
				require.NoError(t, err)
				require.Equal(t, before, after)
				require.Equal(t, uint32(2), accountState(t, env, owner).OwnerCount)
				require.Equal(t, uint32(1), accountState(t, env, owner).SponsoredOwnerCount)
				require.Equal(t, uint32(1), accountState(t, env, originalSponsor).SponsoringOwnerCount)
				require.Zero(t, accountState(t, env, destination).OwnerCount)
				require.False(t, env.TrustLineExists(destination, issuer, "USD"))
				if test.prefunded {
					requireAffectedNodes(t, failed, "ModifiedNode/AccountRoot", "ModifiedNode/Sponsorship")
					budget := sponsorshipEntry(t, env, nextSponsor, destination)
					require.Equal(t, fee, budget.FeeAmount)
					require.Equal(t, uint32(1), budget.RemainingOwnerCount)
					require.Equal(t, funding-1, env.Balance(nextSponsor))
				} else {
					requireAffectedNodes(t, failed, "ModifiedNode/AccountRoot", "ModifiedNode/AccountRoot")
					require.Equal(t, funding-1-fee, env.Balance(nextSponsor))
				}
				sourceNode := metadata.FindNodeByAccount(failed.Metadata, destination.Address)
				require.NotNil(t, sourceNode)
				require.Equal(t, map[string]any{"Sequence": sourceSequence}, sourceNode.PreviousFields)

				setAccountBalance(t, env, nextSponsor, funding)
				result := finish()
				jtx.RequireTxSuccess(t, result)
				require.Equal(t, fee, result.Fee)
				require.Equal(t, sourceBalance, env.Balance(destination))
				require.Equal(t, sourceSequence+2, env.Seq(destination))
				require.Equal(t, required, env.Balance(nextSponsor))
				require.Equal(t, uint32(1), accountState(t, env, owner).OwnerCount)
				require.Zero(t, accountState(t, env, owner).SponsoredOwnerCount)
				require.Equal(t, uint32(1), accountState(t, env, destination).OwnerCount)
				require.Equal(t, uint32(1), accountState(t, env, destination).SponsoredOwnerCount)
				require.Equal(t, uint32(1), accountState(t, env, nextSponsor).SponsoringOwnerCount)
				require.Equal(t, issuer.IOU("USD", 100), *env.IOUBalance(destination, issuer, "USD"))
				require.Equal(t, nextSponsor.Address, trustLineSponsor(t, env, destination, issuer, "USD"))
				if !test.sameSponsor {
					require.Zero(t, accountState(t, env, originalSponsor).SponsoringOwnerCount)
				}
				if test.prefunded {
					budget := sponsorshipEntry(t, env, nextSponsor, destination)
					require.Zero(t, budget.FeeAmount)
					require.Zero(t, budget.RemainingOwnerCount)
				}
				deleted := metadata.FindNode(result.Metadata, "DeletedNode", "Escrow")
				require.NotNil(t, deleted)
				require.Equal(t, originalSponsor.Address, deleted.FinalFields["Sponsor"])
				created := metadata.FindNode(result.Metadata, "CreatedNode", "RippleState")
				require.NotNil(t, created)
				field := "LowSponsor"
				if state.CompareAccountIDs(destination.ID, issuer.ID) > 0 {
					field = "HighSponsor"
				}
				require.Equal(t, nextSponsor.Address, created.NewFields[field])
			})
		}
	}
}

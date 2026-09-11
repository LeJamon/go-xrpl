package sponsor_test

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
	checktx "github.com/LeJamon/go-xrpl/internal/tx/check"
	sponsortx "github.com/LeJamon/go-xrpl/internal/tx/sponsor"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestSponsorshipObjectEndReserve(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, sponsorEnds := range []bool{false, true} {
			for _, delta := range []int64{-1, 0, 1} {
				t.Run(fmt.Sprintf("cleanup=%t/sponsor_ends=%t/delta=%d", cleanup, sponsorEnds, delta), func(t *testing.T) {
					env, sponsee, destination, sponsor, _ := sponsorEnv(t)
					if cleanup {
						env.EnableFeature("fixCleanup3_4_0")
					} else {
						env.DisableFeature("fixCleanup3_4_0")
					}
					env.Close()
					sequence := env.Seq(sponsee)
					require.Equal(t, "tesSUCCESS", env.Submit(checktx.NewCheckCreate(
						sponsee.Address, destination.Address, tx.NewXRPAmount(1))).Code)
					object := keylet.Check(sponsee.ID, sequence)
					remaining := int32(2)
					require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, sponsee, 0, &remaining).Code)
					require.Equal(t, "tesSUCCESS", transferObject(env, sponsee, object, sponsor, sponsortx.SponsorshipTransferFlagCreate).Code)

					required := env.ReserveBase() + env.ReserveIncrement()
					balance := uint64(int64(required) + delta)
					setAccountBalance(t, env, sponsee, balance)
					objectBefore, err := env.LedgerEntry(object)
					require.NoError(t, err)
					budgetKey := keylet.Sponsorship(sponsor.ID, sponsee.ID)
					budgetBefore, err := env.LedgerEntry(budgetKey)
					require.NoError(t, err)
					payer := sponsee
					if sponsorEnds {
						payer = sponsor
					}
					sponseeBefore := accountState(t, env, sponsee)
					sponsorBefore := accountState(t, env, sponsor)
					end := sponsortx.NewSponsorshipTransfer(payer.Address)
					end.Fee = "10"
					end.ObjectID = hex.EncodeToString(object.Key[:])
					end.SetFlags(sponsortx.SponsorshipTransferFlagEnd)
					if sponsorEnds {
						end.Sponsee = sponsee.Address
					}
					result := env.Submit(end)
					failed := cleanup && delta < 0
					if failed {
						require.Equal(t, "tecINSUFFICIENT_RESERVE", result.Code)
						requireAffectedNodes(t, result, "ModifiedNode/AccountRoot")
						objectAfter, err := env.LedgerEntry(object)
						require.NoError(t, err)
						require.Equal(t, objectBefore, objectAfter)
					} else {
						require.Equal(t, "tesSUCCESS", result.Code)
						requireAffectedNodes(t, result,
							"ModifiedNode/AccountRoot", "ModifiedNode/AccountRoot", "ModifiedNode/Check")
						_, present := objectSponsor(t, env, object)
						require.False(t, present)
					}
					sponseeAfter := accountState(t, env, sponsee)
					sponsorAfter := accountState(t, env, sponsor)
					require.Equal(t, sponseeBefore.OwnerCount, sponseeAfter.OwnerCount)
					require.Equal(t, sponsorBefore.OwnerCount, sponsorAfter.OwnerCount)
					wantCount := uint32(0)
					if failed {
						wantCount = 1
					}
					require.Equal(t, wantCount, sponseeAfter.SponsoredOwnerCount)
					require.Equal(t, wantCount, sponsorAfter.SponsoringOwnerCount)
					if sponsorEnds {
						require.Equal(t, balance, sponseeAfter.Balance)
						require.Equal(t, sponsorBefore.Balance-10, sponsorAfter.Balance)
						require.Equal(t, sponseeBefore.Sequence, sponseeAfter.Sequence)
						require.Equal(t, sponsorBefore.Sequence+1, sponsorAfter.Sequence)
					} else {
						require.Equal(t, balance-10, sponseeAfter.Balance)
						require.Equal(t, sponsorBefore.Balance, sponsorAfter.Balance)
						require.Equal(t, sponseeBefore.Sequence+1, sponseeAfter.Sequence)
						require.Equal(t, sponsorBefore.Sequence, sponsorAfter.Sequence)
					}
					budgetAfter, err := env.LedgerEntry(budgetKey)
					require.NoError(t, err)
					require.Equal(t, budgetBefore, budgetAfter)
				})
			}
		}
	}
}

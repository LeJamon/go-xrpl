package sponsor_test

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	checktx "github.com/LeJamon/go-xrpl/internal/tx/check"
	sponsortx "github.com/LeJamon/go-xrpl/internal/tx/sponsor"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestSponsorshipAccountEndReserve(t *testing.T) {
	cleanupCases := []struct {
		name    string
		enabled bool
	}{
		{name: "cleanup-off", enabled: false},
		{name: "cleanup-on", enabled: true},
	}
	initiatorCases := []struct {
		name string
		use  func(*jtx.Account, *jtx.Account) *sponsortx.SponsorshipTransfer
	}{
		{
			name: "sponsee",
			use: func(sponsee, _ *jtx.Account) *sponsortx.SponsorshipTransfer {
				return sponsortx.NewSponsorshipTransfer(sponsee.Address)
			},
		},
		{
			name: "sponsor",
			use: func(sponsee, sponsor *jtx.Account) *sponsortx.SponsorshipTransfer {
				transfer := sponsortx.NewSponsorshipTransfer(sponsor.Address)
				transfer.Sponsee = sponsee.Address
				return transfer
			},
		},
	}
	for _, cleanup := range cleanupCases {
		for _, initiator := range initiatorCases {
			for _, delta := range []int64{-1, 0, 1} {
				name := fmt.Sprintf("%s/%s/%+d", cleanup.name, initiator.name, delta)
				t.Run(name, func(t *testing.T) {
					env, sponsee, sponsor, sponsoredObject := accountEndFixture(t, cleanup.enabled)

					sponseeKey := keylet.Account(sponsee.ID)
					sponsorKey := keylet.Account(sponsor.ID)
					sponseeSetup := accountState(t, env, sponsee)
					sponsorSetup := accountState(t, env, sponsor)
					require.Equal(t, uint32(2), sponseeSetup.OwnerCount)
					require.Equal(t, uint32(1), sponseeSetup.SponsoredOwnerCount)
					require.Equal(t, uint32(0), sponseeSetup.SponsoringOwnerCount)
					require.Equal(t, uint32(0), sponseeSetup.SponsoringAccountCount)
					require.Equal(t, uint32(1), sponsorSetup.SponsoringOwnerCount)
					require.Equal(t, uint32(1), sponsorSetup.SponsoringAccountCount)
					requiredReserve := env.ReserveBase() + env.ReserveIncrement()
					if delta < 0 {
						setAccountBalance(t, env, sponsee, requiredReserve-uint64(-delta))
					} else {
						setAccountBalance(t, env, sponsee, requiredReserve+uint64(delta))
					}

					sponseeBefore := *accountState(t, env, sponsee)
					sponsorBefore := *accountState(t, env, sponsor)
					objectBytesBefore, err := env.LedgerEntry(sponsoredObject)
					require.NoError(t, err)
					relationshipBefore := *sponsorshipEntry(t, env, sponsor, sponsee)

					end := initiator.use(sponsee, sponsor)
					end.SetFlags(sponsortx.SponsorshipTransferFlagEnd)
					end.Fee = "10"
					result := env.Submit(end)

					const fee uint64 = 10
					wantSuccess := delta >= 0
					source := sponsee
					if initiator.name == "sponsor" {
						source = sponsor
					}
					sourceBefore := &sponseeBefore
					if source == sponsor {
						sourceBefore = &sponsorBefore
					}
					if wantSuccess {
						require.Equal(t, "tesSUCCESS", result.Code)
					} else {
						require.Equal(t, "tecINSUFFICIENT_RESERVE", result.Code)
					}
					require.True(t, result.Applied)
					require.Equal(t, fee, result.Fee)
					require.NotNil(t, result.Metadata)
					require.Equal(t, result.Code, result.Metadata.TransactionResult.String())
					if wantSuccess {
						requireAffectedNodes(t, result, "ModifiedNode/AccountRoot", "ModifiedNode/AccountRoot")
					} else {
						requireAffectedNodes(t, result, "ModifiedNode/AccountRoot")
					}

					require.Equal(t, sourceBefore.Balance-fee, env.Balance(source))
					require.Equal(t, sourceBefore.Sequence+1, env.Seq(source))
					if source == sponsee {
						require.Equal(t, sponsorBefore.Balance, env.Balance(sponsor))
						require.Equal(t, sponsorBefore.Sequence, env.Seq(sponsor))
					} else {
						require.Equal(t, sponseeBefore.Balance, env.Balance(sponsee))
						require.Equal(t, sponseeBefore.Sequence, env.Seq(sponsee))
					}

					sponseeAfter := accountState(t, env, sponsee)
					sponsorAfter := accountState(t, env, sponsor)
					if wantSuccess {
						sponseeNode := affectedNodeByKey(result.Metadata, sponseeKey)
						sponsorNode := affectedNodeByKey(result.Metadata, sponsorKey)
						require.NotNil(t, sponseeNode)
						require.NotNil(t, sponsorNode)
						require.Equal(t, sponsor.Address, sponseeNode.PreviousFields["Sponsor"])
						require.NotContains(t, sponseeNode.FinalFields, "Sponsor")
						require.EqualValues(t, sponsorBefore.SponsoringAccountCount, sponsorNode.PreviousFields["SponsoringAccountCount"])
						require.NotContains(t, sponsorNode.FinalFields, "SponsoringAccountCount")
						require.False(t, sponseeAfter.HasSponsor)
						require.Empty(t, sponseeAfter.Sponsor)
						require.Equal(t, sponseeBefore.OwnerCount, sponseeAfter.OwnerCount)
						require.Equal(t, sponseeBefore.SponsoredOwnerCount, sponseeAfter.SponsoredOwnerCount)
						require.Equal(t, sponseeBefore.SponsoringOwnerCount, sponseeAfter.SponsoringOwnerCount)
						require.Equal(t, sponseeBefore.SponsoringAccountCount, sponseeAfter.SponsoringAccountCount)
						require.Equal(t, sponsorBefore.SponsoringOwnerCount, sponsorAfter.SponsoringOwnerCount)
						require.Equal(t, uint32(0), sponsorAfter.SponsoringAccountCount)
						require.NotEqual(t, sponseeBefore.PreviousTxnID, sponseeAfter.PreviousTxnID)
						require.NotEqual(t, sponsorBefore.PreviousTxnID, sponsorAfter.PreviousTxnID)
					} else {
						sourceNode := affectedNodeByKey(result.Metadata, keylet.Account(source.ID))
						target := sponsor
						if source == sponsor {
							target = sponsee
						}
						require.NotNil(t, sourceNode)
						require.Nil(t, affectedNodeByKey(result.Metadata, keylet.Account(target.ID)))
						require.True(t, sponseeAfter.HasSponsor)
						require.Equal(t, sponsor.Address, sponseeAfter.Sponsor)
						require.Equal(t, sponseeBefore.OwnerCount, sponseeAfter.OwnerCount)
						require.Equal(t, sponseeBefore.SponsoredOwnerCount, sponseeAfter.SponsoredOwnerCount)
						require.Equal(t, sponseeBefore.SponsoringOwnerCount, sponseeAfter.SponsoringOwnerCount)
						require.Equal(t, sponseeBefore.SponsoringAccountCount, sponseeAfter.SponsoringAccountCount)
						require.Equal(t, sponsorBefore.SponsoringAccountCount, sponsorAfter.SponsoringAccountCount)
					}

					require.Equal(t, relationshipBefore.RemainingOwnerCount, sponsorshipEntry(t, env, sponsor, sponsee).RemainingOwnerCount)
					require.Equal(t, sponsor.Address, mustObjectSponsor(t, env, sponsoredObject))
					require.Equal(t, objectBytesBefore, mustLedgerEntry(t, env, sponsoredObject))
				})
			}
		}
	}
}

func accountEndFixture(t *testing.T, cleanupEnabled bool) (*jtx.TestEnv, *jtx.Account, *jtx.Account, keylet.Keylet) {
	t.Helper()
	env, sponsee, destination, sponsor, _ := sponsorEnv(t)
	if cleanupEnabled {
		env.EnableFeature("fixCleanup3_4_0")
	} else {
		env.DisableFeature("fixCleanup3_4_0")
	}
	env.Close()

	check := checktx.NewCheckCreate(sponsee.Address, destination.Address, tx.NewXRPAmount(1))
	require.Equal(t, "tesSUCCESS", env.Submit(check).Code)
	env.Close()

	remaining := int32(1)
	require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, sponsee, 0, &remaining).Code)
	env.Close()

	sponsoredSequence := env.Seq(sponsee)
	sponsoredCheck := checktx.NewCheckCreate(sponsee.Address, destination.Address, tx.NewXRPAmount(1))
	sponsoredCheck.Sponsor = sponsor.Address
	reserve := tx.SpfSponsorReserve
	sponsoredCheck.SponsorFlags = &reserve
	require.Equal(t, "tesSUCCESS", env.Submit(sponsoredCheck).Code)
	env.Close()
	sponsoredCheckKey := keylet.Check(sponsee.ID, sponsoredSequence)
	require.Equal(t, sponsor.Address, mustObjectSponsor(t, env, sponsoredCheckKey))

	create := sponsortx.NewSponsorshipTransfer(sponsee.Address)
	create.SetFlags(sponsortx.SponsorshipTransferFlagCreate)
	create.Sponsor = sponsor.Address
	create.SponsorFlags = &reserve
	create.SponsorSignature = &tx.SponsorSignature{}
	require.Equal(t, "tesSUCCESS", env.Submit(create).Code)
	env.Close()

	return env, sponsee, sponsor, sponsoredCheckKey
}

func mustLedgerEntry(t *testing.T, env *jtx.TestEnv, key keylet.Keylet) []byte {
	t.Helper()
	data, err := env.LedgerEntry(key)
	require.NoError(t, err)
	return data
}

func mustObjectSponsor(t *testing.T, env *jtx.TestEnv, key keylet.Keylet) string {
	t.Helper()
	sponsor, present := objectSponsor(t, env, key)
	require.True(t, present)
	return sponsor
}

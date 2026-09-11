package sponsor_test

import (
	"encoding/hex"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	checktx "github.com/LeJamon/go-xrpl/internal/tx/check"
	sponsortx "github.com/LeJamon/go-xrpl/internal/tx/sponsor"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func replacementAccountTransfer(sponsee, sponsor *jtx.Account, sponsorFlags uint32) *sponsortx.SponsorshipTransfer {
	transaction := sponsortx.NewSponsorshipTransfer(sponsee.Address)
	transaction.SetFlags(sponsortx.SponsorshipTransferFlagReassign)
	transaction.Sponsor = sponsor.Address
	transaction.SponsorFlags = &sponsorFlags
	transaction.SponsorSignature = &tx.SponsorSignature{}
	return transaction
}

func replacementObjectTransfer(
	sponsee, sponsor *jtx.Account,
	object keylet.Keylet,
	flags uint32,
	sponsorFlags uint32,
	coSigned bool,
) *sponsortx.SponsorshipTransfer {
	transaction := sponsortx.NewSponsorshipTransfer(sponsee.Address)
	transaction.ObjectID = hex.EncodeToString(object.Key[:])
	transaction.SetFlags(flags)
	transaction.Sponsor = sponsor.Address
	transaction.SponsorFlags = &sponsorFlags
	if coSigned {
		transaction.SponsorSignature = &tx.SponsorSignature{}
	}
	return transaction
}

func replacementSponsoredCheck(t *testing.T) (
	*jtx.TestEnv,
	*jtx.Account,
	*jtx.Account,
	*jtx.Account,
	*jtx.Account,
	keylet.Keylet,
) {
	t.Helper()
	env, sponsee, destination, sponsor1, sponsor2 := sponsorEnv(t)
	sequence := env.Seq(sponsee)
	create := checktx.NewCheckCreate(sponsee.Address, destination.Address, tx.NewXRPAmount(jtx.XRP(1)))
	require.Equal(t, "tesSUCCESS", env.Submit(create).Code)
	checkKey := keylet.Check(sponsee.ID, sequence)

	remaining := int32(2)
	require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor1, sponsee, 0, &remaining).Code)
	require.Equal(t, "tesSUCCESS", transferObject(env, sponsee, checkKey, sponsor1, sponsortx.SponsorshipTransferFlagCreate).Code)
	return env, sponsee, destination, sponsor1, sponsor2, checkKey
}

func TestSponsorshipReplacementReserveAccountBoundary(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		delta   int64
		success bool
	}{
		{name: "one drop short", delta: -1},
		{name: "exact", success: true},
		{name: "one drop over", delta: 1, success: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			env, sponsee, _, sponsor1, sponsor2 := sponsorEnv(t)
			reserveFlags := tx.SpfSponsorReserve

			created := sponsortx.NewSponsorshipTransfer(sponsee.Address)
			created.SetFlags(sponsortx.SponsorshipTransferFlagCreate)
			created.Sponsor = sponsor1.Address
			created.SponsorFlags = &reserveFlags
			created.SponsorSignature = &tx.SponsorSignature{}
			require.Equal(t, "tesSUCCESS", env.Submit(created).Code)

			required := 2 * env.ReserveBase()
			setAccountBalance(t, env, sponsor2, uint64(int64(required)+testCase.delta))
			beforeSponsee := accountState(t, env, sponsee)
			beforeSponsor1 := accountState(t, env, sponsor1)
			beforeSponsor2 := accountState(t, env, sponsor2)
			beforeBalance := env.Balance(sponsee)
			beforeSponsorBalance := env.Balance(sponsor2)
			beforeSequence := env.Seq(sponsee)

			result := env.Submit(replacementAccountTransfer(sponsee, sponsor2, reserveFlags))
			if testCase.success {
				require.Equal(t, "tesSUCCESS", result.Code)
				require.True(t, result.Applied)
				require.Equal(t, env.BaseFee(), result.Fee)
				requireAffectedNodes(t, result,
					"ModifiedNode/AccountRoot",
					"ModifiedNode/AccountRoot",
					"ModifiedNode/AccountRoot",
				)
				require.Equal(t, sponsor2.Address, accountState(t, env, sponsee).Sponsor)
				require.True(t, accountState(t, env, sponsee).HasSponsor)
				require.Equal(t, uint32(0), accountState(t, env, sponsor1).SponsoringAccountCount)
				require.Equal(t, uint32(1), accountState(t, env, sponsor2).SponsoringAccountCount)
				require.Equal(t, beforeBalance-env.BaseFee(), env.Balance(sponsee))
				require.Equal(t, beforeSponsorBalance, env.Balance(sponsor2))
			} else {
				require.Equal(t, "tecINSUFFICIENT_RESERVE", result.Code)
				require.True(t, result.Applied)
				require.Equal(t, env.BaseFee(), result.Fee)
				requireAffectedNodes(t, result, "ModifiedNode/AccountRoot")
				require.Equal(t, beforeSponsee.Sponsor, accountState(t, env, sponsee).Sponsor)
				require.Equal(t, beforeSponsor1.SponsoringAccountCount, accountState(t, env, sponsor1).SponsoringAccountCount)
				require.Equal(t, beforeSponsor2.SponsoringAccountCount, accountState(t, env, sponsor2).SponsoringAccountCount)
				require.Equal(t, beforeBalance-env.BaseFee(), env.Balance(sponsee))
				require.Equal(t, beforeSponsorBalance, env.Balance(sponsor2))
			}
			require.Equal(t, beforeSequence+1, env.Seq(sponsee))
			require.Equal(t, beforeSponsee.SponsoredOwnerCount, accountState(t, env, sponsee).SponsoredOwnerCount)
		})
	}
}

func TestSponsorshipReplacementReserveObjectBoundary(t *testing.T) {
	for _, gate := range []struct {
		name    string
		enabled bool
	}{
		{name: "cleanup off"},
		{name: "cleanup on", enabled: true},
	} {
		t.Run(gate.name, func(t *testing.T) {
			for _, testCase := range []struct {
				name    string
				delta   int64
				success bool
			}{
				{name: "one drop short", delta: -1},
				{name: "exact", success: true},
				{name: "one drop over", delta: 1, success: true},
			} {
				t.Run(testCase.name, func(t *testing.T) {
					env, sponsee, _, sponsor1, sponsor2, checkKey := replacementSponsoredCheck(t)
					remaining := int32(1)
					require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor2, sponsee, 0, &remaining).Code)
					if gate.enabled {
						env.EnableFeature("fixCleanup3_4_0")
					} else {
						env.DisableFeature("fixCleanup3_4_0")
					}
					env.Close()

					require.Equal(t, uint32(1), env.OwnerCount(sponsor2))
					required := env.ReserveBase() + 2*env.ReserveIncrement()
					setAccountBalance(t, env, sponsor2, uint64(int64(required)+testCase.delta))
					beforeSponsee := accountState(t, env, sponsee)
					beforeSponsor1 := accountState(t, env, sponsor1)
					beforeSponsor2 := accountState(t, env, sponsor2)
					beforeBudget := sponsorshipEntry(t, env, sponsor2, sponsee).RemainingOwnerCount
					beforeBalance := env.Balance(sponsee)
					beforeSponsorBalance := env.Balance(sponsor2)
					beforeSequence := env.Seq(sponsee)

					reserveFlags := tx.SpfSponsorReserve
					result := env.Submit(replacementObjectTransfer(
						sponsee,
						sponsor2,
						checkKey,
						sponsortx.SponsorshipTransferFlagReassign,
						reserveFlags,
						false,
					))
					if testCase.success {
						require.Equal(t, "tesSUCCESS", result.Code)
						require.True(t, result.Applied)
						require.Equal(t, env.BaseFee(), result.Fee)
						requireAffectedNodes(t, result,
							"ModifiedNode/AccountRoot",
							"ModifiedNode/AccountRoot",
							"ModifiedNode/AccountRoot",
							"ModifiedNode/Check",
							"ModifiedNode/Sponsorship",
						)
						sponsor, present := objectSponsor(t, env, checkKey)
						require.True(t, present)
						require.Equal(t, sponsor2.Address, sponsor)
						require.Equal(t, uint32(1), accountState(t, env, sponsee).SponsoredOwnerCount)
						require.Equal(t, uint32(0), accountState(t, env, sponsor1).SponsoringOwnerCount)
						require.Equal(t, uint32(1), accountState(t, env, sponsor2).SponsoringOwnerCount)
						require.Zero(t, sponsorshipEntry(t, env, sponsor2, sponsee).RemainingOwnerCount)
						require.Equal(t, beforeSponsorBalance, env.Balance(sponsor2))
					} else {
						require.Equal(t, "tecINSUFFICIENT_RESERVE", result.Code)
						require.True(t, result.Applied)
						require.Equal(t, env.BaseFee(), result.Fee)
						requireAffectedNodes(t, result, "ModifiedNode/AccountRoot")
						sponsor, present := objectSponsor(t, env, checkKey)
						require.True(t, present)
						require.Equal(t, sponsor1.Address, sponsor)
						require.Equal(t, beforeSponsee.SponsoredOwnerCount, accountState(t, env, sponsee).SponsoredOwnerCount)
						require.Equal(t, beforeSponsor1.SponsoringOwnerCount, accountState(t, env, sponsor1).SponsoringOwnerCount)
						require.Equal(t, beforeSponsor2.SponsoringOwnerCount, accountState(t, env, sponsor2).SponsoringOwnerCount)
						require.Equal(t, beforeBudget, sponsorshipEntry(t, env, sponsor2, sponsee).RemainingOwnerCount)
						require.Equal(t, beforeSponsorBalance, env.Balance(sponsor2))
					}
					require.Equal(t, beforeBalance-env.BaseFee(), env.Balance(sponsee))
					require.Equal(t, beforeSequence+1, env.Seq(sponsee))
				})
			}
		})
	}
}

func TestSponsorshipReplacementReserveCosignedFeePayer(t *testing.T) {
	for _, target := range []struct {
		name      string
		object    bool
		cleanupOn bool
		required  func(*jtx.TestEnv) uint64
	}{
		{
			name:     "account cleanup off",
			required: func(env *jtx.TestEnv) uint64 { return 2 * env.ReserveBase() },
		},
		{
			name:      "account cleanup on",
			cleanupOn: true,
			required:  func(env *jtx.TestEnv) uint64 { return 2 * env.ReserveBase() },
		},
		{
			name:     "object cleanup off",
			object:   true,
			required: func(env *jtx.TestEnv) uint64 { return env.ReserveBase() + env.ReserveIncrement() },
		},
	} {
		t.Run(target.name, func(t *testing.T) {
			for _, testCase := range []struct {
				name    string
				delta   int64
				success bool
			}{
				{name: "one drop short", delta: -1},
				{name: "exact", success: true},
				{name: "one drop over", delta: 1, success: true},
			} {
				t.Run(testCase.name, func(t *testing.T) {
					env, sponsee, destination, sponsor1, sponsor2 := sponsorEnv(t)
					var checkKey keylet.Keylet
					if target.object {
						sequence := env.Seq(sponsee)
						require.Equal(t, "tesSUCCESS", env.Submit(checktx.NewCheckCreate(
							sponsee.Address, destination.Address, tx.NewXRPAmount(jtx.XRP(1)),
						)).Code)
						checkKey = keylet.Check(sponsee.ID, sequence)
						reserveFlags := tx.SpfSponsorReserve
						require.Equal(t, "tesSUCCESS", env.Submit(replacementObjectTransfer(
							sponsee,
							sponsor1,
							checkKey,
							sponsortx.SponsorshipTransferFlagCreate,
							reserveFlags,
							true,
						)).Code)
					} else {
						reserveFlags := tx.SpfSponsorReserve
						created := sponsortx.NewSponsorshipTransfer(sponsee.Address)
						created.SetFlags(sponsortx.SponsorshipTransferFlagCreate)
						created.Sponsor = sponsor1.Address
						created.SponsorFlags = &reserveFlags
						created.SponsorSignature = &tx.SponsorSignature{}
						require.Equal(t, "tesSUCCESS", env.Submit(created).Code)
					}

					if target.cleanupOn {
						env.EnableFeature("fixCleanup3_4_0")
					} else {
						env.DisableFeature("fixCleanup3_4_0")
					}
					env.Close()

					required := target.required(env)
					fee := env.BaseFee()
					setAccountBalance(t, env, sponsor2, uint64(int64(required+fee)+testCase.delta))
					beforeSponsee := accountState(t, env, sponsee)
					beforeSponsor1 := accountState(t, env, sponsor1)
					beforeSponsor2 := accountState(t, env, sponsor2)
					beforeSponseeBalance := env.Balance(sponsee)
					beforeSponsorBalance := env.Balance(sponsor2)
					beforeSequence := env.Seq(sponsee)
					require.Equal(t, uint32(0), env.OwnerCount(sponsor2))

					flags := tx.SpfSponsorReserve | tx.SpfSponsorFee
					var result jtx.TxResult
					if target.object {
						result = env.Submit(replacementObjectTransfer(
							sponsee,
							sponsor2,
							checkKey,
							sponsortx.SponsorshipTransferFlagReassign,
							flags,
							true,
						))
					} else {
						result = env.Submit(replacementAccountTransfer(sponsee, sponsor2, flags))
					}
					if testCase.success {
						require.Equal(t, "tesSUCCESS", result.Code)
						require.True(t, result.Applied)
						require.Equal(t, fee, result.Fee)
						if target.object {
							requireAffectedNodes(t, result,
								"ModifiedNode/AccountRoot",
								"ModifiedNode/AccountRoot",
								"ModifiedNode/AccountRoot",
								"ModifiedNode/Check",
							)
							sponsor, present := objectSponsor(t, env, checkKey)
							require.True(t, present)
							require.Equal(t, sponsor2.Address, sponsor)
						} else {
							requireAffectedNodes(t, result,
								"ModifiedNode/AccountRoot",
								"ModifiedNode/AccountRoot",
								"ModifiedNode/AccountRoot",
							)
							require.Equal(t, sponsor2.Address, accountState(t, env, sponsee).Sponsor)
						}
						if target.object {
							require.Equal(t, uint32(1), accountState(t, env, sponsee).SponsoredOwnerCount)
							require.Equal(t, uint32(0), accountState(t, env, sponsor1).SponsoringOwnerCount)
							require.Equal(t, uint32(1), accountState(t, env, sponsor2).SponsoringOwnerCount)
							require.Equal(t, beforeSponsor2.SponsoringAccountCount, accountState(t, env, sponsor2).SponsoringAccountCount)
						} else {
							require.Equal(t, beforeSponsee.SponsoredOwnerCount, accountState(t, env, sponsee).SponsoredOwnerCount)
							require.Equal(t, beforeSponsor1.SponsoringOwnerCount, accountState(t, env, sponsor1).SponsoringOwnerCount)
							require.Equal(t, beforeSponsor2.SponsoringOwnerCount, accountState(t, env, sponsor2).SponsoringOwnerCount)
							require.Equal(t, uint32(0), accountState(t, env, sponsor1).SponsoringAccountCount)
							require.Equal(t, uint32(1), accountState(t, env, sponsor2).SponsoringAccountCount)
						}
					} else {
						require.Equal(t, "tecINSUFFICIENT_RESERVE", result.Code)
						require.True(t, result.Applied)
						require.Equal(t, fee, result.Fee)
						if target.object {
							requireAffectedNodes(t, result,
								"ModifiedNode/AccountRoot",
								"ModifiedNode/AccountRoot",
							)
							require.NotNil(t, affectedNodeByKey(result.Metadata, keylet.Account(sponsee.ID)))
							require.NotNil(t, affectedNodeByKey(result.Metadata, keylet.Account(sponsor2.ID)))
							sponsor, present := objectSponsor(t, env, checkKey)
							require.True(t, present)
							require.Equal(t, sponsor1.Address, sponsor)
						} else {
							requireAffectedNodes(t, result,
								"ModifiedNode/AccountRoot",
								"ModifiedNode/AccountRoot",
							)
							require.NotNil(t, affectedNodeByKey(result.Metadata, keylet.Account(sponsee.ID)))
							require.NotNil(t, affectedNodeByKey(result.Metadata, keylet.Account(sponsor2.ID)))
							require.Equal(t, beforeSponsee.Sponsor, accountState(t, env, sponsee).Sponsor)
						}
						require.Equal(t, beforeSponsee.SponsoredOwnerCount, accountState(t, env, sponsee).SponsoredOwnerCount)
						require.Equal(t, beforeSponsor1.SponsoringOwnerCount, accountState(t, env, sponsor1).SponsoringOwnerCount)
						require.Equal(t, beforeSponsor2.SponsoringOwnerCount, accountState(t, env, sponsor2).SponsoringOwnerCount)
						require.Equal(t, beforeSponsor2.SponsoringAccountCount, accountState(t, env, sponsor2).SponsoringAccountCount)
						if !target.object {
							require.Equal(t, beforeSponsor1.SponsoringAccountCount, accountState(t, env, sponsor1).SponsoringAccountCount)
						}
					}
					require.Equal(t, beforeSponseeBalance, env.Balance(sponsee))
					require.Equal(t, beforeSponsorBalance-fee, env.Balance(sponsor2))
					require.Equal(t, beforeSequence+1, env.Seq(sponsee))
				})
			}
		})
	}
}

func TestSponsorshipReplacementReserveBudgetRollback(t *testing.T) {
	env, sponsee, _, sponsor1, sponsor2, checkKey := replacementSponsoredCheck(t)
	require.Equal(t, "tesSUCCESS", setFeeSponsorship(env, sponsor2, sponsee, 100, 0).Code)

	beforeSponsee := accountState(t, env, sponsee)
	beforeSponsor1 := accountState(t, env, sponsor1)
	beforeSponsor2 := accountState(t, env, sponsor2)
	beforeBudget := sponsorshipEntry(t, env, sponsor2, sponsee)
	beforeBalance := env.Balance(sponsee)
	beforeSequence := env.Seq(sponsee)
	reserveFlags := tx.SpfSponsorReserve
	result := env.Submit(replacementObjectTransfer(
		sponsee,
		sponsor2,
		checkKey,
		sponsortx.SponsorshipTransferFlagReassign,
		reserveFlags,
		false,
	))

	require.Equal(t, "tecINSUFFICIENT_RESERVE", result.Code)
	require.True(t, result.Applied)
	require.Equal(t, env.BaseFee(), result.Fee)
	requireAffectedNodes(t, result, "ModifiedNode/AccountRoot")
	sponsor, present := objectSponsor(t, env, checkKey)
	require.True(t, present)
	require.Equal(t, sponsor1.Address, sponsor)
	require.Equal(t, beforeSponsee.SponsoredOwnerCount, accountState(t, env, sponsee).SponsoredOwnerCount)
	require.Equal(t, beforeSponsor1.SponsoringOwnerCount, accountState(t, env, sponsor1).SponsoringOwnerCount)
	require.Equal(t, beforeSponsor2.SponsoringOwnerCount, accountState(t, env, sponsor2).SponsoringOwnerCount)
	afterBudget := sponsorshipEntry(t, env, sponsor2, sponsee)
	require.Equal(t, beforeBudget.RemainingOwnerCount, afterBudget.RemainingOwnerCount)
	require.Equal(t, beforeBudget.FeeAmount, afterBudget.FeeAmount)
	require.Equal(t, beforeBalance-env.BaseFee(), env.Balance(sponsee))
	require.Equal(t, beforeSequence+1, env.Seq(sponsee))
}

func TestSponsorshipReplacementReservePrecedence(t *testing.T) {
	t.Run("same sponsor precedes reserve", func(t *testing.T) {
		env, sponsee, _, sponsor1, _, checkKey := replacementSponsoredCheck(t)
		setAccountBalance(t, env, sponsor1, env.ReserveBase()-1)
		beforeSponsee := accountState(t, env, sponsee)
		beforeSponsor := accountState(t, env, sponsor1)
		beforeBudget := sponsorshipEntry(t, env, sponsor1, sponsee)
		beforeBalance := env.Balance(sponsee)
		beforeSequence := env.Seq(sponsee)
		reserveFlags := tx.SpfSponsorReserve

		result := env.Submit(replacementObjectTransfer(
			sponsee,
			sponsor1,
			checkKey,
			sponsortx.SponsorshipTransferFlagReassign,
			reserveFlags,
			false,
		))
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		require.True(t, result.Applied)
		require.Equal(t, env.BaseFee(), result.Fee)
		requireAffectedNodes(t, result, "ModifiedNode/AccountRoot")
		require.Equal(t, beforeSponsee.SponsoredOwnerCount, accountState(t, env, sponsee).SponsoredOwnerCount)
		require.Equal(t, beforeSponsor.SponsoringOwnerCount, accountState(t, env, sponsor1).SponsoringOwnerCount)
		require.Equal(t, beforeBudget.RemainingOwnerCount, sponsorshipEntry(t, env, sponsor1, sponsee).RemainingOwnerCount)
		require.Equal(t, beforeBalance-env.BaseFee(), env.Balance(sponsee))
		require.Equal(t, beforeSequence+1, env.Seq(sponsee))
	})

	t.Run("missing authorization precedes reserve", func(t *testing.T) {
		env, sponsee, _, sponsor1, sponsor2, checkKey := replacementSponsoredCheck(t)
		setAccountBalance(t, env, sponsor2, env.ReserveBase()-1)
		beforeSponsee := accountState(t, env, sponsee)
		beforeSponsor1 := accountState(t, env, sponsor1)
		beforeSponsor2 := accountState(t, env, sponsor2)
		beforeBalance := env.Balance(sponsee)
		beforeSequence := env.Seq(sponsee)
		reserveFlags := tx.SpfSponsorReserve

		result := env.Submit(replacementObjectTransfer(
			sponsee,
			sponsor2,
			checkKey,
			sponsortx.SponsorshipTransferFlagReassign,
			reserveFlags,
			false,
		))
		require.Equal(t, "terNO_PERMISSION", result.Code)
		require.False(t, result.Applied)
		require.Zero(t, result.Fee)
		require.Nil(t, result.Metadata)
		require.Equal(t, beforeSponsee.SponsoredOwnerCount, accountState(t, env, sponsee).SponsoredOwnerCount)
		require.Equal(t, beforeSponsor1.SponsoringOwnerCount, accountState(t, env, sponsor1).SponsoringOwnerCount)
		require.Equal(t, beforeSponsor2.SponsoringOwnerCount, accountState(t, env, sponsor2).SponsoringOwnerCount)
		require.Equal(t, beforeBalance, env.Balance(sponsee))
		require.Equal(t, beforeSequence, env.Seq(sponsee))
	})
}

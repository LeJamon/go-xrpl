package amm_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/payment/pathfinder"
)

func TestAMMPathfinderFixAMMOverflowOfferRetirement(t *testing.T) {
	amm.TestAMM(t, nil, 0, func(env *amm.AMMTestEnv, ammAccount *jtx.Account) {
		env.FundBob(30_000, 0)
		env.Trust(env.Bob, env.GW, "USD", 100_000)
		env.Close()

		require.Greater(t, env.AMMPoolXRP(ammAccount), uint64(0))
		require.Greater(t, env.AMMPoolIOU(ammAccount, env.GW, "USD"), float64(0))
		for _, account := range []*jtx.Account{env.GW, env.Alice, env.Carol, env.Bob} {
			require.Empty(t, env.AccountOffers(account), "%s must not contribute CLOB liquidity", account.Name)
		}

		const retired = "fixAMMOverflowOffer"
		// Pathfinder reads the ledger's rules independently of TestEnv's submission rules.
		closed := env.Ledger()
		closeTime := closed.CloseTime().Add(time.Duration(closed.CloseTimeResolution()) * time.Second)
		require.NoError(t, closed.Close(closeTime, 0))
		withRules := closed.Rules()
		require.True(t, withRules.Enabled(amendment.FeatureFixAMMOverflowOffer),
			"the enabled comparison provider must expose the retired amendment")

		withoutRulesBuilder := amendment.NewRulesBuilder()
		for _, id := range withRules.EnabledIDs() {
			withoutRulesBuilder.Enable(id)
		}
		withoutRules := withoutRulesBuilder.Disable(amendment.FeatureFixAMMOverflowOffer).Build()
		require.False(t, withoutRules.Enabled(amendment.FeatureFixAMMOverflowOffer),
			"the disabled comparison provider must omit the retired amendment")
		for _, feature := range amendment.AllFeatures() {
			if feature.ID == amendment.FeatureFixAMMOverflowOffer {
				continue
			}
			require.Equal(t, withRules.Enabled(feature.ID), withoutRules.Enabled(feature.ID),
				"%s changed while toggling %s", feature.Name, retired)
		}

		withRetiredLedger, err := ledger.NewOpenWithRules(closed, closeTime, withRules)
		require.NoError(t, err)
		withoutRetiredLedger, err := ledger.NewOpenWithRules(closed, closeTime, withoutRules)
		require.NoError(t, err)

		quote := func(view *ledger.Ledger, retiredEnabled bool) pathfinder.PathAlternative {
			require.Equal(t, retiredEnabled, view.Rules().Enabled(amendment.FeatureFixAMMOverflowOffer),
				"Pathfinder must receive the requested retired-amendment state")
			dstAmount := amm.IOUAmount(env.GW, "USD", 10)
			request := pathfinder.NewPathRequest(
				env.Alice.ID,
				env.Bob.ID,
				dstAmount,
				nil,
				[]payment.Issue{{Currency: "XRP"}},
				false,
			)
			request.SetSearchLevel(7)
			result := request.Execute(view)
			require.Len(t, result.Alternatives, 1, "AMM should provide one successful quote")
			alternative := result.Alternatives[0]
			require.True(t, alternative.SourceAmount.IsNative())
			require.Equal(t, int64(10_010_011), alternative.SourceAmount.Drops())
			require.False(t, alternative.DestinationAmount.IsNative())
			require.Equal(t, "USD", alternative.DestinationAmount.Currency)
			require.Equal(t, env.GW.Address, alternative.DestinationAmount.Issuer)
			require.Equal(t, dstAmount.Value(), alternative.DestinationAmount.Value())
			require.Equal(t, [][]payment.PathStep{{
				{Currency: "USD", Issuer: env.GW.Address, Type: 0x30},
			}}, alternative.PathsComputed, "the quote must use the AMM's USD book")
			return alternative
		}

		withRetired := quote(withRetiredLedger, true)
		withoutRetired := quote(withoutRetiredLedger, false)
		require.Equal(t, withRetired, withoutRetired,
			"retiring fixAMMOverflowOffer must not change an AMM-backed path quote")
	})
}

// A ledger without the retired fixAMMv1_1 must still quote with the post-fix
// AMM swap rounding, which differs from the legacy math in the last IOU digit.
func TestAMMPathfinderFixAMMv1_1Retirement(t *testing.T) {
	amm.TestAMM(t, nil, 0, func(env *amm.AMMTestEnv, ammAccount *jtx.Account) {
		env.FundBob(30_000, 0)
		env.Trust(env.Bob, env.GW, "USD", 100_000)
		env.Close()
		for _, account := range []*jtx.Account{env.GW, env.Alice, env.Carol, env.Bob} {
			require.Empty(t, env.AccountOffers(account), "%s must not contribute CLOB liquidity", account.Name)
		}

		closed := env.Ledger()
		closeTime := closed.CloseTime().Add(time.Duration(closed.CloseTimeResolution()) * time.Second)
		require.NoError(t, closed.Close(closeTime, 0))
		withRules := closed.Rules()
		require.True(t, withRules.Enabled(amendment.FeatureFixAMMv1_1))
		withoutRules := amendment.NewRulesBuilder()
		for _, id := range withRules.EnabledIDs() {
			withoutRules.Enable(id)
		}
		withoutRetired := withoutRules.Disable(amendment.FeatureFixAMMv1_1).Build()
		require.False(t, withoutRetired.Enabled(amendment.FeatureFixAMMv1_1))

		quote := func(rules *amendment.Rules) pathfinder.PathAlternative {
			view, err := ledger.NewOpenWithRules(closed, closeTime, rules)
			require.NoError(t, err)
			request := pathfinder.NewPathRequest(
				env.Alice.ID,
				env.Bob.ID,
				amm.XRPAmount(1),
				nil,
				[]payment.Issue{{Currency: "USD", Issuer: env.GW.ID}},
				false,
			)
			request.SetSearchLevel(7)
			result := request.Execute(view)
			require.Len(t, result.Alternatives, 1, "AMM should provide one successful quote")
			return result.Alternatives[0]
		}

		with, without := quote(withRules), quote(withoutRetired)
		require.Equal(t, with, without, "retiring fixAMMv1_1 must not change an AMM-backed path quote")
		require.False(t, without.SourceAmount.IsNative())
		require.Equal(t, "USD", without.SourceAmount.Currency)
		require.Equal(t, "1.00010001001", without.SourceAmount.Value())
	})
}

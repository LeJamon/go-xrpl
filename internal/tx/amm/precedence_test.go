package amm

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func fixAMMv1_3On() *amendment.Rules {
	return amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).Build()
}

func fixAMMv1_3Off() *amendment.Rules {
	return amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).DisableByName("fixAMMv1_3").Build()
}

func bidAsset() (tx.Asset, tx.Asset) {
	return tx.Asset{Currency: "USD", Issuer: "rIssuer"}, tx.Asset{Currency: "GBP", Issuer: "rIssuer"}
}

// TestAMMClawbackRequiredAmendments pins the feature-combination gate: AMMClawback
// gates on featureAMMClawback alone (rippled declares no checkExtraFeatures), so it
// must NOT additionally require featureAMM / fixUniversalNumber — otherwise a
// network with AMMClawback but not AMM would wrongly return temDISABLED instead of
// reaching preclaim (terNO_AMM).
// Reference: rippled transactions.macro ttAMM_CLAWBACK + AMMClawback.h.
func TestAMMClawbackRequiredAmendments(t *testing.T) {
	a1, a2 := bidAsset()
	cb := NewAMMClawback("rIssuer", "rHolder", a1, a2)
	require.Equal(t, [][32]byte{amendment.FeatureAMMClawback}, cb.RequiredAmendments())
}

func TestAMMRequiredAmendmentsExcludeRetiredUniversalNumber(t *testing.T) {
	tests := []struct {
		name        string
		transaction interface{ RequiredAmendments() [][32]byte }
	}{
		{"AMMCreate", &AMMCreate{}},
		{"AMMDeposit", &AMMDeposit{}},
		{"AMMWithdraw", &AMMWithdraw{}},
		{"AMMVote", &AMMVote{}},
		{"AMMBid", &AMMBid{}},
		{"AMMDelete", &AMMDelete{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, [][32]byte{amendment.FeatureAMM}, test.transaction.RequiredAmendments())
		})
	}
}

// TestAMMBidAuthAccountsPreflightRules pins that the fixAMMv1_3-gated
// duplicate/self AuthAccounts check lives in PreflightRules (preflight), so it
// runs before any preclaim state check. rippled evaluates it in preflight, gated
// on fixAMMv1_3, so it is a no-op when the amendment is disabled.
// Reference: rippled AMMBid.cpp preflight lines 81-95.
func TestAMMBidAuthAccountsPreflightRules(t *testing.T) {
	a1, a2 := bidAsset()

	mkBid := func(accounts ...string) *AMMBid {
		bid := NewAMMBid("rBidder", a1, a2)
		for _, addr := range accounts {
			bid.AuthAccounts = append(bid.AuthAccounts, AuthAccount{AuthAccount: AuthAccountData{Account: addr}})
		}
		return bid
	}

	t.Run("duplicate is temMALFORMED under fixAMMv1_3", func(t *testing.T) {
		require.ErrorContains(t, mkBid("rX", "rX").PreflightRules(fixAMMv1_3On()), "temMALFORMED")
	})
	t.Run("self is temMALFORMED under fixAMMv1_3", func(t *testing.T) {
		require.ErrorContains(t, mkBid("rBidder").PreflightRules(fixAMMv1_3On()), "temMALFORMED")
	})
	t.Run("no check without fixAMMv1_3", func(t *testing.T) {
		require.NoError(t, mkBid("rX", "rX").PreflightRules(fixAMMv1_3Off()))
	})
	t.Run("distinct accounts pass", func(t *testing.T) {
		require.NoError(t, mkBid("rX", "rY").PreflightRules(fixAMMv1_3On()))
	})
}

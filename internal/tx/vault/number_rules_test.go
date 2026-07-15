package vault

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func TestVaultNumberRulesSelectLegacyAndFixedLargeModes(t *testing.T) {
	legacyRules := amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault})
	fixedRules := amendment.NewRules([][32]byte{
		amendment.FeatureSingleAssetVault,
		amendment.FeatureFixCleanup3_2_0,
	})

	require.Equal(t, state.MantissaScaleLargeLegacy, vaultNumberScale(legacyRules))
	require.Equal(t, state.MantissaScaleLarge, vaultNumberScale(fixedRules))
}

func TestAssociateVaultAssetRoundsAndRemovesDefaultFields(t *testing.T) {
	rules := amendment.NewRules([][32]byte{
		amendment.FeatureSingleAssetVault,
		amendment.FeatureFixCleanup3_2_0,
	})
	vd := &vaultData{
		Asset:           tx.Asset{Currency: "XRP"},
		AssetsTotal:     "2.5",
		AssetsAvailable: "0.4",
		AssetsMaximum:   "8.5",
		LossUnrealized:  "0",
	}

	require.NoError(t, associateVaultAsset(vd, rules))
	require.NotEmpty(t, vd.AssetsTotal)
	require.Empty(t, vd.AssetsAvailable)
	require.NotEmpty(t, vd.AssetsMaximum)
	require.Empty(t, vd.LossUnrealized)

	total, err := vaultNumberForRules(vd.AssetsTotal, rules)
	require.NoError(t, err)
	require.True(t, total.Equal(state.NewXRPLNumberScaled(2, 0, state.MantissaScaleLarge, state.RoundToNearest)))
	maximum, err := vaultNumberForRules(vd.AssetsMaximum, rules)
	require.NoError(t, err)
	require.True(t, maximum.Equal(state.NewXRPLNumberScaled(8, 0, state.MantissaScaleLarge, state.RoundToNearest)))
}

package vault

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// vaultNumber parses a NUMBER field string ("" meaning zero) using the legacy
// small range. Rules-aware Vault paths use vaultNumberForRules.
func vaultNumber(s string) (state.XRPLNumber, error) {
	return vaultNumberScaled(s, state.MantissaScaleSmall)
}

func vaultNumberForRules(s string, rules *amendment.Rules) (state.XRPLNumber, error) {
	return vaultNumberScaled(s, vaultNumberScale(rules))
}

func vaultNumberScaled(s string, scale state.MantissaScale) (state.XRPLNumber, error) {
	if s == "" || s == "0" {
		return state.NewXRPLNumberScaled(0, 0, scale, state.RoundToNearest), nil
	}
	number, err := state.ParseXRPLNumber(s, scale, state.RoundToNearest)
	if err != nil {
		return state.NewXRPLNumberScaled(0, 0, scale, state.RoundToNearest), fmt.Errorf("parse number %q: %w", s, err)
	}
	return number, nil
}

func vaultNumberScale(rules *amendment.Rules) state.MantissaScale {
	return tx.NumberContextForRules(rules).Scale()
}

// numberToString renders an XRPLNumber into the vault NUMBER-field convention:
// "" for zero, otherwise a scientific string the codec re-normalizes to the
// identical value.
func numberToString(n state.XRPLNumber) string {
	if n.IsZero() {
		return ""
	}
	return fmt.Sprintf("%de%d", n.Mantissa(), n.Exponent())
}

// amountToNumber converts an asset amount into an XRPLNumber. XRP is measured in
// drops and MPT in its integer units; an IOU carries a decimal value.
func amountToNumber(a state.Amount) (state.XRPLNumber, error) {
	if a.IsNative() {
		return state.NewXRPLNumber(a.Drops(), 0), nil
	}
	if a.IsMPT() {
		return vaultNumber(a.Value())
	}
	return vaultNumber(a.Value())
}

func amountToNumberForRules(a state.Amount, rules *amendment.Rules) (state.XRPLNumber, error) {
	scale := vaultNumberScale(rules)
	if a.IsNative() {
		return state.NewXRPLNumberScaled(a.Drops(), 0, scale, state.RoundToNearest), nil
	}
	return vaultNumberScaled(a.Value(), scale)
}

func associateVaultAsset(vd *vaultData, rules *amendment.Rules) error {
	integral := vd.AssetIsMPT || isNativeAsset(vd.Asset)
	for _, field := range []*string{
		&vd.AssetsTotal,
		&vd.AssetsAvailable,
		&vd.AssetsMaximum,
		&vd.LossUnrealized,
	} {
		value, err := vaultNumberForRules(*field, rules)
		if err != nil {
			return err
		}
		rounded, remove := state.AssociateAssetField(value, integral, true)
		if remove {
			*field = ""
		} else {
			*field = numberToString(rounded)
		}
	}
	return nil
}

// pow10 returns 10^scale as an XRPLNumber.
func pow10(scale uint8) state.XRPLNumber {
	return state.NewXRPLNumber(1, int(scale))
}

// roundToVaultScale rounds a deposit amount down to the vault's post-deposit
// AssetsTotal scale (fixCleanup3_2_0), so a sub-ULP tail can't be absorbed by
// one accounting rail and not the other. Integral assets are whole units, so it
// is a no-op there.
func roundToVaultScale(amount, assetsTotal state.XRPLNumber, integral bool) state.XRPLNumber {
	if integral {
		return amount
	}
	postScale := assetsTotal.Add(amount).AssetExponent(false, state.RoundToNearest)
	return amount.RoundToAssetScale(false, postScale, state.RoundDownward)
}

// assetsToSharesDeposit converts a deposit of assets into freshly minted shares.
// The share count is truncated toward zero. Reference: rippled View.cpp.
func assetsToSharesDeposit(assetsTotal, shareTotal, assets state.XRPLNumber, scale uint8) state.XRPLNumber {
	if assetsTotal.IsZero() {
		return assets.Mul(pow10(scale)).Truncate()
	}
	return shareTotal.Mul(assets).Div(assetsTotal).Truncate()
}

// sharesToAssetsDeposit converts a share count back to assets on the deposit
// path (used to verify the exchange does not exceed the offered amount).
func sharesToAssetsDeposit(
	assetsTotal, shareTotal, shares state.XRPLNumber,
	scale uint8,
	integral bool,
) state.XRPLNumber {
	if assetsTotal.IsZero() {
		return shares.Div(pow10(scale)).RoundToAsset(integral)
	}
	return assetsTotal.Mul(shares).Div(shareTotal).RoundToAsset(integral)
}

// assetsToSharesWithdraw converts a withdrawal of assets into the shares that
// must be redeemed. The effective asset total excludes unrealized losses.
func assetsToSharesWithdraw(assetsTotal, lossUnrealized, shareTotal, assets state.XRPLNumber, truncate bool) state.XRPLNumber {
	effective := assetsTotal.Sub(lossUnrealized)
	if effective.IsZero() {
		return effective
	}
	result := shareTotal.Mul(assets).Div(effective)
	if truncate {
		return result.Truncate()
	}
	return result.RoundToAsset(true)
}

// sharesToAssetsWithdraw converts a share count into the assets it redeems.
func sharesToAssetsWithdraw(
	assetsTotal, lossUnrealized, shareTotal, shares state.XRPLNumber,
	integral bool,
) state.XRPLNumber {
	effective := assetsTotal.Sub(lossUnrealized)
	if effective.IsZero() {
		return effective
	}
	return effective.Mul(shares).Div(shareTotal).RoundToAsset(integral)
}

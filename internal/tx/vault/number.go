package vault

import (
	"encoding/binary"
	"fmt"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// This file is the local Number seam for the vault package. The vault ledger
// NUMBER fields are carried as their canonical decimal/scientific string; these
// helpers bridge that representation to state.XRPLNumber for the exact rippled
// share-conversion arithmetic. A shared state.Number helper (built in a sibling
// PR) can later replace the string carriage without touching call sites.

// vaultNumber parses a NUMBER field string ("" meaning zero) into an XRPLNumber.
// It round-trips through the binary codec so the parse is byte-identical to how
// the field decodes off the ledger.
func vaultNumber(s string) (state.XRPLNumber, error) {
	if s == "" || s == "0" {
		return state.NewXRPLNumber(0, 0), nil
	}
	num := &types.Number{}
	b, err := num.FromJSON(s)
	if err != nil {
		return state.NewXRPLNumber(0, 0), fmt.Errorf("parse number %q: %w", s, err)
	}
	mantissa := int64(binary.BigEndian.Uint64(b[:8]))
	exp := int32(binary.BigEndian.Uint32(b[8:12]))
	return state.NewXRPLNumber(mantissa, int(exp)), nil
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

// pow10 returns 10^scale as an XRPLNumber.
func pow10(scale uint8) state.XRPLNumber {
	return state.NewXRPLNumber(1, int(scale))
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
func sharesToAssetsDeposit(assetsTotal, shareTotal, shares state.XRPLNumber, scale uint8) state.XRPLNumber {
	if assetsTotal.IsZero() {
		return shares.Div(pow10(scale))
	}
	return assetsTotal.Mul(shares).Div(shareTotal)
}

// assetsToSharesWithdraw converts a withdrawal of assets into the shares that
// must be redeemed. The effective asset total excludes unrealized losses.
func assetsToSharesWithdraw(assetsTotal, lossUnrealized, shareTotal, assets state.XRPLNumber, truncate bool) state.XRPLNumber {
	effective := assetsTotal.Sub(lossUnrealized)
	if effective.IsZero() {
		return state.NewXRPLNumber(0, 0)
	}
	result := shareTotal.Mul(assets).Div(effective)
	if truncate {
		result = result.Truncate()
	}
	return result
}

// sharesToAssetsWithdraw converts a share count into the assets it redeems.
func sharesToAssetsWithdraw(assetsTotal, lossUnrealized, shareTotal, shares state.XRPLNumber) state.XRPLNumber {
	effective := assetsTotal.Sub(lossUnrealized)
	if effective.IsZero() {
		return state.NewXRPLNumber(0, 0)
	}
	return effective.Mul(shares).Div(shareTotal)
}

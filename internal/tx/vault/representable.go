package vault

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// IsRepresentable reports whether n converts to an amount of asset without
// leaving the asset's range: XRP within the native maximum, an MPT within
// int64, an IOU within the maximum exponent.
func IsRepresentable(asset tx.Asset, n state.XRPLNumber) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	switch {
	case asset.IsNative():
		drops := n.ToInt64WithMode(state.RoundToNearest)
		return drops <= int64(state.MaxNativeDrops) && drops >= -int64(state.MaxNativeDrops)
	case asset.IsMPT():
		n.ToInt64WithMode(state.RoundToNearest)
	default:
		n.ToIOUAmountValueRounded(state.RoundToNearest)
	}
	return true
}

package amm

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func decodeMPTAsset(asset tx.Asset) ([24]byte, ter.Result) {
	id, err := mptutil.DecodeID(asset.MPTIssuanceID)
	if err != nil {
		return id, ter.TecOBJECT_NOT_FOUND
	}
	return id, ter.TesSUCCESS
}

func assetIssuerID(asset tx.Asset) ([20]byte, ter.Result) {
	if asset.IsMPT() {
		id, result := decodeMPTAsset(asset)
		if result != ter.TesSUCCESS {
			return [20]byte{}, result
		}
		return mptutil.Issuer(id), ter.TesSUCCESS
	}
	issuer, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return [20]byte{}, ter.TefINTERNAL
	}
	return issuer, ter.TesSUCCESS
}

func requireAssetAuth(view tx.LedgerView, asset tx.Asset, account [20]byte, strong bool, parentCloseTime uint32) ter.Result {
	if !asset.IsMPT() {
		return tx.RequireAuth(view, asset, account)
	}
	id, result := decodeMPTAsset(asset)
	if result != ter.TesSUCCESS {
		return result
	}
	return mptutil.RequireAuthAt(view, id, account, strong, parentCloseTime)
}

func assetFrozen(view tx.LedgerView, account [20]byte, asset tx.Asset) bool {
	if !asset.IsMPT() {
		return tx.IsFrozen(view, account, asset)
	}
	id, result := decodeMPTAsset(asset)
	return result == ter.TesSUCCESS && mptutil.IsFrozen(view, id, account)
}

func assetIndividuallyFrozen(view tx.LedgerView, account [20]byte, asset tx.Asset) bool {
	if !asset.IsMPT() {
		return tx.IsIndividualFrozen(view, account, asset)
	}
	id, result := decodeMPTAsset(asset)
	return result == ter.TesSUCCESS && mptutil.IsIndividualFrozen(view, id, account)
}

func frozenAssetResult(asset tx.Asset) ter.Result {
	if asset.IsMPT() {
		return ter.TecLOCKED
	}
	return ter.TecFROZEN
}

func canMPTTradeAndTransfer(view tx.LedgerView, asset tx.Asset, from, to [20]byte) ter.Result {
	if !asset.IsMPT() {
		return ter.TesSUCCESS
	}
	id, result := decodeMPTAsset(asset)
	if result != ter.TesSUCCESS {
		return result
	}
	if result := mptutil.CanTrade(view, id); result != ter.TesSUCCESS {
		return result
	}
	return mptutil.CanTransfer(view, id, from, to)
}

func mptFunds(view tx.LedgerView, account [20]byte, amount tx.Amount, zeroIfFrozen bool) (int64, ter.Result) {
	id, result := decodeMPTAsset(amountAsset(amount))
	if result != ter.TesSUCCESS {
		return 0, result
	}
	return mptutil.Funds(view, id, account, zeroIfFrozen)
}

func sendMPT(view tx.LedgerView, from, to [20]byte, amount tx.Amount, waiveFee bool) ter.Result {
	id, result := decodeMPTAsset(amountAsset(amount))
	if result != ter.TesSUCCESS {
		return result
	}
	value, ok := amount.MPTRaw()
	if !ok || value < 0 {
		return ter.TefINTERNAL
	}
	_, result = mptutil.Send(view, id, from, to, value, waiveFee, false)
	return result
}

func ensureAMMMPTHolding(view tx.LedgerView, asset tx.Asset, ammAccount [20]byte) ter.Result {
	id, result := decodeMPTAsset(asset)
	if result != ter.TesSUCCESS {
		return result
	}
	return mptutil.EnsureHolding(
		view,
		id,
		ammAccount,
		entry.LsfMPTAMM|entry.LsfMPTAuthorized,
		false,
	)
}

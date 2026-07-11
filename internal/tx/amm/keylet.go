package amm

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/keylet"
)

// ComputeAMMAccountAddress returns the AMM pseudo-account address for the given asset pair.
// Uses the first 20 bytes of the AMM keylet hash as the account ID.
// Exported for use in test helpers.
func ComputeAMMAccountAddress(asset1, asset2 tx.Asset) string {
	ammKey := computeAMMKeylet(asset1, asset2)
	var accountID [20]byte
	copy(accountID[:], ammKey.Key[:20])
	addr, _ := encodeAccountID(accountID)
	return addr
}

// ComputeAMMKeylet computes the AMM keylet from the asset pair.
// Exported for use in test helpers.
func ComputeAMMKeylet(asset1, asset2 tx.Asset) keylet.Keylet {
	return computeAMMKeylet(asset1, asset2)
}

// PseudoAccountAddress derives the AMM pseudo-account ID for the given keylet key.
// Exported for use in test helpers (e.g., PseudoAccount collision tests).
func PseudoAccountAddress(view tx.LedgerView, parentHash [32]byte, key [32]byte) [20]byte {
	return tx.PseudoAccountAddress(view, parentHash, key)
}

// computeAMMKeylet computes the AMM keylet from the asset pair.
func computeAMMKeylet(asset1, asset2 tx.Asset) keylet.Keylet {
	return keylet.AMMAsset(assetBookSide(asset1), assetBookSide(asset2))
}

func assetBookSide(asset tx.Asset) keylet.BookSide {
	if asset.IsMPT() {
		id, _ := mptutil.DecodeID(asset.MPTIssuanceID)
		return keylet.MPTSide(id)
	}
	return keylet.IssueSide(keylet.CurrencyBytes(asset.Currency), getIssuerBytes(asset.Issuer))
}

// getIssuerBytes converts an issuer address string to a 20-byte account ID.
func getIssuerBytes(issuer string) [20]byte {
	if issuer == "" {
		return [20]byte{}
	}
	id, _ := state.DecodeAccountID(issuer)
	return id
}

package nftoken

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// ---------------------------------------------------------------------------
// Serialization helpers
// ---------------------------------------------------------------------------

// SerializeNFTokenPage serializes an NFToken page ledger entry.
// Exported so that LedgerStateFix can use it to repair pages. The serialization
// logic lives in internal/ledger/state alongside ParseNFTokenPage.
func SerializeNFTokenPage(page *state.NFTokenPageData) ([]byte, error) {
	return state.SerializeNFTokenPage(page)
}

// serializeNFTokenPage serializes an NFToken page ledger entry.
func serializeNFTokenPage(page *state.NFTokenPageData) ([]byte, error) {
	return state.SerializeNFTokenPage(page)
}

// amountToCodecFormat converts a tx.Amount to the format expected by binarycodec.Encode.
// XRP → string of drops ("1000000"), IOU → map[string]any{"value":"10","currency":"USD","issuer":"rAddr"}
func amountToCodecFormat(amt tx.Amount) any {
	if amt.IsNative() {
		return fmt.Sprintf("%d", amt.Drops())
	}
	return map[string]any{
		"value":    amt.IOU().String(),
		"currency": amt.Currency,
		"issuer":   amt.Issuer,
	}
}

// serializeNFTokenOfferRaw serializes an NFToken offer ledger entry from
// primitive parameters. amount can be a string (XRP drops) or map[string]any
// (IOU). The serialization logic lives in internal/ledger/state alongside
// ParseNFTokenOffer.
func serializeNFTokenOfferRaw(
	ownerID [20]byte, tokenID [32]byte,
	amount any, flags uint32,
	ownerNode, offerNode uint64,
	destination string, expiration *uint32,
) ([]byte, error) {
	return state.SerializeNFTokenOffer(ownerID, tokenID, amount, flags, ownerNode, offerNode, destination, expiration)
}

// serializeNFTokenOffer serializes an NFToken offer from an NFTokenCreateOffer transaction.
func serializeNFTokenOffer(nftTx *NFTokenCreateOffer, ownerID [20]byte, tokenID [32]byte, sequence uint32, ownerNode uint64, offerNode uint64) ([]byte, error) {
	// The NFTokenOffer ledger object only carries lsfSellNFToken; the rest of the
	// transaction's flags (notably tfFullyCanonicalSig) must not leak into its
	// sfFlags. rippled sets (*offer)[sfFlags] = isSell ? lsfSellNFToken : 0.
	return serializeNFTokenOfferRaw(
		ownerID, tokenID,
		amountToCodecFormat(nftTx.Amount), nftTx.GetFlags()&NFTokenCreateOfferFlagSellNFToken,
		ownerNode, offerNode,
		nftTx.Destination, nftTx.Expiration,
	)
}

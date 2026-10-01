package nftoken

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/ledger/entry"
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

func serializeNFTokenOfferRaw(
	ownerID [20]byte, tokenID [32]byte,
	amount entry.AmountValue, flags uint32,
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
		nftTx.Amount.LedgerValue(), nftTx.GetFlags()&NFTokenCreateOfferFlagSellNFToken,
		ownerNode, offerNode,
		nftTx.Destination, nftTx.Expiration,
	)
}

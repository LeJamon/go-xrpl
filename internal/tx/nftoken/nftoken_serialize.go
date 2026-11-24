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

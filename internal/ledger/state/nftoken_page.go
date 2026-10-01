package state

import (
	"fmt"
	"strings"

	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

// SerializeNFTokenPage serializes an NFToken page ledger entry.
func SerializeNFTokenPage(page *NFTokenPageData) ([]byte, error) {
	entry := &ledgerfields.NFTokenPage{}
	entry.SetFlagsValue(0)

	var emptyHash [32]byte
	if page.PreviousPageMin != emptyHash {
		entry.SetPreviousPageMinValue(page.PreviousPageMin)
	}

	if page.NextPageMin != emptyHash {
		entry.SetNextPageMinValue(page.NextPageMin)
	}

	if page.PreviousTxnID != emptyHash {
		entry.SetPreviousTxnIDValue(page.PreviousTxnID)
		entry.SetPreviousTxnLgrSeqValue(page.PreviousTxnLgrSeq)
	}

	nfTokens := make([]ledgerfields.NFTokenValue, len(page.NFTokens))
	for i, token := range page.NFTokens {
		var nfToken ledgerfields.NFTokenValue
		nfToken.SetNFTokenID(token.NFTokenID)
		if token.URI != "" {
			if err := nfToken.SetURIHex(token.URI); err != nil {
				return nil, err
			}
		}
		nfTokens[i] = nfToken
	}
	if err := entry.SetNFTokensValue(nfTokens); err != nil {
		return nil, err
	}

	return entry.Encode()
}

// SerializeNFTokenOffer serializes an NFTokenOffer ledger entry from its
// primitive fields. rippled's
// NFTokenOffer object uses sfOwner (not sfAccount) and stores only lsfSellNFToken
// in sfFlags; emitting anything else forks account_hash.
func SerializeNFTokenOffer(
	ownerID [20]byte, tokenID [32]byte,
	amount ledgerfields.AmountValue, flags uint32,
	ownerNode, offerNode uint64,
	destination string, expiration *uint32,
) ([]byte, error) {
	entry := &ledgerfields.NFTokenOffer{}
	if err := entry.SetOwnerValue(ownerID); err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}
	if err := entry.SetAmountValue(amount); err != nil {
		return nil, fmt.Errorf("failed to encode NFTokenOffer amount: %w", err)
	}
	entry.SetNFTokenIDValue(tokenID)
	entry.SetOwnerNodeValue(ownerNode)
	entry.SetNFTokenOfferNodeValue(offerNode)
	entry.SetFlagsValue(flags)

	if expiration != nil {
		entry.SetExpirationValue(*expiration)
	}

	if destination != "" {
		entry.SetDestination(destination)
	}

	return entry.Encode()
}

// NFTokenPageData represents an NFToken page ledger entry
type NFTokenPageData struct {
	PreviousPageMin [32]byte
	NextPageMin     [32]byte
	NFTokens        []NFTokenData
	// Round-trips so a no-op modify re-serializes byte-identically and the apply
	// layer's unchanged-entry guard prunes it (ApplyStateTable.cpp:154-157).
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
}

// NFTokenData represents an individual NFToken within a page
type NFTokenData struct {
	NFTokenID [32]byte
	URI       string
}

// NFTokenOfferData represents an NFToken offer ledger entry
type NFTokenOfferData struct {
	Owner     [20]byte
	NFTokenID [32]byte
	Amount    uint64
	// Negative records the sign of the offer Amount, which Amount (a uint64)
	// cannot represent. Pre-fixNFTokenNegOffer offers may carry a negative
	// amount; consumers use this instead of re-scanning the raw SLE bytes.
	Negative         bool
	AmountIOU        *NFTIOUAmount // For IOU amounts
	Flags            uint32
	Destination      [20]byte
	Expiration       uint32
	HasDestination   bool
	OwnerNode        uint64 // Page in owner directory where this offer is listed
	NFTokenOfferNode uint64 // Page in NFTBuys/NFTSells directory where this offer is listed
}

// NFTIOUAmount represents an IOU amount for NFToken offers
// This is a simplified version for NFToken offer storage
type NFTIOUAmount struct {
	Currency string
	Issuer   [20]byte
	Value    string
}

// ParseNFTokenPage parses an NFToken page from binary data
func ParseNFTokenPage(data []byte) (*NFTokenPageData, error) {
	entry := &ledgerfields.NFTokenPage{}
	if err := entry.Decode(data); err != nil {
		return nil, err
	}
	previousPageMin, err := entry.GetPreviousPageMin()
	if err != nil {
		return nil, err
	}
	nextPageMin, err := entry.GetNextPageMin()
	if err != nil {
		return nil, err
	}
	previousTxnID, err := entry.GetPreviousTxnID()
	if err != nil {
		return nil, err
	}
	previousTxnLgrSeq, err := entry.GetPreviousTxnLgrSeq()
	if err != nil {
		return nil, err
	}
	tokens, err := entry.GetNFTokens()
	if err != nil {
		return nil, err
	}
	page := &NFTokenPageData{
		PreviousPageMin:   previousPageMin,
		NextPageMin:       nextPageMin,
		NFTokens:          make([]NFTokenData, 0, len(tokens)),
		PreviousTxnID:     previousTxnID,
		PreviousTxnLgrSeq: previousTxnLgrSeq,
	}
	for _, value := range tokens {
		tokenID, err := value.GetNFTokenID()
		if err != nil {
			return nil, err
		}
		uri, err := value.GetURIHex()
		if err != nil {
			return nil, err
		}
		page.NFTokens = append(page.NFTokens, NFTokenData{
			NFTokenID: tokenID,
			URI:       strings.ToLower(uri),
		})
	}
	return page, nil
}

// ParseNFTokenOffer parses a canonical NFToken offer from binary data.
func ParseNFTokenOffer(data []byte) (*NFTokenOfferData, error) {
	entry := &ledgerfields.NFTokenOffer{}
	if err := entry.Decode(data); err != nil {
		return nil, err
	}
	return parseNFTokenOffer(entry)
}

// ParseNFTokenOfferLegacy parses a pre-canonicalization go-xrpl offer blob.
func ParseNFTokenOfferLegacy(data []byte) (*NFTokenOfferData, error) {
	entry := &ledgerfields.NFTokenOffer{}
	if err := ledgerfields.DecodeLegacy(entry, data); err != nil {
		return nil, err
	}
	return parseNFTokenOffer(entry)
}

func parseNFTokenOffer(entry *ledgerfields.NFTokenOffer) (*NFTokenOfferData, error) {
	flags, err := entry.GetFlags()
	if err != nil {
		return nil, err
	}
	expiration, err := entry.GetExpiration()
	if err != nil {
		return nil, err
	}
	owner, err := entry.GetOwner()
	if err != nil {
		return nil, err
	}
	destination, err := entry.GetDestination()
	if err != nil {
		return nil, err
	}
	tokenID, err := entry.GetNFTokenID()
	if err != nil {
		return nil, err
	}
	ownerNode, err := entry.GetOwnerNode()
	if err != nil {
		return nil, err
	}
	offerNode, err := entry.GetNFTokenOfferNode()
	if err != nil {
		return nil, err
	}
	amountValue, err := entry.GetAmount()
	if err != nil {
		return nil, err
	}
	offer := &NFTokenOfferData{
		Owner:            owner,
		NFTokenID:        tokenID,
		Flags:            flags,
		Expiration:       expiration,
		Destination:      destination,
		HasDestination:   entry.HasDestination(),
		OwnerNode:        ownerNode,
		NFTokenOfferNode: offerNode,
	}
	amount, err := decodeLedgerAmount("NFTokenOffer.Amount", amountValue)
	if err != nil {
		return nil, err
	}
	switch {
	case amount.IsNative():
		drops := amount.Drops()
		if drops < 0 {
			drops = -drops
		}
		offer.Amount = uint64(drops)
		offer.Negative = amount.IsNegative()
	case !amount.IsMPT():
		offer.Negative = amount.IsNegative()
		issuer, err := DecodeAccountID(amount.Issuer)
		if err != nil {
			return nil, err
		}
		offer.AmountIOU = &NFTIOUAmount{
			Currency: amount.Currency,
			Issuer:   issuer,
			Value:    amount.IOU().String(),
		}
	}

	return offer, nil
}

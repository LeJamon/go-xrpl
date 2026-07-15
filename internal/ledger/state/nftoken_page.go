package state

import (
	"encoding/hex"
	"fmt"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
)

// SerializeNFTokenPage serializes an NFToken page ledger entry.
func SerializeNFTokenPage(page *NFTokenPageData) ([]byte, error) {
	entry := &ledgerfields.NFTokenPage{}
	entry.SetFlags(0)

	var emptyHash [32]byte
	if page.PreviousPageMin != emptyHash {
		entry.SetPreviousPageMin(strings.ToUpper(hex.EncodeToString(page.PreviousPageMin[:])))
	}

	if page.NextPageMin != emptyHash {
		entry.SetNextPageMin(strings.ToUpper(hex.EncodeToString(page.NextPageMin[:])))
	}

	if page.PreviousTxnID != emptyHash {
		entry.SetPreviousTxnID(strings.ToUpper(hex.EncodeToString(page.PreviousTxnID[:])))
		entry.SetPreviousTxnLgrSeq(page.PreviousTxnLgrSeq)
	}

	nfTokens := make([]any, len(page.NFTokens))
	for i, token := range page.NFTokens {
		nfTokenFields := map[string]any{
			"NFTokenID": strings.ToUpper(hex.EncodeToString(token.NFTokenID[:])),
		}
		if token.URI != "" {
			nfTokenFields["URI"] = token.URI
		}
		nfTokens[i] = map[string]any{"NFToken": nfTokenFields}
	}
	entry.SetNFTokens(nfTokens)

	return entry.Encode()
}

// SerializeNFTokenOffer serializes an NFTokenOffer ledger entry from its
// primitive fields. amount is a string of XRP drops or an IOU map. rippled's
// NFTokenOffer object uses sfOwner (not sfAccount) and stores only lsfSellNFToken
// in sfFlags; emitting anything else forks account_hash.
func SerializeNFTokenOffer(
	ownerID [20]byte, tokenID [32]byte,
	amount any, flags uint32,
	ownerNode, offerNode uint64,
	destination string, expiration *uint32,
) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	entry := &ledgerfields.NFTokenOffer{}
	entry.SetOwner(ownerAddress)
	entry.SetAmount(amount)
	entry.SetNFTokenID(strings.ToUpper(hex.EncodeToString(tokenID[:])))
	entry.SetOwnerNode(fmt.Sprintf("%x", ownerNode))
	entry.SetNFTokenOfferNode(fmt.Sprintf("%x", offerNode))
	entry.SetFlags(flags)

	if expiration != nil {
		entry.SetExpiration(*expiration)
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
	fields := entry.ToMap()
	page := &NFTokenPageData{
		NFTokens:          make([]NFTokenData, 0, len(entry.NFTokens)),
		PreviousTxnLgrSeq: entry.PreviousTxnLgrSeq,
	}

	if fields["PreviousPageMin"] != nil {
		if err := decodeLedgerHex("NFTokenPage.PreviousPageMin", entry.PreviousPageMin, page.PreviousPageMin[:]); err != nil {
			return nil, err
		}
	}
	if fields["NextPageMin"] != nil {
		if err := decodeLedgerHex("NFTokenPage.NextPageMin", entry.NextPageMin, page.NextPageMin[:]); err != nil {
			return nil, err
		}
	}
	if fields["PreviousTxnID"] != nil {
		if err := decodeLedgerHex("NFTokenPage.PreviousTxnID", entry.PreviousTxnID, page.PreviousTxnID[:]); err != nil {
			return nil, err
		}
	}
	for i, value := range entry.NFTokens {
		wrapper, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("NFTokenPage.NFTokens[%d]: expected object, got %T", i, value)
		}
		value, ok = wrapper["NFToken"]
		if !ok {
			continue
		}
		tokenFields, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("NFTokenPage.NFTokens[%d].NFToken: expected object, got %T", i, value)
		}
		var token NFTokenData
		if tokenID, ok := tokenFields["NFTokenID"].(string); ok {
			if err := decodeLedgerHex("NFTokenPage.NFTokenID", tokenID, token.NFTokenID[:]); err != nil {
				return nil, err
			}
		}
		if uri, ok := tokenFields["URI"].(string); ok {
			token.URI = strings.ToLower(uri)
		}
		page.NFTokens = append(page.NFTokens, token)
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
	fields := entry.ToMap()
	offer := &NFTokenOfferData{
		Flags:          entry.Flags,
		Expiration:     entry.Expiration,
		HasDestination: fields["Destination"] != nil,
	}

	var err error
	if fields["Owner"] != nil {
		offer.Owner, err = decodeLedgerAccount("NFTokenOffer.Owner", entry.Owner)
		if err != nil {
			return nil, err
		}
	}
	if offer.HasDestination {
		offer.Destination, err = decodeLedgerAccount("NFTokenOffer.Destination", entry.Destination)
		if err != nil {
			return nil, err
		}
	}
	if fields["NFTokenID"] != nil {
		if err := decodeLedgerHex("NFTokenOffer.NFTokenID", entry.NFTokenID, offer.NFTokenID[:]); err != nil {
			return nil, err
		}
	}
	if fields["OwnerNode"] != nil {
		offer.OwnerNode, err = parseLedgerUint64("NFTokenOffer.OwnerNode", entry.OwnerNode)
		if err != nil {
			return nil, err
		}
	}
	if fields["NFTokenOfferNode"] != nil {
		offer.NFTokenOfferNode, err = parseLedgerUint64("NFTokenOffer.NFTokenOfferNode", entry.NFTokenOfferNode)
		if err != nil {
			return nil, err
		}
	}
	if fields["Amount"] != nil {
		amount, err := decodeLedgerAmount("NFTokenOffer.Amount", entry.Amount)
		if err != nil {
			return nil, err
		}
		switch {
		case amount.IsNative():
			offer.Amount = nativeMagnitude(amount)
			offer.Negative = amount.IsNegative()
		case !amount.IsMPT():
			offer.Negative = amount.IsNegative()
			issuer, err := decodeLedgerAccount("NFTokenOffer.Amount.issuer", amount.Issuer)
			if err != nil {
				return nil, err
			}
			offer.AmountIOU = &NFTIOUAmount{
				Currency: amount.Currency,
				Issuer:   issuer,
				Value:    amount.IOU().String(),
			}
		}
	}

	return offer, nil
}

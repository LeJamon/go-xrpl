package state

import (
	"encoding/hex"
	"fmt"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
)

// SerializeNFTokenPage serializes an NFToken page ledger entry.
func SerializeNFTokenPage(page *NFTokenPageData) ([]byte, error) {
	jsonObj := map[string]any{
		"LedgerEntryType": "NFTokenPage",
		"Flags":           uint32(0),
	}

	var emptyHash [32]byte
	if page.PreviousPageMin != emptyHash {
		jsonObj["PreviousPageMin"] = strings.ToUpper(hex.EncodeToString(page.PreviousPageMin[:]))
	}

	if page.NextPageMin != emptyHash {
		jsonObj["NextPageMin"] = strings.ToUpper(hex.EncodeToString(page.NextPageMin[:]))
	}

	// Emit only once threaded (fresh pages are stamped by the apply layer) so a no-op
	// modify round-trips byte-identically and the unchanged-entry guard prunes it
	// (ApplyStateTable.cpp:154-157).
	if page.PreviousTxnID != emptyHash {
		jsonObj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(page.PreviousTxnID[:]))
		jsonObj["PreviousTxnLgrSeq"] = page.PreviousTxnLgrSeq
	}

	if len(page.NFTokens) > 0 {
		nfTokens := make([]map[string]any, len(page.NFTokens))
		for i, token := range page.NFTokens {
			nfToken := map[string]any{
				"NFToken": map[string]any{
					"NFTokenID": strings.ToUpper(hex.EncodeToString(token.NFTokenID[:])),
				},
			}
			if token.URI != "" {
				nfToken["NFToken"].(map[string]any)["URI"] = token.URI
			}
			nfTokens[i] = nfToken
		}
		jsonObj["NFTokens"] = nfTokens
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode NFTokenPage: %w", err)
	}

	return hex.DecodeString(hexStr)
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

	jsonObj := map[string]any{
		"LedgerEntryType":  "NFTokenOffer",
		"Owner":            ownerAddress,
		"Amount":           amount,
		"NFTokenID":        strings.ToUpper(hex.EncodeToString(tokenID[:])),
		"OwnerNode":        fmt.Sprintf("%x", ownerNode),
		"NFTokenOfferNode": fmt.Sprintf("%x", offerNode),
		"Flags":            flags,
	}

	if expiration != nil {
		jsonObj["Expiration"] = *expiration
	}

	if destination != "" {
		jsonObj["Destination"] = destination
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode NFTokenOffer: %w", err)
	}

	return hex.DecodeString(hexStr)
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
	page := &NFTokenPageData{
		NFTokens: make([]NFTokenData, 0),
	}

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stHash256:
			switch f.FieldCode {
			case 5: // PreviousTxnID
				page.PreviousTxnID = f.Hash256()
			case 26: // PreviousPageMin
				page.PreviousPageMin = f.Hash256()
			case 27: // NextPageMin
				page.NextPageMin = f.Hash256()
			}

		case stUInt32:
			if f.FieldCode == 5 { // PreviousTxnLgrSeq
				page.PreviousTxnLgrSeq = f.UInt32()
			}

		case stArray:
			// NFTokens: each element is an NFToken object carrying an NFTokenID
			// and (optionally) a URI.
			return WalkFields(f.Value, func(elem Field) error {
				if elem.TypeCode != stObject {
					return nil
				}
				var tok NFTokenData
				if err := WalkFields(elem.Value, func(inner Field) error {
					switch inner.TypeCode {
					case stHash256:
						if inner.FieldCode == 10 { // NFTokenID
							tok.NFTokenID = inner.Hash256()
						}
					case stBlob:
						if inner.FieldCode == 5 { // URI
							tok.URI = hex.EncodeToString(inner.VLBytes())
						}
					}
					return nil
				}); err != nil {
					return err
				}
				page.NFTokens = append(page.NFTokens, tok)
				return nil
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return page, nil
}

// ParseNFTokenOffer parses an NFToken offer from binary data
func ParseNFTokenOffer(data []byte) (*NFTokenOfferData, error) {
	offer := &NFTokenOfferData{}

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stUInt32:
			switch f.FieldCode {
			case 2: // Flags
				offer.Flags = f.UInt32()
			case 10: // Expiration
				offer.Expiration = f.UInt32()
			}

		case stUInt64:
			switch f.FieldCode {
			case 4: // OwnerNode
				offer.OwnerNode = f.UInt64()
			case 12: // NFTokenOfferNode
				offer.NFTokenOfferNode = f.UInt64()
			}

		case stHash256:
			if f.FieldCode == 10 { // NFTokenID
				offer.NFTokenID = f.Hash256()
			}

		case stAmount:
			// The sign lives in bit 62 of the first value word (1 = positive); a
			// clear sign with a non-zero magnitude is negative.
			raw := f.UInt64()
			switch len(f.Value) {
			case 8: // XRP
				value := raw & 0x3FFFFFFFFFFFFFFF
				offer.Amount = value
				offer.Negative = (raw&0x4000000000000000) == 0 && value != 0
			case 48: // IOU
				offer.Negative = raw&0x4000000000000000 == 0 && raw&0x3FFFFFFFFFFFFFFF != 0
				iouAmount, err := ParseIOUAmountBinary(f.Value)
				if err != nil {
					return fmt.Errorf("NFTokenOffer IOU amount parse failed: %w", err)
				}
				var issuerID [20]byte
				copy(issuerID[:], f.Value[28:48])
				offer.AmountIOU = &NFTIOUAmount{
					Currency: iouAmount.Currency,
					Issuer:   issuerID,
					Value:    iouAmount.IOU().String(),
				}
			}

		case stAccountID:
			if id, ok := f.AccountID(); ok {
				switch f.FieldCode {
				case 1: // legacy sfAccount → Owner (pre-sfOwner-fix state)
					offer.Owner = id
				case 2: // sfOwner
					offer.Owner = id
				case 3: // Destination
					offer.Destination = id
					offer.HasDestination = true
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return offer, nil
}

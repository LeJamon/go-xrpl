package state

import (
	"encoding/hex"
	"fmt"
)

// NFTokenPageData represents an NFToken page ledger entry
type NFTokenPageData struct {
	PreviousPageMin [32]byte
	NextPageMin     [32]byte
	NFTokens        []NFTokenData
	// PreviousTxnID / PreviousTxnLgrSeq thread the page's modification history.
	// They must round-trip so a no-op modify (e.g. an NFTokenModify that sets a
	// URI to its current value) re-serializes byte-identically, letting the apply
	// layer's unchanged-entry guard prune it — matching rippled, which emits no
	// ModifiedNode and threads no PreviousTxnID when nothing changed
	// (ApplyStateTable.cpp:154-157). Zero when the page has never been threaded;
	// omitted on serialize in that case.
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

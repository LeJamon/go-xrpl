package sle

import (
	"encoding/binary"
	"encoding/hex"
)

// NFTokenPageData represents an NFToken page ledger entry
type NFTokenPageData struct {
	PreviousPageMin [32]byte
	NextPageMin     [32]byte
	NFTokens        []NFTokenData
}

// NFTokenData represents an individual NFToken within a page
type NFTokenData struct {
	NFTokenID [32]byte
	URI       string
}

// NFTokenOfferData represents an NFToken offer ledger entry
type NFTokenOfferData struct {
	Owner             [20]byte
	NFTokenID         [32]byte
	Amount            uint64
	AmountIOU         *NFTIOUAmount // For IOU amounts
	Flags             uint32
	Destination       [20]byte
	Expiration        uint32
	HasDestination    bool
	OwnerNode         uint64 // Page in owner directory where this offer is listed
	NFTokenOfferNode  uint64 // Page in NFTBuys/NFTSells directory where this offer is listed
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
	offset := 0

	var currentToken NFTokenData
	hasToken := false

	for offset < len(data) {
		if offset+1 > len(data) {
			break
		}

		header := data[offset]
		offset++

		typeCode := (header >> 4) & 0x0F
		fieldCode := header & 0x0F

		if typeCode == 0 {
			if offset >= len(data) {
				break
			}
			typeCode = data[offset]
			offset++
		}

		if fieldCode == 0 {
			if offset >= len(data) {
				break
			}
			fieldCode = data[offset]
			offset++
		}

		switch typeCode {
		case FieldTypeUInt16:
			if offset+2 > len(data) {
				return page, nil
			}
			offset += 2

		case FieldTypeUInt32:
			if offset+4 > len(data) {
				return page, nil
			}
			offset += 4

		case FieldTypeHash256:
			if offset+32 > len(data) {
				return page, nil
			}
			switch fieldCode {
			case 26: // PreviousPageMin (nth=26 per definitions.json)
				copy(page.PreviousPageMin[:], data[offset:offset+32])
			case 27: // NextPageMin (nth=27 per definitions.json)
				copy(page.NextPageMin[:], data[offset:offset+32])
			case 10: // NFTokenID
				if hasToken {
					page.NFTokens = append(page.NFTokens, currentToken)
				}
				copy(currentToken.NFTokenID[:], data[offset:offset+32])
				currentToken.URI = ""
				hasToken = true
			}
			offset += 32

		case FieldTypeUInt64: // 3
			if offset+8 > len(data) {
				return page, nil
			}
			offset += 8

		case FieldTypeBlob:
			if offset >= len(data) {
				return page, nil
			}
			length := int(data[offset])
			offset++
			if length > 192 {
				if offset >= len(data) {
					return page, nil
				}
				length = 193 + ((length-193)<<8 | int(data[offset]))
				offset++
			}
			if offset+length > len(data) {
				return page, nil
			}
			if fieldCode == 5 { // URI
				currentToken.URI = hex.EncodeToString(data[offset : offset+length])
			}
			offset += length

		case 8: // AccountID — 20 bytes
			if offset+20 > len(data) {
				return page, nil
			}
			offset += 20

		case 14, 15:
			// STObject (14) and STArray (15) structural markers.
			// These include array/object start field headers (e.g., 0xFA for NFTokens)
			// and end-of-object (0xE1) / end-of-array (0xF1) markers.
			// They have no payload — just continue parsing inner fields.
			continue

		default:
			if hasToken {
				page.NFTokens = append(page.NFTokens, currentToken)
				hasToken = false
			}
			return page, nil
		}
	}

	if hasToken {
		page.NFTokens = append(page.NFTokens, currentToken)
	}

	return page, nil
}

// ParseNFTokenPageFromBytes is an alias for ParseNFTokenPage
func ParseNFTokenPageFromBytes(data []byte) (*NFTokenPageData, error) {
	return ParseNFTokenPage(data)
}

// ParseNFTokenOffer parses an NFToken offer from binary data
func ParseNFTokenOffer(data []byte) (*NFTokenOfferData, error) {
	offer := &NFTokenOfferData{}
	offset := 0

	for offset < len(data) {
		if offset+1 > len(data) {
			break
		}

		header := data[offset]
		offset++

		typeCode := (header >> 4) & 0x0F
		fieldCode := header & 0x0F

		if typeCode == 0 {
			if offset >= len(data) {
				break
			}
			typeCode = data[offset]
			offset++
		}

		if fieldCode == 0 {
			if offset >= len(data) {
				break
			}
			fieldCode = data[offset]
			offset++
		}

		switch typeCode {
		case FieldTypeUInt16:
			if offset+2 > len(data) {
				return offer, nil
			}
			offset += 2

		case FieldTypeUInt32:
			if offset+4 > len(data) {
				return offer, nil
			}
			value := binary.BigEndian.Uint32(data[offset : offset+4])
			offset += 4
			switch fieldCode {
			case 2: // Flags
				offer.Flags = value
			case 10: // Expiration
				offer.Expiration = value
			}

		case FieldTypeUInt64:
			if offset+8 > len(data) {
				return offer, nil
			}
			value := binary.BigEndian.Uint64(data[offset : offset+8])
			switch fieldCode {
			case 4: // OwnerNode (UInt64 nth=4)
				offer.OwnerNode = value
			case 12: // NFTokenOfferNode (UInt64 nth=12)
				offer.NFTokenOfferNode = value
			}
			offset += 8

		case FieldTypeHash256:
			if offset+32 > len(data) {
				return offer, nil
			}
			if fieldCode == 10 { // NFTokenID
				copy(offer.NFTokenID[:], data[offset:offset+32])
			}
			offset += 32

		case FieldTypeAmount:
			if offset+8 > len(data) {
				return offer, nil
			}
			if data[offset]&0x80 == 0 {
				rawAmount := binary.BigEndian.Uint64(data[offset : offset+8])
				offer.Amount = rawAmount & 0x3FFFFFFFFFFFFFFF
				offset += 8
			} else {
				// IOU amount: 8 bytes value + 20 bytes currency + 20 bytes issuer = 48 bytes
				if offset+48 > len(data) {
					return offer, nil
				}
				iouAmount, err := ParseIOUAmountBinary(data[offset : offset+48])
				if err == nil {
					var issuerID [20]byte
					copy(issuerID[:], data[offset+28:offset+48])
					offer.AmountIOU = &NFTIOUAmount{
						Currency: iouAmount.Currency,
						Issuer:   issuerID,
						Value:    iouAmount.IOU().String(),
					}
				}
				offset += 48
			}

		case FieldTypeAccountID:
			if offset+21 > len(data) {
				return offer, nil
			}
			length := data[offset]
			offset++
			if length == 20 {
				switch fieldCode {
				case 1: // Account/Owner
					copy(offer.Owner[:], data[offset:offset+20])
				case 3: // Destination
					copy(offer.Destination[:], data[offset:offset+20])
					offer.HasDestination = true
				}
				offset += 20
			}

		case FieldTypeBlob: // 7
			if offset >= len(data) {
				return offer, nil
			}
			length := int(data[offset])
			offset++
			if length > 192 {
				if offset >= len(data) {
					return offer, nil
				}
				length = 193 + ((length-193)<<8 | int(data[offset]))
				offset++
			}
			if offset+length > len(data) {
				return offer, nil
			}
			offset += length

		case 14, 15:
			// STObject/STArray structural markers — skip
			continue

		default:
			return offer, nil
		}
	}

	return offer, nil
}

package service

import (
	"encoding/binary"
	"errors"
	"strconv"

	addresscodec "github.com/LeJamon/goXRPLd/codec/address-codec"
	"github.com/LeJamon/goXRPLd/keylet"
)

// NFTOfferInfo represents an individual NFToken offer for nft_buy_offers/nft_sell_offers RPC
type NFTOfferInfo struct {
	NFTOfferIndex string      // Hex string of offer key
	Flags         uint32      // Offer flags
	Owner         string      // Owner address (base58)
	Amount        interface{} // XRP drops (string) or IOU amount (map)
	Destination   string      // Optional destination address
	Expiration    uint32      // Optional expiration timestamp
}

// NFTOffersResult contains the result of nft_buy_offers/nft_sell_offers RPC
type NFTOffersResult struct {
	NFTID       string         // NFToken ID (hex)
	Offers      []NFTOfferInfo // Array of offers
	LedgerIndex uint32         // Ledger sequence
	LedgerHash  [32]byte       // Ledger hash
	Validated   bool           // Whether ledger is validated
	Limit       uint32         // Limit used (only when paginating)
	Marker      string         // Next page marker (only when more results)
}

// lsfSellNFToken is the flag indicating a sell offer
const lsfSellNFToken uint32 = 0x00000001

// GetNFTBuyOffers retrieves buy offers for an NFToken
// Reference: rippled NFTOffers.cpp enumerateNFTOffers with nft_buys keylet
func (s *Service) GetNFTBuyOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*NFTOffersResult, error) {
	return s.getNFTOffers(nftID, ledgerIndex, limit, marker, false)
}

// GetNFTSellOffers retrieves sell offers for an NFToken
// Reference: rippled NFTOffers.cpp enumerateNFTOffers with nft_sells keylet
func (s *Service) GetNFTSellOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*NFTOffersResult, error) {
	return s.getNFTOffers(nftID, ledgerIndex, limit, marker, true)
}

// getNFTOffers is the common implementation for both buy and sell offers
// Reference: rippled NFTOffers.cpp enumerateNFTOffers
func (s *Service) getNFTOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string, isSellOffers bool) (*NFTOffersResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Get the target ledger
	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		if err == ErrLedgerNotFound {
			return nil, errors.New("ledger not found")
		}
		return nil, err
	}

	// Get the directory keylet for buy or sell offers
	var dirKey keylet.Keylet
	if isSellOffers {
		dirKey = keylet.NFTSells(nftID)
	} else {
		dirKey = keylet.NFTBuys(nftID)
	}

	// Check if the directory exists
	exists, err := targetLedger.Exists(dirKey)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.New("object not found")
	}

	result := &NFTOffersResult{
		NFTID:       formatHashHex(nftID),
		Offers:      make([]NFTOfferInfo, 0),
		LedgerIndex: targetLedger.Sequence(),
		LedgerHash:  targetLedger.Hash(),
		Validated:   validated,
	}

	// Read the directory to get offer indexes
	dirData, err := targetLedger.Read(dirKey)
	if err != nil {
		return nil, err
	}

	// Parse directory to get offer indexes
	offerIndexes := parseDirectoryIndexesForNFT(dirData)
	if len(offerIndexes) == 0 {
		// No offers in directory
		return result, nil
	}

	// Handle marker/pagination
	// In rippled, if marker is present, the marker offer is included first, then we fetch limit-1 more
	// Reference: NFTOffers.cpp lines 86-115
	startIdx := 0
	reserve := limit

	if marker != "" {
		// Find the marker in the offer list and validate it
		markerBytes, err := hexDecode(marker)
		if err != nil || len(markerBytes) != 32 {
			return nil, errors.New("invalid marker")
		}
		var markerKey [32]byte
		copy(markerKey[:], markerBytes)

		// Verify the marker offer exists and belongs to this NFT
		markerKeylet := keylet.Keylet{Key: markerKey}
		offerData, err := targetLedger.Read(markerKeylet)
		if err != nil {
			return nil, errors.New("invalid marker")
		}

		// Parse the offer to verify NFTokenID matches
		offer, err := parseNFTokenOfferForQuery(offerData)
		if err != nil || offer.NFTokenID != nftID {
			return nil, errors.New("invalid marker")
		}

		// Add marker offer first
		offerInfo, err := s.buildNFTOfferInfo(markerKey, offer)
		if err == nil {
			result.Offers = append(result.Offers, offerInfo)
		}

		// Find position of marker in directory and start after it
		found := false
		for i, idx := range offerIndexes {
			if idx == markerKey {
				startIdx = i + 1
				found = true
				break
			}
		}
		if !found {
			// Marker not in directory - could be a different page
			// For simplicity, treat as invalid
			return nil, errors.New("invalid marker")
		}
	} else {
		// No marker, we'll fetch limit+1 to check for more results
		reserve++
	}

	// Collect offers from the directory
	offersCollected := make([]NFTOfferInfo, 0, reserve)
	for i := startIdx; i < len(offerIndexes) && uint32(len(offersCollected)) < reserve; i++ {
		offerKey := offerIndexes[i]
		offerKeylet := keylet.Keylet{Key: offerKey}

		offerData, err := targetLedger.Read(offerKeylet)
		if err != nil {
			continue
		}

		offer, err := parseNFTokenOfferForQuery(offerData)
		if err != nil {
			continue
		}

		offerInfo, err := s.buildNFTOfferInfo(offerKey, offer)
		if err != nil {
			continue
		}

		offersCollected = append(offersCollected, offerInfo)
	}

	// Handle pagination: if we got reserve offers, there are more
	if uint32(len(offersCollected)) == reserve && marker == "" {
		// We fetched limit+1 offers, so there's more
		result.Limit = limit
		result.Marker = offersCollected[len(offersCollected)-1].NFTOfferIndex
		// Remove the last offer (it's the marker for next page)
		offersCollected = offersCollected[:len(offersCollected)-1]
	}

	// Combine marker offer (if any) with collected offers
	result.Offers = append(result.Offers, offersCollected...)

	return result, nil
}

// NFTokenOfferForQuery contains parsed offer data for query purposes
type NFTokenOfferForQuery struct {
	Owner          [20]byte
	NFTokenID      [32]byte
	Amount         uint64       // XRP drops
	AmountIOU      *IOUAmountForQuery
	Flags          uint32
	Destination    [20]byte
	HasDestination bool
	Expiration     uint32
	OfferNode      uint64 // NFTokenOfferNode field
}

// IOUAmountForQuery represents an IOU amount for offer queries
type IOUAmountForQuery struct {
	Currency string
	Issuer   [20]byte
	Value    string
}

// buildNFTOfferInfo converts a parsed offer to the RPC response format
func (s *Service) buildNFTOfferInfo(offerKey [32]byte, offer *NFTokenOfferForQuery) (NFTOfferInfo, error) {
	info := NFTOfferInfo{
		NFTOfferIndex: formatHashHex(offerKey),
		Flags:         offer.Flags,
	}

	// Convert owner to base58 address
	ownerAddr, err := addresscodec.EncodeAccountIDToClassicAddress(offer.Owner[:])
	if err != nil {
		return info, err
	}
	info.Owner = ownerAddr

	// Format amount - either XRP drops or IOU
	if offer.AmountIOU != nil {
		// IOU amount
		issuerAddr, err := addresscodec.EncodeAccountIDToClassicAddress(offer.AmountIOU.Issuer[:])
		if err != nil {
			return info, err
		}
		info.Amount = map[string]string{
			"currency": offer.AmountIOU.Currency,
			"issuer":   issuerAddr,
			"value":    offer.AmountIOU.Value,
		}
	} else {
		// XRP amount as string (drops)
		info.Amount = strconv.FormatUint(offer.Amount, 10)
	}

	// Add optional destination
	if offer.HasDestination {
		destAddr, err := addresscodec.EncodeAccountIDToClassicAddress(offer.Destination[:])
		if err == nil {
			info.Destination = destAddr
		}
	}

	// Add optional expiration
	if offer.Expiration > 0 {
		info.Expiration = offer.Expiration
	}

	return info, nil
}

// parseNFTokenOfferForQuery parses an NFToken offer from binary data
// This is similar to tx.parseNFTokenOffer but returns a query-specific struct
func parseNFTokenOfferForQuery(data []byte) (*NFTokenOfferForQuery, error) {
	offer := &NFTokenOfferForQuery{}
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
		case 1: // UInt16 (LedgerEntryType, etc.)
			if offset+2 > len(data) {
				return offer, nil
			}
			offset += 2

		case 2: // UInt32
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

		case 3: // UInt64
			if offset+8 > len(data) {
				return offer, nil
			}
			value := binary.BigEndian.Uint64(data[offset : offset+8])
			offset += 8
			switch fieldCode {
			case 19: // NFTokenOfferNode
				offer.OfferNode = value
			}

		case 5: // Hash256
			if offset+32 > len(data) {
				return offer, nil
			}
			if fieldCode == 10 { // NFTokenID
				copy(offer.NFTokenID[:], data[offset:offset+32])
			}
			offset += 32

		case 6: // Amount
			// Check first byte to determine XRP vs IOU
			if offset+8 > len(data) {
				return offer, nil
			}
			if data[offset]&0x80 == 0 {
				// XRP amount (positive)
				rawAmount := binary.BigEndian.Uint64(data[offset : offset+8])
				offer.Amount = rawAmount & 0x3FFFFFFFFFFFFFFF
				offset += 8
			} else if data[offset] == 0x80 {
				// XRP amount of zero
				offer.Amount = 0
				offset += 8
			} else {
				// IOU amount (48 bytes total)
				if offset+48 > len(data) {
					return offer, nil
				}
				// Parse IOU amount
				iou := &IOUAmountForQuery{}

				// First 8 bytes: mantissa and exponent
				mantissa := binary.BigEndian.Uint64(data[offset : offset+8])
				isNegative := (mantissa & 0x4000000000000000) == 0
				exponent := int8((mantissa >> 54) & 0xFF) - 97
				mantissa = mantissa & 0x003FFFFFFFFFFFFF

				// Calculate the actual value
				if mantissa == 0 {
					iou.Value = "0"
				} else {
					// Convert to string representation
					val := float64(mantissa)
					for i := int8(0); i < exponent; i++ {
						val *= 10
					}
					for i := int8(0); i > exponent; i-- {
						val /= 10
					}
					if isNegative {
						val = -val
					}
					iou.Value = strconv.FormatFloat(val, 'f', -1, 64)
				}
				offset += 8

				// Next 20 bytes: currency
				currencyBytes := data[offset : offset+20]
				if currencyBytes[0] == 0 && currencyBytes[1] == 0 && currencyBytes[2] == 0 {
					// Standard 3-letter currency code at bytes 12-14
					iou.Currency = string(currencyBytes[12:15])
				} else {
					// Hex currency
					iou.Currency = formatHashHex([32]byte{})[:40] // placeholder
				}
				offset += 20

				// Last 20 bytes: issuer
				copy(iou.Issuer[:], data[offset:offset+20])
				offset += 20

				offer.AmountIOU = iou
			}

		case 8: // AccountID
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

		case 7: // Blob (VL encoded)
			if offset >= len(data) {
				return offer, nil
			}
			length := int(data[offset])
			offset++
			if length > 192 {
				if offset >= len(data) {
					return offer, nil
				}
				length = 193 + (length-193)*256 + int(data[offset])
				offset++
			}
			if offset+length > len(data) {
				return offer, nil
			}
			offset += length

		default:
			return offer, nil
		}
	}

	return offer, nil
}

// parseDirectoryIndexesForNFT parses a directory node to extract the Indexes field
// This is similar to parseDirectoryIndexes in apply_nftoken.go but for query purposes
func parseDirectoryIndexesForNFT(data []byte) [][32]byte {
	var indexes [][32]byte
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
		case 1: // UInt16
			if offset+2 > len(data) {
				return indexes
			}
			offset += 2

		case 2: // UInt32
			if offset+4 > len(data) {
				return indexes
			}
			offset += 4

		case 3: // UInt64
			if offset+8 > len(data) {
				return indexes
			}
			offset += 8

		case 5: // Hash256
			if offset+32 > len(data) {
				return indexes
			}
			offset += 32

		case 19: // Vector256 (STI_VECTOR256 = 19)
			// This is the Indexes field
			if fieldCode == 1 { // sfIndexes
				// Read VL length
				if offset >= len(data) {
					return indexes
				}
				length := int(data[offset])
				offset++
				if length > 192 {
					// Extended length encoding
					if offset >= len(data) {
						return indexes
					}
					length = 193 + (length-193)*256 + int(data[offset])
					offset++
				}
				// Each index is 32 bytes
				numIndexes := length / 32
				for i := 0; i < numIndexes && offset+32 <= len(data); i++ {
					var idx [32]byte
					copy(idx[:], data[offset:offset+32])
					indexes = append(indexes, idx)
					offset += 32
				}
			}

		case 8: // AccountID
			if offset >= len(data) {
				return indexes
			}
			length := int(data[offset])
			offset++
			if offset+length > len(data) {
				return indexes
			}
			offset += length

		case 7: // Blob
			if offset >= len(data) {
				return indexes
			}
			length := int(data[offset])
			offset++
			if length > 192 {
				if offset >= len(data) {
					return indexes
				}
				length = 193 + (length-193)*256 + int(data[offset])
				offset++
			}
			if offset+length > len(data) {
				return indexes
			}
			offset += length

		default:
			return indexes
		}
	}

	return indexes
}

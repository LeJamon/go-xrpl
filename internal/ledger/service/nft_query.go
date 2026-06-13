package service

import (
	"context"
	"encoding/hex"
	"strconv"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

// NFTOfferInfo represents an individual NFToken offer for nft_buy_offers/nft_sell_offers RPC
type NFTOfferInfo struct {
	NFTOfferIndex string // Hex string of offer key
	Flags         uint32 // Offer flags
	Owner         string // Owner address (base58)
	Amount        any    // XRP drops (string) or IOU amount (map)
	Destination   string // Optional destination address
	Expiration    uint32 // Optional expiration timestamp
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

// GetNFTBuyOffers retrieves buy offers for an NFToken
// Reference: rippled NFTOffers.cpp enumerateNFTOffers with nft_buys keylet
func (s *Service) GetNFTBuyOffers(ctx context.Context, nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*NFTOffersResult, error) {
	return s.getNFTOffers(ctx, nftID, ledgerIndex, limit, marker, false)
}

// GetNFTSellOffers retrieves sell offers for an NFToken
// Reference: rippled NFTOffers.cpp enumerateNFTOffers with nft_sells keylet
func (s *Service) GetNFTSellOffers(ctx context.Context, nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*NFTOffersResult, error) {
	return s.getNFTOffers(ctx, nftID, ledgerIndex, limit, marker, true)
}

// getNFTOffers is the common implementation for both buy and sell offers
// Reference: rippled NFTOffers.cpp enumerateNFTOffers
func (s *Service) getNFTOffers(ctx context.Context, nftID [32]byte, ledgerIndex string, limit uint32, marker string, isSellOffers bool) (*NFTOffersResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Get the target ledger
	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		if err == ErrLedgerNotFound {
			return nil, svcerr.ErrLedgerNotFound
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
		return nil, svcerr.ErrObjectNotFound
	}

	result := &NFTOffersResult{
		NFTID:       formatHashHex(nftID),
		Offers:      make([]NFTOfferInfo, 0),
		LedgerIndex: targetLedger.Sequence(),
		LedgerHash:  targetLedger.Hash(),
		Validated:   validated,
	}

	// Walk every directory page (following IndexNext) to collect all offer
	// indexes; rippled's enumerateNFTOffers pages through cdirNext, so a single
	// root-page read truncates books with more than one page of offers.
	var offerIndexes [][32]byte
	if walkErr := state.DirForEach(targetLedger, dirKey, func(itemKey [32]byte) error {
		offerIndexes = append(offerIndexes, itemKey)
		return nil
	}); walkErr != nil {
		return nil, walkErr
	}
	if len(offerIndexes) == 0 {
		return result, nil
	}

	// Handle marker/pagination
	// In rippled, if marker is present, the marker offer is included first, then we fetch limit-1 more
	// Reference: NFTOffers.cpp lines 86-115
	startIdx := 0
	reserve := limit

	if marker != "" {
		// Find the marker in the offer list and validate it
		markerBytes, err := hex.DecodeString(marker)
		if err != nil || len(markerBytes) != 32 {
			return nil, svcerr.ErrInvalidMarker
		}
		var markerKey [32]byte
		copy(markerKey[:], markerBytes)

		// Verify the marker offer exists and belongs to this NFT
		markerKeylet := keylet.Keylet{Key: markerKey}
		offerData, err := targetLedger.Read(markerKeylet)
		if err != nil {
			return nil, svcerr.ErrInvalidMarker
		}

		// Parse the offer to verify NFTokenID matches
		offer, err := state.ParseNFTokenOffer(offerData)
		if err != nil || offer.NFTokenID != nftID {
			return nil, svcerr.ErrInvalidMarker
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
			return nil, svcerr.ErrInvalidMarker
		}
	} else {
		// No marker, we'll fetch limit+1 to check for more results
		reserve++
	}

	// Collect offers from the directory
	offersCollected := make([]NFTOfferInfo, 0, reserve)
	for i := startIdx; i < len(offerIndexes) && uint32(len(offersCollected)) < reserve; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		offerKey := offerIndexes[i]
		offerKeylet := keylet.Keylet{Key: offerKey}

		offerData, err := targetLedger.Read(offerKeylet)
		if err != nil {
			continue
		}

		offer, err := state.ParseNFTokenOffer(offerData)
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

// buildNFTOfferInfo converts a parsed offer to the RPC response format
func (s *Service) buildNFTOfferInfo(offerKey [32]byte, offer *state.NFTokenOfferData) (NFTOfferInfo, error) {
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

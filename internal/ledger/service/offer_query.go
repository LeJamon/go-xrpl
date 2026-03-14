package service

import (
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
)

// BookOffer represents an offer in an order book
type BookOffer struct {
	Account         string      `json:"Account"`
	BookDirectory   string      `json:"BookDirectory"`
	BookNode        string      `json:"BookNode"`
	Flags           uint32      `json:"Flags"`
	LedgerEntryType string      `json:"LedgerEntryType"`
	OwnerNode       string      `json:"OwnerNode"`
	Sequence        uint32      `json:"Sequence"`
	TakerGets       interface{} `json:"TakerGets"`
	TakerPays       interface{} `json:"TakerPays"`
	Index           string      `json:"index"`
	Quality         string      `json:"quality"`
	OwnerFunds      string      `json:"owner_funds,omitempty"`
	TakerGetsFunded interface{} `json:"taker_gets_funded,omitempty"`
	TakerPaysFunded interface{} `json:"taker_pays_funded,omitempty"`
}

// BookOffersResult contains the result of book_offers RPC
type BookOffersResult struct {
	LedgerIndex uint32      `json:"ledger_index"`
	LedgerHash  [32]byte    `json:"ledger_hash"`
	Offers      []BookOffer `json:"offers"`
	Validated   bool        `json:"validated"`
}

// GetBookOffers retrieves offers from an order book
func (s *Service) GetBookOffers(takerGets, takerPays tx.Amount, ledgerIndex string, limit uint32) (*BookOffersResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Determine which ledger to use
	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		return nil, err
	}

	// Set default limit
	if limit == 0 || limit > 400 {
		limit = 200
	}

	// Collect matching offers by iterating through ledger entries
	var offers []BookOffer

	targetLedger.ForEach(func(key [32]byte, data []byte) bool {
		// Check if we've reached the limit
		if uint32(len(offers)) >= limit {
			return false
		}

		// Check if this is an Offer entry
		if len(data) < 3 {
			return true
		}

		// Check LedgerEntryType field
		if data[0] != 0x11 {
			return true
		}
		entryType := uint16(data[1])<<8 | uint16(data[2])
		if entryType != 0x006F { // Offer type
			return true
		}

		// Parse the Offer
		offer, err := state.ParseLedgerOfferFromBytes(data)
		if err != nil {
			return true
		}

		// Check if this offer matches the requested book
		// TakerGets in offer should match our takerGets parameter
		// TakerPays in offer should match our takerPays parameter
		getsMatch := amountsMatchCurrency(offer.TakerGets, takerGets)
		paysMatch := amountsMatchCurrency(offer.TakerPays, takerPays)

		if !getsMatch || !paysMatch {
			return true
		}

		// Build book offer response
		bookOffer := BookOffer{
			Account:         offer.Account,
			Flags:           offer.Flags,
			LedgerEntryType: "Offer",
			Sequence:        offer.Sequence,
			Index:           formatHash(key),
			Quality:         calculateOfferQuality(offer.TakerPays, offer.TakerGets),
		}

		// Format TakerGets
		if offer.TakerGets.IsNative() {
			bookOffer.TakerGets = offer.TakerGets.Value()
		} else {
			bookOffer.TakerGets = map[string]string{
				"currency": offer.TakerGets.Currency,
				"issuer":   offer.TakerGets.Issuer,
				"value":    offer.TakerGets.Value(),
			}
		}

		// Format TakerPays
		if offer.TakerPays.IsNative() {
			bookOffer.TakerPays = offer.TakerPays.Value()
		} else {
			bookOffer.TakerPays = map[string]string{
				"currency": offer.TakerPays.Currency,
				"issuer":   offer.TakerPays.Issuer,
				"value":    offer.TakerPays.Value(),
			}
		}

		offers = append(offers, bookOffer)
		return true
	})

	// Sort offers by quality (best first)
	sortBookOffersByQuality(offers)

	return &BookOffersResult{
		LedgerIndex: targetLedger.Sequence(),
		LedgerHash:  targetLedger.Hash(),
		Offers:      offers,
		Validated:   validated,
	}, nil
}

package service

import (
	"context"
	"encoding/binary"

	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/protocol"
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

// GetBookOffers retrieves offers from an order book.
// taker is the optional account viewing the book; when set and equal to the
// takerGets issuer, the transfer-fee deduction is skipped on owner funds.
// Matches rippled NetworkOPsImp::getBookPage (NetworkOPs.cpp).
func (s *Service) GetBookOffers(ctx context.Context, takerGets, takerPays tx.Amount, taker string, ledgerIndex string, limit uint32) (*BookOffersResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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

	// Collect matching offers (and the parsed source data, kept in lockstep
	// so post-sort funding computation has access to the raw amounts +
	// book-directory needed to mirror rippled getBookPage).
	var offers []BookOffer
	var rawOffers []*state.LedgerOffer

	targetLedger.ForEachCtx(ctx, func(key [32]byte, data []byte) bool {
		if ctx.Err() != nil {
			return false
		}
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
		rawOffers = append(rawOffers, offer)
		return true
	})

	// Sort offers by quality (best first) — keeping rawOffers in lockstep
	// so the post-sort funding pass walks them in the same order rippled's
	// directory iteration would (best-quality-first).
	sortBookOffersByQualityWithRaw(offers, rawOffers)

	// Compute owner_funds + taker_*_funded fields per rippled getBookPage.
	applyBookOfferFundingInfo(targetLedger, offers, rawOffers, takerGets, takerPays, taker)

	return &BookOffersResult{
		LedgerIndex: targetLedger.Sequence(),
		LedgerHash:  targetLedger.Hash(),
		Offers:      offers,
		Validated:   validated,
	}, nil
}

// applyBookOfferFundingInfo populates owner_funds, taker_gets_funded, and
// taker_pays_funded on each entry in offers. Mirrors rippled's
// NetworkOPsImp::getBookPage (NetworkOPs.cpp:4430-4629):
//
//  1. Resolve saOwnerFunds per offer (issuer-owned offers are fully funded,
//     globally-frozen books drop to zero, otherwise call accountHolds with
//     fhZERO_IF_FROZEN).
//  2. Apply the takerGets-issuer transfer rate to derive saOwnerFundsLimit
//     unless the taker is the issuer or the offer owner is the issuer.
//  3. If saOwnerFundsLimit < saTakerGets, the offer is partially funded:
//     set taker_gets_funded = saOwnerFundsLimit, and
//     taker_pays_funded = min(saTakerPays, saTakerGetsFunded * qualityRate).
//  4. Track a running owner-balance map so subsequent offers from the same
//     account see the post-deduction balance, and only the first offer per
//     owner gets owner_funds emitted.
func applyBookOfferFundingInfo(
	l *ledger.Ledger,
	offers []BookOffer,
	raw []*state.LedgerOffer,
	takerGets, takerPays tx.Amount,
	taker string,
) {
	if len(offers) == 0 {
		return
	}

	baseFee, reserveBase, reserveIncrement := readFeesFromLedger(l)
	_ = baseFee

	// Globally-frozen books force all third-party offers to zero funds
	// (NetworkOPs.cpp:4457-4458).
	globalFreeze := false
	if !takerGets.IsNative() {
		globalFreeze = globalFreeze || tx.IsGlobalFrozen(l, takerGets.Issuer)
	}
	if !takerPays.IsNative() {
		globalFreeze = globalFreeze || tx.IsGlobalFrozen(l, takerPays.Issuer)
	}

	// Resolve the takerGets issuer's transfer rate. XRP is implicitly
	// parity (no transfer fee).
	rate := protocol.QualityOne
	var issuerID [20]byte
	haveIssuer := false
	if !takerGets.IsNative() {
		if id, err := state.DecodeAccountID(takerGets.Issuer); err == nil {
			issuerID = id
			haveIssuer = true
			rate = getTransferRateForIssuer(l, issuerID)
		}
	}

	// uTakerID: when the requesting taker is the takerGets issuer, the
	// transfer-fee deduction is suppressed (rippled NetworkOPs.cpp:4567).
	var takerID [20]byte
	haveTaker := false
	if taker != "" {
		if id, err := state.DecodeAccountID(taker); err == nil {
			takerID = id
			haveTaker = true
		}
	}

	umBalance := make(map[[20]byte]tx.Amount)
	seenOwner := make(map[[20]byte]bool)

	for i := range offers {
		offer := raw[i]
		ownerID, err := state.DecodeAccountID(offer.Account)
		if err != nil {
			continue
		}

		saTakerGets := offer.TakerGets
		saTakerPays := offer.TakerPays

		var saOwnerFunds tx.Amount
		firstOwnerOffer := !seenOwner[ownerID]

		switch {
		case haveIssuer && ownerID == issuerID:
			// Offer is issuing its own IOUs — always fully funded.
			saOwnerFunds = saTakerGets
		case globalFreeze:
			saOwnerFunds = zeroLike(saTakerGets)
		default:
			if existing, ok := umBalance[ownerID]; ok {
				saOwnerFunds = existing
				firstOwnerOffer = false
			} else {
				saOwnerFunds = tx.AccountFunds(l, ownerID, saTakerGets, true, reserveBase, reserveIncrement)
				if saOwnerFunds.Signum() < 0 {
					saOwnerFunds = zeroLike(saTakerGets)
				}
			}
		}

		offerRate := protocol.QualityOne
		saOwnerFundsLimit := saOwnerFunds
		takerIsIssuer := haveTaker && haveIssuer && takerID == issuerID
		ownerIsIssuer := haveIssuer && ownerID == issuerID
		if rate != protocol.QualityOne && !takerIsIssuer && !ownerIsIssuer {
			offerRate = rate
			saOwnerFundsLimit = saOwnerFunds.MulRatio(protocol.QualityOne, offerRate, false)
		}

		var saTakerGetsFunded tx.Amount
		if saOwnerFundsLimit.Compare(saTakerGets) >= 0 {
			saTakerGetsFunded = saTakerGets
		} else {
			saTakerGetsFunded = saOwnerFundsLimit
			offers[i].TakerGetsFunded = formatAmount(saTakerGetsFunded)

			// taker_pays_funded = min(saTakerPays, saTakerGetsFunded * quality)
			paid := multiplyByQuality(saTakerGetsFunded, offer.BookDirectory, saTakerPays)
			if paid.Compare(saTakerPays) > 0 {
				paid = saTakerPays
			}
			offers[i].TakerPaysFunded = formatAmount(paid)
		}

		// Running balance: saOwnerPays = (parity) ? saTakerGetsFunded :
		//   min(saOwnerFunds, saTakerGetsFunded * offerRate)
		var saOwnerPays tx.Amount
		if offerRate == protocol.QualityOne {
			saOwnerPays = saTakerGetsFunded
		} else {
			grossed := saTakerGetsFunded.MulRatio(offerRate, protocol.QualityOne, false)
			if grossed.Compare(saOwnerFunds) < 0 {
				saOwnerPays = grossed
			} else {
				saOwnerPays = saOwnerFunds
			}
		}

		if remaining, err := saOwnerFunds.Sub(saOwnerPays); err == nil {
			umBalance[ownerID] = remaining
		} else {
			umBalance[ownerID] = zeroLike(saOwnerFunds)
		}

		if firstOwnerOffer {
			offers[i].OwnerFunds = saOwnerFunds.Value()
			seenOwner[ownerID] = true
		}
	}
}

// getTransferRateForIssuer reads the issuer's TransferRate field directly
// from its AccountRoot. Returns QualityOne (no fee) when the account is
// missing or has no transfer rate set. Avoids depending on the heavier
// payment package which would pull the entire flow machinery into the
// service layer.
func getTransferRateForIssuer(l *ledger.Ledger, issuerID [20]byte) uint32 {
	root, err := l.Read(keylet.Account(issuerID))
	if err != nil || root == nil {
		return protocol.QualityOne
	}
	account, err := state.ParseAccountRoot(root)
	if err != nil || account.TransferRate == 0 {
		return protocol.QualityOne
	}
	return account.TransferRate
}

// multiplyByQuality returns (gets * qualityRate) coerced into the issue of
// template. quality is decoded from the low 8 bytes of the BookDirectory
// key — the same encoding rippled uses for getQuality(uTipIndex).
func multiplyByQuality(gets tx.Amount, bookDirectory [32]byte, template tx.Amount) tx.Amount {
	qValue := binary.BigEndian.Uint64(bookDirectory[24:])
	if qValue == 0 {
		return zeroLike(template)
	}
	qMantissa := int64(qValue & 0x00FFFFFFFFFFFFFF)
	qExponent := int(qValue>>56) - 100
	qRate := state.NewIssuedAmountFromValue(qMantissa, qExponent, "", "")

	product := gets.Mul(qRate, false)

	// Coerce the product to the template's issue (XRP vs IOU). rippled's
	// multiply(v1, v2, issue) sets the result's issue argument directly.
	if template.IsNative() {
		mantissa := product.Mantissa()
		exponent := product.Exponent()
		for exponent > 0 {
			mantissa *= 10
			exponent--
		}
		for exponent < 0 {
			mantissa /= 10
			exponent++
		}
		return tx.NewXRPAmount(mantissa)
	}
	return state.NewIssuedAmountFromValue(product.Mantissa(), product.Exponent(), template.Currency, template.Issuer)
}

// zeroLike returns a zero amount with the same kind (XRP vs IOU) as
// template, preserving currency/issuer for IOU.
func zeroLike(template tx.Amount) tx.Amount {
	if template.IsNative() {
		return tx.NewXRPAmount(0)
	}
	return state.NewIssuedAmountFromValue(0, -100, template.Currency, template.Issuer)
}

// formatAmount renders an Amount in the same JSON shape book_offers uses
// for TakerGets / TakerPays: a drops string for XRP, an object for IOU.
func formatAmount(a tx.Amount) interface{} {
	if a.IsNative() {
		return a.Value()
	}
	return map[string]string{
		"currency": a.Currency,
		"issuer":   a.Issuer,
		"value":    a.Value(),
	}
}

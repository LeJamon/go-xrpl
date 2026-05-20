package service

import (
	"context"
	"sort"

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

// rawOffer holds a parsed Offer SLE plus its ledger key, retained between the
// discovery pass and the funded-amount pass.
type rawOffer struct {
	offer *state.LedgerOffer
	key   [32]byte
}

// GetBookOffers retrieves offers from an order book and computes
// owner_funds / taker_gets_funded / taker_pays_funded for each one, mirroring
// rippled NetworkOPsImp::getBookPage (NetworkOPs.cpp).
//
// The taker argument is optional; when it matches the issuer of takerGets,
// rippled's "Not taking offers of own IOUs" branch suppresses the transfer
// fee adjustment.
func (s *Service) GetBookOffers(ctx context.Context, takerGets, takerPays tx.Amount, taker string, ledgerIndex string, limit uint32) (*BookOffersResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		return nil, err
	}

	if limit == 0 || limit > 400 {
		limit = 200
	}

	// Pass 1: collect every matching offer. Funded-amount computation must
	// happen in quality order so per-owner running balances stay consistent
	// with rippled, so we sort before doing it.
	var raws []rawOffer
	targetLedger.ForEachCtx(ctx, func(key [32]byte, data []byte) bool {
		if ctx.Err() != nil {
			return false
		}
		if len(data) < 3 || data[0] != 0x11 {
			return true
		}
		entryType := uint16(data[1])<<8 | uint16(data[2])
		if entryType != 0x006F {
			return true
		}
		offer, err := state.ParseLedgerOfferFromBytes(data)
		if err != nil {
			return true
		}
		if !amountsMatchCurrency(offer.TakerGets, takerGets) ||
			!amountsMatchCurrency(offer.TakerPays, takerPays) {
			return true
		}
		raws = append(raws, rawOffer{offer: offer, key: key})
		return true
	})

	sort.SliceStable(raws, func(i, j int) bool {
		qi := parseAmountValue(raws[i].offer.TakerPays) /
			nonZero(parseAmountValue(raws[i].offer.TakerGets))
		qj := parseAmountValue(raws[j].offer.TakerPays) /
			nonZero(parseAmountValue(raws[j].offer.TakerGets))
		return qi < qj
	})

	if uint32(len(raws)) > limit {
		raws = raws[:limit]
	}

	// Book-level state, computed once for the funded pass.
	getsIssuer := takerGets.Issuer
	bGlobalFreeze := tx.IsGlobalFrozen(targetLedger, takerGets.Issuer) ||
		tx.IsGlobalFrozen(targetLedger, takerPays.Issuer)
	rate := tx.TransferRateParity
	if !takerGets.IsNative() {
		rate = tx.GetTransferRate(targetLedger, getsIssuer)
	}
	_, reserveBase, reserveIncrement := readFeesFromLedger(targetLedger)

	balances := make(map[string]float64)
	balanceSeen := make(map[string]bool)

	offers := make([]BookOffer, 0, len(raws))
	for _, r := range raws {
		offer := r.offer
		bookOffer := BookOffer{
			Account:         offer.Account,
			Flags:           offer.Flags,
			LedgerEntryType: "Offer",
			Sequence:        offer.Sequence,
			Index:           formatHash(r.key),
			Quality:         calculateOfferQuality(offer.TakerPays, offer.TakerGets),
		}
		bookOffer.TakerGets = amountToJSON(offer.TakerGets)
		bookOffer.TakerPays = amountToJSON(offer.TakerPays)

		var ownerFunds float64
		var ownerFundsAmount tx.Amount
		ownerOwnsIssue := !takerGets.IsNative() && offer.Account == getsIssuer

		switch {
		case ownerOwnsIssue:
			ownerFunds = parseAmountValue(offer.TakerGets)
			ownerFundsAmount = offer.TakerGets
		case bGlobalFreeze:
			ownerFunds = 0
			ownerFundsAmount = zeroLike(takerGets)
		default:
			if balanceSeen[offer.Account] {
				ownerFunds = balances[offer.Account]
				ownerFundsAmount = makeAmountFromFloat(takerGets, ownerFunds)
			} else {
				accountID, decErr := decodeAccountIDLocal(offer.Account)
				if decErr != nil {
					return nil, decErr
				}
				ownerFundsAmount = tx.AccountFunds(targetLedger, accountID, takerGets, true, reserveBase, reserveIncrement)
				ownerFunds = parseAmountValue(ownerFundsAmount)
				if ownerFunds < 0 {
					ownerFunds = 0
					ownerFundsAmount = zeroLike(takerGets)
				}
			}
		}

		// Transfer-fee adjustment is skipped when the taker is the issuer of
		// taker_gets, or when the offer owner is that issuer.
		offerRate := tx.TransferRateParity
		ownerFundsLimit := ownerFunds
		if rate != tx.TransferRateParity && !ownerOwnsIssue &&
			(taker == "" || taker != getsIssuer) {
			offerRate = rate
			ownerFundsLimit = ownerFunds / (float64(rate) / float64(tx.TransferRateParity))
		}

		gets := parseAmountValue(offer.TakerGets)
		pays := parseAmountValue(offer.TakerPays)
		var takerGetsFunded float64
		if ownerFundsLimit >= gets {
			takerGetsFunded = gets
		} else {
			takerGetsFunded = ownerFundsLimit
			bookOffer.TakerGetsFunded = amountToJSON(makeAmountFromFloat(offer.TakerGets, takerGetsFunded))
			fundedPays := pays
			if gets > 0 {
				fundedPays = takerGetsFunded * (pays / gets)
				if fundedPays > pays {
					fundedPays = pays
				}
			}
			bookOffer.TakerPaysFunded = amountToJSON(makeAmountFromFloat(offer.TakerPays, fundedPays))
		}

		switch {
		case ownerOwnsIssue:
			if !balanceSeen[offer.Account] {
				bookOffer.OwnerFunds = ownerFundsAmount.Value()
				balanceSeen[offer.Account] = true
			}
		case bGlobalFreeze:
			if !balanceSeen[offer.Account] {
				bookOffer.OwnerFunds = ownerFundsAmount.Value()
				balanceSeen[offer.Account] = true
			}
		default:
			ownerPays := takerGetsFunded
			if offerRate != tx.TransferRateParity {
				ownerPays = takerGetsFunded * (float64(offerRate) / float64(tx.TransferRateParity))
				if ownerPays > ownerFunds {
					ownerPays = ownerFunds
				}
			}
			remaining := ownerFunds - ownerPays
			if remaining < 0 {
				remaining = 0
			}
			if !balanceSeen[offer.Account] {
				bookOffer.OwnerFunds = ownerFundsAmount.Value()
				balanceSeen[offer.Account] = true
			}
			balances[offer.Account] = remaining
		}

		offers = append(offers, bookOffer)
	}

	return &BookOffersResult{
		LedgerIndex: targetLedger.Sequence(),
		LedgerHash:  targetLedger.Hash(),
		Offers:      offers,
		Validated:   validated,
	}, nil
}

// amountToJSON returns rippled's wire format for an Amount: a drops string for
// XRP, a {currency, issuer, value} map for issued amounts.
func amountToJSON(a tx.Amount) interface{} {
	if a.IsNative() {
		return a.Value()
	}
	return map[string]string{
		"currency": a.Currency,
		"issuer":   a.Issuer,
		"value":    a.Value(),
	}
}

func zeroLike(model tx.Amount) tx.Amount {
	if model.IsNative() {
		return tx.NewXRPAmount(0)
	}
	return tx.NewIssuedAmount(0, 0, model.Currency, model.Issuer)
}

// makeAmountFromFloat builds an Amount with `model`'s currency/issuer profile
// and the given magnitude. Precision is bounded by float64 / IOU mantissa.
func makeAmountFromFloat(model tx.Amount, v float64) tx.Amount {
	if v < 0 {
		v = 0
	}
	if model.IsNative() {
		return tx.NewXRPAmount(int64(v))
	}
	return state.NewIssuedAmountFromFloat64(v, model.Currency, model.Issuer)
}

func nonZero(v float64) float64 {
	if v == 0 {
		return 1
	}
	return v
}

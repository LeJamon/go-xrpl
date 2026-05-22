package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/protocol"
)

// BookOffer represents an offer in an order book.
// Field set mirrors rippled NetworkOPs.cpp:4559 (sleOffer->getJson) +
// the quality/owner_funds/taker_*_funded augmentations in NetworkOPs.cpp:4596-4612.
// Expiration is a pointer so it serializes only when the SLE actually carries
// the optional field (rippled emits it only when set).
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
	Expiration      *uint32     `json:"Expiration,omitempty"`
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

// GetBookOffers mirrors rippled NetworkOPsImp::getBookPage
// (NetworkOPs.cpp). taker is the optional account viewing the book —
// when equal to the takerGets issuer it suppresses the transfer-fee
// deduction on owner funds.
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

	if limit == 0 {
		limit = 60
	}

	offers, rawOffers := walkBookOffers(ctx, targetLedger, takerPays, takerGets, limit)
	applyBookOfferFundingInfo(targetLedger, offers, rawOffers, takerGets, takerPays, taker)

	return &BookOffersResult{
		LedgerIndex: targetLedger.Sequence(),
		LedgerHash:  targetLedger.Hash(),
		Offers:      offers,
		Validated:   validated,
	}, nil
}

// walkBookOffers mirrors rippled NetworkOPs.cpp:4430-4629 directory walk:
// step through quality buckets via Succ, then within each bucket follow
// IndexNext page chains. Offers within a single page are emitted in their
// stored Indexes order — matching rippled's cdirFirst / cdirNext.
//
// The iteration budget mirrors rippled's `iLimit-- > 0` loop: each entry
// visited consumes one unit regardless of whether the underlying SLE
// parses cleanly. A book whose directory references a missing or
// malformed Offer SLE will therefore return fewer than `limit` offers,
// just as rippled does (NetworkOPs.cpp:4471, :4506-4612).
func walkBookOffers(
	ctx context.Context,
	l *ledger.Ledger,
	takerPays, takerGets tx.Amount,
	limit uint32,
) ([]BookOffer, []*state.LedgerOffer) {
	takerPaysCurrency := state.GetCurrencyBytes(takerPays.Currency)
	takerPaysIssuer := state.GetIssuerBytes(takerPays.Issuer)
	takerGetsCurrency := state.GetCurrencyBytes(takerGets.Currency)
	takerGetsIssuer := state.GetIssuerBytes(takerGets.Issuer)

	bookBase := keylet.BookDir(takerPaysCurrency, takerPaysIssuer, takerGetsCurrency, takerGetsIssuer).Key
	for i := 24; i < 32; i++ {
		bookBase[i] = 0
	}
	bookPrefix := bookBase[:24]

	var offers []BookOffer
	var rawOffers []*state.LedgerOffer

	remaining := limit
	searchKey := bookBase
	for remaining > 0 {
		if ctx.Err() != nil {
			break
		}
		foundKey, foundData, found, err := l.Succ(searchKey)
		if err != nil || !found {
			break
		}
		if !bytes.Equal(foundKey[:24], bookPrefix) {
			break
		}

		rootKey := foundKey
		dir, err := state.ParseDirectoryNode(foundData)
		if err != nil {
			searchKey = foundKey
			continue
		}
		quality := binary.BigEndian.Uint64(rootKey[24:])
		qualityStr := encodeDirRate(quality)

	pageLoop:
		for {
			for _, idx := range dir.Indexes {
				if remaining == 0 {
					break pageLoop
				}
				remaining--
				offerData, err := l.Read(keylet.Keylet{Key: idx})
				if err != nil || offerData == nil {
					continue
				}
				offer, err := state.ParseLedgerOfferFromBytes(offerData)
				if err != nil {
					continue
				}
				offers = append(offers, makeBookOffer(idx, offer, qualityStr))
				rawOffers = append(rawOffers, offer)
			}
			if dir.IndexNext == 0 {
				break
			}
			nextPage := keylet.DirPage(rootKey, dir.IndexNext)
			pageData, err := l.Read(nextPage)
			if err != nil || pageData == nil {
				break
			}
			dir, err = state.ParseDirectoryNode(pageData)
			if err != nil {
				break
			}
		}
		searchKey = rootKey
	}
	return offers, rawOffers
}

// makeBookOffer renders a parsed LedgerOffer into the wire shape rippled
// emits for book_offers entries (jss::quality from the directory rate).
// BookDirectory / BookNode / OwnerNode / Expiration mirror the fields rippled
// surfaces via sleOffer->getJson at NetworkOPs.cpp:4559 — clients (xrpl.js,
// xrpl-py) rely on BookDirectory for the quality bucket and on Expiration to
// drop soon-to-expire offers client-side.
func makeBookOffer(key [32]byte, offer *state.LedgerOffer, quality string) BookOffer {
	bookOffer := BookOffer{
		Account:         offer.Account,
		BookDirectory:   strings.ToUpper(hex.EncodeToString(offer.BookDirectory[:])),
		BookNode:        fmt.Sprintf("%x", offer.BookNode),
		Flags:           offer.Flags,
		LedgerEntryType: "Offer",
		OwnerNode:       fmt.Sprintf("%x", offer.OwnerNode),
		Sequence:        offer.Sequence,
		Index:           formatHash(key),
		Quality:         quality,
	}
	if offer.Expiration > 0 {
		exp := offer.Expiration
		bookOffer.Expiration = &exp
	}
	if offer.TakerGets.IsNative() {
		bookOffer.TakerGets = offer.TakerGets.Value()
	} else {
		bookOffer.TakerGets = map[string]string{
			"currency": offer.TakerGets.Currency,
			"issuer":   offer.TakerGets.Issuer,
			"value":    offer.TakerGets.Value(),
		}
	}
	if offer.TakerPays.IsNative() {
		bookOffer.TakerPays = offer.TakerPays.Value()
	} else {
		bookOffer.TakerPays = map[string]string{
			"currency": offer.TakerPays.Currency,
			"issuer":   offer.TakerPays.Issuer,
			"value":    offer.TakerPays.Value(),
		}
	}
	return bookOffer
}

// encodeDirRate decodes a directory's packed quality (high byte = exponent
// + 100, low 7 bytes = mantissa) and renders it as rippled's
// saDirRate.getText() — the canonical no-issue STAmount text form.
func encodeDirRate(rate uint64) string {
	if rate == 0 {
		return "0"
	}
	mantissa := int64(rate & 0x00FFFFFFFFFFFFFF)
	exponent := int(rate>>56) - 100
	return state.NewIssuedAmountFromValue(mantissa, exponent, "", "").Value()
}

// applyBookOfferFundingInfo mirrors rippled NetworkOPs.cpp:4430-4629
// (getBookPage's per-offer funding pass).
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

	_, reserveBase, reserveIncrement := readFeesFromLedger(l)

	globalFreeze := false
	if !takerGets.IsNative() {
		globalFreeze = globalFreeze || tx.IsGlobalFrozen(l, takerGets.Issuer)
	}
	if !takerPays.IsNative() {
		globalFreeze = globalFreeze || tx.IsGlobalFrozen(l, takerPays.Issuer)
	}

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

	var takerID [20]byte
	haveTaker := false
	if taker != "" {
		if id, err := state.DecodeAccountID(taker); err == nil {
			takerID = id
			haveTaker = true
		}
	}

	umBalance := make(map[[20]byte]tx.Amount)

	for i := range offers {
		offer := raw[i]
		ownerID, err := state.DecodeAccountID(offer.Account)
		if err != nil {
			continue
		}

		saTakerGets := offer.TakerGets
		saTakerPays := offer.TakerPays

		var saOwnerFunds tx.Amount
		firstOwnerOffer := true

		switch {
		case haveIssuer && ownerID == issuerID:
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

			paid := multiplyByQuality(saTakerGetsFunded, offer.BookDirectory, saTakerPays)
			if paid.Compare(saTakerPays) > 0 {
				paid = saTakerPays
			}
			offers[i].TakerPaysFunded = formatAmount(paid)
		}

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
		}
	}
}

// getTransferRateForIssuer reads the issuer's TransferRate field
// directly so the service layer does not depend on the payment package's
// flow machinery. Returns QualityOne (no fee) when unset or unreadable.
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

// multiplyByQuality returns (gets * qualityRate) coerced into template's
// issue, where qualityRate is decoded from the low 8 bytes of the
// BookDirectory key (rippled's getQuality(uTipIndex) encoding).
func multiplyByQuality(gets tx.Amount, bookDirectory [32]byte, template tx.Amount) tx.Amount {
	qValue := binary.BigEndian.Uint64(bookDirectory[24:])
	if qValue == 0 {
		return zeroLike(template)
	}
	qMantissa := int64(qValue & 0x00FFFFFFFFFFFFFF)
	qExponent := int(qValue>>56) - 100
	qRate := state.NewIssuedAmountFromValue(qMantissa, qExponent, "", "")

	product := gets.Mul(qRate, false)

	// rippled multiply(v1, v2, issue) sets the result's issue directly; coerce here.
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

func zeroLike(template tx.Amount) tx.Amount {
	if template.IsNative() {
		return tx.NewXRPAmount(0)
	}
	return state.NewIssuedAmountFromValue(0, -100, template.Currency, template.Issuer)
}

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

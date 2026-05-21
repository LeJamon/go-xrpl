package service

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
)

// BookOffer represents an offer in an order book. The wire shape mirrors
// rippled's SLE-derived JSON (NetworkOPsImp::getBookPage uses
// sleOffer->getJson(JsonOptions::none)).
type BookOffer struct {
	Account           string        `json:"Account"`
	BookDirectory     string        `json:"BookDirectory"`
	BookNode          string        `json:"BookNode"`
	Expiration        uint32        `json:"Expiration,omitempty"`
	Flags             uint32        `json:"Flags"`
	LedgerEntryType   string        `json:"LedgerEntryType"`
	OwnerNode         string        `json:"OwnerNode"`
	PreviousTxnID     string        `json:"PreviousTxnID"`
	PreviousTxnLgrSeq uint32        `json:"PreviousTxnLgrSeq"`
	Sequence          uint32        `json:"Sequence"`
	TakerGets         interface{}   `json:"TakerGets"`
	TakerPays         interface{}   `json:"TakerPays"`
	DomainID          string        `json:"DomainID,omitempty"`
	AdditionalBooks   []interface{} `json:"AdditionalBooks,omitempty"`
	Index             string        `json:"index"`
	Quality           string        `json:"quality"`
	OwnerFunds        string        `json:"owner_funds,omitempty"`
	TakerGetsFunded   interface{}   `json:"taker_gets_funded,omitempty"`
	TakerPaysFunded   interface{}   `json:"taker_pays_funded,omitempty"`
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
	offer  *state.LedgerOffer
	key    [32]byte
	dirKey uint64 // low 64 bits of sfBookDirectory; monotonic with quality.
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

	// Funded-amount computation must happen in quality order so per-owner
	// running balances stay consistent with rippled. Collect, then sort, then
	// compute. Offers with a zero directory key are unreachable from a valid
	// book directory; skip them so the sort key stays total.
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
		dirKey := binary.BigEndian.Uint64(offer.BookDirectory[24:])
		if dirKey == 0 {
			return true
		}
		raws = append(raws, rawOffer{offer: offer, key: key, dirKey: dirKey})
		return true
	})

	// The directory key is (exponent+100)<<56 | mantissa with a normalized
	// mantissa in [10^15, 10^16-1], so uint64 ordering is monotonic with
	// quality. Ties fall back to SHAMap-key order; rippled iterates the
	// directory tree in insertion order, so attribution of firstOwnerOffer
	// across ties may diverge.
	sort.SliceStable(raws, func(i, j int) bool {
		return raws[i].dirKey < raws[j].dirKey
	})

	if limit > 0 && uint32(len(raws)) > limit {
		raws = raws[:limit]
	}

	getsIssuer := takerGets.Issuer
	bGlobalFreeze := tx.IsGlobalFrozen(targetLedger, takerGets.Issuer) ||
		tx.IsGlobalFrozen(targetLedger, takerPays.Issuer)
	rate := tx.TransferRateParity
	if !takerGets.IsNative() {
		rate = tx.GetTransferRate(targetLedger, getsIssuer)
	}
	_, reserveBase, reserveIncrement := readFeesFromLedger(targetLedger)

	// balances tracks each owner's remaining funds across the iteration.
	// Presence in the map mirrors rippled's umBalanceEntry lookup at
	// NetworkOPs.cpp:4530-4537 — once an owner is in the map, subsequent
	// offers from that owner in the *default* branch suppress owner_funds.
	balances := make(map[string]tx.Amount)

	offers := make([]BookOffer, 0, len(raws))
	for _, r := range raws {
		offer := r.offer
		bookOffer := BookOffer{
			Account:           offer.Account,
			BookDirectory:     hexUpper32(offer.BookDirectory),
			BookNode:          fmt.Sprintf("%x", offer.BookNode),
			Expiration:        offer.Expiration,
			Flags:             offer.Flags,
			LedgerEntryType:   "Offer",
			OwnerNode:         fmt.Sprintf("%x", offer.OwnerNode),
			PreviousTxnID:     hexUpper32(offer.PreviousTxnID),
			PreviousTxnLgrSeq: offer.PreviousTxnLgrSeq,
			Sequence:          offer.Sequence,
			Index:             hexUpper32(r.key),
			Quality:           qualityFromDirKey(r.dirKey),
		}
		if offer.DomainID != ([32]byte{}) {
			bookOffer.DomainID = hexUpper32(offer.DomainID)
		}
		// Hybrid offers (PermissionedDEX) carry a second book directory entry.
		// rippled stores this as sfAdditionalBooks: an STArray of inner sfBook
		// objects (rippled/src/xrpld/app/tx/detail/CreateOffer.cpp:562-571,
		// rippled/include/xrpl/protocol/detail/sfields.macro:380).
		if offer.AdditionalBookDirectory != ([32]byte{}) {
			bookOffer.AdditionalBooks = []interface{}{
				map[string]interface{}{
					"Book": map[string]interface{}{
						"BookDirectory": hexUpper32(offer.AdditionalBookDirectory),
						"BookNode":      fmt.Sprintf("%x", offer.AdditionalBookNode),
					},
				},
			}
		}
		bookOffer.TakerGets = amountToJSON(offer.TakerGets)
		bookOffer.TakerPays = amountToJSON(offer.TakerPays)

		// firstOwnerOffer is per-iteration in rippled (NetworkOPs.cpp:4514).
		// It only flips to false when the default branch finds an existing
		// entry in umBalance — the own-IOU and global-freeze branches never
		// touch it, so they emit owner_funds on every offer.
		firstOwnerOffer := true

		var ownerFunds tx.Amount
		ownerOwnsIssue := !takerGets.IsNative() && offer.Account == getsIssuer

		switch {
		case ownerOwnsIssue:
			// rippled NetworkOPs.cpp:4516-4521: selling issuer's own IOUs ⇒
			// fully funded. firstOwnerOffer stays true.
			ownerFunds = offer.TakerGets
		case bGlobalFreeze:
			// rippled NetworkOPs.cpp:4522-4527: global freeze ⇒ treat as
			// unfunded. firstOwnerOffer stays true.
			ownerFunds = zeroLike(takerGets)
		default:
			if prev, seen := balances[offer.Account]; seen {
				// rippled NetworkOPs.cpp:4530-4537: running-balance hit ⇒
				// reuse remaining balance and suppress owner_funds.
				ownerFunds = prev
				firstOwnerOffer = false
			} else {
				accountID, decErr := decodeAccountIDLocal(offer.Account)
				if decErr != nil {
					return nil, decErr
				}
				ownerFunds = tx.AccountFunds(targetLedger, accountID, takerGets, true, reserveBase, reserveIncrement)
			}
		}

		// Skip the transfer-fee adjustment when the taker is the issuer of
		// taker_gets, or when the offer owner is that issuer. Otherwise
		// saOwnerFundsLimit = ownerFunds / (rate / parityRate).
		offerRate := tx.TransferRateParity
		ownerFundsLimit := ownerFunds
		if rate != tx.TransferRateParity && !ownerOwnsIssue &&
			(taker == "" || taker != getsIssuer) {
			offerRate = rate
			ownerFundsLimit = ownerFunds.MulRatio(tx.TransferRateParity, rate, false)
		}

		var takerGetsFunded tx.Amount
		if ownerFundsLimit.Compare(offer.TakerGets) >= 0 {
			takerGetsFunded = offer.TakerGets
		} else {
			takerGetsFunded = ownerFundsLimit
			bookOffer.TakerGetsFunded = amountToJSON(takerGetsFunded)

			// rippled: saTakerPaysFunded = min(saTakerPays,
			// multiply(saTakerGetsFunded, saDirRate, saTakerPays.issue())).
			// saDirRate equals takerPays/takerGets by construction, so the
			// typed equivalent is saTakerPays * (takerGetsFunded / takerGets).
			ratio := takerGetsFunded.Div(offer.TakerGets, false)
			fundedPays := offer.TakerPays.Mul(ratio, false)
			if fundedPays.Compare(offer.TakerPays) > 0 {
				fundedPays = offer.TakerPays
			}
			bookOffer.TakerPaysFunded = amountToJSON(fundedPays)
		}

		// rippled NetworkOPs.cpp:4596-4601: umBalance updates unconditionally.
		// saOwnerPays = saTakerGetsFunded when rate == parity, else
		// min(saOwnerFunds, multiply(saTakerGetsFunded, offerRate)).
		ownerPays := takerGetsFunded
		if offerRate != tx.TransferRateParity {
			scaled := takerGetsFunded.MulRatio(offerRate, tx.TransferRateParity, false)
			if scaled.Compare(ownerFunds) > 0 {
				ownerPays = ownerFunds
			} else {
				ownerPays = scaled
			}
		}
		remaining, subErr := ownerFunds.Sub(ownerPays)
		if subErr != nil || remaining.IsNegative() {
			remaining = zeroLike(takerGets)
		}
		balances[offer.Account] = remaining

		if firstOwnerOffer {
			bookOffer.OwnerFunds = ownerFunds.Value()
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

// hexUpper32 matches rippled's uint256 JSON emit (uint256::to_string).
func hexUpper32(b [32]byte) string {
	return strings.ToUpper(hex.EncodeToString(b[:]))
}

// qualityFromDirKey formats an offer's directory key as the STAmount text
// rippled emits via saDirRate.getText() (NetworkOPs.cpp:4493,4605). The mantissa
// (low 56 bits) is already normalized to [10^15, 10^16-1] and the exponent is
// biased by +100, so passing them through NewIOUAmountValue is a no-op.
func qualityFromDirKey(q uint64) string {
	mantissa := int64(q & 0x00FFFFFFFFFFFFFF)
	exponent := int(q>>56) - 100
	return tx.NewIssuedAmount(mantissa, exponent, "", "").Value()
}

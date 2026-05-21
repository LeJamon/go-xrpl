package service

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/ledger/entry"
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

type BookOffersResult struct {
	LedgerIndex uint32      `json:"ledger_index"`
	LedgerHash  [32]byte    `json:"ledger_hash"`
	Offers      []BookOffer `json:"offers"`
	Validated   bool        `json:"validated"`
}

// errStopBookWalk is a sentinel used to short-circuit DirForEach when the
// requested limit has been reached.
var errStopBookWalk = errors.New("stop book walk")

// GetBookOffers retrieves offers from an order book and computes
// owner_funds / taker_gets_funded / taker_pays_funded for each one, mirroring
// rippled NetworkOPsImp::getBookPage (NetworkOPs.cpp).
//
// The walk mirrors rippled's directory traversal (view.succ → cdirFirst /
// cdirNext): we hash the book base from (takerPays, takerGets, [domain]) and
// step through the SHAMap to find each successive quality tier. Within a tier,
// offers are visited in their stored directory order, so attribution of
// firstOwnerOffer across equal-quality offers matches rippled.
//
// The taker argument is optional; when it matches the issuer of takerGets,
// rippled's "Not taking offers of own IOUs" branch suppresses the transfer
// fee adjustment. The domainHex argument is the optional permissioned-domain
// uint256 hex string; passing it walks the domain-scoped book base instead of
// the open book.
func (s *Service) GetBookOffers(ctx context.Context, takerGets, takerPays tx.Amount, taker, domainHex, ledgerIndex string, limit uint32) (*BookOffersResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	targetLedger, validated, err := s.getLedgerForQuery(ledgerIndex)
	if err != nil {
		return nil, err
	}

	bookBase, err := computeBookBase(takerPays, takerGets, domainHex)
	if err != nil {
		return nil, err
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

	offers := make([]BookOffer, 0)

	uTipIndex := bookBase
	for limit == 0 || uint32(len(offers)) < limit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		nextKey, _, ok, serr := targetLedger.Succ(uTipIndex)
		if serr != nil {
			return nil, serr
		}
		if !ok {
			break
		}
		// Once the high-24-byte book prefix changes we have left the book —
		// mirrors rippled's `succ(uTipIndex, uBookEnd)` upper bound.
		if !samePrefix24(nextKey, bookBase) {
			break
		}
		uTipIndex = nextKey
		dirQuality := binary.BigEndian.Uint64(nextKey[24:])
		if dirQuality == 0 {
			continue
		}

		walkErr := state.DirForEach(
			targetLedger,
			keylet.Keylet{Type: entry.TypeDirectoryNode, Key: nextKey},
			func(offerKey [32]byte) error {
				if limit > 0 && uint32(len(offers)) >= limit {
					return errStopBookWalk
				}
				offerData, rerr := targetLedger.Read(keylet.Keylet{Type: entry.TypeOffer, Key: offerKey})
				if rerr != nil || offerData == nil {
					return nil
				}
				offer, perr := state.ParseLedgerOfferFromBytes(offerData)
				if perr != nil {
					return nil
				}
				bookOffer, berr := s.buildBookOffer(
					targetLedger, offer, offerKey, dirQuality,
					takerGets, taker, getsIssuer, rate, bGlobalFreeze,
					reserveBase, reserveIncrement, balances,
				)
				if berr != nil {
					return berr
				}
				offers = append(offers, bookOffer)
				return nil
			},
		)
		if walkErr != nil && !errors.Is(walkErr, errStopBookWalk) {
			return nil, walkErr
		}
		if errors.Is(walkErr, errStopBookWalk) {
			break
		}
	}

	return &BookOffersResult{
		LedgerIndex: targetLedger.Sequence(),
		LedgerHash:  targetLedger.Hash(),
		Offers:      offers,
		Validated:   validated,
	}, nil
}

// buildBookOffer is the per-offer payload + funded-amount computation,
// extracted from the directory walk so the loop body stays readable. It
// updates the per-owner running balance map in place, matching rippled's
// unconditional `umBalance[uOfferOwnerID] = saOwnerFunds - saOwnerPays`
// at NetworkOPs.cpp:4601.
func (s *Service) buildBookOffer(
	view *ledger.Ledger,
	offer *state.LedgerOffer,
	key [32]byte,
	dirQuality uint64,
	takerGets tx.Amount,
	taker, getsIssuer string,
	rate uint32,
	bGlobalFreeze bool,
	reserveBase, reserveIncrement uint64,
	balances map[string]tx.Amount,
) (BookOffer, error) {
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
		Index:             hexUpper32(key),
		Quality:           qualityFromDirKey(dirQuality),
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
				return BookOffer{}, decErr
			}
			ownerFunds = tx.AccountFunds(view, accountID, takerGets, true, reserveBase, reserveIncrement)
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
	// min(saOwnerFunds, multiply(saTakerGetsFunded, offerRate)). The
	// subtraction is mathematically ≥ 0 because saOwnerPays ≤ saOwnerFunds
	// holds in both branches; surface a programming error if it isn't.
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
	if subErr != nil {
		return BookOffer{}, fmt.Errorf("book_offers running balance: %w", subErr)
	}
	balances[offer.Account] = remaining

	if firstOwnerOffer {
		bookOffer.OwnerFunds = ownerFunds.Value()
	}
	return bookOffer, nil
}

// computeBookBase returns the high-24-byte book directory base for the given
// taker_pays / taker_gets / optional domain, matching rippled's getBookBase
// (Indexes.cpp). The returned key has the low 8 bytes zeroed.
func computeBookBase(takerPays, takerGets tx.Amount, domainHex string) ([32]byte, error) {
	payCurr := state.GetCurrencyBytes(takerPays.Currency)
	payIssuer := state.GetIssuerBytes(takerPays.Issuer)
	getsCurr := state.GetCurrencyBytes(takerGets.Currency)
	getsIssuer := state.GetIssuerBytes(takerGets.Issuer)

	if domainHex == "" {
		return keylet.BookDir(payCurr, payIssuer, getsCurr, getsIssuer).Key, nil
	}
	var domainID [32]byte
	if err := decodeHex32Into(domainHex, &domainID); err != nil {
		return [32]byte{}, err
	}
	return keylet.BookDirWithDomain(payCurr, payIssuer, getsCurr, getsIssuer, domainID).Key, nil
}

func decodeHex32Into(s string, out *[32]byte) error {
	if len(s) != 64 {
		return fmt.Errorf("expected 64 hex chars, got %d", len(s))
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return err
	}
	copy(out[:], b)
	return nil
}

// samePrefix24 reports whether a and b agree on their first 24 bytes — the
// book-base portion of a directory key.
func samePrefix24(a, b [32]byte) bool {
	for i := 0; i < 24; i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

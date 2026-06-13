package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/shamap"
)

// BookOffer represents an offer in an order book. The wire shape mirrors
// rippled's SLE-derived JSON (NetworkOPsImp::getBookPage uses
// sleOffer->getJson(JsonOptions::none)).
type BookOffer struct {
	Account           string           `json:"Account"`
	BookDirectory     string           `json:"BookDirectory"`
	BookNode          string           `json:"BookNode"`
	Expiration        uint32           `json:"Expiration,omitempty"`
	Flags             uint32           `json:"Flags"`
	LedgerEntryType   string           `json:"LedgerEntryType"`
	OwnerNode         string           `json:"OwnerNode"`
	PreviousTxnID     string           `json:"PreviousTxnID"`
	PreviousTxnLgrSeq uint32           `json:"PreviousTxnLgrSeq"`
	Sequence          uint32           `json:"Sequence"`
	TakerGets         any              `json:"TakerGets"`
	TakerPays         any              `json:"TakerPays"`
	DomainID          string           `json:"DomainID,omitempty"`
	AdditionalBooks   []map[string]any `json:"AdditionalBooks,omitempty"`
	Index             string           `json:"index"`
	Quality           string           `json:"quality"`
	OwnerFunds        string           `json:"owner_funds,omitempty"`
	TakerGetsFunded   any              `json:"taker_gets_funded,omitempty"`
	TakerPaysFunded   any              `json:"taker_pays_funded,omitempty"`
	// Proof carries the SHAMap state-tree proof (leaf-to-root, upper-case
	// hex) for this offer's key when GetBookOffers is called with
	// withProofs=true. See the GetBookOffers doc for the rippled divergence.
	Proof []string `json:"proof,omitempty"`
}

type BookOffersResult struct {
	LedgerIndex uint32      `json:"ledger_index"`
	LedgerHash  [32]byte    `json:"ledger_hash"`
	Offers      []BookOffer `json:"offers"`
	Validated   bool        `json:"validated"`
	// Marker is the resume token returned when the book has more offers than
	// the request's limit. It is the 64-char hex `index` of the last offer
	// emitted in this page; passing it back in a follow-up request continues
	// the walk after that offer. go-xrpl extension — rippled accepts a marker
	// parameter (BookOffers.cpp:201-214) but its NetworkOPsImp::getBookPage
	// implementation never reads or emits one (NetworkOPs.cpp:4627 comments
	// out the response field).
	//
	// Callers paginating across multiple pages should pin the ledger via
	// ledger_index or ledger_hash on every follow-up call. The default
	// "current" ledger advances between calls, so an offer indexed by the
	// marker can be consumed by a concurrent Payment/OfferCreate; the next
	// call then returns ErrStaleMarker. Pinning the ledger guarantees the
	// marker resolves for the duration of the walk.
	Marker string `json:"marker,omitempty"`
}

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
//
// withProofs requests per-offer SHAMap proofs (leaf-to-root, hex-encoded) on
// each BookOffer.Proof. Rippled accepts proof=true and forwards bProof to
// getBookPage but never emits the proof (NetworkOPs.cpp:4430-4628 declares
// the parameter then ignores it); goxrpld emits real proofs so clients can
// independently verify each offer against the parent ledger's account_hash.
//
// marker enables paginated walks: pass the empty string for an initial
// request, then echo back the result.Marker on every follow-up call to
// resume after the last-emitted offer.
func (s *Service) GetBookOffers(ctx context.Context, takerGets, takerPays tx.Amount, taker, domainHex, ledgerIndex string, limit uint32, marker string, withProofs bool) (*BookOffersResult, error) {
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

	// Resolve the marker (if any) to a (resumeDir, skipUntil) pair. The marker
	// is the 64-hex `index` of the last offer returned by the previous page;
	// we look up that offer's BookDirectory and continue from there, skipping
	// entries within that directory until we pass the marker key.
	//
	// Two failure modes are surfaced separately so handlers can map them to
	// distinct rippled error codes (mirrors AccountOffers.cpp:107-132):
	//   - syntactically bad / wrong-book → ErrInvalidMarker → invalid_field_error
	//   - well-formed but referent gone → ErrStaleMarker → rpcINVALID_PARAMS
	var skipUntil [32]byte
	var resumeDir [32]byte
	hasMarker := false
	if marker != "" {
		var markerKey [32]byte
		if err := decodeHex32Into(marker, &markerKey); err != nil {
			return nil, fmt.Errorf("%w: %v", svcerr.ErrInvalidMarker, err)
		}
		offerData, rerr := targetLedger.Read(keylet.Keylet{Type: entry.TypeOffer, Key: markerKey})
		if rerr != nil || offerData == nil {
			return nil, svcerr.ErrStaleMarker
		}
		markerOffer, perr := state.ParseLedgerOfferFromBytes(offerData)
		if perr != nil {
			return nil, svcerr.ErrInvalidMarker
		}
		if !samePrefix24(markerOffer.BookDirectory, bookBase) {
			return nil, svcerr.ErrInvalidMarker
		}
		resumeDir = markerOffer.BookDirectory
		skipUntil = markerKey
		hasMarker = true
	}

	// Snapshot once so every per-offer proof verifies against the same account_hash.
	var stateSnap *shamap.SHAMap
	if withProofs {
		stateSnap, err = targetLedger.StateMapSnapshot()
		if err != nil {
			return nil, fmt.Errorf("book_offers proof snapshot: %w", err)
		}
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
	//
	// On a resumed walk we start with an empty map. rippled never paginates
	// book_offers (NetworkOPsImp::getBookPage ignores its marker arg), so
	// there is no rippled precedent for the cross-page case; this is a
	// go-xrpl-specific choice. Consequence: the first offer of each owner
	// in a new page re-emits owner_funds, which is harmless but means
	// callers concatenating pages will see owner_funds repeated.
	balances := make(map[string]tx.Amount)

	offers := make([]BookOffer, 0)
	var lastOfferKey [32]byte
	hitLimit := false

	// remaining is the rippled-style iLimit counter — decremented once per
	// directory entry visited regardless of whether the offer SLE could be
	// read. Mirrors `while (!bDone && iLimit-- > 0)` at NetworkOPs.cpp:4471
	// and the `if (sleOffer) { ... } else { warn }` branch at :4508-4613 that
	// still falls through to the next iteration.
	remaining := limit
	uTipIndex := bookBase
	skipping := hasMarker
	// resumePending consumes the marker's resumeDir on the first loop
	// iteration; subsequent iterations advance via targetLedger.Succ.
	resumePending := hasMarker
	// The loop continues until Succ leaves the book OR DirForEach signals
	// errStopBookWalk (which it raises the first time `remaining == 0` is
	// observed). The peek-after-limit is what lets us set `hitLimit` and
	// emit a marker rather than silently truncating the page.
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var nextKey [32]byte
		if resumePending {
			nextKey = resumeDir
			resumePending = false
		} else {
			nk, _, ok, serr := targetLedger.Succ(uTipIndex)
			if serr != nil {
				return nil, serr
			}
			if !ok {
				break
			}
			nextKey = nk
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

		// saDirRate is rippled's amountFromQuality(getQuality(uTipIndex))
		// at NetworkOPs.cpp:4493 — one per quality tier, used both for
		// jvOf[jss::quality] and for multiplying saTakerGetsFunded.
		saDirRate := newDirRate(dirQuality, takerPays)

		walkErr := state.DirForEach(
			targetLedger,
			keylet.Keylet{Type: entry.TypeDirectoryNode, Key: nextKey},
			func(offerKey [32]byte) error {
				if skipping {
					if offerKey == skipUntil {
						skipping = false
					}
					return nil
				}
				if remaining == 0 {
					hitLimit = true
					return errStopBookWalk
				}
				remaining--
				offerData, rerr := targetLedger.Read(keylet.Keylet{Type: entry.TypeOffer, Key: offerKey})
				if rerr != nil || offerData == nil {
					return nil
				}
				offer, perr := state.ParseLedgerOfferFromBytes(offerData)
				if perr != nil {
					return nil
				}
				bookOffer, berr := s.buildBookOffer(
					targetLedger, offer, offerKey, dirQuality, saDirRate,
					takerGets, taker, getsIssuer, rate, bGlobalFreeze,
					reserveBase, reserveIncrement, balances,
				)
				if berr != nil {
					return berr
				}
				if stateSnap != nil {
					proofHex, perr := extractOfferProof(stateSnap, offerKey)
					if perr != nil {
						return perr
					}
					bookOffer.Proof = proofHex
				}
				offers = append(offers, bookOffer)
				lastOfferKey = offerKey
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

	// If the supplied marker pointed at an offer that no longer exists in
	// its directory (e.g. the offer was consumed between requests), we'll
	// have walked the page without ever clearing `skipping`. The marker
	// itself was well-formed and resolved to a real (now-deleted) offer —
	// surface this as ErrStaleMarker so the handler can map it to
	// rpcINVALID_PARAMS rather than the malformed-marker error.
	if hasMarker && skipping {
		return nil, svcerr.ErrStaleMarker
	}

	result := &BookOffersResult{
		LedgerIndex: targetLedger.Sequence(),
		LedgerHash:  targetLedger.Hash(),
		Offers:      offers,
		Validated:   validated,
	}
	// Emit the marker only when the limit was reached AND the page produced
	// at least one offer. limit=0 hits errStopBookWalk before any offer is
	// recorded; emitting formatHashHex(lastOfferKey) there would be 64 zeros,
	// which round-trips as a bad marker on the next call.
	if hitLimit && len(offers) > 0 {
		result.Marker = formatHashHex(lastOfferKey)
	}
	return result, nil
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
	saDirRate tx.Amount,
	takerGets tx.Amount,
	taker, getsIssuer string,
	rate uint32,
	bGlobalFreeze bool,
	reserveBase, reserveIncrement uint64,
	balances map[string]tx.Amount,
) (BookOffer, error) {
	bookOffer := BookOffer{
		Account:           offer.Account,
		BookDirectory:     formatHashHex(offer.BookDirectory),
		BookNode:          fmt.Sprintf("%x", offer.BookNode),
		Expiration:        offer.Expiration,
		Flags:             offer.Flags,
		LedgerEntryType:   "Offer",
		OwnerNode:         fmt.Sprintf("%x", offer.OwnerNode),
		PreviousTxnID:     formatHashHex(offer.PreviousTxnID),
		PreviousTxnLgrSeq: offer.PreviousTxnLgrSeq,
		Sequence:          offer.Sequence,
		Index:             formatHashHex(key),
		Quality:           qualityFromDirKey(dirQuality),
	}
	if offer.DomainID != ([32]byte{}) {
		bookOffer.DomainID = formatHashHex(offer.DomainID)
	}
	// Hybrid permissioned offers carry an AdditionalBooks array pointing at
	// the open book entry the offer is also placed in. Rippled emits this
	// via sleOffer->getJson(JsonOptions::none) at NetworkOPs.cpp:4559;
	// CreateOffer.cpp:562-571 constructs the inner STObject as
	// {Book: {BookDirectory, BookNode}}.
	if offer.AdditionalBookDirectory != ([32]byte{}) {
		bookOffer.AdditionalBooks = []map[string]any{
			{
				"Book": map[string]any{
					"BookDirectory": formatHashHex(offer.AdditionalBookDirectory),
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

	// rippled NetworkOPs.cpp:4565-4575: skip the transfer-fee adjustment
	// when the taker is the issuer of taker_gets, or when the offer owner
	// is that issuer (`uTakerID != book.out.account` and `book.out.account
	// != uOfferOwnerID`). Otherwise saOwnerFundsLimit = ownerFunds /
	// (rate / parityRate).
	offerRate := tx.TransferRateParity
	ownerFundsLimit := ownerFunds
	if rate != tx.TransferRateParity && !ownerOwnsIssue && taker != getsIssuer {
		offerRate = rate
		ownerFundsLimit = ownerFunds.MulRatio(tx.TransferRateParity, rate, false)
	}

	var takerGetsFunded tx.Amount
	if ownerFundsLimit.Compare(offer.TakerGets) >= 0 {
		takerGetsFunded = offer.TakerGets
	} else {
		takerGetsFunded = ownerFundsLimit
		bookOffer.TakerGetsFunded = amountToJSON(takerGetsFunded)

		// rippled NetworkOPs.cpp:4587-4593:
		//     saTakerPaysFunded = min(saTakerPays,
		//         multiply(saTakerGetsFunded, saDirRate, saTakerPays.issue()))
		// saDirRate is the directory-key-decoded quality; we use it
		// directly so partially-funded amounts round identically to
		// rippled rather than going via a freshly-computed takerGetsFunded
		// /takerGets ratio.
		fundedPays := multiplyByDirRate(takerGetsFunded, saDirRate, offer.TakerPays)
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

// computeBookBase returns the book directory base for the given
// taker_pays / taker_gets / optional domain, matching rippled's getBookBase
// (Indexes.cpp:114-138). rippled normalizes the low 8 bytes to zero via
// keylet::quality({ltDIR_NODE, index}, 0); keylet.BookDir returns the raw
// hash including its natural low-8 bytes, so we must zero them here so the
// returned key sorts strictly below every quality tier in the book.
func computeBookBase(takerPays, takerGets tx.Amount, domainHex string) ([32]byte, error) {
	payCurr := keylet.CurrencyBytes(takerPays.Currency)
	payIssuer := state.GetIssuerBytes(takerPays.Issuer)
	getsCurr := keylet.CurrencyBytes(takerGets.Currency)
	getsIssuer := state.GetIssuerBytes(takerGets.Issuer)

	var key [32]byte
	if domainHex == "" {
		key = keylet.BookDir(payCurr, payIssuer, getsCurr, getsIssuer).Key
	} else {
		var domainID [32]byte
		if err := decodeHex32Into(domainHex, &domainID); err != nil {
			return [32]byte{}, err
		}
		key = keylet.BookDirWithDomain(payCurr, payIssuer, getsCurr, getsIssuer, domainID).Key
	}
	for i := 24; i < 32; i++ {
		key[i] = 0
	}
	return key, nil
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

func samePrefix24(a, b [32]byte) bool {
	return bytes.Equal(a[:24], b[:24])
}

func amountToJSON(a tx.Amount) any {
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

// qualityFromDirKey formats an offer's directory key as the STAmount text
// rippled emits via saDirRate.getText() (NetworkOPs.cpp:4493,4605). The mantissa
// (low 56 bits) is already normalized to [10^15, 10^16-1] and the exponent is
// biased by +100, so passing them through NewIOUAmountValue is a no-op.
func qualityFromDirKey(q uint64) string {
	mantissa := int64(q & 0x00FFFFFFFFFFFFFF)
	exponent := int(q>>56) - 100
	return tx.NewIssuedAmount(mantissa, exponent, "", "").Value()
}

func dirRateMantissaExp(q uint64) (int64, int) {
	return int64(q & 0x00FFFFFFFFFFFFFF), int(q>>56) - 100
}

// newDirRate constructs saDirRate for the current quality tier. For IOU
// taker_pays we tag the rate with takerPays' currency/issuer so the eventual
// product carries the right issue (mirrors rippled `multiply(getsFunded,
// saDirRate, takerPays.issue())` where the result asset is takerPays'). For
// XRP taker_pays we keep the rate as a unitless IOU and convert the product
// to drops afterwards.
func newDirRate(q uint64, takerPays tx.Amount) tx.Amount {
	mantissa, exponent := dirRateMantissaExp(q)
	if takerPays.IsNative() {
		return tx.NewIssuedAmount(mantissa, exponent, "", "")
	}
	return tx.NewIssuedAmount(mantissa, exponent, takerPays.Currency, takerPays.Issuer)
}

// extractOfferProof returns the SHAMap proof for an offer key as a list of
// upper-case hex strings, ordered leaf-to-root. The returned slice is nil
// when the offer is absent from the supplied snapshot. For closed-ledger
// requests this can't happen — snapshot and walk both read an immutable
// ledger. The narrow case is an open-ledger request where a concurrent
// insert lands in `targetLedger` between the snapshot capture and the book
// walk: that one offer surfaces in the walk but not in the snapshot, so
// its proof is omitted (per `omitempty`) rather than mis-attributed to a
// different account_hash.
func extractOfferProof(snap *shamap.SHAMap, key [32]byte) ([]string, error) {
	proof, err := snap.GetProofPath(key)
	if err != nil {
		return nil, fmt.Errorf("offer proof %s: %w", formatHashHex(key), err)
	}
	if proof == nil || !proof.Found {
		return nil, nil
	}
	out := make([]string, len(proof.Path))
	for i, node := range proof.Path {
		out[i] = strings.ToUpper(hex.EncodeToString(node))
	}
	return out, nil
}

// multiplyByDirRate computes `multiply(getsFunded, saDirRate, takerPays.issue())`.
// `Amount.Mul` preserves the *left* operand's currency, so for IOU output we
// place saDirRate on the left (it already carries takerPays' issue). For XRP
// output the product is a unitless IOU which we collapse to drops — mirrors
// rippled's STAmount(asset, mantissa, exp) cast at STAmount.cpp:1404.
func multiplyByDirRate(getsFunded, saDirRate, takerPays tx.Amount) tx.Amount {
	product := saDirRate.Mul(getsFunded, false)
	if !takerPays.IsNative() {
		return product
	}
	m := product.Mantissa()
	e := product.Exponent()
	for e > 0 {
		m *= 10
		e--
	}
	for e < 0 {
		m /= 10
		e++
	}
	return tx.NewXRPAmount(m)
}

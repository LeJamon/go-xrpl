package pathfinder

import (
	"bytes"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/keylet"
)

// BookIndex provides an index of existing order books in the ledger.
// Rippled maintains an OrderBookDB; we build a lightweight equivalent
// by scanning offers and AMMs on demand.
type BookIndex struct {
	ledger tx.LedgerView
	// byTakerPays maps an Issue (what the taker pays) to a list of Issues
	// (what the taker gets) for all books that exist.
	byTakerPays map[payment.Issue][]payment.Issue
	built       bool
}

// NewBookIndex creates a BookIndex backed by the given ledger.
func NewBookIndex(ledger tx.LedgerView) *BookIndex {
	return &BookIndex{
		ledger:      ledger,
		byTakerPays: make(map[payment.Issue][]payment.Issue),
	}
}

// Build scans the ledger for all order books and AMMs and builds the book index.
// This is called lazily on first use.
func (bi *BookIndex) Build() {
	if bi.built {
		return
	}
	bi.built = true

	// Walk all ledger entries looking for offers and AMMs.
	// The recover() safety net ensures that if any ledger entry causes a panic
	// during parsing (e.g., IOUAmount overflow from malformed data), the entry
	// is skipped rather than crashing the entire RPC handler goroutine.
	seen := make(map[[2]payment.Issue]bool)
	addPair := func(takerPays, takerGets payment.Issue) {
		pair := [2]payment.Issue{takerPays, takerGets}
		if seen[pair] {
			return
		}
		seen[pair] = true
		bi.byTakerPays[takerPays] = append(bi.byTakerPays[takerPays], takerGets)
	}
	_ = bi.ledger.ForEach(func(key [32]byte, data []byte) (cont bool) {
		defer func() {
			if r := recover(); r != nil {
				// Skip this entry — malformed data should not crash the server
				cont = true
			}
		}()

		switch state.EntryType(data) {
		case "Offer":
			offer, err := state.ParseLedgerOffer(data)
			if err == nil {
				addPair(issueFromAmount(offer.TakerPays), issueFromAmount(offer.TakerGets))
			}
		case "AMM":
			pool, err := amm.ParseAMMData(data)
			if err == nil {
				asset1, valid1 := issueFromAsset(pool.Asset)
				asset2, valid2 := issueFromAsset(pool.Asset2)
				if valid1 && valid2 {
					addPair(asset1, asset2)
					addPair(asset2, asset1)
				}
			}
		}
		return true
	})
}

// GetBooksByTakerPays returns all Issues that are available as taker_gets
// for books where the taker_pays matches the given issue.
// Reference: rippled OrderBookDB::getBooksByTakerPays
func (bi *BookIndex) GetBooksByTakerPays(issue payment.Issue) []payment.Issue {
	bi.Build()
	return bi.byTakerPays[issue]
}

// IsBookToXRP returns true if there exists a book where taker_pays is the
// given issue and taker_gets is XRP.
// Reference: rippled OrderBookDB::isBookToXRP
func (bi *BookIndex) IsBookToXRP(issue payment.Issue) bool {
	bi.Build()
	for _, gets := range bi.byTakerPays[issue] {
		if gets.IsXRP() {
			return true
		}
	}
	return false
}

// BookExists checks whether a specific book directory exists in the ledger.
func (bi *BookIndex) BookExists(takerPays, takerGets payment.Issue) bool {
	base := keylet.BookBase(bookSide(takerPays), bookSide(takerGets), nil)
	next, _, ok, err := bi.ledger.Succ(base.Key)
	return err == nil && ok && bytes.Equal(next[:24], base.Key[:24])
}

func bookSide(issue payment.Issue) keylet.BookSide {
	if issue.IsMPT {
		return keylet.MPTSide(issue.MPTID)
	}
	return keylet.IssueSide(keylet.CurrencyBytes(issue.Currency), issue.Issuer)
}

// issueFromAmount extracts an Issue from a state.Amount.
func issueFromAmount(amt state.Amount) payment.Issue {
	return payment.GetIssue(amt)
}

func issueFromAsset(asset tx.Asset) (payment.Issue, bool) {
	if asset.IsMPT() {
		id, err := mptutil.DecodeID(asset.MPTIssuanceID)
		if err != nil {
			return payment.Issue{}, false
		}
		issue := payment.NewMPTIssue(id)
		return issue, issue.IsConsistent()
	}
	if asset.IsNative() {
		return payment.Issue{Currency: "XRP"}, true
	}
	issuer, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return payment.Issue{}, false
	}
	issue := payment.Issue{Currency: asset.Currency, Issuer: issuer}
	return issue, issue.IsConsistent()
}

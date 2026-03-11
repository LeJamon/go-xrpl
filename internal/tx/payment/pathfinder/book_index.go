package pathfinder

import (
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
	"github.com/LeJamon/goXRPLd/keylet"
)

// BookIndex provides an index of existing order books in the ledger.
// Rippled maintains an OrderBookDB; we build a lightweight equivalent
// by scanning the ledger for book directories on demand.
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

// Build scans the ledger for all offer entries and builds the book index.
// This is called lazily on first use.
func (bi *BookIndex) Build() {
	if bi.built {
		return
	}
	bi.built = true

	// Walk all ledger entries looking for offers
	seen := make(map[[2]payment.Issue]bool)
	_ = bi.ledger.ForEach(func(key [32]byte, data []byte) bool {
		offer, err := state.ParseLedgerOffer(data)
		if err != nil {
			return true // not an offer, continue
		}

		takerPays := issueFromAmount(offer.TakerPays)
		takerGets := issueFromAmount(offer.TakerGets)
		pair := [2]payment.Issue{takerPays, takerGets}
		if !seen[pair] {
			seen[pair] = true
			bi.byTakerPays[takerPays] = append(bi.byTakerPays[takerPays], takerGets)
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
	var paysCurrency, paysIssuer, getsCurrency, getsIssuer [20]byte
	paysCurrency = currencyTo20(takerPays.Currency)
	paysIssuer = takerPays.Issuer
	getsCurrency = currencyTo20(takerGets.Currency)
	getsIssuer = takerGets.Issuer
	k := keylet.BookDir(paysCurrency, paysIssuer, getsCurrency, getsIssuer)
	exists, _ := bi.ledger.Exists(k)
	return exists
}

// issueFromAmount extracts an Issue from a state.Amount.
func issueFromAmount(amt state.Amount) payment.Issue {
	if amt.IsNative() {
		return payment.Issue{Currency: "XRP"}
	}
	issuer, _ := state.DecodeAccountID(amt.Issuer)
	return payment.Issue{Currency: amt.Currency, Issuer: issuer}
}

// currencyTo20 converts a currency string to a 20-byte representation.
// XRP is all zeros; 3-char codes are left-padded in bytes 12-14.
func currencyTo20(currency string) [20]byte {
	var result [20]byte
	if currency == "XRP" || currency == "" {
		return result // all zeros for XRP
	}
	if len(currency) == 3 {
		// Standard currency code: placed at bytes 12-14
		result[12] = currency[0]
		result[13] = currency[1]
		result[14] = currency[2]
	}
	// For 40-char hex currencies, we'd decode hex here.
	// For now, standard 3-char codes cover the common cases.
	return result
}

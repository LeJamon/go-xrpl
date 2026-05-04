package ledgerstatefix

import (
	"bytes"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/nftoken"
	"github.com/LeJamon/goXRPLd/keylet"
)

// LedgerStateFix fix types
// Reference: rippled LedgerStateFix.h FixType enum
const (
	// LedgerFixTypeNFTokenPageLink repairs NFToken directory page links
	LedgerFixTypeNFTokenPageLink uint8 = 1
)

// LedgerStateFix errors
var (
	ErrLedgerFixInvalidType   = tx.Errorf(tx.TefINVALID_LEDGER_FIX_TYPE, "invalid LedgerFixType")
	ErrLedgerFixOwnerRequired = tx.Errorf(tx.TemINVALID, "Owner is required for nfTokenPageLink fix")
)

// LedgerStateFix is a system transaction to fix ledger state issues.
// Reference: rippled LedgerStateFix.cpp
type LedgerStateFix struct {
	tx.BaseTx

	// LedgerFixType identifies the type of fix (required)
	LedgerFixType uint8 `json:"LedgerFixType" xrpl:"LedgerFixType"`

	// Owner is the owner account (required for nfTokenPageLink fix)
	Owner string `json:"Owner,omitempty" xrpl:"Owner,omitempty"`
}

func NewLedgerStateFix(account string, fixType uint8) *LedgerStateFix {
	return &LedgerStateFix{
		BaseTx:        *tx.NewBaseTx(tx.TypeLedgerStateFix, account),
		LedgerFixType: fixType,
	}
}

func NewNFTokenPageLinkFix(account, owner string) *LedgerStateFix {
	return &LedgerStateFix{
		BaseTx:        *tx.NewBaseTx(tx.TypeLedgerStateFix, account),
		LedgerFixType: LedgerFixTypeNFTokenPageLink,
		Owner:         owner,
	}
}

func (l *LedgerStateFix) TxType() tx.Type {
	return tx.TypeLedgerStateFix
}

// Reference: rippled LedgerStateFix.cpp preflight()
func (l *LedgerStateFix) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}

	// Reference: rippled LedgerStateFix.cpp:36-37
	if err := tx.CheckFlags(l.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}

	// Validate LedgerFixType and required fields based on type
	// Reference: rippled LedgerStateFix.cpp:42-51
	switch l.LedgerFixType {
	case LedgerFixTypeNFTokenPageLink:
		// Reference: rippled LedgerStateFix.cpp:45-46
		if l.Owner == "" {
			return ErrLedgerFixOwnerRequired
		}
	default:
		// Reference: rippled LedgerStateFix.cpp:49-50
		return ErrLedgerFixInvalidType
	}

	return nil
}

func (l *LedgerStateFix) Flatten() (map[string]any, error) {
	m, err := tx.ReflectFlatten(l)
	if err != nil {
		return nil, err
	}
	// LedgerFixType is UInt16 in the binary codec but uint8 in Go.
	// Convert to int so the codec's UInt16.FromJSON() can handle it.
	if v, ok := m["LedgerFixType"]; ok {
		switch val := v.(type) {
		case uint8:
			m["LedgerFixType"] = int(val)
		}
	}
	return m, nil
}

func (l *LedgerStateFix) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureFixNFTokenPageLinks}
}

// Reference: rippled LedgerStateFix.cpp preclaim() + doApply()
func (l *LedgerStateFix) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("ledger state fix apply",
		"account", l.Account,
		"fixType", l.LedgerFixType,
		"owner", l.Owner,
	)

	switch l.LedgerFixType {
	case LedgerFixTypeNFTokenPageLink:
		// Preclaim: verify owner account exists
		// Reference: rippled LedgerStateFix.cpp preclaim() lines 65-80
		ownerID, err := state.DecodeAccountID(l.Owner)
		if err != nil {
			ctx.Log.Warn("ledger state fix: owner account decode failed",
				"owner", l.Owner,
			)
			return tx.TecOBJECT_NOT_FOUND
		}
		ownerAccountKey := keylet.Account(ownerID)
		exists, existsErr := ctx.View.Exists(ownerAccountKey)
		if existsErr != nil || !exists {
			ctx.Log.Warn("ledger state fix: owner account does not exist",
				"owner", l.Owner,
			)
			return tx.TecOBJECT_NOT_FOUND
		}

		// doApply: repair NFToken directory links
		// Reference: rippled LedgerStateFix.cpp doApply() lines 83-96
		if !repairNFTokenDirectoryLinks(ctx, ownerID) {
			ctx.Log.Warn("ledger state fix: no repairs needed",
				"owner", l.Owner,
			)
			return tx.TecFAILED_PROCESSING
		}
		ctx.Log.Debug("ledger state fix: nftoken page links repaired",
			"owner", l.Owner,
		)
		return tx.TesSUCCESS

	default:
		// preflight should have caught this
		ctx.Log.Error("ledger state fix: unknown fix type", "fixType", l.LedgerFixType)
		return tx.TecINTERNAL
	}
}

// repairNFTokenDirectoryLinks repairs the doubly-linked list of NFTokenPages
// for an account. Returns true if any repairs were made, false if there was
// nothing to repair or no pages exist.
// Reference: rippled NFTokenUtils.cpp repairNFTokenDirectoryLinks() lines 717-834
func repairNFTokenDirectoryLinks(ctx *tx.ApplyContext, owner [20]byte) bool {
	view := ctx.View
	didRepair := false

	last := keylet.NFTokenPageMax(owner)
	min := keylet.NFTokenPageMin(owner)

	// Find the first page: succ(nftpage_min.key, last.key.next())
	// In rippled, succ(start, upperBound) returns the first key in [start, upperBound).
	// Go's Succ(key) returns the first key > key. We start from one less than min
	// to find entries >= min. But since min has the owner prefix with all-zero low bits,
	// we actually want to find the first page key >= min. We use Succ with key that is
	// one less than min.key. However, a simpler approach: use Succ(key) where key is
	// one byte before min. But NFTokenPage keys are [owner_20 | low_12], so min is
	// [owner_20 | 0x000...000]. We need the first entry with key >= min and <= last.
	//
	// rippled: view.succ(keylet::nftpage_min(owner).key, last.key.next())
	// This finds the first key >= min.key and < last.key.next().
	// In Go: we can use Succ with a key that is one less than min to get >= min.
	// Compute min.key - 1:
	searchKey := decrementKey(min.Key)

	firstKey, firstData, found, err := view.Succ(searchKey)
	if err != nil || !found {
		return didRepair
	}

	// Check if the found key is within the owner's page range
	if bytes.Compare(firstKey[:], min.Key[:]) < 0 || bytes.Compare(firstKey[:], last.Key[:]) > 0 {
		return didRepair
	}

	// If no page found at this key, fall back to last page
	// rippled: .value_or(last.key) means use last.key if succ returns nothing
	pageKey := firstKey
	pageData := firstData

	// Parse the page
	page, parseErr := state.ParseNFTokenPage(pageData)
	if parseErr != nil {
		return didRepair
	}

	// Single page case: page key == last key
	// Reference: rippled lines 731-747
	if pageKey == last.Key {
		// There's only one page. It should have no links.
		var emptyHash [32]byte
		nextPresent := page.NextPageMin != emptyHash
		prevPresent := page.PreviousPageMin != emptyHash

		if nextPresent || prevPresent {
			didRepair = true
			ctx.Log.Debug("ledger state fix: clearing links on single page",
				"nextPresent", nextPresent,
				"prevPresent", prevPresent,
			)
			if prevPresent {
				page.PreviousPageMin = emptyHash
			}
			if nextPresent {
				page.NextPageMin = emptyHash
			}
			if serialized, serErr := nftoken.SerializeNFTokenPage(page); serErr == nil {
				pageKl := keylet.Keylet{Type: keylet.NFTokenPageMax(owner).Type, Key: pageKey}
				_ = view.Update(pageKl, serialized)
			}
		}
		return didRepair
	}

	// Multiple pages case.
	// First page should not contain a previous link.
	// Reference: rippled lines 749-757
	var emptyHash [32]byte
	if page.PreviousPageMin != emptyHash {
		didRepair = true
		ctx.Log.Debug("ledger state fix: clearing previous link on first page")
		page.PreviousPageMin = emptyHash
		if serialized, serErr := nftoken.SerializeNFTokenPage(page); serErr == nil {
			pageKl := keylet.Keylet{Type: last.Type, Key: pageKey}
			_ = view.Update(pageKl, serialized)
		}
	}

	// Walk pairs using succ
	// Reference: rippled lines 759-786
	var nextPage *state.NFTokenPageData
	var nextPageKey [32]byte
	foundNextPage := false

	for {
		// Find next page: succ(page.key.next(), last.key.next())
		// In Go: Succ(pageKey) returns first key > pageKey
		nKey, nData, nFound, nErr := view.Succ(pageKey)
		if nErr != nil || !nFound {
			break
		}
		// Check upper bound: key must be <= last.key
		if bytes.Compare(nKey[:], last.Key[:]) > 0 {
			break
		}

		nextPageKey = nKey
		nParsed, nParseErr := state.ParseNFTokenPage(nData)
		if nParseErr != nil {
			break
		}
		nextPage = nParsed
		foundNextPage = true

		// Verify page -> nextPage forward link
		// Reference: rippled lines 765-771
		if page.NextPageMin != nextPageKey {
			didRepair = true
			ctx.Log.Debug("ledger state fix: repairing forward link between pages")
			page.NextPageMin = nextPageKey
			if serialized, serErr := nftoken.SerializeNFTokenPage(page); serErr == nil {
				pageKl := keylet.Keylet{Type: last.Type, Key: pageKey}
				_ = view.Update(pageKl, serialized)
			}
		}

		// Verify nextPage -> page backward link
		// Reference: rippled lines 773-779
		if nextPage.PreviousPageMin != pageKey {
			didRepair = true
			ctx.Log.Debug("ledger state fix: repairing backward link between pages")
			nextPage.PreviousPageMin = pageKey
			if serialized, serErr := nftoken.SerializeNFTokenPage(nextPage); serErr == nil {
				nKl := keylet.Keylet{Type: last.Type, Key: nextPageKey}
				_ = view.Update(nKl, serialized)
			}
		}

		// If nextPage is the last page, break out for special handling
		// Reference: rippled lines 781-783
		if nextPageKey == last.Key {
			break
		}

		// Move forward
		page = nextPage
		pageKey = nextPageKey
	}

	// When we arrive here, nextPage should have the same index as last.
	// If not, we need to fix it by moving the current last page's contents
	// to the correct final position.
	// Reference: rippled lines 790-821
	if !foundNextPage || nextPageKey != last.Key {
		// page is the actual last page, but it doesn't have the expected final index.
		// Move its contents to a new page at the correct last.Key position.
		didRepair = true
		ctx.Log.Debug("ledger state fix: relocating last page to correct position")

		newLastPage := &state.NFTokenPageData{
			NFTokens: page.NFTokens,
		}

		// If page has a PreviousPageMin link, copy it and fix the previous page's
		// NextPageMin to point to the new last page.
		// Reference: rippled lines 806-818
		if page.PreviousPageMin != emptyHash {
			newLastPage.PreviousPageMin = page.PreviousPageMin

			// Fix up the NextPageMin link in the previous page
			prevPageKl := keylet.Keylet{Type: last.Type, Key: page.PreviousPageMin}
			prevData, prevErr := view.Read(prevPageKl)
			if prevErr != nil {
				return false
			}
			prevPage, prevParseErr := state.ParseNFTokenPage(prevData)
			if prevParseErr != nil {
				return false
			}
			prevPage.NextPageMin = last.Key
			if serialized, serErr := nftoken.SerializeNFTokenPage(prevPage); serErr == nil {
				_ = view.Update(prevPageKl, serialized)
			}
		}

		// Erase the old page and insert the new one at the correct position
		// Reference: rippled lines 819-821
		oldPageKl := keylet.Keylet{Type: last.Type, Key: pageKey}
		_ = view.Erase(oldPageKl)

		if serialized, serErr := nftoken.SerializeNFTokenPage(newLastPage); serErr == nil {
			_ = view.Insert(last, serialized)
		}

		return didRepair
	}

	// nextPage is the last page. It should not have a NextPageMin link.
	// Reference: rippled lines 824-833
	if nextPage != nil && nextPage.NextPageMin != emptyHash {
		didRepair = true
		ctx.Log.Debug("ledger state fix: clearing next link on last page")
		nextPage.NextPageMin = emptyHash
		if serialized, serErr := nftoken.SerializeNFTokenPage(nextPage); serErr == nil {
			nKl := keylet.Keylet{Type: last.Type, Key: nextPageKey}
			_ = view.Update(nKl, serialized)
		}
	}

	return didRepair
}

// decrementKey returns key - 1 (treating the 32-byte key as a big-endian integer).
// This is used to find entries >= a given key using Succ (which returns > key).
func decrementKey(key [32]byte) [32]byte {
	result := key
	for i := 31; i >= 0; i-- {
		if result[i] > 0 {
			result[i]--
			return result
		}
		result[i] = 0xFF
	}
	return result
}

// CalculateBaseFee returns the minimum fee for LedgerStateFix transactions.
// The fee required is one owner reserve (increment), just like AccountDelete.
// Reference: rippled LedgerStateFix.cpp calculateBaseFee() returns view.fees().increment
func (l *LedgerStateFix) CalculateBaseFee(_ tx.LedgerView, config tx.EngineConfig) uint64 {
	return config.ReserveIncrement
}

var _ tx.Appliable = (*LedgerStateFix)(nil)
var _ tx.CustomBaseFeeCalculator = (*LedgerStateFix)(nil)

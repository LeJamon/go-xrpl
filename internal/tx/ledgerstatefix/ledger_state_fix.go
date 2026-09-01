package ledgerstatefix

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	ledgercore "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// LedgerStateFix fix types
// Reference: rippled LedgerStateFix.h FixType enum (std::uint16_t)
const (
	// LedgerFixTypeNFTokenPageLink repairs NFToken directory page links
	LedgerFixTypeNFTokenPageLink uint16 = 1
	// LedgerFixTypeBookExchangeRate repairs a book directory root whose
	// sfExchangeRate does not match the quality encoded in its key. Requires
	// fixCleanup3_2_0.
	LedgerFixTypeBookExchangeRate uint16 = 2
)

// LedgerStateFix errors
var (
	ErrLedgerFixInvalidType      = ter.Errorf(ter.TefINVALID_LEDGER_FIX_TYPE, "invalid LedgerFixType")
	ErrLedgerFixOwnerRequired    = ter.Errorf(ter.TemINVALID, "Owner is required for nfTokenPageLink fix")
	ErrLedgerFixUnexpectedField  = ter.Errorf(ter.TemINVALID, "unexpected field for LedgerFixType")
	ErrLedgerFixBookDirRequired  = ter.Errorf(ter.TemINVALID, "BookDirectory is required for bookExchangeRate fix")
	ErrLedgerFixBookExchDisabled = ter.Errorf(ter.TemDISABLED, "bookExchangeRate fix requires fixCleanup3_2_0")
)

// LedgerStateFix is a system transaction to fix ledger state issues.
// Reference: rippled LedgerStateFix.cpp
type LedgerStateFix struct {
	tx.BaseTx

	// LedgerFixType identifies the type of fix (required). sfLedgerFixType is a
	// UINT16 on the wire, so wire values above 255 must still parse and reach the
	// default preflight arm (tefINVALID_LEDGER_FIX_TYPE) rather than failing to
	// decode.
	LedgerFixType uint16 `json:"LedgerFixType" xrpl:"LedgerFixType"`

	// Owner is the owner account (required for nfTokenPageLink fix)
	Owner string `json:"Owner,omitempty" xrpl:"Owner,omitempty"`

	// BookDirectory is the book directory root key to repair (required for the
	// bookExchangeRate fix, and forbidden for any other fix type).
	BookDirectory *string `json:"BookDirectory,omitempty" xrpl:"BookDirectory,omitempty"`
}

func NewLedgerStateFix(account string, fixType uint16) *LedgerStateFix {
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

func NewBookExchangeRateFix(account, bookDirectory string) *LedgerStateFix {
	return &LedgerStateFix{
		BaseTx:        *tx.NewBaseTx(tx.TypeLedgerStateFix, account),
		LedgerFixType: LedgerFixTypeBookExchangeRate,
		BookDirectory: &bookDirectory,
	}
}

func (l *LedgerStateFix) TxType() tx.Type {
	return tx.TypeLedgerStateFix
}

// GetFlagsMask adopts the engine FlagsMasker seam. LedgerStateFix defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0
// (rippled does not override getFlagsMask for LedgerStateFix).
func (l *LedgerStateFix) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

// Reference: rippled LedgerStateFix.cpp preflight()
func (l *LedgerStateFix) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}

	// Rules-free fix-type dispatch. Each fix type allows exactly one fix-specific
	// field. The bookExchangeRate arm defers to PreflightRules, where the
	// amendment gate (temDISABLED) must precede its field-shape check to match
	// rippled's preflight order.
	switch l.LedgerFixType {
	case LedgerFixTypeNFTokenPageLink:
		if l.Owner == "" {
			return ErrLedgerFixOwnerRequired
		}
		if l.BookDirectory != nil {
			return ErrLedgerFixUnexpectedField
		}
	case LedgerFixTypeBookExchangeRate:
		// Amendment-gated: validated in PreflightRules.
	default:
		return ErrLedgerFixInvalidType
	}

	return nil
}

// PreflightRules carries the amendment-gated arm of rippled's LedgerStateFix
// preflight. The bookExchangeRate fix is rejected temDISABLED before the amendment
// activates — ahead of its field-shape checks, matching rippled's switch-then-
// field ordering. Reference: rippled LedgerStateFix.cpp preflight().
func (l *LedgerStateFix) PreflightRules(rules *amendment.Rules) error {
	if l.LedgerFixType != LedgerFixTypeBookExchangeRate {
		return nil
	}
	if !rules.Enabled(amendment.FeatureFixCleanup3_2_0) {
		return ErrLedgerFixBookExchDisabled
	}
	if l.BookDirectory == nil {
		return ErrLedgerFixBookDirRequired
	}
	if l.Owner != "" {
		return ErrLedgerFixUnexpectedField
	}
	return nil
}

func (l *LedgerStateFix) Flatten() (map[string]any, error) {
	m, err := tx.ReflectFlatten(l)
	if err != nil {
		return nil, err
	}
	// Convert to int so the codec's UInt16.FromJSON() can handle it.
	if v, ok := m["LedgerFixType"]; ok {
		switch val := v.(type) {
		case uint16:
			m["LedgerFixType"] = int(val)
		}
	}
	return m, nil
}

func (l *LedgerStateFix) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureFixNFTokenPageLinks}
}

// Preclaim runs LedgerStateFix's ledger-aware check for the nfTokenPageLink fix:
// the owner account must exist (tecOBJECT_NOT_FOUND). Extracting it from Apply
// makes it visible to the preclaim-only paths (TxQ admission, simulate), matching
// rippled where it lives in LedgerStateFix::preclaim. The bookExchangeRate arm
// keeps its checks in Apply, where they deliberately share the single directory
// Read with the mutation so it sees exactly the bytes the checks validated.
// Reference: rippled LedgerStateFix.cpp preclaim().
func (l *LedgerStateFix) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	if l.LedgerFixType != LedgerFixTypeNFTokenPageLink {
		return ter.TesSUCCESS
	}
	ownerID, err := state.DecodeAccountID(l.Owner)
	if err != nil {
		return ter.TecOBJECT_NOT_FOUND
	}
	exists, existsErr := view.Exists(keylet.Account(ownerID))
	if existsErr != nil || !exists {
		return ter.TecOBJECT_NOT_FOUND
	}
	return ter.TesSUCCESS
}

// Reference: rippled LedgerStateFix.cpp doApply()
func (l *LedgerStateFix) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("ledger state fix apply",
		"account", l.Account,
		"fixType", l.LedgerFixType,
		"owner", l.Owner,
	)

	switch l.LedgerFixType {
	case LedgerFixTypeNFTokenPageLink:
		ownerID, err := state.DecodeAccountID(l.Owner)
		if err != nil {
			return ter.TecOBJECT_NOT_FOUND
		}

		// doApply: repair NFToken directory links
		// Reference: rippled LedgerStateFix.cpp doApply() lines 83-96
		repaired, repairErr := repairNFTokenDirectoryLinks(ctx, ownerID)
		if repairErr != nil {
			if ctx.Log != nil {
				ctx.Log.Error("tefEXCEPTION",
					"op", "LedgerStateFix.repairNFTokenDirectoryLinks",
					"err", repairErr,
				)
			}
			return ter.TefEXCEPTION
		}
		if !repaired {
			ctx.Log.Warn("ledger state fix: no repairs needed",
				"owner", l.Owner,
			)
			return ter.TecFAILED_PROCESSING
		}
		ctx.Log.Debug("ledger state fix: nftoken page links repaired",
			"owner", l.Owner,
		)
		return ter.TesSUCCESS

	case LedgerFixTypeBookExchangeRate:
		return l.applyBookExchangeRate(ctx)

	default:
		// preflight should have caught this
		ctx.Log.Error("ledger state fix: unknown fix type", "fixType", l.LedgerFixType)
		return ter.TecINTERNAL
	}
}

// applyBookExchangeRate performs the bookExchangeRate fix: the book directory
// root's sfExchangeRate is rewritten to the quality encoded in the low 64 bits
// of its key. Preclaim and doApply share the single Read so the mutation sees
// exactly the bytes the checks validated.
// Reference: rippled LedgerStateFix.cpp preclaim() + doApply() BookExchangeRate.
func (l *LedgerStateFix) applyBookExchangeRate(ctx *tx.ApplyContext) ter.Result {
	if l.BookDirectory == nil {
		return ter.TecINTERNAL
	}
	dirKeyBytes, err := hex.DecodeString(*l.BookDirectory)
	if err != nil || len(dirKeyBytes) != 32 {
		return ter.TecINTERNAL
	}
	var dirKey [32]byte
	copy(dirKey[:], dirKeyBytes)

	kl := keylet.Keylet{Type: entry.TypeDirectoryNode, Key: dirKey}
	data, rerr := ctx.View.Read(kl)
	if rerr != nil || data == nil {
		return ter.TecOBJECT_NOT_FOUND
	}

	exchangeRate, hasER := directoryExchangeRate(data)
	if !hasER {
		// Not the first page of a book directory.
		return ter.TecNO_PERMISSION
	}

	quality := binary.BigEndian.Uint64(dirKey[24:])
	if quality == exchangeRate {
		// Already correct, nothing to fix.
		return ter.TecNO_PERMISSION
	}

	dir, perr := state.ParseDirectoryNode(data)
	if perr != nil {
		return ter.TecINTERNAL
	}
	dir.ExchangeRate = quality
	serialized, serr := state.SerializeDirectoryNode(dir, true)
	if serr != nil {
		return ter.TecINTERNAL
	}
	if uerr := ctx.View.Update(kl, serialized); uerr != nil {
		return ter.TecINTERNAL
	}
	return ter.TesSUCCESS
}

// directoryExchangeRate reads the sfExchangeRate (UInt64, field code 6) of a
// serialized DirectoryNode, reporting whether the field is present. A book
// directory root always carries it; owner directories and non-root pages do not.
func directoryExchangeRate(data []byte) (uint64, bool) {
	var value uint64
	var present bool
	_ = state.WalkFields(data, func(f state.Field) error {
		if f.TypeCode == 3 && f.FieldCode == 6 && len(f.Value) == 8 { // UInt64 ExchangeRate
			value = binary.BigEndian.Uint64(f.Value)
			present = true
			return errStopWalk
		}
		return nil
	})
	return value, present
}

// errStopWalk halts a WalkFields iteration once the target field is found.
var errStopWalk = errors.New("stop walk")

type nftPageSerializer func(*state.NFTokenPageData) ([]byte, error)

type nftPageMutation struct {
	key    keylet.Keylet
	data   []byte
	update bool
	erase  bool
}

func (m nftPageMutation) apply(view ledgercore.Writer) error {
	switch {
	case m.update:
		return view.Update(m.key, m.data)
	case m.erase:
		return view.Erase(m.key)
	default:
		return view.Insert(m.key, m.data)
	}
}

func repairNFTokenDirectoryLinks(ctx *tx.ApplyContext, owner [20]byte) (bool, error) {
	return repairNFTokenDirectoryLinksWithSerializer(ctx, owner, nftoken.SerializeNFTokenPage)
}

func repairNFTokenDirectoryLinksWithSerializer(
	ctx *tx.ApplyContext,
	owner [20]byte,
	serialize nftPageSerializer,
) (bool, error) {
	mutations, err := planNFTokenDirectoryLinkRepair(ctx, owner, serialize)
	if err != nil {
		return false, err
	}
	if len(mutations) == 0 {
		return false, nil
	}

	view, ok := ctx.View.(ledgercore.AtomicWriter)
	if !ok {
		return false, errors.New("ledger view does not support atomic writes")
	}
	if err := view.ApplyAtomically(func(staged ledgercore.Writer) error {
		for _, mutation := range mutations {
			if err := mutation.apply(staged); err != nil {
				return fmt.Errorf("persist NFToken page %x: %w", mutation.key.Key, err)
			}
		}
		return nil
	}); err != nil {
		return false, err
	}
	return true, nil
}

func planNFTokenDirectoryLinkRepair(
	ctx *tx.ApplyContext,
	owner [20]byte,
	serialize nftPageSerializer,
) ([]nftPageMutation, error) {
	view := ctx.View
	mutations := make([]nftPageMutation, 0)
	stagedUpdates := make(map[[32]byte][]byte)

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
	if err != nil {
		return nil, fmt.Errorf("find first NFToken page: %w", err)
	}
	if !found {
		return nil, nil
	}

	// Check if the found key is within the owner's page range
	if bytes.Compare(firstKey[:], min.Key[:]) < 0 || bytes.Compare(firstKey[:], last.Key[:]) > 0 {
		return nil, nil
	}

	// If no page found at this key, fall back to last page
	// rippled: .value_or(last.key) means use last.key if succ returns nothing
	pageKey := firstKey
	pageData := firstData

	// Parse the page
	page, parseErr := state.ParseNFTokenPage(pageData)
	if parseErr != nil {
		return nil, fmt.Errorf("parse NFToken page %x: %w", pageKey, parseErr)
	}
	stageUpdate := func(key [32]byte, page *state.NFTokenPageData) error {
		serialized, serializeErr := serialize(page)
		if serializeErr != nil {
			return fmt.Errorf("serialize NFToken page %x: %w", key, serializeErr)
		}
		pageKl := keylet.Keylet{Type: last.Type, Key: key}
		mutations = append(mutations, nftPageMutation{key: pageKl, data: serialized, update: true})
		stagedUpdates[key] = serialized
		return nil
	}

	// Single page case: page key == last key
	// Reference: rippled lines 731-747
	if pageKey == last.Key {
		// There's only one page. It should have no links.
		var emptyHash [32]byte
		nextPresent := page.NextPageMin != emptyHash
		prevPresent := page.PreviousPageMin != emptyHash

		if nextPresent || prevPresent {
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
			if err := stageUpdate(pageKey, page); err != nil {
				return nil, err
			}
		}
		return mutations, nil
	}

	// Multiple pages case.
	// First page should not contain a previous link.
	// Reference: rippled lines 749-757
	var emptyHash [32]byte
	if page.PreviousPageMin != emptyHash {
		ctx.Log.Debug("ledger state fix: clearing previous link on first page")
		page.PreviousPageMin = emptyHash
		if err := stageUpdate(pageKey, page); err != nil {
			return nil, err
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
		if nErr != nil {
			return nil, fmt.Errorf("find NFToken page after %x: %w", pageKey, nErr)
		}
		if !nFound {
			break
		}
		// Check upper bound: key must be <= last.key
		if bytes.Compare(nKey[:], last.Key[:]) > 0 {
			break
		}

		nextPageKey = nKey
		nParsed, nParseErr := state.ParseNFTokenPage(nData)
		if nParseErr != nil {
			return nil, fmt.Errorf("parse NFToken page %x: %w", nKey, nParseErr)
		}
		nextPage = nParsed
		foundNextPage = true

		// Verify page -> nextPage forward link
		// Reference: rippled lines 765-771
		if page.NextPageMin != nextPageKey {
			ctx.Log.Debug("ledger state fix: repairing forward link between pages")
			page.NextPageMin = nextPageKey
			if err := stageUpdate(pageKey, page); err != nil {
				return nil, err
			}
		}

		// Verify nextPage -> page backward link
		// Reference: rippled lines 773-779
		if nextPage.PreviousPageMin != pageKey {
			ctx.Log.Debug("ledger state fix: repairing backward link between pages")
			nextPage.PreviousPageMin = pageKey
			if err := stageUpdate(nextPageKey, nextPage); err != nil {
				return nil, err
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
				return nil, fmt.Errorf("read previous NFToken page %x: %w", prevPageKl.Key, prevErr)
			}
			if staged, ok := stagedUpdates[prevPageKl.Key]; ok {
				prevData = staged
			}
			prevPage, prevParseErr := state.ParseNFTokenPage(prevData)
			if prevParseErr != nil {
				return nil, fmt.Errorf("parse previous NFToken page %x: %w", prevPageKl.Key, prevParseErr)
			}
			prevPage.NextPageMin = last.Key
			if err := stageUpdate(prevPageKl.Key, prevPage); err != nil {
				return nil, err
			}
		}

		// Erase the old page and insert the new one at the correct position
		// Reference: rippled lines 819-821
		oldPageKl := keylet.Keylet{Type: last.Type, Key: pageKey}
		mutations = append(mutations, nftPageMutation{key: oldPageKl, erase: true})

		serialized, serializeErr := serialize(newLastPage)
		if serializeErr != nil {
			return nil, fmt.Errorf("serialize relocated NFToken page %x: %w", last.Key, serializeErr)
		}
		mutations = append(mutations, nftPageMutation{key: last, data: serialized})

		return mutations, nil
	}

	// nextPage is the last page. It should not have a NextPageMin link.
	// Reference: rippled lines 824-833
	if nextPage != nil && nextPage.NextPageMin != emptyHash {
		ctx.Log.Debug("ledger state fix: clearing next link on last page")
		nextPage.NextPageMin = emptyHash
		if err := stageUpdate(nextPageKey, nextPage); err != nil {
			return nil, err
		}
	}

	return mutations, nil
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

// CalculateBaseFee returns the minimum fee for LedgerStateFix transactions:
// one owner reserve increment, read from the live FeeSettings, just like
// AccountDelete.
func (l *LedgerStateFix) CalculateBaseFee(view tx.LedgerView, config tx.EngineConfig) uint64 {
	if view != nil {
		data, err := view.Read(keylet.Fees())
		if err == nil && data != nil {
			if fs, err := state.ParseFeeSettings(data); err == nil {
				return fs.GetReserveIncrement()
			}
		}
	}
	return config.ReserveIncrement
}

var _ tx.Appliable = (*LedgerStateFix)(nil)
var _ tx.CustomBaseFeeCalculator = (*LedgerStateFix)(nil)

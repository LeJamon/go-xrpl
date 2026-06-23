package state

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/keylet"
)

// DirNodeMaxPages is the maximum number of directory pages allowed
// without the fixDirectoryLimit amendment.
// Reference: rippled Protocol.h dirNodeMaxPages = 262144
const DirNodeMaxPages uint64 = 262144

// ErrDirFull is returned when a directory cannot accept more entries
// because the page limit has been reached.
var ErrDirFull = errors.New("directory full")

// DirectoryNode represents a directory ledger entry
type DirectoryNode struct {
	// Common fields
	Flags         uint32
	RootIndex     [32]byte
	Indexes       [][32]byte // List of object keys in this directory page
	IndexNext     uint64     // Next page index (0 if none)
	IndexPrevious uint64     // Previous page index (0 if none)

	// indexNextSet / indexPreviousSet track whether the link fields are
	// *present* on the page, independent of their value. rippled writes
	// sfIndexNext / sfIndexPrevious via setFieldU64, which keeps a field present
	// even at value 0: a directory that grew to several pages and then collapsed
	// back to a single root carries zero-valued links, whereas a directory that
	// was only ever one page omits them entirely. Tracking presence lets a
	// collapsed root re-serialize IndexNext=0 / IndexPrevious=0 instead of
	// dropping them. Dropping the zero links forked both the SLE (account_hash)
	// and the ModifiedNode metadata, whose present-field set is parsed back from
	// these same serialized bytes.
	indexNextSet     bool
	indexPreviousSet bool

	// Owner directory specific
	Owner [20]byte // Account that owns this directory (only for owner dirs)

	// Book directory specific (for offer books)
	TakerPaysCurrency [20]byte
	TakerPaysIssuer   [20]byte
	TakerGetsCurrency [20]byte
	TakerGetsIssuer   [20]byte
	ExchangeRate      uint64 // Quality encoded as uint64

	// Optional fields (per rippled ledger_entries.macro)
	NFTokenID [32]byte // For NFToken offer directories
	DomainID  [32]byte // For permissioned domain directories

	// Transaction threading fields. DirectoryNode is a threaded type once
	// the fixPreviousTxnID amendment is enabled, so these must survive a
	// parse→serialize round-trip. Dropping them caused an in-place
	// directory modify (dirRemove + dirInsert of the same key, e.g. a
	// SignerListSet replace) to differ from its pre-tx bytes only in the
	// threading fields; metadata then emitted a spurious ModifiedNode and
	// the threaded PreviousTxnID was bumped when rippled left it untouched
	// (rippled peeks the SLE and mutates sfIndexes in place, preserving
	// sfPreviousTxnID). Reference: ApplyStateTable.cpp:156-157.
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
}

// SetIndexNext sets the next-page link and marks it present, mirroring
// rippled's setFieldU64(sfIndexNext): the field then serializes even at value 0.
func (d *DirectoryNode) SetIndexNext(page uint64) {
	d.IndexNext = page
	d.indexNextSet = true
}

// SetIndexPrevious sets the previous-page link and marks it present, the
// counterpart to SetIndexNext.
func (d *DirectoryNode) SetIndexPrevious(page uint64) {
	d.IndexPrevious = page
	d.indexPreviousSet = true
}

// cMinValue is the minimum normalized mantissa value (10^15)
const cMinValue uint64 = 1000000000000000

// tenTo17 is 10^17
var tenTo17 = new(big.Int).Exp(big.NewInt(10), big.NewInt(17), nil)

// GetRate calculates the quality/exchange rate for an offer.
// This matches rippled's getRate(offerOut, offerIn), which returns
// divide(offerIn, offerOut) packed into a 64-bit quality code.
// Reference: rippled STAmount.cpp getRate / STAmount.h line 693-694:
//
//	"Rate: smaller is better, the taker wants the most out: in/out"
//
// Lower rate value = better for taker (they pay less per unit they get).
// Returns uint64 encoded as: (exponent+100) << 56 | mantissa, or 0 when the
// offer is "too good" (zero result) or the rate overflows ("very bad offer"),
// mirroring rippled's try/catch that returns 0 on any divide exception.
//
// The quotient is rounded by the shared divideIOU core, which is gated on
// fixUniversalNumber: round-to-nearest-ties-to-even on mainnet, truncation in
// the legacy regime. A blind truncation here diverged from rippled (and from
// Amount.Div) on roughly half of non-terminating ratios, shifting the
// ExchangeRate baked into BookDirectory keys.
func GetRate(offerOut, offerIn Amount) (rate uint64) {
	// rippled wraps getRate's divide in try/catch and returns 0 on overflow.
	// divideIOU normalizes through the IOU/Number path, which panics on
	// exponent overflow, so recover here to reproduce the "very bad offer" 0.
	defer func() {
		if recover() != nil {
			rate = 0
		}
	}()

	if offerOut.IsZero() || offerIn.IsZero() {
		return 0
	}

	numVal, numOffset := rateMantissa(offerIn)
	denVal, denOffset := rateMantissa(offerOut)
	if numVal == 0 || denVal == 0 {
		return 0
	}

	r := divideIOU(numVal, numOffset, denVal, denOffset, false, "", "")
	if r.IsZero() {
		return 0
	}
	iou := r.IOU()
	return uint64(iou.Exponent()+100)<<56 | uint64(iou.Mantissa())
}

// rateMantissa returns the unsigned mantissa and exponent of a (non-zero)
// amount for use in GetRate, lifting a native amount's drops into the IOU
// mantissa band [10^15, 10^16) exactly as rippled's divide() does.
func rateMantissa(a Amount) (uint64, int) {
	if a.IsNative() {
		v := uint64(a.Drops())
		offset := 0
		for v < cMinValue && v > 0 {
			v *= 10
			offset--
		}
		return v, offset
	}
	m := a.IOU().Mantissa()
	if m < 0 {
		m = -m
	}
	return uint64(m), a.IOU().Exponent()
}

// SerializeDirectoryNode serializes a DirectoryNode to binary format
func SerializeDirectoryNode(dir *DirectoryNode, isBookDir bool) ([]byte, error) {
	jsonObj := map[string]any{
		"LedgerEntryType": "DirectoryNode",
		"Flags":           dir.Flags,
		"RootIndex":       strings.ToUpper(hex.EncodeToString(dir.RootIndex[:])),
	}

	// sfIndexes is soeREQUIRED on ltDIR_NODE, so it is always serialized —
	// even when empty. dirRemove keeps an emptied root page with an empty
	// Vector256 present (field-ID 0113 + VL 00); omitting it diverges the SLE
	// state on keepRoot deletions.
	indexes := make([]string, len(dir.Indexes))
	for i, idx := range dir.Indexes {
		indexes[i] = strings.ToUpper(hex.EncodeToString(idx[:]))
	}
	jsonObj["Indexes"] = indexes

	// Emit the link fields when present or non-zero. A multi-page directory
	// that collapses back to a single root keeps zero-valued links present (see
	// DirectoryNode.indexNextSet); emitting only on non-zero would drop them and
	// fork the ledger.
	if dir.IndexNext != 0 || dir.indexNextSet {
		jsonObj["IndexNext"] = formatUint64Hex(dir.IndexNext)
	}
	if dir.IndexPrevious != 0 || dir.indexPreviousSet {
		jsonObj["IndexPrevious"] = formatUint64Hex(dir.IndexPrevious)
	}

	// Include Owner field if set
	if dir.Owner != [20]byte{} {
		ownerAddr, err := encodeAccountID(dir.Owner)
		if err == nil {
			jsonObj["Owner"] = ownerAddr
		}
	}

	// Include book directory fields if they exist
	// These fields may exist even on owner directory pages (they're stored in ledger state)
	hasBookFields := isBookDir || dir.ExchangeRate != 0 ||
		dir.TakerPaysCurrency != [20]byte{} || dir.TakerPaysIssuer != [20]byte{} ||
		dir.TakerGetsCurrency != [20]byte{} || dir.TakerGetsIssuer != [20]byte{}

	if hasBookFields {
		// Include all four currency/issuer fields
		jsonObj["TakerPaysCurrency"] = strings.ToUpper(hex.EncodeToString(dir.TakerPaysCurrency[:]))
		jsonObj["TakerPaysIssuer"] = strings.ToUpper(hex.EncodeToString(dir.TakerPaysIssuer[:]))
		jsonObj["TakerGetsCurrency"] = strings.ToUpper(hex.EncodeToString(dir.TakerGetsCurrency[:]))
		jsonObj["TakerGetsIssuer"] = strings.ToUpper(hex.EncodeToString(dir.TakerGetsIssuer[:]))
		if dir.ExchangeRate != 0 {
			jsonObj["ExchangeRate"] = formatUint64Hex(dir.ExchangeRate)
		}
	}

	// Add optional Hash256 fields if set
	var zeroHash [32]byte
	if dir.NFTokenID != zeroHash {
		jsonObj["NFTokenID"] = strings.ToUpper(hex.EncodeToString(dir.NFTokenID[:]))
	}
	if dir.DomainID != zeroHash {
		jsonObj["DomainID"] = strings.ToUpper(hex.EncodeToString(dir.DomainID[:]))
	}

	// Preserve threading fields across the round-trip (set by metadata
	// threading once fixPreviousTxnID is enabled). PreviousTxnLgrSeq is
	// only meaningful alongside PreviousTxnID, so gate both on the id.
	if dir.PreviousTxnID != zeroHash {
		jsonObj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(dir.PreviousTxnID[:]))
		jsonObj["PreviousTxnLgrSeq"] = dir.PreviousTxnLgrSeq
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, err
	}

	return hex.DecodeString(hexStr)
}

// ParseDirectoryNode parses a DirectoryNode from binary data
func ParseDirectoryNode(data []byte) (*DirectoryNode, error) {
	dir := &DirectoryNode{}

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stUInt32:
			switch f.FieldCode {
			case 2: // Flags
				dir.Flags = f.UInt32()
			case 5: // PreviousTxnLgrSeq
				dir.PreviousTxnLgrSeq = f.UInt32()
			}

		case stUInt64:
			switch f.FieldCode {
			case 1: // IndexNext
				dir.SetIndexNext(f.UInt64())
			case 2: // IndexPrevious
				dir.SetIndexPrevious(f.UInt64())
			case 6: // ExchangeRate
				dir.ExchangeRate = f.UInt64()
			}

		case stHash256:
			switch f.FieldCode {
			case 8: // RootIndex
				dir.RootIndex = f.Hash256()
			case 10: // NFTokenID
				dir.NFTokenID = f.Hash256()
			case 34: // DomainID
				dir.DomainID = f.Hash256()
			case 5: // PreviousTxnID
				dir.PreviousTxnID = f.Hash256()
			}

		case stHash160:
			switch f.FieldCode {
			case 1: // TakerPaysCurrency
				dir.TakerPaysCurrency = f.Hash160()
			case 2: // TakerPaysIssuer
				dir.TakerPaysIssuer = f.Hash160()
			case 3: // TakerGetsCurrency
				dir.TakerGetsCurrency = f.Hash160()
			case 4: // TakerGetsIssuer
				dir.TakerGetsIssuer = f.Hash160()
			}

		case stAccountID:
			if f.FieldCode == 2 { // Owner
				if id, ok := f.AccountID(); ok {
					dir.Owner = id
				}
			}

		case stVector256:
			if f.FieldCode == 1 { // Indexes
				idx, err := f.Vector256()
				if err != nil {
					return err
				}
				dir.Indexes = idx
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return dir, nil
}

// uint64ToBytes converts uint64 to big-endian bytes
func uint64ToBytes(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// DirInsertResult contains the result of a directory insert operation
type DirInsertResult struct {
	Page          uint64   // Page where the item was inserted
	Created       bool     // True if the directory was created
	Modified      bool     // True if an existing directory was modified
	DirKey        [32]byte // Key of the directory node that was modified/created
	PreviousState *DirectoryNode
	NewState      *DirectoryNode
	// For multi-page support:
	RootModified      bool           // True if root was modified (for IndexPrevious update)
	RootPrevState     *DirectoryNode // Previous state of root (if root was modified)
	RootNewState      *DirectoryNode // New state of root
	NewPageCreated    bool           // True if a new page was created
	NewPageKey        [32]byte       // Key of the new page created
	NewPageState      *DirectoryNode // State of the new page
	PrevPageModified  bool           // True if previous page was modified (IndexNext update)
	PrevPageKey       [32]byte       // Key of the previous page
	PrevPagePrevState *DirectoryNode // Previous state of prev page
	PrevPageNewState  *DirectoryNode // New state of prev page
}

// dirNodeMaxEntries is the maximum number of entries per directory page (matches rippled)
const dirNodeMaxEntries = 32

// DirInsert adds an item to a directory, creating the directory if needed.
// Returns the page number where the item was inserted.
// Follows rippled's dirAdd algorithm for multi-page directory support.
//
// preserveOrder mirrors rippled's ApplyView::dirAppend (true, ApplyView.h:
// 280-296 — book directories: append at end to preserve insertion order
// across consume-oldest-first offer matching) vs ApplyView::dirInsert
// (false, ApplyView.h:317-333 — owner/NFT/credential/etc directories:
// keep sfIndexes sorted within a page). Callers must choose the value
// matching the directory's semantic role; the previous implementation
// inferred this from taker-field presence on the page, which would
// silently flip on any future directory variant that set taker fields
// without being a book.
func DirInsert(view LedgerView, dirKey keylet.Keylet, itemKey [32]byte, preserveOrder bool, setupFunc func(*DirectoryNode)) (*DirInsertResult, error) {
	result := &DirInsertResult{
		DirKey: dirKey.Key,
	}

	// Check if root directory exists
	exists, err := view.Exists(dirKey)
	if err != nil {
		return nil, err
	}
	// preserveOrder implies a book directory (sfTakerPays/sfTakerGets
	// fields). Use it as the SerializeDirectoryNode is-book hint so the
	// page serializes with taker fields even when the setupFunc happens
	// to set them all to zero (the same outcome as the previous taker-
	// field heuristic, just driven by the explicit caller signal).
	isBookDir := preserveOrder

	if !exists {
		// No root exists - create it with the item
		dir := &DirectoryNode{
			RootIndex: dirKey.Key,
			Indexes:   [][32]byte{itemKey},
		}
		if setupFunc != nil {
			setupFunc(dir)
		}
		result.Created = true
		result.Page = 0
		result.NewState = dir
		result.DirKey = dirKey.Key

		// Serialize and store
		data, err := SerializeDirectoryNode(dir, isBookDir)
		if err != nil {
			return nil, err
		}
		if err := view.Insert(dirKey, data); err != nil {
			return nil, err
		}
		return result, nil
	}

	// Root exists - read it
	rootData, err := view.Read(dirKey)
	if err != nil {
		return nil, err
	}
	root, err := ParseDirectoryNode(rootData)
	if err != nil {
		return nil, err
	}

	// Get the last page number from root's IndexPrevious
	page := root.IndexPrevious
	node := root
	nodeKey := dirKey.Key

	// If page != 0, load that page as the node to insert into
	if page != 0 {
		pageKeylet := keylet.DirPage(dirKey.Key, page)
		nodeKey = pageKeylet.Key
		pageData, err := view.Read(pageKeylet)
		if err != nil {
			return nil, err
		}
		node, err = ParseDirectoryNode(pageData)
		if err != nil {
			return nil, err
		}
	}

	// Check if current page has space
	if len(node.Indexes) < dirNodeMaxEntries {
		// Has space - add item to current page
		prevNode := *node
		// Reference: rippled ApplyView.cpp dirAdd() lines 68-91.
		// Book directories preserve insertion order — offer matching
		// consumes oldest-first within a quality level. All other
		// directories keep their sfIndexes vector sorted within a page:
		// sort the existing entries (in case a legacy page is unsorted),
		// then std::lower_bound + insert the new key. Without this,
		// goxrpl's directory page bytes diverge from rippled's after
		// multiple inserts into the same page, producing different SLE
		// serializations and consensus forks.
		if preserveOrder {
			// rippled's dirAdd checks for a duplicate key in the preserveOrder
			// (book) branch too, raising LogicError on a double insertion rather
			// than silently corrupting the book. Reference: ApplyView.cpp:71-74.
			if slices.Contains(node.Indexes, itemKey) {
				return nil, fmt.Errorf("dirInsert: double insertion")
			}
			node.Indexes = append(node.Indexes, itemKey)
		} else {
			sortIndexes(node.Indexes)
			pos := sortedSearch(node.Indexes, itemKey)
			if pos < len(node.Indexes) && node.Indexes[pos] == itemKey {
				return nil, fmt.Errorf("dirInsert: double insertion")
			}
			node.Indexes = append(node.Indexes, [32]byte{})
			copy(node.Indexes[pos+1:], node.Indexes[pos:])
			node.Indexes[pos] = itemKey
		}

		result.Modified = true
		result.Page = page
		result.DirKey = nodeKey
		result.PreviousState = &prevNode
		result.NewState = node

		// Serialize and update
		data, err := SerializeDirectoryNode(node, isBookDir)
		if err != nil {
			return nil, err
		}
		if err := view.Update(keylet.Keylet{Type: dirKey.Type, Key: nodeKey}, data); err != nil {
			return nil, err
		}
		return result, nil
	}

	// Current page is full - need to create a new page
	newPage := page + 1

	// Check for page overflow (uint64 wraps to 0).
	// Reference: rippled ApplyView.cpp dirAdd() lines 107-113
	if newPage == 0 {
		return nil, ErrDirFull
	}
	// Without fixDirectoryLimit, enforce old page limit.
	// When rules are nil (no amendment config), also enforce the old limit.
	if r := view.Rules(); (r == nil || !r.Enabled(amendment.FeatureFixDirectoryLimit)) && newPage >= DirNodeMaxPages {
		return nil, ErrDirFull
	}

	newPageKeylet := keylet.DirPage(dirKey.Key, newPage)

	// Save previous states
	prevNode := *node
	prevRoot := *root

	// Update current node's IndexNext to point to new page
	node.SetIndexNext(newPage)

	// Update root's IndexPrevious to point to new page
	root.SetIndexPrevious(newPage)

	// Create new page
	newPageNode := &DirectoryNode{
		RootIndex: dirKey.Key,
		Indexes:   [][32]byte{itemKey},
	}
	// Set IndexPrevious on new page (unless it's page 1, whose previous is the
	// root: rippled leaves that link absent to save space).
	if newPage != 1 {
		newPageNode.SetIndexPrevious(newPage - 1)
	}
	// Copy book directory fields if applicable
	if setupFunc != nil {
		setupFunc(newPageNode)
	}

	// Store results
	result.Page = newPage
	result.DirKey = newPageKeylet.Key
	result.NewPageCreated = true
	result.NewPageKey = newPageKeylet.Key
	result.NewPageState = newPageNode

	// Track root modification
	result.RootModified = true
	result.RootPrevState = &prevRoot
	result.RootNewState = root

	// Track previous page modification (if not root)
	if page != 0 {
		result.PrevPageModified = true
		result.PrevPageKey = nodeKey
		result.PrevPagePrevState = &prevNode
		result.PrevPageNewState = node
	} else {
		// Previous page was root, already tracked above
		result.PrevPageModified = false
	}

	// Serialize and store all changes

	// 1. Update current page (node) with new IndexNext
	nodeData, err := SerializeDirectoryNode(node, isBookDir)
	if err != nil {
		return nil, err
	}
	if err := view.Update(keylet.Keylet{Type: dirKey.Type, Key: nodeKey}, nodeData); err != nil {
		return nil, err
	}

	// 2. Update root with new IndexPrevious (only if root != node)
	if page != 0 {
		rootData, err := SerializeDirectoryNode(root, isBookDir)
		if err != nil {
			return nil, err
		}
		if err := view.Update(dirKey, rootData); err != nil {
			return nil, err
		}
	}

	// 3. Insert new page
	newPageData, err := SerializeDirectoryNode(newPageNode, isBookDir)
	if err != nil {
		return nil, err
	}
	if err := view.Insert(newPageKeylet, newPageData); err != nil {
		return nil, err
	}

	return result, nil
}

// GetIssuerBytes converts an issuer address to 20-byte account ID
func GetIssuerBytes(issuer string) [20]byte {
	if issuer == "" {
		return [20]byte{} // All zeros for XRP
	}
	accountID, _ := DecodeAccountID(issuer)
	return accountID
}

// formatUint64Hex formats a uint64 as lowercase hex without leading zeros
func formatUint64Hex(v uint64) string {
	h := hex.EncodeToString(uint64ToBytes(v))
	// Trim leading zeros but keep at least one digit
	h = strings.TrimLeft(h, "0")
	if h == "" {
		h = "0"
	}
	return strings.ToLower(h)
}

// DirRemoveResult contains the result of a directory remove operation
type DirRemoveResult struct {
	Success       bool                    // True if the item was found and removed
	PageModified  bool                    // True if the page was modified but not deleted
	PageDeleted   bool                    // True if the page was deleted (became empty)
	RootDeleted   bool                    // True if the entire directory was deleted
	ModifiedNodes []DirRemoveModifiedNode // Nodes that were modified
	DeletedNodes  []DirRemoveDeletedNode  // Nodes that were deleted
}

// DirRemoveModifiedNode tracks a modified directory node
type DirRemoveModifiedNode struct {
	Key       [32]byte
	PrevState *DirectoryNode
	NewState  *DirectoryNode
}

// DirRemoveDeletedNode tracks a deleted directory node
type DirRemoveDeletedNode struct {
	Key        [32]byte
	FinalState *DirectoryNode
}

// dirRemove removes an item from a directory.
// Follows rippled's dirRemove algorithm for proper page cleanup.
// Parameters:
//   - directory: keylet for the directory (root)
//   - page: the page number where the item is located (from OwnerNode/BookNode field)
//   - key: the item key to remove
//   - keepRoot: if true, don't delete the root even if empty
func DirRemove(view LedgerView, directory keylet.Keylet, page uint64, itemKey [32]byte, keepRoot bool) (*DirRemoveResult, error) {
	result := &DirRemoveResult{
		ModifiedNodes: make([]DirRemoveModifiedNode, 0),
		DeletedNodes:  make([]DirRemoveDeletedNode, 0),
	}

	const rootPage uint64 = 0

	// Get the page where the item should be. Distinguish a real storage error
	// (propagate it) from a genuinely absent page (nil data → not found,
	// Success=false). *ledger.Ledger.Read returns (nil, nil) for a missing key,
	// so the nil-data check is required; without it ParseDirectoryNode(nil) would
	// surface a misleading codec error.
	pageKeylet := keylet.DirPage(directory.Key, page)
	pageData, err := view.Read(pageKeylet)
	if err != nil {
		return nil, err
	}
	if pageData == nil {
		return result, nil // Page not found, Success=false
	}
	node, err := ParseDirectoryNode(pageData)
	if err != nil {
		return nil, err
	}

	// Find and remove the item from Indexes
	found := false
	newIndexes := make([][32]byte, 0, len(node.Indexes))
	for _, idx := range node.Indexes {
		if idx == itemKey {
			found = true
		} else {
			newIndexes = append(newIndexes, idx)
		}
	}

	if !found {
		return result, nil // Item not found
	}

	result.Success = true
	prevNode := *node
	node.Indexes = newIndexes

	// If page still has entries, just update it
	if len(node.Indexes) > 0 {
		result.PageModified = true
		result.ModifiedNodes = append(result.ModifiedNodes, DirRemoveModifiedNode{
			Key:       pageKeylet.Key,
			PrevState: &prevNode,
			NewState:  node,
		})

		// Serialize and update
		isBookDir := node.TakerPaysCurrency != [20]byte{} || node.TakerGetsCurrency != [20]byte{}
		data, err := SerializeDirectoryNode(node, isBookDir)
		if err != nil {
			return nil, err
		}
		if err := view.Update(pageKeylet, data); err != nil {
			return nil, err
		}
		return result, nil
	}

	// Page is now empty - need to handle page deletion
	prevPage := node.IndexPrevious
	nextPage := node.IndexNext

	// Handle root page specially
	if page == rootPage {
		// Check for consistency
		if nextPage == page && prevPage != page {
			return nil, fmt.Errorf("directory chain: fwd link broken")
		}
		if prevPage == page && nextPage != page {
			return nil, fmt.Errorf("directory chain: rev link broken")
		}

		// Handle legacy empty trailing pages
		if nextPage == prevPage && nextPage != page {
			lastPageKeylet := keylet.DirPage(directory.Key, nextPage)
			lastPageData, err := view.Read(lastPageKeylet)
			if err != nil {
				return nil, fmt.Errorf("directory chain: fwd link broken")
			}
			lastPage, err := ParseDirectoryNode(lastPageData)
			if err != nil {
				return nil, err
			}

			if len(lastPage.Indexes) == 0 {
				// Update root's linked list
				node.SetIndexNext(rootPage)
				node.SetIndexPrevious(rootPage)

				// Track root modification
				result.ModifiedNodes = append(result.ModifiedNodes, DirRemoveModifiedNode{
					Key:       pageKeylet.Key,
					PrevState: &prevNode,
					NewState:  node,
				})

				// Track last page deletion
				result.DeletedNodes = append(result.DeletedNodes, DirRemoveDeletedNode{
					Key:        lastPageKeylet.Key,
					FinalState: lastPage,
				})

				// Serialize root update
				isBookDir := node.TakerPaysCurrency != [20]byte{} || node.TakerGetsCurrency != [20]byte{}
				data, err := SerializeDirectoryNode(node, isBookDir)
				if err != nil {
					return nil, err
				}
				if err := view.Update(pageKeylet, data); err != nil {
					return nil, err
				}

				// Erase last page
				if err := view.Erase(lastPageKeylet); err != nil {
					return nil, err
				}

				nextPage = rootPage
				prevPage = rootPage
			}
		}

		if keepRoot {
			// Just mark as modified if we changed it
			if prevNode.IndexNext != node.IndexNext || prevNode.IndexPrevious != node.IndexPrevious {
				// Already tracked above
			} else {
				// Track modification for removing the item
				result.PageModified = true
				result.ModifiedNodes = append(result.ModifiedNodes, DirRemoveModifiedNode{
					Key:       pageKeylet.Key,
					PrevState: &prevNode,
					NewState:  node,
				})

				isBookDir := node.TakerPaysCurrency != [20]byte{} || node.TakerGetsCurrency != [20]byte{}
				data, err := SerializeDirectoryNode(node, isBookDir)
				if err != nil {
					return nil, err
				}
				if err := view.Update(pageKeylet, data); err != nil {
					return nil, err
				}
			}
			return result, nil
		}

		// If no other pages, erase the root
		if nextPage == rootPage && prevPage == rootPage {
			result.PageDeleted = true
			result.RootDeleted = true
			result.DeletedNodes = append(result.DeletedNodes, DirRemoveDeletedNode{
				Key:        pageKeylet.Key,
				FinalState: &prevNode, // Use state before item removal
			})

			if err := view.Erase(pageKeylet); err != nil {
				return nil, err
			}
		} else {
			// Root not empty but we removed an item - just update
			result.PageModified = true
			result.ModifiedNodes = append(result.ModifiedNodes, DirRemoveModifiedNode{
				Key:       pageKeylet.Key,
				PrevState: &prevNode,
				NewState:  node,
			})

			isBookDir := node.TakerPaysCurrency != [20]byte{} || node.TakerGetsCurrency != [20]byte{}
			data, err := SerializeDirectoryNode(node, isBookDir)
			if err != nil {
				return nil, err
			}
			if err := view.Update(pageKeylet, data); err != nil {
				return nil, err
			}
		}

		return result, nil
	}

	// Non-root page - need to unlink from chain and delete

	// Consistency checks
	if nextPage == page {
		return nil, fmt.Errorf("directory chain: fwd link broken")
	}
	if prevPage == page {
		return nil, fmt.Errorf("directory chain: rev link broken")
	}

	// Get prev and next pages
	prevPageKeylet := keylet.DirPage(directory.Key, prevPage)
	prevPageData, err := view.Read(prevPageKeylet)
	if err != nil {
		return nil, fmt.Errorf("directory chain: fwd link broken")
	}
	prev, err := ParseDirectoryNode(prevPageData)
	if err != nil {
		return nil, err
	}
	prevPrev := *prev

	// When prevPage == nextPage the two neighbours are the same page (the
	// removed page is the only non-root page, so both links point at the
	// root). rippled's dirRemove peeks a single shared SLE here
	// (ApplyView.cpp dirRemove), so its IndexNext and IndexPrevious updates
	// both land on one object. Alias next to prev to match: reading the page
	// twice into separate structs and then skipping the duplicate write (as
	// the per-key guards below do) would drop the IndexPrevious update and
	// leave the root back-link pointing at the just-erased page.
	nextPageKeylet := keylet.DirPage(directory.Key, nextPage)
	var next *DirectoryNode
	var nextPrev DirectoryNode
	if nextPageKeylet.Key == prevPageKeylet.Key {
		next = prev
		nextPrev = prevPrev
	} else {
		nextPageData, err := view.Read(nextPageKeylet)
		if err != nil {
			return nil, fmt.Errorf("directory chain: rev link broken")
		}
		next, err = ParseDirectoryNode(nextPageData)
		if err != nil {
			return nil, err
		}
		nextPrev = *next
	}

	// Unlink: prev.IndexNext = nextPage
	prev.SetIndexNext(nextPage)
	// Unlink: next.IndexPrevious = prevPage
	next.SetIndexPrevious(prevPage)

	// Track prev modification
	result.ModifiedNodes = append(result.ModifiedNodes, DirRemoveModifiedNode{
		Key:       prevPageKeylet.Key,
		PrevState: &prevPrev,
		NewState:  prev,
	})

	// Track next modification (only if different from prev)
	if nextPageKeylet.Key != prevPageKeylet.Key {
		result.ModifiedNodes = append(result.ModifiedNodes, DirRemoveModifiedNode{
			Key:       nextPageKeylet.Key,
			PrevState: &nextPrev,
			NewState:  next,
		})
	}

	// Serialize prev update
	prevIsBookDir := prev.TakerPaysCurrency != [20]byte{} || prev.TakerGetsCurrency != [20]byte{}
	prevData, err := SerializeDirectoryNode(prev, prevIsBookDir)
	if err != nil {
		return nil, err
	}
	if err := view.Update(prevPageKeylet, prevData); err != nil {
		return nil, err
	}

	// Serialize next update (only if different from prev)
	if nextPageKeylet.Key != prevPageKeylet.Key {
		nextIsBookDir := next.TakerPaysCurrency != [20]byte{} || next.TakerGetsCurrency != [20]byte{}
		nextData, err := SerializeDirectoryNode(next, nextIsBookDir)
		if err != nil {
			return nil, err
		}
		if err := view.Update(nextPageKeylet, nextData); err != nil {
			return nil, err
		}
	}

	// Delete the now-empty page
	result.PageDeleted = true
	result.DeletedNodes = append(result.DeletedNodes, DirRemoveDeletedNode{
		Key:        pageKeylet.Key,
		FinalState: &prevNode,
	})
	if err := view.Erase(pageKeylet); err != nil {
		return nil, err
	}

	// Check if next page is now the last page and empty - clean it up
	if nextPage != rootPage && next.IndexNext == rootPage && len(next.Indexes) == 0 {
		// Delete next as well
		result.DeletedNodes = append(result.DeletedNodes, DirRemoveDeletedNode{
			Key:        nextPageKeylet.Key,
			FinalState: &nextPrev,
		})
		if err := view.Erase(nextPageKeylet); err != nil {
			return nil, err
		}

		// Update prev to point to root
		prev.SetIndexNext(rootPage)
		// Re-serialize prev
		prevData, err := SerializeDirectoryNode(prev, prevIsBookDir)
		if err != nil {
			return nil, err
		}
		if err := view.Update(prevPageKeylet, prevData); err != nil {
			return nil, err
		}

		// Update root's IndexPrevious
		rootKeylet := keylet.DirPage(directory.Key, rootPage)
		rootData, err := view.Read(rootKeylet)
		if err != nil {
			return nil, err
		}
		root, err := ParseDirectoryNode(rootData)
		if err != nil {
			return nil, err
		}
		rootPrev := *root
		root.SetIndexPrevious(prevPage)

		result.ModifiedNodes = append(result.ModifiedNodes, DirRemoveModifiedNode{
			Key:       rootKeylet.Key,
			PrevState: &rootPrev,
			NewState:  root,
		})

		rootIsBookDir := root.TakerPaysCurrency != [20]byte{} || root.TakerGetsCurrency != [20]byte{}
		rootData, err = SerializeDirectoryNode(root, rootIsBookDir)
		if err != nil {
			return nil, err
		}
		if err := view.Update(rootKeylet, rootData); err != nil {
			return nil, err
		}

		nextPage = rootPage
	}

	// If not keeping root, check if prev is root and now empty
	if !keepRoot && nextPage == rootPage && prevPage == rootPage {
		if len(prev.Indexes) == 0 {
			// Delete root as well
			result.RootDeleted = true
			result.DeletedNodes = append(result.DeletedNodes, DirRemoveDeletedNode{
				Key:        prevPageKeylet.Key,
				FinalState: &prevPrev,
			})
			if err := view.Erase(prevPageKeylet); err != nil {
				return nil, err
			}
		}
	}

	return result, nil
}

// DirForEach iterates all entries in a directory (across all pages), calling fn
// for each item key. If fn returns a non-nil error, iteration stops and the error
// is returned. Returns nil if the directory does not exist.
// Reference: rippled cdirFirst/cdirNext pattern.
func DirForEach(view LedgerView, dirKey keylet.Keylet, fn func(itemKey [32]byte) error) error {
	// Read root page. A real storage error must propagate; only a genuinely
	// absent root means "directory doesn't exist — nothing to iterate".
	rootData, err := view.Read(dirKey)
	if err != nil {
		return err
	}
	if rootData == nil {
		return nil
	}

	root, err := ParseDirectoryNode(rootData)
	if err != nil {
		return fmt.Errorf("failed to parse directory root: %w", err)
	}

	// Iterate root page entries
	for _, idx := range root.Indexes {
		if err := fn(idx); err != nil {
			return err
		}
	}

	// Follow IndexNext chain through subsequent pages
	nextPage := root.IndexNext
	for nextPage != 0 {
		pageKeylet := keylet.DirPage(dirKey.Key, nextPage)
		pageData, err := view.Read(pageKeylet)
		if err != nil {
			return err
		}
		if pageData == nil {
			break
		}

		page, err := ParseDirectoryNode(pageData)
		if err != nil {
			return fmt.Errorf("failed to parse directory page %d: %w", nextPage, err)
		}

		for _, idx := range page.Indexes {
			if err := fn(idx); err != nil {
				return err
			}
		}

		nextPage = page.IndexNext
	}

	return nil
}

// sortIndexes sorts a slice of directory index keys in ascending byte order.
// Mirrors rippled's std::sort over STVector256 used inside dirAdd's
// preserveOrder=false path. The dirNodeMaxEntries=32 cap keeps the
// sort cost negligible.
func sortIndexes(indexes [][32]byte) {
	sort.Slice(indexes, func(i, j int) bool {
		return bytes.Compare(indexes[i][:], indexes[j][:]) < 0
	})
}

// sortedSearch returns the first position i in indexes where indexes[i] >= key.
// Mirrors std::lower_bound over a sorted vector.
func sortedSearch(indexes [][32]byte, key [32]byte) int {
	return sort.Search(len(indexes), func(i int) bool {
		return bytes.Compare(indexes[i][:], key[:]) >= 0
	})
}

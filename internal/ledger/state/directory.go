package state

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/keylet"
)

// DirectoryNode represents a directory ledger entry
type DirectoryNode struct {
	// Common fields
	Flags         uint32
	RootIndex     [32]byte
	Indexes       [][32]byte // List of object keys in this directory page
	IndexNext     uint64     // Next page index (0 if none)
	IndexPrevious uint64     // Previous page index (0 if none)

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
}

// cMinValue is the minimum normalized mantissa value (10^15)
const cMinValue uint64 = 1000000000000000

// tenTo17 is 10^17
var tenTo17 = new(big.Int).Exp(big.NewInt(10), big.NewInt(17), nil)

// GetRate calculates the quality/exchange rate for an offer.
// This matches rippled's getRate(offerOut, offerIn) which returns in/out.
// Reference: rippled STAmount.h line 693-694:
//   "Rate: smaller is better, the taker wants the most out: in/out"
// Lower rate value = better for taker (they pay less per unit they get)
// Returns uint64 encoded as: (exponent+100) << 56 | mantissa
//
// Uses the same integer arithmetic as rippled's divide() function in STAmount.cpp:
//   muldiv(numVal, tenTo17, denVal) + 5
// where muldiv does (a * b) / c with 128-bit precision.
func GetRate(offerOut, offerIn Amount) uint64 {
	// Handle zero case - check offerOut since we divide by it
	if offerOut.IsZero() || offerIn.IsZero() {
		return 0
	}

	// Get mantissa and exponent for numerator (offerIn)
	var numVal uint64
	var numOffset int
	if offerIn.IsNative() {
		numVal = uint64(offerIn.Drops())
		numOffset = 0
	} else {
		iou := offerIn.IOU()
		mantissa := iou.Mantissa()
		if mantissa < 0 {
			mantissa = -mantissa
		}
		numVal = uint64(mantissa)
		numOffset = iou.Exponent()
	}

	// Get mantissa and exponent for denominator (offerOut)
	var denVal uint64
	var denOffset int
	if offerOut.IsNative() {
		denVal = uint64(offerOut.Drops())
		denOffset = 0
	} else {
		iou := offerOut.IOU()
		mantissa := iou.Mantissa()
		if mantissa < 0 {
			mantissa = -mantissa
		}
		denVal = uint64(mantissa)
		denOffset = iou.Exponent()
	}

	// Normalize native amounts to have mantissa in [10^15, 10^16)
	// This matches rippled's divide() function behavior
	if offerIn.IsNative() {
		for numVal < cMinValue && numVal > 0 {
			numVal *= 10
			numOffset--
		}
	}

	if offerOut.IsNative() {
		for denVal < cMinValue && denVal > 0 {
			denVal *= 10
			denOffset--
		}
	}

	if numVal == 0 || denVal == 0 {
		return 0
	}

	// Calculate (numVal * 10^17) / denVal using big.Int for 128-bit precision
	// This matches rippled's muldiv(numVal, tenTo17, denVal)
	bigNum := new(big.Int).SetUint64(numVal)
	bigDen := new(big.Int).SetUint64(denVal)

	// product = numVal * 10^17
	product := new(big.Int).Mul(bigNum, tenTo17)

	// result = product / denVal (truncated integer division)
	result := new(big.Int).Div(product, bigDen)

	// Add 5 for rounding (matches rippled: muldiv(...) + 5)
	result.Add(result, big.NewInt(5))

	// Calculate exponent
	resultOffset := numOffset - denOffset - 17

	// Normalize result to mantissa in [10^15, 10^16)
	mantissa := result.Uint64()
	for mantissa < cMinValue && mantissa > 0 {
		mantissa *= 10
		resultOffset--
	}
	for mantissa >= 10*cMinValue {
		mantissa /= 10
		resultOffset++
	}

	// Encode: upper 8 bits = exponent+100, lower 56 bits = mantissa
	return uint64(resultOffset+100)<<56 | mantissa
}

// SerializeDirectoryNode serializes a DirectoryNode to binary format
func SerializeDirectoryNode(dir *DirectoryNode, isBookDir bool) ([]byte, error) {
	jsonObj := map[string]any{
		"LedgerEntryType": "DirectoryNode",
		"Flags":           dir.Flags,
		"RootIndex":       strings.ToUpper(hex.EncodeToString(dir.RootIndex[:])),
	}

	// Add Indexes if present
	if len(dir.Indexes) > 0 {
		indexes := make([]string, len(dir.Indexes))
		for i, idx := range dir.Indexes {
			indexes[i] = strings.ToUpper(hex.EncodeToString(idx[:]))
		}
		jsonObj["Indexes"] = indexes
	}

	// Add pagination fields if set
	if dir.IndexNext != 0 {
		jsonObj["IndexNext"] = formatUint64Hex(dir.IndexNext)
	}
	if dir.IndexPrevious != 0 {
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

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, err
	}

	return hex.DecodeString(hexStr)
}

// ParseDirectoryNode parses a DirectoryNode from binary data
func ParseDirectoryNode(data []byte) (*DirectoryNode, error) {
	hexStr := hex.EncodeToString(data)
	jsonObj, err := binarycodec.Decode(hexStr)
	if err != nil {
		return nil, err
	}
	dir := &DirectoryNode{}

	if flags, ok := jsonObj["Flags"].(float64); ok {
		dir.Flags = uint32(flags)
	}

	if rootIndex, ok := jsonObj["RootIndex"].(string); ok {
		rootBytes, _ := hex.DecodeString(rootIndex)
		copy(dir.RootIndex[:], rootBytes)
	}

	// Handle both []string and []any for Indexes (binary codec may return either)
	if indexes, ok := jsonObj["Indexes"].([]string); ok {
		dir.Indexes = make([][32]byte, len(indexes))
		for i, idxStr := range indexes {
			idxBytes, _ := hex.DecodeString(idxStr)
			copy(dir.Indexes[i][:], idxBytes)
		}
	} else if indexes, ok := jsonObj["Indexes"].([]any); ok {
		dir.Indexes = make([][32]byte, len(indexes))
		for i, idx := range indexes {
			if idxStr, ok := idx.(string); ok {
				idxBytes, _ := hex.DecodeString(idxStr)
				copy(dir.Indexes[i][:], idxBytes)
			}
		}
	}

	if indexNext, ok := jsonObj["IndexNext"].(string); ok {
		dir.IndexNext = parseUint64Hex(indexNext)
	}

	if indexPrev, ok := jsonObj["IndexPrevious"].(string); ok {
		dir.IndexPrevious = parseUint64Hex(indexPrev)
	}

	if owner, ok := jsonObj["Owner"].(string); ok {
		ownerID, _ := decodeAccountID(owner)
		dir.Owner = ownerID
	}

	// Parse book directory fields (must preserve these even if not used)
	if exchangeRate, ok := jsonObj["ExchangeRate"].(string); ok {
		dir.ExchangeRate = parseUint64Hex(exchangeRate)
	}
	if takerPaysCurrency, ok := jsonObj["TakerPaysCurrency"].(string); ok {
		decoded, _ := hex.DecodeString(takerPaysCurrency)
		copy(dir.TakerPaysCurrency[:], decoded)
	}
	if takerPaysIssuer, ok := jsonObj["TakerPaysIssuer"].(string); ok {
		decoded, _ := hex.DecodeString(takerPaysIssuer)
		copy(dir.TakerPaysIssuer[:], decoded)
	}
	if takerGetsCurrency, ok := jsonObj["TakerGetsCurrency"].(string); ok {
		decoded, _ := hex.DecodeString(takerGetsCurrency)
		copy(dir.TakerGetsCurrency[:], decoded)
	}
	if takerGetsIssuer, ok := jsonObj["TakerGetsIssuer"].(string); ok {
		decoded, _ := hex.DecodeString(takerGetsIssuer)
		copy(dir.TakerGetsIssuer[:], decoded)
	}

	// Parse optional Hash256 fields
	if nfTokenID, ok := jsonObj["NFTokenID"].(string); ok {
		decoded, _ := hex.DecodeString(nfTokenID)
		copy(dir.NFTokenID[:], decoded)
	}
	if domainID, ok := jsonObj["DomainID"].(string); ok {
		decoded, _ := hex.DecodeString(domainID)
		copy(dir.DomainID[:], decoded)
	}

	return dir, nil
}

// uint64ToBytes converts uint64 to big-endian bytes
func uint64ToBytes(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// parseUint64Hex parses a hex string as uint64
func parseUint64Hex(s string) uint64 {
	// Pad to 16 chars
	for len(s) < 16 {
		s = "0" + s
	}
	b, _ := hex.DecodeString(s)
	return binary.BigEndian.Uint64(b)
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

// dirInsert adds an item to a directory, creating the directory if needed.
// Returns the page number where the item was inserted.
// Follows rippled's dirAdd algorithm for multi-page directory support.
func DirInsert(view LedgerView, dirKey keylet.Keylet, itemKey [32]byte, setupFunc func(*DirectoryNode)) (*DirInsertResult, error) {
	result := &DirInsertResult{
		DirKey: dirKey.Key,
	}

	// Check if root directory exists
	exists, err := view.Exists(dirKey)
	if err != nil {
		return nil, err
	}
	// Determine if this is a book directory based on setup function behavior
	var isBookDir bool
	testDir := &DirectoryNode{}
	if setupFunc != nil {
		setupFunc(testDir)
		isBookDir = testDir.TakerPaysCurrency != [20]byte{} || testDir.TakerGetsCurrency != [20]byte{}
	}

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
		node.Indexes = append(node.Indexes, itemKey)

		result.Modified = true
		result.Page = page
		result.DirKey = nodeKey
		result.PreviousState = &prevNode
		result.NewState = node

		// Serialize and update
		nodeIsBookDir := node.TakerPaysCurrency != [20]byte{} || node.TakerGetsCurrency != [20]byte{}
		data, err := SerializeDirectoryNode(node, nodeIsBookDir)
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
	newPageKeylet := keylet.DirPage(dirKey.Key, newPage)

	// Save previous states
	prevNode := *node
	prevRoot := *root

	// Update current node's IndexNext to point to new page
	node.IndexNext = newPage

	// Update root's IndexPrevious to point to new page
	root.IndexPrevious = newPage

	// Create new page
	newPageNode := &DirectoryNode{
		RootIndex: dirKey.Key,
		Indexes:   [][32]byte{itemKey},
	}
	// Set IndexPrevious on new page (unless it's page 1)
	if newPage != 1 {
		newPageNode.IndexPrevious = newPage - 1
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
	nodeIsBookDir := node.TakerPaysCurrency != [20]byte{} || node.TakerGetsCurrency != [20]byte{}
	nodeData, err := SerializeDirectoryNode(node, nodeIsBookDir)
	if err != nil {
		return nil, err
	}
	if err := view.Update(keylet.Keylet{Type: dirKey.Type, Key: nodeKey}, nodeData); err != nil {
		return nil, err
	}

	// 2. Update root with new IndexPrevious (only if root != node)
	if page != 0 {
		rootIsBookDir := root.TakerPaysCurrency != [20]byte{} || root.TakerGetsCurrency != [20]byte{}
		rootData, err := SerializeDirectoryNode(root, rootIsBookDir)
		if err != nil {
			return nil, err
		}
		if err := view.Update(dirKey, rootData); err != nil {
			return nil, err
		}
	}

	// 3. Insert new page
	newPageIsBookDir := newPageNode.TakerPaysCurrency != [20]byte{} || newPageNode.TakerGetsCurrency != [20]byte{}
	newPageData, err := SerializeDirectoryNode(newPageNode, newPageIsBookDir)
	if err != nil {
		return nil, err
	}
	if err := view.Insert(newPageKeylet, newPageData); err != nil {
		return nil, err
	}

	return result, nil
}

// GetCurrencyBytes converts a currency code to 20 bytes
// For standard 3-char codes: 12 zero bytes + 3 char bytes + 5 zero bytes
// For XRP: all zeros
func GetCurrencyBytes(currency string) [20]byte {
	var result [20]byte
	if currency == "" || currency == "XRP" {
		return result // All zeros for XRP
	}

	// Standard 3-character currency code
	if len(currency) == 3 {
		// Format: 12 zero bytes + 3 ASCII bytes + 5 zero bytes
		copy(result[12:15], []byte(currency))
	} else if len(currency) == 40 {
		// Hex-encoded currency (160-bit)
		decoded, _ := hex.DecodeString(currency)
		copy(result[:], decoded)
	}
	return result
}

// GetCurrencyString converts a 20-byte currency representation back to string.
// For standard 3-char codes: reads from bytes 12-15
// For XRP (all zeros): returns "XRP"
// For hex currencies (first byte != 0): returns 40-char hex string
func GetCurrencyString(currency [20]byte) string {
	// Check if all zeros (XRP)
	allZeros := true
	for _, b := range currency {
		if b != 0 {
			allZeros = false
			break
		}
	}
	if allZeros {
		return "XRP"
	}

	// Check if it's a hex currency (first byte is non-zero or has special markers)
	// Standard 3-char currencies have first 12 bytes as zero
	isHexCurrency := false
	for i := 0; i < 12; i++ {
		if currency[i] != 0 {
			isHexCurrency = true
			break
		}
	}
	// Also check last 5 bytes for standard currency
	if !isHexCurrency {
		for i := 15; i < 20; i++ {
			if currency[i] != 0 {
				isHexCurrency = true
				break
			}
		}
	}

	if isHexCurrency {
		return strings.ToUpper(hex.EncodeToString(currency[:]))
	}

	// Standard 3-character currency code
	return string(currency[12:15])
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

// formatUint64HexPadded formats a uint64 as 16-char uppercase hex with leading zeros
// Used for fields like OwnerNode and BookNode that require zero-padding in metadata
func formatUint64HexPadded(v uint64) string {
	return strings.ToUpper(hex.EncodeToString(uint64ToBytes(v)))
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

	// Get the page where the item should be
	pageKeylet := keylet.DirPage(directory.Key, page)
	pageData, err := view.Read(pageKeylet)
	if err != nil {
		return result, nil // Page not found, return success=false
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
				node.IndexNext = rootPage
				node.IndexPrevious = rootPage

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

	nextPageKeylet := keylet.DirPage(directory.Key, nextPage)
	nextPageData, err := view.Read(nextPageKeylet)
	if err != nil {
		return nil, fmt.Errorf("directory chain: rev link broken")
	}
	next, err := ParseDirectoryNode(nextPageData)
	if err != nil {
		return nil, err
	}
	nextPrev := *next

	// Unlink: prev.IndexNext = nextPage
	prev.IndexNext = nextPage
	// Unlink: next.IndexPrevious = prevPage
	next.IndexPrevious = prevPage

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
		prev.IndexNext = rootPage
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
		root.IndexPrevious = prevPage

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
	// Read root page
	rootData, err := view.Read(dirKey)
	if err != nil || rootData == nil {
		return nil // Directory doesn't exist — nothing to iterate
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
		if err != nil || pageData == nil {
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

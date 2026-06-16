package testing

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// BumpDirectoryLastPage moves the last page of an account's owner directory
// to a target page number. This mirrors rippled's test::jtx::directory::bumpLastPage()
// which is used to test directory page limit checks by placing the last page
// near the maximum allowed page number.
//
// The operation:
// 1. Finds the last page of the owner directory
// 2. Copies its entries to a new page at targetPage
// 3. Erases the old last page
// 4. Updates page chain pointers (root, previous page)
// 5. Updates each moved entry's adjustField to the new page number
//
// Reference: rippled src/test/jtx/impl/directory.cpp bumpLastPage()
func (e *TestEnv) BumpDirectoryLastPage(acc *Account, targetPage uint64, adjustField string) error {
	e.t.Helper()

	dirRootKey := keylet.OwnerDir(acc.ID)

	// Read the root directory page
	rootData, err := e.ledger.Read(dirRootKey)
	if err != nil || rootData == nil {
		return fmt.Errorf("directory root not found")
	}
	root, err := state.ParseDirectoryNode(rootData)
	if err != nil {
		return fmt.Errorf("failed to parse root: %v", err)
	}

	// Get last page index from root's IndexPrevious
	lastIndex := root.IndexPrevious
	if lastIndex == 0 {
		return fmt.Errorf("directory too small (only root page)")
	}

	if lastIndex >= targetPage {
		return fmt.Errorf("target page %d must be greater than current last page %d", targetPage, lastIndex)
	}

	// Read the last page
	lastPageKey := keylet.DirPage(dirRootKey.Key, lastIndex)
	lastPageData, err := e.ledger.Read(lastPageKey)
	if err != nil || lastPageData == nil {
		return fmt.Errorf("last page %d not found", lastIndex)
	}
	lastPage, err := state.ParseDirectoryNode(lastPageData)
	if err != nil {
		return fmt.Errorf("failed to parse last page: %v", err)
	}

	// Save the entries and previous page pointer
	indexes := lastPage.Indexes
	prevIndex := lastPage.IndexPrevious

	// Erase the old last page
	if err := e.ledger.Erase(lastPageKey); err != nil {
		return fmt.Errorf("failed to erase old page: %v", err)
	}

	// Create new page at targetPage with the same entries
	newPageKey := keylet.DirPage(dirRootKey.Key, targetPage)
	newPage := &state.DirectoryNode{
		RootIndex: dirRootKey.Key,
		Indexes:   indexes,
		Owner:     root.Owner,
	}
	// Mirror the original last page's link presence: rippled sets IndexPrevious
	// via setFieldU64 only when non-zero (a page whose previous is the root
	// leaves it absent), so guard the setter rather than force a present-at-0
	// link.
	if prevIndex != 0 {
		newPage.SetIndexPrevious(prevIndex)
	}
	newPageData, err := state.SerializeDirectoryNode(newPage, false)
	if err != nil {
		return fmt.Errorf("failed to serialize new page: %v", err)
	}
	if err := e.ledger.Insert(newPageKey, newPageData); err != nil {
		return fmt.Errorf("failed to insert new page: %v", err)
	}

	// Update root's IndexPrevious to point to new page
	root.SetIndexPrevious(targetPage)
	// If the previous page was root (prevIndex == 0), also update IndexNext
	if prevIndex == 0 {
		root.SetIndexNext(targetPage)
	}
	rootData, err = state.SerializeDirectoryNode(root, false)
	if err != nil {
		return fmt.Errorf("failed to serialize root: %v", err)
	}
	if err := e.ledger.Update(dirRootKey, rootData); err != nil {
		return fmt.Errorf("failed to update root: %v", err)
	}

	// If previous page was NOT root, update its IndexNext
	if prevIndex != 0 {
		prevPageKey := keylet.DirPage(dirRootKey.Key, prevIndex)
		prevPageData, err := e.ledger.Read(prevPageKey)
		if err != nil || prevPageData == nil {
			return fmt.Errorf("previous page %d not found", prevIndex)
		}
		prevPage, err := state.ParseDirectoryNode(prevPageData)
		if err != nil {
			return fmt.Errorf("failed to parse previous page: %v", err)
		}
		prevPage.SetIndexNext(targetPage)
		prevPageData, err = state.SerializeDirectoryNode(prevPage, false)
		if err != nil {
			return fmt.Errorf("failed to serialize previous page: %v", err)
		}
		if err := e.ledger.Update(prevPageKey, prevPageData); err != nil {
			return fmt.Errorf("failed to update previous page: %v", err)
		}
	}

	// Adjust each moved entry's directory-node field. rippled's bumpLastPage
	// always runs an adjust callback that rewrites the entry's link to this
	// directory (adjustOwnerNode for tickets, sfIssuerNode for credentials,
	// ...), but the fixture recorder does not capture which field the callback
	// touched. When the fixture names the field, rewrite exactly that one;
	// otherwise rewrite every *Node field that currently holds the old page
	// number — that is, the entry's link(s) into the moved page. Leaving the
	// hint stale would create a state rippled never produces, where a later
	// dirRemove through the recorded hint fails.
	for _, itemKey := range indexes {
		itemKeylet := keylet.Keylet{Key: itemKey}
		itemData, err := e.ledger.Read(itemKeylet)
		if err != nil || itemData == nil {
			continue // Skip entries that can't be read
		}

		updated, err := updateDirNodeFields(itemData, adjustField, lastIndex, targetPage)
		if err != nil {
			return fmt.Errorf("failed to adjust directory node field on entry: %v", err)
		}
		if err := e.ledger.Update(itemKeylet, updated); err != nil {
			return fmt.Errorf("failed to update entry: %v", err)
		}
	}

	return nil
}

// updateDirNodeFields decodes a binary SLE and rewrites its directory page
// hint(s) from oldPage to newPage, then re-encodes it. When fieldName is
// non-empty only that field is rewritten (it must be present); otherwise every
// field named "*Node" whose current value equals oldPage is rewritten, and at
// least one such field must exist — mirroring rippled's adjust callbacks,
// which fail the bump when the entry has no link to update.
func updateDirNodeFields(data []byte, fieldName string, oldPage, newPage uint64) ([]byte, error) {
	// Decode binary to JSON map (Decode expects hex string)
	hexStr := hex.EncodeToString(data)
	jsonMap, err := binarycodec.Decode(hexStr)
	if err != nil {
		return nil, fmt.Errorf("decode failed: %v", err)
	}

	// UInt64 fields are encoded as hex strings.
	newValue := tx.FormatUint64Hex(newPage)
	adjusted := 0
	if fieldName != "" {
		if _, ok := jsonMap[fieldName]; !ok {
			return nil, fmt.Errorf("field %s not present on moved entry", fieldName)
		}
		jsonMap[fieldName] = newValue
		adjusted++
	} else {
		for k, v := range jsonMap {
			if !strings.HasSuffix(k, "Node") {
				continue
			}
			s, ok := v.(string)
			if !ok {
				continue
			}
			cur, err := strconv.ParseUint(s, 16, 64)
			if err != nil || cur != oldPage {
				continue
			}
			jsonMap[k] = newValue
			adjusted++
		}
	}
	if adjusted == 0 {
		return nil, fmt.Errorf("no directory node field pointing at page %d on moved entry", oldPage)
	}

	// Re-encode to binary (Encode returns hex string)
	encodedHex, err := binarycodec.Encode(jsonMap)
	if err != nil {
		return nil, fmt.Errorf("encode failed: %v", err)
	}

	result, err := hex.DecodeString(encodedHex)
	if err != nil {
		return nil, fmt.Errorf("hex decode failed: %v", err)
	}
	return result, nil
}

// ForceOwnerDirEmptyAnchorWithNext rewrites the anchor (root) page of an
// account's owner directory to have an empty Indexes slice while leaving
// IndexNext pointing at a non-zero continuation page. This reproduces the
// state rippled's dirIsEmpty must guard against: an emptied anchor whose
// directory is still non-empty because subsequent pages remain.
//
// Reference: rippled View.cpp dirIsEmpty().
func (e *TestEnv) ForceOwnerDirEmptyAnchorWithNext(acc *Account, nextPage uint64) error {
	e.t.Helper()
	if nextPage == 0 {
		return fmt.Errorf("nextPage must be non-zero")
	}

	dirRootKey := keylet.OwnerDir(acc.ID)
	rootData, err := e.ledger.Read(dirRootKey)
	if err != nil || rootData == nil {
		return fmt.Errorf("directory root not found")
	}
	root, err := state.ParseDirectoryNode(rootData)
	if err != nil {
		return fmt.Errorf("failed to parse root: %v", err)
	}

	root.Indexes = nil
	root.SetIndexNext(nextPage)

	updated, err := state.SerializeDirectoryNode(root, false)
	if err != nil {
		return fmt.Errorf("failed to serialize root: %v", err)
	}
	if err := e.ledger.Update(dirRootKey, updated); err != nil {
		return fmt.Errorf("failed to update root: %v", err)
	}
	return nil
}

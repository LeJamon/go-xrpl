package testing

import (
	"encoding/hex"
	"fmt"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
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
		RootIndex:     dirRootKey.Key,
		Indexes:       indexes,
		Owner:         root.Owner,
		IndexPrevious: prevIndex,
	}
	newPageData, err := state.SerializeDirectoryNode(newPage, false)
	if err != nil {
		return fmt.Errorf("failed to serialize new page: %v", err)
	}
	if err := e.ledger.Insert(newPageKey, newPageData); err != nil {
		return fmt.Errorf("failed to insert new page: %v", err)
	}

	// Update root's IndexPrevious to point to new page
	root.IndexPrevious = targetPage
	// If the previous page was root (prevIndex == 0), also update IndexNext
	if prevIndex == 0 {
		root.IndexNext = targetPage
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
		prevPage.IndexNext = targetPage
		prevPageData, err = state.SerializeDirectoryNode(prevPage, false)
		if err != nil {
			return fmt.Errorf("failed to serialize previous page: %v", err)
		}
		if err := e.ledger.Update(prevPageKey, prevPageData); err != nil {
			return fmt.Errorf("failed to update previous page: %v", err)
		}
	}

	// Adjust the field on each entry that was moved
	if adjustField != "" {
		for _, itemKey := range indexes {
			itemKeylet := keylet.Keylet{Key: itemKey}
			itemData, err := e.ledger.Read(itemKeylet)
			if err != nil || itemData == nil {
				continue // Skip entries that can't be read
			}

			// Decode via binary codec, update the field, re-encode
			updated, err := updateUint64Field(itemData, adjustField, targetPage)
			if err != nil {
				return fmt.Errorf("failed to adjust %s on entry: %v", adjustField, err)
			}
			if err := e.ledger.Update(itemKeylet, updated); err != nil {
				return fmt.Errorf("failed to update entry: %v", err)
			}
		}
	}

	return nil
}

// updateUint64Field decodes a binary SLE, updates a uint64 field, and re-encodes it.
func updateUint64Field(data []byte, fieldName string, value uint64) ([]byte, error) {
	// Decode binary to JSON map (Decode expects hex string)
	hexStr := hex.EncodeToString(data)
	jsonMap, err := binarycodec.Decode(hexStr)
	if err != nil {
		return nil, fmt.Errorf("decode failed: %v", err)
	}

	// Update the field (uint64 fields are encoded as hex strings)
	jsonMap[fieldName] = tx.FormatUint64Hex(value)

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
	root.IndexNext = nextPage

	updated, err := state.SerializeDirectoryNode(root, false)
	if err != nil {
		return fmt.Errorf("failed to serialize root: %v", err)
	}
	if err := e.ledger.Update(dirRootKey, updated); err != nil {
		return fmt.Errorf("failed to update root: %v", err)
	}
	return nil
}

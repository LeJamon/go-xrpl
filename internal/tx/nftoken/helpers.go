package nftoken

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

// Field type constants for binary parsing (from sle package)
const (
	fieldTypeUInt16  = state.FieldTypeUInt16
	fieldTypeUInt32  = state.FieldTypeUInt32
	fieldTypeUInt64  = state.FieldTypeUInt64
	fieldTypeHash256 = state.FieldTypeHash256
	fieldTypeAccount = state.FieldTypeAccount
	fieldTypeBlob    = state.FieldTypeBlob
)

// NFToken ID flag constants (stored in first 2 bytes of NFTokenID).
// These match the mint flags but are used when constructing/inspecting NFToken IDs.
const (
	NFTokenFlagBurnable     uint16 = 0x0001
	NFTokenFlagOnlyXRP      uint16 = 0x0002
	NFTokenFlagTrustLine    uint16 = 0x0004
	NFTokenFlagTransferable uint16 = 0x0008
	NFTokenFlagMutable      uint16 = 0x0010
)

// nftPageMask is the mask for the low 96 bits of an NFTokenID
// This is used to group equivalent NFTs on the same page
var nftPageMask = [32]byte{
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
}

// getNFTIssuer extracts the issuer AccountID from an NFTokenID
// NFTokenID format: Flags(2) + TransferFee(2) + Issuer(20) + Taxon(4) + Sequence(4)
func getNFTIssuer(nftokenID [32]byte) [20]byte {
	var issuer [20]byte
	copy(issuer[:], nftokenID[4:24])
	return issuer
}

// getNFTokenFlags extracts the flags from an NFTokenID string (first 4 hex chars)
func getNFTokenFlags(nftokenID string) uint16 {
	if len(nftokenID) < 4 {
		return 0
	}
	var flags uint16
	for i := 0; i < 4 && i < len(nftokenID); i++ {
		flags <<= 4
		c := nftokenID[i]
		switch {
		case c >= '0' && c <= '9':
			flags |= uint16(c - '0')
		case c >= 'a' && c <= 'f':
			flags |= uint16(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			flags |= uint16(c - 'A' + 10)
		}
	}
	return flags
}

// getNFTTransferFee extracts the transfer fee from an NFTokenID
func getNFTTransferFee(nftokenID [32]byte) uint16 {
	return binary.BigEndian.Uint16(nftokenID[2:4])
}

// getNFTFlagsFromID extracts the flags from an NFTokenID
func getNFTFlagsFromID(nftokenID [32]byte) uint16 {
	return binary.BigEndian.Uint16(nftokenID[0:2])
}

// CipheredTaxon ciphers a taxon using rippled's algorithm to prevent enumeration.
// Matching rippled: (taxon ^ ((tokenSeq ^ 384160001) * 2357503715))
func CipheredTaxon(tokenSeq uint32, taxon uint32) uint32 {
	return cipheredTaxon(tokenSeq, taxon)
}

func cipheredTaxon(tokenSeq uint32, taxon uint32) uint32 {
	// Reference: rippled nft.h cipheredTaxon()
	// Linear congruential generator: f(x) = (m * x + c) mod 2^32
	// m = 384160001, c = 2459
	return taxon ^ ((384160001 * tokenSeq) + 2459)
}

// GenerateNFTokenID generates an NFTokenID based on the minting parameters.
// This is the exported version of generateNFTokenID for use in tests.
// Reference: rippled NFTokenMint.cpp createNFTokenID
func GenerateNFTokenID(issuer [20]byte, taxon, sequence uint32, flags uint16, transferFee uint16) [32]byte {
	return generateNFTokenID(issuer, taxon, sequence, flags, transferFee)
}

// generateNFTokenID generates an NFTokenID based on the minting parameters
// Reference: rippled NFTokenMint.cpp createNFTokenID
func generateNFTokenID(issuer [20]byte, taxon, sequence uint32, flags uint16, transferFee uint16) [32]byte {
	var tokenID [32]byte

	// NFTokenID format (32 bytes):
	// Bytes 0-1: Flags (2 bytes, big endian)
	// Bytes 2-3: TransferFee (2 bytes, big endian)
	// Bytes 4-23: Issuer AccountID (20 bytes)
	// Bytes 24-27: Taxon (ciphered, 4 bytes, big endian)
	// Bytes 28-31: Sequence (4 bytes, big endian)

	binary.BigEndian.PutUint16(tokenID[0:2], flags)
	binary.BigEndian.PutUint16(tokenID[2:4], transferFee)
	copy(tokenID[4:24], issuer[:])

	ciphered := cipheredTaxon(sequence, taxon)
	binary.BigEndian.PutUint32(tokenID[24:28], ciphered)
	binary.BigEndian.PutUint32(tokenID[28:32], sequence)

	return tokenID
}

// ---------------------------------------------------------------------------
// NFToken comparison and page key helpers
// ---------------------------------------------------------------------------

// compareNFTokenID compares two NFTokenIDs using rippled's sort order:
// sort by low 96 bits first, then full 256-bit ID as tiebreaker.
// Reference: rippled NFTokenUtils.cpp compareTokens
func compareNFTokenID(a, b [32]byte) int {
	// Compare low 96 bits (bytes 20-31) first
	if c := bytes.Compare(a[20:], b[20:]); c != 0 {
		return c
	}
	// Full 256-bit comparison as tiebreaker
	return bytes.Compare(a[:], b[:])
}

// getNFTPageKey returns the low 96 bits of an NFTokenID (for page grouping)
func getNFTPageKey(nftokenID [32]byte) [32]byte {
	var result [32]byte
	for i := 0; i < 32; i++ {
		result[i] = nftokenID[i] & nftPageMask[i]
	}
	return result
}

// insertNFTokenSorted inserts an NFToken into the slice maintaining sorted order
func insertNFTokenSorted(tokens []state.NFTokenData, newToken state.NFTokenData) []state.NFTokenData {
	pos := 0
	for i, t := range tokens {
		if compareNFTokenID(newToken.NFTokenID, t.NFTokenID) < 0 {
			pos = i
			break
		}
		pos = i + 1
	}
	tokens = append(tokens, state.NFTokenData{})
	copy(tokens[pos+1:], tokens[pos:])
	tokens[pos] = newToken
	return tokens
}

// ---------------------------------------------------------------------------
// Page traversal — replaces broken hashed-keylet approach
// Reference: rippled NFTokenUtils.cpp locatePage, getPageForToken
// ---------------------------------------------------------------------------

// locatePage finds the NFToken page that should contain (or does contain)
// the given token. It walks backwards from the max page via PreviousPageMin.
// Returns (pageKeylet, pageData, err). If the owner has no pages, returns nil data.
// Reference: rippled NFTokenUtils.cpp locatePage — uses view.succ() which we
// emulate by walking the linked list from the max page.
func locatePage(view tx.LedgerView, owner [20]byte, tokenID [32]byte) (keylet.Keylet, *state.NFTokenPageData, error) {
	base := keylet.NFTokenPageMin(owner)
	first := keylet.NFTokenPageForToken(base, tokenID)
	maxKL := keylet.NFTokenPageMax(owner)

	// Start at max page
	data, err := view.Read(maxKL)
	if err != nil || data == nil {
		return keylet.Keylet{}, nil, nil // No pages for this owner
	}

	currentKL := maxKL
	currentData := data

	for {
		page, err := state.ParseNFTokenPage(currentData)
		if err != nil {
			return keylet.Keylet{}, nil, err
		}

		// Check if there's a previous page
		var emptyHash [32]byte
		if page.PreviousPageMin == emptyHash {
			// This is the leftmost page — token must be here (or not exist)
			return currentKL, page, nil
		}

		// If previous page's key <= first.Key, then current page is the first
		// page with key strictly > first — i.e., the correct page.
		if bytes.Compare(page.PreviousPageMin[:], first.Key[:]) <= 0 {
			return currentKL, page, nil
		}

		// Previous page's key > first, so the right page might be further back
		prevKL := keylet.Keylet{Type: currentKL.Type, Key: page.PreviousPageMin}
		prevData, err := view.Read(prevKL)
		if err != nil || prevData == nil {
			// Broken link — fall back to current page
			return currentKL, page, nil
		}

		currentKL = prevKL
		currentData = prevData
	}
}

// findToken searches the owner's pages for a specific NFT ID.
// Returns the page keylet, the page data, the index of the token within the page,
// and whether it was found.
func findToken(view tx.LedgerView, owner [20]byte, tokenID [32]byte) (keylet.Keylet, *state.NFTokenPageData, int, bool) {
	kl, page, err := locatePage(view, owner, tokenID)
	if err != nil || page == nil {
		return keylet.Keylet{}, nil, -1, false
	}

	for i, t := range page.NFTokens {
		if t.NFTokenID == tokenID {
			return kl, page, i, true
		}
	}

	return keylet.Keylet{}, nil, -1, false
}

// ---------------------------------------------------------------------------
// getPageForToken — finds or creates the right page for inserting a token
// Reference: rippled NFTokenUtils.cpp getPageForToken
// ---------------------------------------------------------------------------

type insertNFTokenResult struct {
	Result       tx.Result
	PagesCreated int
}

// getPageForToken finds the page for inserting a token, creating or splitting
// pages as needed. Returns the page keylet, page data, and pages created count.
// Reference: rippled NFTokenUtils.cpp getPageForToken
func getPageForToken(
	view tx.LedgerView,
	owner [20]byte,
	tokenID [32]byte,
) (keylet.Keylet, *state.NFTokenPageData, int, error) {
	base := keylet.NFTokenPageMin(owner)
	first := keylet.NFTokenPageForToken(base, tokenID)
	maxKL := keylet.NFTokenPageMax(owner)

	// Find the candidate page using succ-like traversal
	cpKL, cpData, err := locatePageForInsert(view, owner, first, maxKL)
	if err != nil {
		return keylet.Keylet{}, nil, 0, err
	}

	// No page exists — create the max page with empty array
	if cpData == nil {
		page := &state.NFTokenPageData{
			NFTokens: []state.NFTokenData{},
		}
		pageBytes, err := serializeNFTokenPage(page)
		if err != nil {
			return keylet.Keylet{}, nil, 0, err
		}
		if err := view.Insert(maxKL, pageBytes); err != nil {
			return keylet.Keylet{}, nil, 0, err
		}
		return maxKL, page, 1, nil
	}

	cp := cpData

	// Page has room — return it
	if len(cp.NFTokens) < dirMaxTokensPerPage {
		return cpKL, cp, 0, nil
	}

	// Page is full — need to split
	// Reference: rippled NFTokenUtils.cpp getPageForToken (split logic)
	return splitPage(view, owner, tokenID, cpKL, cp, base, first)
}

// locatePageForInsert finds the first existing page with key > first.Key,
// or returns nil if no pages exist.
func locatePageForInsert(view tx.LedgerView, owner [20]byte, first, maxKL keylet.Keylet) (keylet.Keylet, *state.NFTokenPageData, error) {
	data, err := view.Read(maxKL)
	if err != nil || data == nil {
		return keylet.Keylet{}, nil, nil // No pages
	}

	currentKL := maxKL
	currentData := data

	for {
		page, err := state.ParseNFTokenPage(currentData)
		if err != nil {
			return keylet.Keylet{}, nil, err
		}

		var emptyHash [32]byte
		if page.PreviousPageMin == emptyHash {
			return currentKL, page, nil
		}

		if bytes.Compare(page.PreviousPageMin[:], first.Key[:]) <= 0 {
			return currentKL, page, nil
		}

		prevKL := keylet.Keylet{Type: currentKL.Type, Key: page.PreviousPageMin}
		prevData, err := view.Read(prevKL)
		if err != nil || prevData == nil {
			return currentKL, page, nil
		}

		currentKL = prevKL
		currentPage, err := state.ParseNFTokenPage(prevData)
		if err != nil {
			return keylet.Keylet{}, nil, err
		}
		currentData = prevData
		_ = currentPage
	}
}

// splitPage splits a full page and returns the right page for the new token.
// Reference: rippled NFTokenUtils.cpp getPageForToken (split section)
func splitPage(
	view tx.LedgerView,
	owner [20]byte,
	tokenID [32]byte,
	cpKL keylet.Keylet,
	cp *state.NFTokenPageData,
	base, first keylet.Keylet,
) (keylet.Keylet, *state.NFTokenPageData, int, error) {
	narr := cp.NFTokens // Will become the "left" page (lower keys)

	// Find the split point
	// We prefer to keep equivalent NFTs on a page boundary.
	// Round up the boundary until there's a non-equivalent entry.
	halfIdx := dirMaxTokensPerPage/2 - 1
	cmp := getNFTPageKey(narr[halfIdx].NFTokenID)

	// Find the first token at or after half that has different low 96 bits
	splitIdx := -1
	for i := dirMaxTokensPerPage / 2; i < len(narr); i++ {
		if getNFTPageKey(narr[i].NFTokenID) != cmp {
			splitIdx = i
			break
		}
	}

	// If couldn't find a split point in the second half, try the first half
	if splitIdx == -1 {
		for i := 0; i < len(narr); i++ {
			if getNFTPageKey(narr[i].NFTokenID) == cmp {
				splitIdx = i
				break
			}
		}
	}

	// If splitIdx is still -1, something is very wrong
	if splitIdx == -1 {
		return keylet.Keylet{}, nil, 0, fmt.Errorf("cannot find split point")
	}

	// If splitIdx == 0, entire page is equivalent tokens
	if splitIdx == 0 {
		tokenPageKey := getNFTPageKey(tokenID)
		if tokenPageKey == cmp {
			// Token belongs on this full page of equivalent tokens — cannot store
			return keylet.Keylet{}, nil, 0, nil
		}

		if bytes.Compare(tokenPageKey[:], cmp[:]) > 0 {
			// New token goes after these equivalent tokens — leave everything
			// in narr (the new left page), carr (right page) gets empty
			splitIdx = len(narr)
		}
		// else: new token goes before — splitIdx stays at 0, all go to carr
	}

	// Split: narr[0:splitIdx] goes to new page (left), narr[splitIdx:] stays (right)
	leftTokens := make([]state.NFTokenData, splitIdx)
	copy(leftTokens, narr[:splitIdx])
	rightTokens := make([]state.NFTokenData, len(narr)-splitIdx)
	copy(rightTokens, narr[splitIdx:])

	// Determine the key for the new page
	// Reference: rippled uses the last token in the full page's half, or the
	// first token in the other half, depending on which page is full.
	var tokenIDForNewPage [32]byte
	if len(leftTokens) == dirMaxTokensPerPage {
		// Left page is full — use next() of last token in left page
		tokenIDForNewPage = uint256Next(leftTokens[dirMaxTokensPerPage-1].NFTokenID)
	} else {
		// Use the first token in the right page
		tokenIDForNewPage = rightTokens[0].NFTokenID
	}

	npKL := keylet.NFTokenPageForToken(base, tokenIDForNewPage)

	// Create the new page (left page = lower keys)
	np := &state.NFTokenPageData{
		NFTokens:    leftTokens,
		NextPageMin: cpKL.Key,
	}

	// Fix up links: new page inherits cp's PreviousPageMin
	var emptyHash [32]byte
	if cp.PreviousPageMin != emptyHash {
		np.PreviousPageMin = cp.PreviousPageMin

		// Update the old previous page's NextPageMin to point to new page
		prevKL := keylet.Keylet{Type: cpKL.Type, Key: cp.PreviousPageMin}
		prevData, err := view.Read(prevKL)
		if err == nil {
			prevPage, err := state.ParseNFTokenPage(prevData)
			if err == nil {
				prevPage.NextPageMin = npKL.Key
				prevBytes, err := serializeNFTokenPage(prevPage)
				if err == nil {
					view.Update(prevKL, prevBytes)
				}
			}
		}
	}

	// Insert new page
	npBytes, err := serializeNFTokenPage(np)
	if err != nil {
		return keylet.Keylet{}, nil, 0, err
	}
	if err := view.Insert(npKL, npBytes); err != nil {
		return keylet.Keylet{}, nil, 0, err
	}

	// Update current page (right page = higher keys)
	cp.NFTokens = rightTokens
	cp.PreviousPageMin = npKL.Key
	cpBytes, err := serializeNFTokenPage(cp)
	if err != nil {
		return keylet.Keylet{}, nil, 0, err
	}
	if err := view.Update(cpKL, cpBytes); err != nil {
		return keylet.Keylet{}, nil, 0, err
	}

	// Determine which page to return for the new token insertion
	// Reference: rippled — with fixNFTokenDirV1: return (first.key < np.key) ? np : cp
	if bytes.Compare(first.Key[:], npKL.Key[:]) < 0 {
		// Re-read np since we just wrote it
		npData, err := view.Read(npKL)
		if err != nil {
			return keylet.Keylet{}, nil, 0, err
		}
		page, err := state.ParseNFTokenPage(npData)
		if err != nil {
			return keylet.Keylet{}, nil, 0, err
		}
		return npKL, page, 1, nil
	}

	// Re-read cp since we just wrote it
	cpData2, err := view.Read(cpKL)
	if err != nil {
		return keylet.Keylet{}, nil, 0, err
	}
	page2, err := state.ParseNFTokenPage(cpData2)
	if err != nil {
		return keylet.Keylet{}, nil, 0, err
	}
	return cpKL, page2, 1, nil
}

// uint256Next returns id + 1 (for page key derivation during splits)
func uint256Next(id [32]byte) [32]byte {
	result := id
	for i := 31; i >= 0; i-- {
		result[i]++
		if result[i] != 0 {
			break
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// insertNFToken — inserts an NFToken into the owner's token directory
// Reference: rippled NFTokenUtils.cpp insertToken
// ---------------------------------------------------------------------------

func insertNFToken(ownerID [20]byte, token state.NFTokenData, view tx.LedgerView) insertNFTokenResult {
	pageKL, page, pagesCreated, err := getPageForToken(view, ownerID, token.NFTokenID)
	if err != nil {
		return insertNFTokenResult{Result: tx.TefINTERNAL}
	}

	if page == nil {
		return insertNFTokenResult{Result: tx.TecNO_SUITABLE_NFTOKEN_PAGE}
	}

	// Insert token in sorted position
	page.NFTokens = insertNFTokenSorted(page.NFTokens, token)

	// Serialize and update
	pageBytes, err := serializeNFTokenPage(page)
	if err != nil {
		return insertNFTokenResult{Result: tx.TefINTERNAL}
	}

	if err := view.Update(pageKL, pageBytes); err != nil {
		return insertNFTokenResult{Result: tx.TefINTERNAL}
	}

	return insertNFTokenResult{Result: tx.TesSUCCESS, PagesCreated: pagesCreated}
}

// ---------------------------------------------------------------------------
// removeToken — removes an NFToken from the owner's directory with page merging
// Reference: rippled NFTokenUtils.cpp removeToken
// ---------------------------------------------------------------------------

func removeToken(view tx.LedgerView, owner [20]byte, tokenID [32]byte, fixPageLinks bool) (tx.Result, int) {
	kl, page, err := locatePage(view, owner, tokenID)
	if err != nil || page == nil {
		return tx.TecNO_ENTRY, 0
	}

	// Find and remove the token
	found := false
	for i, t := range page.NFTokens {
		if t.NFTokenID == tokenID {
			page.NFTokens = append(page.NFTokens[:i], page.NFTokens[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return tx.TecNO_ENTRY, 0
	}

	// Load prev and next pages
	var emptyHash [32]byte
	var prevKL keylet.Keylet
	var prevPage *state.NFTokenPageData
	if page.PreviousPageMin != emptyHash {
		prevKL = keylet.Keylet{Type: kl.Type, Key: page.PreviousPageMin}
		prevData, err := view.Read(prevKL)
		if err == nil {
			prevPage, _ = state.ParseNFTokenPage(prevData)
		}
	}

	var nextKL keylet.Keylet
	var nextPage *state.NFTokenPageData
	if page.NextPageMin != emptyHash {
		nextKL = keylet.Keylet{Type: kl.Type, Key: page.NextPageMin}
		nextData, err := view.Read(nextKL)
		if err == nil {
			nextPage, _ = state.ParseNFTokenPage(nextData)
		}
	}

	pagesRemoved := 0

	if len(page.NFTokens) > 0 {
		// Page not empty — update it and try to consolidate
		pageBytes, err := serializeNFTokenPage(page)
		if err != nil {
			return tx.TefINTERNAL, 0
		}
		if err := view.Update(kl, pageBytes); err != nil {
			return tx.TefINTERNAL, 0
		}

		// Try merging with previous page
		if prevPage != nil {
			if doMergePages(view, prevKL, prevPage, kl, page) {
				pagesRemoved++
				// After merge, "page" has been absorbed into kl (p2).
				// Re-read kl for potential second merge
				klData, err := view.Read(kl)
				if err == nil {
					page, _ = state.ParseNFTokenPage(klData)
				}
			}
		}

		// Try merging with next page
		if nextPage != nil {
			if doMergePages(view, kl, page, nextKL, nextPage) {
				pagesRemoved++
			}
		}

		return tx.TesSUCCESS, pagesRemoved
	}

	// Page is empty

	// Special case: if this is the max page (last page) and there's a prev page,
	// move prev's contents to this page instead of deleting the max page.
	// Reference: rippled's fixNFTokenPageLinks behavior
	isMaxPage := true
	for i := 20; i < 32; i++ {
		if kl.Key[i] != 0xFF {
			isMaxPage = false
			break
		}
	}

	if prevPage != nil && isMaxPage && fixPageLinks {
		// Copy prev's tokens to current (max) page
		page.NFTokens = prevPage.NFTokens
		if prevPage.PreviousPageMin != emptyHash {
			page.PreviousPageMin = prevPage.PreviousPageMin
			// Fix link from prev's previous
			ppKL := keylet.Keylet{Type: kl.Type, Key: prevPage.PreviousPageMin}
			ppData, err := view.Read(ppKL)
			if err == nil {
				ppPage, err := state.ParseNFTokenPage(ppData)
				if err == nil {
					ppPage.NextPageMin = kl.Key
					ppBytes, _ := serializeNFTokenPage(ppPage)
					if ppBytes != nil {
						view.Update(ppKL, ppBytes)
					}
				}
			}
		} else {
			page.PreviousPageMin = emptyHash
		}
		pageBytes, _ := serializeNFTokenPage(page)
		view.Update(kl, pageBytes)
		view.Erase(prevKL)
		return tx.TesSUCCESS, 1
	}

	// Not the max page or no prev — unlink and remove
	if prevPage != nil {
		if nextPage != nil {
			prevPage.NextPageMin = nextKL.Key
		} else {
			prevPage.NextPageMin = emptyHash
		}
		prevBytes, _ := serializeNFTokenPage(prevPage)
		if prevBytes != nil {
			view.Update(prevKL, prevBytes)
		}
	}

	if nextPage != nil {
		if prevPage != nil {
			nextPage.PreviousPageMin = prevKL.Key
		} else {
			nextPage.PreviousPageMin = emptyHash
		}
		nextBytes, _ := serializeNFTokenPage(nextPage)
		if nextBytes != nil {
			view.Update(nextKL, nextBytes)
		}
	}

	view.Erase(kl)
	pagesRemoved = 1

	// After removing the page, try merging prev and next if both exist
	if prevPage != nil && nextPage != nil {
		// Re-read them since they were just updated
		prevData2, err := view.Read(prevKL)
		if err == nil {
			p1, _ := state.ParseNFTokenPage(prevData2)
			nextData2, err := view.Read(nextKL)
			if err == nil {
				p2, _ := state.ParseNFTokenPage(nextData2)
				if p1 != nil && p2 != nil && doMergePages(view, prevKL, p1, nextKL, p2) {
					pagesRemoved++
				}
			}
		}
	}

	return tx.TesSUCCESS, pagesRemoved
}

// doMergePages merges p1's tokens into p2 (p1 is lower, p2 is higher).
// Returns true if merge happened. p1 is erased if merged.
// Reference: rippled NFTokenUtils.cpp mergePages
func doMergePages(
	view tx.LedgerView,
	p1KL keylet.Keylet, p1 *state.NFTokenPageData,
	p2KL keylet.Keylet, p2 *state.NFTokenPageData,
) bool {
	if len(p1.NFTokens)+len(p2.NFTokens) > dirMaxTokensPerPage {
		return false
	}

	// Merge all tokens into p2 (higher page)
	merged := make([]state.NFTokenData, 0, len(p1.NFTokens)+len(p2.NFTokens))
	i, j := 0, 0
	for i < len(p1.NFTokens) && j < len(p2.NFTokens) {
		if compareNFTokenID(p1.NFTokens[i].NFTokenID, p2.NFTokens[j].NFTokenID) < 0 {
			merged = append(merged, p1.NFTokens[i])
			i++
		} else {
			merged = append(merged, p2.NFTokens[j])
			j++
		}
	}
	for ; i < len(p1.NFTokens); i++ {
		merged = append(merged, p1.NFTokens[i])
	}
	for ; j < len(p2.NFTokens); j++ {
		merged = append(merged, p2.NFTokens[j])
	}

	p2.NFTokens = merged

	// Unlink p1: p2's PreviousPageMin = p1's PreviousPageMin
	var emptyHash [32]byte
	p2.PreviousPageMin = emptyHash

	if p1.PreviousPageMin != emptyHash {
		p2.PreviousPageMin = p1.PreviousPageMin

		// Update p0's NextPageMin to point to p2
		p0KL := keylet.Keylet{Type: p1KL.Type, Key: p1.PreviousPageMin}
		p0Data, err := view.Read(p0KL)
		if err == nil {
			p0, err := state.ParseNFTokenPage(p0Data)
			if err == nil {
				p0.NextPageMin = p2KL.Key
				p0Bytes, _ := serializeNFTokenPage(p0)
				if p0Bytes != nil {
					view.Update(p0KL, p0Bytes)
				}
			}
		}
	}

	p2Bytes, _ := serializeNFTokenPage(p2)
	if p2Bytes != nil {
		view.Update(p2KL, p2Bytes)
	}
	view.Erase(p1KL)

	return true
}

// ---------------------------------------------------------------------------
// transferNFToken — transfers an NFToken from one account to another
// Reference: rippled NFTokenUtils.cpp removeToken + insertToken
// ---------------------------------------------------------------------------

// transferNFTokenResult holds the result of an NFToken transfer, including
// page changes for both sender and recipient so callers can properly adjust
// OwnerCount (using ctx.Account for the submitter's account).
type transferNFTokenResult struct {
	Result           tx.Result
	FromPagesRemoved int
	ToPagesCreated   int
}

func transferNFToken(from, to [20]byte, tokenID [32]byte, view tx.LedgerView, fixPageLinks bool) transferNFTokenResult {
	// Find the token on the sender's pages
	_, _, idx, found := findToken(view, from, tokenID)
	if !found {
		return transferNFTokenResult{Result: tx.TefINTERNAL}
	}

	// Re-locate to get the page data (findToken returns a copy)
	kl, page, err := locatePage(view, from, tokenID)
	if err != nil || page == nil {
		return transferNFTokenResult{Result: tx.TefINTERNAL}
	}
	_ = kl

	// Extract the token data
	tokenData := page.NFTokens[idx]

	// Remove from sender using removeToken
	result, pagesRemoved := removeToken(view, from, tokenID, fixPageLinks)
	if result != tx.TesSUCCESS {
		return transferNFTokenResult{Result: result}
	}

	// Insert into recipient
	insertResult := insertNFToken(to, tokenData, view)
	if insertResult.Result != tx.TesSUCCESS {
		return transferNFTokenResult{Result: insertResult.Result}
	}

	// Return page deltas — callers handle OwnerCount adjustments
	return transferNFTokenResult{
		Result:           tx.TesSUCCESS,
		FromPagesRemoved: pagesRemoved,
		ToPagesCreated:   insertResult.PagesCreated,
	}
}

// ---------------------------------------------------------------------------
// Offer management — deleteTokenOffer with proper directory cleanup
// Reference: rippled NFTokenUtils.cpp deleteTokenOffer
// ---------------------------------------------------------------------------

// deleteTokenOffer deletes an NFToken offer and removes it from directories.
// It handles:
// 1. Reading the offer to get owner, token ID, flags
// 2. Removing from owner's directory (using OwnerNode)
// 3. Removing from NFTBuys/NFTSells directory (using NFTokenOfferNode)
// 4. Erasing the offer SLE
// 5. Decrementing owner's OwnerCount
// 6. Refunding escrowed amount for buy offers
func deleteTokenOffer(view tx.LedgerView, offerKL keylet.Keylet) error {
	offerData, err := view.Read(offerKL)
	if err != nil {
		return err
	}

	offer, err := state.ParseNFTokenOffer(offerData)
	if err != nil {
		return err
	}

	// Remove from owner's directory
	ownerDirKey := keylet.OwnerDir(offer.Owner)
	state.DirRemove(view, ownerDirKey, offer.OwnerNode, offerKL.Key, false)

	// Remove from NFTBuys or NFTSells directory
	isSellOffer := offer.Flags&lsfSellNFToken != 0
	var tokenDirKey keylet.Keylet
	if isSellOffer {
		tokenDirKey = keylet.NFTSells(offer.NFTokenID)
	} else {
		tokenDirKey = keylet.NFTBuys(offer.NFTokenID)
	}
	state.DirRemove(view, tokenDirKey, offer.NFTokenOfferNode, offerKL.Key, false)

	// Erase the offer
	view.Erase(offerKL)

	return nil
}

// deleteNFTokenOffersResult holds the result of deleting NFToken offers
type deleteNFTokenOffersResult struct {
	TotalDeleted int
	SelfDeleted  int // offers owned by selfAccountID
}

// deleteNFTokenOffers deletes offers for an NFToken using DirForEach.
// selfAccountID identifies the ctx.Account — offers from this account
// are counted separately so the caller can adjust ctx.Account.OwnerCount
// (since the engine overwrites view changes for ctx.Account).
// Reference: rippled NFTokenUtils.cpp removeTokenOffersWithLimit
func deleteNFTokenOffers(tokenID [32]byte, sellOffers bool, limit int, view tx.LedgerView, selfAccountID [20]byte) deleteNFTokenOffersResult {
	result := deleteNFTokenOffersResult{}
	if limit <= 0 {
		return result
	}

	var dirKey keylet.Keylet
	if sellOffers {
		dirKey = keylet.NFTSells(tokenID)
	} else {
		dirKey = keylet.NFTBuys(tokenID)
	}

	exists, _ := view.Exists(dirKey)
	if !exists {
		return result
	}

	// Collect all offer keys first, then delete (can't modify during iteration)
	var offerKeys [][32]byte
	state.DirForEach(view, dirKey, func(itemKey [32]byte) error {
		if len(offerKeys) < limit {
			offerKeys = append(offerKeys, itemKey)
		}
		return nil
	})

	for _, offerKeyBytes := range offerKeys {
		offerKL := keylet.Keylet{Key: offerKeyBytes}

		offerData, err := view.Read(offerKL)
		if err != nil {
			continue
		}

		offer, err := state.ParseNFTokenOffer(offerData)
		if err != nil {
			continue
		}

		isSelf := offer.Owner == selfAccountID

		// NFToken buy offers do NOT escrow XRP — no refund needed on deletion.
		// Reference: rippled NFTokenUtils.cpp deleteTokenOffer — no balance adjustment

		// Decrement owner count (only via view for non-self accounts)
		if !isSelf {
			adjustOwnerCountViaView(view, offer.Owner, -1)
		}

		// Remove from owner directory
		ownerDirKey := keylet.OwnerDir(offer.Owner)
		state.DirRemove(view, ownerDirKey, offer.OwnerNode, offerKL.Key, false)

		// Erase the offer
		view.Erase(offerKL)

		result.TotalDeleted++
		if isSelf {
			result.SelfDeleted++
		}
	}

	return result
}

// notTooManyOffers checks whether the total number of buy + sell offers
// for a token exceeds maxDeletableTokenOfferEntries.
// Reference: rippled NFTokenUtils.cpp notTooManyOffers
func notTooManyOffers(view tx.LedgerView, tokenID [32]byte) tx.Result {
	totalOffers := 0

	// Count buy offers
	buysKey := keylet.NFTBuys(tokenID)
	if exists, _ := view.Exists(buysKey); exists {
		state.DirForEach(view, buysKey, func(itemKey [32]byte) error {
			totalOffers++
			if totalOffers > maxDeletableTokenOfferEntries {
				return fmt.Errorf("too many")
			}
			return nil
		})
	}

	// Count sell offers
	sellsKey := keylet.NFTSells(tokenID)
	if exists, _ := view.Exists(sellsKey); exists {
		state.DirForEach(view, sellsKey, func(itemKey [32]byte) error {
			totalOffers++
			if totalOffers > maxDeletableTokenOfferEntries {
				return fmt.Errorf("too many")
			}
			return nil
		})
	}

	if totalOffers > maxDeletableTokenOfferEntries {
		return tx.TefTOO_BIG
	}

	return tx.TesSUCCESS
}

// ---------------------------------------------------------------------------
// Brokered mode and direct offer acceptance helpers
// ---------------------------------------------------------------------------

// acceptNFTokenBrokeredMode handles brokered NFToken sales
// Reference: rippled NFTokenAcceptOffer.cpp doApply (brokered mode)
func (n *NFTokenAcceptOffer) acceptNFTokenBrokeredMode(ctx *tx.ApplyContext, accountID [20]byte,
	buyOffer, sellOffer *state.NFTokenOfferData, buyOfferKey, sellOfferKey keylet.Keylet) tx.Result {

	if buyOffer.NFTokenID != sellOffer.NFTokenID {
		return tx.TecNFTOKEN_BUY_SELL_MISMATCH
	}

	buyIsXRP := buyOffer.AmountIOU == nil
	sellIsXRP := sellOffer.AmountIOU == nil
	if buyIsXRP != sellIsXRP {
		return tx.TecNFTOKEN_BUY_SELL_MISMATCH
	}
	if !buyIsXRP && !sellIsXRP {
		if buyOffer.AmountIOU.Currency != sellOffer.AmountIOU.Currency ||
			buyOffer.AmountIOU.Issuer != sellOffer.AmountIOU.Issuer {
			return tx.TecNFTOKEN_BUY_SELL_MISMATCH
		}
	}

	if buyOffer.Owner == sellOffer.Owner {
		return tx.TecCANT_ACCEPT_OWN_NFTOKEN_OFFER
	}

	// Verify the seller owns the token
	sellerID := sellOffer.Owner
	if _, _, _, found := findToken(ctx.View, sellerID, sellOffer.NFTokenID); !found {
		return tx.TecNO_PERMISSION
	}

	// Amount comparison — IOU-aware
	if buyIsXRP {
		if buyOffer.Amount < sellOffer.Amount {
			return tx.TecINSUFFICIENT_PAYMENT
		}
	} else {
		buyAmount := offerIOUToAmount(buyOffer)
		sellAmount := offerIOUToAmount(sellOffer)
		if buyAmount.Compare(sellAmount) < 0 {
			return tx.TecINSUFFICIENT_PAYMENT
		}
	}

	if buyOffer.HasDestination && buyOffer.Destination != accountID {
		return tx.TecNO_PERMISSION
	}
	if sellOffer.HasDestination && sellOffer.Destination != accountID {
		return tx.TecNO_PERMISSION
	}

	buyerID := buyOffer.Owner

	var brokerFee uint64
	var brokerFeeIOU tx.Amount
	if n.NFTokenBrokerFee != nil {
		brokerFeeIsXRP := n.NFTokenBrokerFee.Currency == ""
		if brokerFeeIsXRP != buyIsXRP {
			return tx.TecNFTOKEN_BUY_SELL_MISMATCH
		}

		if buyIsXRP {
			brokerFee = uint64(n.NFTokenBrokerFee.Drops())
			if brokerFee >= buyOffer.Amount {
				return tx.TecINSUFFICIENT_PAYMENT
			}
			if sellOffer.Amount > buyOffer.Amount-brokerFee {
				return tx.TecINSUFFICIENT_PAYMENT
			}
		} else {
			brokerFeeIOU = *n.NFTokenBrokerFee
			buyAmount := offerIOUToAmount(buyOffer)
			sellAmount := offerIOUToAmount(sellOffer)
			if brokerFeeIOU.Compare(buyAmount) >= 0 {
				return tx.TecINSUFFICIENT_PAYMENT
			}
			remainder, _ := buyAmount.Sub(brokerFeeIOU)
			if sellAmount.Compare(remainder) > 0 {
				return tx.TecINSUFFICIENT_PAYMENT
			}
		}
	}

	transferFee := getNFTTransferFee(sellOffer.NFTokenID)
	nftIssuerID := getNFTIssuer(sellOffer.NFTokenID)

	if !buyIsXRP {
		// IOU brokered payment path
		buyAmount := offerIOUToAmount(buyOffer)

		// Step 1: Pay broker fee
		if n.NFTokenBrokerFee != nil && !brokerFeeIOU.IsZero() {
			if r := payIOU(ctx, buyerID, accountID, brokerFeeIOU); r != tx.TesSUCCESS {
				return r
			}
			buyAmount, _ = buyAmount.Sub(brokerFeeIOU)
		}

		// Step 2: Pay issuer cut from transfer fee
		if transferFee != 0 && !buyAmount.IsZero() && sellerID != nftIssuerID && buyerID != nftIssuerID {
			// Check issuer trust line (fixEnforceNFTokenTrustline)
			nftFlags := getNFTFlagsFromID(sellOffer.NFTokenID)
			if r := checkIssuerTrustLineForAccept(ctx, nftIssuerID, buyAmount, nftFlags); r != tx.TesSUCCESS {
				return r
			}
			issuerCut := buyAmount.MulRatio(uint32(transferFee), transferFeeDivisor32, true)
			if !issuerCut.IsZero() {
				if r := payIOU(ctx, buyerID, nftIssuerID, issuerCut); r != tx.TesSUCCESS {
					return r
				}
				buyAmount, _ = buyAmount.Sub(issuerCut)
			}
		}

		// Step 3: Pay seller remainder
		if !buyAmount.IsZero() {
			if r := payIOU(ctx, buyerID, sellerID, buyAmount); r != tx.TesSUCCESS {
				return r
			}
		}
	} else {
		// XRP brokered payment path — deduct from buyer, pay broker + issuer + seller
		// Reference: rippled NFTokenAcceptOffer.cpp — uses accountSend()
		amount := buyOffer.Amount

		// Deduct full amount from buyer's account
		buyerKey := keylet.Account(buyerID)
		buyerData, err := ctx.View.Read(buyerKey)
		if err != nil {
			return tx.TefINTERNAL
		}
		buyerAccount, err := state.ParseAccountRoot(buyerData)
		if err != nil {
			return tx.TefINTERNAL
		}
		if buyerAccount.Balance < amount {
			return tx.TecINSUFFICIENT_FUNDS
		}
		buyerAccount.Balance -= amount
		buyerUpdated, _ := state.SerializeAccountRoot(buyerAccount)
		if err := ctx.View.Update(buyerKey, buyerUpdated); err != nil {
			return tx.TefINTERNAL
		}

		var issuerCut uint64
		if transferFee != 0 && amount > 0 {
			issuerCut = (amount - brokerFee) * uint64(transferFee) / transferFeeDivisor
			if sellerID == nftIssuerID || buyerID == nftIssuerID {
				issuerCut = 0
			}
		}

		// Pay broker fee
		if brokerFee > 0 {
			ctx.Account.Balance += brokerFee
			amount -= brokerFee
		}

		// Pay issuer cut
		if issuerCut > 0 {
			issuerKey := keylet.Account(nftIssuerID)
			issuerData, err := ctx.View.Read(issuerKey)
			if err == nil {
				issuerAccount, err := state.ParseAccountRoot(issuerData)
				if err == nil {
					issuerAccount.Balance += issuerCut
					issuerUpdatedData, _ := state.SerializeAccountRoot(issuerAccount)
					ctx.View.Update(issuerKey, issuerUpdatedData)
				}
			}
			amount -= issuerCut
		}

		// Pay seller
		sellerKey := keylet.Account(sellerID)
		sellerData, err := ctx.View.Read(sellerKey)
		if err != nil {
			return tx.TefINTERNAL
		}
		sellerAccount, err := state.ParseAccountRoot(sellerData)
		if err != nil {
			return tx.TefINTERNAL
		}
		sellerAccount.Balance += amount
		sellerUpdatedData, err := state.SerializeAccountRoot(sellerAccount)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(sellerKey, sellerUpdatedData); err != nil {
			return tx.TefINTERNAL
		}
	}

	// Transfer the NFToken from seller to buyer
	fixPageLinks := ctx.Rules().Enabled(amendment.FeatureFixNFTokenPageLinks)
	xferResult := transferNFToken(sellerID, buyerID, sellOffer.NFTokenID, ctx.View, fixPageLinks)
	if xferResult.Result != tx.TesSUCCESS {
		return xferResult.Result
	}

	// Adjust OwnerCount for page changes from the transfer.
	adjustOwnerCountViaView(ctx.View, sellerID, -xferResult.FromPagesRemoved)
	adjustOwnerCountViaView(ctx.View, buyerID, xferResult.ToPagesCreated)

	// Delete both offers using proper directory cleanup
	deleteTokenOffer(ctx.View, buyOfferKey)
	deleteTokenOffer(ctx.View, sellOfferKey)

	// Decrement owner counts for the deleted offers
	adjustOwnerCountViaView(ctx.View, buyerID, -1)
	adjustOwnerCountViaView(ctx.View, sellerID, -1)

	return tx.TesSUCCESS
}

// acceptNFTokenSellOfferDirect handles direct sell offer acceptance
func (n *NFTokenAcceptOffer) acceptNFTokenSellOfferDirect(ctx *tx.ApplyContext, accountID [20]byte,
	sellOffer *state.NFTokenOfferData, sellOfferKey keylet.Keylet) tx.Result {

	if sellOffer.HasDestination && sellOffer.Destination != accountID {
		return tx.TecNO_PERMISSION
	}

	// Verify seller owns the token
	sellerID := sellOffer.Owner
	if _, _, _, found := findToken(ctx.View, sellerID, sellOffer.NFTokenID); !found {
		return tx.TecNO_PERMISSION
	}

	transferFee := getNFTTransferFee(sellOffer.NFTokenID)
	nftIssuerID := getNFTIssuer(sellOffer.NFTokenID)

	if sellOffer.AmountIOU != nil {
		// IOU payment path
		sellAmount := offerIOUToAmount(sellOffer)

		// Calculate issuer cut for transfer fee
		if transferFee != 0 && !sellAmount.IsZero() && sellerID != nftIssuerID && accountID != nftIssuerID {
			// Check issuer trust line (fixEnforceNFTokenTrustline)
			nftFlags := getNFTFlagsFromID(sellOffer.NFTokenID)
			if r := checkIssuerTrustLineForAccept(ctx, nftIssuerID, sellAmount, nftFlags); r != tx.TesSUCCESS {
				return r
			}
			issuerCut := sellAmount.MulRatio(uint32(transferFee), transferFeeDivisor32, true)
			if !issuerCut.IsZero() {
				if r := payIOU(ctx, accountID, nftIssuerID, issuerCut); r != tx.TesSUCCESS {
					return r
				}
				sellAmount, _ = sellAmount.Sub(issuerCut)
			}
		}

		// Pay seller remainder
		if !sellAmount.IsZero() {
			if r := payIOU(ctx, accountID, sellerID, sellAmount); r != tx.TesSUCCESS {
				return r
			}
		}
	} else {
		// XRP payment path (existing logic)
		amount := sellOffer.Amount
		var issuerCut uint64

		if transferFee != 0 && amount > 0 {
			issuerCut = amount * uint64(transferFee) / transferFeeDivisor
			if sellerID == nftIssuerID || accountID == nftIssuerID {
				issuerCut = 0
			}
		}

		totalCost := amount
		if ctx.Account.Balance < totalCost {
			return tx.TecINSUFFICIENT_FUNDS
		}
		ctx.Account.Balance -= totalCost

		if issuerCut > 0 {
			issuerKey := keylet.Account(nftIssuerID)
			issuerData, err := ctx.View.Read(issuerKey)
			if err == nil {
				issuerAccount, err := state.ParseAccountRoot(issuerData)
				if err == nil {
					issuerAccount.Balance += issuerCut
					issuerUpdatedData, _ := state.SerializeAccountRoot(issuerAccount)
					ctx.View.Update(issuerKey, issuerUpdatedData)
				}
			}
			amount -= issuerCut
		}

		sellerKey := keylet.Account(sellerID)
		sellerData, err := ctx.View.Read(sellerKey)
		if err != nil {
			return tx.TefINTERNAL
		}
		sellerAccount, err := state.ParseAccountRoot(sellerData)
		if err != nil {
			return tx.TefINTERNAL
		}
		sellerAccount.Balance += amount
		sellerUpdatedData, err := state.SerializeAccountRoot(sellerAccount)
		if err != nil {
			return tx.TefINTERNAL
		}
		if err := ctx.View.Update(sellerKey, sellerUpdatedData); err != nil {
			return tx.TefINTERNAL
		}
	}

	// Transfer the NFToken
	fixPageLinks := ctx.Rules().Enabled(amendment.FeatureFixNFTokenPageLinks)
	xferResult := transferNFToken(sellerID, accountID, sellOffer.NFTokenID, ctx.View, fixPageLinks)
	if xferResult.Result != tx.TesSUCCESS {
		return xferResult.Result
	}

	// Adjust OwnerCount for page changes from the transfer.
	adjustOwnerCountViaView(ctx.View, sellerID, -xferResult.FromPagesRemoved)
	ctx.Account.OwnerCount += uint32(xferResult.ToPagesCreated)

	// Delete offer with proper directory cleanup
	deleteTokenOffer(ctx.View, sellOfferKey)

	// Decrement seller's owner count for the deleted offer
	adjustOwnerCountViaView(ctx.View, sellerID, -1)

	return tx.TesSUCCESS
}

// acceptNFTokenBuyOfferDirect handles direct buy offer acceptance
func (n *NFTokenAcceptOffer) acceptNFTokenBuyOfferDirect(ctx *tx.ApplyContext, accountID [20]byte,
	buyOffer *state.NFTokenOfferData, buyOfferKey keylet.Keylet) tx.Result {

	// Verify account owns the token
	if _, _, _, found := findToken(ctx.View, accountID, buyOffer.NFTokenID); !found {
		return tx.TecNO_PERMISSION
	}

	if buyOffer.HasDestination && buyOffer.Destination != accountID {
		return tx.TecNO_PERMISSION
	}

	buyerID := buyOffer.Owner
	transferFee := getNFTTransferFee(buyOffer.NFTokenID)
	nftIssuerID := getNFTIssuer(buyOffer.NFTokenID)

	if buyOffer.AmountIOU != nil {
		// IOU payment path: buyer pays seller via trust lines
		buyAmount := offerIOUToAmount(buyOffer)

		// Calculate issuer cut for transfer fee
		if transferFee != 0 && !buyAmount.IsZero() && accountID != nftIssuerID && buyerID != nftIssuerID {
			// Check issuer trust line (fixEnforceNFTokenTrustline)
			nftFlags := getNFTFlagsFromID(buyOffer.NFTokenID)
			if r := checkIssuerTrustLineForAccept(ctx, nftIssuerID, buyAmount, nftFlags); r != tx.TesSUCCESS {
				return r
			}
			issuerCut := buyAmount.MulRatio(uint32(transferFee), transferFeeDivisor32, true)
			if !issuerCut.IsZero() {
				if r := payIOU(ctx, buyerID, nftIssuerID, issuerCut); r != tx.TesSUCCESS {
					return r
				}
				buyAmount, _ = buyAmount.Sub(issuerCut)
			}
		}

		// Pay seller remainder
		if !buyAmount.IsZero() {
			if r := payIOU(ctx, buyerID, accountID, buyAmount); r != tx.TesSUCCESS {
				return r
			}
		}
	} else {
		// XRP payment path — deduct from buyer, pay issuer + seller
		// Reference: rippled NFTokenAcceptOffer.cpp — uses accountSend()
		amount := buyOffer.Amount

		// Deduct full amount from buyer's account (buyer != ctx.Account)
		buyerKey := keylet.Account(buyerID)
		buyerData, err := ctx.View.Read(buyerKey)
		if err != nil {
			return tx.TefINTERNAL
		}
		buyerAccount, err := state.ParseAccountRoot(buyerData)
		if err != nil {
			return tx.TefINTERNAL
		}
		if buyerAccount.Balance < amount {
			return tx.TecINSUFFICIENT_FUNDS
		}
		buyerAccount.Balance -= amount
		buyerUpdated, _ := state.SerializeAccountRoot(buyerAccount)
		if err := ctx.View.Update(buyerKey, buyerUpdated); err != nil {
			return tx.TefINTERNAL
		}

		var issuerCut uint64
		if transferFee != 0 && amount > 0 {
			issuerCut = amount * uint64(transferFee) / transferFeeDivisor
			if accountID == nftIssuerID || buyerID == nftIssuerID {
				issuerCut = 0
			}
		}

		if issuerCut > 0 {
			issuerKey := keylet.Account(nftIssuerID)
			issuerData, err := ctx.View.Read(issuerKey)
			if err == nil {
				issuerAccount, err := state.ParseAccountRoot(issuerData)
				if err == nil {
					issuerAccount.Balance += issuerCut
					issuerUpdatedData, _ := state.SerializeAccountRoot(issuerAccount)
					ctx.View.Update(issuerKey, issuerUpdatedData)
				}
			}
			amount -= issuerCut
		}

		// Pay seller (the account accepting the buy offer)
		ctx.Account.Balance += amount
	}

	// Transfer the NFToken
	fixPageLinks := ctx.Rules().Enabled(amendment.FeatureFixNFTokenPageLinks)
	xferResult := transferNFToken(accountID, buyerID, buyOffer.NFTokenID, ctx.View, fixPageLinks)
	if xferResult.Result != tx.TesSUCCESS {
		return xferResult.Result
	}

	// Adjust OwnerCount for page changes from the transfer.
	for i := 0; i < xferResult.FromPagesRemoved; i++ {
		if ctx.Account.OwnerCount > 0 {
			ctx.Account.OwnerCount--
		}
	}
	adjustOwnerCountViaView(ctx.View, buyerID, xferResult.ToPagesCreated)

	// Delete offer with proper directory cleanup
	deleteTokenOffer(ctx.View, buyOfferKey)

	// Decrement buyer's owner count for the deleted offer
	adjustOwnerCountViaView(ctx.View, buyerID, -1)

	return tx.TesSUCCESS
}

// adjustOwnerCountViaView adjusts an account's OwnerCount through the view.
// Use this for accounts that are NOT ctx.Account (the submitter).
func adjustOwnerCountViaView(view tx.LedgerView, accountID [20]byte, delta int) {
	if delta == 0 {
		return
	}
	acctKey := keylet.Account(accountID)
	acctData, err := view.Read(acctKey)
	if err != nil {
		return
	}
	acct, err := state.ParseAccountRoot(acctData)
	if err != nil {
		return
	}
	if delta > 0 {
		acct.OwnerCount += uint32(delta)
	} else {
		for i := 0; i < -delta; i++ {
			if acct.OwnerCount > 0 {
				acct.OwnerCount--
			}
		}
	}
	updated, _ := state.SerializeAccountRoot(acct)
	if updated != nil {
		view.Update(acctKey, updated)
	}
}

// ---------------------------------------------------------------------------
// Serialization helpers
// ---------------------------------------------------------------------------

// SerializeNFTokenPage serializes an NFToken page ledger entry.
// Exported so that LedgerStateFix can use it to repair pages.
func SerializeNFTokenPage(page *state.NFTokenPageData) ([]byte, error) {
	return serializeNFTokenPage(page)
}

// serializeNFTokenPage serializes an NFToken page ledger entry
func serializeNFTokenPage(page *state.NFTokenPageData) ([]byte, error) {
	jsonObj := map[string]any{
		"LedgerEntryType": "NFTokenPage",
		"Flags":           uint32(0),
	}

	var emptyHash [32]byte
	if page.PreviousPageMin != emptyHash {
		jsonObj["PreviousPageMin"] = strings.ToUpper(hex.EncodeToString(page.PreviousPageMin[:]))
	}

	if page.NextPageMin != emptyHash {
		jsonObj["NextPageMin"] = strings.ToUpper(hex.EncodeToString(page.NextPageMin[:]))
	}

	if len(page.NFTokens) > 0 {
		nfTokens := make([]map[string]any, len(page.NFTokens))
		for i, token := range page.NFTokens {
			nfToken := map[string]any{
				"NFToken": map[string]any{
					"NFTokenID": strings.ToUpper(hex.EncodeToString(token.NFTokenID[:])),
				},
			}
			if token.URI != "" {
				nfToken["NFToken"].(map[string]any)["URI"] = token.URI
			}
			nfTokens[i] = nfToken
		}
		jsonObj["NFTokens"] = nfTokens
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode NFTokenPage: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// amountToCodecFormat converts a tx.Amount to the format expected by binarycodec.Encode.
// XRP → string of drops ("1000000"), IOU → map[string]any{"value":"10","currency":"USD","issuer":"rAddr"}
func amountToCodecFormat(amt tx.Amount) any {
	if amt.IsNative() {
		return fmt.Sprintf("%d", amt.Drops())
	}
	return map[string]any{
		"value":    amt.IOU().String(),
		"currency": amt.Currency,
		"issuer":   amt.Issuer,
	}
}

// serializeNFTokenOfferRaw serializes an NFToken offer ledger entry from primitive parameters.
// amount can be a string (XRP drops) or map[string]any (IOU).
func serializeNFTokenOfferRaw(
	ownerID [20]byte, tokenID [32]byte,
	amount any, flags uint32,
	ownerNode, offerNode uint64,
	destination string, expiration *uint32,
) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	jsonObj := map[string]any{
		"LedgerEntryType":  "NFTokenOffer",
		"Account":          ownerAddress,
		"Amount":           amount,
		"NFTokenID":        strings.ToUpper(hex.EncodeToString(tokenID[:])),
		"OwnerNode":        fmt.Sprintf("%x", ownerNode),
		"NFTokenOfferNode": fmt.Sprintf("%x", offerNode),
		"Flags":            flags,
	}

	if expiration != nil {
		jsonObj["Expiration"] = *expiration
	}

	if destination != "" {
		jsonObj["Destination"] = destination
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode NFTokenOffer: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// serializeNFTokenOffer serializes an NFToken offer from an NFTokenCreateOffer transaction.
func serializeNFTokenOffer(nftTx *NFTokenCreateOffer, ownerID [20]byte, tokenID [32]byte, sequence uint32, ownerNode uint64, offerNode uint64) ([]byte, error) {
	return serializeNFTokenOfferRaw(
		ownerID, tokenID,
		amountToCodecFormat(nftTx.Amount), nftTx.GetFlags(),
		ownerNode, offerNode,
		nftTx.Destination, nftTx.Expiration,
	)
}

// tokenOfferCreateApply creates a sell offer for a newly minted NFToken.
// This is the shared logic used by both NFTokenCreateOffer and NFTokenMint (with Amount).
// Reference: rippled NFTokenUtils.cpp tokenOfferCreateApply
func tokenOfferCreateApply(
	ctx *tx.ApplyContext,
	accountID [20]byte,
	tokenID [32]byte,
	amount *tx.Amount,
	destination string,
	expiration *uint32,
	seqProxy uint32,
) tx.Result {
	// Check reserve
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
	if ctx.Account.Balance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}

	// Create offer key
	offerKey := keylet.NFTokenOffer(accountID, seqProxy)

	// Insert into owner's directory
	ownerDirKey := keylet.OwnerDir(accountID)
	dirResult, err := state.DirInsert(ctx.View, ownerDirKey, offerKey.Key, nil)
	if err != nil {
		return tx.TefINTERNAL
	}
	ownerNode := dirResult.Page

	// Insert into NFTSells directory (mint always creates sell offers)
	tokenDirKey := keylet.NFTSells(tokenID)
	tokenDirResult, err := state.DirInsert(ctx.View, tokenDirKey, offerKey.Key, nil)
	if err != nil {
		return tx.TefINTERNAL
	}
	offerNode := tokenDirResult.Page

	// Serialize the offer
	flags := uint32(NFTokenCreateOfferFlagSellNFToken) // Always a sell offer

	offerData, err := serializeNFTokenOfferRaw(
		accountID, tokenID,
		amountToCodecFormat(*amount), flags,
		ownerNode, offerNode,
		destination, expiration,
	)
	if err != nil {
		return tx.TefINTERNAL
	}

	if err := ctx.View.Insert(offerKey, offerData); err != nil {
		return tx.TefINTERNAL
	}

	// Increase owner count
	ctx.Account.OwnerCount++

	return tx.TesSUCCESS
}

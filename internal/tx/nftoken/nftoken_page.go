package nftoken

import (
	"bytes"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

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
	fixDirV1 bool,
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
	return splitPage(view, owner, tokenID, cpKL, cp, base, first, fixDirV1)
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
	fixDirV1 bool,
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
		// Prior to fixNFTokenDirV1 we simply stopped.
		// Reference: rippled NFTokenUtils.cpp lines 145-147
		if !fixDirV1 {
			return keylet.Keylet{}, nil, 0, nil
		}

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
	// Reference: rippled — fixNFTokenDirV1 corrects off-by-one: uses < instead of <=
	// Without fixDirV1: return (first.key <= np.key) ? np : cp
	// With fixDirV1:    return (first.key < np.key) ? np : cp
	useNp := false
	if fixDirV1 {
		useNp = bytes.Compare(first.Key[:], npKL.Key[:]) < 0
	} else {
		useNp = bytes.Compare(first.Key[:], npKL.Key[:]) <= 0
	}
	if useNp {
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

// ---------------------------------------------------------------------------
// insertNFToken — inserts an NFToken into the owner's token directory
// Reference: rippled NFTokenUtils.cpp insertToken
// ---------------------------------------------------------------------------

func insertNFToken(ownerID [20]byte, token state.NFTokenData, view tx.LedgerView, fixDirV1 bool) insertNFTokenResult {
	pageKL, page, pagesCreated, err := getPageForToken(view, ownerID, token.NFTokenID, fixDirV1)
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

func transferNFToken(from, to [20]byte, tokenID [32]byte, view tx.LedgerView, fixPageLinks bool, fixDirV1 bool) transferNFTokenResult {
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
	insertResult := insertNFToken(to, tokenData, view, fixDirV1)
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

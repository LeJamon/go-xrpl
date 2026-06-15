package nftoken

import (
	"bytes"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// ---------------------------------------------------------------------------
// Page traversal using Succ (SHAMap upper_bound)
// Reference: rippled NFTokenUtils.cpp locatePage, getPageForToken
// ---------------------------------------------------------------------------

// succNFTokenPage finds the first NFToken page key strictly greater than
// first.Key and at most last.Key, falling back to last.Key if none exists.
// This mirrors rippled's:
//
//	view.succ(first.key, last.key.next()).value_or(last.key)
//
// where succ(start, upperBound) returns the first key > start and < upperBound.
func succNFTokenPage(view tx.LedgerView, first, last keylet.Keylet) ([32]byte, error) {
	foundKey, _, found, err := view.Succ(first.Key)
	if err != nil {
		return [32]byte{}, err
	}
	if found && bytes.Compare(foundKey[:], last.Key[:]) <= 0 {
		return foundKey, nil
	}
	return last.Key, nil
}

// locatePage finds the NFToken page that should contain (or does contain)
// the given token. Uses Succ for direct state tree lookup instead of walking
// the linked list.
// Returns (pageKeylet, pageData, err). If the owner has no pages, returns nil data.
// Reference: rippled NFTokenUtils.cpp locatePage — uses view.succ()
func locatePage(view tx.LedgerView, owner [20]byte, tokenID [32]byte) (keylet.Keylet, *state.NFTokenPageData, error) {
	base := keylet.NFTokenPageMin(owner)
	first := keylet.NFTokenPageForToken(base, tokenID)
	last := keylet.NFTokenPageMax(owner)

	pageKey, err := succNFTokenPage(view, first, last)
	if err != nil {
		return keylet.Keylet{}, nil, err
	}

	kl := keylet.Keylet{Type: last.Type, Key: pageKey}
	data, err := view.Read(kl)
	if err != nil || data == nil {
		return keylet.Keylet{}, nil, nil
	}

	page, err := state.ParseNFTokenPage(data)
	if err != nil {
		return keylet.Keylet{}, nil, err
	}

	return kl, page, nil
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
	Result       ter.Result
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
// or returns nil if no pages exist. Uses Succ for direct state tree lookup.
// Reference: rippled getPageForToken — view.succ(first.key, last.key.next()).value_or(last.key)
func locatePageForInsert(view tx.LedgerView, owner [20]byte, first, maxKL keylet.Keylet) (keylet.Keylet, *state.NFTokenPageData, error) {
	pageKey, err := succNFTokenPage(view, first, maxKL)
	if err != nil {
		return keylet.Keylet{}, nil, err
	}

	kl := keylet.Keylet{Type: maxKL.Type, Key: pageKey}
	data, err := view.Read(kl)
	if err != nil || data == nil {
		return keylet.Keylet{}, nil, nil
	}

	page, err := state.ParseNFTokenPage(data)
	if err != nil {
		return keylet.Keylet{}, nil, err
	}

	return kl, page, nil
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
		for i := range narr {
			if getNFTPageKey(narr[i].NFTokenID) == cmp {
				splitIdx = i
				break
			}
		}
	}

	// If splitIdx is still -1, something is confused. rippled returns nullptr
	// here, which the caller maps to tecNO_SUITABLE_NFTOKEN_PAGE.
	if splitIdx == -1 {
		return keylet.Keylet{}, nil, 0, nil
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

		// Point the old previous page's NextPageMin at the new page. If that page
		// cannot be read the link is dangling; rippled's getPageForToken skips
		// the back-link update in that case rather than failing the transaction.
		prevKL := keylet.Keylet{Type: cpKL.Type, Key: cp.PreviousPageMin}
		if prevData, err := view.Read(prevKL); err == nil && prevData != nil {
			prevPage, err := state.ParseNFTokenPage(prevData)
			if err != nil {
				return keylet.Keylet{}, nil, 0, err
			}
			prevPage.NextPageMin = npKL.Key
			prevBytes, err := serializeNFTokenPage(prevPage)
			if err != nil {
				return keylet.Keylet{}, nil, 0, err
			}
			if err := view.Update(prevKL, prevBytes); err != nil {
				return keylet.Keylet{}, nil, 0, err
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
		return insertNFTokenResult{Result: ter.TefINTERNAL}
	}

	if page == nil {
		return insertNFTokenResult{Result: ter.TecNO_SUITABLE_NFTOKEN_PAGE}
	}

	// Insert token in sorted position
	page.NFTokens = insertNFTokenSorted(page.NFTokens, token)

	// Serialize and update
	pageBytes, err := serializeNFTokenPage(page)
	if err != nil {
		return insertNFTokenResult{Result: ter.TefINTERNAL}
	}

	if err := view.Update(pageKL, pageBytes); err != nil {
		return insertNFTokenResult{Result: ter.TefINTERNAL}
	}

	return insertNFTokenResult{Result: ter.TesSUCCESS, PagesCreated: pagesCreated}
}

// ---------------------------------------------------------------------------
// removeToken — removes an NFToken from the owner's directory with page merging
// Reference: rippled NFTokenUtils.cpp removeToken
// ---------------------------------------------------------------------------

// loadAdjacentPage reads the NFTokenPage referenced by a PreviousPageMin/
// NextPageMin link. An unset (zero) link yields a nil page and success; a set
// link that cannot be read is a broken directory and yields tefEXCEPTION,
// matching rippled's loadPage which throws (caught as tefEXCEPTION) rather than
// treating it as "no neighbour".
func loadAdjacentPage(view tx.LedgerView, pageType entry.Type, link [32]byte) (*state.NFTokenPageData, keylet.Keylet, ter.Result) {
	var emptyHash [32]byte
	if link == emptyHash {
		return nil, keylet.Keylet{}, ter.TesSUCCESS
	}
	kl := keylet.Keylet{Type: pageType, Key: link}
	data, err := view.Read(kl)
	if err != nil || data == nil {
		return nil, kl, ter.TefEXCEPTION
	}
	page, err := state.ParseNFTokenPage(data)
	if err != nil {
		return nil, kl, ter.TefINTERNAL
	}
	return page, kl, ter.TesSUCCESS
}

func removeToken(view tx.LedgerView, owner [20]byte, tokenID [32]byte, fixPageLinks bool) (ter.Result, int) {
	kl, page, err := locatePage(view, owner, tokenID)
	if err != nil {
		return ter.TefINTERNAL, 0
	}
	if page == nil {
		return ter.TecNO_ENTRY, 0
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
		return ter.TecNO_ENTRY, 0
	}

	// Load prev and next pages. A set link to an unreadable page is a broken
	// directory, not an absent neighbour.
	var emptyHash [32]byte
	prevPage, prevKL, r := loadAdjacentPage(view, kl.Type, page.PreviousPageMin)
	if r != ter.TesSUCCESS {
		return r, 0
	}
	nextPage, nextKL, r := loadAdjacentPage(view, kl.Type, page.NextPageMin)
	if r != ter.TesSUCCESS {
		return r, 0
	}

	pagesRemoved := 0

	if len(page.NFTokens) > 0 {
		// Page not empty — update it and try to consolidate
		pageBytes, err := serializeNFTokenPage(page)
		if err != nil {
			return ter.TefINTERNAL, 0
		}
		if err := view.Update(kl, pageBytes); err != nil {
			return ter.TefINTERNAL, 0
		}

		// Try merging with previous page
		if prevPage != nil {
			merged, res := doMergePages(view, prevKL, prevPage, kl, page)
			if res != ter.TesSUCCESS {
				return res, 0
			}
			if merged {
				pagesRemoved++
				// After merge, "page" has been absorbed into kl (p2).
				// Re-read kl for potential second merge.
				klData, err := view.Read(kl)
				if err != nil || klData == nil {
					return ter.TefINTERNAL, 0
				}
				page, err = state.ParseNFTokenPage(klData)
				if err != nil {
					return ter.TefINTERNAL, 0
				}
			}
		}

		// Try merging with next page
		if nextPage != nil {
			merged, res := doMergePages(view, kl, page, nextKL, nextPage)
			if res != ter.TesSUCCESS {
				return res, 0
			}
			if merged {
				pagesRemoved++
			}
		}

		return ter.TesSUCCESS, pagesRemoved
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
			// Fix link from prev's previous; that page must exist.
			ppPage, ppKL, r := loadAdjacentPage(view, kl.Type, prevPage.PreviousPageMin)
			if r != ter.TesSUCCESS {
				return r, 0
			}
			ppPage.NextPageMin = kl.Key
			ppBytes, err := serializeNFTokenPage(ppPage)
			if err != nil {
				return ter.TefINTERNAL, 0
			}
			if err := view.Update(ppKL, ppBytes); err != nil {
				return ter.TefINTERNAL, 0
			}
		} else {
			page.PreviousPageMin = emptyHash
		}
		pageBytes, err := serializeNFTokenPage(page)
		if err != nil {
			return ter.TefINTERNAL, 0
		}
		if err := view.Update(kl, pageBytes); err != nil {
			return ter.TefINTERNAL, 0
		}
		if err := view.Erase(prevKL); err != nil {
			return ter.TefINTERNAL, 0
		}
		return ter.TesSUCCESS, 1
	}

	// Not the max page or no prev — unlink and remove
	if prevPage != nil {
		if nextPage != nil {
			prevPage.NextPageMin = nextKL.Key
		} else {
			prevPage.NextPageMin = emptyHash
		}
		prevBytes, err := serializeNFTokenPage(prevPage)
		if err != nil {
			return ter.TefINTERNAL, 0
		}
		if err := view.Update(prevKL, prevBytes); err != nil {
			return ter.TefINTERNAL, 0
		}
	}

	if nextPage != nil {
		if prevPage != nil {
			nextPage.PreviousPageMin = prevKL.Key
		} else {
			nextPage.PreviousPageMin = emptyHash
		}
		nextBytes, err := serializeNFTokenPage(nextPage)
		if err != nil {
			return ter.TefINTERNAL, 0
		}
		if err := view.Update(nextKL, nextBytes); err != nil {
			return ter.TefINTERNAL, 0
		}
	}

	if err := view.Erase(kl); err != nil {
		return ter.TefINTERNAL, 0
	}
	pagesRemoved = 1

	// After removing the page, try merging prev and next if both exist
	if prevPage != nil && nextPage != nil {
		// Re-read them since they were just updated.
		prevData2, err := view.Read(prevKL)
		if err != nil || prevData2 == nil {
			return ter.TefINTERNAL, 0
		}
		p1, err := state.ParseNFTokenPage(prevData2)
		if err != nil {
			return ter.TefINTERNAL, 0
		}
		nextData2, err := view.Read(nextKL)
		if err != nil || nextData2 == nil {
			return ter.TefINTERNAL, 0
		}
		p2, err := state.ParseNFTokenPage(nextData2)
		if err != nil {
			return ter.TefINTERNAL, 0
		}
		merged, res := doMergePages(view, prevKL, p1, nextKL, p2)
		if res != ter.TesSUCCESS {
			return res, 0
		}
		if merged {
			pagesRemoved++
		}
	}

	return ter.TesSUCCESS, pagesRemoved
}

// doMergePages merges p1's tokens into p2 (p1 is lower, p2 is higher).
// Returns true if merge happened. p1 is erased if merged.
// Reference: rippled NFTokenUtils.cpp mergePages
func doMergePages(
	view tx.LedgerView,
	p1KL keylet.Keylet, p1 *state.NFTokenPageData,
	p2KL keylet.Keylet, p2 *state.NFTokenPageData,
) (bool, ter.Result) {
	// Reject inconsistent inputs before mutating state, matching rippled
	// mergePages: the pages must be ordered (p1 below p2) and linked to each
	// other. A violation is a corrupt directory, surfaced as tefEXCEPTION.
	if bytes.Compare(p1KL.Key[:], p2KL.Key[:]) >= 0 {
		return false, ter.TefEXCEPTION
	}
	if p1.NextPageMin != p2KL.Key {
		return false, ter.TefEXCEPTION
	}
	if p2.PreviousPageMin != p1KL.Key {
		return false, ter.TefEXCEPTION
	}

	if len(p1.NFTokens)+len(p2.NFTokens) > dirMaxTokensPerPage {
		return false, ter.TesSUCCESS
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

		// Update p0's NextPageMin to point to p2; p0 must exist.
		p0, p0KL, r := loadAdjacentPage(view, p1KL.Type, p1.PreviousPageMin)
		if r != ter.TesSUCCESS {
			return false, r
		}
		p0.NextPageMin = p2KL.Key
		p0Bytes, err := serializeNFTokenPage(p0)
		if err != nil {
			return false, ter.TefINTERNAL
		}
		if err := view.Update(p0KL, p0Bytes); err != nil {
			return false, ter.TefINTERNAL
		}
	}

	p2Bytes, err := serializeNFTokenPage(p2)
	if err != nil {
		return false, ter.TefINTERNAL
	}
	if err := view.Update(p2KL, p2Bytes); err != nil {
		return false, ter.TefINTERNAL
	}
	if err := view.Erase(p1KL); err != nil {
		return false, ter.TefINTERNAL
	}

	return true, ter.TesSUCCESS
}

// ---------------------------------------------------------------------------
// transferNFToken — transfers an NFToken from one account to another
// Reference: rippled NFTokenUtils.cpp removeToken + insertToken
// ---------------------------------------------------------------------------

// transferNFTokenResult holds the result of an NFToken transfer, including
// page changes for both sender and recipient so callers can properly adjust
// OwnerCount (using ctx.Account for the submitter's account).
type transferNFTokenResult struct {
	Result           ter.Result
	FromPagesRemoved int
	ToPagesCreated   int
}

func transferNFToken(from, to [20]byte, tokenID [32]byte, view tx.LedgerView, fixPageLinks bool, fixDirV1 bool) transferNFTokenResult {
	// Locate the sender's page holding the token and read its data in one lookup.
	_, page, err := locatePage(view, from, tokenID)
	if err != nil || page == nil {
		return transferNFTokenResult{Result: ter.TefINTERNAL}
	}

	var tokenData state.NFTokenData
	found := false
	for _, t := range page.NFTokens {
		if t.NFTokenID == tokenID {
			tokenData = t
			found = true
			break
		}
	}
	if !found {
		return transferNFTokenResult{Result: ter.TefINTERNAL}
	}

	// Remove from sender using removeToken
	result, pagesRemoved := removeToken(view, from, tokenID, fixPageLinks)
	if result != ter.TesSUCCESS {
		return transferNFTokenResult{Result: result}
	}

	// Insert into recipient
	insertResult := insertNFToken(to, tokenData, view, fixDirV1)
	if insertResult.Result != ter.TesSUCCESS {
		return transferNFTokenResult{Result: insertResult.Result}
	}

	// Return page deltas — callers handle OwnerCount adjustments
	return transferNFTokenResult{
		Result:           ter.TesSUCCESS,
		FromPagesRemoved: pagesRemoved,
		ToPagesCreated:   insertResult.PagesCreated,
	}
}

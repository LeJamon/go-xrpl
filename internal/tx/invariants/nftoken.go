package invariants

import (
	"bytes"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

// nftPageMaskLocal is the low 96 bits (bytes 20-31) used for NFT page grouping.
// Matches keylet.nftPageMask.
var nftPageMaskLocal = [32]byte{
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
}

// nftPageMaskMax is the maximum page boundary (all 1s in low 96 bits).
var nftPageMaskMax = nftPageMaskLocal

// dirMaxTokensPerPage is the maximum number of NFTokens per page.
const dirMaxTokensPerPage = 32

// andKey256 computes a & mask for 32-byte keys.
func andKey256(a, mask [32]byte) [32]byte {
	var result [32]byte
	for i := 0; i < 32; i++ {
		result[i] = a[i] & mask[i]
	}
	return result
}

// notKey256 computes ^mask for a 32-byte key.
func notKey256(mask [32]byte) [32]byte {
	var result [32]byte
	for i := 0; i < 32; i++ {
		result[i] = ^mask[i]
	}
	return result
}

// compareKey256 returns -1, 0, or 1 comparing two 32-byte keys.
func compareKey256(a, b [32]byte) int {
	return bytes.Compare(a[:], b[:])
}

// isZeroKey256 returns true if the key is all zeros.
func isZeroKey256(k [32]byte) bool {
	var zero [32]byte
	return k == zero
}

// compareNFTokenIDs compares two NFToken IDs for sorting.
// Sort by low 96 bits first; if equal, sort by full 256-bit value.
// Reference: rippled NFTokenUtils.cpp compareTokens()
func compareNFTokenIDs(a, b [32]byte) int {
	aLow := andKey256(a, nftPageMaskLocal)
	bLow := andKey256(b, nftPageMaskLocal)
	cmp := compareKey256(aLow, bLow)
	if cmp != 0 {
		return cmp
	}
	return compareKey256(a, b)
}

// checkNFTokenCountTracking verifies that MintedNFTokens and BurnedNFTokens
// fields on AccountRoot entries change correctly based on transaction type.
// Reference: rippled InvariantCheck.cpp — NFTokenCountTracking (lines 1181-1284)
func checkNFTokenCountTracking(txType string, result Result, entries []InvariantEntry) *InvariantViolation {
	var beforeMintedTotal, beforeBurnedTotal uint32
	var afterMintedTotal, afterBurnedTotal uint32

	for _, e := range entries {
		if e.EntryType != "AccountRoot" {
			continue
		}

		// Sum minted/burned from before state
		if e.Before != nil {
			if acct, err := state.ParseAccountRoot(e.Before); err == nil {
				beforeMintedTotal += acct.MintedNFTokens
				beforeBurnedTotal += acct.BurnedNFTokens
			}
		}

		// Sum minted/burned from after state.
		// In rippled, even erased SLEs pass their data as the "after" parameter
		// to visitEntry (ApplyStateTable.cpp line 88-92). For deleted AccountRoots,
		// we must include the before data in the after totals too, matching rippled's
		// behavior where the SLE is passed as "after" even for Action::erase.
		if e.IsDelete && e.Before != nil {
			// Erased entry: rippled passes the SLE data as "after",
			// so the before values appear in both before and after totals.
			if acct, err := state.ParseAccountRoot(e.Before); err == nil {
				afterMintedTotal += acct.MintedNFTokens
				afterBurnedTotal += acct.BurnedNFTokens
			}
		} else if e.After != nil {
			if acct, err := state.ParseAccountRoot(e.After); err == nil {
				afterMintedTotal += acct.MintedNFTokens
				afterBurnedTotal += acct.BurnedNFTokens
			}
		}
	}

	// For non-mint/burn transactions, counts must not change.
	if txType != "NFTokenMint" && txType != "NFTokenBurn" {
		if beforeMintedTotal != afterMintedTotal {
			return &InvariantViolation{
				Name:    "NFTokenCountTracking",
				Message: "the number of minted tokens changed without a mint transaction",
			}
		}
		if beforeBurnedTotal != afterBurnedTotal {
			return &InvariantViolation{
				Name:    "NFTokenCountTracking",
				Message: "the number of burned tokens changed without a burn transaction",
			}
		}
		return nil
	}

	if txType == "NFTokenMint" {
		// Successful mint must increase the minted count.
		if result == TesSUCCESS && beforeMintedTotal >= afterMintedTotal {
			return &InvariantViolation{
				Name:    "NFTokenCountTracking",
				Message: "successful minting didn't increase the number of minted tokens",
			}
		}
		// Failed mint must not change the minted count.
		if result != TesSUCCESS && beforeMintedTotal != afterMintedTotal {
			return &InvariantViolation{
				Name:    "NFTokenCountTracking",
				Message: "failed minting changed the number of minted tokens",
			}
		}
		// Mint must not change the burned count.
		if beforeBurnedTotal != afterBurnedTotal {
			return &InvariantViolation{
				Name:    "NFTokenCountTracking",
				Message: "minting changed the number of burned tokens",
			}
		}
	}

	if txType == "NFTokenBurn" {
		// Successful burn must increase the burned count.
		if result == TesSUCCESS && beforeBurnedTotal >= afterBurnedTotal {
			return &InvariantViolation{
				Name:    "NFTokenCountTracking",
				Message: "successful burning didn't increase the number of burned tokens",
			}
		}
		// Failed burn must not change the burned count.
		if result != TesSUCCESS && beforeBurnedTotal != afterBurnedTotal {
			return &InvariantViolation{
				Name:    "NFTokenCountTracking",
				Message: "failed burning changed the number of burned tokens",
			}
		}
		// Burn must not change the minted count.
		if beforeMintedTotal != afterMintedTotal {
			return &InvariantViolation{
				Name:    "NFTokenCountTracking",
				Message: "burning changed the number of minted tokens",
			}
		}
	}

	return nil
}

// checkNFTokenPageSLE checks a single NFTokenPage SLE for invariant violations.
// Returns boolean flags for each type of violation found.
func checkNFTokenPageSLE(
	pageKey [32]byte,
	page *state.NFTokenPageData,
	isDelete bool,
) (badLink, badEntry, badSort, badURI, invalidSize bool) {
	accountBits := notKey256(nftPageMaskLocal)
	account := andKey256(pageKey, accountBits)
	hiLimit := andKey256(pageKey, nftPageMaskLocal)

	// Check PreviousPageMin link
	if !isZeroKey256(page.PreviousPageMin) {
		prevAccount := andKey256(page.PreviousPageMin, accountBits)
		if prevAccount != account {
			badLink = true
		}
		prevPageBits := andKey256(page.PreviousPageMin, nftPageMaskLocal)
		// hiLimit must be > prevPageBits
		if compareKey256(hiLimit, prevPageBits) <= 0 {
			badLink = true
		}
	}

	// Check NextPageMin link
	if !isZeroKey256(page.NextPageMin) {
		nextAccount := andKey256(page.NextPageMin, accountBits)
		if nextAccount != account {
			badLink = true
		}
		nextPageBits := andKey256(page.NextPageMin, nftPageMaskLocal)
		// hiLimit must be < nextPageBits
		if compareKey256(hiLimit, nextPageBits) >= 0 {
			badLink = true
		}
	}

	// Check token count
	tokenCount := len(page.NFTokens)
	if (!isDelete && tokenCount == 0) || tokenCount > dirMaxTokensPerPage {
		invalidSize = true
	}

	// Determine lower bound for token page bits
	var loLimit [32]byte
	if !isZeroKey256(page.PreviousPageMin) {
		loLimit = andKey256(page.PreviousPageMin, nftPageMaskLocal)
	}
	// else loLimit stays all zeros

	// Verify tokens are sorted and within bounds.
	// rippled initializes loCmp = loLimit and then for each token checks:
	//   if (!nft::compareTokens(loCmp, tokenID)) badSort = true
	// compareTokens(a, b) returns true if a < b.
	// So !compareTokens(loCmp, tokenID) means loCmp >= tokenID => badSort.
	loCmp := loLimit
	for _, token := range page.NFTokens {
		if compareNFTokenIDs(loCmp, token.NFTokenID) >= 0 {
			badSort = true
		}
		loCmp = token.NFTokenID

		// Check token is within page bounds
		tokenPageBits := andKey256(token.NFTokenID, nftPageMaskLocal)
		if compareKey256(tokenPageBits, loLimit) < 0 || compareKey256(tokenPageBits, hiLimit) >= 0 {
			badEntry = true
		}
	}

	return
}

// checkNFTokenPageURIEmpty checks if any NFToken on the page has an explicitly
// present but empty URI. This requires scanning the raw binary to detect
// field presence with zero length.
func checkNFTokenPageURIEmpty(data []byte) bool {
	offset := 0
	for offset < len(data) {
		if offset >= len(data) {
			break
		}
		header := data[offset]
		offset++

		typeCode := (header >> 4) & 0x0F
		fieldCode := header & 0x0F

		if typeCode == 0 {
			if offset >= len(data) {
				break
			}
			typeCode = data[offset]
			offset++
		}
		if fieldCode == 0 {
			if offset >= len(data) {
				break
			}
			fieldCode = data[offset]
			offset++
		}

		switch typeCode {
		case 1: // UInt16
			if offset+2 > len(data) {
				return false
			}
			offset += 2
		case 2: // UInt32
			if offset+4 > len(data) {
				return false
			}
			offset += 4
		case 3: // UInt64
			if offset+8 > len(data) {
				return false
			}
			offset += 8
		case 5: // Hash256
			if offset+32 > len(data) {
				return false
			}
			offset += 32
		case 7: // Blob (VL-encoded)
			if offset >= len(data) {
				return false
			}
			length := int(data[offset])
			extra := 1
			if length > 192 {
				if offset+1 >= len(data) {
					return false
				}
				length = 193 + ((length-193)<<8 | int(data[offset+1]))
				extra = 2
			}
			offset += extra
			// URI is Blob fieldCode 5
			if fieldCode == 5 && length == 0 {
				return true // Found empty URI
			}
			if offset+length > len(data) {
				return false
			}
			offset += length
		case 8: // AccountID
			if offset+20 > len(data) {
				return false
			}
			offset += 20
		case 14, 15: // STObject/STArray structural markers
			continue
		default:
			return false
		}
	}
	return false
}

func checkValidNFTokenPage(entries []InvariantEntry, view ReadView, rules *amendment.Rules) *InvariantViolation {
	var (
		badLink          bool
		badEntry         bool
		badSort          bool
		badURI           bool
		invalidSize      bool
		deletedFinalPage bool
		deletedLink      bool
	)

	for _, e := range entries {
		// Only process NFTokenPage entries.
		// rippled checks: if before and before->getType() != ltNFTOKEN_PAGE, skip
		//                 if after and after->getType() != ltNFTOKEN_PAGE, skip
		if e.EntryType != "NFTokenPage" {
			continue
		}

		// Check before state
		if e.Before != nil {
			page, err := state.ParseNFTokenPage(e.Before)
			if err == nil {
				bl, be, bs, _, is := checkNFTokenPageSLE(e.Key, page, e.IsDelete)
				badLink = badLink || bl
				badEntry = badEntry || be
				badSort = badSort || bs
				invalidSize = invalidSize || is

				// Check for empty URI in raw binary
				if checkNFTokenPageURIEmpty(e.Before) {
					badURI = true
				}

				// Check if deleting final page (low 96 bits == all 1s)
				// with PreviousPageMin present.
				// Reference: rippled line 1098-1102
				if e.IsDelete {
					pageBits := andKey256(e.Key, nftPageMaskLocal)
					if pageBits == nftPageMaskMax && !isZeroKey256(page.PreviousPageMin) {
						deletedFinalPage = true
					}
				}
			}
		}

		// Check after state
		if e.After != nil {
			page, err := state.ParseNFTokenPage(e.After)
			if err == nil {
				bl, be, bs, _, is := checkNFTokenPageSLE(e.Key, page, false)
				badLink = badLink || bl
				badEntry = badEntry || be
				badSort = badSort || bs
				invalidSize = invalidSize || is

				// Check for empty URI in raw binary
				if checkNFTokenPageURIEmpty(e.After) {
					badURI = true
				}
			}
		}

		// Check for lost NextPageMin link (modification, not deletion).
		// If before has NextPageMin and after doesn't, and this is not the final page.
		// Reference: rippled lines 1108-1121
		if !e.IsDelete && e.Before != nil && e.After != nil {
			pageBits := andKey256(e.Key, nftPageMaskLocal)
			if pageBits != nftPageMaskMax {
				beforePage, errB := state.ParseNFTokenPage(e.Before)
				afterPage, errA := state.ParseNFTokenPage(e.After)
				if errB == nil && errA == nil {
					if !isZeroKey256(beforePage.NextPageMin) && isZeroKey256(afterPage.NextPageMin) {
						deletedLink = true
					}
				}
			}
		}
	}

	// Finalize — check violations in the same order as rippled
	if badLink {
		return &InvariantViolation{
			Name:    "ValidNFTokenPage",
			Message: "NFT page is improperly linked",
		}
	}
	if badEntry {
		return &InvariantViolation{
			Name:    "ValidNFTokenPage",
			Message: "NFT found in incorrect page",
		}
	}
	if badSort {
		return &InvariantViolation{
			Name:    "ValidNFTokenPage",
			Message: "NFTs on page are not sorted",
		}
	}
	if badURI {
		return &InvariantViolation{
			Name:    "ValidNFTokenPage",
			Message: "NFT contains empty URI",
		}
	}
	if invalidSize {
		return &InvariantViolation{
			Name:    "ValidNFTokenPage",
			Message: "NFT page has invalid size",
		}
	}

	// Amendment-gated checks
	if rules != nil && rules.Enabled(amendment.FeatureFixNFTokenPageLinks) {
		if deletedFinalPage {
			return &InvariantViolation{
				Name:    "ValidNFTokenPage",
				Message: "Last NFT page deleted with non-empty directory",
			}
		}
		if deletedLink {
			return &InvariantViolation{
				Name:    "ValidNFTokenPage",
				Message: "Lost NextMinPage link",
			}
		}
	}

	return nil
}

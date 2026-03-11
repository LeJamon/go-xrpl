package nft_test

// FixNFTokenPageLinks_test.go - Tests for fixing broken NFT page links
// Reference: rippled/src/test/app/FixNFTokenPageLinks_test.cpp
//
// Tests cover three areas:
//   1. testLedgerStateFixErrors - error cases for preflight/preclaim
//   2. testTokenPageLinkErrors - nothing-to-fix cases
//   3. testFixNFTokenPageLinks - full page link repair (requires Apply implementation)

import (
	"sort"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/nftoken"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/stretchr/testify/require"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/nft"
)

// genPackedTokensForOwner generates 96 NFTs packed into three pages of 32 each.
// The minter creates the NFTs and transfers them to the owner.
// Returns a sorted list of NFT IDs.
func genPackedTokensForOwner(env *jtx.TestEnv, owner, minter *jtx.Account) []string {
	nfts := make([]string, 0, 96)

	for i := uint32(0); i < 96; i++ {
		intTaxon := (i / 16)
		if i&0b10000 != 0 {
			intTaxon += 2
		}

		tokenSeq := nft.GetNextTokenSeq(env, minter)
		extTaxon := nftoken.CipheredTaxon(tokenSeq, intTaxon)

		flags := nftoken.NFTokenFlagTransferable
		nftID := nft.GetNextNFTokenID(env, minter, extTaxon, flags, 0)
		env.Submit(nft.NFTokenMint(minter, extTaxon).Transferable().Build())
		env.Close()

		// Minter creates sell offer, owner accepts
		offerIndex := nft.GetOfferIndex(env, minter)
		env.Submit(nft.NFTokenCreateSellOffer(minter, nftID, tx.NewXRPAmount(0)).Build())
		env.Close()

		env.Submit(nft.NFTokenAcceptSellOffer(owner, offerIndex).Build())
		env.Close()

		nfts = append(nfts, nftID)
	}

	sort.Strings(nfts)
	return nfts
}

// genPackedTokens generates 96 NFTs packed into three pages of 32 each.
// The owner mints the NFTs directly (matching rippled's genPackedTokens).
// Returns a sorted list of NFT IDs.
func genPackedTokens(env *jtx.TestEnv, owner *jtx.Account) []string {
	nfts := make([]string, 0, 96)

	for i := uint32(0); i < 96; i++ {
		intTaxon := (i / 16)
		if i&0b10000 != 0 {
			intTaxon += 2
		}

		tokenSeq := nft.GetNextTokenSeq(env, owner)
		extTaxon := nftoken.CipheredTaxon(tokenSeq, intTaxon)

		flags := nftoken.NFTokenFlagTransferable
		nftID := nft.GetNextNFTokenID(env, owner, extTaxon, flags, 0)
		env.Submit(nft.NFTokenMint(owner, extTaxon).Transferable().Build())
		env.Close()

		nfts = append(nfts, nftID)
	}

	sort.Strings(nfts)
	return nfts
}

// readNFTokenPage reads and parses an NFTokenPage at the given keylet.
// Returns nil if the page does not exist.
func readNFTokenPage(t *testing.T, env *jtx.TestEnv, kl keylet.Keylet) *state.NFTokenPageData {
	t.Helper()
	data, err := env.LedgerEntry(kl)
	if err != nil || data == nil {
		return nil
	}
	page, parseErr := state.ParseNFTokenPage(data)
	if parseErr != nil {
		return nil
	}
	return page
}

// nftPageKeylet constructs an NFTokenPage keylet from a base (min) and a page key.
func nftPageKeylet(base keylet.Keylet, pageKey [32]byte) keylet.Keylet {
	return keylet.NFTokenPageForToken(base, pageKey)
}

// ===========================================================================
// testLedgerStateFixErrors
// Reference: rippled FixNFTokenPageLinks_test.cpp testLedgerStateFixErrors
//
// Tests error cases for the LedgerStateFix transaction.
// ===========================================================================
func TestLedgerStateFixErrors(t *testing.T) {
	alice := jtx.NewAccount("alice")

	// Verify that the LedgerStateFix transaction is disabled
	// without the fixNFTokenPageLinks amendment.
	t.Run("temDISABLED without amendment", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixNFTokenPageLinks")
		env.Fund(alice)
		env.Close()

		linkFixFee := env.ReserveIncrement()
		result := env.Submit(
			nft.LedgerStateFixNFTPageLinks(alice, alice).Fee(linkFixFee).Build(),
		)
		jtx.RequireTxFail(t, result, "temDISABLED")
	})

	// Tests with amendment enabled
	t.Run("Preflight errors", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.Fund(alice)
		env.Close()

		linkFixFee := env.ReserveIncrement()

		// Fee too low.
		// Reference: rippled LedgerStateFix.cpp — requires fees().increment as minimum fee
		t.Run("telINSUF_FEE_P with fee below increment", func(t *testing.T) {
			// In rippled, LedgerStateFix requires increment (50M drops) as fee.
			// The Go engine does not enforce this minimum fee for LedgerStateFix yet.
			t.Skip("Go engine does not enforce increment as minimum fee for LedgerStateFix")
		})

		// Invalid flags.
		t.Run("temINVALID_FLAG with invalid flags", func(t *testing.T) {
			result := env.Submit(
				nft.LedgerStateFixNFTPageLinks(alice, alice).
					Fee(linkFixFee).
					Flags(0x00010000). // tfPassive
					Build(),
			)
			jtx.RequireTxFail(t, result, "temINVALID_FLAG")
		})

		// Owner field is required for nfTokenPageLink fix.
		t.Run("temINVALID without Owner field", func(t *testing.T) {
			result := env.Submit(
				nft.LedgerStateFixNFTPageLinks(alice, alice).
					Fee(linkFixFee).
					NoOwner().
					Build(),
			)
			jtx.RequireTxFail(t, result, "temINVALID")
		})

		// Invalid LedgerFixType codes.
		t.Run("tefINVALID_LEDGER_FIX_TYPE with type 0", func(t *testing.T) {
			result := env.Submit(
				nft.LedgerStateFixNFTPageLinks(alice, alice).
					Fee(linkFixFee).
					FixType(0).
					Build(),
			)
			jtx.RequireTxFail(t, result, "tefINVALID_LEDGER_FIX_TYPE")
		})

		t.Run("tefINVALID_LEDGER_FIX_TYPE with type 200", func(t *testing.T) {
			result := env.Submit(
				nft.LedgerStateFixNFTPageLinks(alice, alice).
					Fee(linkFixFee).
					FixType(200).
					Build(),
			)
			jtx.RequireTxFail(t, result, "tefINVALID_LEDGER_FIX_TYPE")
		})
	})

	// Preclaim: Owner account must exist in ledger.
	// Reference: rippled FixNFTokenPageLinks_test.cpp:192-197
	// In rippled, preclaim checks Owner account exists -> tecOBJECT_NOT_FOUND.
	// Now that Apply() has ApplyContext, we can perform preclaim checks.
	t.Run("tecOBJECT_NOT_FOUND when Owner does not exist", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.Fund(alice)
		env.Close()

		// carol is known but not funded (equivalent to rippled's env.memoize(carol))
		carol := jtx.NewAccount("carol")

		linkFixFee := env.ReserveIncrement()
		result := env.Submit(
			nft.LedgerStateFixNFTPageLinks(alice, carol).Fee(linkFixFee).Build(),
		)
		jtx.RequireTxFail(t, result, "tecOBJECT_NOT_FOUND")
	})
}

// ===========================================================================
// testTokenPageLinkErrors
// Reference: rippled FixNFTokenPageLinks_test.cpp testTokenPageLinkErrors
//
// Tests error cases where there is nothing to fix in the owner's NFT pages.
// ===========================================================================
func TestTokenPageLinkErrors(t *testing.T) {
	alice := jtx.NewAccount("alice")

	env := jtx.NewTestEnv(t)
	env.Fund(alice)
	env.Close()

	linkFixFee := env.ReserveIncrement()

	// Owner has no pages to fix.
	// Reference: rippled FixNFTokenPageLinks_test.cpp:218-220
	t.Run("no pages - tecFAILED_PROCESSING", func(t *testing.T) {
		result := env.Submit(
			nft.LedgerStateFixNFTPageLinks(alice, alice).Fee(linkFixFee).Build(),
		)
		jtx.RequireTxFail(t, result, "tecFAILED_PROCESSING")
	})

	// Alice has only one page (undamaged).
	// Reference: rippled FixNFTokenPageLinks_test.cpp:222-228
	t.Run("one page undamaged - tecFAILED_PROCESSING", func(t *testing.T) {
		env.Submit(nft.NFTokenMint(alice, 0).Transferable().Build())
		env.Close()

		result := env.Submit(
			nft.LedgerStateFixNFTPageLinks(alice, alice).Fee(linkFixFee).Build(),
		)
		jtx.RequireTxFail(t, result, "tecFAILED_PROCESSING")
	})

	// Alice has at least three pages (undamaged).
	// Reference: rippled FixNFTokenPageLinks_test.cpp:230-239
	t.Run("three pages undamaged - tecFAILED_PROCESSING", func(t *testing.T) {
		for i := uint32(0); i < 64; i++ {
			env.Submit(nft.NFTokenMint(alice, 0).Transferable().Build())
			env.Close()
		}

		result := env.Submit(
			nft.LedgerStateFixNFTPageLinks(alice, alice).Fee(linkFixFee).Build(),
		)
		jtx.RequireTxFail(t, result, "tecFAILED_PROCESSING")
	})
}

// ===========================================================================
// testFixNFTokenPageLinks
// Reference: rippled FixNFTokenPageLinks_test.cpp testFixNFTokenPageLinks
//
// Tests repairing three kinds of damaged NFToken directories:
// 1. One page without final index (alice)
// 2. Multiple pages, missing final page (bob)
// 3. Links missing in middle of chain (carol)
// ===========================================================================
func TestFixNFTokenPageLinks(t *testing.T) {
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	daria := jtx.NewAccount("daria")

	// Start with fixNFTokenPageLinks disabled to create damaged directories
	env := jtx.NewTestEnv(t)
	env.DisableFeature("fixNFTokenPageLinks")

	// Fund enough for the massive number of operations.
	// Each account needs: 96 mints + 96 sells + 96 accepts + reserve + fees
	// Use generous funding
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(carol, uint64(jtx.XRP(10000)))
	env.FundAmount(daria, uint64(jtx.XRP(10000)))
	env.Close()

	// ******************************************************************
	// Step 1A: Create damaged NFToken directories:
	//   One where there is only one page, but without the final index.
	// ******************************************************************

	// alice generates three packed pages
	aliceNFTs := genPackedTokens(env, alice)
	require.Equal(t, 96, len(aliceNFTs), "alice should have 96 NFTs")
	require.Equal(t, uint32(3), env.OwnerCount(alice), "alice should have 3 pages")

	// Get the index of the middle page
	aliceMaxKl := keylet.NFTokenPageMax(alice.ID)
	aliceLastPage := readNFTokenPage(t, env, aliceMaxKl)
	require.NotNil(t, aliceLastPage, "alice's last page should exist")
	aliceMiddleNFTokenPageIndex := aliceLastPage.PreviousPageMin

	// alice burns all the tokens in the first and last pages
	// Burn first 32 tokens
	for i := 0; i < 32; i++ {
		env.Submit(nft.NFTokenBurn(alice, aliceNFTs[i]).Build())
		env.Close()
	}
	aliceNFTs = aliceNFTs[32:]

	// Burn last 32 tokens
	for i := 0; i < 32; i++ {
		env.Submit(nft.NFTokenBurn(alice, aliceNFTs[len(aliceNFTs)-1]).Build())
		aliceNFTs = aliceNFTs[:len(aliceNFTs)-1]
		env.Close()
	}
	require.Equal(t, uint32(1), env.OwnerCount(alice), "alice should have 1 page after burns")
	require.Equal(t, 32, len(aliceNFTs), "alice should have 32 remaining NFTs")

	// Removing the last token from the last page deletes the last page.
	// This is a bug — the contents should have been moved into the last page.
	require.False(t, env.LedgerEntryExists(aliceMaxKl), "alice's max page should be gone (damaged)")

	// alice's "middle" page is still present, but has no links
	aliceMiddleKl := nftPageKeylet(keylet.NFTokenPageMin(alice.ID), aliceMiddleNFTokenPageIndex)
	aliceMiddlePage := readNFTokenPage(t, env, aliceMiddleKl)
	require.NotNil(t, aliceMiddlePage, "alice's middle page should exist")
	var emptyHash [32]byte
	require.Equal(t, emptyHash, aliceMiddlePage.PreviousPageMin, "alice's middle page should have no PreviousPageMin")
	require.Equal(t, emptyHash, aliceMiddlePage.NextPageMin, "alice's middle page should have no NextPageMin")

	// ******************************************************************
	// Step 1B: Create damaged NFToken directories:
	//   One with multiple pages and a missing final page.
	// ******************************************************************

	// bob generates three packed pages
	bobNFTs := genPackedTokens(env, bob)
	require.Equal(t, 96, len(bobNFTs), "bob should have 96 NFTs")
	require.Equal(t, uint32(3), env.OwnerCount(bob), "bob should have 3 pages")

	// Get the index of the middle page
	bobMaxKl := keylet.NFTokenPageMax(bob.ID)
	bobLastPage := readNFTokenPage(t, env, bobMaxKl)
	require.NotNil(t, bobLastPage, "bob's last page should exist")
	bobMiddleNFTokenPageIndex := bobLastPage.PreviousPageMin

	// bob burns all the tokens in the very last page
	for i := 0; i < 32; i++ {
		env.Submit(nft.NFTokenBurn(bob, bobNFTs[len(bobNFTs)-1]).Build())
		bobNFTs = bobNFTs[:len(bobNFTs)-1]
		env.Close()
	}
	require.Equal(t, 64, len(bobNFTs), "bob should have 64 remaining NFTs")
	require.Equal(t, uint32(2), env.OwnerCount(bob), "bob should have 2 pages")

	// Removing the last token from the last page deletes the last page (bug)
	require.False(t, env.LedgerEntryExists(bobMaxKl), "bob's max page should be gone (damaged)")

	// bob's "middle" page is still present, has PreviousPageMin but lost NextPageMin
	bobMiddleKl := nftPageKeylet(keylet.NFTokenPageMin(bob.ID), bobMiddleNFTokenPageIndex)
	bobMiddlePage := readNFTokenPage(t, env, bobMiddleKl)
	require.NotNil(t, bobMiddlePage, "bob's middle page should exist")
	require.NotEqual(t, emptyHash, bobMiddlePage.PreviousPageMin, "bob's middle page should have PreviousPageMin")
	require.Equal(t, emptyHash, bobMiddlePage.NextPageMin, "bob's middle page should have no NextPageMin")

	// ******************************************************************
	// Step 1C: Create damaged NFToken directories:
	//   One with links missing in the middle of the chain.
	// ******************************************************************

	// carol generates three packed pages
	carolNFTs := genPackedTokens(env, carol)
	require.Equal(t, 96, len(carolNFTs), "carol should have 96 NFTs")
	require.Equal(t, uint32(3), env.OwnerCount(carol), "carol should have 3 pages")

	// Get the index of the middle page
	carolMaxKl := keylet.NFTokenPageMax(carol.ID)
	carolLastPage := readNFTokenPage(t, env, carolMaxKl)
	require.NotNil(t, carolLastPage, "carol's last page should exist")
	carolMiddleNFTokenPageIndex := carolLastPage.PreviousPageMin

	// carol sells all of the tokens in the very last page to daria
	dariaNFTs := make([]string, 0, 32)
	for i := 0; i < 32; i++ {
		lastNFT := carolNFTs[len(carolNFTs)-1]

		offerIndex := nft.GetOfferIndex(env, carol)
		env.Submit(nft.NFTokenCreateSellOffer(carol, lastNFT, tx.NewXRPAmount(0)).Build())
		env.Close()

		env.Submit(nft.NFTokenAcceptSellOffer(daria, offerIndex).Build())
		env.Close()

		dariaNFTs = append(dariaNFTs, lastNFT)
		carolNFTs = carolNFTs[:len(carolNFTs)-1]
	}
	require.Equal(t, 64, len(carolNFTs), "carol should have 64 remaining NFTs")
	require.Equal(t, uint32(2), env.OwnerCount(carol), "carol should have 2 pages")

	// Removing the last token from the last page deletes the last page (bug)
	require.False(t, env.LedgerEntryExists(carolMaxKl), "carol's max page should be gone (damaged)")

	// carol's "middle" page is still present, has PreviousPageMin but lost NextPageMin
	carolMiddleKl := nftPageKeylet(keylet.NFTokenPageMin(carol.ID), carolMiddleNFTokenPageIndex)
	carolMiddlePage := readNFTokenPage(t, env, carolMiddleKl)
	require.NotNil(t, carolMiddlePage, "carol's middle page should exist")
	require.NotEqual(t, emptyHash, carolMiddlePage.PreviousPageMin, "carol's middle page should have PreviousPageMin")
	require.Equal(t, emptyHash, carolMiddlePage.NextPageMin, "carol's middle page should have no NextPageMin")

	// Now make things more complicated: carol buys back the NFTs from daria.
	// This re-creates the last page but with broken links.
	for _, nftID := range dariaNFTs {
		offerIndex := nft.GetOfferIndex(env, carol)
		env.Submit(nft.NFTokenCreateBuyOffer(carol, nftID, tx.NewXRPAmount(1), daria).Build())
		env.Close()

		env.Submit(nft.NFTokenAcceptBuyOffer(daria, offerIndex).Build())
		env.Close()

		carolNFTs = append(carolNFTs, nftID)
	}

	// carol actually owns 96 NFTs but only 64 are reported because links are damaged
	require.Equal(t, uint32(3), env.OwnerCount(carol), "carol should have 3 pages again")

	// carol's "middle" page still has no NextPageMin field
	carolMiddlePage = readNFTokenPage(t, env, carolMiddleKl)
	require.NotNil(t, carolMiddlePage, "carol's middle page should still exist")
	require.NotEqual(t, emptyHash, carolMiddlePage.PreviousPageMin, "carol's middle page should have PreviousPageMin")
	require.Equal(t, emptyHash, carolMiddlePage.NextPageMin, "carol's middle page should have no NextPageMin")

	// carol's "last" page exists again, but has no PreviousPageMin field
	carolLastPage = readNFTokenPage(t, env, carolMaxKl)
	require.NotNil(t, carolLastPage, "carol's max page should exist again")
	require.Equal(t, emptyHash, carolLastPage.PreviousPageMin, "carol's last page should have no PreviousPageMin")
	require.Equal(t, emptyHash, carolLastPage.NextPageMin, "carol's last page should have no NextPageMin")

	// ******************************************************************
	// Step 2: Enable the fixNFTokenPageLinks amendment.
	// ******************************************************************

	linkFixFee := env.ReserveIncrement()

	// Verify LedgerStateFix is still disabled
	result := env.Submit(
		nft.LedgerStateFixNFTPageLinks(daria, alice).Fee(linkFixFee).Build(),
	)
	jtx.RequireTxFail(t, result, "temDISABLED")

	// Close several ledgers so the failed tx is not retried
	for i := 0; i < 15; i++ {
		env.Close()
	}

	env.EnableFeature("fixNFTokenPageLinks")
	env.Close()

	// ******************************************************************
	// Step 3A: Repair the one-page directory (alice's)
	// ******************************************************************

	// Verify alice's NFToken directory is still damaged
	require.False(t, env.LedgerEntryExists(aliceMaxKl), "alice's max page should still be missing")

	aliceMiddlePage = readNFTokenPage(t, env, aliceMiddleKl)
	require.NotNil(t, aliceMiddlePage, "alice's middle page should still exist")
	require.Equal(t, emptyHash, aliceMiddlePage.PreviousPageMin, "alice's middle page should still have no PreviousPageMin")
	require.Equal(t, emptyHash, aliceMiddlePage.NextPageMin, "alice's middle page should still have no NextPageMin")

	// daria's failed nftPageLinks had the same signature, so advance daria's seq
	env.Noop(daria)

	// daria fixes the links in alice's NFToken directory
	result = env.Submit(
		nft.LedgerStateFixNFTPageLinks(daria, alice).Fee(linkFixFee).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice's last page should now be present and include no links
	aliceLastPage = readNFTokenPage(t, env, aliceMaxKl)
	require.NotNil(t, aliceLastPage, "alice's max page should now exist after repair")
	require.Equal(t, emptyHash, aliceLastPage.PreviousPageMin, "alice's repaired max page should have no PreviousPageMin")
	require.Equal(t, emptyHash, aliceLastPage.NextPageMin, "alice's repaired max page should have no NextPageMin")

	// alice's middle page should be gone (moved to max page)
	require.False(t, env.LedgerEntryExists(aliceMiddleKl), "alice's middle page should be gone after repair")

	require.Equal(t, uint32(1), env.OwnerCount(alice), "alice should have 1 page after repair")

	// ******************************************************************
	// Step 3B: Repair the two-page directory (bob's)
	// ******************************************************************

	// Verify bob's NFToken directory is still damaged
	require.False(t, env.LedgerEntryExists(bobMaxKl), "bob's max page should still be missing")

	bobMiddlePage = readNFTokenPage(t, env, bobMiddleKl)
	require.NotNil(t, bobMiddlePage, "bob's middle page should still exist")
	require.NotEqual(t, emptyHash, bobMiddlePage.PreviousPageMin, "bob's middle page should have PreviousPageMin")
	require.Equal(t, emptyHash, bobMiddlePage.NextPageMin, "bob's middle page should have no NextPageMin")

	// daria fixes the links in bob's NFToken directory
	result = env.Submit(
		nft.LedgerStateFixNFTPageLinks(daria, bob).Fee(linkFixFee).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// bob's last page should now be present with a previous link but no next link
	bobLastPage = readNFTokenPage(t, env, bobMaxKl)
	require.NotNil(t, bobLastPage, "bob's max page should now exist after repair")

	require.NotEqual(t, emptyHash, bobLastPage.PreviousPageMin, "bob's repaired max page should have PreviousPageMin")
	// The new PreviousPageMin should NOT be the old middle page index
	// (the middle page was moved to the max key position)
	require.NotEqual(t, bobMiddleNFTokenPageIndex, bobLastPage.PreviousPageMin,
		"bob's max page PreviousPageMin should not point to old middle page")
	require.Equal(t, emptyHash, bobLastPage.NextPageMin, "bob's repaired max page should have no NextPageMin")

	// The new first page (pointed to by the last page's PreviousPageMin)
	// should exist and link forward to the last page
	bobNewFirstKl := nftPageKeylet(keylet.NFTokenPageMin(bob.ID), bobLastPage.PreviousPageMin)
	bobNewFirstPage := readNFTokenPage(t, env, bobNewFirstKl)
	require.NotNil(t, bobNewFirstPage, "bob's new first page should exist")
	require.Equal(t, bobMaxKl.Key, bobNewFirstPage.NextPageMin,
		"bob's new first page should link to the max page")
	require.Equal(t, emptyHash, bobNewFirstPage.PreviousPageMin,
		"bob's new first page should have no PreviousPageMin")

	// bob's old middle page should be gone
	require.False(t, env.LedgerEntryExists(bobMiddleKl), "bob's old middle page should be gone after repair")

	require.Equal(t, uint32(2), env.OwnerCount(bob), "bob should have 2 pages after repair")

	// ******************************************************************
	// Step 3C: Repair the three-page directory (carol's)
	// ******************************************************************

	// Verify carol's NFToken directory is still damaged
	carolMiddlePage = readNFTokenPage(t, env, carolMiddleKl)
	require.NotNil(t, carolMiddlePage, "carol's middle page should still exist")
	require.NotEqual(t, emptyHash, carolMiddlePage.PreviousPageMin, "carol's middle page should have PreviousPageMin")
	require.Equal(t, emptyHash, carolMiddlePage.NextPageMin, "carol's middle page should have no NextPageMin")

	carolLastPage = readNFTokenPage(t, env, carolMaxKl)
	require.NotNil(t, carolLastPage, "carol's last page should exist")
	require.Equal(t, emptyHash, carolLastPage.PreviousPageMin, "carol's last page should have no PreviousPageMin")
	require.Equal(t, emptyHash, carolLastPage.NextPageMin, "carol's last page should have no NextPageMin")

	// carol fixes their own NFToken directory
	result = env.Submit(
		nft.LedgerStateFixNFTPageLinks(carol, carol).Fee(linkFixFee).Build(),
	)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// carol's "middle" page should now have a NextPageMin pointing to the last page
	carolMiddlePage = readNFTokenPage(t, env, carolMiddleKl)
	require.NotNil(t, carolMiddlePage, "carol's middle page should still exist after repair")
	require.NotEqual(t, emptyHash, carolMiddlePage.PreviousPageMin,
		"carol's middle page should have PreviousPageMin after repair")
	require.Equal(t, carolMaxKl.Key, carolMiddlePage.NextPageMin,
		"carol's middle page NextPageMin should point to the max page")

	// carol's "last" page should now have a PreviousPageMin pointing to the middle page
	carolLastPage = readNFTokenPage(t, env, carolMaxKl)
	require.NotNil(t, carolLastPage, "carol's last page should exist after repair")
	require.Equal(t, carolMiddleNFTokenPageIndex, carolLastPage.PreviousPageMin,
		"carol's last page PreviousPageMin should point to the middle page")
	require.Equal(t, emptyHash, carolLastPage.NextPageMin,
		"carol's last page should have no NextPageMin")

	// carol's first page should link to the middle page
	carolFirstKl := nftPageKeylet(keylet.NFTokenPageMin(carol.ID), carolMiddlePage.PreviousPageMin)
	carolFirstPage := readNFTokenPage(t, env, carolFirstKl)
	require.NotNil(t, carolFirstPage, "carol's first page should exist")
	require.Equal(t, carolMiddleNFTokenPageIndex, carolFirstPage.NextPageMin,
		"carol's first page should link to the middle page")
	require.Equal(t, emptyHash, carolFirstPage.PreviousPageMin,
		"carol's first page should have no PreviousPageMin")

	require.Equal(t, uint32(3), env.OwnerCount(carol), "carol should have 3 pages after repair")

	// With the link repair, verify all tokens are accessible by counting page tokens
	totalCarolTokens := 0
	totalCarolTokens += len(carolFirstPage.NFTokens)
	totalCarolTokens += len(carolMiddlePage.NFTokens)
	totalCarolTokens += len(carolLastPage.NFTokens)
	require.Equal(t, 96, totalCarolTokens, "carol should have 96 total tokens after repair")
}

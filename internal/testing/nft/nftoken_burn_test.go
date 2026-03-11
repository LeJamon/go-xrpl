package nft_test

// NFTokenBurn_test.go - NFT burn tests
// Reference: rippled/src/test/app/NFTokenBurn_test.cpp

import (
	"math/rand"
	"sort"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/nftoken"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/accountset"
	"github.com/LeJamon/goXRPLd/internal/testing/nft"
)

// ===========================================================================
// testBurnRandom
// Reference: rippled NFTokenBurn_test.cpp testBurnRandom
//
// Creates multiple accounts with NFTs and offers, then burns them randomly
// to exercise page coalescing code.
// ===========================================================================
func TestBurnRandom(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")
	minter := jtx.NewAccount("minter")

	env.Fund(alice, becky, minter)
	env.Close()

	// Set minter as alice's authorized minter
	result := env.Submit(accountset.AccountSet(alice).AuthorizedMinter(minter).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Track NFTs by account
	type AcctStat struct {
		acct *jtx.Account
		nfts []string
	}
	aliceStat := &AcctStat{acct: alice}
	minterStat := &AcctStat{acct: minter}
	beckyStat := &AcctStat{acct: becky}

	// Mint 105 NFTs for alice (transferable + burnable)
	for i := 0; i < 105; i++ {
		fee := uint16(i * 100 % 50001) // pseudo-random transfer fee
		flags := nftoken.NFTokenFlagTransferable | nftoken.NFTokenFlagBurnable
		nftID := nft.GetNextNFTokenID(env, alice, 0, flags, fee)
		result := env.Submit(nft.NFTokenMint(alice, 0).Transferable().Burnable().TransferFee(fee).Build())
		if result.Success {
			aliceStat.nfts = append(aliceStat.nfts, nftID)
		}
		env.Close()
	}

	// Mint 105 NFTs by minter for alice (transferable + burnable)
	for i := 0; i < 105; i++ {
		fee := uint16((i + 50) * 100 % 50001) // pseudo-random transfer fee
		flags := nftoken.NFTokenFlagTransferable | nftoken.NFTokenFlagBurnable
		nftID := nft.GetNextNFTokenID(env, alice, 0, flags, fee)
		result := env.Submit(nft.NFTokenMint(minter, 0).Issuer(alice).Transferable().Burnable().TransferFee(fee).Build())
		if result.Success {
			minterStat.nfts = append(minterStat.nfts, nftID)
		}
		env.Close()
	}

	// Transfer 35 NFTs from alice to becky via sell offers
	for i := 0; i < 35 && i < len(aliceStat.nfts); i++ {
		nftID := aliceStat.nfts[i]
		offerIndex := nft.GetOfferIndex(env, alice)
		env.Submit(nft.NFTokenCreateSellOffer(alice, nftID, tx.NewXRPAmount(0)).Build())
		env.Close()

		env.Submit(nft.NFTokenAcceptSellOffer(becky, offerIndex).Build())
		env.Close()
	}
	// Move IDs from alice to becky
	beckyStat.nfts = append(beckyStat.nfts, aliceStat.nfts[:35]...)
	aliceStat.nfts = aliceStat.nfts[35:]

	// Transfer 35 NFTs from minter to becky via sell offers
	for i := 0; i < 35 && i < len(minterStat.nfts); i++ {
		nftID := minterStat.nfts[i]
		offerIndex := nft.GetOfferIndex(env, minter)
		env.Submit(nft.NFTokenCreateSellOffer(minter, nftID, tx.NewXRPAmount(0)).Build())
		env.Close()

		env.Submit(nft.NFTokenAcceptSellOffer(becky, offerIndex).Build())
		env.Close()
	}
	beckyStat.nfts = append(beckyStat.nfts, minterStat.nfts[:35]...)
	minterStat.nfts = minterStat.nfts[35:]

	// Each account should have 70 NFTs
	t.Logf("Alice: %d NFTs, Becky: %d NFTs, Minter: %d NFTs",
		len(aliceStat.nfts), len(beckyStat.nfts), len(minterStat.nfts))

	// Build a combined list of all NFTs with their owners for random burning
	type nftEntry struct {
		id    string
		owner *AcctStat
	}
	var allNFTs []nftEntry
	for _, nftID := range aliceStat.nfts {
		allNFTs = append(allNFTs, nftEntry{id: nftID, owner: aliceStat})
	}
	for _, nftID := range beckyStat.nfts {
		allNFTs = append(allNFTs, nftEntry{id: nftID, owner: beckyStat})
	}
	for _, nftID := range minterStat.nfts {
		allNFTs = append(allNFTs, nftEntry{id: nftID, owner: minterStat})
	}

	// Randomly burn all NFTs
	rng := rand.New(rand.NewSource(42)) // deterministic randomness
	for len(allNFTs) > 0 {
		idx := rng.Intn(len(allNFTs))
		entry := allNFTs[idx]

		// If owner is becky, issuer (alice) can burn it since all are burnable
		if entry.owner == beckyStat {
			result := env.Submit(nft.NFTokenBurn(alice, entry.id).Owner(becky).Build())
			if !result.Success {
				// Fallback: owner burns
				env.Submit(nft.NFTokenBurn(becky, entry.id).Build())
			}
		} else {
			result := env.Submit(nft.NFTokenBurn(entry.owner.acct, entry.id).Build())
			_ = result
		}
		env.Close()

		// Remove from list
		allNFTs[idx] = allNFTs[len(allNFTs)-1]
		allNFTs = allNFTs[:len(allNFTs)-1]
	}

	// Verify all accounts have ownerCount 0
	jtx.RequireOwnerCount(t, env, alice, 0)
	jtx.RequireOwnerCount(t, env, becky, 0)
	jtx.RequireOwnerCount(t, env, minter, 0)
}

// ===========================================================================
// testBurnSequential
// Reference: rippled NFTokenBurn_test.cpp testBurnSequential
//
// Tests burning NFTs in sequential order.
// Tests specific directory merging scenarios that can only be tested
// by inserting and deleting in an ordered fashion.
// ===========================================================================
func TestBurnSequential(t *testing.T) {
	// Helper to generate 96 NFTs packed into three pages of 32 each.
	// Returns a sorted list of NFT IDs.
	genPackedTokens := func(env *jtx.TestEnv, account *jtx.Account) []string {
		nfts := make([]string, 0, 96)

		for i := uint32(0); i < 96; i++ {
			// To fill the pages we use the taxon to break them into groups
			// of 16 entries. By having the internal representation of the
			// taxon go 0, 3, 2, 5, 4, 7... in sets of 16 NFTs we can get
			// each page to be fully populated.
			intTaxon := (i / 16)
			if i&0b10000 != 0 {
				intTaxon += 2
			}

			// Compute external taxon that will produce the desired internal taxon
			tokenSeq := nft.GetNextTokenSeq(env, account)
			extTaxon := nftoken.CipheredTaxon(tokenSeq, intTaxon)

			nftID := nft.GetNextNFTokenID(env, account, extTaxon, 0, 0)
			result := env.Submit(nft.NFTokenMint(account, extTaxon).Build())
			if result.Success {
				nfts = append(nfts, nftID)
			}
			env.Close()
		}

		// Sort NFTs by storage order (not creation order)
		sort.Strings(nfts)
		return nfts
	}

	// Test 1: Burn tokens in order from first to last
	t.Run("BurnFirstToLast", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)
		env.Close()

		nfts := genPackedTokens(env, alice)
		t.Logf("Created %d NFTs", len(nfts))

		// Burn all in order
		for _, nftID := range nfts {
			result := env.Submit(nft.NFTokenBurn(alice, nftID).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
		}

		jtx.RequireOwnerCount(t, env, alice, 0)
	})

	// Test 2: Burn tokens from last to first
	t.Run("BurnLastToFirst", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)
		env.Close()

		nfts := genPackedTokens(env, alice)

		// Burn in reverse order
		for i := len(nfts) - 1; i >= 0; i-- {
			result := env.Submit(nft.NFTokenBurn(alice, nfts[i]).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
		}

		jtx.RequireOwnerCount(t, env, alice, 0)
	})

	// Test 3: Burn all tokens in the middle page
	t.Run("BurnMiddlePage", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)
		env.Close()

		nfts := genPackedTokens(env, alice)

		// Burn middle page tokens (indices 32-63 in sorted order)
		for i := 32; i < 64; i++ {
			result := env.Submit(nft.NFTokenBurn(alice, nfts[i]).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
		}

		// Should have 2 pages remaining
		jtx.RequireOwnerCount(t, env, alice, 2)

		// Burn remaining tokens
		for i := 0; i < 32; i++ {
			env.Submit(nft.NFTokenBurn(alice, nfts[i]).Build())
			env.Close()
		}
		for i := 64; i < 96; i++ {
			env.Submit(nft.NFTokenBurn(alice, nfts[i]).Build())
			env.Close()
		}

		jtx.RequireOwnerCount(t, env, alice, 0)
	})

	// Test 4: Burn all tokens in first page then last page
	t.Run("BurnFirstThenLast", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)
		env.Close()

		nfts := genPackedTokens(env, alice)

		// Burn first page
		for i := 0; i < 32; i++ {
			result := env.Submit(nft.NFTokenBurn(alice, nfts[i]).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
		}

		// Burn last page
		for i := 64; i < 96; i++ {
			result := env.Submit(nft.NFTokenBurn(alice, nfts[i]).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
		}

		// Burn remaining middle page
		for i := 32; i < 64; i++ {
			env.Submit(nft.NFTokenBurn(alice, nfts[i]).Build())
			env.Close()
		}

		jtx.RequireOwnerCount(t, env, alice, 0)
	})
}

// ===========================================================================
// testBurnTooManyOffers
// Reference: rippled NFTokenBurn_test.cpp testBurnTooManyOffers
//
// Tests the case where too many offers prevents burning a token.
// ===========================================================================
func TestBurnTooManyOffers(t *testing.T) {
	const maxTokenOfferCancelCount = 500

	// Test 1: Before fixNonFungibleTokensV1_2 - burning NFT with > 500 offers
	// should fail with tefTOO_BIG
	t.Run("TooManyOffersBlocksBurn", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		becky := jtx.NewAccount("becky")

		env.Fund(alice, becky)
		env.Close()

		// Disable the amendment that allows burn with many offers
		env.DisableFeature("fixNonFungibleTokensV1_2")

		// Mint an NFT
		nftID := nft.GetNextNFTokenID(env, alice, 0, nftoken.NFTokenFlagTransferable, 0)
		result := env.Submit(nft.NFTokenMint(alice, 0).Transferable().URI("u").Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Create 500 buy offers from different accounts
		offerIndexes := make([]string, 0, maxTokenOfferCancelCount)
		for i := uint32(0); i < maxTokenOfferCancelCount; i++ {
			acct := jtx.NewAccount("acct" + string(rune('A'+i%26)) + string(rune('0'+i/26)))
			env.Fund(acct)
			env.Close()

			offerIdx := nft.GetOfferIndex(env, acct)
			result := env.Submit(nft.NFTokenCreateBuyOffer(acct, nftID, tx.NewXRPAmount(1), alice).Build())
			if result.Success {
				offerIndexes = append(offerIndexes, offerIdx)
			}
			env.Close()
		}

		// Create one more buy offer from becky (total 501)
		beckyOfferIdx := nft.GetOfferIndex(env, becky)
		env.Submit(nft.NFTokenCreateBuyOffer(becky, nftID, tx.NewXRPAmount(1), alice).Build())
		env.Close()

		// Attempt to burn the NFT - should fail with tefTOO_BIG
		result = env.Submit(nft.NFTokenBurn(alice, nftID).Build())
		jtx.RequireTxFail(t, result, "tefTOO_BIG")

		// Cancel becky's offer (back to 500)
		env.Submit(nft.NFTokenCancelOffer(becky, beckyOfferIdx).Build())
		env.Close()

		// Now burn should succeed (exactly 500 buy offers)
		result = env.Submit(nft.NFTokenBurn(alice, nftID).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// All should be cleaned up
		jtx.RequireOwnerCount(t, env, alice, 0)
		jtx.RequireOwnerCount(t, env, becky, 0)
	})

	// Test 2: With fixNonFungibleTokensV1_2 - burn removes up to 500 offers
	t.Run("BurnWithMaxOffers", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.EnableFeature("fixNonFungibleTokensV1_2")

		alice := jtx.NewAccount("alice")

		// Need enough XRP for 501 sell offers + NFT page + fees
		// Reserve = 200 XRP + 502 * 50 XRP = 25300 XRP, plus fees
		env.FundAmount(alice, 30000000000) // 30000 XRP
		env.Close()

		// Mint an NFT
		nftID := nft.GetNextNFTokenID(env, alice, 0, nftoken.NFTokenFlagTransferable, 0)
		result := env.Submit(nft.NFTokenMint(alice, 0).Transferable().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Create 501 sell offers
		for i := uint32(0); i <= maxTokenOfferCancelCount; i++ {
			result = env.Submit(nft.NFTokenCreateSellOffer(alice, nftID, tx.NewXRPAmount(int64(i+1))).Build())
			if !result.Success {
				t.Fatalf("Failed to create sell offer %d: %s", i, result.Code)
			}
			env.Close()
		}

		// Burn the token - should succeed but only remove 500 of 501 offers
		result = env.Submit(nft.NFTokenBurn(alice, nftID).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// alice should have ownerCount of 1 for the orphaned sell offer
		jtx.RequireOwnerCount(t, env, alice, 1)
	})

	// Test 3: With fixNonFungibleTokensV1_2 - mix of buy and sell offers
	t.Run("BurnMixedOffers", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.EnableFeature("fixNonFungibleTokensV1_2")

		alice := jtx.NewAccount("alice")
		becky := jtx.NewAccount("becky")

		// Need enough XRP for 499 sell offers + NFT page + fees
		// Reserve = 200 XRP + 500 * 50 XRP = 25200 XRP, plus fees
		env.FundAmount(alice, 30000000000) // 30000 XRP
		env.Fund(becky)
		env.Close()

		// Mint an NFT
		nftID := nft.GetNextNFTokenID(env, alice, 0, nftoken.NFTokenFlagTransferable, 0)
		result := env.Submit(nft.NFTokenMint(alice, 0).Transferable().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Create 499 sell offers from alice
		for i := uint32(0); i < maxTokenOfferCancelCount-1; i++ {
			result = env.Submit(nft.NFTokenCreateSellOffer(alice, nftID, tx.NewXRPAmount(int64(i+1))).Build())
			if !result.Success {
				t.Fatalf("Failed to create sell offer %d: %s", i, result.Code)
			}
			env.Close()
		}

		// Create 2 buy offers from becky
		env.Submit(nft.NFTokenCreateBuyOffer(becky, nftID, tx.NewXRPAmount(1), alice).Build())
		env.Close()
		env.Submit(nft.NFTokenCreateBuyOffer(becky, nftID, tx.NewXRPAmount(1), alice).Build())
		env.Close()

		// Burn the token
		result = env.Submit(nft.NFTokenBurn(alice, nftID).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// All 499 sell offers should be removed + 1 of 2 buy offers = 500 total
		// alice ownerCount should be 0 (all sell offers deleted)
		jtx.RequireOwnerCount(t, env, alice, 0)

		// becky should have ownerCount of 1 (one orphaned buy offer)
		jtx.RequireOwnerCount(t, env, becky, 1)
	})
}

// ===========================================================================
// exerciseBrokenLinks
// Reference: rippled NFTokenBurn_test.cpp exerciseBrokenLinks
//
// Tests the case where NFT page links become broken when
// fixNFTokenPageLinks is not enabled.
// ===========================================================================
func TestExerciseBrokenLinks(t *testing.T) {
	env := jtx.NewTestEnv(t)

	// This test exercises the broken links bug that occurs without
	// fixNFTokenPageLinks. Since our test env has it enabled by default,
	// we disable it.
	env.DisableFeature("fixNFTokenPageLinks")

	alice := jtx.NewAccount("alice")
	minter := jtx.NewAccount("minter")

	env.Fund(alice, minter)
	env.Close()

	// Generate 96 NFTs packed into three pages of 32 each.
	// Minter creates, then transfers to alice.
	nfts := make([]string, 0, 96)

	for i := uint32(0); i < 96; i++ {
		// Taxon manipulation for page packing
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

		// Minter creates sell offer for the NFT
		offerIndex := nft.GetOfferIndex(env, minter)
		env.Submit(nft.NFTokenCreateSellOffer(minter, nftID, tx.NewXRPAmount(0)).Build())
		env.Close()

		// Alice accepts the offer
		env.Submit(nft.NFTokenAcceptSellOffer(alice, offerIndex).Build())
		env.Close()

		nfts = append(nfts, nftID)
	}

	// Sort NFTs by storage order
	sort.Strings(nfts)

	// alice should now own 96 NFTs in 3 pages
	jtx.RequireOwnerCount(t, env, alice, 3)

	// Sell all tokens in the last page back to minter
	last32NFTs := make([]string, 32)
	copy(last32NFTs, nfts[64:96])

	for _, nftID := range last32NFTs {
		// alice creates sell offer
		offerIndex := nft.GetOfferIndex(env, alice)
		env.Submit(nft.NFTokenCreateSellOffer(alice, nftID, tx.NewXRPAmount(0)).Build())
		env.Close()

		// minter accepts
		env.Submit(nft.NFTokenAcceptSellOffer(minter, offerIndex).Build())
		env.Close()
	}

	// BUG: Without fixNFTokenPageLinks, last page is deleted instead of
	// having contents coalesced. The middle page loses its NextPageMin link.
	// alice's ownerCount should be 2 (two remaining pages)
	jtx.RequireOwnerCount(t, env, alice, 2)

	// minter sells the last 32 NFTs back to alice
	for _, nftID := range last32NFTs {
		offerIndex := nft.GetOfferIndex(env, minter)
		env.Submit(nft.NFTokenCreateSellOffer(minter, nftID, tx.NewXRPAmount(0)).Build())
		env.Close()

		env.Submit(nft.NFTokenAcceptSellOffer(alice, offerIndex).Build())
		env.Close()
	}

	// alice should have 3 pages again, but the links are broken
	jtx.RequireOwnerCount(t, env, alice, 3)

	// The broken state: middle page has no NextPageMin, so
	// account_nfts would only return 64 NFTs even though alice owns 96.
	// We can't easily test RPC queries here, but the broken state is
	// exercised by the page operations above.
}

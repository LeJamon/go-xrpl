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

	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/nftoken"
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

		tokenSeq := env.MintedCount(minter)
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
	// In rippled, preclaim checks Owner account exists → tecOBJECT_NOT_FOUND.
	// The Go LedgerStateFix does not implement Appliable (no ApplyContext param),
	// so the engine cannot perform preclaim checks on the Owner field.
	t.Run("tecOBJECT_NOT_FOUND when Owner does not exist", func(t *testing.T) {
		t.Skip("Go LedgerStateFix does not implement preclaim Owner check (no Appliable interface)")
	})
}

// ===========================================================================
// testTokenPageLinkErrors
// Reference: rippled FixNFTokenPageLinks_test.cpp testTokenPageLinkErrors
//
// Tests error cases where there is nothing to fix in the owner's NFT pages.
// ===========================================================================
func TestTokenPageLinkErrors(t *testing.T) {
	// These tests require the LedgerStateFix Apply() to actually inspect
	// NFToken pages and return tecFAILED_PROCESSING when nothing is broken.
	// The current Go Apply() is a stub that returns tesSUCCESS unconditionally.
	t.Skip("Requires LedgerStateFix Apply() to inspect NFToken pages (currently a stub)")
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
	// This test requires the full LedgerStateFix Apply() implementation
	// which inspects and repairs NFToken page links. The current Go
	// implementation is a stub that returns tesSUCCESS without modifying state.
	t.Skip("Requires full LedgerStateFix Apply() implementation (currently a stub)")
}

package nft_test

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
	"github.com/LeJamon/go-xrpl/keylet"
)

// TestNFTokenAcceptOffer_ExpiredOfferDeletion exercises the fixCleanup3_1_3
// behaviour: accepting an expired NFTokenOffer still fails with tecEXPIRED, but
// with the amendment enabled the expired offer is deleted from the ledger
// (DeletedNode in metadata, owner count decremented) instead of persisting.
// Reference: rippled NFTokenAcceptOffer.cpp (PR #5707) + NFToken_test.cpp.
func TestNFTokenAcceptOffer_ExpiredOfferDeletion(t *testing.T) {
	t.Run("amendment enabled deletes offer", func(t *testing.T) {
		testExpiredOfferAcceptance(t, true)
	})
	t.Run("amendment disabled keeps offer", func(t *testing.T) {
		testExpiredOfferAcceptance(t, false)
	})
}

func testExpiredOfferAcceptance(t *testing.T, amendmentEnabled bool) {
	env := jtx.NewTestEnv(t)
	if !amendmentEnabled {
		env.DisableFeature("fixCleanup3_1_3")
	}
	env.Close()

	alice := jtx.NewAccount("alice")
	buyer := jtx.NewAccount("buyer")
	env.Fund(alice)
	env.Fund(buyer)
	env.Close()

	// alice mints a transferable NFT.
	nftID := nft.GetNextNFTokenID(env, alice, 0, nftoken.NFTokenFlagTransferable, 0)
	jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(alice, 0).Transferable().Build()))
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 1) // NFT page

	// alice creates a sell offer that expires shortly in the future.
	offerSeq := env.Seq(alice)
	offerKL := keylet.NFTokenOffer(alice.ID, offerSeq)
	jtx.RequireTxSuccess(t, env.Submit(
		nft.NFTokenCreateSellOffer(alice, nftID, tx.NewXRPAmount(0)).
			Expiration(env.NowRipple()+100).
			Build()))
	env.Close()
	jtx.RequireOwnerCount(t, env, alice, 2) // NFT page + offer
	if !env.LedgerEntryExists(offerKL) {
		t.Fatal("expected sell offer to exist on ledger after creation")
	}

	// Advance the clock well past the offer's expiration.
	env.AdvanceTime(300 * time.Second)
	env.Close()

	// buyer accepts the (now expired) sell offer: always tecEXPIRED.
	result := env.Submit(nft.NFTokenAcceptSellOffer(buyer, hex.EncodeToString(offerKL.Key[:])).Build())
	jtx.RequireTxClaimed(t, result, jtx.TecEXPIRED)
	env.Close()

	if amendmentEnabled {
		// The expired offer was deleted: owner count drops and the offer is gone.
		jtx.RequireOwnerCount(t, env, alice, 1)
		if env.LedgerEntryExists(offerKL) {
			t.Fatal("expected expired sell offer to be deleted from ledger")
		}
		requireDeletedNFTokenOffer(t, result, offerKL)
	} else {
		// Pre-amendment: the expired offer persists on the ledger.
		jtx.RequireOwnerCount(t, env, alice, 2)
		if !env.LedgerEntryExists(offerKL) {
			t.Fatal("expected expired sell offer to remain on ledger pre-amendment")
		}
	}
}

// requireDeletedNFTokenOffer asserts the transaction metadata records the given
// NFTokenOffer as a DeletedNode.
func requireDeletedNFTokenOffer(t *testing.T, result jtx.TxResult, offerKL keylet.Keylet) {
	t.Helper()
	if result.Metadata == nil {
		t.Fatal("expected transaction metadata, got nil")
	}
	want := hex.EncodeToString(offerKL.Key[:])
	for _, n := range result.Metadata.AffectedNodes {
		if n.NodeType == "DeletedNode" && n.LedgerEntryType == "NFTokenOffer" &&
			strings.EqualFold(n.LedgerIndex, want) {
			return
		}
	}
	t.Fatalf("expected a DeletedNode for NFTokenOffer %s in metadata, got %+v", want, result.Metadata.AffectedNodes)
}

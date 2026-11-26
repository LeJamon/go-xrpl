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

// TestNFTokenAcceptOffer_ExpiredOfferRetryPass pins where tecEXPIRED comes
// from on a retry pass. Without fixCleanup3_1_3 it is a preclaim result, so the
// transaction is deferred (not applied). With the amendment it comes from
// doApply, which commits tecEXPIRED and deletes the offer even when retrying.
func TestNFTokenAcceptOffer_ExpiredOfferRetryPass(t *testing.T) {
	for _, amendmentEnabled := range []bool{true, false} {
		name := "amendment enabled applies"
		if !amendmentEnabled {
			name = "amendment disabled defers"
		}
		t.Run(name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			if !amendmentEnabled {
				env.DisableFeature("fixCleanup3_1_3")
			}
			alice := jtx.NewAccount("alice")
			buyer := jtx.NewAccount("buyer")
			env.Fund(alice, buyer)
			env.Close()

			nftID := nft.GetNextNFTokenID(env, alice, 0, nftoken.NFTokenFlagTransferable, 0)
			jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(alice, 0).Transferable().Build()))
			offerKL := keylet.NFTokenOffer(alice.ID, env.Seq(alice))
			jtx.RequireTxSuccess(t, env.Submit(
				nft.NFTokenCreateSellOffer(alice, nftID, tx.NewXRPAmount(0)).
					Expiration(env.NowRipple()+100).
					Build()))
			env.Close()
			env.AdvanceTime(300 * time.Second)
			env.Close()

			buyerSeq := env.Seq(buyer)
			result := env.SubmitWithFlags(
				nft.NFTokenAcceptSellOffer(buyer, hex.EncodeToString(offerKL.Key[:])).Build(),
				tx.TapRETRY)
			if result.Code != jtx.TecEXPIRED {
				t.Fatalf("result = %s, want tecEXPIRED", result.Code)
			}
			if result.Applied != amendmentEnabled {
				t.Fatalf("applied = %v, want %v", result.Applied, amendmentEnabled)
			}
			wantSeq := buyerSeq
			if amendmentEnabled {
				wantSeq++
			}
			if got := env.Seq(buyer); got != wantSeq {
				t.Fatalf("buyer sequence = %d, want %d", got, wantSeq)
			}
			if exists := env.LedgerEntryExists(offerKL); exists == amendmentEnabled {
				t.Fatalf("offer exists = %v, want %v", exists, !amendmentEnabled)
			}
		})
	}
}

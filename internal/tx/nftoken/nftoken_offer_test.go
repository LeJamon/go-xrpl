package nftoken

import (
	"bytes"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestOfferDirectoryResults(t *testing.T) {
	ctx := &tx.ApplyContext{}
	for _, tc := range []struct {
		err  error
		want ter.Result
	}{
		{state.ErrDirFull, ter.TecDIR_FULL},
		{state.ErrMalformedDirectory, ter.TefEXCEPTION},
		{errors.New("storage error"), ter.TefINTERNAL},
	} {
		if got := offerDirInsertError(ctx, "insert offer", tc.err); got != tc.want {
			t.Errorf("%v: got %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestTokenOfferReserveOwnerDelta(t *testing.T) {
	config := tx.EngineConfig{
		ReserveBase:      100,
		ReserveIncrement: 10,
		Rules:            amendment.EmptyRules(),
	}

	legacy := &tx.ApplyContext{
		Account: &state.AccountRoot{OwnerCount: ^uint32(0)},
		Config:  config,
	}
	reserve, ok := tokenOfferReserve(legacy)
	if !ok || reserve != config.AccountReserve(0) {
		t.Fatalf("legacy reserve = (%d, %v), want (%d, true)", reserve, ok, config.AccountReserve(0))
	}

	config.Rules = amendment.NewRulesBuilder().Enable(amendment.FeatureSponsor).Build()
	sponsored := &state.AccountRoot{
		OwnerCount:          ^uint32(0),
		SponsoredOwnerCount: 1,
	}
	sponsoredCtx := &tx.ApplyContext{Account: sponsored, Config: config}
	reserve, ok = tokenOfferReserve(sponsoredCtx)
	want := config.AccountReserveWithCounts(^uint32(0), 1)
	if !ok || reserve != want {
		t.Fatalf("sponsored reserve = (%d, %v), want (%d, true)", reserve, ok, want)
	}
}

// insertSellOffer creates a sell-offer SLE for tokenID owned by owner and
// links it into the owner directory and the NFTSells directory, mirroring the
// state produced by NFTokenCreateOffer.
func insertSellOffer(t *testing.T, view *mockView, owner [20]byte, tokenID [32]byte, seq uint32) keylet.Keylet {
	t.Helper()

	offerKL := keylet.NFTokenOffer(owner, seq)

	ownerRes, err := state.DirInsert(view, keylet.OwnerDir(owner), offerKL.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = owner
	})
	if err != nil {
		t.Fatalf("DirInsert owner dir: %v", err)
	}

	sellRes, err := state.DirInsert(view, keylet.NFTSells(tokenID), offerKL.Key, false, func(dir *state.DirectoryNode) {
		dir.Flags = lsfNFTokenSellOffers
		dir.NFTokenID = tokenID
	})
	if err != nil {
		t.Fatalf("DirInsert sell dir: %v", err)
	}

	data, err := serializeNFTokenOfferRaw(
		owner, tokenID,
		tx.NewXRPAmount(0).LedgerValue(), NFTokenCreateOfferFlagSellNFToken,
		ownerRes.Page, sellRes.Page,
		"", nil,
	)
	if err != nil {
		t.Fatalf("serializeNFTokenOfferRaw: %v", err)
	}
	if err := view.Insert(offerKL, data); err != nil {
		t.Fatalf("Insert offer: %v", err)
	}
	return offerKL
}

// TestDeleteNFTokenOffers_ReverseOrderWithinPage verifies that when the
// deletion limit truncates the offer set, entries are removed from the back
// of each directory page first, matching rippled's removeTokenOffersWithLimit
// reverse iteration.
func TestDeleteNFTokenOffers_ReverseOrderWithinPage(t *testing.T) {
	view := newMockView()
	owner := mustHexIssuer(t, "0102030405060708090A0B0C0D0E0F1011121314")
	tokenID := [32]byte{0xAA}

	offer1 := insertSellOffer(t, view, owner, tokenID, 1)
	offer2 := insertSellOffer(t, view, owner, tokenID, 2)

	// Directory pages keep Indexes sorted ascending; reverse iteration must
	// delete the byte-wise larger key first.
	first, last := offer1, offer2
	if bytes.Compare(first.Key[:], last.Key[:]) > 0 {
		first, last = last, first
	}

	result, res := deleteNFTokenOffers(tokenID, true, 1, view, owner)
	if res != ter.TesSUCCESS {
		t.Fatalf("deleteNFTokenOffers result = %v, want tesSUCCESS", res)
	}
	if result.TotalDeleted != 1 || result.SelfDeleted != 1 {
		t.Fatalf("deleted = %+v, want TotalDeleted=1 SelfDeleted=1", result)
	}

	if data, _ := view.Read(last); data != nil {
		t.Errorf("last page entry %X still exists; reverse iteration should delete it first", last.Key)
	}
	if data, _ := view.Read(first); data == nil {
		t.Errorf("first page entry %X was deleted; it should survive the limit", first.Key)
	}
}

// TestDeleteNFTokenOffers_CorruptOffer verifies that a directory entry whose
// SLE cannot be parsed aborts the cleanup with tefEXCEPTION, matching
// rippled's throw from removeTokenOffersWithLimit (converted to tefEXCEPTION
// by doApply).
func TestDeleteNFTokenOffers_CorruptOffer(t *testing.T) {
	view := newMockView()
	owner := mustHexIssuer(t, "0102030405060708090A0B0C0D0E0F1011121314")
	tokenID := [32]byte{0xAA}

	offerKL := insertSellOffer(t, view, owner, tokenID, 1)
	if err := view.Update(offerKL, []byte{0xDE, 0xAD}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	_, res := deleteNFTokenOffers(tokenID, true, maxDeletableTokenOfferEntries, view, owner)
	if res != ter.TefEXCEPTION {
		t.Fatalf("deleteNFTokenOffers result = %v, want tefEXCEPTION", res)
	}
}

// TestDeleteNFTokenOffers_MissingOfferSkipped verifies that a dangling
// directory entry (offer SLE already gone) is skipped without failing,
// matching rippled's null view.peek.
func TestDeleteNFTokenOffers_MissingOfferSkipped(t *testing.T) {
	view := newMockView()
	owner := mustHexIssuer(t, "0102030405060708090A0B0C0D0E0F1011121314")
	tokenID := [32]byte{0xAA}

	kept := insertSellOffer(t, view, owner, tokenID, 1)
	dangling := insertSellOffer(t, view, owner, tokenID, 2)
	if err := view.Erase(dangling); err != nil {
		t.Fatalf("Erase: %v", err)
	}

	result, res := deleteNFTokenOffers(tokenID, true, maxDeletableTokenOfferEntries, view, owner)
	if res != ter.TesSUCCESS {
		t.Fatalf("deleteNFTokenOffers result = %v, want tesSUCCESS", res)
	}
	if result.TotalDeleted != 1 {
		t.Fatalf("TotalDeleted = %d, want 1", result.TotalDeleted)
	}
	if data, _ := view.Read(kept); data != nil {
		t.Errorf("offer %X should have been deleted", kept.Key)
	}
}

package applystate

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// TestBuildDeletedNode_OfferPrevTxnFromOriginal reproduces the divergence at
// mainnet ledger 99226383: a resting offer fully consumed during crossing is
// threaded (PreviousTxnID set to the current tx) before being erased in the
// same transaction. rippled never threads an erased node, so its DeletedNode
// FinalFields carry the offer's PRIOR PreviousTxnID/PreviousTxnLgrSeq, not the
// crossing tx. buildDeletedNode must source those fields from the pre-tx
// Original rather than the threaded Current.
func TestBuildDeletedNode_OfferPrevTxnFromOriginal(t *testing.T) {
	var priorTxn, currentTxn [32]byte
	for i := range priorTxn {
		priorTxn[i] = 0xAB   // the offer's real last modification (ledger 99226378)
		currentTxn[i] = 0xCD // the crossing tx that deletes it (ledger 99226383)
	}
	const priorSeq = uint32(99226378)
	const currentSeq = uint32(99226383)

	orig := &state.LedgerOffer{
		Account:           "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		Sequence:          96795764,
		TakerPays:         state.NewXRPAmountFromInt(1_000_000_000),
		TakerGets:         state.NewXRPAmountFromInt(2_000_000_000),
		PreviousTxnID:     priorTxn,
		PreviousTxnLgrSeq: priorSeq,
	}
	for i := range orig.BookDirectory {
		orig.BookDirectory[i] = byte(i + 1)
	}
	origBytes, err := state.SerializeLedgerOffer(orig)
	if err != nil {
		t.Fatalf("SerializeLedgerOffer original: %v", err)
	}

	// Current is the state just before deletion: fully consumed (zeroed amounts)
	// AND threaded to the crossing tx, exactly as it lands in the apply state
	// table when an offer is partially then fully consumed across flow steps.
	cur := *orig
	cur.TakerPays = state.NewXRPAmountFromInt(0)
	cur.TakerGets = state.NewXRPAmountFromInt(0)
	cur.PreviousTxnID = currentTxn
	cur.PreviousTxnLgrSeq = currentSeq
	curBytes, err := state.SerializeLedgerOffer(&cur)
	if err != nil {
		t.Fatalf("SerializeLedgerOffer current: %v", err)
	}

	var key [32]byte
	for i := range key {
		key[i] = 0xBB
	}

	tbl := &ApplyStateTable{}
	node, err := tbl.buildDeletedNode(key, origBytes, curBytes)
	if err != nil {
		t.Fatalf("buildDeletedNode: %v", err)
	}
	if node.FinalFields == nil {
		t.Fatal("DeletedNode FinalFields is nil")
	}

	wantID := strings.ToUpper(hex.EncodeToString(priorTxn[:]))
	threadedID := strings.ToUpper(hex.EncodeToString(currentTxn[:]))

	gotID, _ := node.FinalFields["PreviousTxnID"].(string)
	if gotID == threadedID {
		t.Fatalf("DeletedNode FinalFields.PreviousTxnID reports the crossing tx (threaded Current); want the prior pointer %s", wantID)
	}
	if gotID != wantID {
		t.Fatalf("DeletedNode FinalFields.PreviousTxnID = %q, want %q (pre-tx Original)", gotID, wantID)
	}

	gotSeq, _ := node.FinalFields["PreviousTxnLgrSeq"].(uint32)
	if gotSeq != priorSeq {
		t.Fatalf("DeletedNode FinalFields.PreviousTxnLgrSeq = %d, want %d (pre-tx Original)", gotSeq, priorSeq)
	}
}

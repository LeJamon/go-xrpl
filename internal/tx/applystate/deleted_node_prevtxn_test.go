package applystate

import (
	"encoding/hex"
	"strings"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
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

// nftokenPageBytes encodes an NFTokenPage blob. withPrevTxn controls whether
// the stored threading pointer is present: the on-ledger page carries it, but
// serializeNFTokenPage rebuilds a page WITHOUT it (the page is re-serialized
// during a merge before being erased), so the deleted entry's Current is
// threadless.
func nftokenPageBytes(t *testing.T, tokens []string, prevTxn [32]byte, prevSeq uint32, withPrevTxn bool) []byte {
	t.Helper()
	nfTokens := make([]map[string]any, len(tokens))
	for i, id := range tokens {
		nfTokens[i] = map[string]any{
			"NFToken": map[string]any{"NFTokenID": id},
		}
	}
	obj := map[string]any{
		"LedgerEntryType": "NFTokenPage",
		"Flags":           uint32(0),
		"NFTokens":        nfTokens,
	}
	if withPrevTxn {
		obj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(prevTxn[:]))
		obj["PreviousTxnLgrSeq"] = prevSeq
	}
	hexStr, err := binarycodec.Encode(obj)
	if err != nil {
		t.Fatalf("encode NFTokenPage: %v", err)
	}
	b, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("decode NFTokenPage hex: %v", err)
	}
	return b
}

// TestBuildDeletedNode_NFTokenPagePrevTxnFromOriginal reproduces the divergence
// at mainnet ledger 99226885 tx 6 (issue #1047): an NFToken leaves a page, the
// page is re-serialized (serializeNFTokenPage drops PreviousTxnID) and then
// merged into a sibling and erased within the same tx. The erased entry's
// Current is therefore threadless — it carries no PreviousTxnID at all — while
// its on-ledger Original still holds the page's last-modified pointer.
//
// rippled builds the DeletedNode's FinalFields from its in-memory SLE, which
// retains sfPreviousTxnID (sMD_DeleteFinal), so mainnet reports the stored
// pointer. buildDeletedNode must insert that pointer (from the pre-tx Original)
// even though Current omits it.
func TestBuildDeletedNode_NFTokenPagePrevTxnFromOriginal(t *testing.T) {
	var priorTxn [32]byte
	for i := range priorTxn {
		priorTxn[i] = 0xC9
	}
	const priorSeq = uint32(99226875)

	tokens := []string{
		"001A2710E7EFE991D9F52CA949A75D4896F5B94D20E6472DAD8707510449F9C4",
		"001A2710E7EFE991D9F52CA949A75D4896F5B94D20E6472DAD8707510449FAAA",
	}

	// Original: the live page, with its stored threading pointer.
	origBytes := nftokenPageBytes(t, tokens, priorTxn, priorSeq, true)
	// Current: the rebuilt, threadless page (serializeNFTokenPage output).
	curBytes := nftokenPageBytes(t, tokens, priorTxn, priorSeq, false)

	var key [32]byte
	for i := range key {
		key[i] = 0xF6
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
	gotID, ok := node.FinalFields["PreviousTxnID"].(string)
	if !ok {
		t.Fatalf("DeletedNode FinalFields.PreviousTxnID missing; want %s (pre-tx Original)", wantID)
	}
	if gotID != wantID {
		t.Fatalf("DeletedNode FinalFields.PreviousTxnID = %q, want %q (pre-tx Original)", gotID, wantID)
	}

	gotSeq, ok := node.FinalFields["PreviousTxnLgrSeq"].(uint32)
	if !ok {
		t.Fatalf("DeletedNode FinalFields.PreviousTxnLgrSeq missing; want %d (pre-tx Original)", priorSeq)
	}
	if gotSeq != priorSeq {
		t.Fatalf("DeletedNode FinalFields.PreviousTxnLgrSeq = %d, want %d (pre-tx Original)", gotSeq, priorSeq)
	}
}

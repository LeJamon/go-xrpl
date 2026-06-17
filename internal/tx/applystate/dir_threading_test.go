package applystate

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

type affectedView struct {
	idx       string
	nodeType  string
	prevTxnID string
}

// findAffected returns the AffectedNode for the given ledger index (uppercase
// hex) and node type, or fails the test.
func findAffected(t *testing.T, nodes []affectedView, idx, nodeType string) affectedView {
	t.Helper()
	for _, n := range nodes {
		if n.idx == idx && n.nodeType == nodeType {
			return n
		}
	}
	t.Fatalf("no %s for ledger index %s", nodeType, idx)
	return affectedView{}
}

// hexUpper renders a 32-byte key as the uppercase hex the metadata uses.
func hexUpper(k [32]byte) string { return strings.ToUpper(hex.EncodeToString(k[:])) }

// bookDirBytes serializes an order-book directory page. priorTxn != zero sets
// the page's stored PreviousTxnID (as a page carries once it has been threaded).
func bookDirBytes(t *testing.T, root [32]byte, members [][32]byte, priorTxn [32]byte, priorSeq uint32) []byte {
	t.Helper()
	dir := &state.DirectoryNode{
		RootIndex:         root,
		Indexes:           members,
		ExchangeRate:      0x5000000000000000,
		TakerPaysCurrency: [20]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 'C', 'N', 'Y', 0, 0, 0, 0, 0},
		PreviousTxnID:     priorTxn,
		PreviousTxnLgrSeq: priorSeq,
	}
	b, err := state.SerializeDirectoryNode(dir, true)
	if err != nil {
		t.Fatalf("SerializeDirectoryNode: %v", err)
	}
	return b
}

// applyModify runs a single in-place modify of bookKey from orig to cur bytes
// through the full Apply() path (threading + metadata) and returns the
// AffectedNodes flattened to {idx,type,prevTxnID}.
func applyModify(t *testing.T, bookKey [32]byte, orig, cur []byte, txHash [32]byte, txSeq uint32) []affectedView {
	t.Helper()
	base := newMockBaseView()
	base.data[bookKey] = orig

	table := NewApplyStateTable(base, txHash, txSeq, amendment.AllSupportedRules())
	if _, err := table.Read(keylet.Keylet{Key: bookKey}); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if err := table.Update(keylet.Keylet{Key: bookKey}, cur); err != nil {
		t.Fatalf("Update: %v", err)
	}
	meta, err := table.Apply()
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var out []affectedView
	for _, an := range meta.AffectedNodes {
		out = append(out, affectedView{
			idx:       an.LedgerIndex,
			nodeType:  an.NodeType,
			prevTxnID: an.PreviousTxnID,
		})
	}
	return out
}

// TestBookDirRebuild_NoNodeLevelPreviousTxnID reproduces issue #1006: an account
// replacing the only offer at a quality level empties the order-book directory
// page (page erased) and re-adds the new offer (page recreated from scratch by
// state.DirInsert, carrying no PreviousTxnID). The flattened net effect reaching
// the apply table is a plain modify of the page from its prior bytes (which
// carried a pointer) to the rebuilt field-less bytes. rippled threads the
// rebuilt page to self with a zero prior pointer and emits NO node-level
// PreviousTxnID; goXRPL must match (it previously echoed the stale pointer,
// +38 bytes, forking transaction_hash).
func TestBookDirRebuild_NoNodeLevelPreviousTxnID(t *testing.T) {
	var bookKey [32]byte
	for i := range bookKey {
		bookKey[i] = byte(0x10 + i)
	}
	var oldOffer, newOffer, priorTxn, thisTxn [32]byte
	for i := range oldOffer {
		oldOffer[i] = byte(0x20 + i)
		newOffer[i] = byte(0x30 + i)
		priorTxn[i] = byte(0x40 + i)
		thisTxn[i] = byte(0x50 + i)
	}

	orig := bookDirBytes(t, bookKey, [][32]byte{oldOffer}, priorTxn, 99226370)
	// Rebuilt page: fresh, no PreviousTxnID, holds the replacement offer.
	rebuilt := bookDirBytes(t, bookKey, [][32]byte{newOffer}, [32]byte{}, 0)

	meta := applyModify(t, bookKey, orig, rebuilt, thisTxn, 99226371)
	node := findAffected(t, meta, hexUpper(bookKey), "ModifiedNode")

	if node.prevTxnID != "" {
		t.Fatalf("rebuilt book directory must not emit a node-level PreviousTxnID, got %q", node.prevTxnID)
	}
}

// TestDirInPlaceModify_KeepsPreviousTxnID guards the converse: a directory page
// modified in place (e.g. an owner directory gaining/losing an entry) keeps its
// PreviousTxnID across the parse→serialize round-trip, so the node IS threaded
// and the prior pointer IS emitted — exactly as rippled does for an in-place
// peek+update.
func TestDirInPlaceModify_KeepsPreviousTxnID(t *testing.T) {
	var bookKey [32]byte
	for i := range bookKey {
		bookKey[i] = byte(0x11 + i)
	}
	var offerA, offerB, priorTxn, thisTxn [32]byte
	for i := range offerA {
		offerA[i] = byte(0x21 + i)
		offerB[i] = byte(0x31 + i)
		priorTxn[i] = byte(0x41 + i)
		thisTxn[i] = byte(0x51 + i)
	}

	orig := bookDirBytes(t, bookKey, [][32]byte{offerA}, priorTxn, 99226370)
	// In-place modify: an entry is added; the page keeps its stored pointer.
	cur := bookDirBytes(t, bookKey, [][32]byte{offerA, offerB}, priorTxn, 99226370)

	meta := applyModify(t, bookKey, orig, cur, thisTxn, 99226371)
	node := findAffected(t, meta, hexUpper(bookKey), "ModifiedNode")

	want := hexUpper(priorTxn)
	if node.prevTxnID != want {
		t.Fatalf("in-place directory modify must emit the prior PreviousTxnID %s, got %q", want, node.prevTxnID)
	}
}

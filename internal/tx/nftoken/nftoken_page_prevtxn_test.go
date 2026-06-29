package nftoken

import (
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// PR #1131 regression guard: serializeNFTokenPage must round-trip
// PreviousTxnID/PreviousTxnLgrSeq so a no-op NFTokenModify re-serializes
// byte-identically and the apply layer prunes it (ApplyStateTable.cpp:156-157).

func nftThreadTestTxnID() [32]byte {
	var id [32]byte
	for i := range id {
		id[i] = byte(i + 1)
	}
	return id
}

func TestNFTokenPage_ThreadingPointersRoundTrip(t *testing.T) {
	id := nftThreadTestTxnID()
	const seq = uint32(555)

	page := &state.NFTokenPageData{
		NFTokens:          []state.NFTokenData{{NFTokenID: [32]byte{0xAA}, URI: "DEAD"}},
		PreviousTxnID:     id,
		PreviousTxnLgrSeq: seq,
	}
	data, err := serializeNFTokenPage(page)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}

	fields, err := binarycodec.DecodeBytes(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := fields["PreviousTxnID"]; !ok {
		t.Error("PreviousTxnID must be present in the serialized page")
	}
	if _, ok := fields["PreviousTxnLgrSeq"]; !ok {
		t.Error("PreviousTxnLgrSeq must be present in the serialized page")
	}

	parsed, err := state.ParseNFTokenPage(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.PreviousTxnID != id {
		t.Errorf("PreviousTxnID = %x, want %x", parsed.PreviousTxnID, id)
	}
	if parsed.PreviousTxnLgrSeq != seq {
		t.Errorf("PreviousTxnLgrSeq = %d, want %d", parsed.PreviousTxnLgrSeq, seq)
	}
}

func TestNFTokenPage_FreshEntryOmitsThreadingPointers(t *testing.T) {
	page := &state.NFTokenPageData{
		NFTokens: []state.NFTokenData{{NFTokenID: [32]byte{0xAA}, URI: "DEAD"}},
	}
	data, err := serializeNFTokenPage(page)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	fields, err := binarycodec.DecodeBytes(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := fields["PreviousTxnID"]; ok {
		t.Error("fresh page must not serialize PreviousTxnID")
	}
	if _, ok := fields["PreviousTxnLgrSeq"]; ok {
		t.Error("fresh page must not serialize PreviousTxnLgrSeq")
	}
}

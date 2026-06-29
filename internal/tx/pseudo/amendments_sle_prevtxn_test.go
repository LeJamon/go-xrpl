package pseudo

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

// PR #1131 regression guard: SerializeAmendmentsSLE must round-trip
// PreviousTxnID/PreviousTxnLgrSeq so a no-op EnableAmendment update re-serializes
// byte-identically and the apply layer prunes it (ApplyStateTable.cpp:156-157).
// Amendments is threaded only under fixPreviousTxnID.

func TestAmendments_ThreadingPointersRoundTrip(t *testing.T) {
	var id [32]byte
	for i := range id {
		id[i] = byte(i + 1)
	}
	const seq = uint32(33)

	sle := &AmendmentsSLE{
		Amendments:        [][32]byte{{0x01}, {0x02}},
		PreviousTxnID:     id,
		PreviousTxnLgrSeq: seq,
	}
	data, err := SerializeAmendmentsSLE(sle)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}

	parsed, err := ParseAmendmentsSLE(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.PreviousTxnID != id {
		t.Errorf("PreviousTxnID = %x, want %x", parsed.PreviousTxnID, id)
	}
	if parsed.PreviousTxnLgrSeq != seq {
		t.Errorf("PreviousTxnLgrSeq = %d, want %d", parsed.PreviousTxnLgrSeq, seq)
	}

	fields, err := binarycodec.DecodeBytes(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := fields["PreviousTxnID"]; !ok {
		t.Error("PreviousTxnID must be present in the serialized entry")
	}
	if _, ok := fields["PreviousTxnLgrSeq"]; !ok {
		t.Error("PreviousTxnLgrSeq must be present in the serialized entry")
	}
}

func TestAmendments_FreshEntryOmitsThreadingPointers(t *testing.T) {
	sle := &AmendmentsSLE{Amendments: [][32]byte{{0x01}}}
	data, err := SerializeAmendmentsSLE(sle)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	fields, err := binarycodec.DecodeBytes(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := fields["PreviousTxnID"]; ok {
		t.Error("fresh entry must not serialize PreviousTxnID")
	}
	if _, ok := fields["PreviousTxnLgrSeq"]; ok {
		t.Error("fresh entry must not serialize PreviousTxnLgrSeq")
	}
}

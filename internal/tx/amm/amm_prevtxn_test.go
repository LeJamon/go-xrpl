package amm

import "testing"

// PR #1131 regression guard: serializeAMMData must round-trip
// PreviousTxnID/PreviousTxnLgrSeq. AMM is threaded only under fixPreviousTxnID;
// once threaded a no-op modify must re-serialize byte-identically so the apply
// layer's unchanged-entry guard prunes it (ApplyStateTable.cpp:156-157).

func TestAMM_ThreadingPointersRoundTrip(t *testing.T) {
	var id [32]byte
	for i := range id {
		id[i] = byte(i + 1)
	}
	const seq = uint32(900)

	amm := buildTestAMM(t, 500)
	amm.PreviousTxnID = id
	amm.PreviousTxnLgrSeq = seq

	data, err := serializeAMMData(amm)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	fields := decodeFieldsBytes(t, data)
	if _, ok := fields["PreviousTxnID"]; !ok {
		t.Error("PreviousTxnID must be present in the serialized AMM")
	}
	if v, ok := fields["PreviousTxnLgrSeq"]; !ok || toUint64(v) != uint64(seq) {
		t.Errorf("PreviousTxnLgrSeq = %v, want %d", fields["PreviousTxnLgrSeq"], seq)
	}

	parsed, err := ParseAMMData(data)
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

func TestAMM_FreshEntryOmitsThreadingPointers(t *testing.T) {
	data, err := serializeAMMData(buildTestAMM(t, 500))
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	fields := decodeFieldsBytes(t, data)
	if _, ok := fields["PreviousTxnID"]; ok {
		t.Error("fresh AMM must not serialize PreviousTxnID")
	}
	if _, ok := fields["PreviousTxnLgrSeq"]; ok {
		t.Error("fresh AMM must not serialize PreviousTxnLgrSeq")
	}
}

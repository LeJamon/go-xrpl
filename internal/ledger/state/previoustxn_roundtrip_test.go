package state

import "testing"

// PR #1131 regression guard: the Delegate/DID/Oracle/PermissionedDomain
// serializers must round-trip PreviousTxnID/PreviousTxnLgrSeq so a no-op modify
// re-serializes byte-identically and the apply layer's unchanged-entry guard
// prunes it (ApplyStateTable.cpp:156-157). Mirrors the NegativeUNL guard in
// pseudo/negative_unl_sle_test.go.

func threadTestTxnID() [32]byte {
	var id [32]byte
	for i := range id {
		id[i] = byte(i + 1)
	}
	return id
}

func assertPrevTxnPresent(t *testing.T, data []byte, wantSeq uint32) {
	t.Helper()
	fields := decodeSLE(t, data)
	if _, ok := fields["PreviousTxnID"]; !ok {
		t.Error("PreviousTxnID must be present in the serialized entry")
	}
	seq, ok := fields["PreviousTxnLgrSeq"]
	if !ok {
		t.Fatal("PreviousTxnLgrSeq must be present in the serialized entry")
	}
	if v, _ := soeToUint64(seq); v != uint64(wantSeq) {
		t.Errorf("PreviousTxnLgrSeq = %v, want %d", seq, wantSeq)
	}
}

func assertPrevTxnAbsent(t *testing.T, data []byte) {
	t.Helper()
	fields := decodeSLE(t, data)
	if _, ok := fields["PreviousTxnID"]; ok {
		t.Error("fresh entry must not serialize PreviousTxnID")
	}
	if _, ok := fields["PreviousTxnLgrSeq"]; ok {
		t.Error("fresh entry must not serialize PreviousTxnLgrSeq")
	}
}

func TestDelegate_ThreadingPointersRoundTrip(t *testing.T) {
	id := threadTestTxnID()
	const seq = uint32(99240960)

	data, err := SerializeDelegate([20]byte{0x01}, [20]byte{0x02}, []uint32{1}, 7, id, seq)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	assertPrevTxnPresent(t, data, seq)

	parsed, err := ParseDelegate(data)
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

func TestDelegate_FreshEntryOmitsThreadingPointers(t *testing.T) {
	data, err := SerializeDelegate([20]byte{0x01}, [20]byte{0x02}, []uint32{1}, 7, [32]byte{}, 0)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	assertPrevTxnAbsent(t, data)
}

func TestDID_ThreadingPointersRoundTrip(t *testing.T) {
	id := threadTestTxnID()
	const seq = uint32(12345)
	addr, err := EncodeAccountID([20]byte{0x01})
	if err != nil {
		t.Fatalf("encode account: %v", err)
	}

	did := &DIDData{Account: [20]byte{0x01}, URI: "ABCD", PreviousTxnID: id, PreviousTxnLgrSeq: seq}
	data, err := SerializeDID(did, addr)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	assertPrevTxnPresent(t, data, seq)

	parsed, err := ParseDID(data)
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

func TestDID_FreshEntryOmitsThreadingPointers(t *testing.T) {
	addr, err := EncodeAccountID([20]byte{0x01})
	if err != nil {
		t.Fatalf("encode account: %v", err)
	}
	data, err := SerializeDID(&DIDData{Account: [20]byte{0x01}, URI: "ABCD"}, addr)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	assertPrevTxnAbsent(t, data)
}

func TestOracle_ThreadingPointersRoundTrip(t *testing.T) {
	id := threadTestTxnID()
	const seq = uint32(777)

	o := &OracleData{
		Owner:          [20]byte{0x01},
		Provider:       "464F4F",
		AssetClass:     "63757272656E6379",
		LastUpdateTime: 100,
		PreviousTxnID:  id, PreviousTxnLgrSeq: seq,
	}
	data, err := SerializeOracle(o)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	assertPrevTxnPresent(t, data, seq)

	parsed, err := ParseOracle(data)
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

func TestOracle_FreshEntryOmitsThreadingPointers(t *testing.T) {
	o := &OracleData{Owner: [20]byte{0x01}, Provider: "464F4F", AssetClass: "63757272656E6379", LastUpdateTime: 100}
	data, err := SerializeOracle(o)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	assertPrevTxnAbsent(t, data)
}

func TestPermissionedDomain_ThreadingPointersRoundTrip(t *testing.T) {
	id := threadTestTxnID()
	const seq = uint32(424242)
	addr, err := EncodeAccountID([20]byte{0x01})
	if err != nil {
		t.Fatalf("encode account: %v", err)
	}

	pd := &PermissionedDomainData{
		Owner:     [20]byte{0x01},
		Sequence:  5,
		OwnerNode: 0,
		AcceptedCredentials: []PermissionedDomainCredential{
			{Issuer: [20]byte{0x05}, CredentialType: []byte("KYC")},
		},
		PreviousTxnID: id, PreviousTxnLgrSeq: seq,
	}
	data, err := SerializePermissionedDomain(pd, addr)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	assertPrevTxnPresent(t, data, seq)

	parsed, err := ParsePermissionedDomain(data)
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

func TestPermissionedDomain_FreshEntryOmitsThreadingPointers(t *testing.T) {
	addr, err := EncodeAccountID([20]byte{0x01})
	if err != nil {
		t.Fatalf("encode account: %v", err)
	}
	pd := &PermissionedDomainData{
		Owner:    [20]byte{0x01},
		Sequence: 5,
		AcceptedCredentials: []PermissionedDomainCredential{
			{Issuer: [20]byte{0x05}, CredentialType: []byte("KYC")},
		},
	}
	data, err := SerializePermissionedDomain(pd, addr)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	assertPrevTxnAbsent(t, data)
}

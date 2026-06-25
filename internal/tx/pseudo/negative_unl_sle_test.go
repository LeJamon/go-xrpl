package pseudo

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

func makeKey(b byte) []byte {
	k := make([]byte, 33)
	k[0] = 0xED
	for i := 1; i < 33; i++ {
		k[i] = b
	}
	return k
}

// TestNegativeUNL_FirstLedgerSequenceRoundTrips asserts each DisabledValidator
// inner object carries sfFirstLedgerSequence through serialize → parse, and
// that it is emitted even at its zero value (both inner fields are soeREQUIRED).
func TestNegativeUNL_FirstLedgerSequenceRoundTrips(t *testing.T) {
	key1 := makeKey(0x01)
	key2 := makeKey(0x02)

	sle := &NegativeUNLSLE{
		DisabledValidators: []DisabledValidator{
			{PublicKey: key1, FirstLedgerSequence: 256},
			{PublicKey: key2, FirstLedgerSequence: 0},
		},
	}

	data, err := SerializeNegativeUNLSLE(sle)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}

	parsed, err := ParseNegativeUNLSLE(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(parsed.DisabledValidators) != 2 {
		t.Fatalf("DisabledValidators len = %d, want 2", len(parsed.DisabledValidators))
	}
	for i, want := range sle.DisabledValidators {
		got := parsed.DisabledValidators[i]
		if !bytes.Equal(got.PublicKey, want.PublicKey) {
			t.Errorf("entry %d PublicKey mismatch", i)
		}
		if got.FirstLedgerSequence != want.FirstLedgerSequence {
			t.Errorf("entry %d FirstLedgerSequence = %d, want %d", i, got.FirstLedgerSequence, want.FirstLedgerSequence)
		}
	}
}

// TestNegativeUNL_FirstLedgerSequenceSerializedPresent decodes the raw blob and
// asserts sfFirstLedgerSequence is present in the inner object (it is
// soeREQUIRED in rippled's sfDisabledValidator template). A zero value must
// still be serialized, mirroring the present-with-zero SLE-fork class.
func TestNegativeUNL_FirstLedgerSequenceSerializedPresent(t *testing.T) {
	sle := &NegativeUNLSLE{
		DisabledValidators: []DisabledValidator{
			{PublicKey: makeKey(0x07), FirstLedgerSequence: 0},
		},
	}

	data, err := SerializeNegativeUNLSLE(sle)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}

	fields, err := binarycodec.DecodeBytes(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	arr, ok := fields["DisabledValidators"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("DisabledValidators = %#v, want one-element array", fields["DisabledValidators"])
	}
	wrapper, ok := arr[0].(map[string]any)
	if !ok {
		t.Fatalf("entry type = %T, want map", arr[0])
	}
	inner, ok := wrapper["DisabledValidator"].(map[string]any)
	if !ok {
		t.Fatalf("DisabledValidator type = %T, want map", wrapper["DisabledValidator"])
	}
	if _, ok := inner["PublicKey"]; !ok {
		t.Error("PublicKey must be present (soeREQUIRED)")
	}
	v, ok := inner["FirstLedgerSequence"]
	if !ok {
		t.Fatal("FirstLedgerSequence must be present even at 0 (soeREQUIRED)")
	}
	if toUint32(v) != 0 {
		t.Errorf("FirstLedgerSequence = %v, want 0", v)
	}
}

// TestNegativeUNL_ThreadingPointersRoundTrip asserts the PreviousTxnID and
// PreviousTxnLgrSeq threading pointers survive serialize → parse and are emitted
// into the raw blob. The flag-ledger NegativeUNL transition re-serializes the
// entry outside any transaction (it is not re-threaded), so dropping these
// pointers forks account_hash — the #1092 regression guard.
func TestNegativeUNL_ThreadingPointersRoundTrip(t *testing.T) {
	txnID := make([]byte, 32)
	for i := range txnID {
		txnID[i] = byte(i + 1)
	}

	sle := &NegativeUNLSLE{
		DisabledValidators: []DisabledValidator{
			{PublicKey: makeKey(0x03), FirstLedgerSequence: 256},
		},
		PreviousTxnID:     txnID,
		PreviousTxnLgrSeq: 99240960,
	}

	data, err := SerializeNegativeUNLSLE(sle)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}

	parsed, err := ParseNegativeUNLSLE(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !bytes.Equal(parsed.PreviousTxnID, txnID) {
		t.Errorf("PreviousTxnID = %x, want %x", parsed.PreviousTxnID, txnID)
	}
	if parsed.PreviousTxnLgrSeq != 99240960 {
		t.Errorf("PreviousTxnLgrSeq = %d, want 99240960", parsed.PreviousTxnLgrSeq)
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

// TestNegativeUNL_FreshEntryOmitsThreadingPointers asserts a brand-new entry (no
// tx has threaded it yet) serializes without PreviousTxn pointers — the
// ApplyStateTable threads the creating tx in afterwards, matching rippled where
// they are absent at creation.
func TestNegativeUNL_FreshEntryOmitsThreadingPointers(t *testing.T) {
	sle := &NegativeUNLSLE{
		ValidatorToDisable: makeKey(0x05),
	}

	data, err := SerializeNegativeUNLSLE(sle)
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

	parsed, err := ParseNegativeUNLSLE(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.PreviousTxnID) != 0 || parsed.PreviousTxnLgrSeq != 0 {
		t.Errorf("fresh entry round-trip leaked threading pointers: id=%x seq=%d", parsed.PreviousTxnID, parsed.PreviousTxnLgrSeq)
	}
}

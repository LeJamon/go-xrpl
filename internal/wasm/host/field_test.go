package host

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/internal/wasm"
)

func fieldCode(t *testing.T, name string) int32 {
	t.Helper()
	fi, err := definitions.Get().GetFieldInstanceByFieldName(name)
	if err != nil {
		t.Fatalf("field %q: %v", name, err)
	}
	return fi.Ordinal
}

const testAddr = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

func encodeTx(t *testing.T, tx map[string]any) []byte {
	t.Helper()
	b, err := binarycodec.EncodeBytes(tx)
	if err != nil {
		t.Fatalf("encode tx: %v", err)
	}
	return b
}

// TestGetTxField checks the per-type value formatting of the field reader
// against a transaction encoded by goXRPL's binary codec.
func TestGetTxField(t *testing.T) {
	tx := map[string]any{
		"TransactionType": "Payment",
		"Account":         testAddr,
		"Destination":     testAddr,
		"Sequence":        uint32(42),
		"Fee":             "10",
		"Amount":          "1000000",
	}
	e := New(&mockView{tx: encodeTx(t, tx)})

	// UInt32 -> little-endian value bytes.
	got, herr := e.GetTxField(fieldCode(t, "Sequence"))
	if herr != wasm.HfSuccess {
		t.Fatalf("Sequence herr %d", herr)
	}
	if len(got) != 4 || binary.LittleEndian.Uint32(got) != 42 {
		t.Errorf("Sequence = %x, want little-endian 42", got)
	}

	// AccountID -> raw 20 bytes, no length prefix.
	_, wantAcct, err := addresscodec.DecodeClassicAddressToAccountID(testAddr)
	if err != nil {
		t.Fatal(err)
	}
	got, herr = e.GetTxField(fieldCode(t, "Account"))
	if herr != wasm.HfSuccess || !bytes.Equal(got, wantAcct) {
		t.Errorf("Account = %x (herr %d), want %x", got, herr, wantAcct)
	}

	// Amount (XRP) -> 8 wire bytes.
	got, herr = e.GetTxField(fieldCode(t, "Amount"))
	if herr != wasm.HfSuccess || len(got) != 8 {
		t.Errorf("Amount len = %d (herr %d), want 8", len(got), herr)
	}
}

func TestGetTxFieldErrors(t *testing.T) {
	tx := map[string]any{
		"TransactionType": "Payment",
		"Account":         testAddr,
		"Destination":     testAddr,
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        uint32(1),
	}
	e := New(&mockView{tx: encodeTx(t, tx)})

	// A valid field that is absent from this tx.
	if _, herr := e.GetTxField(fieldCode(t, "DestinationTag")); herr != wasm.HfFieldNotFound {
		t.Errorf("absent field herr = %d, want HfFieldNotFound", herr)
	}
	// An unknown field code.
	if _, herr := e.GetTxField(0x7FFF0001); herr != wasm.HfInvalidField {
		t.Errorf("unknown field herr = %d, want HfInvalidField", herr)
	}
}

// TestGetTxArrayLen counts an STArray field.
func TestGetTxArrayLen(t *testing.T) {
	tx := map[string]any{
		"TransactionType": "Payment",
		"Account":         testAddr,
		"Destination":     testAddr,
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        uint32(1),
		"Memos": []any{
			map[string]any{"Memo": map[string]any{"MemoData": "AABB"}},
			map[string]any{"Memo": map[string]any{"MemoData": "CCDD"}},
			map[string]any{"Memo": map[string]any{"MemoData": "EEFF"}},
		},
	}
	e := New(&mockView{tx: encodeTx(t, tx)})

	n, herr := e.GetTxArrayLen(fieldCode(t, "Memos"))
	if herr != wasm.HfSuccess || n != 3 {
		t.Errorf("Memos len = %d (herr %d), want 3", n, herr)
	}
	// Memos is an array, not a leaf field.
	if _, herr := e.GetTxField(fieldCode(t, "Memos")); herr != wasm.HfNotLeafField {
		t.Errorf("GetTxField(Memos) herr = %d, want HfNotLeafField", herr)
	}
	// A non-array field reports NoArray.
	if _, herr := e.GetTxArrayLen(fieldCode(t, "Sequence")); herr != wasm.HfNoArray {
		t.Errorf("ArrayLen(Sequence) herr = %d, want HfNoArray", herr)
	}
}

// TestCacheAndLedgerObjField caches a ledger entry and reads a field from it.
func TestCacheAndLedgerObjField(t *testing.T) {
	obj := encodeTx(t, map[string]any{
		"TransactionType": "Payment",
		"Account":         testAddr,
		"Destination":     testAddr,
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        uint32(99),
	})
	var idx [32]byte
	idx[0], idx[31] = 0xAB, 0xCD
	e := New(&mockView{sles: map[[32]byte][]byte{idx: obj}})

	slot, herr := e.CacheLedgerObj(idx[:], 0)
	if herr != wasm.HfSuccess || slot != 1 {
		t.Fatalf("CacheLedgerObj = %d, %d, want slot 1", slot, herr)
	}
	got, herr := e.GetLedgerObjField(slot, fieldCode(t, "Sequence"))
	if herr != wasm.HfSuccess || binary.LittleEndian.Uint32(got) != 99 {
		t.Errorf("cached Sequence = %x (herr %d), want 99", got, herr)
	}
	// Missing object.
	var missing [32]byte
	missing[0] = 0x01
	if _, herr := e.CacheLedgerObj(missing[:], 0); herr != wasm.HfLedgerObjNotFound {
		t.Errorf("missing obj herr = %d, want HfLedgerObjNotFound", herr)
	}
	// Empty slot.
	if _, herr := e.GetLedgerObjField(5, fieldCode(t, "Sequence")); herr != wasm.HfEmptySlot {
		t.Errorf("empty slot herr = %d, want HfEmptySlot", herr)
	}
}

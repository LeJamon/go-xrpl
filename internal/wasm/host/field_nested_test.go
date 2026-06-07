package host

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/wasm"
)

func locatorBytes(codes ...int32) []byte {
	b := make([]byte, 4*len(codes))
	for i, c := range codes {
		binary.LittleEndian.PutUint32(b[i*4:], uint32(c))
	}
	return b
}

// TestNestedField navigates into an array of objects: Memos[i].MemoData.
func TestNestedField(t *testing.T) {
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
		},
	}
	e := New(&mockView{tx: encodeTx(t, tx)})
	memos := fieldCode(t, "Memos")
	memoData := fieldCode(t, "MemoData")

	got, herr := e.GetTxNestedField(locatorBytes(memos, 0, memoData))
	if herr != wasm.HfSuccess || !bytes.Equal(got, []byte{0xAA, 0xBB}) {
		t.Errorf("Memos[0].MemoData = %x (herr %d), want AABB", got, herr)
	}
	got, herr = e.GetTxNestedField(locatorBytes(memos, 1, memoData))
	if herr != wasm.HfSuccess || !bytes.Equal(got, []byte{0xCC, 0xDD}) {
		t.Errorf("Memos[1].MemoData = %x (herr %d), want CCDD", got, herr)
	}

	// Nested array length of the top-level Memos field.
	if n, herr := e.GetTxNestedArrayLen(locatorBytes(memos)); herr != wasm.HfSuccess || n != 2 {
		t.Errorf("nested Memos len = %d (herr %d), want 2", n, herr)
	}
}

func TestNestedFieldErrors(t *testing.T) {
	tx := map[string]any{
		"TransactionType": "Payment",
		"Account":         testAddr,
		"Destination":     testAddr,
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        uint32(1),
		"Memos": []any{
			map[string]any{"Memo": map[string]any{"MemoData": "AABB"}},
		},
	}
	e := New(&mockView{tx: encodeTx(t, tx)})
	memos := fieldCode(t, "Memos")
	memoData := fieldCode(t, "MemoData")

	// Array index out of bounds.
	if _, herr := e.GetTxNestedField(locatorBytes(memos, 5, memoData)); herr != wasm.HfIndexOutOfBounds {
		t.Errorf("out-of-bounds herr = %d, want HfIndexOutOfBounds", herr)
	}
	// Malformed locator (not a multiple of 4).
	if _, herr := e.GetTxNestedField([]byte{1, 2, 3}); herr != wasm.HfLocatorMalformed {
		t.Errorf("malformed locator herr = %d, want HfLocatorMalformed", herr)
	}
	// Empty locator.
	if _, herr := e.GetTxNestedField(nil); herr != wasm.HfLocatorMalformed {
		t.Errorf("empty locator herr = %d, want HfLocatorMalformed", herr)
	}
}

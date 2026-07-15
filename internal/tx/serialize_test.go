package tx

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"
)

func TestTransactionIndexFromMetadata(t *testing.T) {
	metaData, err := SerializeMetadata(&Metadata{TransactionIndex: 37})
	if err != nil {
		t.Fatal(err)
	}

	got, ok := TransactionIndexFromMetadata(metaData)
	if !ok {
		t.Fatal("TransactionIndexFromMetadata rejected canonical metadata")
	}
	if got != 37 {
		t.Fatalf("transaction index = %d, want 37", got)
	}
}

func TestTransactionIndexFromTxWithMetaBlob_JSONFallback(t *testing.T) {
	tests := []struct {
		name         string
		value        any
		includeField bool
		want         uint32
		wantOK       bool
	}{
		{name: "zero", value: 0, includeField: true, wantOK: true},
		{name: "integral", value: 19, includeField: true, want: 19, wantOK: true},
		{name: "maximum", value: uint64(math.MaxUint32), includeField: true, want: math.MaxUint32, wantOK: true},
		{name: "fractional", value: 1.5, includeField: true},
		{name: "negative", value: -1, includeField: true},
		{name: "overflow", value: uint64(math.MaxUint32) + 1, includeField: true},
		{name: "string", value: "7", includeField: true},
		{name: "null", value: nil, includeField: true},
		{name: "missing"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			meta := map[string]any{}
			if tc.includeField {
				meta["TransactionIndex"] = tc.value
			}
			blob, err := json.Marshal(map[string]any{
				"tx_json": map[string]any{},
				"meta":    meta,
			})
			if err != nil {
				t.Fatal(err)
			}

			got, ok := TransactionIndexFromTxWithMetaBlob(blob)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Fatalf("transaction index = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSplitTxWithMetaBlob_RoundTrip(t *testing.T) {
	txData := []byte("transaction data here")
	metaData := []byte("metadata bytes here")

	// Manually create the blob the same way CreateTxWithMetaBlob does
	vlTx, err := EncodeWithVL(txData)
	if err != nil {
		t.Fatal(err)
	}
	vlMeta, err := EncodeWithVL(metaData)
	if err != nil {
		t.Fatal(err)
	}
	blob := append(vlTx, vlMeta...)

	// Split it back
	gotTx, gotMeta, err := SplitTxWithMetaBlob(blob)
	if err != nil {
		t.Fatalf("SplitTxWithMetaBlob error: %v", err)
	}
	if !bytes.Equal(gotTx, txData) {
		t.Errorf("tx data mismatch: got %q, want %q", gotTx, txData)
	}
	if !bytes.Equal(gotMeta, metaData) {
		t.Errorf("meta data mismatch: got %q, want %q", gotMeta, metaData)
	}
}

func TestSplitTxWithMetaBlob_LargeData(t *testing.T) {
	// Test with data sizes that cross VL prefix boundaries
	sizes := []int{192, 193, 500, 12480}

	for _, txSize := range sizes {
		for _, metaSize := range sizes {
			txData := bytes.Repeat([]byte{0xAB}, txSize)
			metaData := bytes.Repeat([]byte{0xCD}, metaSize)

			vlTx, _ := EncodeWithVL(txData)
			vlMeta, _ := EncodeWithVL(metaData)
			blob := append(vlTx, vlMeta...)

			gotTx, gotMeta, err := SplitTxWithMetaBlob(blob)
			if err != nil {
				t.Fatalf("SplitTxWithMetaBlob(%d,%d) error: %v", txSize, metaSize, err)
			}
			if !bytes.Equal(gotTx, txData) {
				t.Errorf("tx data mismatch for sizes (%d,%d)", txSize, metaSize)
			}
			if !bytes.Equal(gotMeta, metaData) {
				t.Errorf("meta data mismatch for sizes (%d,%d)", txSize, metaSize)
			}
		}
	}
}

func TestSplitTxWithMetaBlob_TxOnly(t *testing.T) {
	txData := []byte("just tx, no meta")
	vlTx, _ := EncodeWithVL(txData)

	gotTx, gotMeta, err := SplitTxWithMetaBlob(vlTx)
	if err != nil {
		t.Fatalf("SplitTxWithMetaBlob error: %v", err)
	}
	if !bytes.Equal(gotTx, txData) {
		t.Errorf("tx data mismatch: got %q, want %q", gotTx, txData)
	}
	if gotMeta != nil {
		t.Errorf("expected nil meta, got %q", gotMeta)
	}
}

func TestSplitTxWithMetaBlob_Errors(t *testing.T) {
	// Empty blob
	_, _, err := SplitTxWithMetaBlob(nil)
	if err == nil {
		t.Error("expected error for nil blob")
	}

	// Truncated tx data (VL says 100 bytes but only 0 follow)
	_, _, err = SplitTxWithMetaBlob([]byte{100})
	if err == nil {
		t.Error("expected error for truncated tx data")
	}
}

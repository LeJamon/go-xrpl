package tx

import (
	"bytes"
	"testing"
)

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

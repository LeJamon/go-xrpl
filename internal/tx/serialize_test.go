package tx

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestCreateTxWithMetaBlob_TrustedMetadataOverJSONLimit(t *testing.T) {
	const (
		inputOffers   = 500
		deletedNodes  = 962
		affectedNodes = 1027
	)

	offers := make([]any, inputOffers)
	for i := range offers {
		offers[i] = fmt.Sprintf("%064X", i+1)
	}
	txHex, err := binarycodec.Encode(map[string]any{
		"TransactionType": "NFTokenCancelOffer",
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Fee":             "10",
		"Sequence":        uint32(1),
		"SigningPubKey":   "",
		"NFTokenOffers":   offers,
	})
	if err != nil {
		t.Fatalf("encode NFTokenCancelOffer at protocol limit: %v", err)
	}
	txBlob, err := hex.DecodeString(txHex)
	if err != nil {
		t.Fatalf("decode transaction blob: %v", err)
	}

	nodes := make([]AffectedNode, affectedNodes)
	for i := range nodes {
		nodes[i] = AffectedNode{
			NodeType:        "DeletedNode",
			LedgerEntryType: "NFTokenOffer",
			LedgerIndex:     fmt.Sprintf("%064X", i+1),
		}
		if i >= deletedNodes {
			nodes[i].NodeType = "ModifiedNode"
			nodes[i].LedgerEntryType = "AccountRoot"
		}
	}

	combined, err := CreateTxWithMetaBlob(txBlob, &Metadata{
		AffectedNodes:     nodes,
		TransactionResult: ter.TesSUCCESS,
	})
	if err != nil {
		t.Fatalf("create tx+meta blob with %d affected nodes: %v", affectedNodes, err)
	}

	gotTx, metaBlob, err := SplitTxWithMetaBlob(combined)
	if err != nil {
		t.Fatalf("split tx+meta blob: %v", err)
	}
	if !bytes.Equal(gotTx, txBlob) {
		t.Fatal("transaction blob changed during combination")
	}
	decoded, err := binarycodec.DecodeBytes(metaBlob)
	if err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	gotNodes, ok := decoded["AffectedNodes"].([]any)
	if !ok {
		t.Fatalf("AffectedNodes type = %T, want []any", decoded["AffectedNodes"])
	}
	if len(gotNodes) != affectedNodes {
		t.Fatalf("AffectedNodes count = %d, want %d", len(gotNodes), affectedNodes)
	}
}

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

func TestSerializeMetadataIncludesParentBatchID(t *testing.T) {
	parent := [32]byte{1, 2, 3, 4}
	encoded, err := SerializeMetadata(&Metadata{
		TransactionResult: ter.TesSUCCESS,
		TransactionIndex:  7,
		AffectedNodes:     []AffectedNode{},
		ParentBatchID:     &parent,
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := binarycodec.Decode(hex.EncodeToString(encoded))
	if err != nil {
		t.Fatal(err)
	}
	want := "01020304" + string(bytes.Repeat([]byte{'0'}, 56))
	if got := decoded["ParentBatchID"]; got != want {
		t.Fatalf("ParentBatchID = %v, want %s", got, want)
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

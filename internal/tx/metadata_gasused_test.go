package tx

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

// TestSerializeMetadata_WASMRejectedWithGasUsed pins the SmartEscrow metadata
// path end to end: a tecWASM_REJECTED result (an applied tec, so its metadata
// must serialize) round-trips through the binary codec, and a GasUsed field
// (sfGasUsed, field 73) is carried alongside it.
func TestSerializeMetadata_WASMRejectedWithGasUsed(t *testing.T) {
	gas := uint32(1137)
	meta := &Metadata{
		TransactionResult: TecWASM_REJECTED,
		TransactionIndex:  2,
		GasUsed:           &gas,
	}

	blob, err := SerializeMetadata(meta)
	if err != nil {
		t.Fatalf("SerializeMetadata(tecWASM_REJECTED + GasUsed): %v", err)
	}
	if len(blob) == 0 {
		t.Fatal("empty metadata blob")
	}

	decoded, err := binarycodec.DecodeBytes(blob)
	if err != nil {
		t.Fatalf("decode metadata blob: %v", err)
	}
	if got, ok := decoded["TransactionResult"].(string); !ok || got != "tecWASM_REJECTED" {
		t.Errorf("TransactionResult = %v (%T), want %q", decoded["TransactionResult"], decoded["TransactionResult"], "tecWASM_REJECTED")
	}
	if got, ok := decoded["GasUsed"].(uint32); !ok || got != gas {
		t.Errorf("GasUsed = %v (%T), want uint32 %d", decoded["GasUsed"], decoded["GasUsed"], gas)
	}
}

// TestSerializeMetadata_NoGasUsedWhenAbsent confirms GasUsed is omitted from the
// metadata when no FinishFunction ran, so non-SmartEscrow metadata is unchanged.
func TestSerializeMetadata_NoGasUsedWhenAbsent(t *testing.T) {
	meta := &Metadata{TransactionResult: TesSUCCESS, TransactionIndex: 0}

	blob, err := SerializeMetadata(meta)
	if err != nil {
		t.Fatalf("SerializeMetadata: %v", err)
	}
	decoded, err := binarycodec.DecodeBytes(blob)
	if err != nil {
		t.Fatalf("decode metadata blob: %v", err)
	}
	if _, present := decoded["GasUsed"]; present {
		t.Error("GasUsed should be absent when no FinishFunction ran")
	}
}

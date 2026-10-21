package tx_test

import (
	"testing"

	binarytypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
)

func TestCanonicalizeSerializedTransactionPreservesOverCapNativeAmount(t *testing.T) {
	all.RegisterAll()
	const overCap = uint64(binarytypes.MaxDrops) + 1
	fields := map[string]any{
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Amount":          binarytypes.RawXRPAmount{Drops: overCap},
		"Destination":     "r4bbzCamAis69rNoRdSaMSmPb1kDUHXcAL",
		"Fee":             "10",
		"Sequence":        uint32(1),
		"SigningPubKey":   "",
		"TransactionType": "EscrowCreate",
	}

	canonical, err := tx.CanonicalizeSerializedTransaction(fields)
	if err != nil {
		t.Fatalf("CanonicalizeSerializedTransaction: %v", err)
	}
	raw, ok := canonical["Amount"].(binarytypes.RawXRPAmount)
	if !ok {
		t.Fatalf("canonical Amount type = %T, want RawXRPAmount", canonical["Amount"])
	}
	if raw.Drops != overCap {
		t.Fatalf("canonical Amount drops = %d, want %d", raw.Drops, overCap)
	}
	if _, err := tx.CanonicalizeSerializedObject(fields); err == nil {
		t.Fatal("CanonicalizeSerializedObject accepted an over-cap transaction amount")
	}
}

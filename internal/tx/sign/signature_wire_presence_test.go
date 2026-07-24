package sign

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
)

func TestVerifySignaturePreservesExplicitEmptyMemos(t *testing.T) {
	wire := map[string]any{
		"Account":            "rNjPKYwqAn1a5gjAHfDabDSoGNeHUt86jE",
		"Amount":             "60270816",
		"Destination":        "rA7nApscqBLTFaEpbbvwjknLBVWi1yyGN",
		"Fee":                "10",
		"LastLedgerSequence": uint32(19272585),
		"Memos":              []map[string]any{},
		"Sequence":           uint32(15252466),
		"SigningPubKey":      "EDEB4CCCDB7ABDB201DEDFCCC4B11FF93E9ECB17077FDE13A5AC3C0F99B6DE123E",
		"TransactionType":    "Payment",
		"TxnSignature":       "C638A6D85A5E39AD42F67330E54EB9F3B4FED99403F85FA70425B459F5BFC6493481178BFE35C65A23DC2747792F445BF06466DFED7692F016B299C08030C406",
	}

	blob, err := binarycodec.EncodeBytes(wire)
	if err != nil {
		t.Fatalf("encode transaction: %v", err)
	}
	txn, err := txcore.ParseFromBinary(blob)
	if err != nil {
		t.Fatalf("parse transaction: %v", err)
	}

	hash, err := txcore.ComputeTransactionHash(txn)
	if err != nil {
		t.Fatalf("compute transaction hash: %v", err)
	}
	const expectedHash = "AF75562294643489489838F72B71BF17DA6E57D1103BE004193A53B0BC5E46A8"
	if got := fmt.Sprintf("%X", hash); got != expectedHash {
		t.Fatalf("transaction hash = %s, want %s", got, expectedHash)
	}

	if err := VerifySignature(txn, true); err != nil {
		t.Fatalf("verify signature: %v", err)
	}
}

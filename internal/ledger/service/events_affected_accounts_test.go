package service

import (
	"encoding/hex"
	"sort"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestExtractAffectedAccountsUsesMetadataNodes(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	blob, err := txcore.CreateTxWithMetaBlob([]byte{0x12, 0x00}, &txcore.Metadata{
		TransactionResult: ter.TesSUCCESS,
		AffectedNodes: []txcore.AffectedNode{{
			NodeType:        "CreatedNode",
			LedgerEntryType: "AccountRoot",
			LedgerIndex:     "0000000000000000000000000000000000000000000000000000000000000001",
			NewFields:       map[string]any{"Account": account},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	accounts := ParseAcceptedTransaction(blob).AffectedAccounts()
	if len(accounts) != 1 || accounts[0] != account {
		t.Fatalf("affected accounts = %v, want [%s]", accounts, account)
	}
}

func TestExtractMentionedAccountsUsesTopLevelSTTxFields(t *testing.T) {
	const (
		source      = "r9cZA1mLK5R5Am25ArfXFmqgNwjZgnfk59"
		destination = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		issuer      = "rDsbeomae4FXwgQTJp9Rs64Qg9vDiTCdBv"
		signer      = "rB5Ux4Lv2nRx6eeoAAsZmtctnBQ2LiACnk"
	)
	blobHex, err := binarycodec.Encode(map[string]any{
		"TransactionType": "Payment",
		"Account":         source,
		"Destination":     destination,
		"Amount": map[string]any{
			"currency": "USD",
			"issuer":   issuer,
			"value":    "1",
		},
		"Signers": []any{
			map[string]any{
				"Signer": map[string]any{
					"Account":       signer,
					"SigningPubKey": "",
					"TxnSignature":  "",
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := hex.DecodeString(blobHex)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{source, destination, issuer}
	sort.Strings(want)
	got := extractMentionedAccounts(blob)
	if len(got) != len(want) {
		t.Fatalf("mentioned accounts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mentioned accounts = %v, want %v", got, want)
		}
	}
}

func TestMayPublishProposedTransactionRejectsInnerBatch(t *testing.T) {
	if !mayPublishProposedTransaction(0) {
		t.Fatal("ordinary transaction should be publishable")
	}
	if mayPublishProposedTransaction(txcore.TfInnerBatchTxn) {
		t.Fatal("inner Batch transaction must not be published")
	}
}

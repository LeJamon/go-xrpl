package openledger_test

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/shamap"
)

// Testnet ledger 21403321 had identical state, transactions, fees and close time,
// but a different transaction root: an expired mint committed during the retry
// pass instead of waiting until the fee-claim pass after successful transactions.
func TestApplyTxs_ExpiredNFTokenMintDefersFeeClaim(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	alice := jtx.NewAccount("expired-mint-alice")
	bob := jtx.NewAccount("expired-mint-bob")
	carol := jtx.NewAccount("expired-mint-carol")
	env.Fund(alice, bob, carol)
	view := freshView(t, env)
	mint := nft.NFTokenMint(alice, 0).Amount(jtx.XRPTxAmount(1)).Expiration(100).Sequence(env.Seq(alice)).Build()
	mintBlob := buildSignedBlob(t, env, mint, alice)
	mintPending, err := openledger.ParsePendingTx(mintBlob)
	if err != nil {
		t.Fatal(err)
	}
	pay := pendingPayment(t, env, payment.Pay(bob, carol, 1_000_000).Sequence(env.Seq(bob)), bob)
	var retries []openledger.PendingTx
	if err := openledger.ApplyTxs(view, []openledger.PendingTx{mintPending, pay}, &retries, openledger.ApplyConfig{
		BaseFee: 10, ReserveBase: 200_000_000, ReserveIncrement: 50_000_000,
		LedgerSequence: view.Sequence(), ParentCloseTime: 100,
		Rules: amendment.AllSupportedRules(), Mode: openledger.BuildLedgerMode,
	}); err != nil {
		t.Fatal(err)
	}
	if len(retries) != 0 {
		t.Fatalf("leftover retries = %d", len(retries))
	}
	for _, want := range []struct {
		hash  [32]byte
		index uint32
	}{{pay.Hash, 0}, {mintPending.Hash, 1}} {
		blob, found, err := view.GetTransaction(want.hash)
		if err != nil || !found {
			t.Fatalf("transaction %x missing: %v", want.hash, err)
		}
		_, metadata, err := tx.SplitTxWithMetaBlobStrict(blob)
		if err != nil {
			t.Fatal(err)
		}
		index, ok := tx.TransactionIndexFromMetadata(metadata)
		if !ok || index != want.index {
			t.Errorf("transaction %x index=%d, want %d", want.hash, index, want.index)
		}
		if want.hash == mintPending.Hash {
			fields, err := binarycodec.DecodeBytes(metadata)
			if err != nil {
				t.Fatal(err)
			}
			if fields["TransactionResult"] != "tecEXPIRED" {
				t.Errorf("mint result = %v", fields["TransactionResult"])
			}
		}
	}
}

// Prove the incident's exact transaction-root difference is explained by the
// fee-claim ordering alone. This fixture contains public validated testnet data.
func TestTestnet21403321_ExpiredMintIndexExplainsDivergence(t *testing.T) {
	data, err := os.ReadFile("testdata/testnet-21403321.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Result struct {
			Ledger struct {
				Transactions    []map[string]any `json:"transactions"`
				TransactionHash string           `json:"transaction_hash"`
			} `json:"ledger"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, earlyMint := range []bool{false, true} {
		tree := shamap.New(shamap.TypeTransaction)
		for _, record := range fixture.Result.Ledger.Transactions {
			fields := make(map[string]any, len(record))
			for key, value := range record {
				if key != "hash" && key != "metaData" {
					fields[key] = value
				}
			}
			metadata := make(map[string]any)
			for key, value := range record["metaData"].(map[string]any) {
				metadata[key] = value
			}
			if earlyMint {
				index := int(metadata["TransactionIndex"].(float64))
				if index == 11 {
					metadata["TransactionIndex"] = 7
				} else if index >= 7 {
					metadata["TransactionIndex"] = index + 1
				}
			}
			txBlob, err := binarycodec.EncodeBytes(fields)
			if err != nil {
				t.Fatal(err)
			}
			metaBlob, err := binarycodec.EncodeBytesTrusted(metadata)
			if err != nil {
				t.Fatal(err)
			}
			vlTx, err := tx.EncodeWithVL(txBlob)
			if err != nil {
				t.Fatal(err)
			}
			vlMeta, err := tx.EncodeWithVL(metaBlob)
			if err != nil {
				t.Fatal(err)
			}
			id, err := hex.DecodeString(record["hash"].(string))
			if err != nil {
				t.Fatal(err)
			}
			var hash [32]byte
			copy(hash[:], id)
			if err := tree.PutWithNodeType(hash, append(vlTx, vlMeta...), shamap.NodeTypeTransactionWithMeta); err != nil {
				t.Fatal(err)
			}
		}
		root, err := tree.Hash()
		if err != nil {
			t.Fatal(err)
		}
		got := fmt.Sprintf("%x", root)
		if !earlyMint && !strings.EqualFold(got, fixture.Result.Ledger.TransactionHash) {
			t.Fatalf("canonical root = %s", got)
		}
		if earlyMint && !strings.HasPrefix(got, "a7e48eecd7c7bc36") {
			t.Fatalf("early-mint root = %s, want observed divergent prefix a7e48eecd7c7bc36", got)
		}
		t.Logf("earlyMint=%t transaction_root=%s", earlyMint, got)
	}
}

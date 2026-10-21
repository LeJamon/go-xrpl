package openledger_test

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
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
	fixture := readTransactionRootFixture(t, "testnet-21403321.json")
	for _, earlyMint := range []bool{false, true} {
		var mutateMetadata func(map[string]any) error
		if earlyMint {
			mutateMetadata = shiftTransactionIndex(11, 7)
		}
		got := reconstructTransactionRoot(t, fixture.Result.Ledger.Transactions, mutateMetadata)
		if !earlyMint && !strings.EqualFold(got, fixture.Result.Ledger.TransactionHash) {
			t.Fatalf("canonical root = %s", got)
		}
		if earlyMint && !strings.HasPrefix(got, "a7e48eecd7c7bc36") {
			t.Fatalf("early-mint root = %s, want observed divergent prefix a7e48eecd7c7bc36", got)
		}
		t.Logf("earlyMint=%t transaction_root=%s", earlyMint, got)
	}
}

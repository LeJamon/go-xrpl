package openledger_test

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
)

func TestApplyTxs_SeededRetriesHonorRetrySalt(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)

	alice := jtx.NewAccount("retry-salt-alice")
	bob := jtx.NewAccount("retry-salt-bob")
	env.Fund(alice, bob)
	view := freshView(t, env)

	seeded := []openledger.PendingTx{
		pendingPayment(t, env, payment.Pay(alice, bob, 1_000_000).Sequence(env.Seq(alice)), alice),
		pendingPayment(t, env, payment.Pay(bob, alice, 1_000_000).Sequence(env.Seq(bob)), bob),
	}
	salt := reverseAccountOrderSalt(seeded[0].Account, seeded[1].Account)
	canonical := append([]openledger.PendingTx(nil), seeded...)
	openledger.CanonicalSort(canonical, salt)
	if canonical[0].Hash != seeded[1].Hash || canonical[1].Hash != seeded[0].Hash {
		t.Fatalf("salt did not reverse seeded insertion order: got %x, %x want %x, %x",
			canonical[0].Hash[:4], canonical[1].Hash[:4], seeded[1].Hash[:4], seeded[0].Hash[:4])
	}

	retries := append([]openledger.PendingTx(nil), seeded...)
	if err := openledger.ApplyTxs(view, nil, &retries, openledger.ApplyConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   view.Sequence(),
		NetworkID:        0,
		Rules:            amendment.AllSupportedRules(),
		RetrySalt:        &salt,
	}); err != nil {
		t.Fatalf("ApplyTxs seeded retries: %v", err)
	}
	if len(retries) != 0 {
		t.Fatalf("retries = %d, want 0", len(retries))
	}

	indexes := make(map[[32]byte]uint32, len(seeded))
	var visitErr error
	if err := view.ForEachTransaction(func(hash [32]byte, data []byte) bool {
		_, metadataBlob, err := txcore.SplitTxWithMetaBlobStrict(data)
		if err != nil {
			visitErr = fmt.Errorf("split transaction %x: %w", hash, err)
			return false
		}
		index, ok := txcore.TransactionIndexFromMetadata(metadataBlob)
		if !ok {
			visitErr = fmt.Errorf("transaction %x has no metadata index", hash)
			return false
		}
		indexes[hash] = index
		return true
	}); err != nil {
		t.Fatalf("ForEachTransaction: %v", err)
	}
	if visitErr != nil {
		t.Fatal(visitErr)
	}
	if len(indexes) != len(seeded) {
		t.Fatalf("metadata indexes = %d, want %d", len(indexes), len(seeded))
	}
	if indexes[canonical[0].Hash] >= indexes[canonical[1].Hash] {
		t.Fatalf("metadata indexes do not follow RetrySalt order: first=%d second=%d",
			indexes[canonical[0].Hash], indexes[canonical[1].Hash])
	}
}

func reverseAccountOrderSalt(first, second [20]byte) [32]byte {
	var salt [32]byte
	for i := range first {
		diff := first[i] ^ second[i]
		for mask := byte(0x80); mask != 0; mask >>= 1 {
			if diff&mask != 0 {
				salt[i] = ^first[i] & mask
				return salt
			}
		}
	}
	panic("cannot choose salt for identical account IDs")
}

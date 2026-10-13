package openledger_test

import (
	"context"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	ledgertestutil "github.com/LeJamon/go-xrpl/internal/ledger/testutil"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/shamap"
)

func TestApplyTxsMembershipErrorDoesNotMutateView(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	ptx := pendingPayment(t, env, payment.Pay(alice, bob, 1).Sequence(1), alice)

	source := shamap.New(shamap.TypeTransaction)
	for branch := range byte(16) {
		key := [32]byte{branch << 4}
		if err := source.PutWithNodeType(key, []byte{branch, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, shamap.NodeTypeTransactionWithMeta); err != nil {
			t.Fatalf("build transaction map: %v", err)
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("hash transaction map: %v", err)
	}
	var entries []shamap.FlushEntry
	if err := source.StoreDirty(func(dirty []shamap.FlushEntry) error {
		entries = dirty
		return nil
	}); err != nil {
		t.Fatalf("flush transaction map: %v", err)
	}
	family := make(membershipFamily)
	if err := family.StoreBatch(context.Background(), entries); err != nil {
		t.Fatal(err)
	}
	partial, err := shamap.NewFromRootHash(shamap.TypeTransaction, rootHash, family)
	if err != nil {
		t.Fatalf("load backed transaction map: %v", err)
	}
	view, err := ledger.NewOpenWithHeader(header.LedgerHeader{LedgerIndex: 1}, shamap.New(shamap.TypeState), partial, drops.Fees{}, ledgertestutil.NewExecutionStore(t, family))
	if err != nil {
		t.Fatalf("create partial ledger view: %v", err)
	}
	t.Cleanup(view.Discard)
	for hash := range family {
		if hash != rootHash {
			delete(family, hash)
		}
	}
	before, err := view.TxMapHash()
	if err != nil {
		t.Fatalf("transaction root before apply: %v", err)
	}

	err = openledger.ApplyTxs(view, []openledger.PendingTx{ptx}, nil, openledger.ApplyConfig{
		Mode:  openledger.OpenLedgerMode,
		Rules: amendment.AllSupportedRules(),
	})
	if err == nil {
		t.Fatal("ApplyTxs succeeded with an incomplete transaction map")
	}
	if !errors.Is(err, shamap.ErrNodeNotInStore) {
		t.Fatalf("ApplyTxs error = %v, want missing-node error", err)
	}
	after, hashErr := view.TxMapHash()
	if hashErr != nil {
		t.Fatalf("transaction root after apply: %v", hashErr)
	}
	if after != before {
		t.Fatalf("transaction root changed after membership error: %x -> %x", before, after)
	}
}

type membershipFamily map[[32]byte][]byte

func (f membershipFamily) Fetch(_ context.Context, hash [32]byte) ([]byte, error) {
	return f[hash], nil
}

func (f membershipFamily) StoreBatch(_ context.Context, entries []shamap.FlushEntry) error {
	for _, entry := range entries {
		f[entry.Hash] = entry.Data
	}
	return nil
}

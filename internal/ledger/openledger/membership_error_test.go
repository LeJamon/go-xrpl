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
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

func TestApplyTxsMembershipErrorDoesNotMutateView(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	ptx := pendingPayment(t, env, payment.Pay(alice, bob, 1).Sequence(1), alice)

	source := shamap.New(shamap.TypeTransaction)
	for branch := range byte(16) {
		key := [32]byte{branch << 4}
		if err := source.Put(key, []byte{branch, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}); err != nil {
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
	var rootData []byte
	for _, entry := range entries {
		if entry.Hash == rootHash {
			rootData = entry.Data
			break
		}
	}
	if rootData == nil {
		t.Fatal("transaction map root not flushed")
	}

	family := backend.NewMemory()
	if err := family.StoreBatch(context.Background(), []shamap.FlushEntry{{
		Hash: rootHash, Data: rootData, MapType: shamap.TypeTransaction,
	}}); err != nil {
		t.Fatalf("store transaction root: %v", err)
	}
	partial, err := shamap.NewFromRootHash(shamap.TypeTransaction, rootHash, family)
	if err != nil {
		t.Fatalf("load backed transaction map: %v", err)
	}
	view, err := ledger.NewOpenWithHeader(header.LedgerHeader{LedgerIndex: 1}, shamap.New(shamap.TypeState), partial, drops.Fees{})
	if err != nil {
		t.Fatalf("create partial ledger view: %v", err)
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

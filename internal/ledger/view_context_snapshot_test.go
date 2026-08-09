package ledger

import (
	"context"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
)

type canceledSnapshotFamily struct {
	storeCalls int
}

func (f *canceledSnapshotFamily) Fetch(context.Context, [32]byte) ([]byte, error) {
	return nil, nil
}

func (f *canceledSnapshotFamily) StoreBatch(ctx context.Context, _ []shamap.FlushEntry) error {
	f.storeCalls++
	return ctx.Err()
}

func TestContextIterationCancelsDirtySnapshotBeforeTraversal(t *testing.T) {
	family := &canceledSnapshotFamily{}
	stateMap, err := shamap.NewBacked(shamap.TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked state: %v", err)
	}
	txMap, err := shamap.NewBacked(shamap.TypeTransaction, family)
	if err != nil {
		t.Fatalf("NewBacked transaction: %v", err)
	}
	l, err := NewOpenWithHeader(header.LedgerHeader{LedgerIndex: 88}, stateMap, txMap, drops.Fees{})
	if err != nil {
		t.Fatalf("NewOpenWithHeader: %v", err)
	}
	if err := l.Insert(mutAcct(0x81), mutData(0x11)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := l.AddTransaction([32]byte{0x82}, []byte("transaction data")); err != nil {
		t.Fatalf("AddTransaction: %v", err)
	}

	tests := map[string]func(context.Context, func() bool) error{
		"state foreach": func(ctx context.Context, traversed func() bool) error {
			return l.ForEachContext(ctx, func([32]byte, []byte) bool { return traversed() })
		},
		"state iterate": func(ctx context.Context, traversed func() bool) error {
			return l.IterateStateFrom(ctx, [32]byte{}, func([32]byte, []byte) bool { return traversed() })
		},
		"transaction foreach": func(ctx context.Context, traversed func() bool) error {
			return l.ForEachTransactionContext(ctx, func([32]byte, []byte) bool { return traversed() })
		},
	}

	for name, iterate := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			traversed := false
			callsBefore := family.storeCalls
			err := iterate(ctx, func() bool {
				traversed = true
				return true
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("iteration error = %v, want context.Canceled", err)
			}
			if traversed {
				t.Fatal("iteration traversed after canceled snapshot persistence")
			}
			if family.storeCalls != callsBefore+1 {
				t.Fatalf("StoreBatch calls = %d, want %d", family.storeCalls, callsBefore+1)
			}
		})
	}
}

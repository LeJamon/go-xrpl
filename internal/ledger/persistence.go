package ledger

import (
	"context"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
)

// StateMapSnapshot returns a mutable snapshot of the state map (e.g. for chaining
// one block's output into the next during continuous replay).
func (l *Ledger) StateMapSnapshot() (*shamap.SHAMap, error) {
	return l.StateMapSnapshotContext(context.Background())
}

// StateMapSnapshotContext propagates cancellation through dirty-node persistence.
func (l *Ledger) StateMapSnapshotContext(ctx context.Context) (*shamap.SHAMap, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.execution.state.mapSnapshot(ctx)
}

// TxMapSnapshot returns a mutable snapshot of the transaction map.
func (l *Ledger) TxMapSnapshot() (*shamap.SHAMap, error) {
	return l.TxMapSnapshotContext(context.Background())
}

// TxMapSnapshotContext propagates cancellation through dirty-node persistence.
func (l *Ledger) TxMapSnapshotContext(ctx context.Context) (*shamap.SHAMap, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.execution.transactions.mapSnapshot(ctx)
}

func (l *Ledger) SerializeHeader() []byte {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return header.AddRaw(l.header, true)
}

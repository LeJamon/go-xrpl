package ledger

import (
	"context"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
)

// StoreDirtyBatched bounds temporary copies while persisting both ledger trees.
func (l *Ledger) StoreDirtyBatched(ctx context.Context, budget shamap.DirtyMemoryBudget, stateStore, txStore func([]shamap.FlushEntry) error) error {
	l.mu.RLock()
	if l.execution != nil {
		defer l.mu.RUnlock()
		if _, err := l.execution.state.hash(ctx); err != nil {
			return err
		}
		_, err := l.execution.transactions.hash(ctx)
		return err
	}
	stateMap, txMap := l.stateMap, l.txMap
	if l.state != StateOpen {
		l.mu.RUnlock()
	} else {
		defer l.mu.RUnlock()
	}
	if stateMap != nil {
		if err := stateMap.StoreDirtyBatched(ctx, budget, stateStore); err != nil {
			return err
		}
	}
	if txMap != nil {
		return txMap.StoreDirtyBatched(ctx, budget, txStore)
	}
	return nil
}

// StateMapSnapshot returns a mutable snapshot of the state map (e.g. for chaining
// one block's output into the next during continuous replay).
func (l *Ledger) StateMapSnapshot() (*shamap.SHAMap, error) {
	return l.StateMapSnapshotContext(context.Background())
}

// StateMapSnapshotContext propagates cancellation through dirty-node persistence.
func (l *Ledger) StateMapSnapshotContext(ctx context.Context) (*shamap.SHAMap, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.execution != nil {
		return l.execution.state.mapSnapshot(ctx)
	}
	return l.stateMap.SnapshotMutableContext(ctx)
}

// TxMapSnapshot returns a mutable snapshot of the transaction map.
func (l *Ledger) TxMapSnapshot() (*shamap.SHAMap, error) {
	return l.TxMapSnapshotContext(context.Background())
}

// TxMapSnapshotContext propagates cancellation through dirty-node persistence.
func (l *Ledger) TxMapSnapshotContext(ctx context.Context) (*shamap.SHAMap, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.execution != nil {
		return l.execution.transactions.mapSnapshot(ctx)
	}
	return l.txMap.SnapshotMutableContext(ctx)
}

// Mutable ledgers may have temporary SHAMap forks in flight, so retain the
// ledger read lock through persistence to keep fork ownership serialized.
// Closed and validated ledgers own immutable maps; their map pointers remain
// valid after the ledger lock is released for storage I/O.
func (l *Ledger) StoreStateDirty(store func([]shamap.FlushEntry) error) error {
	l.mu.RLock()
	if l.execution != nil {
		defer l.mu.RUnlock()
		_, err := l.execution.state.hash(context.Background())
		return err
	}
	stateMap := l.stateMap
	if stateMap == nil {
		l.mu.RUnlock()
		return nil
	}
	if l.state != StateOpen {
		l.mu.RUnlock()
	} else {
		defer l.mu.RUnlock()
	}
	return stateMap.StoreDirty(store)
}

func (l *Ledger) StoreTransactionDirty(store func([]shamap.FlushEntry) error) error {
	l.mu.RLock()
	if l.execution != nil {
		defer l.mu.RUnlock()
		_, err := l.execution.transactions.hash(context.Background())
		return err
	}
	txMap := l.txMap
	if txMap == nil {
		l.mu.RUnlock()
		return nil
	}
	if l.state != StateOpen {
		l.mu.RUnlock()
	} else {
		defer l.mu.RUnlock()
	}
	return txMap.StoreDirty(store)
}

// SetSHAMapFamily backs both ledger maps with the same node family.
func (l *Ledger) SetSHAMapFamily(family shamap.Family) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stateMap != nil {
		l.stateMap.SetFamily(family)
	}
	if l.txMap != nil {
		l.txMap.SetFamily(family)
	}
}

func (l *Ledger) SetStateMapFamily(family shamap.Family) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stateMap != nil {
		l.stateMap.SetFamily(family)
	}
}

func (l *Ledger) SerializeHeader() []byte {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return header.AddRaw(l.header, true)
}

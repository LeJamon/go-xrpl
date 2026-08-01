package ledger

import (
	"context"

	"github.com/LeJamon/go-xrpl/shamap"
)

func (l *Ledger) Snapshot() (*Ledger, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	stateMapCopy, err := l.stateMap.SnapshotImmutable()
	if err != nil {
		return nil, err
	}

	txMapCopy, err := l.txMap.SnapshotImmutable()
	if err != nil {
		return nil, err
	}

	return &Ledger{
		stateMap:       stateMapCopy,
		txMap:          txMapCopy,
		header:         l.header,
		fees:           l.fees,
		state:          l.state,
		writable:       false,
		dropsDestroyed: l.dropsDestroyed,
		rules:          l.rules,
	}, nil
}

// MutableSnapshot returns a mutable deep copy suitable for further apply
// operations (unlike the immutable Snapshot). The clone inherits state from the
// parent; callers applying txs must ensure the parent was open (see OpenLedger.Modify).
func (l *Ledger) MutableSnapshot() (*Ledger, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.state != StateOpen || !l.writable {
		return nil, ErrLedgerImmutable
	}

	stateMapCopy, err := l.stateMap.SnapshotMutable()
	if err != nil {
		return nil, err
	}
	txMapCopy, err := l.txMap.SnapshotMutable()
	if err != nil {
		return nil, err
	}
	return &Ledger{
		stateMap:       stateMapCopy,
		txMap:          txMapCopy,
		header:         l.header,
		fees:           l.fees,
		state:          l.state,
		writable:       true,
		dropsDestroyed: l.dropsDestroyed,
		rules:          l.rules,
	}, nil
}

// MutableSnapshotUnflushed returns a mutable copy without persisting dirty
// SHAMap nodes, for short-lived transactional staging that is then adopted or
// discarded.
func (l *Ledger) MutableSnapshotUnflushed() (*Ledger, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.state != StateOpen || !l.writable {
		return nil, ErrLedgerImmutable
	}

	stateMapCopy, err := l.stateMap.MutableFork()
	if err != nil {
		return nil, err
	}
	txMapCopy, err := l.txMap.MutableFork()
	if err != nil {
		return nil, err
	}
	return &Ledger{
		stateMap:       stateMapCopy,
		txMap:          txMapCopy,
		header:         l.header,
		fees:           l.fees,
		state:          l.state,
		writable:       true,
		dropsDestroyed: l.dropsDestroyed,
		rules:          l.rules,
	}, nil
}

func (l *Ledger) StateMapHash() ([32]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.stateMap.Hash()
}

func (l *Ledger) TxMapHash() ([32]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.txMap.Hash()
}

// ForEach calls fn for each state entry (key, data); return false to stop early.
func (l *Ledger) ForEach(fn func(key [32]byte, data []byte) bool) error {
	return l.ForEachContext(context.Background(), fn)
}

// ForEachContext is the context-aware ForEach; iteration aborts with ctx.Err() even
// between leaf callbacks (the SHAMap descent observes ctx).
func (l *Ledger) ForEachContext(ctx context.Context, fn func(key [32]byte, data []byte) bool) error {
	l.mu.RLock()
	stateMap, err := l.stateMap.SnapshotImmutableContext(ctx)
	l.mu.RUnlock()
	if err != nil {
		return err
	}
	return stateMap.ForEachCtx(ctx, func(item *shamap.Item) bool {
		return fn(item.Key(), item.Data())
	})
}

// Succ returns the first state entry with key > the given key (O(log n) UpperBound).
func (l *Ledger) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	return l.SuccContext(context.Background(), key)
}

func (l *Ledger) SuccContext(ctx context.Context, key [32]byte) ([32]byte, []byte, bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	it := l.stateMap.UpperBoundContext(ctx, key)
	if it.Valid() {
		item := it.Item()
		if item != nil {
			return item.Key(), item.Data(), true, nil
		}
	}
	if err := it.Err(); err != nil {
		return [32]byte{}, nil, false, err
	}
	return [32]byte{}, nil, false, nil
}

// IterateStateFrom walks state entries in ascending key order starting strictly
// after `after` (zero key = from the beginning); fn returns false to stop. Using
// strictly-greater UpperBound means a resume marker pointing at a since-deleted
// entry continues from the next entry instead of rescanning or yielding nothing.
func (l *Ledger) IterateStateFrom(ctx context.Context, after [32]byte, fn func(key [32]byte, data []byte) bool) error {
	l.mu.RLock()
	stateMap, err := l.stateMap.SnapshotImmutableContext(ctx)
	l.mu.RUnlock()
	if err != nil {
		return err
	}
	it := stateMap.UpperBoundContext(ctx, after)
	for it.Valid() {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := it.Item()
		if item == nil {
			break
		}
		if !fn(item.Key(), item.Data()) {
			return nil
		}
		it.Next()
	}
	return it.Err()
}

// DecrementKey returns key-1 as a big-endian 32-byte integer (wrapping at zero).
// Recording DecrementKey(firstUnemittedKey) as a page marker makes the next
// IterateStateFrom resume exactly on that first un-emitted entry.
func DecrementKey(key [32]byte) [32]byte {
	out := key
	for i := 31; i >= 0; i-- {
		if out[i] > 0 {
			out[i]--
			return out
		}
		out[i] = 0xFF
	}
	return out
}

// ForEachTransaction calls fn for each tx (hash, data); return false to stop early.
func (l *Ledger) ForEachTransaction(fn func(txHash [32]byte, txData []byte) bool) error {
	return l.ForEachTransactionContext(context.Background(), fn)
}

func (l *Ledger) ForEachTransactionContext(ctx context.Context, fn func(txHash [32]byte, txData []byte) bool) error {
	l.mu.RLock()
	txMap, err := l.txMap.SnapshotImmutableContext(ctx)
	l.mu.RUnlock()
	if err != nil {
		return err
	}
	return txMap.ForEachCtx(ctx, func(item *shamap.Item) bool {
		return fn(item.Key(), item.Data())
	})
}

func (l *Ledger) TxCount() uint32 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return uint32(l.txMap.Size())
}

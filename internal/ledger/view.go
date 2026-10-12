package ledger

import (
	"context"
)

func (l *Ledger) Snapshot() (*Ledger, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.executionSnapshot(context.Background(), false)
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

	return l.executionSnapshot(context.Background(), true)
}

func (l *Ledger) StateMapHash() ([32]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.execution.state.hash(context.Background())
}

func (l *Ledger) TxMapHash() ([32]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.execution.transactions.hash(context.Background())
}

// ForEach calls fn for each state entry (key, data); return false to stop early.
func (l *Ledger) ForEach(fn func(key [32]byte, data []byte) bool) error {
	return l.ForEachContext(context.Background(), fn)
}

// ForEachContext is the context-aware ForEach; iteration aborts with ctx.Err() even
// between leaf callbacks (the SHAMap descent observes ctx).
func (l *Ledger) ForEachContext(ctx context.Context, fn func(key [32]byte, data []byte) bool) error {
	l.mu.RLock()
	read, release, err := l.execution.state.readSnapshot()
	l.mu.RUnlock()
	if err != nil {
		return err
	}
	defer release()
	return read.Iterate(ctx, nil, fn)
}

// Succ returns the first state entry with key > the given key (O(log n) UpperBound).
func (l *Ledger) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	return l.SuccContext(context.Background(), key)
}

func (l *Ledger) SuccContext(ctx context.Context, key [32]byte) ([32]byte, []byte, bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var next [32]byte
	var value []byte
	found := false
	err := l.execution.state.iterate(ctx, &key, func(k [32]byte, data []byte) bool {
		next, value, found = k, append([]byte(nil), data...), true
		return false
	})
	return next, value, found, err
}

// IterateStateFrom walks state entries in ascending key order starting strictly
// after `after` (zero key = from the beginning); fn returns false to stop. Using
// strictly-greater UpperBound means a resume marker pointing at a since-deleted
// entry continues from the next entry instead of rescanning or yielding nothing.
func (l *Ledger) IterateStateFrom(ctx context.Context, after [32]byte, fn func(key [32]byte, data []byte) bool) error {
	l.mu.RLock()
	read, release, err := l.execution.state.readSnapshot()
	l.mu.RUnlock()
	if err != nil {
		return err
	}
	defer release()
	return read.Iterate(ctx, &after, fn)
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
	read, release, err := l.execution.transactions.readSnapshot()
	l.mu.RUnlock()
	if err != nil {
		return err
	}
	defer release()
	return read.Iterate(ctx, nil, func(k [32]byte, data []byte) bool { return fn(k, data[1:]) })
}

func (l *Ledger) TxCount() uint32 {
	l.mu.RLock()
	tree := l.execution.transactions
	l.mu.RUnlock()
	count, err := tree.countLeaves(context.Background())
	if err != nil {
		return 0
	}
	return count
}

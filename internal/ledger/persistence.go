package ledger

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
)

// StateMapSnapshot returns a mutable snapshot of the state map (e.g. for chaining
// one block's output into the next during continuous replay).
func (l *Ledger) StateMapSnapshot() (*shamap.SHAMap, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.stateMap.SnapshotMutable()
}

// TxMapSnapshot returns a mutable snapshot of the transaction map.
func (l *Ledger) TxMapSnapshot() (*shamap.SHAMap, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.txMap.SnapshotMutable()
}

// Mutable ledgers may have temporary SHAMap forks in flight, so retain the
// ledger read lock through persistence to keep fork ownership serialized.
// Closed and validated ledgers own immutable maps; their map pointers remain
// valid after the ledger lock is released for storage I/O.
func (l *Ledger) StoreStateDirty(store func([]shamap.FlushEntry) error) error {
	l.mu.RLock()
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

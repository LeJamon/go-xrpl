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

func (l *Ledger) StoreStateDirty(store func([]shamap.FlushEntry) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stateMap == nil {
		return nil
	}
	return l.stateMap.StoreDirty(store)
}

func (l *Ledger) StoreTransactionDirty(store func([]shamap.FlushEntry) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.txMap == nil {
		return nil
	}
	return l.txMap.StoreDirty(store)
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

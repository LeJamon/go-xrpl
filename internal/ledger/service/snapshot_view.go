package service

import (
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/shamap"
)

// snapshotView implements tx.LedgerView using a SHAMap snapshot for isolation.
// Changes made via Insert/Update/Erase go to the snapshot only and do not
// affect the original ledger.
type snapshotView struct {
	stateMap *shamap.SHAMap
	ledger   *ledger.Ledger // for TxExists
}

// newSnapshotView creates a new snapshot-based ledger view.
func newSnapshotView(snapshot *shamap.SHAMap, l *ledger.Ledger) *snapshotView {
	return &snapshotView{
		stateMap: snapshot,
		ledger:   l,
	}
}

func (v *snapshotView) Read(k keylet.Keylet) ([]byte, error) {
	item, found, err := v.stateMap.Get(k.Key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return item.Data(), nil
}

func (v *snapshotView) Exists(k keylet.Keylet) (bool, error) {
	return v.stateMap.Has(k.Key)
}

func (v *snapshotView) Insert(k keylet.Keylet, data []byte) error {
	return v.stateMap.Put(k.Key, data)
}

func (v *snapshotView) Update(k keylet.Keylet, data []byte) error {
	return v.stateMap.Put(k.Key, data)
}

func (v *snapshotView) Erase(k keylet.Keylet) error {
	return v.stateMap.Delete(k.Key)
}

func (v *snapshotView) AdjustDropsDestroyed(d drops.XRPAmount) {
	// No-op for simulation — drops destroyed are discarded
}

func (v *snapshotView) ForEach(fn func(key [32]byte, data []byte) bool) error {
	return v.stateMap.ForEach(func(item *shamap.Item) bool {
		return fn(item.Key(), item.Data())
	})
}

func (v *snapshotView) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	it := v.stateMap.UpperBound(key)
	if it.Valid() {
		item := it.Item()
		if item != nil {
			return item.Key(), item.Data(), true, nil
		}
	}
	return [32]byte{}, nil, false, nil
}

func (v *snapshotView) TxExists(txID [32]byte) bool {
	return v.ledger.TxExists(txID)
}

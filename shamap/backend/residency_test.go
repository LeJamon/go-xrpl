package backend

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type residencyPruneStore struct {
	nodestore.Database
	hidden  atomic.Bool
	fetched chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (d *residencyPruneStore) Fetch(ctx context.Context, hash nodestore.Hash256) (*nodestore.Node, error) {
	if d.hidden.Load() {
		return nil, nil
	}
	node, err := d.Database.Fetch(ctx, hash)
	if d.fetched != nil {
		d.once.Do(func() {
			close(d.fetched)
			select {
			case <-d.resume:
			case <-ctx.Done():
			}
		})
	}
	return node, err
}

func TestPruneInvalidatesCleanResidency(t *testing.T) {
	for _, inFlight := range []bool{false, true} {
		t.Run(map[bool]string{false: "resident", true: "in-flight"}[inFlight], func(t *testing.T) {
			base := NewMemory()
			db := &residencyPruneStore{Database: base.db}
			family := New(db)
			t.Cleanup(func() {
				require.NoError(t, family.Close())
				base.SHAMapCleanResidency().Close()
				base.FullBelowCache().Close()
			})
			tree, err := shamap.NewBacked(shamap.TypeState, family)
			require.NoError(t, err)
			key := [32]byte{1}
			require.NoError(t, tree.Put(key, make([]byte, 24)))
			require.NoError(t, tree.StoreDirtyAndRelease(func(entries []shamap.FlushEntry) error {
				return family.StoreBatch(t.Context(), entries)
			}))
			var readDone chan error
			var resume func()
			if inFlight {
				db.fetched = make(chan struct{})
				db.resume = make(chan struct{})
				resume = sync.OnceFunc(func() { close(db.resume) })
				t.Cleanup(resume)
				readDone = make(chan error, 1)
				go func() { _, _, err := tree.GetContext(t.Context(), key); readDone <- err }()
				<-db.fetched
			} else {
				_, found, err := tree.Get(key)
				require.NoError(t, err)
				require.True(t, found)
				require.Positive(t, family.SHAMapCleanResidency().Stats().UsedBytes)
			}
			finish := family.BeginPrune()
			db.hidden.Store(true)
			finish()
			if inFlight {
				resume()
				require.NoError(t, <-readDone)
			}
			require.Zero(t, family.SHAMapCleanResidency().Stats().UsedBytes)
			_, _, err = tree.Get(key)
			var missing *shamap.MissingNodeError
			require.ErrorAs(t, err, &missing, "retired nodes must not be served from the clean cache")
		})
	}
}

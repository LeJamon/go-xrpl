package adaptor

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

func TestAcquisitionStoreScopeRetainsDurableGeneration(t *testing.T) {
	db, err := nodestore.NewKVDatabase(memorydb.New(), nodestore.DatabaseConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	family := backend.New(db)
	lane := newAcquisitionStoreLane(family, nil, 1)
	scope := lane.scope().(*acquisitionStoreScope)
	fingerprint, release, err := scope.AcquireDurableSnapshot(t.Context())
	require.NoError(t, err)
	require.NotNil(t, release)
	t.Cleanup(release)
	want, err := db.DurableFingerprint(t.Context())
	require.NoError(t, err)
	require.Equal(t, want, fingerprint)
	require.NoError(t, scope.Retire(t.Context()))

	started := make(chan struct{})
	deleted := make(chan error, 1)
	go func() {
		close(started)
		_, deleteErr := db.DeleteBeforeWithPrune(context.Background(), 1, 1, family.BeginPrune)
		deleted <- deleteErr
	}()
	<-started
	select {
	case <-deleted:
		t.Fatal("retiring the acquisition released a caller-owned durable pin")
	case <-time.After(25 * time.Millisecond):
	}
	release()
	select {
	case deleteErr := <-deleted:
		require.NoError(t, deleteErr)
	case <-time.After(time.Second):
		t.Fatal("managed deletion remained blocked after releasing the durable pin")
	}
	after, err := db.DurableFingerprint(t.Context())
	require.NoError(t, err)
	require.NotEqual(t, fingerprint, after)
}

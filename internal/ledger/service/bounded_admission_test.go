package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/stretchr/testify/require"
)

type blockingBoundedRepository struct {
	relationaldb.RepositoryManager
	block   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (r *blockingBoundedRepository) PersistLedgerStream(ctx context.Context, info relationaldb.LedgerInfo, id [32]byte, intent string, next relationaldb.IndexedTransactionStream) error {
	if r.block.CompareAndSwap(true, false) {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.RepositoryManager.(relationaldb.StreamingLedgerPublisher).PersistLedgerStream(ctx, info, id, intent, next)
}

func TestBoundedCloseDoesNotPinGenerationDuringSQL(t *testing.T) {
	paths := newBoundedTestPaths(t)
	repository := &blockingBoundedRepository{
		RepositoryManager: newBoundedTestRepository(t, paths),
		entered:           make(chan struct{}),
		release:           make(chan struct{}),
	}
	runtime := newBoundedTestRuntime(t, paths, repository, boundedFlatOptions())
	t.Cleanup(func() { runtime.close(t) })
	unblock := sync.OnceFunc(func() { close(repository.release) })
	defer unblock()
	require.NoError(t, runtime.service.Start())
	repository.block.Store(true)
	closed := make(chan error, 1)
	go func() {
		_, err := runtime.service.AcceptLedger(context.Background())
		closed <- err
	}()
	select {
	case <-repository.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ledger close did not reach SQL publication")
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	stopBeforeMutation := errors.New("mutation gate acquired")
	db := runtime.service.nodeStore.(*nodestore.KVDatabase)
	_, err := db.DeleteBeforeWithRetention(ctx, 1, 1, func(context.Context) (func(), error) {
		return nil, stopBeforeMutation
	}, nil)
	require.ErrorIs(t, err, stopBeforeMutation)
	unblock()
	require.NoError(t, <-closed)
}

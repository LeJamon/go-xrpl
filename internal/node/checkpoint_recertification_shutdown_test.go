package node

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

func TestNodeRuntimeShutdownStopsStateBaseRecertificationBeforeRotator(t *testing.T) {
	const deleteInterval = 4

	root := t.TempDir()
	cfg := &config.Config{
		NetworkID:    config.NetworkID{Set: true, ID: 0},
		NodeSize:     "small",
		DatabasePath: filepath.Join(root, "relational"),
		NodeDB: config.NodeDBConfig{
			Path:            filepath.Join(root, "nodes"),
			OnlineDelete:    deleteInterval,
			FastLoad:        true,
			FastLoadWorkers: 1,
		},
	}
	runtime := newNodeRuntime(
		t.Context(),
		cfg,
		"",
		true,
		service.StartupConfig{Mode: service.StartupFresh},
		xrpllog.Discard(),
		xrpllog.Discard(),
		RunOptions{},
	)

	require.NoError(t, runtime.configureStorage())
	base, ok := runtime.nodeStore.(*nodestore.RotatingKVDatabase)
	require.True(t, ok, "online-delete storage must use a rotating node store")
	store := newShutdownRecertificationStore(base)
	runtime.nodeStore = store
	runtime.nodeFamily = backend.New(store)

	cleanup := true
	t.Cleanup(func() {
		if !cleanup {
			return
		}
		runtime.stopRuntime()
		_ = runtime.shutdownWithin(10 * time.Second)
	})

	require.NoError(t, runtime.configureLedger())
	require.NoError(t, runtime.configureMaintenance())
	var checkpointPrepared atomic.Bool
	runtime.prepareFastLoadCheckpoint = func(ctx context.Context) (bool, error) {
		prepared, err := runtime.ledger.PrepareFastLoadCheckpoint(ctx)
		checkpointPrepared.Store(prepared)
		return prepared, err
	}

	ctx := t.Context()
	validatedSeq, err := runtime.ledger.AcceptLedger(ctx)
	require.NoError(t, err)
	runtime.ledger.FlushPersists()

	runtime.rotator.Notify(validatedSeq)
	require.Eventually(t, func() bool {
		return runtime.services.AdvisoryDeleteState.GetLastRotated() == validatedSeq
	}, 10*time.Second, 10*time.Millisecond)

	store.blockHash = nodestore.Hash256(runtime.ledger.GetValidatedLedger().Header().AccountHash)
	store.armed.Store(true)
	runtime.ledger.InvalidateFastLoadCheckpointEligibility()
	runtime.ledger.RequestStateBaseRecertification()
	select {
	case <-store.readStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("state-base recertification did not reach an uncached read")
	}

	latestSeq := validatedSeq
	for range deleteInterval {
		latestSeq, err = runtime.ledger.AcceptLedger(ctx)
		require.NoError(t, err)
	}
	runtime.ledger.FlushPersists()
	runtime.rotator.Notify(latestSeq)
	select {
	case <-store.rotateEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("production rotator did not enter RotateGenerationWithPrune")
	}

	runtime.stopRuntime()
	require.NoError(t, runtime.shutdownWithin(10*time.Second))
	cleanup = false

	select {
	case <-store.readFinished:
	case <-time.After(time.Second):
		t.Fatal("state-base recertification read remained blocked after shutdown")
	}
	select {
	case <-store.rotateFinished:
	case <-time.After(time.Second):
		t.Fatal("queued online-delete rotation did not drain after verifier cancellation")
	}
	require.True(t, checkpointPrepared.Load(), "shutdown did not prepare a verified checkpoint")
	require.True(t, store.closed.Load(), "runtime shutdown did not close the node store")
}

type shutdownRecertificationStore struct {
	*nodestore.RotatingKVDatabase

	armed     atomic.Bool
	closed    atomic.Bool
	blockHash nodestore.Hash256

	readStarted    chan struct{}
	readFinished   chan struct{}
	rotateEntered  chan struct{}
	rotateFinished chan struct{}

	readStartedOnce    sync.Once
	readFinishedOnce   sync.Once
	rotateEnteredOnce  sync.Once
	rotateFinishedOnce sync.Once
}

func newShutdownRecertificationStore(base *nodestore.RotatingKVDatabase) *shutdownRecertificationStore {
	return &shutdownRecertificationStore{
		RotatingKVDatabase: base,
		readStarted:        make(chan struct{}),
		readFinished:       make(chan struct{}),
		rotateEntered:      make(chan struct{}),
		rotateFinished:     make(chan struct{}),
	}
}

func (s *shutdownRecertificationStore) blockFirstRead(ctx context.Context, hash nodestore.Hash256) error {
	if hash != s.blockHash || !s.armed.CompareAndSwap(true, false) {
		return nil
	}
	s.readStartedOnce.Do(func() { close(s.readStarted) })
	defer s.readFinishedOnce.Do(func() { close(s.readFinished) })
	<-ctx.Done()
	return ctx.Err()
}

func (s *shutdownRecertificationStore) FetchDataUncached(
	ctx context.Context,
	hash nodestore.Hash256,
) ([]byte, error) {
	if err := s.blockFirstRead(ctx, hash); err != nil {
		return nil, err
	}
	return s.RotatingKVDatabase.FetchDataUncached(ctx, hash)
}

func (s *shutdownRecertificationStore) FetchBatchUncached(
	ctx context.Context,
	hashes []nodestore.Hash256,
	maxNodes, maxBytes int,
) ([]*nodestore.Node, error) {
	for _, hash := range hashes {
		if err := s.blockFirstRead(ctx, hash); err != nil {
			return nil, err
		}
	}
	return s.RotatingKVDatabase.FetchBatchUncached(ctx, hashes, maxNodes, maxBytes)
}

func (s *shutdownRecertificationStore) RotateGenerationWithPrune(
	ctx context.Context,
	lastRotated, minimumOnline uint32,
	beginPrune func() func(),
) (bool, error) {
	s.rotateEnteredOnce.Do(func() { close(s.rotateEntered) })
	defer s.rotateFinishedOnce.Do(func() { close(s.rotateFinished) })
	return s.RotatingKVDatabase.RotateGenerationWithPrune(
		ctx,
		lastRotated,
		minimumOnline,
		beginPrune,
	)
}

func (s *shutdownRecertificationStore) Close() error {
	s.closed.Store(true)
	return s.RotatingKVDatabase.Close()
}

package service

import (
	"context"
	"encoding/binary"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	shamapbackend "github.com/LeJamon/go-xrpl/shamap/backend"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type countingGenerationDatabase struct {
	nodestore.Database
	generation            nodestore.GenerationDatabase
	storeBatchNodes       int
	promotionFetches      atomic.Int64
	promotionsInFlight    atomic.Int64
	maxPromotionsInFlight atomic.Int64
	promotionDelay        time.Duration
	promotionStart        chan struct{}
	promotionOnce         sync.Once
	refreshChecks         atomic.Int64
	storeBatchOnce        sync.Once
	storeBatchStart       chan struct{}
	storeBatchResume      chan struct{}
}

func (d *countingGenerationDatabase) StoreBatch(ctx context.Context, nodes []*nodestore.Node) error {
	if d.storeBatchStart != nil {
		d.storeBatchOnce.Do(func() {
			close(d.storeBatchStart)
			select {
			case <-ctx.Done():
			case <-d.storeBatchResume:
			}
		})
	}
	d.storeBatchNodes += len(nodes)
	return d.Database.StoreBatch(ctx, nodes)
}

func (d *countingGenerationDatabase) FetchForPromotion(
	ctx context.Context,
	hash nodestore.Hash256,
) (*nodestore.Node, error) {
	d.promotionFetches.Add(1)
	inFlight := d.promotionsInFlight.Add(1)
	defer d.promotionsInFlight.Add(-1)
	for {
		peak := d.maxPromotionsInFlight.Load()
		if inFlight <= peak || d.maxPromotionsInFlight.CompareAndSwap(peak, inFlight) {
			break
		}
	}
	if d.promotionStart != nil {
		d.promotionOnce.Do(func() { close(d.promotionStart) })
	}
	if d.promotionDelay > 0 {
		timer := time.NewTimer(d.promotionDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return d.generation.FetchForPromotion(ctx, hash)
}

func (d *countingGenerationDatabase) CanRotateWithoutRefresh(ctx context.Context) (bool, error) {
	d.refreshChecks.Add(1)
	return d.generation.CanRotateWithoutRefresh(ctx)
}

func (d *countingGenerationDatabase) RotateGeneration(
	ctx context.Context,
	lastRotated, minimumOnline uint32,
) (bool, error) {
	return d.generation.RotateGeneration(ctx, lastRotated, minimumOnline)
}

func (d *countingGenerationDatabase) GenerationState() (uint32, uint32) {
	return d.generation.GenerationState()
}

func TestService_RefreshValidatedStateSurvivesPruning(t *testing.T) {
	ctx := context.Background()
	db := newTestNodeStore(t, 10_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	family := shamapbackend.New(db)
	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  family,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	rotationTarget, err := svc.AcceptLedger(ctx)
	require.NoError(t, err)
	seq, err := svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()
	latest := svc.GetValidatedLedger()
	require.Equal(t, seq, latest.Sequence())
	root, err := latest.StateMapHash()
	require.NoError(t, err)

	refreshedSeq, err := svc.RefreshValidatedState(ctx, rotationTarget, nil)
	require.NoError(t, err)
	require.Equal(t, seq, refreshedSeq)
	_, err = db.DeleteBefore(ctx, seq, 1024)
	require.NoError(t, err)
	require.NoError(t, svc.verifyStoredSHAMap(ctx, root, shamap.TypeState))
}

func TestService_RefreshValidatedStatePromotesWithoutRestamping(t *testing.T) {
	ctx := context.Background()
	backend, err := kvpebble.NewRotating(
		filepath.Join(t.TempDir(), "nodes"),
		kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200},
	)
	require.NoError(t, err)
	base := newTestRotatingNodeStore(t, backend, 64)
	db := &countingGenerationDatabase{Database: base, generation: base}
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamapbackend.New(db),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	for i := uint32(1); i <= 1024; i++ {
		var key [32]byte
		binary.BigEndian.PutUint32(key[28:], i)
		data := make([]byte, 12)
		binary.BigEndian.PutUint32(data[8:], i)
		require.NoError(t, svc.openLedger.Insert(keylet.Keylet{Key: key}, data))
	}
	seq, err := svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()
	validated := svc.GetValidatedLedger()
	root, err := validated.StateMapHash()
	require.NoError(t, err)
	db.storeBatchNodes = 0

	refreshedSeq, err := svc.RefreshValidatedState(ctx, seq, nil)
	require.NoError(t, err)
	require.Equal(t, seq, refreshedSeq)
	require.Zero(t, db.storeBatchNodes)
	require.Zero(t, db.promotionFetches.Load())

	committed, err := db.RotateGeneration(ctx, seq, 1)
	require.True(t, committed)
	require.NoError(t, err)
	db.promotionFetches.Store(0)
	refreshedSeq, err = svc.RefreshValidatedState(ctx, seq, nil)
	require.NoError(t, err)
	require.Equal(t, seq, refreshedSeq)
	require.Positive(t, db.promotionFetches.Load())
	committed, err = db.RotateGeneration(ctx, seq+1, 1)
	require.True(t, committed)
	require.NoError(t, err)
	require.Zero(t, db.storeBatchNodes)
	require.NoError(t, svc.verifyStoredSHAMap(ctx, root, shamap.TypeState))
}

func TestService_RefreshSnapshotsValidatedLedgerBeforePersistenceBarrier(t *testing.T) {
	ctx := context.Background()
	backend, err := kvpebble.NewRotating(
		filepath.Join(t.TempDir(), "nodes"),
		kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200},
	)
	require.NoError(t, err)
	base := newTestRotatingNodeStore(t, backend, 64)
	storeBatchStart := make(chan struct{})
	storeBatchResume := make(chan struct{})
	var releaseOnce sync.Once
	releasePersist := func() { releaseOnce.Do(func() { close(storeBatchResume) }) }
	t.Cleanup(releasePersist)
	db := &countingGenerationDatabase{
		Database:   base,
		generation: base,
	}
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamapbackend.New(db),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	db.storeBatchStart = storeBatchStart
	db.storeBatchResume = storeBatchResume

	blocker := buildLedgerWithState(t, 299)
	selected := buildLedgerWithState(t, 300)
	newer := buildLedgerWithState(t, 301)
	selectedSeq := selected.Sequence()
	root, err := selected.StateMapHash()
	require.NoError(t, err)

	svc.mu.Lock()
	svc.enqueueValidatedHistoryPersist(blocker)
	svc.mu.Unlock()
	<-storeBatchStart

	svc.mu.Lock()
	svc.validatedLedger = selected
	svc.enqueuePersist(selected)
	svc.mu.Unlock()

	type refreshResult struct {
		seq uint32
		err error
	}
	done := make(chan refreshResult, 1)
	go func() {
		refreshedSeq, err := svc.RefreshValidatedState(ctx, selectedSeq, nil)
		done <- refreshResult{seq: refreshedSeq, err: err}
	}()

	require.Eventually(t, func() bool {
		svc.persistMu.Lock()
		defer svc.persistMu.Unlock()
		for _, job := range svc.persistQueue {
			if job.done != nil {
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond)

	svc.mu.Lock()
	svc.validatedLedger = newer
	svc.enqueuePersist(newer)
	svc.mu.Unlock()

	select {
	case result := <-done:
		t.Fatalf("refresh bypassed selected-ledger persistence: %+v", result)
	default:
	}
	require.Zero(t, db.refreshChecks.Load())

	releasePersist()
	select {
	case result := <-done:
		require.NoError(t, result.err)
		require.Equal(t, selectedSeq, result.seq)
	case <-time.After(time.Second):
		t.Fatal("refresh did not resume after selected-ledger persistence")
	}
	require.Equal(t, int64(1), db.refreshChecks.Load())
	require.Zero(t, db.promotionFetches.Load())
	committed, err := db.RotateGeneration(ctx, selectedSeq, 1)
	require.True(t, committed)
	require.NoError(t, err)
	require.NoError(t, svc.verifyStoredSHAMap(ctx, root, shamap.TypeState))
}

func TestService_RefreshValidatedStateRunsInWalkCheckpoint(t *testing.T) {
	ctx := context.Background()
	db := newTestNodeStore(t, 10_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamapbackend.New(db),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	for i := uint32(1); i <= refreshHealthCheckInterval; i++ {
		var key [32]byte
		binary.BigEndian.PutUint32(key[28:], i)
		data := make([]byte, 12)
		binary.BigEndian.PutUint32(data[8:], i)
		require.NoError(t, svc.openLedger.Insert(keylet.Keylet{Key: key}, data))
	}
	seq, err := svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()

	wantErr := errors.New("checkpoint stopped traversal")
	checks := 0
	refreshedSeq, err := svc.RefreshValidatedState(ctx, seq, func(time.Duration) error {
		checks++
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)
	require.Zero(t, refreshedSeq)
	require.Equal(t, 1, checks)

	type refreshResult struct {
		seq uint32
		err error
	}
	checkpointReached := make(chan struct{}, 1)
	recovered := make(chan struct{})
	done := make(chan refreshResult, 1)
	go func() {
		seq, err := svc.RefreshValidatedState(ctx, seq, func(time.Duration) error {
			select {
			case checkpointReached <- struct{}{}:
			default:
			}
			<-recovered
			return nil
		})
		done <- refreshResult{seq: seq, err: err}
	}()
	<-checkpointReached
	select {
	case result := <-done:
		t.Fatalf("refresh completed while unhealthy: %+v", result)
	default:
	}
	close(recovered)
	select {
	case result := <-done:
		require.NoError(t, result.err)
		require.Equal(t, seq, result.seq)
	case <-time.After(time.Second):
		t.Fatal("refresh did not resume after health recovery")
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	cancelReached := make(chan struct{})
	done = make(chan refreshResult, 1)
	go func() {
		seq, err := svc.RefreshValidatedState(cancelCtx, seq, func(time.Duration) error {
			close(cancelReached)
			<-cancelCtx.Done()
			return cancelCtx.Err()
		})
		done <- refreshResult{seq: seq, err: err}
	}()
	<-cancelReached
	cancel()
	select {
	case result := <-done:
		require.ErrorIs(t, result.err, context.Canceled)
		require.Zero(t, result.seq)
	case <-time.After(time.Second):
		t.Fatal("refresh did not stop promptly after cancellation")
	}
}

func TestService_RefreshValidatedStateChecksHealthByElapsedWork(t *testing.T) {
	ctx := context.Background()
	backend, err := kvpebble.NewRotating(
		filepath.Join(t.TempDir(), "nodes"),
		kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200},
	)
	require.NoError(t, err)
	base := newTestRotatingNodeStore(t, backend, 64)
	db := &countingGenerationDatabase{Database: base, generation: base}
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamapbackend.New(db),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	for i := uint32(1); i <= 64; i++ {
		var key [32]byte
		binary.BigEndian.PutUint32(key[28:], i)
		data := make([]byte, 12)
		binary.BigEndian.PutUint32(data[8:], i)
		require.NoError(t, svc.openLedger.Insert(keylet.Keylet{Key: key}, data))
	}
	seq, err := svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()
	committed, err := db.RotateGeneration(ctx, seq, 1)
	require.True(t, committed)
	require.NoError(t, err)

	db.promotionFetches.Store(0)
	db.promotionDelay = 2 * time.Millisecond
	wantErr := errors.New("health checkpoint reached")
	refreshedSeq, err := svc.RefreshValidatedState(ctx, seq, func(time.Duration) error {
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)
	require.Zero(t, refreshedSeq)
	require.Positive(t, db.promotionFetches.Load())
	require.Less(t, db.promotionFetches.Load(), int64(refreshHealthCheckInterval))
}

func TestService_RefreshValidatedStateUsesBoundedConcurrencyAndReportsProgress(t *testing.T) {
	svc, db, seq := newRotatingRefreshFixture(t, 256)
	db.promotionDelay = 2 * time.Millisecond
	capture, logger := newVerificationLogCapture()
	svc.logger = logger

	refreshedSeq, err := svc.RefreshValidatedState(t.Context(), seq, nil)
	require.NoError(t, err)
	require.Equal(t, seq, refreshedSeq)
	require.Greater(t, db.maxPromotionsInFlight.Load(), int64(1))
	require.LessOrEqual(
		t,
		db.maxPromotionsInFlight.Load(),
		int64(resolveOnlineDeleteRefreshWorkers()),
	)

	records := decodeVerificationLogs(t, capture)
	require.Len(t, records, 2)
	require.Equal(t, "online delete: live-state refresh started", records[0].Message)
	require.EqualValues(t, resolveOnlineDeleteRefreshWorkers(), records[0].Workers)
	require.Equal(t, "online delete: live-state refresh complete", records[1].Message)
	require.EqualValues(t, db.promotionFetches.Load(), records[1].NodesChecked)
	require.Greater(t, records[1].NodeStoreReadsAfter, records[1].NodeStoreReadsBefore)
}

func TestService_RefreshValidatedStateReportsCancellation(t *testing.T) {
	svc, db, seq := newRotatingRefreshFixture(t, 16)
	db.promotionDelay = time.Second
	db.promotionStart = make(chan struct{})
	capture, logger := newVerificationLogCapture()
	svc.logger = logger

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := svc.RefreshValidatedState(ctx, seq, nil)
		done <- err
	}()
	<-db.promotionStart
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("refresh did not stop promptly after cancellation")
	}
	records := decodeVerificationLogs(t, capture)
	require.Len(t, records, 2)
	require.Equal(t, "online delete: live-state refresh started", records[0].Message)
	require.Equal(t, "online delete: live-state refresh canceled", records[1].Message)
	require.Contains(t, records[1].VerificationError, context.Canceled.Error())
}

func newRotatingRefreshFixture(
	t *testing.T,
	entries int,
) (*Service, *countingGenerationDatabase, uint32) {
	t.Helper()
	backend, err := kvpebble.NewRotating(
		filepath.Join(t.TempDir(), "nodes"),
		kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200},
	)
	require.NoError(t, err)
	base := newTestRotatingNodeStore(t, backend, entries*4)
	db := &countingGenerationDatabase{Database: base, generation: base}
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamapbackend.New(db),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	for i := range entries {
		var key [32]byte
		binary.BigEndian.PutUint32(key[28:], uint32(i+1))
		data := make([]byte, 12)
		binary.BigEndian.PutUint32(data[8:], uint32(i+1))
		require.NoError(t, svc.openLedger.Insert(keylet.Keylet{Key: key}, data))
	}
	seq, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	svc.FlushPersists()
	committed, err := db.RotateGeneration(t.Context(), seq, 1)
	require.True(t, committed)
	require.NoError(t, err)
	return svc, db, seq
}

func BenchmarkService_RefreshValidatedState(b *testing.B) {
	const entries = 16_384
	backend, err := kvpebble.NewRotating(
		filepath.Join(b.TempDir(), "nodes"),
		kvpebble.Options{BlockCacheBytes: 64 << 20, MaxOpenFiles: 200},
	)
	require.NoError(b, err)
	base, err := nodestore.NewRotatingKVDatabase(backend, nodestore.DatabaseConfig{})
	require.NoError(b, err)
	db := &countingGenerationDatabase{Database: base, generation: base}
	b.Cleanup(func() { require.NoError(b, db.Close()) })
	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  shamapbackend.New(db),
	})
	require.NoError(b, err)
	require.NoError(b, svc.Start())
	b.Cleanup(svc.Stop)

	for i := range entries {
		var key [32]byte
		binary.BigEndian.PutUint32(key[28:], uint32(i+1))
		data := make([]byte, 12)
		binary.BigEndian.PutUint32(data[8:], uint32(i+1))
		require.NoError(b, svc.openLedger.Insert(keylet.Keylet{Key: key}, data))
	}
	seq, err := svc.AcceptLedger(b.Context())
	require.NoError(b, err)
	svc.FlushPersists()
	committed, err := db.RotateGeneration(b.Context(), seq, 1)
	require.True(b, committed)
	require.NoError(b, err)
	db.promotionFetches.Store(0)

	b.ResetTimer()
	for iteration := range b.N {
		refreshedSeq, refreshErr := svc.RefreshValidatedState(b.Context(), seq, nil)
		require.NoError(b, refreshErr)
		require.Equal(b, seq, refreshedSeq)

		b.StopTimer()
		committed, rotateErr := db.RotateGeneration(b.Context(), seq+uint32(iteration)+1, 1)
		require.True(b, committed)
		require.NoError(b, rotateErr)
		b.StartTimer()
	}
	b.StopTimer()
	b.ReportMetric(
		float64(db.promotionFetches.Load())/b.Elapsed().Seconds(),
		"nodes/s",
	)
	b.ReportMetric(float64(resolveOnlineDeleteRefreshWorkers()), "workers")
}

package service

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/stretchr/testify/require"
)

func TestFastLoadCheckpointEncodingRejectsMalformedData(t *testing.T) {
	checkpoint := testFastLoadCheckpoint()
	valid := encodeFastLoadCheckpoint(checkpoint)
	activeOffset := 8 + 2 + 1
	stateRootOffset := activeOffset + 4 + 32
	strictMetricsOffset := stateRootOffset + 32 + 32 + 32 + 32
	stateCountOffset := strictMetricsOffset + 16
	proofOffset := stateCountOffset + 8

	rechecksum := func(data []byte) []byte {
		return finishFastLoadCheckpointEncoding(data[:len(data)-32])
	}
	tests := map[string][]byte{
		"truncated": valid[:20],
		"oversized": make([]byte, maxFastLoadCheckpointSize()+1),
		"checksum": func() []byte {
			data := append([]byte(nil), valid...)
			data[len(data)-1] ^= 1
			return data
		}(),
		"version": func() []byte {
			data := append([]byte(nil), valid...)
			binary.BigEndian.PutUint16(data[8:10], fastLoadCheckpointVersion+1)
			return rechecksum(data)
		}(),
		"zero sequence": func() []byte {
			data := append([]byte(nil), valid...)
			clear(data[activeOffset : activeOffset+4])
			return rechecksum(data)
		}(),
		"root mismatch": func() []byte {
			data := append([]byte(nil), valid...)
			data[stateRootOffset] ^= 1
			return rechecksum(data)
		}(),
		"proof length": func() []byte {
			data := append([]byte(nil), valid...)
			binary.BigEndian.PutUint32(data[stateCountOffset:stateCountOffset+4], 2)
			return rechecksum(data)
		}(),
		"duplicate proof": func() []byte {
			data := append([]byte(nil), valid...)
			binary.BigEndian.PutUint32(data[stateCountOffset:stateCountOffset+4], 2)
			data = append(data[:proofOffset+32], append([]byte(nil), data[proofOffset:]...)...)
			copy(data[proofOffset+32:proofOffset+64], data[proofOffset:proofOffset+32])
			return rechecksum(data)
		}(),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := decodeFastLoadCheckpoint(data)
			require.Error(t, err)
		})
	}
}

func TestFastLoadCheckpointTombstoneRoundTrip(t *testing.T) {
	checkpoint, tombstone, err := decodeFastLoadCheckpoint(encodeFastLoadCheckpoint(nil))
	require.NoError(t, err)
	require.True(t, tombstone)
	require.Nil(t, checkpoint)
}

func TestService_FastLoadCheckpointCapturesStrictTraversalMetrics(t *testing.T) {
	svc, _, root, expectedNodes, _ := newStoredVerificationFixture(t, shamap.BranchFactor)
	metrics, err := svc.verifyStoredSHAMapMeasured(t.Context(), root, shamap.TypeState)
	require.NoError(t, err)
	require.Equal(t, expectedNodes, metrics.nodes)
	require.Positive(t, metrics.elapsed)
}

func TestService_FastLoadCheckpointCleanRestartAndOneUse(t *testing.T) {
	ctx := context.Background()
	base := newTestNodeStore(t, 100_000)
	t.Cleanup(func() { require.NoError(t, base.Close()) })
	tracked := &checkpointTrackingDatabase{Database: base, uncached: base}
	repositories := newTestRepositories(t, ctx)

	writer := newFastLoadCheckpointService(t, tracked, repositories, true)
	require.NoError(t, writer.Start())
	for i := range 32 {
		var key [32]byte
		key[0] = 0xee
		key[4] = byte(i)
		data := make([]byte, 12)
		data[11] = byte(i + 1)
		require.NoError(t, writer.openLedger.Insert(keylet.Keylet{Key: key}, data))
	}
	rawTx, _ := validRelationalTestTransaction(t, 1)
	txBlob, txHash := makeTxMetaBlobForTest(t, rawTx, 0)
	require.NoError(t, writer.openLedger.AddTransactionWithMeta(txHash, txBlob))
	_, err := writer.AcceptLedger(ctx)
	require.NoError(t, err)
	writer.FlushPersists()
	want := writer.GetValidatedLedger()
	require.NotNil(t, want)
	writer.Stop()
	writer.fastLoadStrictNodes.Store(27_807_179)
	writer.fastLoadStrictElapsed.Store(uint64(28*time.Minute + 44*time.Second))
	prepared, err := writer.PrepareFastLoadCheckpoint(ctx)
	require.NoError(t, err)
	require.True(t, prepared)

	stored, err := tracked.Fetch(ctx, fastLoadCheckpointKey)
	require.NoError(t, err)
	require.NotNil(t, stored)
	checkpoint, tombstone, err := decodeFastLoadCheckpoint(stored.Data)
	require.NoError(t, err)
	require.False(t, tombstone)
	require.NotEmpty(t, checkpoint.stateProofs)
	require.NotEmpty(t, checkpoint.txProofs)
	require.Equal(t, uint64(27_807_179), checkpoint.strictNodes)
	require.Equal(t, uint64(28*time.Minute+44*time.Second), checkpoint.strictElapsed)
	tracked.resetUncachedReads()
	reader := newFastLoadCheckpointService(t, tracked, repositories, false)
	require.NoError(t, reader.Start())
	require.True(t, reader.IsFastLoadProvisional())
	require.Equal(t, want.Hash(), reader.GetValidatedLedger().Hash())
	require.Equal(t, checkpoint.strictNodes, reader.fastLoadStrictNodes.Load())
	require.Equal(t, checkpoint.strictElapsed, reader.fastLoadStrictElapsed.Load())
	require.Equal(t, len(checkpoint.stateProofs)+len(checkpoint.txProofs), tracked.uncachedReads())
	baseRoot, releaseBase, available, err := reader.AcquireFastLoadStateBase(ctx)
	require.NoError(t, err)
	require.True(t, available)
	require.Equal(t, checkpoint.stateRoot, baseRoot)
	releaseBase()
	cache := reader.shamapFamily.(interface {
		FullBelowCache() *shamap.FullBelowCache
	}).FullBelowCache()
	generation := cache.Generation()
	require.True(t, cache.Has(generation, checkpoint.stateRoot))
	require.True(t, cache.Has(generation, checkpoint.txRoot))
	reader.Stop()

	consumed, err := tracked.Fetch(ctx, fastLoadCheckpointKey)
	require.NoError(t, err)
	_, tombstone, err = decodeFastLoadCheckpoint(consumed.Data)
	require.NoError(t, err)
	require.True(t, tombstone)

	tracked.resetUncachedReads()
	secondRestart := newFastLoadCheckpointService(t, tracked, repositories, false)
	require.NoError(t, secondRestart.Start())
	require.Greater(t, tracked.uncachedReads(), len(checkpoint.stateProofs)+len(checkpoint.txProofs))
	baseRoot, releaseBase, available, err = secondRestart.AcquireFastLoadStateBase(ctx)
	require.NoError(t, err)
	require.True(t, available)
	require.Equal(t, checkpoint.stateRoot, baseRoot)
	releaseBase()
	secondRestart.Stop()
}

func TestService_FastLoadCheckpointZeroTransactionRoot(t *testing.T) {
	ctx := context.Background()
	db := newTestNodeStore(t, 100_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	repositories := newTestRepositories(t, ctx)

	writer := newFastLoadCheckpointService(t, db, repositories, true)
	require.NoError(t, writer.Start())
	_, err := writer.AcceptLedger(ctx)
	require.NoError(t, err)
	writer.FlushPersists()
	writer.Stop()
	prepared, err := writer.PrepareFastLoadCheckpoint(ctx)
	require.NoError(t, err)
	require.True(t, prepared)
	stored, err := db.Fetch(ctx, fastLoadCheckpointKey)
	require.NoError(t, err)
	checkpoint, _, err := decodeFastLoadCheckpoint(stored.Data)
	require.NoError(t, err)
	require.Equal(t, [32]byte{}, checkpoint.txRoot)
	require.Empty(t, checkpoint.txProofs)

	reader := newFastLoadCheckpointService(t, db, repositories, false)
	require.NoError(t, reader.Start())
	require.True(t, reader.IsFastLoadProvisional())
	reader.Stop()
}

func TestService_FastLoadCheckpointConsumeSyncFailureAbortsStartup(t *testing.T) {
	ctx := context.Background()
	base := newTestNodeStore(t, 100_000)
	t.Cleanup(func() { require.NoError(t, base.Close()) })
	checkpoint := testFastLoadCheckpoint()
	require.NoError(t, base.Store(ctx, &nodestore.Node{
		Type: nodestore.NodeLedger, Hash: fastLoadCheckpointKey,
		Data: encodeFastLoadCheckpoint(checkpoint), LedgerSeq: checkpoint.sequence,
	}))
	db := &checkpointTrackingDatabase{Database: base, syncErr: errors.New("injected sync failure")}
	svc := newFastLoadCheckpointService(t, db, newTestRepositories(t, ctx), false)
	err := svc.Start()
	require.ErrorContains(t, err, "consume fast-load checkpoint")
}

func TestService_FastLoadCheckpointPreservedWhenStartupCannotUseIt(t *testing.T) {
	tests := []struct {
		name     string
		fastLoad bool
		mode     StartupMode
	}{
		{name: "fast load disabled", mode: StartupNormal},
		{name: "network startup", fastLoad: true, mode: StartupNetwork},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db := newTestNodeStore(t, 100_000)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			checkpoint := testFastLoadCheckpoint()
			require.NoError(t, db.Store(ctx, &nodestore.Node{
				Type: nodestore.NodeLedger, Hash: fastLoadCheckpointKey,
				Data: encodeFastLoadCheckpoint(checkpoint), LedgerSeq: checkpoint.sequence,
			}))
			svc, err := New(Config{
				Standalone: true, Startup: StartupConfig{Mode: test.mode},
				GenesisConfig: genesis.DefaultConfig(), NodeStore: db,
				SHAMapFamily: backend.New(db), RelationalDB: newTestRepositories(t, ctx),
				FastLoad: test.fastLoad,
			})
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			svc.Stop()

			stored, err := db.Fetch(ctx, fastLoadCheckpointKey)
			require.NoError(t, err)
			require.NotNil(t, stored)
			got, tombstone, err := decodeFastLoadCheckpoint(stored.Data)
			require.NoError(t, err)
			require.False(t, tombstone)
			require.Equal(t, checkpoint, got)
		})
	}
}

func TestService_FastLoadCheckpointConsumeWaitsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base := newTestNodeStore(t, 100_000)
	t.Cleanup(func() { require.NoError(t, base.Close()) })
	checkpoint := testFastLoadCheckpoint()
	require.NoError(t, base.Store(context.Background(), &nodestore.Node{
		Type: nodestore.NodeLedger, Hash: fastLoadCheckpointKey,
		Data: encodeFastLoadCheckpoint(checkpoint), LedgerSeq: checkpoint.sequence,
	}))
	db := &checkpointTrackingDatabase{
		Database: base, uncached: base, blockOnSyncCall: 1,
		syncStarted: make(chan struct{}, 1), syncRelease: make(chan struct{}, 1),
	}
	svc := newFastLoadCheckpointService(t, db, newTestRepositories(t, context.Background()), false)
	type result struct {
		checkpoint *fastLoadCheckpoint
		err        error
	}
	done := make(chan result, 1)
	go func() {
		got, err := svc.consumeFastLoadCheckpoint(ctx)
		done <- result{checkpoint: got, err: err}
	}()
	<-db.syncStarted
	cancel()
	select {
	case got := <-done:
		t.Fatalf("consume returned before tombstone flush completed: %v", got.err)
	case <-time.After(25 * time.Millisecond):
	}
	db.syncRelease <- struct{}{}
	got := <-done
	require.NoError(t, got.err)
	require.NotNil(t, got.checkpoint)
	stored, err := base.Fetch(context.Background(), fastLoadCheckpointKey)
	require.NoError(t, err)
	_, tombstone, err := decodeFastLoadCheckpoint(stored.Data)
	require.NoError(t, err)
	require.True(t, tombstone)
	second, err := svc.consumeFastLoadCheckpoint(context.Background())
	require.NoError(t, err)
	require.Nil(t, second)
}

func TestService_FastLoadCheckpointMismatchFallsBackToStrictTraversal(t *testing.T) {
	ctx := context.Background()
	tests := map[string]func(*fastLoadCheckpoint){
		"root": func(checkpoint *fastLoadCheckpoint) {
			checkpoint.stateRoot = [32]byte{0xfa}
			checkpoint.stateProofs[0] = checkpoint.stateRoot
		},
		"proof": func(checkpoint *fastLoadCheckpoint) {
			checkpoint.stateProofs = append(checkpoint.stateProofs, [32]byte{0xfb})
		},
		"NodeStore fingerprint": func(checkpoint *fastLoadCheckpoint) {
			checkpoint.nodeStoreFingerprint[0] ^= 1
		},
		"relational schema fingerprint": func(checkpoint *fastLoadCheckpoint) {
			checkpoint.schemaFingerprint[0] ^= 1
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			db := newTestNodeStore(t, 100_000)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			repositories := newTestRepositories(t, ctx)
			writer := durableCheckpointServiceWithRepositories(t, ctx, db, repositories)
			prepared, prepareErr := writer.PrepareFastLoadCheckpoint(ctx)
			require.NoError(t, prepareErr)
			require.True(t, prepared)
			stored, err := db.Fetch(ctx, fastLoadCheckpointKey)
			require.NoError(t, err)
			checkpoint, _, err := decodeFastLoadCheckpoint(stored.Data)
			require.NoError(t, err)
			mutate(checkpoint)
			require.NoError(t, db.Store(ctx, &nodestore.Node{
				Type: nodestore.NodeLedger, Hash: fastLoadCheckpointKey,
				Data: encodeFastLoadCheckpoint(checkpoint), LedgerSeq: checkpoint.sequence,
			}))
			require.NoError(t, db.Sync(ctx))

			reader := newFastLoadCheckpointService(t, db, repositories, false)
			require.NoError(t, reader.Start())
			require.True(t, reader.IsFastLoadProvisional())
			require.Equal(t, checkpoint.sequence, reader.GetValidatedLedgerIndex())
			reader.Stop()
		})
	}
}

func TestService_FastLoadCheckpointManagedMutationFallsBackToStrictTraversal(t *testing.T) {
	ctx := context.Background()
	base := newTestNodeStore(t, 100_000)
	t.Cleanup(func() { require.NoError(t, base.Close()) })
	tracked := &checkpointTrackingDatabase{Database: base, uncached: base}
	repositories := newTestRepositories(t, ctx)
	writer := newFastLoadCheckpointService(t, tracked, repositories, true)
	require.NoError(t, writer.Start())
	for i := range 32 {
		var key [32]byte
		key[0] = 0xee
		key[4] = byte(i)
		data := make([]byte, 12)
		data[11] = byte(i + 1)
		require.NoError(t, writer.openLedger.Insert(keylet.Keylet{Key: key}, data))
	}
	_, err := writer.AcceptLedger(ctx)
	require.NoError(t, err)
	writer.FlushPersists()
	writer.Stop()
	prepared, err := writer.PrepareFastLoadCheckpoint(ctx)
	require.NoError(t, err)
	require.True(t, prepared)
	stored, err := base.Fetch(ctx, fastLoadCheckpointKey)
	require.NoError(t, err)
	checkpoint, _, err := decodeFastLoadCheckpoint(stored.Data)
	require.NoError(t, err)

	deleted, err := base.DeleteBefore(ctx, 1, 1)
	require.NoError(t, err)
	require.Zero(t, deleted)
	tracked.resetUncachedReads()
	reader := newFastLoadCheckpointService(t, tracked, repositories, false)
	require.NoError(t, reader.Start())
	require.Greater(t, tracked.uncachedReads(), len(checkpoint.stateProofs)+len(checkpoint.txProofs))
	reader.Stop()
}

func TestService_FastLoadStrictTraversalDoesNotRequireReusableSnapshot(t *testing.T) {
	tests := map[string]func(*nodestore.KVDatabase) nodestore.Database{
		"snapshot setup failure": func(base *nodestore.KVDatabase) nodestore.Database {
			return &checkpointTrackingDatabase{
				Database: base, uncached: base, snapshotErr: errors.New("injected snapshot failure"),
			}
		},
		"durable database without retained snapshots": func(base *nodestore.KVDatabase) nodestore.Database {
			return &durableOnlyDatabase{Database: base, DurableDatabase: base}
		},
	}
	for name, wrap := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			base := newTestNodeStore(t, 100_000)
			t.Cleanup(func() { require.NoError(t, base.Close()) })
			repositories := newTestRepositories(t, ctx)

			writer := newFastLoadCheckpointService(t, base, repositories, true)
			require.NoError(t, writer.Start())
			_, err := writer.AcceptLedger(ctx)
			require.NoError(t, err)
			writer.FlushPersists()
			want := writer.GetValidatedLedger()
			require.NotNil(t, want)
			writer.Stop()

			reader := newFastLoadCheckpointService(t, wrap(base), repositories, false)
			require.NoError(t, reader.Start())
			require.True(t, reader.IsFastLoadProvisional())
			require.Equal(t, want.Hash(), reader.GetValidatedLedger().Hash())
			require.Positive(t, reader.fastLoadStrictNodes.Load())
			_, release, available, err := reader.AcquireFastLoadStateBase(ctx)
			require.NoError(t, err)
			require.False(t, available)
			require.Nil(t, release)
			reader.Stop()
		})
	}
}

func TestService_FastLoadBaseRejectsMutationBeforePivot(t *testing.T) {
	ctx := context.Background()
	db := newTestNodeStore(t, 100_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	fingerprint, err := db.DurableFingerprint(ctx)
	require.NoError(t, err)
	svc := newFastLoadCheckpointService(t, db, newTestRepositories(t, ctx), false)
	svc.mu.Lock()
	svc.networkLedgerState = networkLedgerFastLoadProvisional
	svc.fastLoadBaseStateRoot = [32]byte{0xaa}
	svc.fastLoadBaseFingerprint = fingerprint
	svc.fastLoadBaseVerified = true
	svc.mu.Unlock()

	_, err = db.DeleteBefore(ctx, 1, 1)
	require.NoError(t, err)
	_, release, available, err := svc.AcquireFastLoadStateBase(ctx)
	require.ErrorContains(t, err, "mutation generation changed")
	require.False(t, available)
	require.Nil(t, release)
}

func TestService_FastLoadCheckpointPreparationOrderingAndFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("store then sync", func(t *testing.T) {
		base := newTestNodeStore(t, 100_000)
		t.Cleanup(func() { require.NoError(t, base.Close()) })
		db := &checkpointTrackingDatabase{Database: base, uncached: base}
		svc := durableCheckpointService(t, ctx, db)
		db.resetOperations()
		prepared, err := svc.PrepareFastLoadCheckpoint(ctx)
		require.NoError(t, err)
		require.True(t, prepared)
		require.Equal(t, []string{"sync", "checkpoint-store", "sync"}, db.operations())
	})

	t.Run("store failure", func(t *testing.T) {
		base := newTestNodeStore(t, 100_000)
		t.Cleanup(func() { require.NoError(t, base.Close()) })
		db := &checkpointTrackingDatabase{Database: base, uncached: base}
		svc := durableCheckpointService(t, ctx, db)
		db.checkpointStoreErr = errors.New("injected store failure")
		_, err := svc.PrepareFastLoadCheckpoint(ctx)
		require.ErrorContains(t, err, "durably store fast-load checkpoint")
	})

	t.Run("cancellation after store waits for durable publication", func(t *testing.T) {
		base := newTestNodeStore(t, 100_000)
		t.Cleanup(func() { require.NoError(t, base.Close()) })
		db := &checkpointTrackingDatabase{Database: base, uncached: base}
		svc := durableCheckpointService(t, ctx, db)
		db.resetOperations()
		prepareCtx, cancelPrepare := context.WithCancel(ctx)
		db.cancelOnActiveStore = cancelPrepare
		prepared, err := svc.PrepareFastLoadCheckpoint(prepareCtx)
		require.NoError(t, err)
		require.True(t, prepared)
		stored, fetchErr := db.Fetch(ctx, fastLoadCheckpointKey)
		require.NoError(t, fetchErr)
		checkpoint, tombstone, decodeErr := decodeFastLoadCheckpoint(stored.Data)
		require.NoError(t, decodeErr)
		require.False(t, tombstone)
		require.NotNil(t, checkpoint)
	})

	t.Run("blocking flush does not return on cancellation", func(t *testing.T) {
		base := newTestNodeStore(t, 100_000)
		t.Cleanup(func() { require.NoError(t, base.Close()) })
		db := &checkpointTrackingDatabase{Database: base, uncached: base}
		svc := durableCheckpointService(t, ctx, db)
		db.resetOperations()
		db.mu.Lock()
		db.blockOnSyncCall = 2
		db.syncStarted = make(chan struct{}, 1)
		db.syncRelease = make(chan struct{}, 1)
		db.mu.Unlock()
		prepareCtx, cancelPrepare := context.WithCancel(ctx)
		type result struct {
			prepared bool
			err      error
		}
		done := make(chan result, 1)
		go func() {
			prepared, err := svc.PrepareFastLoadCheckpoint(prepareCtx)
			done <- result{prepared: prepared, err: err}
		}()
		<-db.syncStarted
		cancelPrepare()
		select {
		case got := <-done:
			t.Fatalf("prepare returned before active flush completed: %v", got.err)
		case <-time.After(25 * time.Millisecond):
		}
		db.syncRelease <- struct{}{}
		got := <-done
		require.NoError(t, got.err)
		require.True(t, got.prepared)
	})
}

func TestService_FastLoadCheckpointDeadlinePreservesTombstone(t *testing.T) {
	ctx := context.Background()
	base := newTestNodeStore(t, 100_000)
	t.Cleanup(func() { require.NoError(t, base.Close()) })
	tracked := &checkpointTrackingDatabase{Database: base, uncached: base}
	svc := durableCheckpointService(t, ctx, tracked)
	require.NoError(t, tracked.StoreDurable(ctx, &nodestore.Node{
		Type: nodestore.NodeLedger, Hash: fastLoadCheckpointKey,
		Data: encodeFastLoadCheckpoint(nil),
	}))

	blocked := &deadlineCheckpointDatabase{
		checkpointTrackingDatabase: tracked,
		started:                    make(chan struct{}, 1),
	}
	svc.nodeStore = blocked
	prepareCtx, cancelPrepare := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancelPrepare()
	prepared, err := svc.PrepareFastLoadCheckpoint(prepareCtx)
	require.False(t, prepared)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-blocked.started:
	default:
		t.Fatal("checkpoint proof read did not observe the deadline")
	}
	stored, err := tracked.Fetch(ctx, fastLoadCheckpointKey)
	require.NoError(t, err)
	_, tombstone, err := decodeFastLoadCheckpoint(stored.Data)
	require.NoError(t, err)
	require.True(t, tombstone)
}

func TestService_FastLoadCheckpointRefusesInvalidTipAndPrune(t *testing.T) {
	ctx := context.Background()
	t.Run("tip mismatch", func(t *testing.T) {
		base := newTestNodeStore(t, 100_000)
		t.Cleanup(func() { require.NoError(t, base.Close()) })
		db := &checkpointTrackingDatabase{Database: base, uncached: base}
		svc := durableCheckpointService(t, ctx, db)
		require.NoError(t, base.Store(ctx, &nodestore.Node{
			Type: nodestore.NodeLedger, Hash: validatedTipKey,
			Data: make([]byte, 32), LedgerSeq: svc.GetValidatedLedgerIndex(),
		}))
		_, err := svc.PrepareFastLoadCheckpoint(ctx)
		require.ErrorContains(t, err, "durable validated tip")
	})

	t.Run("prune invalidation", func(t *testing.T) {
		base := newTestNodeStore(t, 100_000)
		t.Cleanup(func() { require.NoError(t, base.Close()) })
		db := &checkpointTrackingDatabase{Database: base, uncached: base}
		svc := durableCheckpointService(t, ctx, db)
		svc.InvalidateFastLoadCheckpointEligibility()
		svc.markFastLoadCheckpointEligible()
		prepared, err := svc.PrepareFastLoadCheckpoint(ctx)
		require.NoError(t, err)
		require.False(t, prepared)
		stored, err := db.Fetch(ctx, fastLoadCheckpointKey)
		require.NoError(t, err)
		require.Nil(t, stored)
	})
}

func testFastLoadCheckpoint() *fastLoadCheckpoint {
	return &fastLoadCheckpoint{
		sequence: 10, ledgerHash: [32]byte{1}, stateRoot: [32]byte{2},
		txRoot: [32]byte{3}, stateProofs: [][32]byte{{2}}, txProofs: [][32]byte{{3}},
		nodeStoreFingerprint: [32]byte{4}, schemaFingerprint: [32]byte{5},
		strictNodes: 27_807_179, strictElapsed: uint64(28*time.Minute + 44*time.Second),
	}
}

func newFastLoadCheckpointService(
	t *testing.T,
	db nodestore.Database,
	repositories relationaldb.RepositoryManager,
	standalone bool,
) *Service {
	t.Helper()
	svc, err := New(Config{
		Standalone: standalone, GenesisConfig: genesis.DefaultConfig(),
		NodeStore: db, SHAMapFamily: backend.New(db), RelationalDB: repositories, FastLoad: true,
	})
	require.NoError(t, err)
	return svc
}

type uncachedNodeReader interface {
	FetchDataUncached(context.Context, nodestore.Hash256) ([]byte, error)
}

type checkpointTrackingDatabase struct {
	nodestore.Database
	uncached            uncachedNodeReader
	mu                  sync.Mutex
	reads               int
	ops                 []string
	syncErr             error
	checkpointStoreErr  error
	cancelOnActiveStore context.CancelFunc
	syncCalls           int
	blockOnSyncCall     int
	syncStarted         chan struct{}
	syncRelease         chan struct{}
	snapshotErr         error
}

type deadlineCheckpointDatabase struct {
	*checkpointTrackingDatabase
	started chan struct{}
}

func (d *deadlineCheckpointDatabase) FetchDataUncached(ctx context.Context, _ nodestore.Hash256) ([]byte, error) {
	select {
	case d.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, context.Cause(ctx)
}

func (d *checkpointTrackingDatabase) FetchDataUncached(ctx context.Context, hash nodestore.Hash256) ([]byte, error) {
	d.mu.Lock()
	d.reads++
	d.mu.Unlock()
	return d.uncached.FetchDataUncached(ctx, hash)
}

func (d *checkpointTrackingDatabase) Store(ctx context.Context, node *nodestore.Node) error {
	if node.Hash == fastLoadCheckpointKey {
		d.mu.Lock()
		d.ops = append(d.ops, "checkpoint-store")
		err := d.checkpointStoreErr
		cancel := d.cancelOnActiveStore
		d.mu.Unlock()
		if err != nil {
			return err
		}
		storeErr := d.Database.Store(ctx, node)
		if storeErr == nil && node.LedgerSeq != 0 && cancel != nil {
			cancel()
		}
		return storeErr
	}
	return d.Database.Store(ctx, node)
}

func (d *checkpointTrackingDatabase) Sync(ctx context.Context) error {
	d.mu.Lock()
	d.ops = append(d.ops, "sync")
	d.syncCalls++
	call := d.syncCalls
	fail := d.syncErr
	block := call == d.blockOnSyncCall
	started := d.syncStarted
	release := d.syncRelease
	d.mu.Unlock()
	if fail != nil {
		return fail
	}
	if block {
		started <- struct{}{}
		<-release
	}
	return d.Database.Sync(ctx)
}

func (d *checkpointTrackingDatabase) StoreDurable(ctx context.Context, node *nodestore.Node) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.Store(ctx, node); err != nil {
		return err
	}
	return d.Sync(context.Background())
}

func (d *checkpointTrackingDatabase) DurableFingerprint(ctx context.Context) ([32]byte, error) {
	durable, ok := d.Database.(nodestore.DurableDatabase)
	if !ok {
		return [32]byte{}, errors.New("missing durable database capability")
	}
	return durable.DurableFingerprint(ctx)
}

func (d *checkpointTrackingDatabase) WithDurableSnapshot(
	ctx context.Context,
	fn func([32]byte) error,
) error {
	durable, ok := d.Database.(nodestore.DurableDatabase)
	if !ok {
		return errors.New("missing durable database capability")
	}
	return durable.WithDurableSnapshot(ctx, fn)
}

func (d *checkpointTrackingDatabase) AcquireDurableSnapshot(ctx context.Context) ([32]byte, func(), error) {
	d.mu.Lock()
	err := d.snapshotErr
	d.mu.Unlock()
	if err != nil {
		return [32]byte{}, nil, err
	}
	durable, ok := d.Database.(nodestore.DurableSnapshotDatabase)
	if !ok {
		return [32]byte{}, nil, errors.New("missing retained durable snapshot capability")
	}
	return durable.AcquireDurableSnapshot(ctx)
}

type durableOnlyDatabase struct {
	nodestore.Database
	nodestore.DurableDatabase
}

func (d *checkpointTrackingDatabase) uncachedReads() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reads
}

func (d *checkpointTrackingDatabase) resetUncachedReads() {
	d.mu.Lock()
	d.reads = 0
	d.mu.Unlock()
}

func (d *checkpointTrackingDatabase) resetOperations() {
	d.mu.Lock()
	d.ops = nil
	d.syncCalls = 0
	d.mu.Unlock()
}

func (d *checkpointTrackingDatabase) operations() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.ops...)
}

func durableCheckpointService(t *testing.T, ctx context.Context, db nodestore.Database) *Service {
	t.Helper()
	repositories := newTestRepositories(t, ctx)
	return durableCheckpointServiceWithRepositories(t, ctx, db, repositories)
}

func durableCheckpointServiceWithRepositories(
	t *testing.T,
	ctx context.Context,
	db nodestore.Database,
	repositories relationaldb.RepositoryManager,
) *Service {
	t.Helper()
	svc, err := New(Config{
		Standalone: true, GenesisConfig: genesis.DefaultConfig(),
		NodeStore: db, SHAMapFamily: backend.New(db), RelationalDB: repositories, FastLoad: true,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	_, err = svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()
	svc.Stop()
	return svc
}

func TestService_FastLoadCheckpointRequiresConsensusDrain(t *testing.T) {
	ctx := t.Context()
	base := newTestNodeStore(t, 100_000)
	t.Cleanup(func() { require.NoError(t, base.Close()) })
	svc := newFastLoadCheckpointService(t, base, newTestRepositories(t, ctx), true)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	_, err := svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()
	svc.lifecycleMu.Lock()
	svc.consensusWG.Add(1)
	svc.lifecycleMu.Unlock()
	var once sync.Once
	finish := func() { once.Do(svc.consensusWG.Done) }
	defer finish()
	stopped := make(chan struct{})
	go func() { svc.Stop(); close(stopped) }()
	require.Eventually(t, func() bool {
		svc.lifecycleMu.Lock()
		defer svc.lifecycleMu.Unlock()
		return svc.lifecycleState == serviceStopping
	}, time.Second, time.Millisecond)
	prepared, err := svc.PrepareFastLoadCheckpoint(ctx)
	require.False(t, prepared)
	require.ErrorContains(t, err, "before ledger service stopped")
	select {
	case <-stopped:
		t.Fatal("Stop returned before consensus work drained")
	default:
	}
	finish()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not finish after consensus drain")
	}
	prepared, err = svc.PrepareFastLoadCheckpoint(ctx)
	require.NoError(t, err)
	require.True(t, prepared)
}

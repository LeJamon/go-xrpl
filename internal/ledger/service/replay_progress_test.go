package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type proofRefreshBlockingFamily struct {
	*replayAdmissionFamily
	entered chan struct{}
	once    sync.Once
}

func (f *proofRefreshBlockingFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.once.Do(func() { close(f.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestColdReplayProofRefreshRequiresExactLedgerAndGeneration(t *testing.T) {
	for _, kind := range []string{"matching", "wrong ledger", "wrong generation", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			base := newReplayAdmissionFamily(t)
			family := &proofRefreshBlockingFamily{replayAdmissionFamily: base, entered: make(chan struct{})}
			svc, err := New(Config{SHAMapFamily: family})
			require.NoError(t, err)
			state := persistedReplayAdmissionState(t, base, 32)
			parent := replayAdmissionLedger(t, 500, [32]byte{50}, [32]byte{49}, state, shamap.New(shamap.TypeTransaction))
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			type result struct {
				proof *PreparedReplayParent
				err   error
			}
			ready := make(chan result, 1)
			go func() { p, err := svc.PrepareReplayParent(ctx, parent); ready <- result{p, err} }()
			select {
			case <-family.entered:
			case <-ctx.Done():
				t.Fatal("cold read did not start")
			}
			h := parent.Header()
			fp := base.fingerprint
			switch kind {
			case "wrong ledger":
				h.Hash[0]++
			case "wrong generation":
				fp[0]++
			}
			if kind == "canceled" {
				cancel()
			} else {
				svc.rememberReplayProof(h, fp, false)
			}
			if kind == "wrong ledger" || kind == "wrong generation" {
				select {
				case got := <-ready:
					if got.proof != nil {
						got.proof.Release()
					}
					t.Fatalf("unrelated proof admitted the parent: %v", got.err)
				case <-time.After(550 * time.Millisecond):
				}
				cancel()
			}
			select {
			case got := <-ready:
				if kind == "matching" {
					require.NoError(t, got.err)
					require.NotNil(t, got.proof)
					require.True(t, got.proof.Stats().ProofCacheHit)
					got.proof.Release()
				} else {
					require.ErrorIs(t, got.err, context.Canceled)
				}
			case <-time.After(time.Second):
				t.Fatal("cold verification did not release")
			}
		})
	}
}

func TestColdVerificationCannotConsumePersistenceProgressReserve(t *testing.T) {
	const verificationBytes = uint64(2 * ReplayVerificationWorkspaceBytes)
	pool := membudget.New(verificationBytes + shamap.DirtyWorkspaceBytes)
	persist, err := pool.NewAccount("persistence", shamap.DirtyWorkspaceBytes, shamap.DirtyWorkspaceBytes)
	require.NoError(t, err)
	verify, err := pool.NewAccount("verification", verificationBytes, verificationBytes)
	require.NoError(t, err)
	svc := &Service{config: Config{PersistenceMemory: persist, ReplayVerificationMemory: verify, FastLoadWorkers: 2}}
	workers, _, release, err := svc.acquireReplayColdWorkspace(t.Context())
	require.NoError(t, err)
	defer release()
	require.Equal(t, 2, workers)
	// Execute actual persistence while both cold-walk reservations remain held.
	state := buildStateForPersistenceProgress(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	stored := 0
	require.NoError(t, state.StoreDirtyBatched(ctx, persist, func(entries []shamap.FlushEntry) error {
		stored += len(entries)
		return nil
	}))
	require.Positive(t, stored)
	release()
	require.Zero(t, pool.Snapshot().Used)
}

func buildStateForPersistenceProgress(t *testing.T) *shamap.SHAMap {
	t.Helper()
	state := shamap.New(shamap.TypeState)
	require.NoError(t, state.PutWithNodeType([32]byte{1}, make([]byte, 32), shamap.NodeTypeAccountState))
	return state
}

func TestColdReplayVerificationUsesBoundedUncachedBatches(t *testing.T) {
	svc, db, root, _ := newParallelStoredVerificationFixture(t)
	tracked := &batchVerificationDatabase{Database: db}
	svc.nodeStore = tracked
	svc.shamapFamily = backend.New(tracked)
	var expectedNodes uint64
	require.NoError(t, svc.walkStoredSHAMap(t.Context(), root, shamap.TypeState, func([32]byte, *nodestore.Node) error {
		expectedNodes++
		return nil
	}))
	before := db.Stats()
	stats, supported, err := svc.verifyStoredReplaySnapshot(t.Context(), "test parent", root, shamap.TypeState)
	require.NoError(t, err)
	require.True(t, supported)
	require.Equal(t, expectedNodes, stats.NodesLoaded)
	require.Equal(t, expectedNodes, stats.DurableReads)
	calls, _, requests := tracked.counts()
	require.Positive(t, calls)
	for _, hashes := range requests {
		require.LessOrEqual(t, len(hashes), storedSHAMapVerificationBatchNodes)
	}
	after := db.Stats()
	require.Equal(t, before.CacheSize, after.CacheSize)
	require.Equal(t, before.CacheHits, after.CacheHits)
	require.Equal(t, before.CacheMisses, after.CacheMisses)
}

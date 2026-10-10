package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

type replayAdmissionFamily struct {
	*backend.NodeStore

	mu          sync.Mutex
	fingerprint [32]byte
	hidden      map[[32]byte]bool
	readHashes  map[[32]byte]int
	reads       int
}

type delayedReplayAdmissionFamily struct {
	*replayAdmissionFamily

	delay  time.Duration
	mu     sync.Mutex
	active int
	peak   int
}

type replaySnapshotOnlyFamily struct {
	*backend.NodeStore
	fingerprint [32]byte
}

func newReplaySnapshotOnlyFamily(t *testing.T) *replaySnapshotOnlyFamily {
	t.Helper()
	db := newTestNodeStore(t, 0)
	family := &replaySnapshotOnlyFamily{
		NodeStore:   backend.New(db),
		fingerprint: [32]byte{7},
	}
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return family
}

func (f *replaySnapshotOnlyFamily) AcquireDurableSnapshot(context.Context) ([32]byte, func(), error) {
	return f.fingerprint, func() {}, nil
}

type blockingReplaySnapshotFamily struct {
	*replayAdmissionFamily
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *blockingReplaySnapshotFamily) AcquireDurableSnapshot(ctx context.Context) ([32]byte, func(), error) {
	f.once.Do(func() { close(f.entered) })
	select {
	case <-f.release:
		f.mu.Lock()
		fingerprint := f.fingerprint
		f.mu.Unlock()
		return fingerprint, func() {}, nil
	case <-ctx.Done():
		return [32]byte{}, nil, ctx.Err()
	}
}

func newReplayAdmissionFamily(t *testing.T) *replayAdmissionFamily {
	t.Helper()
	db := newTestNodeStore(t, 0)
	family := &replayAdmissionFamily{
		NodeStore:   backend.New(db),
		fingerprint: [32]byte{1},
		hidden:      make(map[[32]byte]bool),
		readHashes:  make(map[[32]byte]int),
	}
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return family
}

func (f *replayAdmissionFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.mu.Lock()
	f.reads++
	f.readHashes[hash]++
	hidden := f.hidden[hash]
	f.mu.Unlock()
	if hidden {
		return nil, nil
	}
	return f.NodeStore.FetchDurable(ctx, hash)
}

func (f *replayAdmissionFamily) AcquireDurableSnapshot(context.Context) ([32]byte, func(), error) {
	f.mu.Lock()
	fingerprint := f.fingerprint
	f.mu.Unlock()
	return fingerprint, func() {}, nil
}

func (f *replayAdmissionFamily) DurableFingerprint(context.Context) ([32]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fingerprint, nil
}

func (f *replayAdmissionFamily) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func (f *replayAdmissionFamily) resetReads() {
	f.mu.Lock()
	f.reads = 0
	f.mu.Unlock()
}

func (f *replayAdmissionFamily) hide(hash [32]byte) {
	f.mu.Lock()
	f.hidden[hash] = true
	f.fingerprint[0]++
	f.mu.Unlock()
}

func (f *replayAdmissionFamily) hideWithoutGeneration(hash [32]byte) {
	f.mu.Lock()
	f.hidden[hash] = true
	f.mu.Unlock()
}

func (f *replayAdmissionFamily) bumpFingerprint() {
	f.mu.Lock()
	f.fingerprint[0]++
	f.mu.Unlock()
}

func (f *delayedReplayAdmissionFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.mu.Lock()
	f.active++
	if f.active > f.peak {
		f.peak = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	timer := time.NewTimer(f.delay)
	select {
	case <-ctx.Done():
		if !timer.Stop() {
			<-timer.C
		}
		return nil, ctx.Err()
	case <-timer.C:
	}
	return f.replayAdmissionFamily.FetchDurable(ctx, hash)
}

func (f *delayedReplayAdmissionFamily) peakReaders() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peak
}

func persistedReplayAdmissionState(t *testing.T, family *replayAdmissionFamily, count int) *shamap.SHAMap {
	t.Helper()
	source := shamap.New(shamap.TypeState)
	for i := range count {
		key := [32]byte{byte(i), byte(i >> 8), byte(i >> 16), byte(i >> 24), 1}
		require.NoError(t, source.PutWithNodeType(key, []byte("replay-admission-value"), shamap.NodeTypeAccountState))
	}
	require.NoError(t, source.StoreDirty(func(entries []shamap.FlushEntry) error {
		return family.StoreBatch(t.Context(), entries)
	}))
	root, err := source.Hash()
	require.NoError(t, err)
	loaded, err := shamap.NewFromRootHashContext(t.Context(), shamap.TypeState, root, family)
	require.NoError(t, err)
	return loaded
}

func replayAdmissionLedger(t *testing.T, sequence uint32, hash, parentHash [32]byte, state, tx *shamap.SHAMap) *ledger.Ledger {
	t.Helper()
	stateRoot, err := state.Hash()
	require.NoError(t, err)
	txRoot, err := tx.Hash()
	require.NoError(t, err)
	result, err := ledger.NewFromHeader(header.LedgerHeader{
		LedgerIndex: sequence,
		Hash:        hash,
		ParentHash:  parentHash,
		AccountHash: stateRoot,
		TxHash:      txRoot,
	}, state, tx, drops.Fees{})
	require.NoError(t, err)
	return result
}

func replayAdmissionFirstChildHash(t *testing.T, family *replayAdmissionFamily, root [32]byte) [32]byte {
	t.Helper()
	data, err := family.FetchDurable(t.Context(), root)
	require.NoError(t, err)
	node, err := shamap.DeserializeFromPrefix(data)
	require.NoError(t, err)
	inner, ok := node.(shamap.InnerNodeReader)
	require.True(t, ok)
	for branch := range shamap.BranchFactor {
		if inner.IsEmptyBranch(branch) {
			continue
		}
		child, err := inner.ChildHash(branch)
		require.NoError(t, err)
		return child
	}
	t.Fatal("persisted replay state root has no child")
	return [32]byte{}
}

func replayAdmissionPathDescendantHash(
	t *testing.T,
	family *replayAdmissionFamily,
	root, key [32]byte,
	depth int,
) [32]byte {
	t.Helper()
	current := root
	for level := 0; level < depth; level++ {
		data, err := family.FetchDurable(t.Context(), current)
		require.NoError(t, err)
		node, err := shamap.DeserializeFromPrefix(data)
		require.NoError(t, err)
		inner, ok := node.(shamap.InnerNodeReader)
		require.True(t, ok)
		branch := int(key[level/2] >> 4)
		if level%2 == 1 {
			branch = int(key[level/2] & 0x0f)
		}
		child, err := inner.ChildHash(branch)
		require.NoError(t, err)
		require.NotZero(t, child)
		current = child
	}
	return current
}

func TestReplayAdmissionUsesGenerationBoundParentProofAfterCacheEviction(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)

	parentState := persistedReplayAdmissionState(t, family, 4096)
	parentHash := [32]byte{0x10}
	parent := replayAdmissionLedger(t, 100, parentHash, [32]byte{0x01}, parentState, shamap.New(shamap.TypeTransaction))
	parentProof, err := svc.PrepareReplayParent(t.Context(), parent)
	require.NoError(t, err)
	require.NotNil(t, parentProof)
	parentStats := parentProof.Stats()
	require.Greater(t, parentStats.State.NodesLoaded, uint64(4000))
	require.Equal(t, parentStats.State.NodesLoaded, parentStats.State.DurableReads)
	require.True(t, parentStats.ProofCacheChecked)
	require.True(t, parentStats.ProofCacheMiss)

	unchangedState, err := parent.StateMapSnapshotContext(t.Context())
	require.NoError(t, err)
	unchanged := replayAdmissionLedger(t, 101, [32]byte{0x11}, parentHash, unchangedState, shamap.New(shamap.TypeTransaction))
	family.resetReads()
	unchangedPrepared, err := svc.PrepareReplayLedger(t.Context(), unchanged, parentProof)
	require.NoError(t, err)
	unchangedStats := unchangedPrepared.Stats()
	require.True(t, unchangedStats.ParentProofUsed)
	require.True(t, unchangedStats.State.UsedVerifiedBase)
	require.Less(t, unchangedStats.State.NodesLoaded, parentStats.State.NodesLoaded/100)
	unchangedReads := family.readCount()
	t.Logf("replay admission unchanged: parent nodes=%d durable reads=%d, child nodes=%d durable reads=%d, family reads=%d", parentStats.State.NodesLoaded, parentStats.State.DurableReads, unchangedStats.State.NodesLoaded, unchangedStats.State.DurableReads, unchangedReads)
	require.LessOrEqual(t, unchangedReads, 1)
	unchangedPrepared.Release()

	childState, err := parent.StateMapSnapshotContext(t.Context())
	require.NoError(t, err)
	changedKey := [32]byte{0xf0, 1}
	require.NoError(t, childState.PutWithNodeType(changedKey, []byte("changed-replay-value"), shamap.NodeTypeAccountState))
	child := replayAdmissionLedger(t, 102, [32]byte{0x12}, parentHash, childState, shamap.New(shamap.TypeTransaction))

	family.resetReads()
	prepared, err := svc.PrepareReplayLedger(t.Context(), child, parentProof)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	childStats := prepared.Stats()
	require.True(t, childStats.ParentProofUsed)
	require.True(t, childStats.State.UsedVerifiedBase)
	require.Positive(t, childStats.State.EqualSubtreesSkipped)
	require.Less(t, childStats.State.NodesLoaded, parentStats.State.NodesLoaded/4)
	changedReads := family.readCount()
	t.Logf("replay admission changed: parent nodes=%d durable reads=%d, child nodes=%d durable reads=%d, family reads=%d", parentStats.State.NodesLoaded, parentStats.State.DurableReads, childStats.State.NodesLoaded, childStats.State.DurableReads, changedReads)
	require.Positive(t, changedReads, "the changed path must be checked against durable storage")
	require.Less(t, changedReads, 128, "changed-path verification must not refetch the full tree")
	prepared.Release()
	family.FullBelowCache().Bump()
	refreshedProof, err := svc.PrepareReplayParent(t.Context(), parent)
	require.NoError(t, err)
	require.True(t, refreshedProof.Stats().ProofCacheHit)
	refreshedProof.Release()
	parentProof.Release()

	// The service certificate is independent of FullBelow cache residency.
	require.NoError(t, svc.VerifyDetachedLedger(t.Context(), child))

	// A generation change invalidates the certificate, so a reclaimed node is
	// observed by the cold fallback even if a warm alias remains attached.
	childRoot, err := child.StateMapSnapshotContext(t.Context())
	require.NoError(t, err)
	root, err := childRoot.Hash()
	require.NoError(t, err)
	family.hide(root)
	require.ErrorIs(t, svc.VerifyDetachedLedger(t.Context(), child), shamap.ErrNodeNotInStore)
}

func TestReplayAdmissionRejectsReclaimedWarmDescendantAfterGenerationChange(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	parentState := persistedReplayAdmissionState(t, family, 1024)
	parentHash := [32]byte{0x60}
	parent := replayAdmissionLedger(t, 600, parentHash, [32]byte{0x06}, parentState, shamap.New(shamap.TypeTransaction))
	parentProof, err := svc.PrepareReplayParent(t.Context(), parent)
	require.NoError(t, err)
	defer parentProof.Release()
	root, err := parentState.Hash()
	require.NoError(t, err)
	family.hide(replayAdmissionFirstChildHash(t, family, root))
	unchangedState, err := parent.StateMapSnapshotContext(t.Context())
	require.NoError(t, err)
	unchanged := replayAdmissionLedger(t, 601, [32]byte{0x61}, parentHash, unchangedState, shamap.New(shamap.TypeTransaction))
	_, err = svc.PrepareReplayLedger(t.Context(), unchanged, parentProof)
	require.ErrorIs(t, err, shamap.ErrNodeNotInStore)
}

func TestReplayAdmissionRejectsMissingChangedRootBeforePreparation(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	parentState := persistedReplayAdmissionState(t, family, 1024)
	parentHash := [32]byte{0x70}
	parent := replayAdmissionLedger(t, 700, parentHash, [32]byte{0x07}, parentState, shamap.New(shamap.TypeTransaction))
	parentProof, err := svc.PrepareReplayParent(t.Context(), parent)
	require.NoError(t, err)
	defer parentProof.Release()
	changedState, err := parent.StateMapSnapshotContext(t.Context())
	require.NoError(t, err)
	require.NoError(t, changedState.PutWithNodeType([32]byte{0xf7, 1}, []byte("missing-changed-root"), shamap.NodeTypeAccountState))
	changedRoot, err := changedState.Hash()
	require.NoError(t, err)
	family.hide(changedRoot)
	changed := replayAdmissionLedger(t, 701, [32]byte{0x71}, parentHash, changedState, shamap.New(shamap.TypeTransaction))
	_, err = svc.PrepareReplayLedger(t.Context(), changed, parentProof)
	require.ErrorIs(t, err, shamap.ErrNodeNotInStore)
}

func TestReplayAdmissionRejectsMissingChangedDescendantWithWarmAlias(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	parentState := persistedReplayAdmissionState(t, family, 1024)
	parentHash := [32]byte{0x78}
	parent := replayAdmissionLedger(t, 780, parentHash, [32]byte{0x07}, parentState, shamap.New(shamap.TypeTransaction))
	parentProof, err := svc.PrepareReplayParent(t.Context(), parent)
	require.NoError(t, err)
	defer parentProof.Release()

	changedKey := [32]byte{0xf0, 1}
	changedState, err := parent.StateMapSnapshotContext(t.Context())
	require.NoError(t, err)
	require.NoError(t, changedState.PutWithNodeType(changedKey, []byte("missing-changed-descendant"), shamap.NodeTypeAccountState))
	changed := replayAdmissionLedger(t, 781, [32]byte{0x79}, parentHash, changedState, shamap.New(shamap.TypeTransaction))
	root, err := parentState.Hash()
	require.NoError(t, err)
	changedRoot, err := changedState.Hash()
	require.NoError(t, err)
	missingDescendant := replayAdmissionPathDescendantHash(t, family, changedRoot, changedKey, 1)
	family.hideWithoutGeneration(missingDescendant)
	prepared, err := svc.PrepareReplayLedger(t.Context(), changed, parentProof)
	family.mu.Lock()
	requestedHidden := family.readHashes[missingDescendant]
	family.mu.Unlock()
	t.Logf("missing changed descendant root=%x parent=%x requested=%d", changedRoot[:8], root[:8], requestedHidden)
	require.Positive(t, requestedHidden)
	require.ErrorIs(t, err, shamap.ErrNodeNotInStore)
	require.Nil(t, prepared)
	svc.replayProofMu.Lock()
	require.Zero(t, svc.replayPreparedSlotCount)
	require.Empty(t, svc.replayPreparedProofs)
	svc.replayProofMu.Unlock()
}

func TestReplayAdmissionPreparedLedgerIsOneShotAndGenerationBound(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)

	parentState := persistedReplayAdmissionState(t, family, 8)
	parentHash := [32]byte{0x20}
	parent := replayAdmissionLedger(t, 200, parentHash, [32]byte{0x02}, parentState, shamap.New(shamap.TypeTransaction))
	parentProof, err := svc.PrepareReplayParent(t.Context(), parent)
	require.NoError(t, err)
	defer parentProof.Release()

	childState, err := parent.StateMapSnapshotContext(t.Context())
	require.NoError(t, err)
	require.NoError(t, childState.PutWithNodeType([32]byte{0xf1}, []byte("child-replay-value"), shamap.NodeTypeAccountState))
	child := replayAdmissionLedger(t, 201, [32]byte{0x21}, parentHash, childState, shamap.New(shamap.TypeTransaction))
	family.bumpFingerprint()
	prepared, err := svc.PrepareReplayLedger(t.Context(), child, parentProof)
	require.NoError(t, err)
	require.True(t, prepared.Stats().GenerationChanged)
	otherSvc, err := New(DefaultConfig())
	require.NoError(t, err)
	_, err = otherSvc.BootstrapPreparedLedger(t.Context(), prepared)
	require.ErrorIs(t, err, errReplayAdmissionService)

	family.mu.Lock()
	family.fingerprint[0]++
	family.mu.Unlock()
	_, err = svc.BootstrapPreparedLedger(t.Context(), prepared)
	require.Error(t, err)
	require.True(t, prepared.Stats().GenerationChanged)
	prepared.Release()
	svc.replayProofMu.Lock()
	require.Zero(t, svc.replayPreparedSlotCount)
	require.Empty(t, svc.replayPreparedProofs)
	svc.replayProofMu.Unlock()

	// The failed one-shot token cannot be consumed again, and Release remains
	// idempotent after the failed publication.
	_, err = svc.BootstrapPreparedLedger(t.Context(), prepared)
	require.ErrorIs(t, err, errReplayAdmissionState)
	prepared.Release()
}

func TestReplayAdmissionFallsBackWithoutDurableSnapshotSupport(t *testing.T) {
	family := newVerifierFamily()
	service := newVerifierService(t, family)
	parentState := newPersistedVerifierMap(t, family, shamap.TypeState, 16)
	parent := replayAdmissionLedger(t, 250, [32]byte{0x25}, [32]byte{0x05}, parentState, shamap.New(shamap.TypeTransaction))

	parentProof, err := service.PrepareReplayParent(t.Context(), parent)
	require.NoError(t, err)
	defer parentProof.Release()
	childState, err := parent.StateMapSnapshotContext(t.Context())
	require.NoError(t, err)
	require.NoError(t, childState.PutWithNodeType([32]byte{0xf2}, []byte("cold-fallback-value"), shamap.NodeTypeAccountState))
	child := replayAdmissionLedger(t, 251, [32]byte{0x26}, [32]byte{0x25}, childState, shamap.New(shamap.TypeTransaction))
	prepared, err := service.PrepareReplayLedger(t.Context(), child, parentProof)
	require.NoError(t, err)
	prepared.Release()
}

func TestReplayAdmissionPreparationPropagatesCancellation(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	parentState := persistedReplayAdmissionState(t, family, 8)
	parent := replayAdmissionLedger(t, 300, [32]byte{0x30}, [32]byte{0x03}, parentState, shamap.New(shamap.TypeTransaction))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = svc.PrepareReplayParent(ctx, parent)
	require.ErrorIs(t, err, context.Canceled)
}

func TestReplayAdmissionWorkspaceUsesSharedBudget(t *testing.T) {
	budget := &relayTestMemory{limit: replayAdmissionColdWorkspaceBytes * 4}
	svc := &Service{config: Config{PersistenceMemory: budget, FastLoadWorkers: 4}}
	workers, workspace, release, err := svc.acquireReplayColdWorkspace(t.Context())
	require.NoError(t, err)
	require.Equal(t, 3, workers, "one workspace remains reserved for transactions")
	require.Equal(t, uint64(replayAdmissionColdWorkspaceBytes*3), workspace)
	require.Equal(t, workspace, budget.used)
	require.NotNil(t, release)
	release()
	release()
	require.Zero(t, budget.used)
}

func TestReplayAdmissionColdWalkUsesAdmittedWorkersAndReleasesWorkspace(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	budget := &relayTestMemory{limit: replayAdmissionColdWorkspaceBytes * 3}
	svc, err := New(Config{
		SHAMapFamily:      family,
		PersistenceMemory: budget,
		FastLoadWorkers:   2,
	})
	require.NoError(t, err)
	state := persistedReplayAdmissionState(t, family, 512)
	parent := replayAdmissionLedger(t, 450, [32]byte{0x45}, [32]byte{0x04}, state, shamap.New(shamap.TypeTransaction))
	proof, err := svc.PrepareReplayParent(t.Context(), parent)
	require.NoError(t, err)
	stats := proof.Stats()
	require.Equal(t, 2, stats.State.Workers)
	require.Equal(t, uint64(replayAdmissionColdWorkspaceBytes*2), stats.State.WorkspaceBytes)
	require.Zero(t, budget.used)
	proof.Release()
}

func TestReplayAdmissionColdWalkReaderCapAndWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		workers int
	}{
		{name: "one worker", workers: 1},
		{name: "two workers", workers: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := newReplayAdmissionFamily(t)
			family := &delayedReplayAdmissionFamily{
				replayAdmissionFamily: base,
				delay:                 500 * time.Microsecond,
			}
			budget := &relayTestMemory{limit: replayAdmissionColdWorkspaceBytes * uint64(tc.workers+1)}
			svc, err := New(Config{
				SHAMapFamily:      family,
				PersistenceMemory: budget,
				FastLoadWorkers:   2,
			})
			require.NoError(t, err)
			state := persistedReplayAdmissionState(t, base, 1024)
			parent := replayAdmissionLedger(t, 460+uint32(tc.workers), [32]byte{byte(0x46 + tc.workers)}, [32]byte{0x04}, state, shamap.New(shamap.TypeTransaction))
			proof, err := svc.PrepareReplayParent(t.Context(), parent)
			require.NoError(t, err)
			stats := proof.Stats()
			peak := family.peakReaders()
			t.Logf("cold walk workers=%d workspace=%d peak durable readers=%d budget used=%d", stats.State.Workers, stats.State.WorkspaceBytes, peak, budget.used)
			require.Equal(t, tc.workers, stats.State.Workers)
			require.Equal(t, uint64(tc.workers)*replayAdmissionColdWorkspaceBytes, stats.State.WorkspaceBytes)
			require.GreaterOrEqual(t, peak, 1)
			if tc.workers > 1 {
				require.GreaterOrEqual(t, peak, 2)
			}
			require.LessOrEqual(t, peak, tc.workers)
			require.Zero(t, budget.used)
			proof.Release()
		})
	}
}

func TestReplayAdmissionWorkspaceRejectsInsufficientCapacityAndHonorsCancellation(t *testing.T) {
	tooSmall := &relayTestMemory{limit: replayAdmissionColdWorkspaceBytes - 1}
	svc := &Service{config: Config{PersistenceMemory: tooSmall}}
	_, _, _, err := svc.acquireReplayColdWorkspace(t.Context())
	require.ErrorIs(t, err, ErrReplayAdmissionWorkspace)

	budget := &relayTestMemory{limit: replayAdmissionColdWorkspaceBytes}
	require.True(t, budget.TryReserve(replayAdmissionColdWorkspaceBytes))
	defer budget.Release(replayAdmissionColdWorkspaceBytes)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, _, _, err = (&Service{config: Config{PersistenceMemory: budget}}).acquireReplayColdWorkspace(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestReplayAdmissionFingerprintStaysBoundToSnapshotProvider(t *testing.T) {
	family := newReplaySnapshotOnlyFamily(t)
	pin := newReplaySnapshotPin(family, family.fingerprint, func() {})
	fingerprint, available, err := currentReplayFingerprint(t.Context(), pin)
	require.NoError(t, err)
	require.False(t, available)
	require.Zero(t, fingerprint)
	pin.Release()

	noRelease := &replayNilReleaseFamily{replaySnapshotOnlyFamily: family}
	svc := &Service{shamapFamily: noRelease}
	got, pinned, err := svc.acquireReplaySnapshot(t.Context())
	require.NoError(t, err)
	require.False(t, pinned)
	require.Nil(t, got)
}

type replayNilReleaseFamily struct {
	*replaySnapshotOnlyFamily
}

func (f *replayNilReleaseFamily) AcquireDurableSnapshot(context.Context) ([32]byte, func(), error) {
	return [32]byte{9}, nil, nil
}

func TestReplayAdmissionPreparationKeepsStopBehindColdWalk(t *testing.T) {
	base := newReplayAdmissionFamily(t)
	family := &blockingReplaySnapshotFamily{
		replayAdmissionFamily: base,
		entered:               make(chan struct{}),
		release:               make(chan struct{}),
	}
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	parentState := persistedReplayAdmissionState(t, family.replayAdmissionFamily, 8)
	parent := replayAdmissionLedger(t, 400, [32]byte{0x40}, [32]byte{0x04}, parentState, shamap.New(shamap.TypeTransaction))

	prepared := make(chan *PreparedReplayParent, 1)
	prepErr := make(chan error, 1)
	go func() {
		proof, prepareErr := svc.PrepareReplayParent(context.Background(), parent)
		prepared <- proof
		prepErr <- prepareErr
	}()
	<-family.entered
	stopped := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while replay preparation was still admitted")
	case <-time.After(20 * time.Millisecond):
	}
	close(family.release)
	proof := <-prepared
	require.NoError(t, <-prepErr)
	proof.Release()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not drain replay preparation")
	}
}

func TestReplayAdmissionBootstrapSuccessConsumesAndClearsPreparedLedger(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	parentState := persistedReplayAdmissionState(t, family, 16)
	parentHash := [32]byte{0x50}
	parent := replayAdmissionLedger(t, 500, parentHash, [32]byte{0x05}, parentState, shamap.New(shamap.TypeTransaction))
	svc.genesisLedger = parent
	proof, err := svc.PrepareReplayParent(t.Context(), parent)
	require.NoError(t, err)
	defer proof.Release()
	childState, err := parent.StateMapSnapshotContext(t.Context())
	require.NoError(t, err)
	require.NoError(t, childState.PutWithNodeType([32]byte{0xf5}, []byte("publication-value"), shamap.NodeTypeAccountState))
	txMap := shamap.New(shamap.TypeTransaction)
	stateRoot, err := childState.Hash()
	require.NoError(t, err)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)
	childHeader := header.LedgerHeader{
		LedgerIndex: 501,
		ParentHash:  parentHash,
		AccountHash: stateRoot,
		TxHash:      txRoot,
	}
	childHeader.Hash = header.CalculateHash(childHeader)
	child, err := ledger.NewFromHeader(childHeader, childState, txMap, drops.Fees{})
	require.NoError(t, err)
	prepared, err := svc.PrepareReplayLedger(t.Context(), child, proof)
	require.NoError(t, err)
	stats := prepared.Stats()
	_, err = svc.BootstrapPreparedLedger(t.Context(), prepared)
	require.NoError(t, err)
	require.False(t, stats.GenerationChanged)
	prepared.mu.Lock()
	require.Nil(t, prepared.stateMap)
	require.Nil(t, prepared.txMap)
	prepared.mu.Unlock()
	_, err = svc.BootstrapPreparedLedger(t.Context(), prepared)
	require.ErrorIs(t, err, errReplayAdmissionState)
	prepared.Release()
}

func TestReplayAdmissionPublishedProofSurvivesLRUEvictionUntilRelease(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	parentState := persistedReplayAdmissionState(t, family, 64)
	parentHash := [32]byte{0x90}
	parent := replayAdmissionLedger(t, 900, parentHash, [32]byte{0x09}, parentState, shamap.New(shamap.TypeTransaction))
	svc.genesisLedger = parent
	parentProof, err := svc.PrepareReplayParent(t.Context(), parent)
	require.NoError(t, err)
	defer parentProof.Release()

	childState, err := parent.StateMapSnapshotContext(t.Context())
	require.NoError(t, err)
	require.NoError(t, childState.PutWithNodeType([32]byte{0xf9}, []byte("activation-proof"), shamap.NodeTypeAccountState))
	txMap := shamap.New(shamap.TypeTransaction)
	stateRoot, err := childState.Hash()
	require.NoError(t, err)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)
	constructedHeader := header.LedgerHeader{LedgerIndex: 901, ParentHash: parentHash, AccountHash: stateRoot, TxHash: txRoot}
	constructedHeader.Hash = header.CalculateHash(constructedHeader)
	child := replayAdmissionLedger(t, constructedHeader.LedgerIndex, constructedHeader.Hash, parentHash, childState, txMap)
	prepared, err := svc.PrepareReplayLedger(t.Context(), child, parentProof)
	require.NoError(t, err)
	_, err = svc.BootstrapPreparedLedger(t.Context(), prepared)
	require.NoError(t, err)

	// Fill the ordinary four-entry LRU with unrelated certificates, then force
	// a FullBelow generation so the active prepared proof is the only shortcut.
	family.mu.Lock()
	fingerprint := family.fingerprint
	family.mu.Unlock()
	childHeader := child.Header()
	for i := 0; i <= replayProofCacheLimit; i++ {
		fake := childHeader
		fake.LedgerIndex += uint32(i + 1)
		fake.Hash = [32]byte{byte(0xa0 + i)}
		svc.rememberReplayProof(fake, fingerprint, true)
	}
	family.FullBelowCache().Bump()
	family.resetReads()
	require.NoError(t, svc.VerifyDetachedLedger(t.Context(), child))
	require.Zero(t, family.readCount(), "published active proof should avoid durable reads after LRU eviction")

	prepared.Release()
	family.FullBelowCache().Bump()
	family.resetReads()
	require.NoError(t, svc.VerifyDetachedLedger(t.Context(), child))
	require.Positive(t, family.readCount(), "releasing the prepared token must restore cold verification")
}

func TestReplayAdmissionPreparedProofRejectsGenerationChange(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	parentState := persistedReplayAdmissionState(t, family, 64)
	parentHash := [32]byte{0x92}
	parent := replayAdmissionLedger(t, 920, parentHash, [32]byte{0x29}, parentState, shamap.New(shamap.TypeTransaction))
	svc.genesisLedger = parent
	parentProof, err := svc.PrepareReplayParent(t.Context(), parent)
	require.NoError(t, err)
	defer parentProof.Release()
	childState, err := parent.StateMapSnapshotContext(t.Context())
	require.NoError(t, err)
	require.NoError(t, childState.PutWithNodeType([32]byte{0xfa}, []byte("generation-bound"), shamap.NodeTypeAccountState))
	txMap := shamap.New(shamap.TypeTransaction)
	stateRoot, err := childState.Hash()
	require.NoError(t, err)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)
	childHeader := header.LedgerHeader{LedgerIndex: 921, ParentHash: parentHash, AccountHash: stateRoot, TxHash: txRoot}
	childHeader.Hash = header.CalculateHash(childHeader)
	child := replayAdmissionLedger(t, childHeader.LedgerIndex, childHeader.Hash, parentHash, childState, txMap)
	prepared, err := svc.PrepareReplayLedger(t.Context(), child, parentProof)
	require.NoError(t, err)
	_, err = svc.BootstrapPreparedLedger(t.Context(), prepared)
	require.NoError(t, err)

	family.mu.Lock()
	fingerprint := family.fingerprint
	family.mu.Unlock()
	for i := 0; i <= replayProofCacheLimit; i++ {
		fake := child.Header()
		fake.LedgerIndex += uint32(i + 1)
		fake.Hash = [32]byte{byte(0xb0 + i)}
		svc.rememberReplayProof(fake, fingerprint, true)
	}
	family.bumpFingerprint()
	family.FullBelowCache().Bump()
	family.resetReads()
	require.NoError(t, svc.VerifyDetachedLedger(t.Context(), child))
	require.Positive(t, family.readCount(), "a changed durable generation must bypass the retained proof")
	prepared.Release()
}

func TestReplayAdmissionStopWaitsForPublishedProofRelease(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	parentState := persistedReplayAdmissionState(t, family, 16)
	parentHash := [32]byte{0x94}
	parent := replayAdmissionLedger(t, 940, parentHash, [32]byte{0x49}, parentState, shamap.New(shamap.TypeTransaction))
	svc.genesisLedger = parent
	parentProof, err := svc.PrepareReplayParent(t.Context(), parent)
	require.NoError(t, err)
	childState, err := parent.StateMapSnapshotContext(t.Context())
	require.NoError(t, err)
	require.NoError(t, childState.PutWithNodeType([32]byte{0xfb}, []byte("stop-lifetime"), shamap.NodeTypeAccountState))
	txMap := shamap.New(shamap.TypeTransaction)
	stateRoot, err := childState.Hash()
	require.NoError(t, err)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)
	childHeader := header.LedgerHeader{LedgerIndex: 941, ParentHash: parentHash, AccountHash: stateRoot, TxHash: txRoot}
	childHeader.Hash = header.CalculateHash(childHeader)
	child := replayAdmissionLedger(t, childHeader.LedgerIndex, childHeader.Hash, parentHash, childState, txMap)
	prepared, err := svc.PrepareReplayLedger(t.Context(), child, parentProof)
	require.NoError(t, err)
	_, err = svc.BootstrapPreparedLedger(t.Context(), prepared)
	require.NoError(t, err)
	parentProof.Release()

	stopped := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while the published proof was still owned")
	case <-time.After(20 * time.Millisecond):
	}
	prepared.Release()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not drain the published proof lifetime")
	}
	svc.replayProofMu.Lock()
	require.Zero(t, svc.replayPreparedSlotCount)
	require.Empty(t, svc.replayPreparedProofs)
	svc.replayProofMu.Unlock()
}

func TestReplayAdmissionPreparedProofSlotsAreBoundedAndReleased(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	releases := make([]func(), 0, replayPreparedProofSlotLimit)
	for range replayPreparedProofSlotLimit {
		release, acquireErr := svc.acquireReplayPreparedSlot(t.Context())
		require.NoError(t, acquireErr)
		releases = append(releases, release)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = svc.acquireReplayPreparedSlot(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	svc.replayProofMu.Lock()
	require.Equal(t, replayPreparedProofSlotLimit, svc.replayPreparedSlotCount)
	svc.replayProofMu.Unlock()
	releases[0]()
	release, err := svc.acquireReplayPreparedSlot(t.Context())
	require.NoError(t, err)
	release()
	for _, release = range releases[1:] {
		release()
	}
	svc.replayProofMu.Lock()
	require.Zero(t, svc.replayPreparedSlotCount)
	require.Empty(t, svc.replayPreparedProofs)
	svc.replayProofMu.Unlock()
}

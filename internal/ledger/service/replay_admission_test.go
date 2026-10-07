package service

import (
	"context"
	"sync"
	"testing"

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
	reads       int
}

func newReplayAdmissionFamily(t *testing.T) *replayAdmissionFamily {
	t.Helper()
	db := newTestNodeStore(t, 0)
	family := &replayAdmissionFamily{
		NodeStore:   backend.New(db),
		fingerprint: [32]byte{1},
		hidden:      make(map[[32]byte]bool),
	}
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return family
}

func (f *replayAdmissionFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.mu.Lock()
	f.reads++
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

func TestReplayAdmissionUsesGenerationBoundParentProofAfterCacheEviction(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)

	parentState := persistedReplayAdmissionState(t, family, 96)
	parentHash := [32]byte{0x10}
	parent := replayAdmissionLedger(t, 100, parentHash, [32]byte{0x01}, parentState, shamap.New(shamap.TypeTransaction))
	parentProof, err := svc.PrepareReplayParent(t.Context(), parent)
	require.NoError(t, err)
	require.NotNil(t, parentProof)

	childState, err := parent.StateMapSnapshotContext(t.Context())
	require.NoError(t, err)
	changedKey := [32]byte{0xf0, 1}
	require.NoError(t, childState.PutWithNodeType(changedKey, []byte("changed-replay-value"), shamap.NodeTypeAccountState))
	child := replayAdmissionLedger(t, 101, [32]byte{0x11}, parentHash, childState, shamap.New(shamap.TypeTransaction))

	family.resetReads()
	prepared, err := svc.PrepareReplayLedger(t.Context(), child, parentProof)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	require.Positive(t, family.readCount(), "the changed path must be checked against durable storage")
	prepared.Release()
	parentProof.Release()

	// The service certificate is independent of FullBelow cache residency.
	family.FullBelowCache().Bump()
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
	prepared, err := svc.PrepareReplayLedger(t.Context(), child, parentProof)
	require.NoError(t, err)
	otherSvc, err := New(DefaultConfig())
	require.NoError(t, err)
	_, err = otherSvc.BootstrapPreparedLedger(t.Context(), prepared)
	require.ErrorIs(t, err, errReplayAdmissionService)

	family.mu.Lock()
	family.fingerprint[0]++
	family.mu.Unlock()
	_, err = svc.BootstrapPreparedLedger(t.Context(), prepared)
	require.Error(t, err)
	prepared.Release()

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

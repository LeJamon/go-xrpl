package service

import (
	"context"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestDetachedMapVerificationLeavesSourceMutable(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)

	source := shamap.New(shamap.TypeState)
	require.NoError(t, source.Put([32]byte{1}, []byte("state-value-1234")))
	verifier := svc.NewDetachedMapVerification(source, nil)

	require.NoError(t, verifier.Verify(t.Context()))
	require.NoError(t, source.Put([32]byte{2}, []byte("state-value-5678")))
}

type verifierFamily struct {
	*backend.NodeStore

	mu          sync.RWMutex
	hidden      map[[32]byte]bool
	corrupt     map[[32]byte]bool
	fingerprint [32]byte
}

func newVerifierFamily() *verifierFamily {
	return &verifierFamily{
		NodeStore:   backend.NewMemory(),
		hidden:      make(map[[32]byte]bool),
		corrupt:     make(map[[32]byte]bool),
		fingerprint: [32]byte{1},
	}
}

func (f *verifierFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.mu.RLock()
	hidden, corrupt := f.hidden[hash], f.corrupt[hash]
	f.mu.RUnlock()
	if hidden {
		return nil, nil
	}
	data, err := f.NodeStore.FetchDurable(ctx, hash)
	if err != nil || !corrupt || data == nil {
		return data, err
	}
	data = append([]byte(nil), data...)
	data[0] ^= 0xff
	return data, nil
}

func (f *verifierFamily) AcquireDurableSnapshot(ctx context.Context) ([32]byte, func(), error) {
	_, release, err := f.NodeStore.AcquireDurableSnapshot(ctx)
	f.mu.RLock()
	fingerprint := f.fingerprint
	f.mu.RUnlock()
	return fingerprint, release, err
}

func (f *verifierFamily) hide(hash [32]byte) {
	f.mu.Lock()
	f.hidden[hash] = true
	f.fingerprint[0]++
	f.mu.Unlock()
}

func (f *verifierFamily) corruptNode(hash [32]byte) {
	f.mu.Lock()
	f.corrupt[hash] = true
	f.fingerprint[0]++
	f.mu.Unlock()
}

func newVerifierService(t *testing.T, family *verifierFamily) *Service {
	t.Helper()
	t.Cleanup(func() { require.NoError(t, family.Close()) })
	return &Service{shamapFamily: family}
}

func newPersistedVerifierMap(t *testing.T, family *verifierFamily, mapType shamap.Type, count int) *shamap.SHAMap {
	t.Helper()
	source := shamap.New(mapType)
	for i := range count {
		key := [32]byte{byte(i), byte(i >> 8), byte(i >> 16), byte(i >> 24), 1}
		kind := shamap.NodeTypeAccountState
		if mapType == shamap.TypeTransaction {
			kind = shamap.NodeTypeTransactionWithMeta
		}
		require.NoError(t, source.PutWithNodeType(key, []byte("verification-value-1234"), kind))
	}
	require.NoError(t, source.StoreDirty(func(entries []shamap.FlushEntry) error {
		return family.StoreBatch(t.Context(), entries)
	}))
	root, err := source.Hash()
	require.NoError(t, err)
	loaded, err := shamap.NewFromRootHashContext(t.Context(), mapType, root, family)
	require.NoError(t, err)
	return loaded
}

func verifierRootChild(t *testing.T, family *verifierFamily, m *shamap.SHAMap) [32]byte {
	t.Helper()
	root, err := m.Hash()
	require.NoError(t, err)
	data, err := family.Fetch(t.Context(), root)
	require.NoError(t, err)
	node, err := shamap.DeserializeFromPrefix(data)
	require.NoError(t, err)
	reader, ok := node.(shamap.InnerNodeReader)
	require.True(t, ok)
	for branch := range shamap.BranchFactor {
		child, err := reader.ChildHash(branch)
		require.NoError(t, err)
		if child != ([32]byte{}) {
			return child
		}
	}
	t.Fatal("persisted map root has no child")
	return [32]byte{}
}

func verifyUntilComplete(t *testing.T, v *DetachedMapVerification, budget int64) {
	t.Helper()
	for attempt := range 100 {
		err := v.Verify(shamap.WithTraversalBudget(t.Context(), budget))
		if err == nil {
			return
		}
		require.ErrorIs(t, err, shamap.ErrTraversalBudget, "attempt %d", attempt)
	}
	t.Fatal("verification did not complete within bounded retries")
}

func TestDetachedMapVerificationResumesStateBeforeTransaction(t *testing.T) {
	family := newVerifierFamily()
	state := newPersistedVerifierMap(t, family, shamap.TypeState, 96)
	tx := newPersistedVerifierMap(t, family, shamap.TypeTransaction, 96)
	verifier := newVerifierService(t, family).NewDetachedMapVerification(state, tx)

	err := verifier.Verify(shamap.WithTraversalBudget(t.Context(), 3))
	require.ErrorIs(t, err, shamap.ErrTraversalBudget)
	require.NotNil(t, verifier.state)
	require.NotNil(t, verifier.tx)
	require.Zero(t, verifier.tx.BackedWalkStats().NodesDescended)
	verifyUntilComplete(t, verifier, 8)
}

func TestDetachedMapVerificationRejectsMissingAndCorruptNodes(t *testing.T) {
	t.Run("missing root", func(t *testing.T) {
		family := newVerifierFamily()
		state := newPersistedVerifierMap(t, family, shamap.TypeState, 8)
		root, err := state.Hash()
		require.NoError(t, err)
		family.hide(root)
		verifier := newVerifierService(t, family).NewDetachedMapVerification(state, nil)
		require.ErrorIs(t, verifier.Verify(t.Context()), shamap.ErrNodeNotInStore)
	})

	t.Run("missing descendant", func(t *testing.T) {
		family := newVerifierFamily()
		state := newPersistedVerifierMap(t, family, shamap.TypeState, 32)
		family.hide(verifierRootChild(t, family, state))
		verifier := newVerifierService(t, family).NewDetachedMapVerification(state, nil)
		require.ErrorIs(t, verifier.Verify(t.Context()), shamap.ErrNodeNotInStore)
	})

	t.Run("corrupt root", func(t *testing.T) {
		family := newVerifierFamily()
		state := newPersistedVerifierMap(t, family, shamap.TypeState, 8)
		root, err := state.Hash()
		require.NoError(t, err)
		family.corruptNode(root)
		verifier := newVerifierService(t, family).NewDetachedMapVerification(state, nil)
		require.Error(t, verifier.Verify(t.Context()))
	})

	t.Run("corrupt descendant", func(t *testing.T) {
		family := newVerifierFamily()
		state := newPersistedVerifierMap(t, family, shamap.TypeState, 32)
		family.corruptNode(verifierRootChild(t, family, state))
		verifier := newVerifierService(t, family).NewDetachedMapVerification(state, nil)
		require.Error(t, verifier.Verify(t.Context()))
	})
}

func TestDetachedMapVerificationRevalidatesAfterFingerprintAndCacheChanges(t *testing.T) {
	family := newVerifierFamily()
	state := newPersistedVerifierMap(t, family, shamap.TypeState, 96)
	tx := newPersistedVerifierMap(t, family, shamap.TypeTransaction, 96)
	verifier := newVerifierService(t, family).NewDetachedMapVerification(state, tx)

	err := verifier.Verify(shamap.WithTraversalBudget(t.Context(), 3))
	require.ErrorIs(t, err, shamap.ErrTraversalBudget)
	root, err := state.Hash()
	require.NoError(t, err)
	family.hide(root)
	require.ErrorIs(t, verifier.Verify(shamap.WithTraversalBudget(t.Context(), 8)), shamap.ErrNodeNotInStore)
	require.Zero(t, verifier.tx.BackedWalkStats().NodesDescended)

	family.mu.Lock()
	delete(family.hidden, root)
	family.fingerprint[0]++
	family.mu.Unlock()
	verifyUntilComplete(t, verifier, 8)
	family.FullBelowCache().Bump()
	err = verifier.Verify(shamap.WithTraversalBudget(t.Context(), 3))
	require.ErrorIs(t, err, shamap.ErrTraversalBudget)
	verifyUntilComplete(t, verifier, 8)
}

func TestDetachedMapVerificationCancellationDoesNotComplete(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	state := shamap.New(shamap.TypeState)
	require.NoError(t, state.Put([32]byte{1}, []byte("state-value-1234")))
	verifier := svc.NewDetachedMapVerification(state, nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, verifier.Verify(ctx), context.Canceled)
	require.False(t, verifier.initialized)
	require.NoError(t, verifier.Verify(t.Context()))
}

func TestDetachedMapVerificationRechecksCompletedStateWhileTransactionsResume(t *testing.T) {
	for _, tc := range []struct {
		name               string
		fingerprintChanges bool
	}{
		{name: "cache invalidated"},
		{name: "storage changed", fingerprintChanges: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			family := newVerifierFamily()
			state := newPersistedVerifierMap(t, family, shamap.TypeState, 32)
			txs := newPersistedVerifierMap(t, family, shamap.TypeTransaction, 96)
			verifier := newVerifierService(t, family).NewDetachedMapVerification(state, txs)
			for attempt := 0; attempt < 100; attempt++ {
				require.ErrorIs(t, verifier.Verify(shamap.WithTraversalBudget(t.Context(), 8)), shamap.ErrTraversalBudget)
				if verifier.tx.BackedWalkStats().NodesDescended > 0 {
					break
				}
			}
			require.Positive(t, verifier.tx.BackedWalkStats().NodesDescended)
			root, err := state.Hash()
			require.NoError(t, err)
			require.True(t, family.FullBelowCache().Has(family.FullBelowCache().Generation(), root))
			missing := verifierRootChild(t, family, state)
			if tc.fingerprintChanges {
				family.hide(missing)
			} else {
				family.mu.Lock()
				family.hidden[missing] = true
				family.mu.Unlock()
				family.FullBelowCache().Bump()
			}
			require.ErrorIs(t, verifier.Verify(t.Context()), shamap.ErrNodeNotInStore)
			family.mu.Lock()
			delete(family.hidden, missing)
			family.mu.Unlock()
			verifyUntilComplete(t, verifier, 8)
		})
	}
}

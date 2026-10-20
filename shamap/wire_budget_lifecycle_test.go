package shamap

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type wireLifecycleFamily struct {
	*memoryFamily
	cache *CleanResidency

	boundedCalls atomic.Int32
	blockFirst   atomic.Bool
	firstEntered chan struct{}
	firstRelease chan struct{}
}

func newWireLifecycleFamily(cache *CleanResidency) *wireLifecycleFamily {
	return &wireLifecycleFamily{
		memoryFamily: newMemoryFamily(),
		cache:        cache,
		firstEntered: make(chan struct{}),
		firstRelease: make(chan struct{}),
	}
}

func (f *wireLifecycleFamily) FetchBounded(ctx context.Context, hash [32]byte, maxPrefixBytes int) ([]byte, bool, error) {
	f.boundedCalls.Add(1)
	if f.blockFirst.CompareAndSwap(true, false) {
		close(f.firstEntered)
		select {
		case <-f.firstRelease:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	return f.memoryFamily.FetchBounded(ctx, hash, maxPrefixBytes)
}

func (f *wireLifecycleFamily) SHAMapCleanResidency() *CleanResidency {
	return f.cache
}

func wireLifecycleFixture(t testing.TB, family *wireLifecycleFamily) (*SHAMap, *SHAMap) {
	t.Helper()
	source := New(TypeState)
	require.NoError(t, source.Put([32]byte{0x01}, bytes.Repeat([]byte{0xA5}, 32)))
	require.NoError(t, source.Put([32]byte{0x10}, bytes.Repeat([]byte{0x5A}, 32)))
	root, err := source.Hash()
	require.NoError(t, err)
	require.NoError(t, flushToFamily(source, family.memoryFamily))
	backed, err := NewFromRootHash(TypeState, root, family)
	require.NoError(t, err)
	return source, backed
}

func wireLifecycleChild(t testing.TB, root *innerNode) ([32]byte, int) {
	t.Helper()
	for branch := range BranchFactor {
		_, hash, present := root.LoadChild(branch)
		if present {
			return hash, branch
		}
	}
	t.Fatal("fixture root has no child")
	return [32]byte{}, -1
}

func wireLifecycleBudget() *WireBudget {
	return &WireBudget{
		RemainingNodes:     64,
		RemainingWireBytes: 1 << 20,
		RemainingWork:      1 << 20,
	}
}

func waitWireLifecycleSignal(t testing.TB, signal <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-signal:
	case <-timer.C:
		t.Fatal("timed out waiting for bounded cold read")
	}
}

type wireLifecycleResult struct {
	nodes []WireNode
	err   error
}

func TestGetNodeFatByPathBoundedCancellationReturnsNoResponse(t *testing.T) {
	family := newWireLifecycleFamily(nil)
	_, backed := wireLifecycleFixture(t, family)
	family.blockFirst.Store(true)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	t.Cleanup(func() {
		select {
		case <-family.firstRelease:
		default:
			close(family.firstRelease)
		}
	})
	resultCh := make(chan wireLifecycleResult, 1)
	go func() {
		nodes, err := backed.GetNodeFatByPathBounded(ctx, [32]byte{}, 0, 1, true, wireLifecycleBudget())
		resultCh <- wireLifecycleResult{nodes: nodes, err: err}
	}()

	waitWireLifecycleSignal(t, family.firstEntered)
	cancel()
	select {
	case result := <-resultCh:
		require.ErrorIs(t, result.err, context.Canceled)
		require.Nil(t, result.nodes, "canceled serving must not return a partial response")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for canceled bounded walk")
	}
	require.Equal(t, int32(1), family.boundedCalls.Load())
}

func TestGetNodeFatByPathBoundedRejectsMissingAndCorruptColdChildren(t *testing.T) {
	tests := map[string]func(testing.TB, *wireLifecycleFamily, [32]byte){
		"missing": func(t testing.TB, family *wireLifecycleFamily, hash [32]byte) {
			family.memoryFamily.mu.Lock()
			delete(family.memoryFamily.store, hash)
			family.memoryFamily.mu.Unlock()
		},
		"corrupt-prefix": func(t testing.TB, family *wireLifecycleFamily, hash [32]byte) {
			family.memoryFamily.mu.Lock()
			family.memoryFamily.store[hash] = []byte{0xff, 0xee, 0xdd, 0xcc}
			family.memoryFamily.mu.Unlock()
		},
		"hash-mismatch": func(t testing.TB, family *wireLifecycleFamily, hash [32]byte) {
			family.memoryFamily.mu.RLock()
			data := bytes.Clone(family.memoryFamily.store[hash])
			family.memoryFamily.mu.RUnlock()
			require.Greater(t, len(data), 4)
			data[4] ^= 0x01
			family.memoryFamily.mu.Lock()
			family.memoryFamily.store[hash] = data
			family.memoryFamily.mu.Unlock()
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			family := newWireLifecycleFamily(nil)
			_, backed := wireLifecycleFixture(t, family)
			childHash, _ := wireLifecycleChild(t, backed.tree.root)
			mutate(t, family, childHash)

			nodes, err := backed.GetNodeFatByPathBounded(t.Context(), [32]byte{}, 0, 1, true, wireLifecycleBudget())
			require.Nil(t, nodes)
			require.Equal(t, int32(2), family.boundedCalls.Load())
			switch name {
			case "missing":
				require.ErrorIs(t, err, ErrNodeNotInStore)
				var missing *MissingNodeError
				require.ErrorAs(t, err, &missing)
				require.Equal(t, childHash, missing.Hash)
			case "hash-mismatch":
				require.ErrorIs(t, err, ErrInvalidNodeData)
				require.Contains(t, err.Error(), "hash mismatch")
			default:
				require.ErrorIs(t, err, ErrInvalidNodeData)
			}
		})
	}
}

func TestGetNodeFatByPathBoundedRetiredReadCannotRepopulateResidency(t *testing.T) {
	cache := NewCleanResidency(1<<20, nil)
	t.Cleanup(cache.Close)
	family := newWireLifecycleFamily(cache)
	family.blockFirst.Store(true)
	_, backed := wireLifecycleFixture(t, family)
	childBranch := 1
	_, childHash, present := backed.tree.root.LoadChild(childBranch)
	require.True(t, present)

	resultCh := make(chan wireLifecycleResult, 1)
	go func() {
		nodes, err := backed.GetNodeFatByPathBounded(t.Context(), [32]byte{}, 0, 1, true, &WireBudget{
			RemainingNodes:     2,
			RemainingWireBytes: 1 << 20,
			RemainingWork:      1 << 20,
		})
		resultCh <- wireLifecycleResult{nodes: nodes, err: err}
	}()
	waitWireLifecycleSignal(t, family.firstEntered)

	finishMutation := cache.BeginMutation()
	finishMutation()
	t.Cleanup(func() {
		select {
		case <-family.firstRelease:
		default:
			close(family.firstRelease)
		}
	})
	close(family.firstRelease)
	select {
	case result := <-resultCh:
		require.ErrorIs(t, result.err, ErrWireBudget)
		require.Len(t, result.nodes, 2)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retired-generation read")
	}

	stats := cache.Stats()
	require.Zero(t, stats.Peer.Nodes)
	require.Positive(t, stats.Peer.AdmissionDenials)
	require.Equal(t, int32(1), family.boundedCalls.Load())

	childPath := childPathForBranch([32]byte{}, 0, childBranch)
	nodes, err := backed.GetNodeFatByPathBounded(t.Context(), childPath, 1, 0, true, &WireBudget{
		RemainingNodes:     1,
		RemainingWireBytes: 1 << 20,
		RemainingWork:      1 << 20,
	})
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Equal(t, int32(2), family.boundedCalls.Load())
	require.Equal(t, uint64(1), cache.Stats().Peer.Nodes)
	_, ok := cache.lookupForPolicy(childHash, ReadPeer)
	require.True(t, ok)
}

func TestGetNodeFatByPathBoundedRejectsOversizedCachedLeaf(t *testing.T) {
	cache := NewCleanResidency(1<<20, nil)
	t.Cleanup(cache.Close)
	family := newWireLifecycleFamily(cache)
	source := New(TypeState)
	require.NoError(t, source.Put([32]byte{0x01}, make([]byte, 700)))
	require.NoError(t, source.Put([32]byte{0x10}, make([]byte, 700)))
	root, err := source.Hash()
	require.NoError(t, err)
	require.NoError(t, flushToFamily(source, family.memoryFamily))
	backed, err := NewFromRootHash(TypeState, root, family)
	require.NoError(t, err)
	_, err = backed.GetNodeFatByPath([32]byte{}, 0, 1, true)
	require.NoError(t, err)
	require.Equal(t, uint64(2), cache.Stats().Peer.Nodes)

	rootBytes, err := wireNodeSize(backed.tree.root)
	require.NoError(t, err)
	nodes, err := backed.GetNodeFatByPathBounded(t.Context(), [32]byte{}, 0, 1, true, &WireBudget{
		RemainingNodes: 16, RemainingWireBytes: rootBytes + 546, RemainingWork: 128,
	})
	require.ErrorIs(t, err, ErrWireBudget)
	require.Len(t, nodes, 1)
	require.Zero(t, family.boundedCalls.Load(), "cached size rejection must not fall back to storage")
}

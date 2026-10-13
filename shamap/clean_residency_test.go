package shamap

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type cleanResidencyFamily struct {
	*memoryFamily
	cache    *CleanResidency
	fetches  atomic.Uint64
	storeErr error
}

func (f *cleanResidencyFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.fetches.Add(1)
	return f.memoryFamily.Fetch(ctx, hash)
}

func (f *cleanResidencyFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	if f.storeErr != nil {
		return f.storeErr
	}
	return f.memoryFamily.StoreBatch(ctx, entries)
}

func (f *cleanResidencyFamily) SHAMapCleanResidency() *CleanResidency {
	return f.cache
}

func TestCleanResidencyReusesVerifiedRecordsWithoutAttaching(t *testing.T) {
	family := &cleanResidencyFamily{
		memoryFamily: newMemoryFamily(),
		cache:        NewCleanResidency(1<<20, nil),
	}
	source := New(TypeState)
	key := [32]byte{0x12, 0x34}
	value := bytes.Repeat([]byte{0xA5}, 32)
	require.NoError(t, source.Put(key, value))
	rootHash, err := source.Hash()
	require.NoError(t, err)
	entries, err := collectDirtyForTest(source)
	require.NoError(t, err)
	require.NoError(t, family.StoreBatch(t.Context(), entries))

	backed, err := NewFromRootHash(TypeState, rootHash, family)
	require.NoError(t, err)
	before := family.fetches.Load()
	item, found, err := backed.Get(key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, value, item.Data())
	firstFetches := family.fetches.Load()
	require.Greater(t, firstFetches, before)
	require.Zero(t, loadedChildCount(backed.tree.root))

	item, found, err = backed.Get(key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, value, item.Data())
	require.Equal(t, firstFetches, family.fetches.Load())
	stats := family.cache.Stats()
	require.Positive(t, stats.Hits)
	require.Positive(t, stats.Misses)
	require.Positive(t, stats.Nodes)
	require.LessOrEqual(t, stats.UsedBytes, stats.Limit)

	// A fresh map still sees the exact same content after the first map has
	// materialized and then discarded its operation-owned path.
	other, err := NewFromRootHash(TypeState, rootHash, family)
	require.NoError(t, err)
	_, found, err = other.Get(key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, firstFetches+1, family.fetches.Load(), "only the second map's root should be fetched")
}

func TestCleanResidencyAdmissionAndCloseReturnBudget(t *testing.T) {
	budget := &cacheTestBudget{limit: 1 << 20}
	cache := NewCleanResidency(1<<20, budget)
	body := prefixNodeBody{kind: storedInner, payload: make([]byte, BranchFactor*32)}
	cache.insert([32]byte{1}, body)
	stats := cache.Stats()
	require.Equal(t, uint64(1), stats.Nodes)
	require.Positive(t, stats.UsedBytes)
	require.Equal(t, stats.UsedBytes, budget.used)

	node, ok := cache.lookup([32]byte{1})
	require.True(t, ok)
	require.NotNil(t, node)
	cache.Close()
	require.Equal(t, uint64(0), cache.Stats().UsedBytes)
	require.Zero(t, budget.used)
	require.Zero(t, cache.Stats().Nodes)
	inner, ok := node.(*innerNode)
	require.True(t, ok)
	require.Equal(t, [32]byte{}, inner.Hash())
}

func TestCleanResidencyEvictionKeepsStructuralCharge(t *testing.T) {
	payload := uint64(16)
	limit := 2*cleanResidencyFixedBytes + 2*payload
	budget := &cacheTestBudget{limit: limit}
	cache := NewCleanResidency(limit, budget)
	body := prefixNodeBody{kind: storedInner, payload: make([]byte, payload)}
	cache.insert([32]byte{1}, body)
	cache.insert([32]byte{2}, body)
	cache.insert([32]byte{3}, body)
	stats := cache.Stats()
	require.Equal(t, uint64(2), stats.Nodes)
	require.Equal(t, uint64(1), stats.Evictions)
	require.Equal(t, limit, stats.UsedBytes)
	require.Equal(t, stats.UsedBytes, budget.used)
	cache.Close()
	require.Zero(t, budget.used)
}

func TestBackedMutationDoesNotRetainCleanHistoryChildren(t *testing.T) {
	family := newMemoryFamily()
	source := New(TypeState)
	oldKey := [32]byte{0x10}
	oldValue := bytes.Repeat([]byte{1}, 16)
	newKey := [32]byte{0xE0}
	require.NoError(t, source.Put(oldKey, oldValue))
	rootHash, err := source.Hash()
	require.NoError(t, err)
	require.NoError(t, flushToFamily(source, family))

	backed, err := NewFromRootHash(TypeState, rootHash, family)
	require.NoError(t, err)
	_, found, err := backed.Get(oldKey)
	require.NoError(t, err)
	require.True(t, found)
	require.Zero(t, loadedChildCount(backed.tree.root))
	require.NoError(t, backed.Put(newKey, bytes.Repeat([]byte{2}, 16)))
	require.LessOrEqual(t, loadedChildCount(backed.tree.root), 1)

	item, found, err := backed.Get(oldKey)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, oldValue, item.Data())
}

func TestStoreDirtyBatchedDurabilityTokenRetainsUntilAcknowledged(t *testing.T) {
	storeErr := errors.New("store unavailable")
	family := &cleanResidencyFamily{memoryFamily: newMemoryFamily(), storeErr: storeErr}
	sm, err := NewBacked(TypeState, family)
	require.NoError(t, err)
	key := [32]byte{0x22}
	require.NoError(t, sm.Put(key, bytes.Repeat([]byte{3}, 16)))
	budget := &dirtyTestBudget{limit: 64 << 10}
	require.ErrorIs(t, sm.StoreDirtyBatched(t.Context(), budget, func(entries []FlushEntry) error { return family.StoreBatch(t.Context(), entries) }), storeErr)
	require.True(t, sm.tree.root.IsDirty())
	require.NotZero(t, loadedChildCount(sm.tree.root))
	require.Zero(t, budget.used)

	family.storeErr = nil
	token := sm.PrepareCleanResidencyRelease()
	require.NotNil(t, token)
	require.Same(t, family, token.Family())
	require.NoError(t, sm.StoreDirtyBatched(t.Context(), budget, func(entries []FlushEntry) error { return family.StoreBatch(t.Context(), entries) }))
	require.False(t, sm.tree.root.IsDirty())
	// StoreBatch visibility alone is not the service's durability fence. The
	// tree remains attached until the caller acknowledges that fence.
	require.NotZero(t, loadedChildCount(sm.tree.root))
	require.Zero(t, budget.used)
	require.True(t, sm.AcknowledgeCleanResidencyRelease(token))
	require.Zero(t, loadedChildCount(sm.tree.root))
	require.False(t, sm.AcknowledgeCleanResidencyRelease(token))
	item, found, err := sm.Get(key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, bytes.Repeat([]byte{3}, 16), item.Data())
}

func TestCleanResidencyReleaseRejectsFamilyReplacement(t *testing.T) {
	family := &cleanResidencyFamily{memoryFamily: newMemoryFamily()}
	sm, err := NewBacked(TypeState, family)
	require.NoError(t, err)
	require.NoError(t, sm.Put([32]byte{0x33}, bytes.Repeat([]byte{4}, 16)))
	budget := &dirtyTestBudget{limit: 64 << 10}
	require.NoError(t, sm.StoreDirtyBatched(t.Context(), budget, func(entries []FlushEntry) error { return family.StoreBatch(t.Context(), entries) }))
	token := sm.PrepareCleanResidencyRelease()
	require.NotNil(t, token)
	sm.SetFamily(newMemoryFamily())
	require.False(t, sm.AcknowledgeCleanResidencyRelease(token))
	require.NotZero(t, loadedChildCount(sm.tree.root))
}

func TestCleanResidencyReleaseRejectsRootReplacement(t *testing.T) {
	family := &cleanResidencyFamily{memoryFamily: newMemoryFamily()}
	sm, err := NewBacked(TypeState, family)
	require.NoError(t, err)
	require.NoError(t, sm.Put([32]byte{0x44}, bytes.Repeat([]byte{5}, 16)))
	budget := &dirtyTestBudget{limit: 64 << 10}
	require.NoError(t, sm.StoreDirtyBatched(t.Context(), budget, func(entries []FlushEntry) error { return family.StoreBatch(t.Context(), entries) }))
	token := sm.PrepareCleanResidencyRelease()
	require.NotNil(t, token)
	require.NoError(t, sm.Put([32]byte{0x55}, bytes.Repeat([]byte{6}, 16)))
	require.False(t, sm.AcknowledgeCleanResidencyRelease(token))
	require.NotZero(t, loadedChildCount(sm.tree.root))
}

type cacheTestBudget struct {
	limit uint64
	used  uint64
}

func (b *cacheTestBudget) TryReserve(bytes uint64) bool {
	if bytes > b.limit-b.used {
		return false
	}
	b.used += bytes
	return true
}

func (b *cacheTestBudget) Release(bytes uint64) {
	if bytes > b.used {
		panic("cache test budget underflow")
	}
	b.used -= bytes
}

func loadedChildCount(node *innerNode) int {
	if node == nil {
		return 0
	}
	count := 0
	for branch := range BranchFactor {
		child, _, present := node.LoadChild(branch)
		if !present || child == nil {
			continue
		}
		count++
		if inner, ok := child.(*innerNode); ok {
			count += loadedChildCount(inner)
		}
	}
	return count
}

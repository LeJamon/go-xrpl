package shamap

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWireDataSizeMatchesWireSerializers(t *testing.T) {
	inner := &innerNode{isBranch: 1}
	size, err := wireDataSize(inner)
	require.NoError(t, err)
	require.Equal(t, 34, size)

	inner.isBranch = (1 << 11) - 1
	size, err = wireDataSize(inner)
	require.NoError(t, err)
	require.Equal(t, 11*33+1, size)

	inner.isBranch = (1 << 12) - 1
	size, err = wireDataSize(inner)
	require.NoError(t, err)
	require.Equal(t, BranchFactor*32+1, size)

	inner.isBranch = ^uint16(0)
	size, err = wireDataSize(inner)
	require.NoError(t, err)
	require.Equal(t, BranchFactor*32+1, size)

	key := [32]byte{1}
	leaf, err := newAccountStateLeafNode(NewItem(key, make([]byte, 12)))
	require.NoError(t, err)
	size, err = wireDataSize(leaf)
	require.NoError(t, err)
	require.Equal(t, 45, size)
}

func TestGetNodeFatByPathBoundedStopsBeforeFetchingAfterBudget(t *testing.T) {
	sm := New(TypeState)
	require.NoError(t, sm.Put([32]byte{0x00}, make([]byte, 12)))
	require.NoError(t, sm.Put([32]byte{0x10}, make([]byte, 12)))

	rootBytes, err := wireNodeSize(sm.tree.root)
	require.NoError(t, err)
	budget := &WireBudget{
		RemainingNodes:     1,
		RemainingWireBytes: rootBytes + wireNodeIDSize,
		RemainingWork:      128,
	}
	nodes, err := sm.GetNodeFatByPathBounded(context.Background(), [32]byte{}, 0, 2, true, budget)
	require.ErrorIs(t, err, ErrWireBudget)
	require.Len(t, nodes, 1)
	require.Zero(t, budget.RemainingNodes)
}

func TestGetNodeFatByPathBoundedStopsColdFetchesAtNodeCap(t *testing.T) {
	source := buildWideWireState(t)
	root, err := source.Hash()
	require.NoError(t, err)
	family := &boundedWireFamily{memoryFamily: newMemoryFamily()}
	require.NoError(t, flushToFamily(source, family.memoryFamily))

	for _, test := range []struct {
		name         string
		nodes        int
		wantFetches  int32
		wantReturned int
	}{
		{name: "one", nodes: 1, wantFetches: 0, wantReturned: 1},
		{name: "two", nodes: 2, wantFetches: 1, wantReturned: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			backed, err := NewFromRootHash(TypeState, root, family)
			require.NoError(t, err)
			family.boundedCalls.Store(0)

			budget := &WireBudget{
				RemainingNodes:     test.nodes,
				RemainingWireBytes: 1 << 20,
				RemainingWork:      1 << 20,
			}
			nodes, err := backed.GetNodeFatByPathBounded(context.Background(), [32]byte{}, 0, 1, true, budget)
			require.ErrorIs(t, err, ErrWireBudget)
			require.Len(t, nodes, test.wantReturned)
			require.Equal(t, test.wantFetches, family.boundedCalls.Load())
		})
	}
}

func TestGetNodeFatByPathBoundedAccountsSkippedColdLeaves(t *testing.T) {
	const payloadSize = 700
	source := New(TypeState)
	for branch := range BranchFactor {
		var key [32]byte
		key[0] = byte(branch << 4)
		require.NoError(t, source.Put(key, make([]byte, payloadSize)))
	}
	family := &boundedWireFamily{memoryFamily: newMemoryFamily()}
	require.NoError(t, flushToFamily(source, family.memoryFamily))
	root, err := source.Hash()
	require.NoError(t, err)
	backed, err := NewFromRootHash(TypeState, root, family)
	require.NoError(t, err)

	rootBytes, err := wireNodeSize(backed.tree.root)
	require.NoError(t, err)
	child, err := source.tree.root.Child(0)
	require.NoError(t, err)
	leafBytes, err := wireNodeSize(child)
	require.NoError(t, err)
	budget := &WireBudget{
		RemainingNodes:     2,
		RemainingWireBytes: rootBytes + leafBytes,
		RemainingWork:      128,
	}

	nodes, err := backed.GetNodeFatByPathBounded(context.Background(), [32]byte{}, 0, 1, false, budget)
	require.ErrorIs(t, err, ErrWireBudget)
	require.Len(t, nodes, 1)
	require.Equal(t, int32(1), family.boundedCalls.Load())
	require.Zero(t, budget.RemainingNodes)
	require.Zero(t, budget.RemainingWireBytes)
}

func TestGetNodeFatByPathBoundedPartialReplyCanContinueAtNextNodeID(t *testing.T) {
	source := buildWideWireState(t)
	root, err := source.Hash()
	require.NoError(t, err)
	family := &boundedWireFamily{memoryFamily: newMemoryFamily()}
	require.NoError(t, flushToFamily(source, family.memoryFamily))
	backed, err := NewFromRootHash(TypeState, root, family)
	require.NoError(t, err)

	full, err := source.GetNodeFatByPath([32]byte{}, 0, 1, true)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(full), 2)

	firstBudget := &WireBudget{
		RemainingNodes:     1,
		RemainingWireBytes: 1 << 20,
		RemainingWork:      1 << 20,
	}
	first, err := backed.GetNodeFatByPathBounded(context.Background(), [32]byte{}, 0, 1, true, firstBudget)
	require.ErrorIs(t, err, ErrWireBudget)
	require.Equal(t, full[0], first[0])
	require.Zero(t, family.boundedCalls.Load())

	nextID, err := ParseNodeID(full[1].NodeID)
	require.NoError(t, err)
	nextBudget := &WireBudget{
		RemainingNodes:     1,
		RemainingWireBytes: 1 << 20,
		RemainingWork:      1 << 20,
	}
	next, err := backed.GetNodeFatByPathBounded(context.Background(), nextID.ID(), int(nextID.Depth()), 1, true, nextBudget)
	require.NoError(t, err)
	require.Len(t, next, 1)
	require.Equal(t, full[1], next[0])
	require.Equal(t, int32(1), family.boundedCalls.Load())
}

func buildWideWireState(t testing.TB) *SHAMap {
	t.Helper()
	sm := New(TypeState)
	for branch := range BranchFactor {
		var key [32]byte
		key[0] = byte(branch<<4 | ((branch + 1) & 0x0f))
		if err := sm.Put(key, make([]byte, 12)); err != nil {
			t.Fatalf("Put branch %d: %v", branch, err)
		}
	}
	return sm
}

type boundedWireFamily struct {
	*memoryFamily
	boundedCalls atomic.Int32
	maxPrefix    atomic.Int64
	exceeded     atomic.Int32
}

func (f *boundedWireFamily) FetchBounded(ctx context.Context, hash [32]byte, maxPrefixBytes int) ([]byte, bool, error) {
	f.boundedCalls.Add(1)
	f.maxPrefix.Store(int64(maxPrefixBytes))
	data, exceeded, err := f.memoryFamily.FetchBounded(ctx, hash, maxPrefixBytes)
	if exceeded {
		f.exceeded.Add(1)
	}
	return data, exceeded, err
}

func TestGetNodeFatByPathBoundedBoundsColdPrefixBeforeMaterialization(t *testing.T) {
	family := &boundedWireFamily{memoryFamily: newMemoryFamily()}
	source := New(TypeState)
	large := make([]byte, 700)
	require.NoError(t, source.Put([32]byte{0x00}, large))
	require.NoError(t, source.Put([32]byte{0x10}, large))
	require.NoError(t, flushToFamily(source, family.memoryFamily))
	root, err := source.Hash()
	require.NoError(t, err)
	backed, err := NewFromRootHash(TypeState, root, family)
	require.NoError(t, err)

	rootBytes, err := wireNodeSize(backed.tree.root)
	require.NoError(t, err)
	budget := &WireBudget{
		RemainingNodes:     16,
		RemainingWireBytes: rootBytes + 546,
		RemainingWork:      128,
	}
	nodes, err := backed.GetNodeFatByPathBounded(context.Background(), [32]byte{}, 0, 1, true, budget)
	require.ErrorIs(t, err, ErrWireBudget)
	require.Len(t, nodes, 1)
	require.Positive(t, family.boundedCalls.Load())
	require.Positive(t, family.exceeded.Load())
	require.Equal(t, int64(516), family.maxPrefix.Load())
}

func TestGetNodeFatByPathBoundedRequiresBoundedFamilyForColdNode(t *testing.T) {
	family := newUnboundedWireFamily()
	source := New(TypeState)
	require.NoError(t, source.Put([32]byte{0x00}, make([]byte, 12)))
	require.NoError(t, source.Put([32]byte{0x10}, make([]byte, 12)))
	require.NoError(t, flushToFamily(source, family.base))
	root, err := source.Hash()
	require.NoError(t, err)
	backed, err := NewFromRootHash(TypeState, root, family)
	require.NoError(t, err)

	budget := &WireBudget{
		RemainingNodes:     16,
		RemainingWireBytes: 1 << 20,
		RemainingWork:      128,
	}
	nodes, err := backed.GetNodeFatByPathBounded(context.Background(), [32]byte{}, 0, 1, true, budget)
	require.Nil(t, nodes)
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.ErrUnsupported))
}

type unboundedWireFamily struct {
	base *memoryFamily
}

func newUnboundedWireFamily() *unboundedWireFamily {
	return &unboundedWireFamily{base: newMemoryFamily()}
}

func (f *unboundedWireFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.base.Fetch(ctx, hash)
}

func (f *unboundedWireFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	return f.base.StoreBatch(ctx, entries)
}

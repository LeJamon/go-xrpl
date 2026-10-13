package shamap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

type dirtyTestBudget struct {
	limit, used, peak uint64
}

func (b *dirtyTestBudget) Limit() uint64            { return b.limit }
func (b *dirtyTestBudget) Changed() <-chan struct{} { return nil }
func (b *dirtyTestBudget) TryReserve(n uint64) bool {
	if n > b.limit-b.used {
		return false
	}
	b.used += n
	b.peak = max(b.peak, b.used)
	return true
}
func (b *dirtyTestBudget) Release(n uint64) {
	if n > b.used {
		panic("dirty test reservation underflow")
	}
	b.used -= n
}

func TestStoreDirtyBatchedRetriesFailedBatchAndMatchesUnbounded(t *testing.T) {
	bounded, reference := New(TypeState), New(TypeState)
	for i := range 1200 {
		var seed [8]byte
		binary.BigEndian.PutUint64(seed[:], uint64(i))
		key := sha256.Sum256(seed[:])
		value := bytes.Repeat(seed[:], 16)
		require.NoError(t, bounded.Put(key, value))
		require.NoError(t, reference.Put(key, value))
	}
	expected := make(map[[32]byte][]byte)
	require.NoError(t, reference.StoreDirty(func(entries []FlushEntry) error {
		for _, entry := range entries {
			expected[entry.Hash] = bytes.Clone(entry.Data)
		}
		return nil
	}))
	budget := &dirtyTestBudget{limit: 64 << 10}
	persisted := make(map[[32]byte][]byte)
	storeFailure := errors.New("storage unavailable")
	calls := 0
	fail := true
	store := func(entries []FlushEntry) error {
		calls++
		require.Positive(t, budget.used)
		require.LessOrEqual(t, len(entries), 256)
		if fail && calls == 2 {
			return storeFailure
		}
		for _, entry := range entries {
			persisted[entry.Hash] = bytes.Clone(entry.Data)
		}
		return nil
	}
	require.ErrorIs(t, bounded.StoreDirtyBatched(context.Background(), budget, store), storeFailure)
	require.Zero(t, budget.used)
	require.True(t, bounded.tree.root.IsDirty())
	fail = false
	require.NoError(t, bounded.StoreDirtyBatched(context.Background(), budget, store))
	require.Zero(t, budget.used)
	require.LessOrEqual(t, budget.peak, budget.limit)
	require.Equal(t, expected, persisted)
	got, err := bounded.Hash()
	require.NoError(t, err)
	want, err := reference.Hash()
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.False(t, bounded.tree.root.IsDirty())
	before := calls
	require.NoError(t, bounded.StoreDirtyBatched(context.Background(), budget, store))
	require.Equal(t, before, calls)
}

func TestStoreDirtyBatchedOversizedNodeAndCancellation(t *testing.T) {
	sm := New(TypeState)
	require.NoError(t, sm.Put([32]byte{1}, make([]byte, 32<<10)))
	budget := &dirtyTestBudget{limit: 64 << 10}
	err := sm.StoreDirtyBatched(context.Background(), budget, func([]FlushEntry) error {
		t.Fatal("oversized serialization reached storage")
		return nil
	})
	require.ErrorContains(t, err, "workspace")
	require.Zero(t, budget.used)
	require.True(t, sm.tree.root.IsDirty())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, sm.StoreDirtyBatched(ctx, budget, func([]FlushEntry) error { return nil }), context.Canceled)
	require.Zero(t, budget.used)
}

func TestStoreDirtyBatchedEmptyTree(t *testing.T) {
	budget := &dirtyTestBudget{limit: 64 << 10}
	require.NoError(t, New(TypeState).StoreDirtyBatched(context.Background(), budget, func([]FlushEntry) error {
		t.Fatal("empty root must not be serialized")
		return nil
	}))
	require.Zero(t, budget.used)
}

func TestStoreAllBatchedCopiesCleanStateAndTransactionTrees(t *testing.T) {
	for _, mapType := range []Type{TypeState, TypeTransaction} {
		t.Run(mapType.String(), func(t *testing.T) {
			source := newStoreAllTestMap(t, mapType)
			wantRoot, err := source.Hash()
			require.NoError(t, err)
			wantHashes := wireNodeHashes(t, source)

			family := newMemoryFamily()
			budget := &dirtyTestBudget{limit: 64 << 10}
			var stored []FlushEntry
			require.NoError(t, source.StoreAllBatched(context.Background(), budget, func(entries []FlushEntry) error {
				stored = append(stored, cloneFlushEntries(entries)...)
				return family.StoreBatch(context.Background(), entries)
			}))
			require.Zero(t, budget.used)
			require.LessOrEqual(t, budget.peak, budget.limit)
			require.Equal(t, wantRoot, mustMapHash(t, source))
			require.False(t, source.tree.root.IsDirty())
			require.Equal(t, wantHashes, storedNodeHashes(stored))

			family.mu.RLock()
			require.Equal(t, wantHashes, storedNodeHashesFromFamily(family))
			family.mu.RUnlock()
			for _, entry := range stored {
				require.Equal(t, uint32(17), entry.LedgerSeq)
				require.Equal(t, mapType, entry.MapType)
			}

			loaded, err := NewFromRootHash(mapType, wantRoot, family)
			require.NoError(t, err)
			var items int
			require.NoError(t, loaded.ForEach(func(*Item) bool {
				items++
				return true
			}))
			require.Equal(t, storeAllTestLeafCount, items)
		})
	}
}

func TestStoreAllBatchedReleasesColdBackedChildren(t *testing.T) {
	for _, mapType := range []Type{TypeState, TypeTransaction} {
		t.Run(mapType.String(), func(t *testing.T) {
			source := newStoreAllTestMap(t, mapType)
			root, err := source.Hash()
			require.NoError(t, err)
			wantHashes := wireNodeHashes(t, source)

			backing := newMemoryFamily()
			require.NoError(t, source.StoreDirty(func(entries []FlushEntry) error {
				return backing.StoreBatch(context.Background(), entries)
			}))
			cold, err := NewFromRootHash(mapType, root, backing)
			require.NoError(t, err)
			for branch := range BranchFactor {
				child, _, present := cold.tree.root.LoadChild(branch)
				require.Falsef(t, present && child != nil, "backed root branch %d was resident before walk", branch)
			}

			destination := newMemoryFamily()
			budget := &dirtyTestBudget{limit: 64 << 10}
			require.NoError(t, cold.StoreAllBatched(context.Background(), budget, func(entries []FlushEntry) error {
				return destination.StoreBatch(context.Background(), entries)
			}))
			require.Zero(t, budget.used)
			require.Equal(t, root, mustMapHash(t, cold))
			for branch := range BranchFactor {
				child, _, present := cold.tree.root.LoadChild(branch)
				require.Falsef(t, present && child != nil, "backed root branch %d remained resident after walk", branch)
			}

			destination.mu.RLock()
			require.Equal(t, wantHashes, storedNodeHashesFromFamily(destination))
			destination.mu.RUnlock()
			loaded, err := NewFromRootHash(mapType, root, destination)
			require.NoError(t, err)
			var items int
			require.NoError(t, loaded.ForEach(func(*Item) bool {
				items++
				return true
			}))
			require.Equal(t, storeAllTestLeafCount, items)
		})
	}
}

func TestStoreAllBatchedRejectsIncompleteUnbackedTree(t *testing.T) {
	source := newStoreAllTestMap(t, TypeState)
	wantRoot := mustMapHash(t, source)
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)

	partial := New(TypeState)
	_, err = partial.AddRootNodeWithEntry(wantRoot, rootData)
	require.NoError(t, err)
	var wantMissing [32]byte
	for branch := range BranchFactor {
		_, childHash, present := partial.tree.root.LoadChild(branch)
		if present {
			wantMissing = childHash
			break
		}
	}
	require.NotEqual(t, [32]byte{}, wantMissing)

	budget := &dirtyTestBudget{limit: 64 << 10}
	storeCalls := 0
	err = partial.StoreAllBatched(context.Background(), budget, func([]FlushEntry) error {
		storeCalls++
		return nil
	})
	var missing *MissingNodeError
	require.True(t, errors.As(err, &missing), "StoreAllBatched error = %v", err)
	require.Equal(t, wantMissing, missing.Hash)
	require.Zero(t, storeCalls)
	require.Zero(t, budget.used)
	require.Equal(t, wantRoot, mustMapHash(t, partial))
}

func TestStoreAllBatchedCancellationAndStoreFailureReleaseBudget(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(context.Context, *SHAMap, *dirtyTestBudget) error
	}{
		{
			name: "store failure",
			call: func(ctx context.Context, source *SHAMap, budget *dirtyTestBudget) error {
				failure := errors.New("store unavailable")
				calls := 0
				return source.StoreAllBatched(ctx, budget, func([]FlushEntry) error {
					calls++
					if calls == 1 {
						return failure
					}
					return nil
				})
			},
		},
		{
			name: "cancellation after first batch",
			call: func(ctx context.Context, source *SHAMap, budget *dirtyTestBudget) error {
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				calls := 0
				return source.StoreAllBatched(ctx, budget, func([]FlushEntry) error {
					calls++
					if calls == 1 {
						cancel()
					}
					return nil
				})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := newStoreAllTestMap(t, TypeState)
			before := mustMapHash(t, source)
			budget := &dirtyTestBudget{limit: 64 << 10}
			err := test.call(context.Background(), source, budget)
			if test.name == "store failure" {
				require.ErrorContains(t, err, "store unavailable")
			} else {
				require.ErrorIs(t, err, context.Canceled)
			}
			require.Zero(t, budget.used)
			require.LessOrEqual(t, budget.peak, budget.limit)
			require.Equal(t, before, mustMapHash(t, source))
			require.True(t, source.tree.root.IsDirty())
		})
	}
}

const storeAllTestLeafCount = 400

func newStoreAllTestMap(t *testing.T, mapType Type) *SHAMap {
	t.Helper()
	sm := New(mapType)
	sm.SetLedgerSeq(17)
	for i := range storeAllTestLeafCount {
		var seed [8]byte
		binary.BigEndian.PutUint64(seed[:], uint64(i+1))
		key := sha256.Sum256(seed[:])
		data := bytes.Repeat(seed[:], 2)
		if mapType == TypeTransaction {
			key = sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), data)
		}
		if err := sm.Put(key, data); err != nil {
			t.Fatalf("Put(%d): %v", i, err)
		}
	}
	return sm
}

func wireNodeHashes(t *testing.T, sm *SHAMap) map[[32]byte]struct{} {
	t.Helper()
	nodes, err := sm.WalkWireNodes()
	require.NoError(t, err)
	hashes := make(map[[32]byte]struct{}, len(nodes))
	for _, node := range nodes {
		decoded, err := DeserializeFromWire(node.Data)
		require.NoError(t, err)
		hashes[decoded.Hash()] = struct{}{}
	}
	return hashes
}

func cloneFlushEntries(entries []FlushEntry) []FlushEntry {
	cloned := make([]FlushEntry, len(entries))
	for i, entry := range entries {
		cloned[i] = entry
		cloned[i].Data = bytes.Clone(entry.Data)
	}
	return cloned
}

func storedNodeHashes(entries []FlushEntry) map[[32]byte]struct{} {
	hashes := make(map[[32]byte]struct{}, len(entries))
	for _, entry := range entries {
		hashes[entry.Hash] = struct{}{}
	}
	return hashes
}

func storedNodeHashesFromFamily(family *memoryFamily) map[[32]byte]struct{} {
	hashes := make(map[[32]byte]struct{}, len(family.store))
	for hash := range family.store {
		hashes[hash] = struct{}{}
	}
	return hashes
}

func mustMapHash(t *testing.T, sm *SHAMap) [32]byte {
	t.Helper()
	hash, err := sm.Hash()
	require.NoError(t, err)
	return hash
}

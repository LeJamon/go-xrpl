package shamap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"testing"

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

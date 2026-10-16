package shamap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

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

func TestStoreDirtyBatchedReservesOnlyTinyBatch(t *testing.T) {
	sm := New(TypeState)
	require.NoError(t, sm.Put([32]byte{1}, bytes.Repeat([]byte{2}, 32)))
	budget := &dirtyTestBudget{limit: DirtyWorkspaceBytes}
	var serializedCharge uint64
	var entriesCount uint64
	require.NoError(t, sm.StoreDirtyBatched(context.Background(), budget, func(entries []FlushEntry) error {
		entriesCount += uint64(len(entries))
		for _, entry := range entries {
			serializedCharge += 3 * (uint64(len(entry.Data)) + 128)
		}
		return nil
	}))
	require.Equal(t, uint64(2), entriesCount)
	want := serializedCharge + dirtyBatchStructureBytes
	require.Equal(t, want, budget.peak)
	require.Less(t, budget.peak, uint64(1<<20))
	require.Zero(t, budget.used)
}

func TestStoreDirtyBatchedAllowsSingleNodeAboveBatchTarget(t *testing.T) {
	sm := New(TypeState)
	require.NoError(t, sm.Put([32]byte{1}, bytes.Repeat([]byte{2}, 9<<20)))
	budget := &dirtyTestBudget{limit: DirtyWorkspaceBytes}
	calls := 0
	largeNodeSeen := false
	require.NoError(t, sm.StoreDirtyBatched(context.Background(), budget, func(entries []FlushEntry) error {
		calls++
		for _, entry := range entries {
			if len(entry.Data) > 8<<20 {
				largeNodeSeen = true
			}
		}
		return nil
	}))
	require.Equal(t, 2, calls)
	require.True(t, largeNodeSeen)
	require.Greater(t, budget.peak, uint64(8<<20))
	require.LessOrEqual(t, budget.peak, uint64(DirtyWorkspaceBytes))
	require.Zero(t, budget.used)
}

type wakeupDirtyBudget struct {
	mu       sync.Mutex
	limit    uint64
	used     uint64
	changed  chan struct{}
	attempts chan struct{}
}

func newWakeupDirtyBudget(limit uint64) *wakeupDirtyBudget {
	return &wakeupDirtyBudget{
		limit:    limit,
		changed:  make(chan struct{}),
		attempts: make(chan struct{}, 1),
	}
}

func (b *wakeupDirtyBudget) Limit() uint64 { return b.limit }

func (b *wakeupDirtyBudget) Changed() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.changed
}

func (b *wakeupDirtyBudget) TryReserve(bytes uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bytes <= b.limit-b.used {
		b.used += bytes
		return true
	}
	select {
	case b.attempts <- struct{}{}:
	default:
	}
	return false
}

func (b *wakeupDirtyBudget) Release(bytes uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bytes > b.used {
		panic("wakeup dirty budget underflow")
	}
	b.used -= bytes
	close(b.changed)
	b.changed = make(chan struct{})
}

func (b *wakeupDirtyBudget) Used() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

func TestStoreDirtyBatchedSharedBudgetWakeupAndCancellation(t *testing.T) {
	const limit = 1 << 20
	sm := New(TypeState)
	require.NoError(t, sm.Put([32]byte{1}, bytes.Repeat([]byte{2}, 32)))
	const nodeCharge = 3 * (68 + 128)
	required := uint64(nodeCharge) + dirtyBatchStructureBytes
	budget := newWakeupDirtyBudget(limit)
	require.True(t, budget.TryReserve(limit-required+1))

	wakeDone := make(chan error, 1)
	go func() {
		wakeDone <- sm.StoreDirtyBatched(context.Background(), budget, func(entries []FlushEntry) error {
			if len(entries) == 0 {
				return errors.New("empty dirty batch")
			}
			return nil
		})
	}()
	select {
	case <-budget.attempts:
	case <-time.After(5 * time.Second):
		t.Fatal("bounded persistence did not wait for shared budget")
	}
	budget.Release(limit - required + 1)
	select {
	case err := <-wakeDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("bounded persistence did not wake after budget release")
	}
	require.Zero(t, budget.Used())

	// A waiter that is canceled before capacity returns must release nothing
	// and leave the external owner's reservation intact.
	require.NoError(t, sm.Put([32]byte{2}, bytes.Repeat([]byte{3}, 32)))
	require.True(t, budget.TryReserve(limit-required+1))
	ctx, cancel := context.WithCancel(context.Background())
	cancelDone := make(chan error, 1)
	go func() {
		cancelDone <- sm.StoreDirtyBatched(ctx, budget, func([]FlushEntry) error { return nil })
	}()
	select {
	case <-budget.attempts:
	case <-time.After(5 * time.Second):
		t.Fatal("bounded persistence did not attempt canceled reservation")
	}
	cancel()
	select {
	case err := <-cancelDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("bounded persistence did not observe cancellation")
	}
	require.Equal(t, limit-required+1, budget.Used())
	budget.Release(limit - required + 1)
	require.Zero(t, budget.Used())
}

func TestStoreDirtyBatchedBudgetWaitRevalidatesTree(t *testing.T) {
	for _, test := range []struct {
		name      string
		valueSize int
	}{
		{name: "smaller", valueSize: 16},
		{name: "larger", valueSize: 64 << 10},
		{name: "removed", valueSize: 0},
		{name: "oversized", valueSize: 512 << 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			const limit = 1 << 20
			key := [32]byte{1}
			sm := New(TypeState)
			require.NoError(t, sm.Put(key, bytes.Repeat([]byte{2}, 32)))
			budget := newWakeupDirtyBudget(limit)
			require.True(t, budget.TryReserve(limit))
			release := sync.OnceFunc(func() { budget.Release(limit) })
			defer release()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stored := make(map[[32]byte][]byte)
			done := make(chan error, 1)
			go func() {
				done <- sm.StoreDirtyBatched(ctx, budget, func(entries []FlushEntry) error {
					charge := dirtyBatchStructureBytes
					for _, entry := range entries {
						charge += 3 * (uint64(len(entry.Data)) + 128)
						stored[entry.Hash] = bytes.Clone(entry.Data)
					}
					if used := budget.Used(); used != charge {
						return fmt.Errorf("batch charge is %d, want %d", used, charge)
					}
					return nil
				})
			}()
			select {
			case <-budget.attempts:
			case <-time.After(5 * time.Second):
				t.Fatal("persistence did not wait for memory")
			}

			value := bytes.Repeat([]byte{3}, test.valueSize)
			mutated := make(chan error, 1)
			go func() {
				if len(value) == 0 {
					mutated <- sm.Delete(key)
				} else {
					mutated <- sm.Put(key, value)
				}
			}()
			select {
			case err := <-mutated:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("memory wait retained the tree lock")
			}
			release()
			select {
			case err := <-done:
				if test.name == "oversized" {
					require.ErrorContains(t, err, "workspace")
					require.Empty(t, stored)
					require.True(t, sm.tree.root.IsDirty())
				} else {
					require.NoError(t, err)
					reference := New(TypeState)
					if len(value) > 0 {
						require.NoError(t, reference.Put(key, value))
					}
					want := make(map[[32]byte][]byte)
					require.NoError(t, reference.StoreDirty(func(entries []FlushEntry) error {
						for _, entry := range entries {
							want[entry.Hash] = bytes.Clone(entry.Data)
						}
						return nil
					}))
					require.Equal(t, want, stored)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("persistence did not resume after memory release")
			}
			require.Zero(t, budget.Used())
		})
	}
}

func TestStoreDirtyBatchedPartialBatchWaitReleasesTree(t *testing.T) {
	const limit = 1 << 20
	const firstCharge = 3 * (32 + 4 + 32 + 128)
	sm := New(TypeState)
	require.NoError(t, sm.Put([32]byte{0x10}, bytes.Repeat([]byte{1}, 32)))
	require.NoError(t, sm.Put([32]byte{0x20}, bytes.Repeat([]byte{2}, 64<<10)))
	budget := newWakeupDirtyBudget(limit)
	held := uint64(limit) - dirtyBatchStructureBytes - firstCharge
	require.True(t, budget.TryReserve(held))
	release := sync.OnceFunc(func() { budget.Release(held) })
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	flushed := make(chan struct{})
	firstFlush := sync.OnceFunc(func() { close(flushed) })
	done := make(chan error, 1)
	go func() {
		done <- sm.StoreDirtyBatched(ctx, budget, func([]FlushEntry) error {
			firstFlush()
			return nil
		})
	}()
	select {
	case <-flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("persistence did not flush its partial batch")
	}
	read := make(chan error, 1)
	go func() {
		_, err := sm.Hash()
		read <- err
	}()
	select {
	case err := <-read:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("partial-batch memory wait retained the tree lock")
	}
	require.Equal(t, held, budget.Used(), "persistence must release its partial batch before waiting")
	release()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("partial batch did not resume")
	}
	require.Zero(t, budget.Used())
	require.False(t, sm.tree.root.IsDirty())
}

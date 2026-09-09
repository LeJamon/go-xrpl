package pebble

import (
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/cockroachdb/pebble/vfs/errorfs"
	"github.com/stretchr/testify/require"
)

func TestReadOnlyPromotionsDoNotInvalidateWritableSnapshots(t *testing.T) {
	store, err := NewRotating(filepath.Join(t.TempDir(), "nodes"), Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	key := []byte("present")
	require.NoError(t, store.Put(key, []byte("value")))
	before := store.mutationVersions
	for range 3 {
		records, stats, err := store.PromoteBatch([][]byte{key, []byte("missing")}, 1024)
		require.NoError(t, err)
		require.Len(t, records, 2)
		require.Zero(t, stats.VersionMismatches)
		require.Zero(t, stats.Promoted)
		require.Zero(t, stats.Batches)
		_, err = store.Promote(key)
		require.NoError(t, err)
		_, err = store.Promote([]byte("missing"))
		require.ErrorIs(t, err, kvstore.ErrNotFound)
	}
	require.Equal(t, before, store.mutationVersions)
}

func TestPromotionFallbackPreservesPrecedenceAndByteLimitedPrefix(t *testing.T) {
	for _, test := range []struct {
		name         string
		writable     bool
		limit        int
		wantConsumed int
		wantBatches  int
	}{
		{name: "oversized writable and missing", writable: true, limit: 1, wantConsumed: 2},
		{name: "archive prefix", limit: 8, wantConsumed: 2, wantBatches: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewRotating(filepath.Join(t.TempDir(), "nodes"), Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			keys := [][]byte{[]byte("a"), []byte("b"), []byte("c")}
			require.NoError(t, store.archive.Put(keys[0], []byte("archive")))
			require.NoError(t, store.archive.Put(keys[2], []byte("next")))
			want := []byte("archive")
			if test.writable {
				want = []byte("new writable")
				require.NoError(t, store.Put(keys[0], want))
			}
			store.mu.RLock()
			defer store.mu.RUnlock()
			store.archiveMu.RLock()
			defer store.archiveMu.RUnlock()
			archive, _, err := store.prefetchPromotionPass(keys, 1024, nil, [rotatingStoreMutationStripes]uint64{}, &kvstore.PromotionStats{})
			require.NoError(t, err)
			var records []kvstore.Promotion
			var stats kvstore.PromotionStats
			for index, key := range keys {
				var consumed bool
				records, consumed, err = store.promoteOne(key, archive[index], ^uint64(0), test.limit, records, &stats)
				require.NoError(t, err)
				if !consumed {
					break
				}
			}
			require.Len(t, records, test.wantConsumed)
			require.Equal(t, test.wantConsumed, stats.Consumed)
			require.Equal(t, test.wantBatches, stats.Batches)
			require.Equal(t, want, records[0].Value)
			require.False(t, records[1].Found)
			require.Equal(t, len(want), stats.BufferedBytes)
			require.Equal(t, 3, stats.Fallbacks)
			value, err := store.writable.Get(keys[0])
			require.NoError(t, err)
			require.Equal(t, want, value)
			_, err = store.writable.Get(keys[2])
			require.ErrorIs(t, err, kvstore.ErrNotFound)
		})
	}
}

func TestPromotionCopyForwardDoesNotRetryConcurrentObservation(t *testing.T) {
	var armed atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	injector := errorfs.InjectorFunc(func(op errorfs.Op, path string) error {
		if filepath.Ext(path) == ".sst" && (op == errorfs.OpFileRead || op == errorfs.OpFileReadAt) && armed.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return nil
	})
	key := []byte("key")
	store := openPromotionPrefetchStore(t, injector)
	t.Cleanup(func() { closePromotionGate(release) })
	collision := promotionKeyOnStripe(mutationStripe(key), "copy-forward")
	require.NoError(t, store.archive.Put(collision, []byte("archive")))
	type result struct {
		records []kvstore.Promotion
		stats   kvstore.PromotionStats
		err     error
	}
	done := make(chan result, 1)
	armed.Store(true)
	go func() {
		records, stats, err := store.PromoteBatch([][]byte{key}, 1024)
		done <- result{records, stats, err}
	}()
	waitPromotionSignal(t, entered, "concurrent observation")
	copied := make(chan result, 1)
	go func() {
		records, stats, err := store.PromoteBatch([][]byte{collision}, 1024)
		copied <- result{records, stats, err}
	}()
	copyResult := waitPromotionResultValue(t, copied, "copy-forward")
	require.NoError(t, copyResult.err)
	require.Equal(t, 1, copyResult.stats.Promoted)
	closePromotionGate(release)
	observed := waitPromotionResultValue(t, done, "original promotion")
	require.NoError(t, observed.err)
	require.Len(t, observed.records, 1)
	require.Equal(t, []byte("old"), observed.records[0].Value)
	require.Zero(t, observed.stats.VersionMismatches)
	require.Zero(t, observed.stats.Retries)
	require.Zero(t, observed.stats.Fallbacks)
}

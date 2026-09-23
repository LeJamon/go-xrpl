package adaptor

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

// Model NodeStore's distinct cache-aware runtime and uncached durable readers.
type runtimeCacheTestFamily struct {
	*acquisitionStoreTestFamily
	runtimeMisses int
	durableReads  int
}

func (f *runtimeCacheTestFamily) Fetch(_ context.Context, hash [32]byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if data := f.cached[hash]; data != nil {
		return bytes.Clone(data), nil
	}
	f.runtimeMisses++
	data := bytes.Clone(f.data[hash])
	if data != nil {
		f.cached[hash] = bytes.Clone(data)
	}
	return data, nil
}

func (f *runtimeCacheTestFamily) FetchDurable(_ context.Context, hash [32]byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.durableReads++
	return bytes.Clone(f.data[hash]), nil
}

func TestAcquisitionPromotedRuntimeReadsUseNodeCache(t *testing.T) {
	base := &runtimeCacheTestFamily{acquisitionStoreTestFamily: newAcquisitionStoreTestFamily()}
	entry := acquisitionEntry(31)
	require.NoError(t, base.StoreBatch(t.Context(), []shamap.FlushEntry{entry}))
	base.clearCached()
	lane := newAcquisitionStoreLane(base, slog.Default(), 1)
	scope := lane.scope().(*acquisitionStoreScope)
	require.NoError(t, scope.Promote(t.Context()))
	for range 2 {
		data, err := scope.Fetch(t.Context(), entry.Hash)
		require.NoError(t, err)
		require.Equal(t, entry.Data, data)
	}
	require.Equal(t, 1, base.runtimeMisses, "promoted reads must warm and reuse NodeStore cache")
	require.Zero(t, base.durableReads)

	// A cache-only node must still not satisfy durable placement or a retired
	// scope. Runtime caching is not authority to certify storage completeness.
	cacheOnly := acquisitionEntry(32)
	base.cached[cacheOnly.Hash] = cacheOnly.Data
	data, err := scope.FetchForNodePlacement(t.Context(), cacheOnly.Hash)
	require.NoError(t, err)
	require.Nil(t, data)
	require.Equal(t, 1, base.durableReads)
	require.NoError(t, scope.Retire(t.Context()))
	data, err = scope.Fetch(t.Context(), cacheOnly.Hash)
	require.NoError(t, err)
	require.Nil(t, data)
	require.Equal(t, 2, base.durableReads)
}

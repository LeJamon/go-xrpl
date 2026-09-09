package service

import (
	"context"
	"fmt"
	"runtime"
	"runtime/pprof"
	"testing"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/stretchr/testify/require"
)

func BenchmarkService_RefreshValidatedState(b *testing.B) {
	for _, warm := range []bool{false, true} {
		for _, workers := range []int{1, 2, 4} {
			for _, batchNodes := range []int{0, 64, storedSHAMapPromotionBatchNodes} {
				b.Run(fmt.Sprintf("warm=%t/workers=%d/batch=%d", warm, workers, batchNodes), func(b *testing.B) {
					benchmarkRefreshValidatedState(b, warm, workers, batchNodes)
				})
			}
		}
	}
}

func benchmarkRefreshValidatedState(b *testing.B, warm bool, workers, batchNodes int) {
	cacheBytes := int64(256 << 10)
	if warm {
		cacheBytes = 8 << 20
	}
	fixture := newBenchmarkRefreshFixture(b, 16_384, cacheBytes)
	var fetches, batches, maxBytes int64
	var hits, misses int64
	var materializedBytes uint64
	var io kvstore.IOMetrics
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		if warm {
			require.NoError(b, fixture.svc.walkStoredSHAMap(b.Context(), fixture.root, shamap.TypeState, nil))
		}
		runtime.GC()
		cacheBefore := fixture.base.PromotionCacheMetrics()
		ioBefore := fixture.base.PromotionIOMetrics()
		b.StartTimer()
		var err error
		pprof.Do(b.Context(), pprof.Labels("phase", "refresh"), func(ctx context.Context) {
			err = fixture.svc.refreshGenerationStateWithBatch(
				ctx, fixture.root, fixture.seq, fixture.db, nil,
				workers, batchNodes, storedSHAMapPromotionBatchBytes,
			)
		})
		b.StopTimer()
		require.NoError(b, err)
		cacheAfter := fixture.base.PromotionCacheMetrics()
		hits += cacheAfter.Hits - cacheBefore.Hits
		misses += cacheAfter.Misses - cacheBefore.Misses
		addBenchmarkIOMetrics(&io, benchmarkIOMetricsDelta(fixture.base.PromotionIOMetrics(), ioBefore))
		fetches += fixture.db.promotionFetches.Load()
		batches += fixture.db.promotionBatches.Load()
		maxBytes = max(maxBytes, fixture.db.maxPromotionBytes.Load())
		fixture.rotateAndReopen(b)
		materializedBytes += fixture.base.PromotionIOMetrics().SSTableBytes
	}
	if warm {
		require.Positive(b, hits, "prewarmed traversal must use the block cache")
	} else {
		require.Positive(b, misses, "cold SSTable reads must reach the block cache")
	}
	b.ReportMetric(float64(fetches)/b.Elapsed().Seconds(), "nodes/s")
	b.ReportMetric(float64(hits)/float64(fetches), "cache-hits/node")
	b.ReportMetric(float64(misses)/float64(fetches), "cache-misses/node")
	b.ReportMetric(float64(io.WALBytesWritten)/float64(b.N), "wal-bytes/op")
	b.ReportMetric(float64(materializedBytes)/float64(b.N), "materialized-sst-bytes/op")
	b.ReportMetric(float64(io.FlushBytesWritten)/float64(b.N), "flush-bytes/op")
	b.ReportMetric(float64(io.CompactionBytesRead)/float64(b.N), "compaction-read-bytes/op")
	b.ReportMetric(float64(io.CompactionBytesWritten)/float64(b.N), "compaction-write-bytes/op")
	if io.LogicalBytesWritten > 0 {
		b.ReportMetric(float64(io.WALBytesWritten)/float64(io.LogicalBytesWritten), "wal-amp")
	}
	if batches > 0 {
		b.ReportMetric(float64(batches)/float64(b.N), "batches/op")
		b.ReportMetric(float64(fetches)/float64(batches), "nodes/batch")
		b.ReportMetric(float64(maxBytes), "max-batch-bytes")
	}
}

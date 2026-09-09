package service

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

const (
	benchmarkVerificationLeaves  = 16_384
	benchmarkVerificationWorkers = 4
)

func BenchmarkService_VerifyStoredSHAMapBatch(b *testing.B) {
	for _, warm := range []bool{false, true} {
		for _, batchNodes := range []int{0, 32, 128, 256, 1024} {
			name := "scalar"
			if batchNodes > 0 {
				name = fmt.Sprintf("batch=%d", batchNodes)
			}
			b.Run(fmt.Sprintf("warm=%t/%s", warm, name), func(b *testing.B) {
				benchmarkStoredSHAMapBatch(b, warm, batchNodes)
			})
		}
	}
}

func benchmarkStoredSHAMapBatch(b *testing.B, warm bool, batchNodes int) {
	cacheBytes := int64(256 << 10)
	if warm {
		cacheBytes = 8 << 20
	}
	fixture := newBenchmarkRefreshFixture(b, benchmarkVerificationLeaves, cacheBytes)
	fixture.svc.nodeStore = fixture.base
	b.Logf(
		"fixture leaves=%d root=%x workers=%d mode=%s batch-bytes=%d cache-bytes=%d reopened-persisted=true",
		benchmarkVerificationLeaves,
		fixture.root,
		benchmarkVerificationWorkers,
		benchmarkVerificationMode(batchNodes),
		storedSHAMapVerificationBatchBytes,
		cacheBytes,
	)

	var totalNodes uint64
	var totalLogicalBytes uint64
	var cacheHits, cacheMisses int64
	var nodesPerOp uint64
	var logicalBytesPerOp uint64
	var readsPerOp uint64

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		if fixture.db != nil {
			fixture.close(b)
		}
		fixture.open(b)
		fixture.svc.nodeStore = fixture.base
		fetch := fixture.svc.storedSHAMapVerificationFetch()
		batchFetch := benchmarkBatchFetch(fixture, batchNodes)
		if warm {
			_, err := benchmarkWalkStoredSHAMap(
				b.Context(), fixture, fetch, batchFetch, batchNodes,
			)
			if err != nil {
				b.Fatalf("warm-up verification: %v", err)
			}
		}
		runtime.GC()
		statsBefore := fixture.base.Stats()
		cacheBefore := fixture.base.PromotionCacheMetrics()
		b.StartTimer()
		nodes, err := benchmarkWalkStoredSHAMap(b.Context(), fixture, fetch, batchFetch, batchNodes)
		b.StopTimer()
		if err != nil {
			b.Fatalf("verification: %v", err)
		}
		statsAfter := fixture.base.Stats()
		cacheAfter := fixture.base.PromotionCacheMetrics()
		logicalBytes := statsAfter.ReadBytes - statsBefore.ReadBytes
		reads := statsAfter.Reads - statsBefore.Reads
		if nodesPerOp == 0 {
			nodesPerOp = nodes
			logicalBytesPerOp = logicalBytes
			readsPerOp = reads
		}
		if nodes != nodesPerOp || logicalBytes != logicalBytesPerOp || reads != readsPerOp {
			b.Fatalf(
				"verification changed between iterations: nodes=%d/%d logical-bytes=%d/%d reads=%d/%d",
				nodes,
				nodesPerOp,
				logicalBytes,
				logicalBytesPerOp,
				reads,
				readsPerOp,
			)
		}
		totalNodes += nodes
		totalLogicalBytes += logicalBytes
		cacheHits += cacheAfter.Hits - cacheBefore.Hits
		cacheMisses += cacheAfter.Misses - cacheBefore.Misses
	}
	if totalNodes == 0 {
		b.Fatal("verification visited no nodes")
	}
	b.ReportMetric(float64(totalNodes)/float64(b.N), "nodes/op")
	b.ReportMetric(float64(totalLogicalBytes)/float64(b.N), "logical-bytes/op")
	b.ReportMetric(float64(cacheHits)/float64(b.N), "cache-hits/op")
	b.ReportMetric(float64(cacheMisses)/float64(b.N), "cache-misses/op")
	if elapsed := b.Elapsed().Seconds(); elapsed > 0 {
		b.ReportMetric(float64(totalNodes)/elapsed, "nodes/s")
	}
}

func benchmarkVerificationMode(batchNodes int) string {
	if batchNodes <= 0 {
		return "scalar"
	}
	return fmt.Sprintf("batch=%d", batchNodes)
}

func benchmarkBatchFetch(
	fixture *benchmarkRefreshFixture,
	batchNodes int,
) storedSHAMapBatchFetch {
	if batchNodes <= 0 {
		return nil
	}
	return func(
		ctx context.Context,
		hashes []nodestore.Hash256,
		maxBytes int,
	) ([]*nodestore.Node, kvstore.PromotionStats, error) {
		nodes, err := fixture.base.FetchBatchUncached(ctx, hashes, batchNodes, maxBytes)
		return nodes, kvstore.PromotionStats{}, err
	}
}

func benchmarkWalkStoredSHAMap(
	ctx context.Context,
	fixture *benchmarkRefreshFixture,
	fetch storedSHAMapFetch,
	batchFetch storedSHAMapBatchFetch,
	batchNodes int,
) (uint64, error) {
	progress := newStoredSHAMapVerificationProgress(
		fixture.svc.logger,
		fixture.svc.nodeStore,
		fixture.root,
		shamap.TypeState,
		time.Now(),
	)
	err := fixture.svc.walkStoredSHAMapConcurrentWithFetch(
		ctx,
		fixture.root,
		shamap.TypeState,
		fetch,
		benchmarkVerificationWorkers,
		storedSHAMapWalkControl{
			progress:   progress,
			batchFetch: batchFetch,
			batchNodes: batchNodes,
			batchBytes: storedSHAMapVerificationBatchBytes,
		},
		nil,
	)
	return progress.nodesChecked.Load(), err
}

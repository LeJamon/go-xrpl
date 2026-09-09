package pebble

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	cockroachpebble "github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
)

const (
	promotionBenchmarkKeys     = 1024
	promotionBenchmarkBatch    = 256
	promotionBenchmarkValueLen = 1024
	promotionBenchmarkCache    = 8 << 20
	promotionBenchmarkPutCount = 64
	promotionBenchmarkMaxBytes = 128 << 10
	promotionBenchmarkDelay    = 50 * time.Microsecond
	promotionBenchmarkOverlap  = 5 * time.Second
)

type promotionReadCounters struct {
	calls atomic.Uint64
	bytes atomic.Uint64
}

type promotionReadSnapshot struct {
	calls uint64
	bytes uint64
}

func (c *promotionReadCounters) snapshot() promotionReadSnapshot {
	return promotionReadSnapshot{
		calls: c.calls.Load(),
		bytes: c.bytes.Load(),
	}
}

func (c *promotionReadCounters) reset() {
	c.calls.Store(0)
	c.bytes.Store(0)
}

// promotionReadFS counts successful reads of persisted SSTables by generation.
// The counters are armed only after Pebble's asynchronous table-stat scan and
// any warm-up traversal, keeping setup work outside the timed operation.
type promotionReadFS struct {
	vfs.FS
	writablePath    string
	archivePath     string
	writable        promotionReadCounters
	archive         promotionReadCounters
	delay           time.Duration
	armed           atomic.Bool
	readStarted     chan struct{}
	readOnce        sync.Once
	foregroundReady chan struct{}
}

func (f *promotionReadFS) Open(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	file, err := f.FS.Open(name, opts...)
	if err != nil {
		return nil, err
	}
	return f.wrapFile(name, file), nil
}

func (f *promotionReadFS) OpenReadWrite(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	file, err := f.FS.OpenReadWrite(name, opts...)
	if err != nil {
		return nil, err
	}
	return f.wrapFile(name, file), nil
}

func (f *promotionReadFS) wrapFile(name string, file vfs.File) vfs.File {
	path := filepath.Clean(name)
	var counters *promotionReadCounters
	switch {
	case path == f.writablePath || strings.HasPrefix(path, f.writablePath+string(os.PathSeparator)):
		counters = &f.writable
	case path == f.archivePath || strings.HasPrefix(path, f.archivePath+string(os.PathSeparator)):
		counters = &f.archive
	}
	return &promotionReadFile{
		File:     file,
		owner:    f,
		counters: counters,
		sstable:  filepath.Ext(path) == ".sst",
		armed:    &f.armed,
		delay:    f.delay,
	}
}

type promotionReadFile struct {
	vfs.File
	owner    *promotionReadFS
	counters *promotionReadCounters
	armed    *atomic.Bool
	sstable  bool
	delay    time.Duration
}

func (f *promotionReadFile) Read(p []byte) (int, error) {
	f.beforeRead()
	n, err := f.File.Read(p)
	f.afterRead(n)
	return n, err
}

func (f *promotionReadFile) ReadAt(p []byte, offset int64) (int, error) {
	f.beforeRead()
	n, err := f.File.ReadAt(p, offset)
	f.afterRead(n)
	return n, err
}

func (f *promotionReadFile) beforeRead() {
	if f.sstable && f.counters != nil && f.armed.Load() {
		f.owner.readOnce.Do(func() {
			close(f.owner.readStarted)
			if f.owner.foregroundReady != nil {
				<-f.owner.foregroundReady
			}
		})
		if f.delay > 0 {
			time.Sleep(f.delay)
		}
	}
}

func (f *promotionReadFile) afterRead(n int) {
	if n > 0 && f.sstable && f.counters != nil && f.armed.Load() {
		f.counters.calls.Add(1)
		f.counters.bytes.Add(uint64(n))
	}
}

func (f *promotionReadFS) reset() {
	f.writable.reset()
	f.archive.reset()
}

func (f *promotionReadFS) snapshot() (promotionReadSnapshot, promotionReadSnapshot) {
	return f.writable.snapshot(), f.archive.snapshot()
}

type promotionReadFixture struct {
	root     string
	path     string
	keys     [][]byte
	hitCount int
	options  Options
}

func BenchmarkPromotionReadAmplification(b *testing.B) {
	for _, writableHits := range []int{0, 50, 100} {
		for _, warm := range []bool{false, true} {
			for _, delayed := range []bool{false, true} {
				for _, workers := range []int{1, 2, 4} {
					name := fmt.Sprintf(
						"writable-hits=%d/warm=%t/delayed=%t/workers=%d",
						writableHits,
						warm,
						delayed,
						workers,
					)
					b.Run(name, func(b *testing.B) {
						benchmarkPromotionReadAmplification(b, writableHits, warm, delayed, workers)
					})
				}
			}
		}
	}
}

func benchmarkPromotionReadAmplification(
	b *testing.B,
	writableHits int,
	warm, delayed bool,
	workers int,
) {
	b.Helper()
	template := newPromotionReadFixture(b, writableHits)
	expected := makePromotionExpectations(template.keys, template.hitCount)
	puts := make([]kvstore.Promotion, promotionBenchmarkPutCount)
	for index := range puts {
		puts[index] = kvstore.Promotion{Key: promotionBenchmarkPutKey(index), Value: promotionBenchmarkPutValue(index)}
	}
	var (
		refreshWall      time.Duration
		workerBusy       time.Duration
		writableRead     promotionReadSnapshot
		archiveRead      promotionReadSnapshot
		putLatencies     = make([]time.Duration, 0, b.N*promotionBenchmarkPutCount)
		putSamples       int
		cacheHits        int64
		cacheMisses      int64
		consumed         int
		promoted         int
		writableHitCount int
		archiveHitCount  int
	)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		fixture := clonePromotionReadFixture(b, template)
		store, reads, err := openMeasuredPromotionStore(fixture.path, fixture.options, delayed)
		if err != nil {
			b.Fatal(err)
		}
		if warm {
			if err := warmPromotionStore(store, fixture.keys); err != nil {
				reads.armed.Store(false)
				_ = store.Close()
				b.Fatal(err)
			}
		}
		runtime.GC()
		cacheBefore := store.CacheMetrics()
		reads.reset()
		reads.armed.Store(true)
		b.StartTimer()
		var run promotionRunResult
		pprof.Do(context.Background(), pprof.Labels("phase", "promotion"), func(context.Context) {
			run = runPromotionWorkers(
				store,
				fixture.keys,
				expected,
				puts,
				workers,
				reads,
				!warm,
			)
		})
		b.StopTimer()
		reads.armed.Store(false)
		writable, archive := reads.snapshot()
		cacheAfter := store.CacheMetrics()
		closeErr := store.Close()
		if run.err != nil {
			b.Fatal(run.err)
		}
		if closeErr != nil {
			b.Fatal(closeErr)
		}
		expectedArchiveHits := len(fixture.keys) - fixture.hitCount
		if run.consumed != len(fixture.keys) ||
			run.writableHits != fixture.hitCount ||
			run.archiveHits != expectedArchiveHits ||
			run.promoted != expectedArchiveHits {
			b.Fatalf(
				"promotion distribution = consumed %d, writable hits %d, archive hits %d, promoted %d; want %d, %d, %d, %d",
				run.consumed,
				run.writableHits,
				run.archiveHits,
				run.promoted,
				len(fixture.keys),
				fixture.hitCount,
				expectedArchiveHits,
				expectedArchiveHits,
			)
		}
		if !warm && !run.readStarted {
			b.Fatalf("cold promotion completed without an SST read start")
		}
		readCalls := writable.calls + archive.calls
		if !warm && readCalls == 0 {
			b.Fatalf("cold promotion completed without SST reads")
		}
		cacheHitDelta := cacheAfter.Hits - cacheBefore.Hits
		cacheMissDelta := cacheAfter.Misses - cacheBefore.Misses
		if warm && (cacheHitDelta == 0 || cacheMissDelta != 0 || readCalls != 0) {
			b.Fatalf("warm promotion must hit cache without SST reads: hits=%d misses=%d reads=%d", cacheHitDelta, cacheMissDelta, readCalls)
		}
		if !warm && cacheMissDelta == 0 {
			b.Fatalf("cold promotion completed without block-cache misses")
		}
		refreshWall += run.wall
		workerBusy += run.busy
		writableRead.calls += writable.calls
		writableRead.bytes += writable.bytes
		archiveRead.calls += archive.calls
		archiveRead.bytes += archive.bytes
		putLatencies = append(putLatencies, run.putLatencies...)
		putSamples += len(run.putLatencies)
		cacheHits += cacheHitDelta
		cacheMisses += cacheMissDelta
		consumed += run.consumed
		promoted += run.promoted
		writableHitCount += run.writableHits
		archiveHitCount += run.archiveHits
	}

	perOp := float64(b.N)
	b.ReportMetric(float64(refreshWall)/perOp/float64(time.Millisecond), "refresh-ms/op")
	b.ReportMetric(float64(writableRead.calls)/perOp, "writable-sst-reads/op")
	b.ReportMetric(float64(archiveRead.calls)/perOp, "archive-sst-reads/op")
	b.ReportMetric(float64(writableRead.calls+archiveRead.calls)/perOp, "sst-reads/op")
	b.ReportMetric(float64(writableRead.bytes)/perOp, "writable-sst-bytes/op")
	b.ReportMetric(float64(archiveRead.bytes)/perOp, "archive-sst-bytes/op")
	b.ReportMetric(float64(writableRead.bytes+archiveRead.bytes)/perOp, "sst-bytes/op")
	b.ReportMetric(float64(cacheHits)/perOp, "block-cache-hits/op")
	b.ReportMetric(float64(cacheMisses)/perOp, "block-cache-misses/op")
	b.ReportMetric(float64(consumed)/perOp, "consumed/op")
	b.ReportMetric(float64(promoted)/perOp, "promoted/op")
	b.ReportMetric(float64(writableHitCount)/perOp, "writable-hits/op")
	b.ReportMetric(float64(archiveHitCount)/perOp, "archive-hits/op")
	workerUtilization := 0.0
	if refreshWall > 0 {
		workerUtilization = float64(workerBusy) / float64(refreshWall) / float64(workers)
	}
	b.ReportMetric(workerUtilization, "worker-utilization")
	b.ReportMetric(float64(putSamples)/perOp, "overlap-put-samples/op")
	b.ReportMetric(percentileNanos(putLatencies, 0.50), "overlap-put-p50-ns")
	b.ReportMetric(percentileNanos(putLatencies, 0.95), "overlap-put-p95-ns")
	b.ReportMetric(percentileNanos(putLatencies, 0.99), "overlap-put-p99-ns")
}

func newPromotionReadFixture(b *testing.B, writableHits int) promotionReadFixture {
	b.Helper()
	options := Options{
		BlockCacheBytes: promotionBenchmarkCache,
		MaxOpenFiles:    200,
	}
	root := b.TempDir()
	fixture := promotionReadFixture{
		root:    root,
		path:    filepath.Join(root, "nodes"),
		options: options,
		keys:    makePromotionBenchmarkKeys(promotionBenchmarkKeys),
	}
	fixture.hitCount = len(fixture.keys) * writableHits / 100
	store, err := NewRotating(fixture.path, options)
	if err != nil {
		b.Fatal(err)
	}
	batch, err := store.NewBatch()
	if err != nil {
		_ = store.Close()
		b.Fatal(err)
	}
	for index, key := range fixture.keys {
		if err := batch.Put(key, promotionBenchmarkValue(index, 0)); err != nil {
			_ = batch.Close()
			_ = store.Close()
			b.Fatal(err)
		}
	}
	if err := batch.Write(); err != nil {
		_ = batch.Close()
		_ = store.Close()
		b.Fatal(err)
	}
	if err := batch.Close(); err != nil {
		_ = store.Close()
		b.Fatal(err)
	}
	if err := store.Sync(); err != nil {
		_ = store.Close()
		b.Fatal(err)
	}
	committed, err := store.Rotate(1, 1)
	if err != nil || !committed {
		_ = store.Close()
		if err == nil {
			err = fmt.Errorf("initial fixture rotation did not commit")
		}
		b.Fatal(err)
	}
	batch, err = store.NewBatch()
	if err != nil {
		_ = store.Close()
		b.Fatal(err)
	}
	for index := 0; index < fixture.hitCount; index++ {
		if err := batch.Put(fixture.keys[index], promotionBenchmarkValue(index, 1)); err != nil {
			_ = batch.Close()
			_ = store.Close()
			b.Fatal(err)
		}
	}
	if err := batch.Write(); err != nil {
		_ = batch.Close()
		_ = store.Close()
		b.Fatal(err)
	}
	if err := batch.Close(); err != nil {
		_ = store.Close()
		b.Fatal(err)
	}
	if err := store.Sync(); err != nil {
		_ = store.Close()
		b.Fatal(err)
	}
	if err := store.writable.db.Flush(); err != nil {
		_ = store.Close()
		b.Fatal(err)
	}
	if err := store.archive.db.Flush(); err != nil {
		_ = store.Close()
		b.Fatal(err)
	}
	if err := store.Close(); err != nil {
		b.Fatal(err)
	}
	return fixture
}

func clonePromotionReadFixture(b *testing.B, source promotionReadFixture) promotionReadFixture {
	b.Helper()
	root := b.TempDir()
	if err := os.CopyFS(root, os.DirFS(source.root)); err != nil {
		b.Fatal(err)
	}
	clone := source
	clone.root = root
	clone.path = filepath.Join(root, filepath.Base(source.path))
	return clone
}

func makePromotionBenchmarkKeys(count int) [][]byte {
	keys := make([][]byte, count)
	for index := range keys {
		var input [16]byte
		binary.BigEndian.PutUint64(input[:8], uint64(index))
		binary.BigEndian.PutUint64(input[8:], 0x70726f6d6f74696f)
		hash := sha256.Sum256(input[:])
		keys[index] = append([]byte(nil), hash[:]...)
	}
	return keys
}

func promotionBenchmarkValue(index, generation int) []byte {
	value := make([]byte, promotionBenchmarkValueLen)
	for offset := 0; offset < len(value); offset += sha256.Size {
		var input [24]byte
		binary.BigEndian.PutUint64(input[:8], uint64(index))
		binary.BigEndian.PutUint64(input[8:16], uint64(generation))
		binary.BigEndian.PutUint64(input[16:], uint64(offset))
		hash := sha256.Sum256(input[:])
		copy(value[offset:], hash[:])
	}
	return value
}

func openMeasuredPromotionStore(
	path string,
	options Options,
	delayed bool,
) (*RotatingStore, *promotionReadFS, error) {
	resolved, perGeneration, err := resolveRotatingOptions(options)
	if err != nil {
		return nil, nil, err
	}
	store, found, err := prepareRotatingStore(path, perGeneration)
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, nil, fmt.Errorf("promotion benchmark fixture has no rotation manifest")
	}
	reads := &promotionReadFS{
		FS:           vfs.Default,
		writablePath: store.writablePath,
		archivePath:  store.archivePath,
		readStarted:  make(chan struct{}),
	}
	if delayed {
		reads.delay = promotionBenchmarkDelay
	}
	loaded := make(chan struct{}, 2)
	openGeneration := func(path string, cache *cockroachpebble.Cache, options Options) (*Store, error) {
		pebbleOptions := makePebbleOptions(options, cache)
		pebbleOptions.FS = reads
		pebbleOptions.DisableAutomaticCompactions = true
		pebbleOptions.EventListener = &cockroachpebble.EventListener{
			TableStatsLoaded: func(cockroachpebble.TableStatsInfo) { loaded <- struct{}{} },
		}
		db, err := cockroachpebble.Open(path, pebbleOptions)
		if err != nil {
			return nil, err
		}
		return &Store{db: db}, nil
	}
	if err := store.openGenerations(resolved.BlockCacheBytes, true, openGeneration); err != nil {
		return nil, nil, err
	}
	for range 2 {
		select {
		case <-loaded:
		case <-time.After(10 * time.Second):
			_ = store.Close()
			return nil, nil, fmt.Errorf("timed out waiting for Pebble table stats")
		}
	}
	return store, reads, nil
}

func warmPromotionStore(store *RotatingStore, keys [][]byte) error {
	sorted := append([][]byte(nil), keys...)
	sort.Slice(sorted, func(i, j int) bool { return string(sorted[i]) < string(sorted[j]) })
	for _, generation := range []*Store{store.writable, store.archive} {
		iterator, err := generation.newPointIterator()
		if err != nil {
			return err
		}
		for _, key := range sorted {
			if _, _, _, err := iterator.get(key, int(^uint(0)>>1), true); err != nil {
				_ = iterator.Close()
				return err
			}
		}
		if err := iterator.Close(); err != nil {
			return err
		}
	}
	return nil
}

type promotionRunResult struct {
	err          error
	wall         time.Duration
	busy         time.Duration
	putLatencies []time.Duration
	readStarted  bool
	consumed     int
	promoted     int
	writableHits int
	archiveHits  int
}

type expectedPromotion struct {
	value       []byte
	writableHit bool
}

type promotionBatchResult struct {
	consumed     int
	promoted     int
	writableHits int
	archiveHits  int
}

func runPromotionWorkers(
	store *RotatingStore,
	keys [][]byte,
	expected map[string]expectedPromotion,
	puts []kvstore.Promotion,
	workers int,
	reads *promotionReadFS,
	waitForRead bool,
) promotionRunResult {
	batches := make([][][]byte, 0, (len(keys)+promotionBenchmarkBatch-1)/promotionBenchmarkBatch)
	for start := 0; start < len(keys); start += promotionBenchmarkBatch {
		end := min(start+promotionBenchmarkBatch, len(keys))
		batches = append(batches, keys[start:end])
	}
	start := make(chan struct{})
	refreshDone := make(chan struct{})
	errCh := make(chan error, workers+1)
	var group sync.WaitGroup
	var busy atomic.Int64
	var consumed atomic.Int64
	var promoted atomic.Int64
	var writableHits atomic.Int64
	var archiveHits atomic.Int64
	var readOverlap atomic.Bool
	foregroundReady := make(chan struct{})
	if waitForRead {
		reads.foregroundReady = foregroundReady
	} else {
		reads.foregroundReady = nil
	}
	group.Add(workers)
	for worker := range workers {
		go func(worker int) {
			defer group.Done()
			<-start
			started := time.Now()
			for batchIndex := worker; batchIndex < len(batches); batchIndex += workers {
				batchResult, err := promotePromotionKeys(
					store,
					batches[batchIndex],
					expected,
					promotionBenchmarkMaxBytes,
				)
				if err != nil {
					errCh <- err
					return
				}
				consumed.Add(int64(batchResult.consumed))
				promoted.Add(int64(batchResult.promoted))
				writableHits.Add(int64(batchResult.writableHits))
				archiveHits.Add(int64(batchResult.archiveHits))
			}
			busy.Add(int64(time.Since(started)))
		}(worker)
	}
	putLatencies := make(chan []time.Duration, 1)
	go func() {
		<-start
		if waitForRead {
			select {
			case <-reads.readStarted:
				readOverlap.Store(true)
			case <-time.After(promotionBenchmarkOverlap):
				errCh <- fmt.Errorf("timed out waiting for an SST read to overlap foreground puts")
				close(foregroundReady)
				putLatencies <- nil
				return
			}
		}
		latencies := make([]time.Duration, 0, promotionBenchmarkPutCount)
		for index, put := range puts {
			if index > 0 || !waitForRead {
				select {
				case <-refreshDone:
					putLatencies <- latencies
					return
				default:
				}
			}
			started := time.Now()
			if index == 0 && waitForRead {
				close(foregroundReady)
			}
			if err := store.Put(put.Key, put.Value); err != nil {
				errCh <- err
				putLatencies <- latencies
				return
			}
			latencies = append(latencies, time.Since(started))
		}
		putLatencies <- latencies
	}()

	started := time.Now()
	close(start)
	group.Wait()
	wall := time.Since(started)
	close(refreshDone)
	latencies := <-putLatencies
	result := promotionRunResult{
		wall:         wall,
		busy:         time.Duration(busy.Load()),
		putLatencies: latencies,
		readStarted:  readOverlap.Load(),
		consumed:     int(consumed.Load()),
		promoted:     int(promoted.Load()),
		writableHits: int(writableHits.Load()),
		archiveHits:  int(archiveHits.Load()),
	}
	select {
	case result.err = <-errCh:
	default:
	}
	return result
}

func makePromotionExpectations(keys [][]byte, hitCount int) map[string]expectedPromotion {
	expected := make(map[string]expectedPromotion, len(keys))
	for index, key := range keys {
		generation := 0
		if index < hitCount {
			generation = 1
		}
		expected[string(key)] = expectedPromotion{
			value:       promotionBenchmarkValue(index, generation),
			writableHit: index < hitCount,
		}
	}
	return expected
}

func promotePromotionKeys(
	store *RotatingStore,
	keys [][]byte,
	expected map[string]expectedPromotion,
	maxBytes int,
) (promotionBatchResult, error) {
	sorted := append([][]byte(nil), keys...)
	sort.Slice(sorted, func(i, j int) bool { return bytes.Compare(sorted[i], sorted[j]) < 0 })
	var result promotionBatchResult
	for offset := 0; offset < len(sorted); {
		remaining := sorted[offset:]
		promotions, stats, err := store.PromoteBatch(remaining, maxBytes)
		if err != nil {
			return result, err
		}
		if len(promotions) == 0 || stats.Consumed != len(promotions) || len(promotions) > len(remaining) {
			return result, fmt.Errorf(
				"promotion consumed %d records for %d returned records from %d keys",
				stats.Consumed,
				len(promotions),
				len(remaining),
			)
		}
		var expectedStats kvstore.PromotionStats
		expectedStats.Requested = len(remaining)
		expectedStats.Consumed = len(promotions)
		for index, promotion := range promotions {
			key := remaining[index]
			if !bytes.Equal(promotion.Key, key) {
				return result, fmt.Errorf("promotion key at index %d is out of order", index)
			}
			expectedPromotion, ok := expected[string(key)]
			if !ok || !promotion.Found || !bytes.Equal(promotion.Value, expectedPromotion.value) {
				return result, fmt.Errorf("promotion value mismatch for key %x", key)
			}
			valueSize := len(expectedPromotion.value)
			expectedStats.BufferedBytes += valueSize
			if expectedPromotion.writableHit {
				expectedStats.WritableHits++
			} else {
				expectedStats.WritableMisses++
				expectedStats.ArchiveHits++
				expectedStats.Promoted++
				expectedStats.PromotedBytes += valueSize
			}
		}
		if expectedStats.Promoted > 0 {
			if stats.Batches < 1 || stats.Batches > expectedStats.Promoted ||
				(stats.Retries < promotionRetryLimit && stats.Batches != 1) {
				return result, fmt.Errorf("promotion writes = %d for %d promoted records after %d retries", stats.Batches, expectedStats.Promoted, stats.Retries)
			}
			expectedStats.Batches = stats.Batches
		}
		observed := kvstore.PromotionStats{
			Requested: stats.Requested, Consumed: stats.Consumed,
			WritableHits: stats.WritableHits, WritableMisses: stats.WritableMisses,
			ArchiveHits: stats.ArchiveHits, ArchiveMisses: stats.ArchiveMisses,
			Promoted: stats.Promoted, PromotedBytes: stats.PromotedBytes,
			BufferedBytes: stats.BufferedBytes, Batches: stats.Batches,
		}
		if observed != expectedStats {
			return result, fmt.Errorf("promotion stats = %+v, want %+v", observed, expectedStats)
		}
		result.consumed += stats.Consumed
		result.promoted += stats.Promoted
		result.writableHits += stats.WritableHits
		result.archiveHits += stats.ArchiveHits
		offset += len(promotions)
	}
	return result, nil
}

func promotionBenchmarkPutKey(index int) []byte {
	var input [16]byte
	binary.BigEndian.PutUint64(input[:8], uint64(index))
	binary.BigEndian.PutUint64(input[8:], 0x7075742d6f766572)
	hash := sha256.Sum256(input[:])
	return hash[:]
}

func promotionBenchmarkPutValue(index int) []byte {
	return promotionBenchmarkValue(index+promotionBenchmarkKeys, 2)[:64]
}

func percentileNanos(values []time.Duration, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(math.Ceil(float64(len(sorted))*percentile)) - 1
	index = max(index, 0)
	index = min(index, len(sorted)-1)
	return float64(sorted[index].Nanoseconds())
}

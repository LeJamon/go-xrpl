package pebble

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

func BenchmarkPromotionContention(b *testing.B) {
	for _, cacheBytes := range []int64{1 << 20, 64 << 20} {
		for _, mode := range []string{"none", "paced", "saturated"} {
			b.Run(fmt.Sprintf("cache_%dMiB/writer_%s", cacheBytes>>20, mode), func(b *testing.B) {
				benchmarkPromotionContention(b, cacheBytes, mode)
			})
		}
	}
}

func benchmarkPromotionContention(b *testing.B, cacheBytes int64, mode string) {
	const keyCount = 4096
	const batchSize = 256
	store, err := NewRotating(filepath.Join(b.TempDir(), "nodes"), Options{BlockCacheBytes: cacheBytes, MaxOpenFiles: 200})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := store.Close(); err != nil {
			b.Error(err)
		}
	})
	keys := make([][]byte, keyCount)
	value := make([]byte, 4096)
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range value {
		value[i] = byte(rng.Uint32())
	}
	for i := range keys {
		var input [8]byte
		binary.BigEndian.PutUint64(input[:], uint64(i))
		hash := sha256.Sum256(input[:])
		keys[i] = append([]byte(nil), hash[:]...)
		if err := store.Put(keys[i], value); err != nil {
			b.Fatal(err)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
	if err := store.writable.db.Flush(); err != nil {
		b.Fatal(err)
	}
	// Populate the block cache up to its configured capacity before timing.
	for _, key := range keys {
		if _, err := store.Get(key); err != nil {
			b.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	failures := make(chan error, 4)
	latencies := make([]time.Duration, 0, 65536)
	if mode != "none" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var ticks <-chan time.Time
			if mode == "paced" {
				ticker := time.NewTicker(100 * time.Microsecond)
				defer ticker.Stop()
				ticks = ticker.C
			}
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if ticks != nil {
					select {
					case <-stop:
						return
					case <-ticks:
					}
				}
				// Distinct foreground keys collide with the selected promotion stripes.
				key := append([]byte("foreground/"), keys[i%keyCount]...)
				started := time.Now()
				err := store.Put(key, value[:64])
				elapsed := time.Since(started)
				if err != nil {
					failures <- err
					return
				}
				latencies = append(latencies, elapsed)
			}
		}()
	}
	var totals kvstore.PromotionStats
	var totalsMu sync.Mutex
	var calls int
	cacheBefore := store.CacheMetrics()
	b.ResetTimer()
	var promotionWorkers sync.WaitGroup
	for worker := 0; worker < 3; worker++ {
		promotionWorkers.Add(1)
		go func(worker int) {
			defer promotionWorkers.Done()
			for iteration := worker; iteration < b.N; iteration += 3 {
				offset := (iteration % (keyCount / batchSize)) * batchSize
				for consumed := 0; consumed < batchSize; {
					promotions, stats, err := store.PromoteBatch(keys[offset+consumed:offset+batchSize], 4<<20)
					if err != nil {
						failures <- err
						return
					}
					if len(promotions) == 0 || len(promotions) > batchSize-consumed || stats.Consumed != len(promotions) {
						failures <- fmt.Errorf("invalid prefix of %d for %d remaining keys", len(promotions), batchSize-consumed)
						return
					}
					for index, promotion := range promotions {
						if !promotion.Found || !bytes.Equal(promotion.Key, keys[offset+consumed+index]) || !bytes.Equal(promotion.Value, value) {
							failures <- fmt.Errorf("incorrect promotion at index %d", offset+consumed+index)
							return
						}
					}
					consumed += len(promotions)
					totalsMu.Lock()
					calls++
					totals.Consumed += stats.Consumed
					totals.VersionMismatches += stats.VersionMismatches
					totals.Retries += stats.Retries
					totals.Fallbacks += stats.Fallbacks
					totalsMu.Unlock()
				}
			}
		}(worker)
	}
	promotionWorkers.Wait()
	b.StopTimer()
	close(stop)
	wg.Wait()
	close(failures)
	for err := range failures {
		b.Fatal(err)
	}
	cacheAfter := store.CacheMetrics()
	b.ReportMetric(float64(cacheAfter.Misses-cacheBefore.Misses)/float64(b.N), "cache-misses/batch")
	b.ReportMetric(float64(totals.VersionMismatches)/float64(totals.Consumed), "mismatches/key")
	b.ReportMetric(float64(totals.Retries)/float64(calls), "retries/call")
	b.ReportMetric(float64(totals.Fallbacks)/float64(totals.Consumed), "fallbacks/key")
	b.ReportMetric(float64(calls)/float64(b.N), "calls/group")
	b.ReportMetric(float64(totals.Consumed)/float64(b.N), "keys/batch")
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		b.ReportMetric(float64(latencies[(len(latencies)-1)*95/100].Nanoseconds()), "put-p95-ns")
		b.ReportMetric(float64(latencies[(len(latencies)-1)*99/100].Nanoseconds()), "put-p99-ns")
		b.ReportMetric(float64(len(latencies)), "puts")
	}
}

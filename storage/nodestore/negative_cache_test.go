package nodestore

import (
	"encoding/binary"
	"sync"
	"testing"
	"time"
)

func negativeTestHash(value uint64) Hash256 {
	var hash Hash256
	binary.BigEndian.PutUint64(hash[:8], value)
	return hash
}

func TestNegativeCacheOperations(t *testing.T) {
	cache := newNegativeCache(time.Hour, 10)
	hash := negativeTestHash(1)
	if cache.IsMissing(hash) {
		t.Fatal("new cache reported a missing hash")
	}
	cache.MarkMissing(hash)
	if !cache.IsMissing(hash) {
		t.Fatal("marked hash was not found")
	}
	cache.Remove(hash)
	if cache.IsMissing(hash) {
		t.Fatal("removed hash remained cached")
	}
	cache.MarkMissing(hash)
	cache.Clear()
	if cache.IsMissing(hash) || len(cache.entries) != 0 || cache.order.Len() != 0 {
		t.Fatal("Clear did not empty every index")
	}
}

func TestNegativeCacheSweepsExpiredBeforeEviction(t *testing.T) {
	cache := newNegativeCache(time.Hour, 2)
	expired := negativeTestHash(1)
	fresh := negativeTestHash(2)
	added := negativeTestHash(3)
	cache.MarkMissing(expired)
	cache.MarkMissing(fresh)

	cache.mu.Lock()
	cache.entries[expired].Value.(*negativeCacheEntry).expiresAt = time.Now().Add(-time.Hour)
	cache.mu.Unlock()
	cache.MarkMissing(added)

	if cache.IsMissing(expired) {
		t.Fatal("expired entry remained cached")
	}
	if !cache.IsMissing(fresh) || !cache.IsMissing(added) {
		t.Fatal("capacity insertion evicted a fresh entry before an expired entry")
	}
}

func TestNegativeCacheRefreshMovesEntryToBack(t *testing.T) {
	cache := newNegativeCache(time.Hour, 2)
	first := negativeTestHash(1)
	second := negativeTestHash(2)
	third := negativeTestHash(3)
	cache.MarkMissing(first)
	cache.MarkMissing(second)
	cache.MarkMissing(first)
	cache.MarkMissing(third)

	if !cache.IsMissing(first) || !cache.IsMissing(third) {
		t.Fatal("refreshed or new entry was evicted")
	}
	if cache.IsMissing(second) {
		t.Fatal("oldest entry was not evicted")
	}
}

func TestNegativeCacheSweep(t *testing.T) {
	cache := newNegativeCache(time.Hour, 10)
	for i := uint64(0); i < 5; i++ {
		cache.MarkMissing(negativeTestHash(i))
	}
	cache.mu.Lock()
	now := time.Now()
	for element := cache.order.Front(); element != nil; element = element.Next() {
		element.Value.(*negativeCacheEntry).expiresAt = now.Add(-time.Hour)
	}
	cache.mu.Unlock()
	if removed := cache.Sweep(); removed != 5 {
		t.Fatalf("Sweep removed %d entries, want 5", removed)
	}
	if len(cache.entries) != 0 || cache.order.Len() != 0 {
		t.Fatal("Sweep left stale indexes")
	}
}

func TestNegativeCacheConcurrentAccess(t *testing.T) {
	cache := newNegativeCache(time.Hour, 1000)
	const workers = 16
	const operations = 1000
	var wait sync.WaitGroup
	wait.Add(workers)
	for worker := range workers {
		go func() {
			defer wait.Done()
			for operation := range operations {
				hash := negativeTestHash(uint64(worker*operations + operation))
				cache.MarkMissing(hash)
				cache.IsMissing(hash)
				if operation%3 == 0 {
					cache.Remove(hash)
				}
				if operation%101 == 0 {
					cache.Sweep()
				}
			}
		}()
	}
	wait.Wait()

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) != cache.order.Len() {
		t.Fatalf("indexes disagree: entries=%d order=%d", len(cache.entries), cache.order.Len())
	}
	if len(cache.entries) > cache.maxSize {
		t.Fatalf("cache size=%d exceeds max=%d", len(cache.entries), cache.maxSize)
	}
}

func BenchmarkNegativeCacheCapacity(b *testing.B) {
	const capacity = 100000
	cache := newNegativeCache(time.Hour, capacity)
	for i := uint64(0); i < capacity; i++ {
		cache.MarkMissing(negativeTestHash(i))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		cache.MarkMissing(negativeTestHash(uint64(capacity + i)))
	}
}

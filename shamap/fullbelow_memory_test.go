package shamap

import (
	"crypto/sha256"
	"sync"
	"testing"
	"time"
)

type fullBelowTestBudget struct {
	mu    sync.Mutex
	limit uint64
	used  uint64
}

func (b *fullBelowTestBudget) TryReserve(bytes uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used > b.limit || bytes > b.limit-b.used {
		return false
	}
	b.used += bytes
	return true
}

func (b *fullBelowTestBudget) Release(bytes uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bytes > b.used {
		panic("full-below test budget release exceeds reservation")
	}
	b.used -= bytes
}

func (b *fullBelowTestBudget) Used() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

func TestFullBelowCacheBudgetTracksBothGenerationsAndClearCapacity(t *testing.T) {
	budget := &fullBelowTestBudget{limit: 1 << 20}
	cache := newFullBelowCacheWithClockAndBudget(32, time.Hour, time.Now, budget)
	generation := cache.Generation()
	first := [32]byte{0x01}
	second := [32]byte{0x01, 0x01}
	cache.Insert(generation, first)
	firstCharge := budget.Used()
	if firstCharge != fullBelowEntryBytes+fullBelowStructuralBytes {
		t.Fatalf("first mark charge = %d, want %d", firstCharge, fullBelowEntryBytes+fullBelowStructuralBytes)
	}
	cache.Insert(generation, second)
	if got := cache.Size(); got != 2 {
		t.Fatalf("two-generation cache size = %d, want 2", got)
	}
	if got := budget.Used(); got != 2*(fullBelowEntryBytes+fullBelowStructuralBytes) {
		t.Fatalf("two-generation charge = %d, want %d", got, 2*(fullBelowEntryBytes+fullBelowStructuralBytes))
	}

	endMutation := cache.BeginMutation()
	endMutation()
	if got := cache.Size(); got != 0 {
		t.Fatalf("cache size after Clear = %d, want 0", got)
	}
	if got := budget.Used(); got != 2*fullBelowStructuralBytes {
		t.Fatalf("Clear released retained map capacity: charge=%d, want %d", got, 2*fullBelowStructuralBytes)
	}

	cache.Bump()
	if got := budget.Used(); got != 2*fullBelowStructuralBytes {
		t.Fatalf("generation mutation changed structural charge: %d", got)
	}
	cache.Close()
	if got := budget.Used(); got != 0 {
		t.Fatalf("Close leaked full-below charge: %d", got)
	}
}

func TestFullBelowCacheBudgetDeniesAdmissionWithoutChangingGeneration(t *testing.T) {
	budget := &fullBelowTestBudget{limit: fullBelowEntryBytes + fullBelowStructuralBytes - 1}
	cache := NewFullBelowCacheWithBudget(budget)
	generation := cache.Generation()
	hash := [32]byte{0xCA}
	cache.Insert(generation, hash)
	if cache.Size() != 0 {
		t.Fatal("full-below cache admitted a mark over its shared allowance")
	}
	if got := cache.Generation(); got != generation {
		t.Fatalf("denied admission advanced generation from %d to %d", generation, got)
	}
	cache.Close()
	if got := budget.Used(); got != 0 {
		t.Fatalf("denied admission leaked charge: %d", got)
	}
}

func TestFullBelowCacheBudgetConcurrentLifecycle(t *testing.T) {
	budget := &fullBelowTestBudget{limit: 1 << 20}
	cache := newFullBelowCacheWithClockAndBudget(512, time.Hour, time.Now, budget)
	generation := cache.Generation()
	var wg sync.WaitGroup
	for worker := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range 5_000 {
				key := sha256.Sum256([]byte{byte(worker), byte(index), byte(index >> 8)})
				cache.Insert(generation, key)
				if index%11 == 0 {
					cache.Has(generation, key)
				}
			}
		}()
	}
	for range 20 {
		cache.Sweep()
	}
	wg.Wait()
	if got := cache.Size(); got > 512 {
		t.Fatalf("budgeted cache exceeded target: %d", got)
	}
	cache.Close()
	if got := budget.Used(); got != 0 {
		t.Fatalf("concurrent close leaked charge: %d", got)
	}
}

func TestFullBelowCacheCloseRacesReaders(t *testing.T) {
	budget := &fullBelowTestBudget{limit: 1 << 20}
	cache := newFullBelowCacheWithClockAndBudget(64, time.Hour, time.Now, budget)
	generation := cache.Generation()
	var wg sync.WaitGroup
	for worker := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range 2_000 {
				hash := [32]byte{byte(worker), byte(index)}
				cache.Insert(generation, hash)
				cache.Has(generation, hash)
				cache.Sweep()
			}
		}()
	}
	cache.Close()
	wg.Wait()
	if got := budget.Used(); got != 0 {
		t.Fatalf("racing close leaked charge: %d", got)
	}
}

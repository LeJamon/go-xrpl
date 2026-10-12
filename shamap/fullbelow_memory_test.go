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

func TestFullBelowCacheMemoryCommitmentAdmitsSameShardRoots(t *testing.T) {
	if want := uint64(fullBelowCacheTarget) * (fullBelowEntryBytes + fullBelowStructuralBytes); FullBelowCacheMemoryBytes != want {
		t.Fatalf("default FullBelow commitment = %d, want %d", FullBelowCacheMemoryBytes, want)
	}

	defaultBudget := &fullBelowTestBudget{limit: FullBelowCacheMemoryBytes}
	defaultCache := NewFullBelowCacheWithBudget(defaultBudget)
	defaultGeneration := defaultCache.Generation()
	stateRoot := [32]byte{0x00, 0x01}
	txRoot := [32]byte{0x00, 0x02}
	defaultCache.Insert(defaultGeneration, stateRoot)
	defaultCache.Insert(defaultGeneration, txRoot)
	if !defaultCache.Has(defaultGeneration, stateRoot) || !defaultCache.Has(defaultGeneration, txRoot) {
		t.Fatal("default FullBelow commitment denied same-shard proof roots")
	}
	defaultCache.Close()
	if got := defaultBudget.Used(); got != 0 {
		t.Fatalf("default FullBelow commitment leaked charge: %d", got)
	}

	const targetSize = 8
	rotationBudget := &fullBelowTestBudget{
		limit: uint64(targetSize) * (fullBelowEntryBytes + fullBelowStructuralBytes),
	}
	cache := newFullBelowCacheWithClockAndBudget(targetSize, time.Hour, time.Now, rotationBudget)
	generation := cache.Generation()
	for index := range 4 * targetSize {
		hash := [32]byte{0x00, byte(index + 3)}
		cache.Insert(generation, hash)
		if got := cache.Size(); got > targetSize {
			t.Fatalf("rotating cache exceeded target after mark %d: %d", index, got)
		}
	}
	if got := rotationBudget.Used(); got > rotationBudget.limit {
		t.Fatalf("rotating cache exceeded exact commitment: %d > %d", got, rotationBudget.limit)
	}
	if got := cache.Stats().Inserts; got != 4*targetSize {
		t.Fatalf("exact commitment denied a rotation mark: inserts=%d, want %d", got, 4*targetSize)
	}
	cache.Close()
	if got := rotationBudget.Used(); got != 0 {
		t.Fatalf("rotating cache leaked charge: %d", got)
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

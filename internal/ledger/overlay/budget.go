package overlay

import (
	"math"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/membudget"
)

// budget is shared by all immutable descendants of one overlay tree. It keeps
// package-local ceilings separate from the process-wide membudget account while
// preserving one admission decision for every allocation.
type budget struct {
	mu sync.Mutex

	maxMemory uint64
	maxDisk   uint64
	memory    uint64
	disk      uint64

	memoryAccount *membudget.Account
	diskAccount   *membudget.Account
}

func newBudget(cfg Config) *budget {
	return &budget{
		maxMemory:     cfg.MaxMemoryBytes,
		maxDisk:       cfg.MaxDiskBytes,
		memoryAccount: cfg.MemoryBudget,
		diskAccount:   cfg.DiskBudget,
	}
}

func (b *budget) reserveMemory(bytes uint64) bool {
	if bytes == 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if bytes > math.MaxUint64-b.memory || (b.maxMemory > 0 && bytes > b.maxMemory-b.memory) {
		return false
	}
	if b.memoryAccount != nil && !b.memoryAccount.TryReserve(bytes) {
		return false
	}
	b.memory += bytes
	return true
}

func (b *budget) releaseMemory(bytes uint64) {
	if b == nil || bytes == 0 {
		return
	}
	b.mu.Lock()
	if bytes > b.memory {
		b.memory = 0
	} else {
		b.memory -= bytes
	}
	b.mu.Unlock()
	if b.memoryAccount != nil {
		b.memoryAccount.Release(bytes)
	}
}

func (b *budget) reserveDisk(bytes uint64) bool {
	if bytes == 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if bytes > math.MaxUint64-b.disk || b.maxDisk == 0 || bytes > b.maxDisk-b.disk {
		return false
	}
	if b.diskAccount != nil && !b.diskAccount.TryReserve(bytes) {
		return false
	}
	b.disk += bytes
	return true
}

func (b *budget) releaseDisk(bytes uint64) {
	if b == nil || bytes == 0 {
		return
	}
	b.mu.Lock()
	if bytes > b.disk {
		b.disk = 0
	} else {
		b.disk -= bytes
	}
	b.mu.Unlock()
	if b.diskAccount != nil {
		b.diskAccount.Release(bytes)
	}
}

type BudgetStats struct {
	MemoryBytes uint64
	DiskBytes   uint64
	MaxMemory   uint64
	MaxDisk     uint64
}

func (b *budget) stats() BudgetStats {
	if b == nil {
		return BudgetStats{}
	}
	b.mu.Lock()
	s := BudgetStats{MemoryBytes: b.memory, DiskBytes: b.disk, MaxMemory: b.maxMemory, MaxDisk: b.maxDisk}
	b.mu.Unlock()
	return s
}

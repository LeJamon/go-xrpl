package shamap

import (
	"bytes"
	"context"
	"fmt"
	"sync"
)

type memoryFamily struct {
	mu        sync.RWMutex
	store     map[[32]byte][]byte
	fullBelow *FullBelowCache
}

func newMemoryFamily() *memoryFamily {
	return &memoryFamily{
		store:     make(map[[32]byte][]byte),
		fullBelow: NewFullBelowCache(),
	}
}

func (f *memoryFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return bytes.Clone(f.store[hash]), nil
}

func (f *memoryFamily) FetchCached(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.Fetch(ctx, hash)
}

func (f *memoryFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.Fetch(ctx, hash)
}

func (f *memoryFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, entry := range entries {
		f.store[entry.Hash] = bytes.Clone(entry.Data)
	}
	return nil
}

func (f *memoryFamily) FullBelowCache() *FullBelowCache {
	return f.fullBelow
}

func (f *memoryFamily) BeginPrune() func() {
	return f.fullBelow.BeginMutation()
}

func (f *memoryFamily) Len() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.store)
}

func flushToFamily(sm *SHAMap, family *memoryFamily) error {
	batch, err := sm.FlushDirty()
	if err != nil {
		return fmt.Errorf("FlushDirty: %w", err)
	}
	if len(batch.Entries) > 0 {
		return family.StoreBatch(context.Background(), batch.Entries)
	}
	return nil
}

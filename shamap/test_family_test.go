package shamap

import (
	"bytes"
	"context"
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

type testNodeBatch struct {
	Entries []FlushEntry
}

func collectDirtyForTest(sm *SHAMap) (*testNodeBatch, error) {
	return collectDirtyWith(sm.StoreDirty)
}

func collectDirtyAndReleaseForTest(sm *SHAMap) (*testNodeBatch, error) {
	return collectDirtyWith(sm.StoreDirtyAndRelease)
}

func collectDirtyWith(store func(func([]FlushEntry) error) error) (*testNodeBatch, error) {
	batch := &testNodeBatch{}
	err := store(func(entries []FlushEntry) error {
		batch.Entries = entries
		return nil
	})
	return batch, err
}

func flushToFamily(sm *SHAMap, family *memoryFamily) error {
	return sm.StoreDirty(func(entries []FlushEntry) error {
		return family.StoreBatch(context.Background(), entries)
	})
}

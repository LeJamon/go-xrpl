package shamap

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
)

type rootReaderTestLeaf struct {
	key  [32]byte
	data []byte
	kind NodeType
}

func buildRootReaderTestTree(t *testing.T, family *memoryFamily, count byte) ([32]byte, []rootReaderTestLeaf) {
	t.Helper()
	sm := New(TypeState)
	want := make([]rootReaderTestLeaf, 0, count)
	for i := byte(1); i <= count; i++ {
		key := commitmentTestKey(i)
		data := commitmentTestData(i)
		if err := sm.Put(key, data); err != nil {
			t.Fatalf("Put(%x): %v", key[:1], err)
		}
		want = append(want, rootReaderTestLeaf{key: key, data: bytes.Clone(data), kind: NodeTypeAccountState})
	}
	if err := flushToFamily(sm, family); err != nil {
		t.Fatalf("flush: %v", err)
	}
	root, err := sm.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	return root, want
}

func assertRootReaderTestLeaves(t *testing.T, got, want []rootReaderTestLeaf) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("leaf count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].key != want[i].key || got[i].kind != want[i].kind || !bytes.Equal(got[i].data, want[i].data) {
			t.Fatalf("leaf %d = {%x, %x, %v}, want {%x, %x, %v}", i, got[i].key[:4], got[i].data, got[i].kind, want[i].key[:4], want[i].data, want[i].kind)
		}
	}
}

func TestRootReaderIterateLeafDataParityAfterAndEarlyStop(t *testing.T) {
	family := newMemoryFamily()
	root, want := buildRootReaderTestTree(t, family, 8)
	reader := NewRootReader(TypeState, root, family)

	var items []rootReaderTestLeaf
	if err := reader.IterateLeaves(context.Background(), nil, func(item *Item, kind NodeType) bool {
		items = append(items, rootReaderTestLeaf{key: item.Key(), data: item.Data(), kind: kind})
		return true
	}); err != nil {
		t.Fatalf("IterateLeaves: %v", err)
	}
	assertRootReaderTestLeaves(t, items, want)

	var raw []rootReaderTestLeaf
	if err := reader.IterateLeafData(context.Background(), nil, func(key [32]byte, data []byte, kind NodeType) bool {
		raw = append(raw, rootReaderTestLeaf{key: key, data: bytes.Clone(data), kind: kind})
		return true
	}); err != nil {
		t.Fatalf("IterateLeafData: %v", err)
	}
	assertRootReaderTestLeaves(t, raw, want)

	after := want[2].key
	var suffix []rootReaderTestLeaf
	if err := reader.IterateLeafData(context.Background(), &after, func(key [32]byte, data []byte, kind NodeType) bool {
		suffix = append(suffix, rootReaderTestLeaf{key: key, data: bytes.Clone(data), kind: kind})
		return true
	}); err != nil {
		t.Fatalf("IterateLeafData(after): %v", err)
	}
	assertRootReaderTestLeaves(t, suffix, want[3:])

	count := 0
	if err := reader.IterateLeafData(context.Background(), nil, func([32]byte, []byte, NodeType) bool {
		count++
		return count < 3
	}); err != nil {
		t.Fatalf("IterateLeafData early stop: %v", err)
	}
	if count != 3 {
		t.Fatalf("early-stop callback count = %d, want 3", count)
	}
}

func TestRootReaderIterateLeafDataCancellation(t *testing.T) {
	family := newMemoryFamily()
	root, _ := buildRootReaderTestTree(t, family, 4)
	reader := NewRootReader(TypeState, root, family)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	err := reader.IterateLeafData(ctx, nil, func([32]byte, []byte, NodeType) bool {
		called = true
		return true
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("IterateLeafData error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("IterateLeafData called callback after cancellation")
	}
}

type rootReaderReusableFamily struct {
	base    *memoryFamily
	mu      sync.Mutex
	scratch []byte
}

func (f *rootReaderReusableFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	data, err := f.base.Fetch(ctx, hash)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scratch = append(f.scratch[:0], data...)
	return f.scratch, nil
}

func (f *rootReaderReusableFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	return f.base.StoreBatch(ctx, entries)
}

func (f *rootReaderReusableFamily) overwriteScratch(value byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.scratch {
		f.scratch[i] = value
	}
}

func TestRootReaderIterateLeafDataClonesFamilyLeafBytes(t *testing.T) {
	base := newMemoryFamily()
	root, want := buildRootReaderTestTree(t, base, 6)
	family := &rootReaderReusableFamily{base: base}
	reader := NewRootReader(TypeState, root, family)

	var got []rootReaderTestLeaf
	mutated := false
	if err := reader.IterateLeafData(context.Background(), nil, func(key [32]byte, data []byte, kind NodeType) bool {
		if !mutated {
			before := bytes.Clone(data)
			family.overwriteScratch(0xee)
			if !bytes.Equal(data, before) {
				t.Fatalf("leaf data changed when Family reused its fetch buffer")
			}
			mutated = true
		}
		got = append(got, rootReaderTestLeaf{key: key, data: bytes.Clone(data), kind: kind})
		return true
	}); err != nil {
		t.Fatalf("IterateLeafData: %v", err)
	}
	assertRootReaderTestLeaves(t, got, want)
}

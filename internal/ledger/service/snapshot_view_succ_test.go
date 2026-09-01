package service

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/shamap"
	shamapbackend "github.com/LeJamon/go-xrpl/shamap/backend"
)

type failAfterRootFetchFamily struct {
	shamap.Family
	err     error
	fetches int
}

func (f *failAfterRootFetchFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.fetches++
	if f.fetches > 1 {
		return nil, f.err
	}
	return f.Family.Fetch(ctx, hash)
}

func TestSnapshotViewSucc(t *testing.T) {
	stateMap := shamap.New(shamap.TypeState)
	firstKey := [32]byte{0x10}
	secondKey := [32]byte{0x20}
	secondData := bytes.Repeat([]byte{2}, 12)
	if err := stateMap.Put(firstKey, bytes.Repeat([]byte{1}, 12)); err != nil {
		t.Fatalf("put first item: %v", err)
	}
	if err := stateMap.Put(secondKey, secondData); err != nil {
		t.Fatalf("put second item: %v", err)
	}
	view := newSnapshotView(stateMap, nil)

	key, data, found, err := view.Succ(firstKey)
	if err != nil || !found || key != secondKey || !bytes.Equal(data, secondData) {
		t.Fatalf("Succ(first) = (%x, %x, %v, %v), want (%x, %x, true, nil)", key, data, found, err, secondKey, secondData)
	}

	key, data, found, err = view.Succ(secondKey)
	if err != nil || found || key != ([32]byte{}) || data != nil {
		t.Fatalf("Succ(last) = (%x, %x, %v, %v), want empty exhaustion", key, data, found, err)
	}
}

func TestSnapshotViewSuccPropagatesIteratorError(t *testing.T) {
	stateMap := shamap.New(shamap.TypeState)
	if err := stateMap.Put([32]byte{0x10}, bytes.Repeat([]byte{1}, 12)); err != nil {
		t.Fatalf("put item: %v", err)
	}
	root, err := stateMap.Hash()
	if err != nil {
		t.Fatalf("hash state map: %v", err)
	}
	base := shamapbackend.NewMemory()
	if err := stateMap.StoreDirty(func(entries []shamap.FlushEntry) error {
		return base.StoreBatch(context.Background(), entries)
	}); err != nil {
		t.Fatalf("store state map: %v", err)
	}

	injected := errors.New("injected traversal failure")
	family := &failAfterRootFetchFamily{Family: base, err: injected}
	backed, err := shamap.NewFromRootHash(shamap.TypeState, root, family)
	if err != nil {
		t.Fatalf("load backed state map: %v", err)
	}

	key, data, found, err := newSnapshotView(backed, nil).Succ([32]byte{})
	if !errors.Is(err, injected) || found || key != ([32]byte{}) || data != nil {
		t.Fatalf("Succ with traversal failure = (%x, %x, %v, %v), want empty result and %v", key, data, found, err, injected)
	}
}

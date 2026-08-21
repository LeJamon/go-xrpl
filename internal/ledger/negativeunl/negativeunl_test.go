package negativeunl

import (
	"context"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

var errNegativeUNLRead = errors.New("negative UNL read failed")

type failingFamily struct {
	nodes map[[32]byte][]byte
	fail  bool
}

func (f *failingFamily) Fetch(_ context.Context, hash [32]byte) ([]byte, error) {
	if f.fail {
		return nil, errNegativeUNLRead
	}
	return f.nodes[hash], nil
}

func (f *failingFamily) StoreBatch(_ context.Context, entries []shamap.FlushEntry) error {
	for _, entry := range entries {
		f.nodes[entry.Hash] = append([]byte(nil), entry.Data...)
	}
	return nil
}

func TestApplyPropagatesStateMapReadFailure(t *testing.T) {
	stateMap := shamap.New(shamap.TypeState)
	negativeUNLKey := keylet.NegativeUNL().Key
	if err := stateMap.Put(negativeUNLKey, make([]byte, 12)); err != nil {
		t.Fatalf("put NegativeUNL: %v", err)
	}
	otherKey := negativeUNLKey
	otherKey[0] ^= 0xf0
	if err := stateMap.Put(otherKey, make([]byte, 12)); err != nil {
		t.Fatalf("put sibling: %v", err)
	}

	root, err := stateMap.Hash()
	if err != nil {
		t.Fatalf("hash state map: %v", err)
	}
	family := &failingFamily{nodes: make(map[[32]byte][]byte)}
	if err := stateMap.StoreDirty(func(entries []shamap.FlushEntry) error {
		return family.StoreBatch(context.Background(), entries)
	}); err != nil {
		t.Fatalf("store state map: %v", err)
	}

	backed, err := shamap.NewFromRootHash(shamap.TypeState, root, family)
	if err != nil {
		t.Fatalf("load state map root: %v", err)
	}
	family.fail = true

	err = Apply(backed, 256)
	if !errors.Is(err, errNegativeUNLRead) {
		t.Fatalf("Apply error = %v, want %v", err, errNegativeUNLRead)
	}
}

func TestApplyAbsentNegativeUNLIsNoOp(t *testing.T) {
	stateMap := shamap.New(shamap.TypeState)
	if err := Apply(stateMap, 256); err != nil {
		t.Fatalf("Apply absent NegativeUNL: %v", err)
	}
}

func TestApplyRejectsMalformedNegativeUNL(t *testing.T) {
	stateMap := shamap.New(shamap.TypeState)
	if err := stateMap.Put(keylet.NegativeUNL().Key, make([]byte, 12)); err != nil {
		t.Fatalf("put malformed NegativeUNL: %v", err)
	}

	err := Apply(stateMap, 256)
	if err == nil {
		t.Fatal("Apply malformed NegativeUNL returned nil")
	}
}

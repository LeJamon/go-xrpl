package adaptor

import (
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

func txpoolBlob(i int) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(i))
	return b
}

func TestTxPool_AddDedup(t *testing.T) {
	p := NewTxPool()

	blob := txpoolBlob(1)
	if !p.Add(blob) {
		t.Fatal("first Add should report the tx as new")
	}
	if p.Add(blob) {
		t.Fatal("second Add of the same blob should report a duplicate")
	}
	if got := p.Size(); got != 1 {
		t.Fatalf("Size = %d, want 1", got)
	}

	id := computeTxID(blob)
	if !p.Has(id) {
		t.Fatal("Has should be true for an added tx")
	}
	if got := p.Get(id); string(got) != string(blob) {
		t.Fatalf("Get returned %x, want %x", got, blob)
	}

	var missing consensus.TxID
	if p.Has(missing) {
		t.Fatal("Has should be false for an unknown id")
	}
	if p.Get(missing) != nil {
		t.Fatal("Get should return nil for an unknown id")
	}
}

func TestTxPool_GetAll(t *testing.T) {
	p := NewTxPool()
	for i := 0; i < 5; i++ {
		if !p.Add(txpoolBlob(i)) {
			t.Fatalf("Add(%d) should be new", i)
		}
	}
	all := p.GetAll()
	if len(all) != 5 {
		t.Fatalf("GetAll returned %d blobs, want 5", len(all))
	}
}

func TestTxPool_RemoveAndClear(t *testing.T) {
	p := NewTxPool()
	ids := make([]consensus.TxID, 0, 3)
	for i := 0; i < 3; i++ {
		blob := txpoolBlob(i)
		p.Add(blob)
		ids = append(ids, computeTxID(blob))
	}

	p.Remove(ids[:2])
	if got := p.Size(); got != 1 {
		t.Fatalf("Size after Remove = %d, want 1", got)
	}
	// Removed entries stay in the recently-seen cache, so Has remains true.
	if !p.Has(ids[0]) {
		t.Fatal("removed tx should still be in the recently-seen cache")
	}

	p.Clear()
	if got := p.Size(); got != 0 {
		t.Fatalf("Size after Clear = %d, want 0", got)
	}
}

func TestTxPool_RecentEviction(t *testing.T) {
	p := NewTxPool()

	// Fill past the recently-seen capacity so the first entry is evicted.
	total := maxRecentTxs + 1
	for i := 0; i < total; i++ {
		if !p.Add(txpoolBlob(i)) {
			t.Fatalf("Add(%d) should be new", i)
		}
	}

	first := computeTxID(txpoolBlob(0))
	// The oldest id has been evicted from the ring buffer but is still pending,
	// so Has reports true via the pending map. Remove it from pending to observe
	// that the recently-seen cache no longer holds it.
	p.Remove([]consensus.TxID{first})
	if p.Has(first) {
		t.Fatal("oldest tx should have been evicted from the recently-seen cache")
	}

	// A freshly evicted id is treated as new again.
	if !p.Add(txpoolBlob(0)) {
		t.Fatal("re-adding an evicted, removed tx should report it as new")
	}
}

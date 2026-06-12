package shamap

import (
	"bytes"
	"testing"
)

// ibTree builds a state map whose keys share leading nibbles so several
// leaves live under one inner subtree — the shape that exposed the bound
// iterator's continuation skipping the rest of a subtree after boundBelow.
func ibTree(t *testing.T) (*SHAMap, [][32]byte) {
	t.Helper()
	sm := New(TypeState)
	keys := make([][32]byte, 0, 64)
	for hi := range byte(4) {
		for lo := range byte(16) {
			var k [32]byte
			k[0] = hi<<4 | lo
			k[1] = 0x42
			k[2] = hi + lo
			data := []byte{hi, lo, 0xAB, 0xCD, 0xEF, 1, 2, 3, 4, 5, 6, 7}
			if err := sm.Put(k, data); err != nil {
				t.Fatalf("Put(%x): %v", k[:3], err)
			}
			keys = append(keys, k)
		}
	}
	return sm, keys // inserted in ascending key order
}

func ibDrain(t *testing.T, it *Iterator) [][32]byte {
	t.Helper()
	var got [][32]byte
	for it.Valid() {
		got = append(got, it.Item().Key())
		it.Next()
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
	return got
}

// Mirrors the ordering walk of rippled SHAMap_test.cpp:173-194, but starting
// from upper_bound positions: draining UpperBound(keys[i]) must yield exactly
// keys[i+1:], in order, with no duplicates or skips — including the leaves
// that share an inner subtree with the bound key.
func TestUpperBoundDrain(t *testing.T) {
	sm, keys := ibTree(t)

	for i := range keys {
		got := ibDrain(t, sm.UpperBound(keys[i]))
		want := keys[i+1:]
		if len(got) != len(want) {
			t.Fatalf("UpperBound(keys[%d]) drained %d items, want %d", i, len(got), len(want))
		}
		for j := range want {
			if !bytes.Equal(got[j][:], want[j][:]) {
				t.Fatalf("UpperBound(keys[%d]) item %d = %x, want %x", i, j, got[j][:3], want[j][:3])
			}
		}
	}

	// A probe key strictly between two stored keys positions on the next one.
	probe := keys[10]
	probe[31] = 0x01
	got := ibDrain(t, sm.UpperBound(probe))
	if len(got) != len(keys)-11 {
		t.Fatalf("UpperBound(between) drained %d items, want %d", len(got), len(keys)-11)
	}
	if !bytes.Equal(got[0][:], keys[11][:]) {
		t.Fatalf("UpperBound(between) first = %x, want %x", got[0][:3], keys[11][:3])
	}

	// Past the last key: invalid iterator, Next stays false.
	it := sm.UpperBound(keys[len(keys)-1])
	if it.Valid() {
		t.Fatal("UpperBound(last) must be invalid")
	}
	if it.Next() {
		t.Fatal("Next on exhausted bound iterator must stay false")
	}
}

// lowerBound positions on the greatest key < id; Next then ascends through
// id itself (when present) and the rest of the map, matching ++ on rippled's
// lower_bound iterator.
func TestLowerBoundThenNext(t *testing.T) {
	sm, keys := ibTree(t)

	for i := 1; i < len(keys); i++ {
		it := sm.lowerBound(keys[i])
		if !it.Valid() {
			t.Fatalf("lowerBound(keys[%d]) invalid", i)
		}
		got := ibDrain(t, it)
		want := keys[i-1:] // predecessor, then id itself, then the tail
		if len(got) != len(want) {
			t.Fatalf("lowerBound(keys[%d]) drained %d items, want %d", i, len(got), len(want))
		}
		for j := range want {
			if !bytes.Equal(got[j][:], want[j][:]) {
				t.Fatalf("lowerBound(keys[%d]) item %d = %x, want %x", i, j, got[j][:3], want[j][:3])
			}
		}
	}

	// Below the smallest key: invalid.
	if it := sm.lowerBound(keys[0]); it.Valid() {
		t.Fatal("lowerBound(first) must be invalid")
	}
}

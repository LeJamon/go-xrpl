package shamap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
)

func TestOnlyBelowOccupancy(t *testing.T) {
	sm := New(TypeState)
	item := NewItem([32]byte{0xf0}, bytes.Repeat([]byte{1}, 12))
	leaf, err := newAccountStateLeafNode(item)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range []mapNode{nil, newInnerNode()} {
		got, err := sm.onlyBelow(node)
		if err != nil || got != nil {
			t.Fatalf("empty subtree: item=%v error=%v", got, err)
		}
	}
	got, err := sm.onlyBelow(leaf)
	if err != nil || got != item {
		t.Fatalf("leaf: item=%v error=%v", got, err)
	}

	// Exercise every bit, including the first and last branch, through a
	// chain as deep as the shared prefixes in quality-directory keys.
	for branch := range BranchFactor {
		t.Run(fmt.Sprint(branch), func(t *testing.T) {
			var node mapNode = leaf
			for range 48 {
				parent := newInnerNode()
				if err := parent.SetChild(branch, node); err != nil {
					t.Fatal(err)
				}
				node = parent
			}
			got, err := sm.onlyBelow(node)
			if err != nil || got != item {
				t.Fatalf("single-child chain: item=%v error=%v", got, err)
			}
		})
	}
}

func TestOnlyBelowBackedBranches(t *testing.T) {
	item := NewItem([32]byte{0xf0}, bytes.Repeat([]byte{1}, 12))
	leaf, err := newAccountStateLeafNode(item)
	if err != nil {
		t.Fatal(err)
	}
	data, err := leaf.SerializeWithPrefix()
	if err != nil {
		t.Fatal(err)
	}
	mismatched := bytes.Clone(data)
	mismatched[4] ^= 1 // Valid leaf encoding, but no longer the requested hash.
	fetchErr := errors.New("fetch failed")
	for _, tc := range []struct {
		name     string
		branches []int
		data     []byte
		fetchErr error
		wantErr  bool
	}{
		{name: "empty"},
		{name: "multiple unloaded children", branches: []int{0, 15}},
		{name: "single unloaded child", branches: []int{15}, data: data},
		{name: "missing single child", branches: []int{15}, wantErr: true},
		{name: "fetch error", branches: []int{15}, fetchErr: fetchErr, wantErr: true},
		{name: "invalid content", branches: []int{15}, data: []byte{1}, wantErr: true},
		{name: "mismatched content", branches: []int{15}, data: mismatched, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			family := &familyAccessBase{data: tc.data, err: tc.fetchErr}
			sm, err := NewBacked(TypeState, family)
			if err != nil {
				t.Fatal(err)
			}
			inner := newInnerNode()
			for _, branch := range tc.branches {
				if err := inner.SetChild(branch, leaf); err != nil {
					t.Fatal(err)
				}
				if !inner.ReleaseChild(branch, leaf) {
					t.Fatal("failed to release child")
				}
			}
			got, err := sm.onlyBelow(inner)
			if (err != nil) != tc.wantErr {
				t.Fatalf("item=%v error=%v, want error=%v", got, err, tc.wantErr)
			}
			if tc.fetchErr != nil && !errors.Is(err, tc.fetchErr) {
				t.Fatalf("fetch error not preserved: %v", err)
			}
			if tc.name == "mismatched content" && !errors.Is(err, ErrInvalidNodeData) {
				t.Fatalf("hash mismatch error not preserved: %v", err)
			}
			wantFetches := 0
			if len(tc.branches) == 1 {
				wantFetches = 1
				if !tc.wantErr && (got == nil || got.Key() != item.Key() || !bytes.Equal(got.Data(), item.Data())) {
					t.Fatalf("single child: got %v, want %v", got, item)
				}
			} else if got != nil {
				t.Fatalf("non-single subtree returned %v", got)
			}
			if family.calls != wantFetches {
				t.Fatalf("fetches=%d, want %d", family.calls, wantFetches)
			}
		})
	}
}

func TestDeleteCommonPrefixPreservesRootsAndSnapshot(t *testing.T) {
	for _, backed := range []bool{false, true} {
		t.Run(fmt.Sprintf("backed=%v", backed), func(t *testing.T) {
			sm := New(TypeState)
			keys := make([][32]byte, 32)
			data := bytes.Repeat([]byte{1}, 12)
			for i := range keys {
				// Book quality keys share the first 24 bytes.
				copy(keys[i][:24], bytes.Repeat([]byte{0xab}, 24))
				binary.BigEndian.PutUint64(keys[i][24:], uint64(i+1))
				if err := sm.Put(keys[i], data); err != nil {
					t.Fatal(err)
				}
			}
			originalHash, err := sm.Hash()
			if err != nil {
				t.Fatal(err)
			}
			if backed {
				family := newMemoryFamily()
				if err := flushToFamily(sm, family); err != nil {
					t.Fatal(err)
				}
				sm, err = NewFromRootHash(TypeState, originalHash, family)
				if err != nil {
					t.Fatal(err)
				}
			}
			snapshot, err := sm.SnapshotImmutable()
			if err != nil {
				t.Fatal(err)
			}
			for i, key := range keys {
				if err := sm.Delete(key); err != nil {
					t.Fatal(err)
				}
				want := New(TypeState)
				for _, remaining := range keys[i+1:] {
					if err := want.Put(remaining, data); err != nil {
						t.Fatal(err)
					}
				}
				gotHash, err := sm.Hash()
				if err != nil {
					t.Fatal(err)
				}
				wantHash, err := want.Hash()
				if err != nil || gotHash != wantHash {
					t.Fatalf("delete %d: root=%x want=%x error=%v", i, gotHash, wantHash, err)
				}
				snapshotHash, err := snapshot.Hash()
				if err != nil || snapshotHash != originalHash {
					t.Fatalf("snapshot changed: root=%x want=%x error=%v", snapshotHash, originalHash, err)
				}
				item, found, err := snapshot.Get(key)
				if err != nil || !found || item == nil || !bytes.Equal(item.Data(), data) {
					t.Fatalf("deleted key changed in snapshot: found=%v item=%v error=%v", found, item, err)
				}
			}
		})
	}
}

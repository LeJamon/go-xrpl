package shamap

import (
	"bytes"
	"context"
	"testing"
)

type rootReaderRetainedChange struct {
	change   Change
	mutation byte
}

func buildRootReaderDiffOwnershipRoot(t *testing.T, family *memoryFamily, values map[byte]byte) ([32]byte, map[[32]byte][]byte) {
	t.Helper()
	sm := New(TypeState)
	want := make(map[[32]byte][]byte, len(values))
	for keyByte, dataByte := range values {
		key := commitmentTestKey(keyByte)
		data := commitmentTestData(dataByte)
		if err := sm.Put(key, data); err != nil {
			t.Fatalf("Put(%x): %v", key[:1], err)
		}
		want[key] = bytes.Clone(data)
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

func assertRootReaderDiffOwnershipRoot(t *testing.T, family Family, root [32]byte, want map[[32]byte][]byte, keys map[[32]byte]struct{}) {
	t.Helper()
	reader := NewRootReader(TypeState, root, family)
	for key := range keys {
		item, kind, found, err := reader.GetLeaf(context.Background(), key)
		if err != nil {
			t.Fatalf("GetLeaf(%x): %v", key[:4], err)
		}
		expected, exists := want[key]
		if !exists {
			if found {
				t.Fatalf("GetLeaf(%x) found deleted key", key[:4])
			}
			continue
		}
		if !found || kind != NodeTypeAccountState || !bytes.Equal(item.Data(), expected) {
			t.Fatalf("GetLeaf(%x) = found=%v kind=%v data=%x, want kind=%v data=%x", key[:4], found, kind, item.Data(), NodeTypeAccountState, expected)
		}
	}
}

func TestRootReaderDiffTransfersOwnedLeafData(t *testing.T) {
	cases := []struct {
		name          string
		old           map[byte]byte
		new           map[byte]byte
		minNonDeletes int
	}{
		{
			name:          "multiple emissions",
			old:           map[byte]byte{0x10: 0x10, 0x21: 0x21, 0xa0: 0xa0},
			new:           map[byte]byte{0x10: 0x90, 0x21: 0x91, 0xa0: 0xa0, 0xb0: 0xb0},
			minNonDeletes: 3,
		},
		{
			name:          "old leaf and new subtree",
			old:           map[byte]byte{0x10: 0x10, 0xa0: 0xa0},
			new:           map[byte]byte{0x10: 0x90, 0x11: 0x11, 0xa0: 0xa0},
			minNonDeletes: 2,
		},
		{
			name:          "old subtree and new leaf",
			old:           map[byte]byte{0x10: 0x10, 0x11: 0x11, 0xa0: 0xa0},
			new:           map[byte]byte{0x10: 0x90, 0xa0: 0xa0},
			minNonDeletes: 1,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			base := newMemoryFamily()
			oldRoot, oldWant := buildRootReaderDiffOwnershipRoot(t, base, test.old)
			newRoot, newWant := buildRootReaderDiffOwnershipRoot(t, base, test.new)
			family := &rootReaderReusableFamily{base: base}

			keys := make(map[[32]byte]struct{}, len(oldWant)+len(newWant))
			for key := range oldWant {
				keys[key] = struct{}{}
			}
			for key := range newWant {
				keys[key] = struct{}{}
			}

			var retained []rootReaderRetainedChange
			nonDeletes := 0
			familyMutated := false
			err := NewRootReader(TypeState, oldRoot, family).Diff(context.Background(), newRoot, func(change Change) error {
				if change.Delete {
					if _, ok := oldWant[change.Key]; !ok {
						t.Fatalf("deleted unknown key %x", change.Key[:4])
					}
					if _, ok := newWant[change.Key]; ok {
						t.Fatalf("deleted key retained in target %x", change.Key[:4])
					}
					retained = append(retained, rootReaderRetainedChange{change: change})
					return nil
				}

				expected, ok := newWant[change.Key]
				if !ok || !bytes.Equal(change.Data, expected) {
					t.Fatalf("emitted value for %x = %x, want %x", change.Key[:4], change.Data, expected)
				}
				mutation := byte(0x40 + nonDeletes)
				nonDeletes++
				retained = append(retained, rootReaderRetainedChange{change: change, mutation: mutation})
				for i := range change.Data {
					change.Data[i] = mutation
				}
				if !familyMutated {
					family.overwriteScratch(0xee)
					familyMutated = true
				}
				return nil
			})
			if err != nil {
				t.Fatalf("Diff: %v", err)
			}
			if nonDeletes < test.minNonDeletes {
				t.Fatalf("non-delete changes = %d, want at least %d", nonDeletes, test.minNonDeletes)
			}
			for i, retainedChange := range retained {
				if retainedChange.change.Delete {
					continue
				}
				for _, value := range retainedChange.change.Data {
					if value != retainedChange.mutation {
						t.Fatalf("retained change %d was altered by a later emission", i)
					}
				}
			}

			assertRootReaderDiffOwnershipRoot(t, family, oldRoot, oldWant, keys)
			assertRootReaderDiffOwnershipRoot(t, family, newRoot, newWant, keys)
		})
	}
}

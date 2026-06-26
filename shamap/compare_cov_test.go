package shamap

import (
	"bytes"
	"testing"
)

type cmp_entry struct {
	key [32]byte
	val []byte
}

func cmp_makeMap(t *testing.T, entries []cmp_entry) *SHAMap {
	t.Helper()
	m := New(TypeState)
	for _, e := range entries {
		if err := m.Put(e.key, e.val); err != nil {
			t.Fatalf("cmp_makeMap: Put: %v", err)
		}
	}
	return m
}

func cmp_key(seed byte) [32]byte {
	var k [32]byte
	k[0] = seed
	k[31] = seed ^ 0xFF
	return k
}

func cmp_val(seed byte) []byte {
	return []byte{seed, seed + 1, seed + 2, 0, 0, 0, 0, 0, 0, 0, 0, 0}
}

func cmp_diffsByType(ds *DifferenceSet) (added, removed, modified [][32]byte) {
	for _, d := range ds.Differences {
		switch d.Type {
		case DiffAdded:
			added = append(added, d.Key)
		case DiffRemoved:
			removed = append(removed, d.Key)
		case DiffModified:
			modified = append(modified, d.Key)
		}
	}
	return
}

func TestCmpIdenticalMaps(t *testing.T) {
	entries := []cmp_entry{
		{cmp_key(1), cmp_val(1)},
		{cmp_key(2), cmp_val(2)},
		{cmp_key(3), cmp_val(3)},
	}
	m1 := cmp_makeMap(t, entries)
	m2 := cmp_makeMap(t, entries)

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !ds.IsEmpty() {
		t.Errorf("identical maps: expected 0 differences, got %d\n%s", ds.Len(), ds.String())
	}
	if !ds.Complete {
		t.Error("Complete should be true for identical maps")
	}
}

func TestCmpBothEmpty(t *testing.T) {
	m1 := New(TypeState)
	m2 := New(TypeState)

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !ds.IsEmpty() {
		t.Errorf("both empty: expected 0 differences, got %d", ds.Len())
	}
}

func TestCmpAddedKeys(t *testing.T) {
	m1 := cmp_makeMap(t, []cmp_entry{
		{cmp_key(10), cmp_val(10)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{
		{cmp_key(10), cmp_val(10)},
		{cmp_key(20), cmp_val(20)},
		{cmp_key(30), cmp_val(30)},
	})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	added, removed, modified := cmp_diffsByType(ds)
	if len(added) != 2 {
		t.Errorf("expected 2 added, got %d", len(added))
	}
	if len(removed) != 0 {
		t.Errorf("expected 0 removed, got %d", len(removed))
	}
	if len(modified) != 0 {
		t.Errorf("expected 0 modified, got %d", len(modified))
	}
}

func TestCmpRemovedKeys(t *testing.T) {
	m1 := cmp_makeMap(t, []cmp_entry{
		{cmp_key(10), cmp_val(10)},
		{cmp_key(20), cmp_val(20)},
		{cmp_key(30), cmp_val(30)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{
		{cmp_key(10), cmp_val(10)},
	})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	added, removed, modified := cmp_diffsByType(ds)
	if len(removed) != 2 {
		t.Errorf("expected 2 removed, got %d", len(removed))
	}
	if len(added) != 0 {
		t.Errorf("expected 0 added, got %d", len(added))
	}
	if len(modified) != 0 {
		t.Errorf("expected 0 modified, got %d", len(modified))
	}
}

func TestCmpModifiedValues(t *testing.T) {
	k := cmp_key(42)
	m1 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(1)}})
	m2 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(2)}})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	_, _, modified := cmp_diffsByType(ds)
	if len(modified) != 1 {
		t.Fatalf("expected 1 modified, got %d", len(modified))
	}
	if modified[0] != k {
		t.Errorf("modified key mismatch: got %x want %x", modified[0], k)
	}
	for _, d := range ds.Differences {
		if d.Type == DiffModified {
			if d.FirstItem == nil || d.SecondItem == nil {
				t.Error("DiffModified: FirstItem and SecondItem must both be non-nil")
			}
		}
	}
}

func TestCmpFirstMapEmpty(t *testing.T) {
	m1 := New(TypeState)
	m2 := cmp_makeMap(t, []cmp_entry{
		{cmp_key(5), cmp_val(5)},
		{cmp_key(6), cmp_val(6)},
	})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	added, removed, _ := cmp_diffsByType(ds)
	if len(added) != 2 {
		t.Errorf("expected 2 added, got %d", len(added))
	}
	if len(removed) != 0 {
		t.Errorf("expected 0 removed, got %d", len(removed))
	}
}

func TestCmpSecondMapEmpty(t *testing.T) {
	m1 := cmp_makeMap(t, []cmp_entry{
		{cmp_key(5), cmp_val(5)},
		{cmp_key(6), cmp_val(6)},
	})
	m2 := New(TypeState)

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	added, removed, _ := cmp_diffsByType(ds)
	if len(removed) != 2 {
		t.Errorf("expected 2 removed, got %d", len(removed))
	}
	if len(added) != 0 {
		t.Errorf("expected 0 added, got %d", len(added))
	}
}

func TestCmpDisjointSets(t *testing.T) {
	m1 := cmp_makeMap(t, []cmp_entry{
		{cmp_key(1), cmp_val(1)},
		{cmp_key(2), cmp_val(2)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{
		{cmp_key(100), cmp_val(100)},
		{cmp_key(101), cmp_val(101)},
	})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	added, removed, modified := cmp_diffsByType(ds)
	if len(added) != 2 {
		t.Errorf("expected 2 added, got %d", len(added))
	}
	if len(removed) != 2 {
		t.Errorf("expected 2 removed, got %d", len(removed))
	}
	if len(modified) != 0 {
		t.Errorf("expected 0 modified, got %d", len(modified))
	}
}

func TestCmpMixedDifferences(t *testing.T) {
	kCommon := cmp_key(50)
	kRemoved := cmp_key(60)
	kAdded := cmp_key(70)
	kModified := cmp_key(80)

	m1 := cmp_makeMap(t, []cmp_entry{
		{kCommon, cmp_val(50)},
		{kRemoved, cmp_val(60)},
		{kModified, cmp_val(1)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{
		{kCommon, cmp_val(50)},
		{kAdded, cmp_val(70)},
		{kModified, cmp_val(2)},
	})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	added, removed, modified := cmp_diffsByType(ds)
	if len(added) != 1 {
		t.Errorf("added: want 1, got %d", len(added))
	}
	if len(removed) != 1 {
		t.Errorf("removed: want 1, got %d", len(removed))
	}
	if len(modified) != 1 {
		t.Errorf("modified: want 1, got %d", len(modified))
	}
	if len(added) == 1 && added[0] != kAdded {
		t.Errorf("added key mismatch: got %x want %x", added[0], kAdded)
	}
	if len(removed) == 1 && removed[0] != kRemoved {
		t.Errorf("removed key mismatch: got %x want %x", removed[0], kRemoved)
	}
	if len(modified) == 1 && modified[0] != kModified {
		t.Errorf("modified key mismatch: got %x want %x", modified[0], kModified)
	}
}

func TestCmpMaxCountTruncation(t *testing.T) {
	m1 := New(TypeState)
	var entries []cmp_entry
	for i := byte(0); i < 10; i++ {
		entries = append(entries, cmp_entry{cmp_key(i + 100), cmp_val(i)})
	}
	m2 := cmp_makeMap(t, entries)

	ds, err := m1.Compare(m2, 3)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if ds.Len() > 3 {
		t.Errorf("expected at most 3 differences with maxCount=3, got %d", ds.Len())
	}
	if ds.Complete {
		t.Error("Complete should be false when truncated by maxCount")
	}
	if !ds.HasMore() {
		t.Error("HasMore() should return true when truncated")
	}
}

func TestCmpMaxCountZeroNoLimit(t *testing.T) {
	var entries []cmp_entry
	for i := byte(0); i < 20; i++ {
		entries = append(entries, cmp_entry{cmp_key(i + 50), cmp_val(i)})
	}
	m1 := cmp_makeMap(t, entries)
	m2 := cmp_makeMap(t, entries)

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if ds.Len() != 0 {
		t.Errorf("expected 0 differences for identical maps, got %d", ds.Len())
	}
	if !ds.Complete {
		t.Error("Complete should be true when maps are identical (no truncation)")
	}
}

func TestCmpInvalidMapError(t *testing.T) {
	valid := New(TypeState)
	invalid := New(TypeState)
	invalid.state = StateInvalid

	_, err := valid.Compare(invalid, 0)
	if err == nil {
		t.Error("Compare with invalid map should return error")
	}

	_, err = invalid.Compare(valid, 0)
	if err == nil {
		t.Error("Compare with invalid self should return error")
	}
}

func TestCmpLargeMaps(t *testing.T) {
	const n = 100
	var common []cmp_entry
	for i := byte(0); i < n; i++ {
		var k [32]byte
		k[0] = i
		k[1] = i ^ 0xAA
		v := []byte{i, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		common = append(common, cmp_entry{k, v})
	}

	m1 := cmp_makeMap(t, common)
	m2 := cmp_makeMap(t, common)

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare large identical: %v", err)
	}
	if !ds.IsEmpty() {
		t.Errorf("large identical maps: expected 0 differences, got %d", ds.Len())
	}

	for i := byte(0); i < n/2; i++ {
		var k [32]byte
		k[0] = i
		k[1] = i ^ 0xAA
		v := []byte{i + 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		if err := m2.Put(k, v); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	ds, err = m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare large modified: %v", err)
	}
	_, _, modified := cmp_diffsByType(ds)
	if len(modified) != n/2 {
		t.Errorf("expected %d modified entries, got %d", n/2, len(modified))
	}
}

func TestCmpDifferenceSetString(t *testing.T) {
	m1 := cmp_makeMap(t, []cmp_entry{{cmp_key(1), cmp_val(1)}})
	m2 := cmp_makeMap(t, []cmp_entry{{cmp_key(2), cmp_val(2)}})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}

	s := ds.String()
	if len(s) == 0 {
		t.Error("DifferenceSet.String() should not be empty")
	}
	if ds.HasMore() {
		t.Error("HasMore() should be false when not truncated")
	}
}

func TestCmpDifferenceItemString(t *testing.T) {
	k := cmp_key(77)
	di := DifferenceItem{Key: k, Type: DiffAdded}
	s := di.String()
	if len(s) == 0 {
		t.Error("DifferenceItem.String() should not be empty")
	}
}

func TestCmpDifferenceTypeString(t *testing.T) {
	tests := []struct {
		dt   DifferenceType
		want string
	}{
		{DiffAdded, "added"},
		{DiffRemoved, "removed"},
		{DiffModified, "modified"},
		{DifferenceType(99), "unknown(99)"},
	}
	for _, tt := range tests {
		got := tt.dt.String()
		if got != tt.want {
			t.Errorf("DifferenceType(%d).String() = %q, want %q", int(tt.dt), got, tt.want)
		}
	}
}

func TestCmpFirstItemsPopulated(t *testing.T) {
	k := cmp_key(55)
	m1 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(55)}})
	m2 := New(TypeState)

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	for _, d := range ds.Differences {
		if d.Type != DiffRemoved {
			t.Errorf("unexpected diff type %s", d.Type)
			continue
		}
		if d.FirstItem == nil {
			t.Error("DiffRemoved: FirstItem must not be nil")
		}
		if d.SecondItem != nil {
			t.Error("DiffRemoved: SecondItem must be nil")
		}
		if !bytes.Equal(d.FirstItem.DataUnsafe(), cmp_val(55)) {
			t.Error("DiffRemoved: FirstItem data mismatch")
		}
	}
}

func TestCmpSecondItemsPopulated(t *testing.T) {
	k := cmp_key(66)
	m1 := New(TypeState)
	m2 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(66)}})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	for _, d := range ds.Differences {
		if d.Type != DiffAdded {
			t.Errorf("unexpected diff type %s", d.Type)
			continue
		}
		if d.SecondItem == nil {
			t.Error("DiffAdded: SecondItem must not be nil")
		}
		if d.FirstItem != nil {
			t.Error("DiffAdded: FirstItem must be nil")
		}
		if !bytes.Equal(d.SecondItem.DataUnsafe(), cmp_val(66)) {
			t.Error("DiffAdded: SecondItem data mismatch")
		}
	}
}

func TestCmpStructurallyDifferentDepths(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k2 := hexToHash("b92691fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k3 := hexToHash("b92791fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	m1 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
		{k2, cmp_val(3)},
		{k3, cmp_val(4)},
	})
	kOther := hexToHash("f22891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	m2 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{kOther, cmp_val(99)},
	})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare deep structure: %v", err)
	}
	if ds.IsEmpty() {
		t.Error("deep structure: expected non-zero differences")
	}
}

func TestCmpSingleItemBothMaps(t *testing.T) {
	k := cmp_key(33)

	m1 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(33)}})
	m2 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(33)}})
	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !ds.IsEmpty() {
		t.Errorf("same single-item maps: expected 0 diffs, got %d", ds.Len())
	}

	m3 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(34)}})
	ds, err = m1.Compare(m3, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if ds.Len() != 1 {
		t.Errorf("modified single-item: expected 1 diff, got %d", ds.Len())
	}
}

func TestCmpLeafVsInner(t *testing.T) {
	// These keys share the same first nibble (0xb9) so they collide deep in the tree.
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k2 := hexToHash("b92691fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k3 := hexToHash("b92791fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	m1 := cmp_makeMap(t, []cmp_entry{{k0, cmp_val(1)}})
	m2 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
		{k2, cmp_val(3)},
		{k3, cmp_val(4)},
	})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare leaf-vs-inner: %v", err)
	}
	added, removed, modified := cmp_diffsByType(ds)
	if len(added) != 3 {
		t.Errorf("leaf-vs-inner: expected 3 added, got %d", len(added))
	}
	if len(removed) != 0 {
		t.Errorf("leaf-vs-inner: expected 0 removed, got %d", len(removed))
	}
	if len(modified) != 0 {
		t.Errorf("leaf-vs-inner: expected 0 modified, got %d", len(modified))
	}
}

func TestCmpInnerVsLeaf(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k2 := hexToHash("b92691fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k3 := hexToHash("b92791fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	m1 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
		{k2, cmp_val(3)},
		{k3, cmp_val(4)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{{k0, cmp_val(1)}})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare inner-vs-leaf: %v", err)
	}
	added, removed, modified := cmp_diffsByType(ds)
	if len(removed) != 3 {
		t.Errorf("inner-vs-leaf: expected 3 removed, got %d", len(removed))
	}
	if len(added) != 0 {
		t.Errorf("inner-vs-leaf: expected 0 added, got %d", len(added))
	}
	if len(modified) != 0 {
		t.Errorf("inner-vs-leaf: expected 0 modified, got %d", len(modified))
	}
}

func TestCmpWalkBranchMatchedItem(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	m1 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(10)},
		{k1, cmp_val(11)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{{k0, cmp_val(10)}})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare walkBranch-match: %v", err)
	}
	added, removed, modified := cmp_diffsByType(ds)
	if len(removed) != 1 {
		t.Errorf("walkBranch-match: expected 1 removed (k1), got %d", len(removed))
	}
	if len(added) != 0 {
		t.Errorf("walkBranch-match: expected 0 added, got %d", len(added))
	}
	if len(modified) != 0 {
		t.Errorf("walkBranch-match: expected 0 modified, got %d", len(modified))
	}
}

func TestCmpWalkBranchModifiedItem(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	m1 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{{k0, cmp_val(99)}})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare walkBranch-modified: %v", err)
	}
	added, removed, modified := cmp_diffsByType(ds)
	if len(modified) != 1 {
		t.Errorf("walkBranch-modified: expected 1 modified, got %d", len(modified))
	}
	if len(removed) != 1 {
		t.Errorf("walkBranch-modified: expected 1 removed (k1), got %d", len(removed))
	}
	if len(added) != 0 {
		t.Errorf("walkBranch-modified: expected 0 added, got %d", len(added))
	}
}

func TestCmpWalkBranchOtherItemUnmatched(t *testing.T) {
	// These two keys share the same leading nibbles, so they go into the same deep subtree.
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	// This key starts with b9 but diverges immediately at byte 2 → same root branch, different sub-branch
	kOther := hexToHash("b99891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	m1 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{{kOther, cmp_val(3)}})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare unmatched: %v", err)
	}
	if ds.IsEmpty() {
		t.Error("unmatched: expected differences, got none")
	}
}

func TestCmpCompareMaxCountOnInnerNodes(t *testing.T) {
	var entries []cmp_entry
	for i := byte(0); i < 30; i++ {
		var k [32]byte
		k[0] = i * 7
		k[1] = i
		entries = append(entries, cmp_entry{k, cmp_val(i)})
	}
	m1 := New(TypeState)
	m2 := cmp_makeMap(t, entries)

	ds, err := m1.Compare(m2, 5)
	if err != nil {
		t.Fatalf("Compare maxCount inner: %v", err)
	}
	if ds.Len() > 5 {
		t.Errorf("maxCount=5: got %d diffs, expected at most 5", ds.Len())
	}
}

func TestCmpLeafMaxCountTruncation(t *testing.T) {
	k1 := cmp_key(11)
	k2 := cmp_key(12)
	m1 := cmp_makeMap(t, []cmp_entry{{k1, cmp_val(11)}})
	m2 := cmp_makeMap(t, []cmp_entry{{k2, cmp_val(12)}})

	ds, err := m1.Compare(m2, 1)
	if err != nil {
		t.Fatalf("Compare leaf maxCount: %v", err)
	}
	if ds.Len() > 1 {
		t.Errorf("leaf maxCount=1: expected at most 1 diff, got %d", ds.Len())
	}
}

func TestCmpModifiedMaxCountTruncation(t *testing.T) {
	k := cmp_key(42)
	m1 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(1)}})
	m2 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(2)}})

	ds, err := m1.Compare(m2, 1)
	if err != nil {
		t.Fatalf("Compare modified maxCount: %v", err)
	}
	if ds.Len() != 1 {
		t.Errorf("modified maxCount=1: expected 1 diff, got %d", ds.Len())
	}
	if ds.Complete {
		t.Error("Complete should be false when maxCount reached on modified leaf")
	}
}

func TestCmpLeafVsInnerModified(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	m1 := cmp_makeMap(t, []cmp_entry{{k0, cmp_val(99)}})
	m2 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
	})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare leaf-vs-inner modified: %v", err)
	}
	added, removed, modified := cmp_diffsByType(ds)
	if len(modified) != 1 {
		t.Errorf("leaf-vs-inner modified: expected 1 modified, got %d (added=%d removed=%d)", len(modified), len(added), len(removed))
	}
	for _, d := range ds.Differences {
		if d.Type == DiffModified {
			if d.FirstItem == nil || d.SecondItem == nil {
				t.Error("modified entry must have both FirstItem and SecondItem set")
			}
		}
	}
}

func TestCmpLeafVsInnerNoMatch(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k2 := hexToHash("b92691fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	// kOurs shares the same first byte but diverges at byte 1 — it would end up
	// in the same root branch (0xb9...) but go elsewhere inside the subtree.
	kOurs := hexToHash("b99891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	m1 := cmp_makeMap(t, []cmp_entry{{kOurs, cmp_val(1)}})
	m2 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
		{k2, cmp_val(3)},
	})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare leaf-vs-inner no match: %v", err)
	}
	if ds.IsEmpty() {
		t.Error("leaf-vs-inner no match: expected differences")
	}
}

func TestCmpWalkBranchMaxCountAfterModified(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	m1 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{{k0, cmp_val(99)}})

	ds, err := m1.Compare(m2, 1)
	if err != nil {
		t.Fatalf("Compare walkBranch maxCount modified: %v", err)
	}
	if ds.Len() > 1 {
		t.Errorf("maxCount=1 walkBranch modified: expected at most 1 diff, got %d", ds.Len())
	}
}

func TestCmpWalkBranchPostLoopAdded(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	kNew := hexToHash("b99891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	m1 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{{kNew, cmp_val(3)}})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare walkBranch post-loop added: %v", err)
	}
	if ds.IsEmpty() {
		t.Error("walkBranch post-loop: expected differences")
	}
}

func TestCmpWalkBranchPostLoopMaxCount(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	kNew := hexToHash("b99891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	m1 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{{kNew, cmp_val(3)}})

	ds, err := m1.Compare(m2, 3)
	if err != nil {
		t.Fatalf("Compare walkBranch post-loop maxCount: %v", err)
	}
	if ds.Len() > 3 {
		t.Errorf("walkBranch post-loop maxCount=3: expected at most 3, got %d", ds.Len())
	}
}

func TestCmpInnerCompareMaxCountTruncation(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k2 := hexToHash("b92691fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	m1 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
		{k2, cmp_val(3)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{{k0, cmp_val(99)}})

	ds, err := m1.Compare(m2, 1)
	if err != nil {
		t.Fatalf("Compare inner-vs-leaf maxCount: %v", err)
	}
	if ds.Len() > 1 {
		t.Errorf("inner-vs-leaf maxCount=1: expected at most 1, got %d", ds.Len())
	}

	m3 := cmp_makeMap(t, []cmp_entry{{k0, cmp_val(99)}})
	m4 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
		{k2, cmp_val(3)},
	})

	ds, err = m3.Compare(m4, 1)
	if err != nil {
		t.Fatalf("Compare leaf-vs-inner maxCount: %v", err)
	}
	if ds.Len() > 1 {
		t.Errorf("leaf-vs-inner maxCount=1: expected at most 1, got %d", ds.Len())
	}
}

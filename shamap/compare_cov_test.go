package shamap

import (
	"bytes"
	"testing"
)

// cmp_entry is a key/value pair used to populate test SHAMaps.
type cmp_entry struct {
	key [32]byte
	val []byte
}

// cmp_makeMap creates a TypeState SHAMap populated with the given entries.
func cmp_makeMap(t *testing.T, entries []cmp_entry) *SHAMap {
	t.Helper()
	m, err := New(TypeState)
	if err != nil {
		t.Fatalf("cmp_makeMap: New: %v", err)
	}
	for _, e := range entries {
		if err := m.Put(e.key, e.val); err != nil {
			t.Fatalf("cmp_makeMap: Put: %v", err)
		}
	}
	return m
}

// cmp_key builds a deterministic 32-byte key from a single byte seed.
func cmp_key(seed byte) [32]byte {
	var k [32]byte
	k[0] = seed
	k[31] = seed ^ 0xFF
	return k
}

// cmp_val returns a 12-byte value slice distinct per seed.
func cmp_val(seed byte) []byte {
	return []byte{seed, seed + 1, seed + 2, 0, 0, 0, 0, 0, 0, 0, 0, 0}
}

// cmp_diffsByType partitions a DifferenceSet into added/removed/modified key slices.
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

// TestCmpIdenticalMaps ensures Compare returns zero differences for equal maps.
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

// TestCmpBothEmpty ensures two empty maps report no differences.
func TestCmpBothEmpty(t *testing.T) {
	m1, _ := New(TypeState)
	m2, _ := New(TypeState)

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !ds.IsEmpty() {
		t.Errorf("both empty: expected 0 differences, got %d", ds.Len())
	}
}

// TestCmpAddedKeys exercises the DiffAdded path: m2 has keys not in m1.
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

// TestCmpRemovedKeys exercises the DiffRemoved path: m1 has keys not in m2.
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

// TestCmpModifiedValues exercises the DiffModified path: same key, different value.
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

// TestCmpFirstMapEmpty exercises the case where the first map is empty.
func TestCmpFirstMapEmpty(t *testing.T) {
	m1, _ := New(TypeState)
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

// TestCmpSecondMapEmpty exercises the case where the second map is empty.
func TestCmpSecondMapEmpty(t *testing.T) {
	m1 := cmp_makeMap(t, []cmp_entry{
		{cmp_key(5), cmp_val(5)},
		{cmp_key(6), cmp_val(6)},
	})
	m2, _ := New(TypeState)

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

// TestCmpDisjointSets exercises both added and removed when maps share no keys.
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

// TestCmpMixedDifferences tests a map with added, removed, and modified entries simultaneously.
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

// TestCmpMaxCountTruncation verifies that maxCount limits results and sets Complete=false.
func TestCmpMaxCountTruncation(t *testing.T) {
	m1, _ := New(TypeState)
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

// TestCmpMaxCountZeroNoLimit verifies maxCount=0 means unlimited (no truncation of results).
func TestCmpMaxCountZeroNoLimit(t *testing.T) {
	var entries []cmp_entry
	for i := byte(0); i < 20; i++ {
		entries = append(entries, cmp_entry{cmp_key(i + 50), cmp_val(i)})
	}
	// Use two equal maps to confirm zero differences and Complete=true.
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

// TestCmpInvalidMapError checks that comparing an invalid map returns an error.
func TestCmpInvalidMapError(t *testing.T) {
	valid, _ := New(TypeState)
	invalid, _ := New(TypeState)
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

// TestCmpEqualMethod tests the Equal() convenience method.
func TestCmpEqualMethod(t *testing.T) {
	m1 := cmp_makeMap(t, []cmp_entry{{cmp_key(1), cmp_val(1)}})
	m2 := cmp_makeMap(t, []cmp_entry{{cmp_key(1), cmp_val(1)}})
	m3 := cmp_makeMap(t, []cmp_entry{{cmp_key(1), cmp_val(2)}})

	eq, err := m1.Equal(m2)
	if err != nil {
		t.Fatalf("Equal: %v", err)
	}
	if !eq {
		t.Error("identical maps should be Equal")
	}

	eq, err = m1.Equal(m3)
	if err != nil {
		t.Fatalf("Equal: %v", err)
	}
	if eq {
		t.Error("different maps should not be Equal")
	}
}

// TestCmpEqualInvalidMap checks that Equal returns error on invalid maps.
func TestCmpEqualInvalidMap(t *testing.T) {
	valid, _ := New(TypeState)
	invalid, _ := New(TypeState)
	invalid.state = StateInvalid

	_, err := valid.Equal(invalid)
	if err == nil {
		t.Error("Equal with invalid map should error")
	}
}

// TestCmpDeepEqual tests DeepEqual() for identical and different maps.
func TestCmpDeepEqual(t *testing.T) {
	m1 := cmp_makeMap(t, []cmp_entry{
		{cmp_key(7), cmp_val(7)},
		{cmp_key(8), cmp_val(8)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{
		{cmp_key(7), cmp_val(7)},
		{cmp_key(8), cmp_val(8)},
	})
	m3 := cmp_makeMap(t, []cmp_entry{
		{cmp_key(7), cmp_val(9)},
	})

	eq, err := m1.DeepEqual(m2)
	if err != nil {
		t.Fatalf("DeepEqual: %v", err)
	}
	if !eq {
		t.Error("DeepEqual: identical maps should return true")
	}

	eq, err = m1.DeepEqual(m3)
	if err != nil {
		t.Fatalf("DeepEqual: %v", err)
	}
	if eq {
		t.Error("DeepEqual: different maps should return false")
	}
}

// TestCmpDeepEqualInvalid checks DeepEqual error on invalid maps.
func TestCmpDeepEqualInvalid(t *testing.T) {
	valid, _ := New(TypeState)
	invalid, _ := New(TypeState)
	invalid.state = StateInvalid

	_, err := valid.DeepEqual(invalid)
	if err == nil {
		t.Error("DeepEqual with invalid map should error")
	}
}

// TestCmpHasDifferences tests HasDifferences() convenience method.
func TestCmpHasDifferences(t *testing.T) {
	m1 := cmp_makeMap(t, []cmp_entry{{cmp_key(1), cmp_val(1)}})
	m2 := cmp_makeMap(t, []cmp_entry{{cmp_key(1), cmp_val(1)}})
	m3 := cmp_makeMap(t, []cmp_entry{{cmp_key(1), cmp_val(2)}})

	hasDiff, err := m1.HasDifferences(m2)
	if err != nil {
		t.Fatalf("HasDifferences: %v", err)
	}
	if hasDiff {
		t.Error("identical maps should have no differences")
	}

	hasDiff, err = m1.HasDifferences(m3)
	if err != nil {
		t.Fatalf("HasDifferences: %v", err)
	}
	if !hasDiff {
		t.Error("different maps should have differences")
	}
}

// TestCmpDifferencesChannel tests the Differences() channel-based API with identical maps.
// Note: Differences() uses an unbuffered channel with non-blocking sends internally;
// it reliably reports zero differences on identical maps.
func TestCmpDifferencesChannel(t *testing.T) {
	k := cmp_key(10)
	m1 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(10)}})
	m2 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(10)}})

	count := 0
	for range m1.Differences(m2) {
		count++
	}
	if count != 0 {
		t.Errorf("Differences() on identical maps: want 0, got %d", count)
	}
}

// TestCmpDifferencesWithErrorBuffered tests DifferencesWithError with a buffered channel
// to reliably receive all differences.
func TestCmpDifferencesWithErrorBuffered(t *testing.T) {
	kRemoved := cmp_key(10)
	kAdded := cmp_key(20)
	kModified := cmp_key(30)
	kCommon := cmp_key(40)

	m1 := cmp_makeMap(t, []cmp_entry{
		{kRemoved, cmp_val(10)},
		{kModified, cmp_val(1)},
		{kCommon, cmp_val(40)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{
		{kAdded, cmp_val(20)},
		{kModified, cmp_val(2)},
		{kCommon, cmp_val(40)},
	})

	ch := make(chan DifferenceItem, 100)
	err := m1.DifferencesWithError(m2, ch)
	close(ch)
	if err != nil {
		t.Fatalf("DifferencesWithError: %v", err)
	}

	var added, removed, modified int
	for d := range ch {
		switch d.Type {
		case DiffAdded:
			added++
		case DiffRemoved:
			removed++
		case DiffModified:
			modified++
		}
	}

	if added != 1 {
		t.Errorf("buffered channel: want 1 added, got %d", added)
	}
	if removed != 1 {
		t.Errorf("buffered channel: want 1 removed, got %d", removed)
	}
	if modified != 1 {
		t.Errorf("buffered channel: want 1 modified, got %d", modified)
	}
}

// TestCmpDifferencesIdenticalChannel verifies Differences() sends nothing for equal maps.
func TestCmpDifferencesIdenticalChannel(t *testing.T) {
	m1 := cmp_makeMap(t, []cmp_entry{{cmp_key(5), cmp_val(5)}})
	m2 := cmp_makeMap(t, []cmp_entry{{cmp_key(5), cmp_val(5)}})

	count := 0
	for range m1.Differences(m2) {
		count++
	}
	if count != 0 {
		t.Errorf("Differences channel: identical maps: expected 0, got %d", count)
	}
}

// TestCmpDifferencesWithError tests the DifferencesWithError API.
func TestCmpDifferencesWithError(t *testing.T) {
	m1 := cmp_makeMap(t, []cmp_entry{{cmp_key(1), cmp_val(1)}})
	m2 := cmp_makeMap(t, []cmp_entry{{cmp_key(2), cmp_val(2)}})

	ch := make(chan DifferenceItem, 100)
	err := m1.DifferencesWithError(m2, ch)
	close(ch)
	if err != nil {
		t.Fatalf("DifferencesWithError: %v", err)
	}

	var count int
	for range ch {
		count++
	}
	if count == 0 {
		t.Error("DifferencesWithError: expected at least one difference")
	}
}

// TestCmpDifferencesWithErrorInvalid ensures DifferencesWithError returns error on invalid map.
func TestCmpDifferencesWithErrorInvalid(t *testing.T) {
	valid, _ := New(TypeState)
	invalid, _ := New(TypeState)
	invalid.state = StateInvalid

	ch := make(chan DifferenceItem, 1)
	err := valid.DifferencesWithError(invalid, ch)
	if err == nil {
		t.Error("DifferencesWithError with invalid map should error")
	}
}

// TestCmpLargeMaps tests compare with many entries to exercise deep inner node paths.
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

	// Modify half the keys in m2
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

// TestCmpDifferenceSetString exercises the String() and HasMore() methods on DifferenceSet.
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

// TestCmpDifferenceItemString exercises DifferenceItem.String().
func TestCmpDifferenceItemString(t *testing.T) {
	k := cmp_key(77)
	di := DifferenceItem{Key: k, Type: DiffAdded}
	s := di.String()
	if len(s) == 0 {
		t.Error("DifferenceItem.String() should not be empty")
	}
}

// TestCmpDifferenceTypeString exercises DifferenceType.String().
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

// TestCmpFirstItemsPopulated verifies that DiffRemoved items have FirstItem set and SecondItem nil.
func TestCmpFirstItemsPopulated(t *testing.T) {
	k := cmp_key(55)
	m1 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(55)}})
	m2, _ := New(TypeState)

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

// TestCmpSecondItemsPopulated verifies that DiffAdded items have SecondItem set and FirstItem nil.
func TestCmpSecondItemsPopulated(t *testing.T) {
	k := cmp_key(66)
	m1, _ := New(TypeState)
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

// TestCmpStructurallyDifferentDepths tests maps where one has deeper inner node structure.
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

// TestCmpDifferencesChannelEmptyMaps verifies Differences() on two empty maps sends nothing.
func TestCmpDifferencesChannelEmptyMaps(t *testing.T) {
	m1, _ := New(TypeState)
	m2, _ := New(TypeState)

	count := 0
	for range m1.Differences(m2) {
		count++
	}
	if count != 0 {
		t.Errorf("empty maps: Differences() should send 0 items, got %d", count)
	}
}

// TestCmpDifferencesWithErrorEmptyMaps verifies DifferencesWithError on identical empty maps is clean.
func TestCmpDifferencesWithErrorEmptyMaps(t *testing.T) {
	m1, _ := New(TypeState)
	m2, _ := New(TypeState)

	ch := make(chan DifferenceItem, 1)
	err := m1.DifferencesWithError(m2, ch)
	close(ch)
	if err != nil {
		t.Fatalf("DifferencesWithError empty: %v", err)
	}
	count := 0
	for range ch {
		count++
	}
	if count != 0 {
		t.Errorf("empty maps: expected 0 differences, got %d", count)
	}
}

// TestCmpSingleItemBothMaps exercises single-item-vs-single-item leaf comparison.
func TestCmpSingleItemBothMaps(t *testing.T) {
	k := cmp_key(33)

	// Same data
	m1 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(33)}})
	m2 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(33)}})
	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !ds.IsEmpty() {
		t.Errorf("same single-item maps: expected 0 diffs, got %d", ds.Len())
	}

	// Different data
	m3 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(34)}})
	ds, err = m1.Compare(m3, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if ds.Len() != 1 {
		t.Errorf("modified single-item: expected 1 diff, got %d", ds.Len())
	}
}

// TestCmpLeafVsInner exercises the path where ourNode is a leaf and otherNode is an inner node.
// This happens when one map has a single item in a subtree and the other has many items
// that share the same leading nibbles, forcing multiple levels of inner nodes.
func TestCmpLeafVsInner(t *testing.T) {
	// These keys share the same first nibble (0xb9) so they collide deep in the tree.
	// m1 has only ONE of them (leaf at depth), m2 has ALL of them (inner node subtree at same position).
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k2 := hexToHash("b92691fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k3 := hexToHash("b92791fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	// m1 has only k0 → at some depth this is a leaf where m2 has an inner node
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
	// k0 matches; k1, k2, k3 are added
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

// TestCmpInnerVsLeaf exercises the reverse: m1 has many keys (inner node subtree),
// m2 has only one of them (single leaf at same position).
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

// TestCmpWalkBranchMatchedItem exercises walkBranch with a matching otherMapItem
// (same key in the subtree being walked, identical data → exact match).
func TestCmpWalkBranchMatchedItem(t *testing.T) {
	// k0 is shared with identical data; k1,k2 only in m1 → inner-vs-leaf scenario
	// where the matching item is k0 which lives inside the inner subtree of m1.
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	m1 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(10)},
		{k1, cmp_val(11)},
	})
	// m2 has only k0 with the same value — walkBranch should find the match and skip it.
	m2 := cmp_makeMap(t, []cmp_entry{{k0, cmp_val(10)}})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare walkBranch-match: %v", err)
	}
	// k0 is matched (no diff), k1 only in m1 → removed
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

// TestCmpWalkBranchModifiedItem exercises walkBranch where the otherMapItem is present
// in the subtree but with different data (DiffModified via walkBranch).
func TestCmpWalkBranchModifiedItem(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	// m1 inner subtree contains k0 with val(1) and k1 with val(2)
	// m2 leaf at same depth = k0 with val(99) → modified
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
	// k0 modified, k1 removed
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

// TestCmpWalkBranchOtherItemUnmatched exercises the post-loop "otherMapItem was unmatched" path.
// This occurs when walkBranch processes all leaves in the inner subtree but the single
// otherMapItem key never appears in that subtree.
func TestCmpWalkBranchOtherItemUnmatched(t *testing.T) {
	// These two keys share the same leading nibbles, so they go into the same deep subtree.
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	// This key starts with b9 but diverges immediately at byte 2 → same root branch, different sub-branch
	kOther := hexToHash("b99891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	// m1 has k0+k1 in a deep subtree
	m1 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
	})
	// m2 has kOther as a single item that ends up at a leaf at the depth
	// where m1 has an inner subtree containing k0 and k1
	m2 := cmp_makeMap(t, []cmp_entry{{kOther, cmp_val(3)}})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare unmatched: %v", err)
	}
	if ds.IsEmpty() {
		t.Error("unmatched: expected differences, got none")
	}
}

// TestCmpChannelLeafVsInner exercises DifferencesWithError with a leaf-vs-inner scenario.
func TestCmpChannelLeafVsInner(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k2 := hexToHash("b92691fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	m1 := cmp_makeMap(t, []cmp_entry{{k0, cmp_val(1)}})
	m2 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
		{k2, cmp_val(3)},
	})

	ch := make(chan DifferenceItem, 100)
	err := m1.DifferencesWithError(m2, ch)
	close(ch)
	if err != nil {
		t.Fatalf("DifferencesWithError leaf-vs-inner: %v", err)
	}

	var added, removed int
	for d := range ch {
		switch d.Type {
		case DiffAdded:
			added++
		case DiffRemoved:
			removed++
		}
	}
	if added != 2 {
		t.Errorf("channel leaf-vs-inner: want 2 added, got %d", added)
	}
	if removed != 0 {
		t.Errorf("channel leaf-vs-inner: want 0 removed, got %d", removed)
	}
}

// TestCmpChannelInnerVsLeaf exercises DifferencesWithError with an inner-vs-leaf scenario.
func TestCmpChannelInnerVsLeaf(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k2 := hexToHash("b92691fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	m1 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
		{k2, cmp_val(3)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{{k0, cmp_val(1)}})

	ch := make(chan DifferenceItem, 100)
	err := m1.DifferencesWithError(m2, ch)
	close(ch)
	if err != nil {
		t.Fatalf("DifferencesWithError inner-vs-leaf: %v", err)
	}

	var removed int
	for d := range ch {
		if d.Type == DiffRemoved {
			removed++
		}
	}
	if removed != 2 {
		t.Errorf("channel inner-vs-leaf: want 2 removed, got %d", removed)
	}
}

// TestCmpChannelModifiedLeaf exercises DifferencesWithError for a modified item via the channel path.
func TestCmpChannelModifiedLeaf(t *testing.T) {
	k := cmp_key(77)
	m1 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(1)}})
	m2 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(2)}})

	ch := make(chan DifferenceItem, 100)
	err := m1.DifferencesWithError(m2, ch)
	close(ch)
	if err != nil {
		t.Fatalf("DifferencesWithError modified leaf: %v", err)
	}

	var modified int
	for d := range ch {
		if d.Type == DiffModified {
			if d.FirstItem == nil || d.SecondItem == nil {
				t.Error("modified via channel: both items must be non-nil")
			}
			modified++
		}
	}
	if modified != 1 {
		t.Errorf("channel modified leaf: want 1, got %d", modified)
	}
}

// TestCmpCompareMaxCountOnInnerNodes exercises maxCount truncation when processing inner nodes.
func TestCmpCompareMaxCountOnInnerNodes(t *testing.T) {
	// Many added keys to ensure multiple branches are explored before truncation.
	var entries []cmp_entry
	for i := byte(0); i < 30; i++ {
		var k [32]byte
		k[0] = byte(i * 7) // spread across different nibbles
		k[1] = byte(i)
		entries = append(entries, cmp_entry{k, cmp_val(i)})
	}
	m1, _ := New(TypeState)
	m2 := cmp_makeMap(t, entries)

	ds, err := m1.Compare(m2, 5)
	if err != nil {
		t.Fatalf("Compare maxCount inner: %v", err)
	}
	if ds.Len() > 5 {
		t.Errorf("maxCount=5: got %d diffs, expected at most 5", ds.Len())
	}
}

// TestCmpDifferencesWithErrorIdentical checks DifferencesWithError returns no diffs for equal maps.
func TestCmpDifferencesWithErrorIdentical(t *testing.T) {
	m1 := cmp_makeMap(t, []cmp_entry{
		{cmp_key(1), cmp_val(1)},
		{cmp_key(2), cmp_val(2)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{
		{cmp_key(1), cmp_val(1)},
		{cmp_key(2), cmp_val(2)},
	})

	ch := make(chan DifferenceItem, 100)
	err := m1.DifferencesWithError(m2, ch)
	close(ch)
	if err != nil {
		t.Fatalf("DifferencesWithError identical: %v", err)
	}
	count := 0
	for range ch {
		count++
	}
	if count != 0 {
		t.Errorf("DifferencesWithError identical: expected 0, got %d", count)
	}
}

// TestCmpLeafMaxCountTruncation triggers maxCount truncation inside handleLeafComparison.
// Two maps with one item each, different keys → two differences. maxCount=1 → truncation.
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

// TestCmpModifiedMaxCountTruncation triggers maxCount truncation after a DiffModified leaf.
func TestCmpModifiedMaxCountTruncation(t *testing.T) {
	k := cmp_key(42)
	m1 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(1)}})
	m2 := cmp_makeMap(t, []cmp_entry{{k, cmp_val(2)}})

	// maxCount=1: the single modified item fills the limit; Complete must be false
	ds, err := m1.Compare(m2, 1)
	if err != nil {
		t.Fatalf("Compare modified maxCount: %v", err)
	}
	if ds.Len() != 1 {
		t.Errorf("modified maxCount=1: expected 1 diff, got %d", ds.Len())
	}
	// Complete should be false because maxCount was hit
	if ds.Complete {
		t.Error("Complete should be false when maxCount reached on modified leaf")
	}
}

// TestCmpLeafVsInnerModified exercises the walkBranch path where isFirstMap=false
// and the otherMapItem key is found in the subtree with different data → DiffModified
// with firstItem=otherMapItem, secondItem=item (lines 309-312 in compare.go).
func TestCmpLeafVsInnerModified(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	// m1 has k0 as a single leaf; m2 has k0 + k1 forming an inner node subtree.
	// When comparing, ourNode(m1)=leaf(k0), otherNode(m2)=inner.
	// We call other.walkBranch(m2Inner, m1Leaf(k0), isFirstMap=false, ...).
	// In the subtree: find k0 with different data → DiffModified with swapped items.
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
	// k0 modified, k1 added (from m2 perspective, but from m1 perspective k1 is added)
	if len(modified) != 1 {
		t.Errorf("leaf-vs-inner modified: expected 1 modified, got %d (added=%d removed=%d)", len(modified), len(added), len(removed))
	}
	// Verify the modified entry has both items set
	for _, d := range ds.Differences {
		if d.Type == DiffModified {
			if d.FirstItem == nil || d.SecondItem == nil {
				t.Error("modified entry must have both FirstItem and SecondItem set")
			}
		}
	}
}

// TestCmpLeafVsInnerNoMatch exercises the post-loop "otherMapItem was unmatched" path
// for isFirstMap=false (lines 343-347): our map has a single leaf whose key doesn't
// exist in the other map's inner subtree.
func TestCmpLeafVsInnerNoMatch(t *testing.T) {
	// k_ours is in m1 at a leaf. m2 has a different set of deeply nested keys
	// that go into the same branch as k_ours, forming an inner node there.
	// k_ours won't appear in m2's inner subtree → post-loop DiffRemoved for k_ours.
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

// TestCmpWalkBranchMaxCountAfterModified exercises the maxCount return inside walkBranch
// after recording a DiffModified (line 316-318 in compare.go).
func TestCmpWalkBranchMaxCountAfterModified(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	// m1 inner subtree with k0+k1; m2 single leaf k0 with different data.
	// walkBranch(m1Inner, k0_val_different, isFirstMap=true, maxCount=1) →
	// on finding k0 with different data, records DiffModified, then hits maxCount.
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

// TestCmpWalkBranchPostLoopIsFirstFalse exercises the post-loop DiffRemoved path
// for isFirstMap=true (lines 338-342): the inner subtree is in m1 (first map),
// we walk it with otherMapItem from m2 that doesn't exist in m1's subtree.
// After the walk, otherMapItem is unmatched → DiffAdded.
func TestCmpWalkBranchPostLoopAdded(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	kNew := hexToHash("b99891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	m1 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
	})
	// m2 has kNew as its only item in the b9 branch; m1 has k0+k1 there.
	m2 := cmp_makeMap(t, []cmp_entry{{kNew, cmp_val(3)}})

	ds, err := m1.Compare(m2, 0)
	if err != nil {
		t.Fatalf("Compare walkBranch post-loop added: %v", err)
	}
	if ds.IsEmpty() {
		t.Error("walkBranch post-loop: expected differences")
	}
}

// TestCmpChannelDisjointSets exercises DifferencesWithError with completely disjoint keys.
func TestCmpChannelDisjointSets(t *testing.T) {
	m1 := cmp_makeMap(t, []cmp_entry{
		{cmp_key(1), cmp_val(1)},
		{cmp_key(2), cmp_val(2)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{
		{cmp_key(100), cmp_val(100)},
		{cmp_key(101), cmp_val(101)},
	})

	ch := make(chan DifferenceItem, 100)
	err := m1.DifferencesWithError(m2, ch)
	close(ch)
	if err != nil {
		t.Fatalf("DifferencesWithError disjoint: %v", err)
	}

	var added, removed int
	for d := range ch {
		switch d.Type {
		case DiffAdded:
			added++
		case DiffRemoved:
			removed++
		}
	}
	if added != 2 || removed != 2 {
		t.Errorf("disjoint channel: want 2 added + 2 removed, got added=%d removed=%d", added, removed)
	}
}

// TestCmpChannelLeafVsInnerModified exercises the channel path for
// ourNode=leaf, otherNode=inner with a modified item.
func TestCmpChannelLeafVsInnerModified(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	m1 := cmp_makeMap(t, []cmp_entry{{k0, cmp_val(99)}})
	m2 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
	})

	ch := make(chan DifferenceItem, 100)
	err := m1.DifferencesWithError(m2, ch)
	close(ch)
	if err != nil {
		t.Fatalf("DifferencesWithError leaf-vs-inner modified: %v", err)
	}

	var modified int
	for d := range ch {
		if d.Type == DiffModified {
			modified++
		}
	}
	if modified != 1 {
		t.Errorf("channel leaf-vs-inner modified: want 1 modified, got %d", modified)
	}
}

// TestCmpChannelWalkBranchPostLoop exercises walkBranchWithChannel post-loop:
// otherMapItem was not found in the inner subtree (DiffAdded via isFirstMap=true).
func TestCmpChannelWalkBranchPostLoop(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	kNew := hexToHash("b99891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	// m1 has k0+k1 in a deep inner subtree; m2 has kNew as a single leaf
	// that maps to the same root branch (0xb9 prefix) but different sub-branch.
	// When DifferencesWithError runs:
	//   ourNode(m1)=inner, otherNode(m2)=leaf(kNew)
	//   → walkBranchWithChannel(m1Inner, kNew, isFirstMap=true, ch)
	//   → walks k0 and k1 (neither matches kNew) → post-loop: DiffAdded(kNew)
	m1 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{{kNew, cmp_val(3)}})

	ch := make(chan DifferenceItem, 100)
	err := m1.DifferencesWithError(m2, ch)
	close(ch)
	if err != nil {
		t.Fatalf("DifferencesWithError walkBranchWithChannel post-loop: %v", err)
	}

	var diffs []DifferenceItem
	for d := range ch {
		diffs = append(diffs, d)
	}
	if len(diffs) == 0 {
		t.Error("channel walkBranchWithChannel post-loop: expected at least one difference")
	}
}

// TestCmpChannelWalkBranchPostLoopRemoved exercises walkBranchWithChannel post-loop
// for isFirstMap=false → DiffRemoved (lines 773-777 in compare.go).
func TestCmpChannelWalkBranchPostLoopRemoved(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	kOurs := hexToHash("b99891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	// m1 has kOurs as single leaf; m2 has k0+k1 forming an inner subtree.
	// DifferencesWithError: ourNode(m1)=leaf(kOurs), otherNode(m2)=inner
	// → other.walkBranchWithChannel(m2Inner, kOurs, isFirstMap=false, ch)
	// → walks k0, k1 (neither matches kOurs) → post-loop: DiffRemoved(kOurs)
	m1 := cmp_makeMap(t, []cmp_entry{{kOurs, cmp_val(1)}})
	m2 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(2)},
		{k1, cmp_val(3)},
	})

	ch := make(chan DifferenceItem, 100)
	err := m1.DifferencesWithError(m2, ch)
	close(ch)
	if err != nil {
		t.Fatalf("DifferencesWithError walkBranchWithChannel post-loop removed: %v", err)
	}

	var diffs []DifferenceItem
	for d := range ch {
		diffs = append(diffs, d)
	}
	if len(diffs) == 0 {
		t.Error("channel walkBranchWithChannel post-loop removed: expected at least one difference")
	}
}

// TestCmpChannelWalkBranchMatchedSameData exercises walkBranchWithChannel exact-match path
// where the leaf in the inner subtree matches otherMapItem by key and data.
func TestCmpChannelWalkBranchMatchedSameData(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	// m1 has k0+k1; m2 has only k0 with same value.
	// DifferencesWithError: ourNode(m1)=inner, otherNode(m2)=leaf(k0)
	// walkBranchWithChannel(m1Inner, k0_same, isFirstMap=true)
	// → k0 matches exactly (emptyBranch=true), k1 is unmatched → DiffRemoved(k1)
	m1 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(5)},
		{k1, cmp_val(6)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{{k0, cmp_val(5)}})

	ch := make(chan DifferenceItem, 100)
	err := m1.DifferencesWithError(m2, ch)
	close(ch)
	if err != nil {
		t.Fatalf("DifferencesWithError walkBranchWithChannel exact-match: %v", err)
	}

	var removed int
	for d := range ch {
		if d.Type == DiffRemoved {
			removed++
		}
	}
	if removed != 1 {
		t.Errorf("channel walkBranch exact-match: want 1 removed (k1), got %d", removed)
	}
}

// TestCmpChannelWalkBranchModified exercises walkBranchWithChannel where the matched key
// has different data → DiffModified (line 725-748).
func TestCmpChannelWalkBranchModified(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	// m1 inner subtree: k0 (val=1), k1 (val=2)
	// m2 leaf: k0 (val=99) — different data
	// walkBranchWithChannel(m1Inner, k0_val99, isFirstMap=true)
	// → finds k0 with different data → DiffModified
	m1 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{{k0, cmp_val(99)}})

	ch := make(chan DifferenceItem, 100)
	err := m1.DifferencesWithError(m2, ch)
	close(ch)
	if err != nil {
		t.Fatalf("DifferencesWithError walkBranchWithChannel modified: %v", err)
	}

	var modified, removed int
	for d := range ch {
		switch d.Type {
		case DiffModified:
			modified++
		case DiffRemoved:
			removed++
		}
	}
	if modified != 1 {
		t.Errorf("channel walkBranch modified: want 1 modified, got %d", modified)
	}
	if removed != 1 {
		t.Errorf("channel walkBranch modified: want 1 removed (k1), got %d", removed)
	}
}

// TestCmpWalkBranchPostLoopMaxCount exercises the maxCount check inside the walkBranch
// post-loop (line 351-353): adds a difference right at the limit.
func TestCmpWalkBranchPostLoopMaxCount(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	kNew := hexToHash("b99891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	// m1 has k0+k1; m2 has kNew. maxCount=1 should truncate after the first difference.
	// The walkBranch call processes k0 and k1 as unmatched (kNew not in subtree).
	// If maxCount=1 is hit during the leaf walk, the post-loop is never reached.
	// If maxCount=3 (more than the 2 leaves + post-loop), post-loop difference is the 3rd.
	m1 := cmp_makeMap(t, []cmp_entry{
		{k0, cmp_val(1)},
		{k1, cmp_val(2)},
	})
	m2 := cmp_makeMap(t, []cmp_entry{{kNew, cmp_val(3)}})

	// Use maxCount=3 to allow all leaf differences through and hit the post-loop check.
	ds, err := m1.Compare(m2, 3)
	if err != nil {
		t.Fatalf("Compare walkBranch post-loop maxCount: %v", err)
	}
	// There are 3 differences: k0 removed, k1 removed, kNew added (post-loop).
	// With maxCount=3, the third one triggers the truncation check.
	if ds.Len() > 3 {
		t.Errorf("walkBranch post-loop maxCount=3: expected at most 3, got %d", ds.Len())
	}
}

// TestCmpInnerCompareMaxCountTruncation exercises the maxCount truncation path
// in compareUnsafe when processing inner+leaf (lines 187-193) and leaf+inner (lines 206-212).
func TestCmpInnerCompareMaxCountTruncation(t *testing.T) {
	k0 := hexToHash("b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k1 := hexToHash("b92881fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")
	k2 := hexToHash("b92691fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8")

	// Case 1: inner-vs-leaf with maxCount=1 → truncation during walkBranch
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

	// Case 2: leaf-vs-inner with maxCount=1
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

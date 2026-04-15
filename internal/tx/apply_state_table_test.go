package tx

import (
	"bytes"
	"sort"
	"testing"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/keylet"
)

// mockBaseView implements LedgerView for ApplyStateTable tests.
type mockBaseView struct {
	data map[[32]byte][]byte
}

func newMockBaseView() *mockBaseView {
	return &mockBaseView{data: make(map[[32]byte][]byte)}
}

func (m *mockBaseView) Read(k keylet.Keylet) ([]byte, error) {
	return m.data[k.Key], nil
}

func (m *mockBaseView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := m.data[k.Key]
	return ok, nil
}

func (m *mockBaseView) Insert(k keylet.Keylet, data []byte) error {
	m.data[k.Key] = data
	return nil
}

func (m *mockBaseView) Update(k keylet.Keylet, data []byte) error {
	m.data[k.Key] = data
	return nil
}

func (m *mockBaseView) Erase(k keylet.Keylet) error {
	delete(m.data, k.Key)
	return nil
}

func (m *mockBaseView) AdjustDropsDestroyed(drops.XRPAmount) {}

func (m *mockBaseView) TxExists([32]byte) bool { return false }

func (m *mockBaseView) Rules() *amendment.Rules { return nil }

func (m *mockBaseView) ForEach(fn func(key [32]byte, data []byte) bool) error {
	for k, v := range m.data {
		if !fn(k, v) {
			break
		}
	}
	return nil
}

func (m *mockBaseView) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	var best [32]byte
	found := false
	for k := range m.data {
		if bytes.Compare(k[:], key[:]) > 0 {
			if !found || bytes.Compare(k[:], best[:]) < 0 {
				best = k
				found = true
			}
		}
	}
	if found {
		return best, m.data[best], true, nil
	}
	return [32]byte{}, nil, false, nil
}

// helpers

func key(b byte) [32]byte {
	var k [32]byte
	k[0] = b
	return k
}

func kl(b byte) keylet.Keylet {
	return keylet.Keylet{Key: key(b)}
}

// collectForEach runs ForEach and returns all yielded key-data pairs sorted by key.
func collectForEach(t *testing.T, table *ApplyStateTable) [][2][]byte {
	t.Helper()
	var results [][2][]byte
	err := table.ForEach(func(k [32]byte, data []byte) bool {
		results = append(results, [2][]byte{k[:], data})
		return true
	})
	if err != nil {
		t.Fatalf("ForEach returned error: %v", err)
	}
	sort.Slice(results, func(i, j int) bool {
		return bytes.Compare(results[i][0], results[j][0]) < 0
	})
	return results
}

func TestForEach_BaseOnly(t *testing.T) {
	base := newMockBaseView()
	base.data[key(1)] = []byte("a")
	base.data[key(2)] = []byte("b")

	table := NewApplyStateTable(base, [32]byte{}, 0, nil)
	results := collectForEach(t, table)

	if len(results) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(results))
	}
	if !bytes.Equal(results[0][1], []byte("a")) {
		t.Errorf("expected 'a', got %q", results[0][1])
	}
	if !bytes.Equal(results[1][1], []byte("b")) {
		t.Errorf("expected 'b', got %q", results[1][1])
	}
}

func TestForEach_InsertedEntry(t *testing.T) {
	base := newMockBaseView()
	base.data[key(1)] = []byte("a")

	table := NewApplyStateTable(base, [32]byte{}, 0, nil)
	if err := table.Insert(kl(2), []byte("new")); err != nil {
		t.Fatal(err)
	}

	results := collectForEach(t, table)

	if len(results) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(results))
	}
	if !bytes.Equal(results[1][1], []byte("new")) {
		t.Errorf("expected 'new', got %q", results[1][1])
	}
}

func TestForEach_ModifiedEntry(t *testing.T) {
	base := newMockBaseView()
	base.data[key(1)] = []byte("old")

	table := NewApplyStateTable(base, [32]byte{}, 0, nil)
	// Read first to track it
	if _, err := table.Read(kl(1)); err != nil {
		t.Fatal(err)
	}
	if err := table.Update(kl(1), []byte("updated")); err != nil {
		t.Fatal(err)
	}

	results := collectForEach(t, table)

	if len(results) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(results))
	}
	if !bytes.Equal(results[0][1], []byte("updated")) {
		t.Errorf("expected 'updated', got %q", results[0][1])
	}
}

func TestForEach_ErasedEntry(t *testing.T) {
	base := newMockBaseView()
	base.data[key(1)] = []byte("a")
	base.data[key(2)] = []byte("b")

	table := NewApplyStateTable(base, [32]byte{}, 0, nil)
	if err := table.Erase(kl(1)); err != nil {
		t.Fatal(err)
	}

	results := collectForEach(t, table)

	if len(results) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(results))
	}
	if !bytes.Equal(results[0][1], []byte("b")) {
		t.Errorf("expected 'b', got %q", results[0][1])
	}
}

func TestForEach_CombinedOperations(t *testing.T) {
	base := newMockBaseView()
	base.data[key(1)] = []byte("base1")
	base.data[key(2)] = []byte("base2")
	base.data[key(3)] = []byte("base3")

	table := NewApplyStateTable(base, [32]byte{}, 0, nil)

	// Erase key(1)
	if err := table.Erase(kl(1)); err != nil {
		t.Fatal(err)
	}
	// Modify key(2)
	if _, err := table.Read(kl(2)); err != nil {
		t.Fatal(err)
	}
	if err := table.Update(kl(2), []byte("mod2")); err != nil {
		t.Fatal(err)
	}
	// Insert key(4)
	if err := table.Insert(kl(4), []byte("new4")); err != nil {
		t.Fatal(err)
	}
	// key(3) untouched in base

	results := collectForEach(t, table)

	if len(results) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(results))
	}

	// key(2) = "mod2", key(3) = "base3", key(4) = "new4"
	expected := map[byte]string{
		2: "mod2",
		3: "base3",
		4: "new4",
	}
	for _, r := range results {
		k := r[0][0]
		want, ok := expected[k]
		if !ok {
			t.Errorf("unexpected key %d", k)
			continue
		}
		if !bytes.Equal(r[1], []byte(want)) {
			t.Errorf("key %d: expected %q, got %q", k, want, r[1])
		}
	}
}

func TestForEach_EarlyStop(t *testing.T) {
	base := newMockBaseView()
	base.data[key(1)] = []byte("a")
	base.data[key(2)] = []byte("b")
	base.data[key(3)] = []byte("c")

	table := NewApplyStateTable(base, [32]byte{}, 0, nil)

	count := 0
	err := table.ForEach(func(k [32]byte, data []byte) bool {
		count++
		return count < 2 // stop after 2nd entry
	})
	if err != nil {
		t.Fatalf("ForEach returned error: %v", err)
	}
	if count > 2 {
		t.Errorf("expected early stop after 2, but iterated %d times", count)
	}
}

func TestForEach_NoDuplicates(t *testing.T) {
	base := newMockBaseView()
	base.data[key(1)] = []byte("base")

	table := NewApplyStateTable(base, [32]byte{}, 0, nil)
	// Read key(1) to cache it — it should appear exactly once
	if _, err := table.Read(kl(1)); err != nil {
		t.Fatal(err)
	}

	results := collectForEach(t, table)
	if len(results) != 1 {
		t.Fatalf("expected 1 entry (no duplicate for cached read), got %d", len(results))
	}
}

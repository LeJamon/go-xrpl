package cleaner

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/shamap"
)

// fakeFamily is a controllable in-memory Family the test can mutate to induce
// missing/corrupt nodes.
type fakeFamily struct {
	mu    sync.RWMutex
	store map[[32]byte][]byte
}

func newFakeFamily() *fakeFamily { return &fakeFamily{store: map[[32]byte][]byte{}} }

func (f *fakeFamily) Fetch(_ context.Context, hash [32]byte) ([]byte, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	data, ok := f.store[hash]
	if !ok {
		return nil, nil
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

func (f *fakeFamily) StoreBatch(_ context.Context, entries []shamap.FlushEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range entries {
		cp := make([]byte, len(e.Data))
		copy(cp, e.Data)
		f.store[e.Hash] = cp
	}
	return nil
}

func (f *fakeFamily) delete(h [32]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.store, h)
}

func (f *fakeFamily) deleteOneNonRoot(root [32]byte) (deleted [32]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for h := range f.store {
		if h != root {
			delete(f.store, h)
			return h
		}
	}
	return [32]byte{}
}

// fakeSource implements LedgerSource over a fakeFamily and a per-seq root table.
type fakeSource struct {
	family   *fakeFamily
	roots    map[uint32][2][32]byte // seq -> {stateRoot, txRoot}
	min, max uint32
	hasRange bool
}

func (s *fakeSource) AvailableRange() (uint32, uint32, bool) { return s.min, s.max, s.hasRange }
func (s *fakeSource) Family() shamap.Family                  { return s.family }
func (s *fakeSource) LedgerRoots(seq uint32) ([32]byte, [32]byte, bool) {
	r, ok := s.roots[seq]
	return r[0], r[1], ok
}

// putStateTree builds a state map from keys, flushes it into family, and
// returns the root hash.
func putStateTree(t *testing.T, family *fakeFamily, keys []string) [32]byte {
	t.Helper()
	sm, err := shamap.New(shamap.TypeState)
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range keys {
		var key [32]byte
		copy(key[:], mustHex(t, k))
		data := make([]byte, 16)
		data[0] = byte(i + 1)
		if err := sm.Put(key, data); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	root, err := sm.Hash()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := sm.FlushDirty(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := family.StoreBatch(context.Background(), batch.Entries); err != nil {
		t.Fatal(err)
	}
	return root
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b := make([]byte, 32)
	for i := 0; i < 32 && i*2+1 < len(s); i++ {
		b[i] = hexByte(s[i*2])<<4 | hexByte(s[i*2+1])
	}
	return b
}

func hexByte(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

var sampleKeys = []string{
	"092891fe4ef6cee585fdc6fda0e09eb4d386363158ec3321b8123e5a772c6ca7",
	"436ccbac3347baa1f1e53baeef1f43334da88f1f6d70d963b833afd6dfa289fe",
	"b92891fe4ef6cee585fdc6fda1e09eb4d386363158ec3321b8123e5a772c6ca8",
}

// waitIdle polls until the cleaner reports idle or the deadline passes.
func waitIdle(t *testing.T, c *Cleaner) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := c.Status(); s.State == "idle" && s.LedgersChecked > 0 {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("cleaner did not reach idle in time; status=%+v", c.Status())
	return Status{}
}

func TestCleaner_VerifiesCompleteRange(t *testing.T) {
	family := newFakeFamily()
	root := putStateTree(t, family, sampleKeys)
	src := &fakeSource{
		family:   family,
		roots:    map[uint32][2][32]byte{10: {root, {}}},
		min:      10,
		max:      10,
		hasRange: true,
	}

	c := New(src, nil)
	c.Start()
	defer c.Stop()

	st := c.Clean(Params{Full: true})
	if st.State != "running" {
		t.Fatalf("expected running after Clean, got %q", st.State)
	}

	final := waitIdle(t, c)
	if final.MissingNodes != 0 {
		t.Errorf("expected 0 missing nodes, got %d", final.MissingNodes)
	}
	if final.Failures != 0 {
		t.Errorf("expected 0 failures, got %d", final.Failures)
	}
	if final.NodesChecked == 0 {
		t.Errorf("expected some nodes checked")
	}
	if final.LedgersChecked != 1 {
		t.Errorf("expected 1 ledger checked, got %d", final.LedgersChecked)
	}
}

func TestCleaner_DetectsMissingNode(t *testing.T) {
	family := newFakeFamily()
	root := putStateTree(t, family, sampleKeys)
	if del := family.deleteOneNonRoot(root); del == ([32]byte{}) {
		t.Fatal("failed to delete a non-root node")
	}

	src := &fakeSource{
		family:   family,
		roots:    map[uint32][2][32]byte{10: {root, {}}},
		min:      10,
		max:      10,
		hasRange: true,
	}
	c := New(src, nil)
	c.Start()
	defer c.Stop()

	c.Clean(Params{Full: true})
	final := waitIdle(t, c)
	if final.MissingNodes == 0 {
		t.Errorf("expected missing nodes after deleting one, got 0")
	}
	if final.Failures == 0 {
		t.Errorf("expected failures recorded for an incomplete ledger")
	}
}

func TestCleaner_ShallowSkipsDeepWalk(t *testing.T) {
	family := newFakeFamily()
	root := putStateTree(t, family, sampleKeys)
	// Delete a non-root node: a shallow check only looks at the root, so it
	// should still report the ledger complete.
	family.deleteOneNonRoot(root)

	src := &fakeSource{
		family:   family,
		roots:    map[uint32][2][32]byte{10: {root, {}}},
		min:      10,
		max:      10,
		hasRange: true,
	}
	c := New(src, nil)
	c.Start()
	defer c.Stop()

	c.Clean(Params{CheckNodes: false}) // shallow
	final := waitIdle(t, c)
	if final.MissingNodes != 0 {
		t.Errorf("shallow check should not walk into the deleted node; missing=%d", final.MissingNodes)
	}
	if final.CheckNodes {
		t.Errorf("shallow run should report CheckNodes=false")
	}
}

func TestCleaner_ShallowDetectsMissingRoot(t *testing.T) {
	family := newFakeFamily()
	root := putStateTree(t, family, sampleKeys)
	family.delete(root) // remove the root itself

	src := &fakeSource{
		family:   family,
		roots:    map[uint32][2][32]byte{10: {root, {}}},
		min:      10,
		max:      10,
		hasRange: true,
	}
	c := New(src, nil)
	c.Start()
	defer c.Stop()

	c.Clean(Params{CheckNodes: false})
	final := waitIdle(t, c)
	if final.MissingNodes == 0 {
		t.Errorf("shallow check must detect a missing root")
	}
}

func TestCleaner_RangeDrainsAcrossLedgers(t *testing.T) {
	family := newFakeFamily()
	roots := map[uint32][2][32]byte{}
	for seq := uint32(5); seq <= 8; seq++ {
		roots[seq] = [2][32]byte{putStateTree(t, family, sampleKeys), {}}
	}
	src := &fakeSource{family: family, roots: roots, min: 5, max: 8, hasRange: true}

	c := New(src, nil)
	c.Start()
	defer c.Stop()

	c.Clean(Params{Full: true})
	final := waitIdle(t, c)
	if final.LedgersChecked != 4 {
		t.Errorf("expected 4 ledgers checked across the range, got %d", final.LedgersChecked)
	}
	if final.MissingNodes != 0 || final.Failures != 0 {
		t.Errorf("expected a clean drain, got missing=%d failures=%d", final.MissingNodes, final.Failures)
	}
}

func TestCleaner_StopHaltsRun(t *testing.T) {
	src := &fakeSource{family: newFakeFamily(), roots: map[uint32][2][32]byte{}, min: 1, max: 100, hasRange: true}
	c := New(src, nil)
	c.Start()
	c.Clean(Params{Full: true})
	st := c.Clean(Params{Stop: true})
	if st.State != "idle" {
		t.Errorf("expected idle after Stop param, got %q", st.State)
	}
	c.Stop() // must not hang
}

func TestCleaner_NoRangeAvailable(t *testing.T) {
	src := &fakeSource{family: newFakeFamily(), roots: map[uint32][2][32]byte{}, hasRange: false}
	c := New(src, nil)
	c.Start()
	defer c.Stop()
	st := c.Clean(Params{Full: true})
	if st.State != "idle" {
		t.Errorf("expected idle when no range available, got %q", st.State)
	}
	if st.LastError == "" {
		t.Errorf("expected a LastError explaining nothing to verify")
	}
}

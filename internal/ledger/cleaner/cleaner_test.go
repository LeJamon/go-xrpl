package cleaner

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/shamap"
)

// fakeFamily is a controllable in-memory Family the test can mutate to induce
// missing/corrupt nodes.
type fakeFamily struct {
	mu          sync.RWMutex
	store       map[[32]byte][]byte
	beforeFetch func(context.Context, [32]byte) error
}

func newFakeFamily() *fakeFamily { return &fakeFamily{store: map[[32]byte][]byte{}} }

func (f *fakeFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.mu.RLock()
	hook := f.beforeFetch
	data, ok := f.store[hash]
	data = append([]byte(nil), data...)
	f.mu.RUnlock()
	if hook != nil {
		if err := hook(ctx, hash); err != nil {
			return nil, err
		}
	}
	if !ok {
		return nil, nil
	}
	return data, nil
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

func (f *fakeFamily) restore(h [32]byte, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.store[h] = append([]byte(nil), data...)
}

func (f *fakeFamily) replace(h [32]byte, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.store[h] = append([]byte(nil), data...)
}

func (f *fakeFamily) firstNonRoot(root [32]byte) (selected [32]byte) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for h := range f.store {
		if h != root && (selected == ([32]byte{}) || string(h[:]) < string(selected[:])) {
			selected = h
		}
	}
	return selected
}

func (f *fakeFamily) deleteOneNonRoot(root [32]byte) (deleted [32]byte) {
	deleted = f.firstNonRoot(root)
	f.delete(deleted)
	return deleted
}

// fakeSource implements LedgerSource over a fakeFamily and a per-seq root table.
type fakeSource struct {
	family      *fakeFamily
	roots       map[uint32][2][32]byte // seq -> {stateRoot, txRoot}
	min, max    uint32
	hasRange    bool
	reacquire   func(context.Context, uint32) error
	repair      func(context.Context, uint32) error
	canonical   func(context.Context, uint32) ([32]byte, bool, error)
	repairIndex func(context.Context, LedgerData) (bool, error)
	ledger      func(context.Context, uint32) (LedgerData, bool, error)
}

func (s *fakeSource) AvailableRange() (uint32, uint32, bool) { return s.min, s.max, s.hasRange }
func (s *fakeSource) Family() shamap.Family                  { return s.family }
func (s *fakeSource) Ledger(ctx context.Context, seq uint32) (LedgerData, bool, error) {
	if s.ledger != nil {
		return s.ledger(ctx, seq)
	}
	r, ok := s.roots[seq]
	if !ok {
		return LedgerData{}, false, nil
	}
	parent := [32]byte{}
	if seq > 1 {
		parent[0] = byte(seq - 1)
	}
	return LedgerData{
		Sequence:   seq,
		Hash:       [32]byte{byte(seq)},
		ParentHash: parent,
		StateRoot:  r[0],
		TxRoot:     r[1],
	}, true, nil
}
func (s *fakeSource) CanonicalHash(ctx context.Context, seq uint32) ([32]byte, bool, error) {
	if s.canonical != nil {
		return s.canonical(ctx, seq)
	}
	return [32]byte{byte(seq)}, true, nil
}
func (s *fakeSource) RepairLedgerIndex(ctx context.Context, info LedgerData) (bool, error) {
	if s.repairIndex == nil {
		return false, nil
	}
	return s.repairIndex(ctx, info)
}
func (s *fakeSource) Reacquire(ctx context.Context, seq uint32) error {
	if s.reacquire == nil {
		return nil
	}
	return s.reacquire(ctx, seq)
}
func (s *fakeSource) RepairTransactions(ctx context.Context, seq uint32) error {
	if s.repair == nil {
		return nil
	}
	return s.repair(ctx, seq)
}

// putStateTree builds a state map from keys, flushes it into family, and
// returns the root hash.
func putStateTree(t *testing.T, family *fakeFamily, keys []string) [32]byte {
	return putTree(t, family, shamap.TypeState, keys)
}

func putTree(t *testing.T, family *fakeFamily, mapType shamap.Type, keys []string) [32]byte {
	t.Helper()
	sm := shamap.New(mapType)
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
	if err := sm.StoreDirty(func(entries []shamap.FlushEntry) error {
		return family.StoreBatch(context.Background(), entries)
	}); err != nil {
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

func boolPtr(v bool) *bool { return &v }

func uint32Ptr(v uint32) *uint32 { return &v }

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

func TestCleaner_ZeroRangeStopsWithoutLedgerAccess(t *testing.T) {
	tests := []struct {
		name   string
		params Params
	}{
		{name: "ledger", params: Params{Ledger: uint32Ptr(0)}},
		{name: "minimum", params: Params{MinLedger: uint32Ptr(0)}},
		{name: "maximum", params: Params{MaxLedger: uint32Ptr(0)}},
		{
			name: "inverted",
			params: Params{
				MinLedger: uint32Ptr(9),
				MaxLedger: uint32Ptr(8),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var ledgerCalls atomic.Int32
			src := &fakeSource{
				family:   newFakeFamily(),
				min:      1,
				max:      10,
				hasRange: true,
				ledger: func(context.Context, uint32) (LedgerData, bool, error) {
					ledgerCalls.Add(1)
					return LedgerData{}, false, nil
				},
			}
			c := New(src, nil)
			c.Start()
			defer c.Stop()

			c.Clean(test.params)
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				status := c.Status()
				if status.State == "idle" {
					if status.MinLedger != 0 || status.MaxLedger != 0 {
						t.Fatalf("zero range not cleared: %+v", status)
					}
					if calls := ledgerCalls.Load(); calls != 0 {
						t.Fatalf("ledger calls = %d, want 0", calls)
					}
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatalf("cleaner did not stop zero range: %+v", c.Status())
		})
	}
}

func waitFailure(t *testing.T, c *Cleaner) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := c.Status(); s.Failures > 0 {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("cleaner did not report a failure in time; status=%+v", c.Status())
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

	st := c.Clean(Params{Full: boolPtr(true)})
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
	c.retryDelay = time.Millisecond
	c.Start()
	defer c.Stop()

	c.Clean(Params{Full: boolPtr(true)})
	final := waitFailure(t, c)
	if final.MissingNodes == 0 {
		t.Errorf("expected missing nodes after deleting one, got 0")
	}
	if final.Failures == 0 {
		t.Errorf("expected failures recorded for an incomplete ledger")
	}
	if final.MinLedger != 10 || final.MaxLedger != 10 {
		t.Fatalf("failed ledger advanced out of range: %+v", final)
	}
	c.Clean(Params{Stop: true})
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

	checkNodes := false
	c.Clean(Params{CheckNodes: &checkNodes}) // shallow
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
	c.retryDelay = time.Millisecond
	c.Start()
	defer c.Stop()

	checkNodes := false
	c.Clean(Params{CheckNodes: &checkNodes})
	final := waitFailure(t, c)
	if final.MissingNodes == 0 {
		t.Errorf("shallow check must detect a missing root")
	}
	c.Clean(Params{Stop: true})
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

	c.Clean(Params{Full: boolPtr(true)})
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
	c.Clean(Params{Full: boolPtr(true)})
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
	st := c.Clean(Params{Full: boolPtr(true)})
	if st.State != "idle" {
		t.Errorf("expected idle when no range available, got %q", st.State)
	}
	if st.LastError == "" {
		t.Errorf("expected a LastError explaining nothing to verify")
	}
}

func TestCleaner_ReacquiresAndRetriesSameSequence(t *testing.T) {
	family := newFakeFamily()
	root := putStateTree(t, family, sampleKeys)
	src := &fakeSource{
		family:   family,
		roots:    map[uint32][2][32]byte{},
		min:      10,
		max:      10,
		hasRange: true,
	}
	var mu sync.Mutex
	var reacquired []uint32
	src.reacquire = func(_ context.Context, seq uint32) error {
		mu.Lock()
		reacquired = append(reacquired, seq)
		mu.Unlock()
		src.roots[seq] = [2][32]byte{root, {}}
		return nil
	}

	c := New(src, nil)
	c.retryDelay = time.Millisecond
	c.Start()
	defer c.Stop()
	c.Clean(Params{Full: boolPtr(true)})
	final := waitIdle(t, c)

	mu.Lock()
	defer mu.Unlock()
	if len(reacquired) == 0 {
		t.Fatal("incomplete ledger was not reacquired")
	}
	for _, seq := range reacquired {
		if seq != 10 {
			t.Fatalf("reacquired sequence %d, want 10", seq)
		}
	}
	if final.Failures != 0 {
		t.Fatalf("failures after successful retry = %d, want 0", final.Failures)
	}
	if final.LedgersChecked != 1 {
		t.Fatalf("ledgers checked = %d, want 1 successful ledger", final.LedgersChecked)
	}
}

func TestCleaner_ExplicitFlagsOverrideForcedDefaults(t *testing.T) {
	family := newFakeFamily()
	root := putStateTree(t, family, sampleKeys)
	var repairs int
	src := &fakeSource{
		family:   family,
		roots:    map[uint32][2][32]byte{10: {root, {}}},
		min:      10,
		max:      10,
		hasRange: true,
		repair: func(context.Context, uint32) error {
			repairs++
			return nil
		},
	}
	c := New(src, nil)
	c.Start()
	defer c.Stop()

	ledger := uint32(10)
	disabled := false
	st := c.Clean(Params{
		Ledger:     &ledger,
		Full:       boolPtr(true),
		FixTxns:    &disabled,
		CheckNodes: &disabled,
	})
	if st.FixTxns || st.CheckNodes {
		t.Fatalf("explicit false did not override forced defaults: %+v", st)
	}
	waitIdle(t, c)
	if repairs != 0 {
		t.Fatalf("repair calls = %d, want 0", repairs)
	}
}

func TestCleaner_TransactionRepairFailureRetriesWithoutReacquiring(t *testing.T) {
	family := newFakeFamily()
	root := putStateTree(t, family, sampleKeys)
	var mu sync.Mutex
	var repairs, reacquires int
	src := &fakeSource{
		family:   family,
		roots:    map[uint32][2][32]byte{10: {root, {}}},
		min:      10,
		max:      10,
		hasRange: true,
		repair: func(context.Context, uint32) error {
			mu.Lock()
			defer mu.Unlock()
			repairs++
			if repairs == 1 {
				return errors.New("repair failed")
			}
			return nil
		},
		reacquire: func(context.Context, uint32) error {
			mu.Lock()
			reacquires++
			mu.Unlock()
			return nil
		},
	}
	c := New(src, nil)
	c.retryDelay = time.Millisecond
	c.Start()
	defer c.Stop()

	fix := true
	c.Clean(Params{FixTxns: &fix})
	final := waitIdle(t, c)

	mu.Lock()
	defer mu.Unlock()
	if repairs != 2 {
		t.Fatalf("repair attempts = %d, want 2", repairs)
	}
	if reacquires != 0 {
		t.Fatalf("reacquire calls = %d, want 0", reacquires)
	}
	if final.Failures != 0 {
		t.Fatalf("failures after successful repair retry = %d, want 0", final.Failures)
	}
}

func TestCleaner_IndexRepairFailureRetriesWithoutReacquiring(t *testing.T) {
	family := newFakeFamily()
	root := putStateTree(t, family, sampleKeys)
	var repairs, reacquires atomic.Int32
	src := &fakeSource{
		family:   family,
		roots:    map[uint32][2][32]byte{10: {root, {}}},
		min:      10,
		max:      10,
		hasRange: true,
		repairIndex: func(context.Context, LedgerData) (bool, error) {
			if repairs.Add(1) == 1 {
				return false, errors.New("index repair failed")
			}
			return false, nil
		},
		reacquire: func(context.Context, uint32) error {
			reacquires.Add(1)
			return nil
		},
	}
	c := New(src, nil)
	c.retryDelay = time.Millisecond
	c.Start()
	defer c.Stop()

	c.Clean(Params{})
	final := waitIdle(t, c)

	if got := repairs.Load(); got != 2 {
		t.Fatalf("index repair attempts = %d, want 2", got)
	}
	if got := reacquires.Load(); got != 0 {
		t.Fatalf("reacquire calls = %d, want 0", got)
	}
	if final.Failures != 0 {
		t.Fatalf("failures after successful index repair retry = %d, want 0", final.Failures)
	}
}

func TestCleaner_ReconfigureDiscardsCanceledRunResult(t *testing.T) {
	family := newFakeFamily()
	root := putStateTree(t, family, sampleKeys)
	entered := make(chan struct{})
	var once sync.Once
	var repairedMu sync.Mutex
	var repaired []uint32
	src := &fakeSource{
		family: family,
		roots: map[uint32][2][32]byte{
			10: {root, {}},
			20: {root, {}},
		},
		min:      10,
		max:      20,
		hasRange: true,
		repair: func(ctx context.Context, seq uint32) error {
			if seq == 10 {
				once.Do(func() { close(entered) })
				<-ctx.Done()
				return ctx.Err()
			}
			repairedMu.Lock()
			repaired = append(repaired, seq)
			repairedMu.Unlock()
			return nil
		},
	}
	c := New(src, nil)
	c.Start()
	defer c.Stop()

	fix := true
	first := uint32(10)
	c.Clean(Params{Ledger: &first, FixTxns: &fix})
	<-entered
	second := uint32(20)
	c.Clean(Params{Ledger: &second, FixTxns: &fix})
	final := waitIdle(t, c)

	if final.LedgersChecked != 1 || final.Failures != 0 {
		t.Fatalf("stale run changed replacement progress: %+v", final)
	}
	repairedMu.Lock()
	defer repairedMu.Unlock()
	if len(repaired) != 1 || repaired[0] != 20 {
		t.Fatalf("successful repairs = %v, want [20]", repaired)
	}
}

func TestCleaner_StopCommandCancelsBlockingWork(t *testing.T) {
	family := newFakeFamily()
	root := putStateTree(t, family, sampleKeys)
	entered := make(chan struct{})
	canceled := make(chan struct{})
	var once sync.Once
	src := &fakeSource{
		family:   family,
		roots:    map[uint32][2][32]byte{10: {root, {}}},
		min:      10,
		max:      10,
		hasRange: true,
		repair: func(ctx context.Context, _ uint32) error {
			once.Do(func() { close(entered) })
			<-ctx.Done()
			close(canceled)
			return ctx.Err()
		},
	}
	c := New(src, nil)
	c.Start()
	defer c.Stop()
	fix := true
	c.Clean(Params{FixTxns: &fix})
	<-entered

	if st := c.Clean(Params{Stop: true}); st.State != "idle" {
		t.Fatalf("status after stop command = %+v", st)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("stop command did not cancel blocking repair")
	}
}

func TestCleaner_RejectsCanonicalHeaderMismatchBeforeNodeWalk(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*LedgerData)
	}{
		{
			name: "hash",
			mutate: func(info *LedgerData) {
				info.Hash = [32]byte{0xEE}
			},
		},
		{
			name: "parent",
			mutate: func(info *LedgerData) {
				info.ParentHash = [32]byte{0xEE}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			family := newFakeFamily()
			root := putStateTree(t, family, sampleKeys)
			var reacquires atomic.Int32
			src := &fakeSource{
				family:   family,
				roots:    map[uint32][2][32]byte{10: {root, {}}},
				min:      10,
				max:      10,
				hasRange: true,
				reacquire: func(context.Context, uint32) error {
					reacquires.Add(1)
					return nil
				},
			}
			src.ledger = func(_ context.Context, seq uint32) (LedgerData, bool, error) {
				info := LedgerData{
					Sequence:   seq,
					Hash:       [32]byte{byte(seq)},
					ParentHash: [32]byte{byte(seq - 1)},
					StateRoot:  root,
				}
				test.mutate(&info)
				return info, true, nil
			}

			c := New(src, nil)
			c.retryDelay = time.Millisecond
			c.Start()
			defer c.Stop()
			c.Clean(Params{Full: boolPtr(true)})
			final := waitFailure(t, c)
			c.Clean(Params{Stop: true})

			if reacquires.Load() == 0 {
				t.Fatal("canonical header mismatch did not trigger reacquisition")
			}
			if final.NodesChecked != 0 {
				t.Fatalf("nodes checked before canonical header validation: %d", final.NodesChecked)
			}
			if final.MinLedger != 10 || final.MaxLedger != 10 {
				t.Fatalf("mismatched ledger advanced: %+v", final)
			}
		})
	}
}

func TestCleaner_IndexRepairForcesTransactionRewrite(t *testing.T) {
	family := newFakeFamily()
	root := putStateTree(t, family, sampleKeys)
	var indexRepairs, transactionRepairs int
	src := &fakeSource{
		family:   family,
		roots:    map[uint32][2][32]byte{10: {root, {}}},
		min:      10,
		max:      10,
		hasRange: true,
		repairIndex: func(context.Context, LedgerData) (bool, error) {
			indexRepairs++
			return true, nil
		},
		repair: func(context.Context, uint32) error {
			transactionRepairs++
			return nil
		},
	}
	c := New(src, nil)
	c.Start()
	defer c.Stop()

	c.Clean(Params{})
	final := waitIdle(t, c)
	if indexRepairs != 1 {
		t.Fatalf("index repair calls = %d, want 1", indexRepairs)
	}
	if transactionRepairs != 1 {
		t.Fatalf("transaction repair calls = %d, want forced rewrite", transactionRepairs)
	}
	if final.Failures != 0 {
		t.Fatalf("unexpected failures: %+v", final)
	}
}

func TestCleaner_IndexRepairRewriteIntentSurvivesRetry(t *testing.T) {
	family := newFakeFamily()
	root := putStateTree(t, family, sampleKeys)
	var indexRepairs, transactionRepairs atomic.Int32
	src := &fakeSource{
		family:   family,
		roots:    map[uint32][2][32]byte{10: {root, {}}},
		min:      10,
		max:      10,
		hasRange: true,
		repairIndex: func(context.Context, LedgerData) (bool, error) {
			return indexRepairs.Add(1) == 1, nil
		},
		repair: func(context.Context, uint32) error {
			if transactionRepairs.Add(1) == 1 {
				return errors.New("transaction rewrite failed")
			}
			return nil
		},
	}
	c := New(src, nil)
	c.retryDelay = time.Millisecond
	c.Start()
	defer c.Stop()

	c.Clean(Params{})
	final := waitIdle(t, c)
	if got := indexRepairs.Load(); got != 2 {
		t.Fatalf("index checks = %d, want 2", got)
	}
	if got := transactionRepairs.Load(); got != 2 {
		t.Fatalf("transaction rewrite attempts = %d, want 2", got)
	}
	if final.LedgersChecked != 1 || final.Failures != 0 {
		t.Fatalf("unexpected final status: %+v", final)
	}
}

func TestCleaner_IndexRepairRewriteIntentSurvivesTreeReacquisition(t *testing.T) {
	family := newFakeFamily()
	root := putStateTree(t, family, sampleKeys)
	rootData, err := family.Fetch(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	family.delete(root)

	var indexRepairs, transactionRepairs, reacquires atomic.Int32
	src := &fakeSource{
		family:   family,
		roots:    map[uint32][2][32]byte{10: {root, {}}},
		min:      10,
		max:      10,
		hasRange: true,
		repairIndex: func(context.Context, LedgerData) (bool, error) {
			return indexRepairs.Add(1) == 1, nil
		},
		repair: func(context.Context, uint32) error {
			transactionRepairs.Add(1)
			return nil
		},
		reacquire: func(context.Context, uint32) error {
			reacquires.Add(1)
			family.restore(root, rootData)
			return nil
		},
	}
	c := New(src, nil)
	c.retryDelay = time.Millisecond
	c.Start()
	defer c.Stop()

	c.Clean(Params{})
	final := waitIdle(t, c)
	if got := indexRepairs.Load(); got != 2 {
		t.Fatalf("index checks = %d, want 2", got)
	}
	if got := reacquires.Load(); got != 1 {
		t.Fatalf("reacquisitions = %d, want 1", got)
	}
	if got := transactionRepairs.Load(); got != 1 {
		t.Fatalf("transaction rewrites = %d, want 1", got)
	}
	if final.LedgersChecked != 1 || final.Failures != 0 || final.MissingNodes == 0 {
		t.Fatalf("unexpected final status: %+v", final)
	}
}

func TestCleaner_ParameterPrecedenceAndExplicitRanges(t *testing.T) {
	tests := []struct {
		name      string
		available bool
		params    Params
		wantMin   uint32
		wantMax   uint32
		wantCheck bool
		wantFix   bool
	}{
		{
			name:      "available defaults",
			available: true,
			wantMin:   10,
			wantMax:   20,
		},
		{
			name:      "later parameters override earlier ones",
			available: true,
			params: Params{
				Ledger:     uint32Ptr(15),
				MaxLedger:  uint32Ptr(18),
				MinLedger:  uint32Ptr(12),
				Full:       boolPtr(false),
				FixTxns:    boolPtr(true),
				CheckNodes: boolPtr(true),
			},
			wantMin:   12,
			wantMax:   18,
			wantCheck: true,
			wantFix:   true,
		},
		{
			name:      "explicit range outside available defaults",
			available: true,
			params: Params{
				MinLedger: uint32Ptr(1),
				MaxLedger: uint32Ptr(30),
			},
			wantMin: 1,
			wantMax: 30,
		},
		{
			name:      "explicit ledger without available defaults",
			params:    Params{Ledger: uint32Ptr(25)},
			wantMin:   25,
			wantMax:   25,
			wantCheck: true,
			wantFix:   true,
		},
		{
			name: "explicit range without available defaults",
			params: Params{
				MinLedger: uint32Ptr(25),
				MaxLedger: uint32Ptr(30),
			},
			wantMin: 25,
			wantMax: 30,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			src := &fakeSource{
				family:   newFakeFamily(),
				roots:    map[uint32][2][32]byte{},
				min:      10,
				max:      20,
				hasRange: test.available,
			}
			c := New(src, nil)
			status := c.Clean(test.params)
			defer c.Stop()

			if status.State != "running" || status.MinLedger != test.wantMin || status.MaxLedger != test.wantMax {
				t.Fatalf("configured status = %+v, want running range %d-%d", status, test.wantMin, test.wantMax)
			}
			if status.CheckNodes != test.wantCheck || status.FixTxns != test.wantFix {
				t.Fatalf("configured flags = check:%t fix:%t, want check:%t fix:%t",
					status.CheckNodes, status.FixTxns, test.wantCheck, test.wantFix)
			}
		})
	}
}

func TestCleaner_MaxUint32TerminalLedger(t *testing.T) {
	const seq = ^uint32(0)
	family := newFakeFamily()
	root := putStateTree(t, family, sampleKeys)
	src := &fakeSource{
		family:   family,
		roots:    map[uint32][2][32]byte{seq: {root, {}}},
		min:      seq,
		max:      seq,
		hasRange: true,
	}
	c := New(src, nil)
	c.Start()
	defer c.Stop()

	c.Clean(Params{Ledger: uint32Ptr(seq)})
	final := waitIdle(t, c)
	if final.MinLedger != 0 || final.MaxLedger != 0 {
		t.Fatalf("terminal bounds wrapped: %+v", final)
	}
	if final.LedgersChecked != 1 || final.Failures != 0 {
		t.Fatalf("unexpected final status: %+v", final)
	}
}

func TestCleaner_RootFetchObservesRunCancellation(t *testing.T) {
	for _, mapType := range []shamap.Type{shamap.TypeState, shamap.TypeTransaction} {
		t.Run(mapType.String(), func(t *testing.T) {
			for _, action := range []string{"stop", "reconfigure"} {
				t.Run(action, func(t *testing.T) {
					family := newFakeFamily()
					blockedRoot := putTree(t, family, mapType, sampleKeys)
					replacementRoot := putStateTree(t, family, sampleKeys[:2])
					entered := make(chan struct{})
					canceled := make(chan struct{})
					var once sync.Once
					family.beforeFetch = func(ctx context.Context, hash [32]byte) error {
						if hash != blockedRoot {
							return nil
						}
						once.Do(func() { close(entered) })
						<-ctx.Done()
						close(canceled)
						return ctx.Err()
					}
					roots := [2][32]byte{}
					if mapType == shamap.TypeState {
						roots[0] = blockedRoot
					} else {
						roots[1] = blockedRoot
					}
					src := &fakeSource{
						family: family,
						roots: map[uint32][2][32]byte{
							10: roots,
							20: {replacementRoot, {}},
						},
						min:      10,
						max:      20,
						hasRange: true,
					}
					c := New(src, nil)
					c.Start()
					defer c.Stop()
					c.Clean(Params{Ledger: uint32Ptr(10)})

					select {
					case <-entered:
					case <-time.After(time.Second):
						t.Fatal("root fetch did not start")
					}
					if action == "stop" {
						c.Clean(Params{Stop: true})
					} else {
						c.Clean(Params{Ledger: uint32Ptr(20)})
					}
					select {
					case <-canceled:
					case <-time.After(time.Second):
						t.Fatal("root fetch did not observe cancellation")
					}
					if action == "reconfigure" {
						final := waitIdle(t, c)
						if final.LedgersChecked != 1 || final.Failures != 0 {
							t.Fatalf("replacement run failed: %+v", final)
						}
					}
				})
			}
		})
	}
}

func TestCleaner_ReconfigureResetsRunCounters(t *testing.T) {
	family := newFakeFamily()
	root := putStateTree(t, family, sampleKeys)
	missingRoot := [32]byte{0xFF, 0xEE}
	src := &fakeSource{
		family: family,
		roots: map[uint32][2][32]byte{
			10: {missingRoot, {}},
			20: {root, {}},
		},
		min:      10,
		max:      20,
		hasRange: true,
	}
	c := New(src, nil)
	c.retryDelay = time.Millisecond
	c.Start()
	defer c.Stop()

	c.Clean(Params{Ledger: uint32Ptr(10)})
	failed := waitFailure(t, c)
	if failed.MissingNodes == 0 {
		t.Fatalf("first run did not record missing nodes: %+v", failed)
	}
	reconfigured := c.Clean(Params{Ledger: uint32Ptr(20)})
	if reconfigured.Failures != 0 || reconfigured.LedgersChecked != 0 ||
		reconfigured.NodesChecked != 0 || reconfigured.MissingNodes != 0 || reconfigured.LastError != "" {
		t.Fatalf("reconfigured run retained prior counters: %+v", reconfigured)
	}
	final := waitIdle(t, c)
	if final.LedgersChecked != 1 || final.Failures != 0 || final.MissingNodes != 0 {
		t.Fatalf("unexpected replacement status: %+v", final)
	}
}

func TestCleaner_ClassifiesStateAndTransactionTreeFaults(t *testing.T) {
	storageErr := errors.New("injected storage read error")
	for _, mapType := range []shamap.Type{shamap.TypeState, shamap.TypeTransaction} {
		for _, fault := range []struct {
			name       string
			descendant bool
			storage    bool
			apply      func(*fakeFamily, [32]byte)
		}{
			{name: "missing root", apply: func(f *fakeFamily, h [32]byte) { f.delete(h) }},
			{name: "corrupt root", apply: func(f *fakeFamily, h [32]byte) { f.replace(h, []byte{0xFF}) }},
			{name: "missing descendant", descendant: true, apply: func(f *fakeFamily, h [32]byte) { f.delete(h) }},
			{name: "corrupt descendant", descendant: true, apply: func(f *fakeFamily, h [32]byte) { f.replace(h, []byte{0xFF}) }},
			{name: "root storage error", storage: true},
			{name: "descendant storage error", descendant: true, storage: true},
		} {
			t.Run(mapType.String()+"/"+fault.name, func(t *testing.T) {
				family := newFakeFamily()
				root := putTree(t, family, mapType, sampleKeys)
				target := root
				if fault.descendant {
					target = family.firstNonRoot(root)
					if target == ([32]byte{}) {
						t.Fatal("tree has no non-root node")
					}
				}
				if fault.storage {
					family.beforeFetch = func(_ context.Context, hash [32]byte) error {
						if hash == target {
							return storageErr
						}
						return nil
					}
				} else {
					fault.apply(family, target)
				}
				roots := [2][32]byte{}
				if mapType == shamap.TypeState {
					roots[0] = root
				} else {
					roots[1] = root
				}
				var reacquires atomic.Int32
				src := &fakeSource{
					family:   family,
					roots:    map[uint32][2][32]byte{10: roots},
					min:      10,
					max:      10,
					hasRange: true,
					reacquire: func(context.Context, uint32) error {
						reacquires.Add(1)
						return nil
					},
				}
				cleaner := New(src, nil)
				cleaner.retryDelay = time.Millisecond
				cleaner.Start()
				defer cleaner.Stop()
				cleaner.Clean(Params{Full: boolPtr(true)})
				status := waitFailure(t, cleaner)
				cleaner.Clean(Params{Stop: true})

				if reacquires.Load() == 0 {
					t.Fatal("fault did not trigger reacquisition")
				}
				if status.MinLedger != 10 || status.MaxLedger != 10 {
					t.Fatalf("failed ledger advanced: %+v", status)
				}
				if fault.storage {
					if status.MissingNodes != 0 || !strings.Contains(status.LastError, storageErr.Error()) {
						t.Fatalf("storage error was not distinguished: %+v", status)
					}
				} else if status.MissingNodes == 0 {
					t.Fatalf("missing/corrupt node was not counted: %+v", status)
				}
			})
		}
	}
}

func TestCleaner_CleanAfterStopIsRejected(t *testing.T) {
	src := &fakeSource{
		family:   newFakeFamily(),
		roots:    map[uint32][2][32]byte{},
		min:      1,
		max:      1,
		hasRange: true,
	}
	c := New(src, nil)
	c.Start()
	c.Stop()

	st := c.Clean(Params{Full: boolPtr(true)})
	if st.State != "stopped" {
		t.Fatalf("state after Clean on stopped cleaner = %q, want stopped", st.State)
	}
	if st.LastError != "ledger_cleaner: cleaner stopped" {
		t.Fatalf("last error = %q", st.LastError)
	}
	c.Start()
	if st := c.Status(); st.State != "stopped" {
		t.Fatalf("Start revived stopped cleaner: %+v", st)
	}
}

package cleaner_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/cleaner"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/shamap"
)

// ctrlFamily is a content-addressed store the test fully controls, so it can
// induce a missing node by deleting one entry.
type ctrlFamily struct {
	mu    sync.RWMutex
	store map[[32]byte][]byte
}

func newCtrlFamily() *ctrlFamily { return &ctrlFamily{store: map[[32]byte][]byte{}} }

func (f *ctrlFamily) Fetch(_ context.Context, hash [32]byte) ([]byte, error) {
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

func (f *ctrlFamily) StoreBatch(_ context.Context, entries []shamap.FlushEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range entries {
		cp := make([]byte, len(e.Data))
		copy(cp, e.Data)
		f.store[e.Hash] = cp
	}
	return nil
}

func (f *ctrlFamily) deleteOneNonRoot(root [32]byte) (deleted [32]byte) {
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

// integrationSource feeds the cleaner the state root of one real ledger, walked
// against the content-addressed family the test materialised from that ledger.
type integrationSource struct {
	family    shamap.Family
	seq       uint32
	stateRoot [32]byte
}

func (s *integrationSource) AvailableRange() (uint32, uint32, bool) { return s.seq, s.seq, true }
func (s *integrationSource) Family() shamap.Family                  { return s.family }
func (s *integrationSource) LedgerRoots(seq uint32) ([32]byte, [32]byte, bool) {
	if seq != s.seq {
		return [32]byte{}, [32]byte{}, false
	}
	return s.stateRoot, [32]byte{}, true // tx tree empty for this fixture
}

// materializeState rebuilds the closed ledger's state tree into a fresh
// content-addressed family — exactly what the content-addressed persistence
// migration will do at chain-advance time — and asserts the rebuilt root hash
// equals the real ledger's account_hash, proving it is genuinely that ledger's
// state.
func materializeState(t *testing.T, env *jtx.TestEnv) (*ctrlFamily, [32]byte) {
	t.Helper()
	closed := env.LastClosedLedger()
	if closed == nil {
		t.Fatal("no closed ledger")
	}
	wantRoot, err := closed.StateMapHash()
	if err != nil {
		t.Fatalf("StateMapHash: %v", err)
	}

	rebuilt, err := shamap.New(shamap.TypeState)
	if err != nil {
		t.Fatal(err)
	}
	leaves := 0
	if err := closed.ForEach(func(key [32]byte, data []byte) bool {
		if perr := rebuilt.Put(key, data); perr != nil {
			t.Fatalf("Put state entry: %v", perr)
		}
		leaves++
		return true
	}); err != nil {
		t.Fatalf("ForEach state: %v", err)
	}
	if leaves == 0 {
		t.Fatal("expected real ledger state entries, got none")
	}

	gotRoot, err := rebuilt.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != wantRoot {
		t.Fatalf("rebuilt state root %x != ledger account_hash %x", gotRoot[:8], wantRoot[:8])
	}

	family := newCtrlFamily()
	batch, err := rebuilt.FlushDirty(false)
	if err != nil {
		t.Fatalf("FlushDirty: %v", err)
	}
	if err := family.StoreBatch(context.Background(), batch.Entries); err != nil {
		t.Fatalf("StoreBatch: %v", err)
	}
	return family, wantRoot
}

func runToIdle(t *testing.T, c *cleaner.Cleaner) cleaner.Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := c.Status(); s.State == "idle" && s.LedgersChecked > 0 {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("cleaner did not finish; status=%+v", c.Status())
	return cleaner.Status{}
}

// TestLedgerCleaner_VerifiesRealLedger builds a real ledger with funded
// accounts, materialises its state tree into a content-addressed store, and
// confirms the verifier walks it complete.
func TestLedgerCleaner_VerifiesRealLedger(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	env.Fund(alice, bob, carol)
	env.Close()

	family, root := materializeState(t, env)
	src := &integrationSource{family: family, seq: env.LedgerSeq(), stateRoot: root}

	c := cleaner.New(src, nil)
	c.Start()
	defer c.Stop()

	c.Clean(cleaner.Params{Full: true})
	final := runToIdle(t, c)

	if final.MissingNodes != 0 {
		t.Errorf("real ledger should verify complete, got %d missing", final.MissingNodes)
	}
	if final.Failures != 0 {
		t.Errorf("expected 0 failures, got %d", final.Failures)
	}
	if final.NodesChecked == 0 {
		t.Errorf("expected the walk to inspect nodes")
	}
}

// TestLedgerCleaner_DetectsInducedMissingNode removes one node from the store
// of a real ledger and confirms the verifier reports the gap.
func TestLedgerCleaner_DetectsInducedMissingNode(t *testing.T) {
	env := jtx.NewTestEnv(t)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		env.Fund(jtx.NewAccount(name))
	}
	env.Close()

	family, root := materializeState(t, env)
	if del := family.deleteOneNonRoot(root); del == ([32]byte{}) {
		t.Fatal("could not induce a missing node (no non-root node stored)")
	}

	src := &integrationSource{family: family, seq: env.LedgerSeq(), stateRoot: root}
	c := cleaner.New(src, nil)
	c.Start()
	defer c.Stop()

	c.Clean(cleaner.Params{Full: true})
	final := runToIdle(t, c)

	if final.MissingNodes == 0 {
		t.Errorf("expected the verifier to detect the induced missing node")
	}
	if final.Failures == 0 {
		t.Errorf("expected a failure recorded for the incomplete ledger")
	}
}

package rcl

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// fakeArchive captures the OnStale stream and the NoteFullyValidated
// pivot so engine tests can assert against the archive contract without
// pulling in the relational DB.
type fakeArchive struct {
	mu      sync.Mutex
	stale   []*consensus.Validation
	lastSeq atomic.Uint32

	closeCalls atomic.Int32
}

func (f *fakeArchive) OnStale(v *consensus.Validation) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stale = append(f.stale, v)
}

func (f *fakeArchive) NoteFullyValidated(seq uint32) {
	for {
		cur := f.lastSeq.Load()
		if seq <= cur {
			return
		}
		if f.lastSeq.CompareAndSwap(cur, seq) {
			return
		}
	}
}

func (f *fakeArchive) Close(ctx context.Context) error {
	f.closeCalls.Add(1)
	return nil
}

func (f *fakeArchive) staleCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.stale)
}

// TestEngine_SetArchive_PostStart wires an archive AFTER Start; the next
// stale-eviction in the tracker must still reach the archive.
func TestEngine_SetArchive_PostStart(t *testing.T) {
	adaptor := newMockAdaptor()
	engine := NewEngine(adaptor, DefaultConfig())

	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Stop() })

	arc := &fakeArchive{}
	engine.SetArchive(arc)

	// Inject a validation directly on the tracker, then expire it.
	v := &consensus.Validation{
		LedgerSeq: 100,
		LedgerID:  consensus.LedgerID{0x1},
		NodeID:    consensus.NodeID{0x2},
		SignTime:  time.Now(),
		Full:      true,
	}
	if !engine.validationTracker.Add(v) {
		t.Fatal("Add returned false; precondition broken")
	}
	engine.validationTracker.ExpireOld(200)

	if arc.staleCount() != 1 {
		t.Fatalf("archive received %d stale validations, want 1", arc.staleCount())
	}
}

// TestEngine_FullyValidated_TriggersExpireOldAndArchive verifies the
// happy path: a fully-validated callback advances the archive's pivot and
// calls ExpireOld which streams pruned validations into the archive.
func TestEngine_FullyValidated_TriggersExpireOldAndArchive(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.setTrusted([]consensus.NodeID{{1}, {2}, {3}})
	adaptor.quorum = 2
	engine := NewEngine(adaptor, DefaultConfig())

	arc := &fakeArchive{}
	engine.SetArchive(arc)
	engine.SetInMemoryLedgers(50)

	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Stop() })

	now := time.Now()

	// Seed an old validation at seq 100 — well below the cutoff that the
	// fully-validated callback at seq=300 will compute (300-50=250 → seq
	// 100 is stale and must be archived on eviction).
	old := &consensus.Validation{
		LedgerSeq: 100,
		LedgerID:  consensus.LedgerID{0xA},
		NodeID:    consensus.NodeID{0x1},
		SignTime:  now,
		Full:      true,
	}
	if !engine.validationTracker.Add(old) {
		t.Fatal("seed Add returned false")
	}

	// Drive two trusted validations at seq 300 so the tracker fires the
	// fully-validated callback. Quorum=2.
	for _, n := range []consensus.NodeID{{1}, {2}} {
		v := &consensus.Validation{
			LedgerSeq: 300,
			LedgerID:  consensus.LedgerID{0xB},
			NodeID:    n,
			SignTime:  now,
			Full:      true,
		}
		engine.validationTracker.Add(v)
	}

	if got := arc.lastSeq.Load(); got != 300 {
		t.Fatalf("archive lastSeq=%d, want 300", got)
	}
	if arc.staleCount() != 1 {
		t.Fatalf("archive received %d stale rows, want 1 (the seq-100 seed)", arc.staleCount())
	}
}

// TestEngine_Stop_ClosesArchive confirms shutdown drains and closes the
// archive — no validations should be lost across shutdown.
func TestEngine_Stop_ClosesArchive(t *testing.T) {
	adaptor := newMockAdaptor()
	engine := NewEngine(adaptor, DefaultConfig())

	arc := &fakeArchive{}
	engine.SetArchive(arc)

	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := engine.Stop(); err != nil {
		t.Fatal(err)
	}

	if arc.closeCalls.Load() != 1 {
		t.Fatalf("Stop did not close the archive; closeCalls=%d", arc.closeCalls.Load())
	}
}

// TestEngine_SetArchive_NilDetaches verifies that SetArchive(nil) clears
// the tracker's onStale callback. Without that, a Close()d archive whose
// pointer was overwritten could still receive channel sends from the
// tracker — use-after-close.
func TestEngine_SetArchive_NilDetaches(t *testing.T) {
	adaptor := newMockAdaptor()
	engine := NewEngine(adaptor, DefaultConfig())

	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Stop() })

	arc := &fakeArchive{}
	engine.SetArchive(arc)
	engine.SetArchive(nil) // detach

	v := &consensus.Validation{
		LedgerSeq: 100, LedgerID: consensus.LedgerID{0x1},
		NodeID: consensus.NodeID{0x2}, SignTime: time.Now(), Full: true,
	}
	if !engine.validationTracker.Add(v) {
		t.Fatal("Add returned false; precondition broken")
	}
	engine.validationTracker.ExpireOld(200)

	if got := arc.staleCount(); got != 0 {
		t.Fatalf("detached archive received %d stale rows; want 0", got)
	}
}

// TestEngine_SetArchive_ConcurrentWithCallback exercises the race the
// reviewer flagged: the fully-validated callback reads e.archive while
// other goroutines call SetArchive / SetInMemoryLedgers. Run under
// `go test -race` to verify no race is reported.
func TestEngine_SetArchive_ConcurrentWithCallback(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.setTrusted([]consensus.NodeID{{1}, {2}, {3}})
	adaptor.quorum = 2
	engine := NewEngine(adaptor, DefaultConfig())

	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Stop() })

	arc1 := &fakeArchive{}
	arc2 := &fakeArchive{}

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine A: hammer SetArchive / SetInMemoryLedgers.
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if i%2 == 0 {
				engine.SetArchive(arc1)
			} else {
				engine.SetArchive(arc2)
			}
			engine.SetInMemoryLedgers(uint32(50 + i))
		}
	}()

	// Goroutine B: drive Add() to fire the fully-validated callback
	// repeatedly. Quorum=2 so two trusted validations per ledger fire
	// the callback.
	go func() {
		defer wg.Done()
		now := time.Now()
		for seq := uint32(100); seq < 200; seq++ {
			ledger := consensus.LedgerID{byte(seq), byte(seq >> 8)}
			for _, n := range []consensus.NodeID{{1}, {2}} {
				v := &consensus.Validation{
					LedgerSeq: seq, LedgerID: ledger, NodeID: n,
					SignTime: now, Full: true,
				}
				engine.validationTracker.Add(v)
			}
		}
	}()

	wg.Wait()
}

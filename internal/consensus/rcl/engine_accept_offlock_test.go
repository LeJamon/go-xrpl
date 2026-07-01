package rcl

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// fakeClock is a deterministic, advance-on-demand wall clock for tests that
// need to measure a duration across the off-lock ledger build.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// driveToEstablish puts a freshly started proposing round into PhaseEstablish
// with a non-nil prevLedger so acceptLedger runs its full accept path.
func driveToEstablish(t *testing.T, e *Engine, a *mockAdaptor) {
	t.Helper()
	e.mu.Lock()
	e.prevLedger = a.lastLCL
	e.setPhase(consensus.PhaseEstablish)
	e.mu.Unlock()
}

// TestEngine_AcceptLedger_OffLockDoesNotBlockPeerHandlers proves the core
// liveness invariant: while BuildLedger applies the LCL, e.mu is released, so
// inbound OnProposal/OnValidation return promptly and buffer for the next round
// instead of blocking on the consensus lock (rippled onAccept→jtACCEPT). Run
// with -race to catch any shared-state access during the unlocked apply.
func TestEngine_AcceptLedger_OffLockDoesNotBlockPeerHandlers(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.setTrusted([]consensus.NodeID{{2}})
	adaptor.opMode = consensus.OpModeFull
	adaptor.validator = true

	entered := make(chan struct{})
	release := make(chan struct{})
	adaptor.buildLedgerHook = func() {
		close(entered)
		<-release
	}

	engine := NewEngine(adaptor, DefaultConfig())
	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)
	driveToEstablish(t, engine, adaptor)

	// The parent is seq 100; the mock mints its child as ID {101}. A proposal
	// built on that child belongs to the NEXT round.
	nextParent := consensus.LedgerID{101}

	// Run the accept (holding e.mu, exactly as timerEntry does).
	acceptDone := make(chan struct{})
	go func() {
		engine.mu.Lock()
		engine.acceptLedger(consensus.ResultSuccess)
		engine.mu.Unlock()
		close(acceptDone)
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("BuildLedger never started")
	}

	// e.mu is now released for the whole (blocked) apply. Peer handlers must
	// return without waiting on the build.
	handlerDone := make(chan error, 2)
	go func() {
		handlerDone <- engine.OnProposal(&consensus.Proposal{
			Round:          consensus.RoundID{Seq: 102, ParentHash: nextParent},
			NodeID:         consensus.NodeID{2},
			Position:       0,
			TxSet:          consensus.TxSetID{9},
			CloseTime:      time.Now(),
			PreviousLedger: nextParent,
			Timestamp:      time.Now(),
		}, 0)
	}()
	go func() {
		handlerDone <- engine.OnValidation(&consensus.Validation{
			LedgerID:  consensus.LedgerID{101},
			LedgerSeq: 101,
			NodeID:    consensus.NodeID{2},
			SignTime:  time.Now(),
			SeenTime:  time.Now(),
			Full:      true,
		}, 0)
	}()

	for i := 0; i < 2; i++ {
		select {
		case err := <-handlerDone:
			if err != nil {
				t.Fatalf("peer handler returned error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("peer handler blocked during the off-lock apply — e.mu was held across BuildLedger")
		}
	}

	// The proposal arrived while the round was frozen (PhaseAccepted); it must
	// have buffered for the next round rather than mutating the closing one.
	engine.mu.RLock()
	buffered := engine.proposalTracker.HasBufferedFor(nextParent)
	engine.mu.RUnlock()
	if !buffered {
		t.Fatal("proposal arriving during the apply was not buffered for the next round")
	}

	close(release)
	select {
	case <-acceptDone:
	case <-time.After(2 * time.Second):
		t.Fatal("acceptLedger never completed after releasing the build")
	}

	// The commit tail ran under the relock and advanced the LCL.
	engine.mu.RLock()
	newSeq := engine.prevLedger.Seq()
	engine.mu.RUnlock()
	if newSeq != 101 {
		t.Fatalf("expected prevLedger to advance to seq 101, got %d", newSeq)
	}
}

// TestEngine_AcceptLedger_PrevRoundTimeExcludesBuild proves prevRoundTime is
// captured from the establish duration, not inflated by the LCL apply — the
// convergePercent divisor and abandon clamp must track convergence only
// (rippled ConsensusParms.h: "Does not include the time to build the LCL").
func TestEngine_AcceptLedger_PrevRoundTimeExcludesBuild(t *testing.T) {
	const establishDur = 40 * time.Millisecond
	const buildDur = 400 * time.Millisecond

	clk := &fakeClock{t: time.Unix(1_000_000, 0).UTC()}

	adaptor := newMockAdaptor()
	adaptor.opMode = consensus.OpModeFull
	adaptor.validator = true
	adaptor.buildLedgerHook = func() { clk.advance(buildDur) }

	config := DefaultConfig()
	config.Clock = clk.now
	engine := NewEngine(adaptor, config)

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true) // roundStartTime = clk (T0)
	driveToEstablish(t, engine, adaptor)

	clk.advance(establishDur) // the establish phase elapses before accept

	engine.mu.Lock()
	engine.acceptLedger(consensus.ResultSuccess)
	engine.mu.Unlock()

	engine.mu.RLock()
	prt := engine.prevRoundTime
	engine.mu.RUnlock()

	if prt != establishDur {
		t.Fatalf("prevRoundTime = %v; want the %v establish duration (build of %v must be excluded)",
			prt, establishDur, buildDur)
	}
	if prt >= buildDur {
		t.Fatalf("prevRoundTime = %v includes the %v build time", prt, buildDur)
	}
}

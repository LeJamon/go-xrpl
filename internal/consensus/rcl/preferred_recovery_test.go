package rcl

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/stretchr/testify/require"
)

type preferredRecoveryTestAdaptor struct {
	*mockAdaptor
	prepareStarted  chan struct{}
	prepareRelease  chan struct{}
	prepareOnce     sync.Once
	preferredNotify func(consensus.Ledger)
	finishCallback  func()
}

func (a *preferredRecoveryTestAdaptor) OnPreferredLedgerSwitched(ledger consensus.Ledger) error {
	if a.preferredNotify != nil {
		a.preferredNotify(ledger)
	}
	return nil
}

type preferredRecoveryTestPreparation struct {
	finish         chan struct{}
	release        chan struct{}
	finishCallback func()
}

func (a *preferredRecoveryTestAdaptor) PreparePreferredLedger(ctx context.Context, parent consensus.Ledger) (consensus.PreferredLedgerPreparation, error) {
	a.prepareOnce.Do(func() { close(a.prepareStarted) })
	select {
	case <-a.prepareRelease:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &preferredRecoveryTestPreparation{
		finish:         make(chan struct{}),
		release:        make(chan struct{}),
		finishCallback: a.finishCallback,
	}, nil
}

func (p *preferredRecoveryTestPreparation) Wait(context.Context) error { return nil }

func (p *preferredRecoveryTestPreparation) TryPublish() (bool, error) {
	return true, nil
}

func (p *preferredRecoveryTestPreparation) Finish() {
	if p.finishCallback != nil {
		p.finishCallback()
	}
	select {
	case <-p.finish:
	default:
		close(p.finish)
	}
}

func (p *preferredRecoveryTestPreparation) Release() {
	select {
	case <-p.release:
	default:
		close(p.release)
	}
}

func TestPreferredRecoveryPreparationDoesNotHoldEngineMutex(t *testing.T) {
	base := newMockAdaptor()
	target := chainLedger(101, 2, 1)
	base.ledgers[target.ID()] = target
	a := &preferredRecoveryTestAdaptor{
		mockAdaptor:    base,
		prepareStarted: make(chan struct{}),
		prepareRelease: make(chan struct{}),
	}
	e := NewEngine(a, DefaultConfig())
	e.prevLedger = base.lastLCL
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = target.ID()
	t.Cleanup(func() { _ = e.Stop() })

	result, err := e.TrySwitchToLedger(target.ID())
	require.NoError(t, err)
	require.Equal(t, consensus.LedgerSwitchBusy, result)
	require.Equal(t, base.lastLCL.ID(), e.prevLedger.ID())
	select {
	case <-a.prepareStarted:
	case <-time.After(time.Second):
		t.Fatal("preferred preparation did not start")
	}

	// A blocked service preparation must not block ordinary engine ingress.
	phaseDone := make(chan struct{})
	go func() {
		_ = e.Phase()
		close(phaseDone)
	}()
	select {
	case <-phaseDone:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("engine mutex remained held during preferred preparation")
	}
	close(a.prepareRelease)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		e.mu.RLock()
		adopted := e.prevLedger != nil && e.prevLedger.ID() == target.ID()
		e.mu.RUnlock()
		if adopted {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("preferred recovery did not adopt the prepared target")
}

func TestPreferredRecoveryStopCancelsPreparation(t *testing.T) {
	base := newMockAdaptor()
	target := chainLedger(101, 2, 1)
	base.ledgers[target.ID()] = target
	a := &preferredRecoveryTestAdaptor{
		mockAdaptor:    base,
		prepareStarted: make(chan struct{}),
		prepareRelease: make(chan struct{}),
	}
	e := NewEngine(a, DefaultConfig())
	e.prevLedger = base.lastLCL
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = target.ID()

	result, err := e.TrySwitchToLedger(target.ID())
	require.NoError(t, err)
	require.Equal(t, consensus.LedgerSwitchBusy, result)
	select {
	case <-a.prepareStarted:
	case <-time.After(time.Second):
		t.Fatal("preferred preparation did not start")
	}

	stopped := make(chan error, 1)
	go func() { stopped <- e.Stop() }()
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("engine stop did not cancel preferred preparation")
	}
	close(a.prepareRelease)
}

func TestPreferredRecoveryHeartbeatUsesDetachedPreparation(t *testing.T) {
	base := newMockAdaptor()
	target := chainLedger(101, 2, 1)
	base.ledgers[target.ID()] = target
	base.peerLCLs = []consensus.LedgerID{target.ID(), target.ID()}
	a := &preferredRecoveryTestAdaptor{
		mockAdaptor:    base,
		prepareStarted: make(chan struct{}),
		prepareRelease: make(chan struct{}),
	}
	e := NewEngine(a, DefaultConfig())
	e.prevLedger = base.lastLCL
	e.mode = consensus.ModeProposing

	e.mu.Lock()
	e.handleWrongLedger(target.ID(), target)
	e.mu.Unlock()
	require.Equal(t, consensus.ModeObserving, e.Mode())
	select {
	case <-a.prepareStarted:
	case <-time.After(time.Second):
		t.Fatal("heartbeat preferred preparation did not start")
	}
	close(a.prepareRelease)
	t.Cleanup(func() { _ = e.Stop() })

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		e.mu.RLock()
		adopted := e.prevLedger != nil && e.prevLedger.ID() == target.ID()
		e.mu.RUnlock()
		if adopted {
			require.Equal(t, consensus.ModeSwitchedLedger, e.Mode())
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("heartbeat preferred recovery did not adopt the prepared target")
}

func TestPreferredRecoveryRejectsChangedNetworkPreference(t *testing.T) {
	base := newMockAdaptor()
	target := chainLedger(101, 2, 1)
	base.ledgers[target.ID()] = target
	base.peerLCLs = []consensus.LedgerID{target.ID(), target.ID()}
	a := &preferredRecoveryTestAdaptor{
		mockAdaptor:    base,
		prepareStarted: make(chan struct{}),
		prepareRelease: make(chan struct{}),
	}
	e := NewEngine(a, DefaultConfig())
	e.prevLedger = base.lastLCL
	e.mode = consensus.ModeObserving

	result, err := e.TrySwitchToLedger(target.ID())
	require.NoError(t, err)
	require.Equal(t, consensus.LedgerSwitchBusy, result)
	select {
	case <-a.prepareStarted:
	case <-time.After(time.Second):
		t.Fatal("preferred preparation did not start")
	}
	base.mu.Lock()
	base.peerLCLs = nil
	base.mu.Unlock()
	close(a.prepareRelease)
	t.Cleanup(func() { _ = e.Stop() })

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		e.mu.RLock()
		adopted := e.prevLedger != nil && e.prevLedger.ID() == target.ID()
		e.mu.RUnlock()
		if adopted {
			t.Fatal("stale network preference was adopted")
		}
		if _, ok := e.preferredRecoveryTarget(); !ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("stale preferred recovery did not finish")
}

func TestPreferredRecoveryRejectsRoundRestart(t *testing.T) {
	base := newMockAdaptor()
	target := chainLedger(101, 2, 1)
	base.ledgers[target.ID()] = target
	base.peerLCLs = []consensus.LedgerID{target.ID(), target.ID()}
	a := &preferredRecoveryTestAdaptor{
		mockAdaptor:    base,
		prepareStarted: make(chan struct{}),
		prepareRelease: make(chan struct{}),
	}
	e := NewEngine(a, DefaultConfig())
	initial := base.lastLCL
	e.prevLedger = initial
	e.mode = consensus.ModeObserving

	result, err := e.TrySwitchToLedger(target.ID())
	require.NoError(t, err)
	require.Equal(t, consensus.LedgerSwitchBusy, result)
	select {
	case <-a.prepareStarted:
	case <-time.After(time.Second):
		t.Fatal("preferred preparation did not start")
	}
	require.NoError(t, e.StartRound(consensus.RoundID{Seq: initial.Seq() + 1, ParentHash: initial.ID()}, false))
	close(a.prepareRelease)
	t.Cleanup(func() { _ = e.Stop() })

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		e.mu.RLock()
		adopted := e.prevLedger != nil && e.prevLedger.ID() == target.ID()
		e.mu.RUnlock()
		if adopted {
			t.Fatal("stale round preparation was adopted")
		}
		if _, ok := e.preferredRecoveryTarget(); !ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("stale preferred recovery did not finish")
}

func TestPreferredRecoveryNotifiesAfterEngineUnlock(t *testing.T) {
	base := newMockAdaptor()
	target := chainLedger(101, 2, 1)
	base.ledgers[target.ID()] = target
	notified := make(chan struct{})
	var e *Engine
	a := &preferredRecoveryTestAdaptor{
		mockAdaptor:    base,
		prepareStarted: make(chan struct{}),
		prepareRelease: make(chan struct{}),
		preferredNotify: func(consensus.Ledger) {
			_ = e.Phase()
			close(notified)
		},
	}
	e = NewEngine(a, DefaultConfig())
	e.prevLedger = base.lastLCL
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = target.ID()

	result, err := e.TrySwitchToLedger(target.ID())
	require.NoError(t, err)
	require.Equal(t, consensus.LedgerSwitchBusy, result)
	<-a.prepareStarted
	close(a.prepareRelease)
	select {
	case <-notified:
	case <-time.After(time.Second):
		t.Fatal("preferred notification did not run after engine unlock")
	}
	_ = e.Stop()
}

func TestPreferredRecoveryFinishCanStopEngine(t *testing.T) {
	base := newMockAdaptor()
	target := chainLedger(101, 2, 1)
	base.ledgers[target.ID()] = target
	finishStopped := make(chan struct{})
	var e *Engine
	a := &preferredRecoveryTestAdaptor{
		mockAdaptor:    base,
		prepareStarted: make(chan struct{}),
		prepareRelease: make(chan struct{}),
		finishCallback: func() {
			_ = e.Stop()
			close(finishStopped)
		},
	}
	e = NewEngine(a, DefaultConfig())
	e.prevLedger = base.lastLCL
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = target.ID()

	result, err := e.TrySwitchToLedger(target.ID())
	require.NoError(t, err)
	require.Equal(t, consensus.LedgerSwitchBusy, result)
	<-a.prepareStarted
	close(a.prepareRelease)
	select {
	case <-finishStopped:
	case <-time.After(time.Second):
		t.Fatal("Finish callback deadlocked while stopping engine")
	}
}

func TestPreferredRecoverySameWrongLedgerRestartsRound(t *testing.T) {
	base := newMockAdaptor()
	current := base.lastLCL
	a := &preferredRecoveryTestAdaptor{
		mockAdaptor:    base,
		prepareStarted: make(chan struct{}),
		prepareRelease: make(chan struct{}),
	}
	e := NewEngine(a, DefaultConfig())
	e.prevLedger = current
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = current.ID()

	result, err := e.TrySwitchToLedger(current.ID())
	require.NoError(t, err)
	require.Equal(t, consensus.LedgerSwitchBusy, result)
	<-a.prepareStarted
	close(a.prepareRelease)
	t.Cleanup(func() { _ = e.Stop() })

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if e.Mode() == consensus.ModeSwitchedLedger {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("same-ledger WrongLedger recovery did not restart")
}

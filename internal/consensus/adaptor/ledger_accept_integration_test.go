package adaptor

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/stretchr/testify/require"
)

type acceptValidationSender struct {
	noopSender

	mu          sync.Mutex
	validations []*consensus.Validation
	emitted     chan struct{}
	emittedOnce sync.Once
}

func (s *acceptValidationSender) BroadcastValidation(validation *consensus.Validation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *validation
	s.validations = append(s.validations, &copy)
	s.emittedOnce.Do(func() { close(s.emitted) })
	return nil
}

func (s *acceptValidationSender) snapshot() []*consensus.Validation {
	s.mu.Lock()
	defer s.mu.Unlock()
	validations := make([]*consensus.Validation, len(s.validations))
	copy(validations, s.validations)
	return validations
}

type blockedBuildAdaptor struct {
	*Adaptor
	entered       chan struct{}
	release       chan struct{}
	completed     chan struct{}
	enteredOnce   sync.Once
	releaseOnce   sync.Once
	completedOnce sync.Once
}

func (a *blockedBuildAdaptor) GetPendingTxs() [][]byte {
	return [][]byte{{0x01}}
}

func (a *blockedBuildAdaptor) GetProposableTxs(consensus.Ledger) [][]byte {
	return [][]byte{{0x01}}
}

func (a *blockedBuildAdaptor) BuildTxSet(_ [][]byte) (consensus.TxSet, error) {
	return a.Adaptor.BuildTxSet(nil)
}

func (a *blockedBuildAdaptor) BuildLedger(
	parent consensus.Ledger,
	txSet consensus.TxSet,
	closeTime time.Time,
	closeTimeCorrect bool,
	disputedTxs [][]byte,
) (consensus.Ledger, error) {
	a.enteredOnce.Do(func() { close(a.entered) })
	<-a.release
	ledger, err := a.Adaptor.BuildLedger(parent, txSet, closeTime, closeTimeCorrect, disputedTxs)
	a.completedOnce.Do(func() { close(a.completed) })
	return ledger, err
}

func (a *blockedBuildAdaptor) releaseBuild() {
	a.releaseOnce.Do(func() { close(a.release) })
}

func TestProductionAdaptorAcceptanceKeepsTimerResponsiveDuringBlockedBuild(t *testing.T) {
	base := newTestAdaptor(t)
	t.Cleanup(base.ledgerService.Stop)
	sender := &acceptValidationSender{emitted: make(chan struct{})}
	base.sender = sender
	base.SetOperatingMode(consensus.OpModeFull)

	blocked := &blockedBuildAdaptor{
		Adaptor:   base,
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
		completed: make(chan struct{}),
	}
	config := rcl.DefaultConfig()
	config.ManualTick = true
	clockNow := time.Now()
	config.Clock = func() time.Time { return clockNow }
	config.Timing.LedgerMinClose = 0
	config.Timing.LedgerIdleInterval = 0
	config.Timing.LedgerMinConsensus = 0
	config.Timing.LedgerMaxConsensus = time.Millisecond
	config.Timing.LedgerAbandonConsensus = time.Second
	engine := rcl.NewEngine(blocked, config)
	require.NoError(t, engine.Start(t.Context()))
	t.Cleanup(func() {
		blocked.releaseBuild()
		select {
		case <-blocked.completed:
		case <-time.After(time.Second):
			t.Error("production ledger acceptance did not complete during cleanup")
		}
		require.NoError(t, engine.Stop())
	})

	closed := base.ledgerService.GetClosedLedger()
	require.NotNil(t, closed)
	require.NoError(t, engine.StartRound(consensus.RoundID{
		Seq:        closed.Sequence() + 1,
		ParentHash: consensus.LedgerID(closed.Hash()),
	}, true))

	runTick := func() {
		done := make(chan struct{})
		go func() {
			engine.TimerEntry()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("consensus timer blocked before the deferred acceptance started")
		}
	}
	runTick()
	runTick()
	clockNow = clockNow.Add(2 * time.Millisecond)
	runTick()

	select {
	case <-blocked.entered:
	case <-time.After(time.Second):
		t.Fatal("production acceptance did not enter the blocked build")
	}

	// A heartbeat arriving during the real BuildLedger call must park behind
	// the in-flight acceptance rather than wait on the ledger service.
	tickDone := make(chan struct{})
	go func() {
		engine.TimerEntry()
		close(tickDone)
	}()
	select {
	case <-tickDone:
	case <-time.After(time.Second):
		t.Fatal("consensus timer waited for the blocked production ledger build")
	}

	blocked.releaseBuild()
	select {
	case <-blocked.completed:
	case <-time.After(time.Second):
		t.Fatal("production ledger acceptance did not complete")
	}
	select {
	case <-sender.emitted:
	case <-time.After(time.Second):
		t.Fatal("production ledger acceptance did not emit a validation")
	}

	validations := sender.snapshot()
	require.Len(t, validations, 1)
	require.True(t, validations[0].Full)
	require.Equal(t, closed.Sequence()+1, validations[0].LedgerSeq)
	accepted := base.ledgerService.GetClosedLedger()
	require.NotNil(t, accepted)
	require.Equal(t, consensus.LedgerID(accepted.Hash()), validations[0].LedgerID)
}

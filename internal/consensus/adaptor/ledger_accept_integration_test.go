package adaptor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/stretchr/testify/require"
)

func TestBroadcastValidationRejectsAuthenticatedConflictBeforeIssuingSequence(t *testing.T) {
	a := newTestAdaptor(t)
	t.Cleanup(a.ledgerService.Stop)
	sender := &acceptValidationSender{emitted: make(chan struct{})}
	a.sender = sender
	tracker := rcl.NewValidationTracker(2)
	nodes := []consensus.NodeID{{1}, {2}}
	tracker.SetTrustedAndQuorum(nodes, 2)
	a.SetValidationHistorian(tracker)
	canonical := consensus.LedgerID{9}
	now := time.Now()
	for _, node := range nodes {
		require.True(t, tracker.Add(&consensus.Validation{NodeID: node, LedgerID: canonical,
			LedgerSeq: 100, SignTime: now, SeenTime: now, Full: true}))
	}
	err := a.BroadcastValidation(&consensus.Validation{LedgerID: consensus.LedgerID{8}, LedgerSeq: 100, Full: true})
	require.ErrorIs(t, err, service.ErrConsensusPublicationStale)
	require.Empty(t, sender.snapshot())
	require.Zero(t, a.lastIssuedValidationSeq.Load())
	require.NoError(t, a.BroadcastValidation(&consensus.Validation{LedgerID: canonical, LedgerSeq: 100, Full: true}))
	require.Len(t, sender.snapshot(), 1)
	require.Equal(t, uint32(100), a.lastIssuedValidationSeq.Load())
}

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
	ctx context.Context,
	parent consensus.Ledger,
	txSet consensus.TxSet,
	closeTime time.Time,
	closeTimeCorrect bool,
	disputedTxs [][]byte,
) (consensus.Ledger, error) {
	a.enteredOnce.Do(func() { close(a.entered) })
	<-a.release
	ledger, err := a.Adaptor.BuildLedger(ctx, parent, txSet, closeTime, closeTimeCorrect, disputedTxs)
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
	require.NotNil(t, base.validationHistorian, "engine did not wire the validation historian")
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

func TestProductionAdaptorRejectsStaleBuildAndRecoversHeldCanonicalLedger(t *testing.T) {
	base := newTestAdaptor(t)
	var requests atomic.Int32
	base.SetOnLedgerRequested(func(consensus.LedgerID) error {
		requests.Add(1)
		return nil
	})
	t.Cleanup(base.ledgerService.Stop)
	secondIdentity, err := NewValidatorIdentity("sn3nxiW7v8KXzPzAqzyHXbSSKNuN9")
	require.NoError(t, err)
	base.SetTrustedValidators(
		[]consensus.NodeID{base.identity.NodeID, secondIdentity.NodeID},
		[][33]byte{base.identity.MasterKey, secondIdentity.MasterKey},
	)
	sender := &acceptValidationSender{emitted: make(chan struct{})}
	base.sender = sender
	base.SetOperatingMode(consensus.OpModeFull)

	parent := base.ledgerService.GetClosedLedger()
	require.NotNil(t, parent)
	canonical, err := ledger.NewOpen(parent, parent.CloseTime().Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, canonical.AddTransaction([32]byte{0xc1}, make([]byte, 12)))
	require.NoError(t, canonical.Close(canonical.CloseTime(), 0))
	stateMap, err := canonical.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := canonical.TxMapSnapshot()
	require.NoError(t, err)
	header := canonical.Header()
	require.NoError(t, base.ledgerService.StoreLedgerWithState(t.Context(), &header, stateMap, txMap))
	_, held := base.ledgerService.GetCachedLedgerByHash(canonical.Hash())
	require.True(t, held, "canonical recovery ledger was not held locally")
	base.ledgerService.PromoteStoredValidatedLedgerAt(canonical.Sequence(), canonical.Hash(), canonical.CloseTime())
	require.Equal(t, canonical.Hash(), base.ledgerService.GetValidatedLedger().Hash())
	require.Equal(t, parent.Hash(), base.ledgerService.GetClosedLedger().Hash())

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
			t.Error("production stale-build acceptance did not complete during cleanup")
		}
		require.NoError(t, engine.Stop())
	})
	require.NoError(t, engine.StartRound(consensus.RoundID{
		Seq:        parent.Sequence() + 1,
		ParentHash: consensus.LedgerID(parent.Hash()),
	}, true))

	driveToAcceptance := func() {
		for range 3 {
			engine.TimerEntry()
		}
		clockNow = clockNow.Add(2 * time.Millisecond)
		engine.TimerEntry()
	}
	driveToAcceptance()
	select {
	case <-blocked.entered:
	case <-time.After(time.Second):
		t.Fatal("production acceptance did not enter the blocked build")
	}

	validationTime := base.Now()
	for _, identity := range []*ValidatorIdentity{base.identity, secondIdentity} {
		validation := &consensus.Validation{
			LedgerID:  consensus.LedgerID(canonical.Hash()),
			LedgerSeq: canonical.Sequence(),
			SignTime:  validationTime,
			SeenTime:  validationTime,
			Full:      true,
		}
		require.NoError(t, identity.SignValidation(validation))
		require.NoError(t, engine.OnValidation(validation, 0))
	}

	blocked.releaseBuild()
	select {
	case <-blocked.completed:
	case <-time.After(time.Second):
		t.Fatal("stale production acceptance did not complete")
	}
	require.Empty(t, sender.snapshot(), "stale build must not emit a validation")

	// The canonical candidate is already held locally. The normal recovery path
	// should adopt it without requesting full state.
	clockNow = clockNow.Add(time.Second)
	result, err := engine.TrySwitchToLedger(consensus.LedgerID(canonical.Hash()))
	require.NoError(t, err)
	require.Equal(t, consensus.LedgerSwitchBusy, result)
	require.Eventually(t, func() bool {
		closed := base.ledgerService.GetClosedLedger()
		return closed != nil && closed.Hash() == canonical.Hash()
	}, time.Second, time.Millisecond)
	require.Equal(t, canonical.Hash(), base.ledgerService.GetClosedLedger().Hash())
	require.Zero(t, requests.Load(), "held canonical ledger must not trigger acquisition")
	require.False(t, base.ledgerService.ReplayBlocked())
}

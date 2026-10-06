package adaptor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

type countingAcquisitionMemoryAccount struct {
	mu            sync.Mutex
	changed       chan struct{}
	subscriptions int
}

func (a *countingAcquisitionMemoryAccount) TryReserve(uint64) bool { return true }

func (a *countingAcquisitionMemoryAccount) Release(uint64) {}

func (a *countingAcquisitionMemoryAccount) Changed() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.changed == nil {
		a.changed = make(chan struct{})
	}
	a.subscriptions++
	return a.changed
}

func (a *countingAcquisitionMemoryAccount) subscriptionsCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.subscriptions
}

func TestAcquisitionMemoryChannelsAreCached(t *testing.T) {
	parent := &countingAcquisitionMemoryAccount{}
	budget, err := shamap.NewAcquisitionBudgetWithParent(512<<20, parent)
	require.NoError(t, err)
	t.Cleanup(func() { require.True(t, budget.Close()) })
	c := &catchupReplayCoordinator{acquisitionMemory: budget}

	changed := c.acquisitionMemoryChangedChannel()
	require.NotNil(t, changed)
	require.Equal(t, changed, c.acquisitionMemoryChangedChannel())
	local := c.acquisitionMemoryLocalChangedChannel()
	require.NotNil(t, local)
	require.Equal(t, local, c.acquisitionMemoryLocalChangedChannel())
	require.Equal(t, 1, parent.subscriptionsCount(), "parent Changed must be called once while the channel is cached")

	c.notifyAcquisitionMemoryAvailable()
	require.Equal(t, 2, parent.subscriptionsCount(), "a wake must refresh the parent channel before retry work")
	require.Equal(t, changed, c.acquisitionMemoryChangedChannel(), "the refreshed channel remains cached for the next select")
}

func TestCatchupCoordinatorRejectsSameHashRetiredCompletion(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	base := svc.GetClosedLedger()
	links := buildStandardReplayTestChain(t, r, base, 2)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	c := r.catchupReplay
	old := c.fetchTracker.Find(links[0].hash)
	require.NotNil(t, old)
	generation := r.FastSyncMetrics().ReplayPipelineGeneration
	r.ClearFetchInfo()
	armStandardReplayTestPipeline(t, r, a, sender, links)
	replacement := c.fetchTracker.Find(links[0].hash)
	require.NotNil(t, replacement)
	require.NotSame(t, old, replacement)
	require.Greater(t, r.FastSyncMetrics().ReplayPipelineGeneration, generation)
	before := r.FastSyncMetrics()
	ack := make(chan struct{})
	c.handleAcquisitionWorkResult(acquisitionWorkResult{ledger: old, complete: true, ack: ack})
	select {
	case <-ack:
	default:
		t.Fatal("retired completion was not acknowledged")
	}
	require.Same(t, replacement, c.fetchTracker.Find(links[0].hash))
	require.Equal(t, before.ReplayPipelineReady, r.FastSyncMetrics().ReplayPipelineReady)
	require.Equal(t, before.ReplayPipelineApplied, r.FastSyncMetrics().ReplayPipelineApplied)
	require.Empty(t, c.standardReplayDrainWake)
	require.Equal(t, base.Hash(), svc.GetClosedLedger().Hash())
	for _, link := range links {
		completeStandardReplayTestLink(t, r, link)
	}
	drainStandardReplayTestWakes(t, r)
	require.Equal(t, uint64(len(links)), r.FastSyncMetrics().ReplayPipelineApplied)
}

func TestCatchupCoordinatorYieldRearmsSameGenerationOnce(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	c := r.catchupReplay
	c.standardReplay = standardReplayPipeline{
		active: true, pivotReady: true, generation: 7, anchorSeq: 10,
		entries: map[uint32]*standardReplayEntry{11: {readyAt: time.Now()}},
	}
	owner := c.beginStandardReplayDrain()
	require.NotNil(t, owner)
	owner.yielded = true
	c.finishStandardReplayDrain(owner)
	c.finishStandardReplayDrain(owner)
	require.Len(t, c.standardReplayDrainWake, 1)
	<-c.standardReplayDrainWake
	next := c.beginStandardReplayDrain()
	require.NotNil(t, next)
	require.Equal(t, owner.generation, next.generation)
	require.Nil(t, c.beginStandardReplayDrain())
	c.finishStandardReplayDrain(owner)
	require.Same(t, next, c.standardReplayDrainOwner)
	require.Empty(t, c.standardReplayDrainWake)
	c.finishStandardReplayDrain(next)
}

func TestCatchupCoordinatorShutdownDuringReplayHandoff(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 2)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	c := r.catchupReplay
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	c.engine = &mockEngine{switchResult: consensus.LedgerSwitchAccepted, switchHook: func(id consensus.LedgerID) {
		if [32]byte(id) == links[len(links)-1].hash {
			close(entered)
			<-release
		}
	}}
	completeStandardReplayTestLink(t, r, links[1])
	first := c.fetchTracker.Find(links[0].hash)
	require.NotNil(t, first)
	require.NoError(t, first.GotBase([]message.LedgerNode{{NodeData: links[0].response.LedgerHeader}, {NodeData: []byte{1}}}))
	done := make(chan struct{})
	go func() { defer close(done); c.completeInboundLedger(first) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("replay did not reach handoff")
	}
	stopped := make(chan struct{})
	go func() { r.StopAcquisitions(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown waited on the handoff while holding transition locks")
	}
	unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retired replay drain did not finish")
	}
	require.Nil(t, c.standardReplayDrainOwner)
	require.False(t, c.standardReplay.active)
	require.False(t, c.standardReplay.applying)
	require.Empty(t, c.standardReplayDrainWake)
	require.Empty(t, c.fetchTracker.Active())
	require.False(t, c.startLedgerAcquisition(links[1].seq+1, [32]byte{9}, 7))
	before := r.FastSyncMetrics().ReplayPipelineApplied
	c.maintenanceTick()
	c.drainStandardReplayPipeline()
	require.Equal(t, before, r.FastSyncMetrics().ReplayPipelineApplied)
	require.Empty(t, c.standardReplayDrainWake)
}

func TestCatchupCoordinatorShutdownRejectsReplayAndAncestryRearm(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	base := svc.GetClosedLedger()
	links := buildStandardReplayTestChain(t, r, base, 2)
	c := r.catchupReplay
	target := links[len(links)-1]
	c.recordValidationCatchupTarget(target.seq, target.hash, 7, catchupSourceQuorum)
	r.StopAcquisitions()
	require.False(t, c.tryArmStandardReplayPipeline(svc, base, target.seq, target.hash, 7))
	require.False(t, c.beginFrozenPivotRecovery(target.seq, target.hash, 7))
	require.False(t, c.startHeaderParentDiscovery(base, target.seq, target.hash, 7, catchupSourceQuorum))
	require.False(t, c.standardReplay.active)
	require.Nil(t, c.headerDiscovery)
	require.Empty(t, c.fetchTracker.Active())
	require.Empty(t, c.standardReplayDrainWake)
}

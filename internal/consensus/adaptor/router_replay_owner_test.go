package adaptor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/stretchr/testify/require"
)

func drainStandardReplayTestWakes(t *testing.T, r *Router) {
	t.Helper()
	for range 32 {
		select {
		case <-r.catchupReplay.standardReplayDrainWake:
			r.catchupReplay.drainStandardReplayPipeline()
		default:
			return
		}
	}
	t.Fatal("replay wake loop failed to settle")
}

// Exercise the real target-reached cancellation inside the consensus handoff,
// rather than synthesizing an orphaned flag. A later pipeline must be able to
// apply whether it arrives before or after the old drain's deferred release.
func TestStandardReplayDrainCancellationDuringHandoff(t *testing.T) {
	for _, replaceDuringHandoff := range []bool{false, true} {
		name := "replacement_after_release"
		if replaceDuringHandoff {
			name = "replacement_before_release"
		}
		t.Run(name, func(t *testing.T) {
			r, a, sender, svc := makeRouter(t)
			_, err := svc.AcceptLedger(context.Background())
			require.NoError(t, err)
			links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 2)
			armStandardReplayTestPipeline(t, r, a, sender, links)
			terminal := links[len(links)-1]
			next := buildStandardReplayTestChain(t, r, terminal.ledger, 2)
			completeReplacement := func() {
				armStandardReplayTestPipeline(t, r, a, sender, next)
				for _, link := range next {
					completeStandardReplayTestLink(t, r, link)
				}
			}
			canceled := false
			r.engine = &mockEngine{
				switchResult: consensus.LedgerSwitchAccepted,
				switchHook: func(id consensus.LedgerID) {
					if [32]byte(id) != terminal.hash || canceled {
						return
					}
					canceled = true
					owner := r.catchupReplay.standardReplayDrainOwner
					require.NotNil(t, owner)
					require.True(t, r.catchupReplay.tryArmStandardReplayPipeline(svc, nil, terminal.seq, terminal.hash, 7))
					require.False(t, r.catchupReplay.standardReplay.active)
					require.False(t, r.catchupReplay.standardReplay.applying)
					require.Same(t, owner, r.catchupReplay.standardReplayDrainOwner)
					if replaceDuringHandoff {
						completeReplacement()
						// Reentrant completions cannot start a second actual applier.
						require.Equal(t, uint64(len(links)), r.FastSyncMetrics().ReplayPipelineApplied)
						require.Equal(t, uint32(len(next)), r.FastSyncMetrics().ReplayPipelineReadyDepth)
						require.Same(t, owner, r.catchupReplay.standardReplayDrainOwner)
					}
				},
			}
			r.catchupReplay.engine = r.engine
			for _, link := range links {
				completeStandardReplayTestLink(t, r, link)
			}
			drainStandardReplayTestWakes(t, r)
			require.True(t, canceled)
			require.Nil(t, r.catchupReplay.standardReplayDrainOwner)
			if !replaceDuringHandoff {
				require.False(t, r.catchupReplay.standardReplay.applying)
				completeReplacement()
				drainStandardReplayTestWakes(t, r)
			}
			require.Equal(t, uint64(len(links)+len(next)), r.FastSyncMetrics().ReplayPipelineApplied)
			require.Zero(t, r.FastSyncMetrics().ReplayPipelineReadyDepth)
			require.False(t, r.catchupReplay.standardReplay.applying)
			require.Nil(t, r.catchupReplay.standardReplayDrainOwner)
		})
	}
}

func TestStandardReplayDrainOwnerSingleFlightAndStaleRelease(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	r.catchupReplay.standardReplay = standardReplayPipeline{active: true, pivotReady: true, generation: 1}
	owners := make(chan *standardReplayDrainOwner, 16)
	var wg sync.WaitGroup
	for range cap(owners) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			owners <- r.catchupReplay.beginStandardReplayDrain()
		}()
	}
	wg.Wait()
	close(owners)
	var owner *standardReplayDrainOwner
	for candidate := range owners {
		if candidate != nil {
			require.Nil(t, owner, "only one actual drain may execute")
			owner = candidate
		}
	}
	require.NotNil(t, owner)
	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.cancelStandardReplayPipelineLocked("test_replacement")
	r.catchupReplay.standardReplay = standardReplayPipeline{
		active: true, pivotReady: true, generation: 3, anchorSeq: 10,
		entries: map[uint32]*standardReplayEntry{11: {readyAt: time.Now()}},
	}
	r.catchupReplay.acquisitionMu.Unlock()
	require.Nil(t, r.catchupReplay.beginStandardReplayDrain(), "replacement must wait for old execution to return")
	r.catchupReplay.finishStandardReplayDrain(owner)
	require.Len(t, r.catchupReplay.standardReplayDrainWake, 1)
	next := r.catchupReplay.beginStandardReplayDrain()
	require.NotNil(t, next)
	require.Equal(t, uint64(3), next.generation)
	r.catchupReplay.finishStandardReplayDrain(owner)
	require.Same(t, next, r.catchupReplay.standardReplayDrainOwner, "stale release cannot unlock a newer owner")
	require.True(t, r.catchupReplay.standardReplay.applying)
	r.catchupReplay.finishStandardReplayDrain(next)
}

func TestStandardReplayWatchdogRepairsOrphanedReadyDrain(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 2)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	// Model a lost wake/reservation from an older session, without an owner.
	r.catchupReplay.standardReplay.applying = true
	for _, link := range links {
		completeStandardReplayTestLink(t, r, link)
	}
	require.Zero(t, r.FastSyncMetrics().ReplayPipelineApplied)
	require.Empty(t, r.catchupReplay.standardReplayDrainWake)
	generation := r.catchupReplay.standardReplay.generation
	require.True(t, r.catchupReplay.rebootstrapFrozenPivotIfStalled(time.Now().Add(3*time.Minute)))
	require.Equal(t, generation, r.catchupReplay.standardReplay.generation, "ready work must resume without repivoting")
	require.Len(t, r.catchupReplay.standardReplayDrainWake, 1)
	drainStandardReplayTestWakes(t, r)
	require.Equal(t, uint64(len(links)), r.FastSyncMetrics().ReplayPipelineApplied)
	require.Zero(t, r.FastSyncMetrics().ReplayPipelineFallbacks)
	require.False(t, r.catchupReplay.standardReplay.applying)
}

func TestStandardReplayWatchdogProtectsExecutingOwner(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	r.catchupReplay.standardReplay = standardReplayPipeline{
		active: true, pivotReady: true, generation: 1, anchorSeq: 10, targetSeq: 20,
		entries: map[uint32]*standardReplayEntry{11: {readyAt: time.Now()}},
	}
	owner := r.catchupReplay.beginStandardReplayDrain()
	require.NotNil(t, owner)
	defer r.catchupReplay.finishStandardReplayDrain(owner)
	require.False(t, r.catchupReplay.rebootstrapFrozenPivotIfStalled(time.Now().Add(24*time.Hour)))
	require.Same(t, owner, r.catchupReplay.standardReplayDrainOwner)
	require.Empty(t, r.catchupReplay.standardReplayDrainWake)
	require.Zero(t, r.catchupReplay.standardReplay.stalledSamples)
}

func TestStandardReplayWatchdogMissingHeadDoesNotKeepOrphanedFlag(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	started := time.Unix(100, 0)
	targetHash := [32]byte{0xd1}
	trackCatchupPeer(r, 7, 200, targetHash)
	r.catchupReplay.recordValidationCatchupTarget(200, targetHash, 7, catchupSourceQuorum)
	r.catchupReplay.standardReplay = standardReplayPipeline{
		generation: 3, active: true, applying: true, pivotReady: true,
		pivotSeq: 100, anchorSeq: 100, targetSeq: 200, targetHash: targetHash,
		entries:          make(map[uint32]*standardReplayEntry),
		progressSampleAt: started, sampleAnchorSeq: 100,
	}
	require.False(t, r.catchupReplay.rebootstrapFrozenPivotIfStalled(started.Add(standardReplayProgressWindow)))
	require.False(t, r.catchupReplay.standardReplay.applying)
	require.Equal(t, uint8(1), r.catchupReplay.standardReplay.stalledSamples)
	require.True(t, r.catchupReplay.rebootstrapFrozenPivotIfStalled(started.Add(2*standardReplayProgressWindow)))
	require.False(t, r.catchupReplay.standardReplay.pivotReady)
	require.Equal(t, uint32(200), r.catchupReplay.standardReplay.pivotSeq)
	require.Equal(t, uint64(1), r.FastSyncMetrics().ReplayPipelineFallbacks)
}

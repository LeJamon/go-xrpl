package adaptor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFrozenPivotRecoveryWaitsForUnknownSuccessorLink(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xa1}
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	generation := r.standardReplay.generation
	require.True(t, r.continueFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	require.True(t, r.standardReplay.active)
	require.Equal(t, generation, r.standardReplay.generation)

	targetSeq := pivotSeq + 2
	targetHash := [32]byte{0xa3}
	require.True(t, r.continueFrozenPivotRecovery(targetSeq, targetHash, 7))
	require.True(t, r.standardReplay.active)
	require.Equal(t, generation, r.standardReplay.generation)
	require.Equal(t, pivotSeq, r.standardReplay.pivotSeq)
	require.Equal(t, targetSeq, r.standardReplay.targetSeq)
	require.Empty(t, r.standardReplay.entries)
	require.Len(t, sender.legacyCalls(), 1)
}

func TestFrozenPivotRecoveryRejectsUnknownSequenceTarget(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xa4}
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	generation := r.standardReplay.generation

	assert.False(t, r.continueFrozenPivotRecovery(0, [32]byte{0xa5}, 7))
	assert.True(t, r.standardReplay.active)
	assert.Equal(t, generation, r.standardReplay.generation)
	assert.Equal(t, pivotSeq, r.standardReplay.targetSeq)
	assert.Equal(t, pivotHash, r.standardReplay.targetHash)
}

func TestFrozenPivotRecoveryKeepsExactConsensusTarget(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xa6}
	exactHash := [32]byte{0xa7}
	movingHash := [32]byte{0xa8}
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	require.True(t, r.continueFrozenPivotRecovery(pivotSeq+1, exactHash, 7))
	r.acquisitionMu.Lock()
	r.consensusRecovery.targetHash = exactHash
	r.acquisitionMu.Unlock()

	require.True(t, r.continueFrozenPivotRecovery(pivotSeq+2, movingHash, 7))
	assert.Equal(t, pivotSeq+2, r.standardReplay.targetSeq)
	assert.Equal(t, movingHash, r.standardReplay.targetHash)
	assert.Equal(t, exactHash, r.consensusRecovery.targetHash)
}

func TestFrozenPivotRecoveryAdvancesConsensusTargetOnTrustedEvidence(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xa9}
	exactHash := [32]byte{0xaa}
	validatedHash := [32]byte{0xab}
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	require.True(t, r.continueFrozenPivotRecovery(pivotSeq+1, exactHash, 7))
	r.acquisitionMu.Lock()
	r.consensusRecovery.targetHash = exactHash
	r.acquisitionMu.Unlock()
	r.recordValidationCatchupTarget(
		pivotSeq+2, validatedHash, 7, catchupSourceQuorum,
	)

	require.True(t, r.continueFrozenPivotRecovery(pivotSeq+2, validatedHash, 7))
	assert.Equal(t, validatedHash, r.standardReplay.targetHash)
	assert.Equal(t, validatedHash, r.consensusRecovery.targetHash)
}

func TestFrozenPivotRecoveryCancelsConflictingPivotEvidence(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xb1}
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	generation := r.standardReplay.generation

	require.False(t, r.continueFrozenPivotRecovery(pivotSeq, [32]byte{0xb2}, 7))
	assert.False(t, r.standardReplay.active)
	assert.Greater(t, r.standardReplay.generation, generation)
	assert.Nil(t, r.fetchTracker.Find(pivotHash))
	assert.Len(t, sender.legacyCalls(), 1)
}

func TestFrozenPivotRecoveryFailureReleasesPivotGeneration(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xc1}
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	generation := r.standardReplay.generation

	require.True(t, r.failFrozenPivotRecovery(pivotHash))
	assert.False(t, r.standardReplay.active)
	assert.Greater(t, r.standardReplay.generation, generation)
	assert.Nil(t, r.fetchTracker.Find(pivotHash))
}

func TestFrozenPivotFailedStartCannotBeKeptAliveByTargetAdvance(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xc2}
	r.markFailedCatchupAcquisition(pivotHash)

	r.replayCommitMu.Lock()
	done := make(chan bool, 1)
	go func() {
		done <- r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7)
	}()
	require.Eventually(t, func() bool {
		r.acquisitionMu.Lock()
		defer r.acquisitionMu.Unlock()
		return r.standardReplay.active && r.standardReplay.pivotHash == pivotHash
	}, time.Second, time.Millisecond)
	require.Nil(t, r.fetchTracker.Find(pivotHash))

	advancedHash := [32]byte{0xc3}
	require.True(t, r.continueFrozenPivotRecovery(pivotSeq+1, advancedHash, 7))
	r.replayCommitMu.Unlock()

	require.False(t, <-done)
	assert.False(t, r.standardReplay.active)
	assert.Nil(t, r.fetchTracker.Find(pivotHash))
}

func TestFrozenPivotRecoveryRebootstrapsAfterTwoNoProgressWindows(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	started := time.Unix(100, 0)
	targetHash := [32]byte{0xd1}
	trackCatchupPeer(r, 7, 200, targetHash)
	r.standardReplay = standardReplayPipeline{
		generation:       3,
		active:           true,
		pivotReady:       true,
		pivotSeq:         100,
		anchorSeq:        100,
		targetSeq:        200,
		targetHash:       targetHash,
		entries:          make(map[uint32]*standardReplayEntry),
		progressSampleAt: started,
		sampleAnchorSeq:  100,
	}

	assert.False(t, r.rebootstrapFrozenPivotIfStalled(started.Add(standardReplayProgressWindow)))
	assert.True(t, r.standardReplay.active)
	assert.Equal(t, uint8(1), r.standardReplay.stalledSamples)

	assert.True(t, r.rebootstrapFrozenPivotIfStalled(started.Add(2*standardReplayProgressWindow)))
	assert.True(t, r.standardReplay.active)
	assert.False(t, r.standardReplay.pivotReady)
	assert.Equal(t, uint64(5), r.standardReplay.generation)
	assert.Equal(t, uint32(200), r.standardReplay.pivotSeq)
	assert.Equal(t, targetHash, r.standardReplay.pivotHash)
	pivot := r.fetchTracker.Find(targetHash)
	require.NotNil(t, pivot)
	assert.False(t, pivot.TransactionOnly())
	assert.Equal(t, uint64(1), r.FastSyncMetrics().ReplayPipelineFallbacks)
}

func TestFrozenPivotRecoveryKeepsAdvancingReplayAtMovingTipRate(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	started := time.Unix(200, 0)
	r.standardReplay = standardReplayPipeline{
		generation:       5,
		active:           true,
		pivotReady:       true,
		pivotSeq:         100,
		anchorSeq:        100,
		targetSeq:        200,
		targetHash:       [32]byte{0xe1},
		entries:          make(map[uint32]*standardReplayEntry),
		progressSampleAt: started,
		sampleAnchorSeq:  100,
		stalledSamples:   1,
	}
	r.standardReplay.anchorSeq = 110
	r.standardReplay.targetSeq = 210

	assert.False(t, r.rebootstrapFrozenPivotIfStalled(started.Add(standardReplayProgressWindow)))
	assert.True(t, r.standardReplay.active)
	assert.Zero(t, r.standardReplay.stalledSamples)
	assert.Equal(t, uint64(5), r.standardReplay.generation)
}

func TestFrozenPivotRecoveryDoesNotTimeoutPivotDownload(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	started := time.Unix(300, 0)
	r.standardReplay = standardReplayPipeline{
		generation:       8,
		active:           true,
		pivotReady:       false,
		pivotSeq:         100,
		anchorSeq:        100,
		targetSeq:        300,
		targetHash:       [32]byte{0xf1},
		entries:          make(map[uint32]*standardReplayEntry),
		progressSampleAt: started,
		sampleAnchorSeq:  100,
	}

	assert.False(t, r.rebootstrapFrozenPivotIfStalled(started.Add(24*time.Hour)))
	assert.True(t, r.standardReplay.active)
	assert.Equal(t, uint64(8), r.standardReplay.generation)
}

func TestFrozenPivotBootstrapFailureRearmsTrustedTarget(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivot := completedCatchUpAcquisition(t, svc.GetClosedLedgerIndex()+10)
	pivotSeq, pivotHash := pivot.Seq(), pivot.Hash()
	r.fetchTracker.Track(pivot)
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))

	replacement := completedCatchUpAcquisition(t, pivotSeq+10)
	trackCatchupPeer(r, 7, replacement.Seq(), replacement.Hash())
	r.recordValidationCatchupTarget(
		replacement.Seq(), replacement.Hash(), 7, catchupSourceQuorum,
	)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	r.lifecycleMu.Lock()
	r.lifecycleCtx = canceled
	r.lifecycleMu.Unlock()
	t.Cleanup(func() {
		r.lifecycleMu.Lock()
		r.lifecycleCtx = context.Background()
		r.lifecycleMu.Unlock()
	})

	r.completeInboundLedger(pivot)

	assert.Nil(t, r.fetchTracker.Find(pivotHash))
	assert.True(t, r.standardReplay.active)
	assert.False(t, r.standardReplay.pivotReady)
	assert.Equal(t, replacement.Seq(), r.standardReplay.pivotSeq)
	assert.Equal(t, replacement.Hash(), r.standardReplay.pivotHash)
	rearmed := r.fetchTracker.Find(replacement.Hash())
	require.NotNil(t, rearmed)
	assert.False(t, rearmed.TransactionOnly())
}

func TestFrozenPivotHandoffKeepsSessionWhenTargetAdvances(t *testing.T) {
	r, a, _, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, closed)
	links := buildStandardReplayTestChain(t, r, pivot, 1)
	r.recordSeqHash(pivotSeq, pivotHash, closed.Hash(), true)
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	r.acquisitionMu.Lock()
	r.consensusRecovery.targetHash = pivotHash
	r.acquisitionMu.Unlock()
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	pivotAcquisition := r.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivotAcquisition)
	generation := r.standardReplay.generation

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	r.engine = &mockEngine{
		switchResult: consensus.LedgerSwitchAccepted,
		switchHook: func(consensus.LedgerID) {
			once.Do(func() {
				close(entered)
				<-release
			})
		},
	}
	storeRecoveryLedger(t, svc, pivot)
	require.True(t, r.fetchTracker.RemoveExpectedWithSnapshot(
		pivotAcquisition, pivotAcquisition.Snapshot(), true,
	))
	pivotHeader := pivot.Header()
	done := make(chan bool, 1)
	go func() {
		done <- r.completeFrozenPivotAcquisition(&pivotHeader, true)
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("pivot did not reach the handoff barrier")
	}
	trackCatchupPeer(r, 7, links[0].seq, links[0].hash)
	require.NoError(t, a.RequestLedger(consensus.LedgerID(links[0].hash)))
	assert.True(t, r.standardReplay.active)
	assert.Equal(t, generation, r.standardReplay.generation)
	assert.Equal(t, pivotHash, r.standardReplay.pivotHash)
	next := r.fetchTracker.Find(links[0].hash)
	require.NotNil(t, next)
	assert.True(t, next.TransactionOnly())

	close(release)
	select {
	case handled := <-done:
		assert.True(t, handled)
	case <-time.After(time.Second):
		t.Fatal("pivot did not leave the handoff barrier")
	}
}

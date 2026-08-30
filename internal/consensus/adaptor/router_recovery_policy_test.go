package adaptor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func policyReplayState(generation, pivotSeq, targetSeq uint32) standardReplayPipeline {
	return standardReplayPipeline{
		generation:       uint64(generation),
		active:           true,
		pivotReady:       true,
		pivotSeq:         pivotSeq,
		pivotHash:        [32]byte{0x10},
		anchorSeq:        pivotSeq,
		anchorHash:       [32]byte{0x10},
		collectSeq:       pivotSeq,
		collectHash:      [32]byte{0x10},
		targetSeq:        targetSeq,
		targetHash:       [32]byte{0x20},
		entries:          make(map[uint32]*standardReplayEntry),
		progressSampleAt: time.Unix(100, 0),
		sampleAnchorSeq:  pivotSeq,
		sampleTargetSeq:  targetSeq,
		strategy:         "replay",
	}
}

func TestFrozenPivotRecoveryRebasesImmediatelyWhenAncestryIsUnavailable(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	pivotSeq := uint32(100)
	targetSeq := pivotSeq + seqHashRetain + 1
	targetHash := [32]byte{0x21}
	trackCatchupPeer(r, 7, targetSeq, targetHash)
	r.recordValidationCatchupTarget(targetSeq, targetHash, 7, catchupSourceQuorum)
	r.standardReplay = policyReplayState(3, pivotSeq, targetSeq)

	require.True(t, r.maybeRebaseForMissingReplayAncestry(time.Unix(101, 0)))
	assert.True(t, r.standardReplay.active)
	assert.False(t, r.standardReplay.pivotReady)
	assert.Equal(t, targetSeq, r.standardReplay.pivotSeq)
	assert.Equal(t, targetHash, r.standardReplay.pivotHash)
	metrics := r.FastSyncMetrics()
	assert.Equal(t, uint64(1), metrics.ReplayPipelineAncestryUnavailable)
	assert.Equal(t, uint64(1), metrics.ReplayPipelineRebasesStarted)
	assert.Equal(t, uint64(1), metrics.ReplayPipelineRebasesSucceeded)
	assert.Equal(t, frozenPivotRetargetAncestryUnavailable, frozenPivotRetargetReason(metrics.ReplayPipelineDecisionReason))
}

func TestFrozenPivotRecoveryKeepsReplayWhenNextLinkIsKnown(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	pivotSeq := uint32(100)
	targetSeq := pivotSeq + seqHashRetain + 1
	targetHash := [32]byte{0x22}
	nextHash := [32]byte{0x23}
	r.recordSeqHash(pivotSeq+1, nextHash, [32]byte{0x10}, true)
	trackCatchupPeer(r, 7, targetSeq, targetHash)
	r.recordValidationCatchupTarget(targetSeq, targetHash, 7, catchupSourceQuorum)
	r.standardReplay = policyReplayState(3, pivotSeq, targetSeq)

	assert.False(t, r.maybeRebaseForMissingReplayAncestry(time.Unix(101, 0)))
	assert.True(t, r.standardReplay.active)
	assert.True(t, r.standardReplay.pivotReady)
	assert.Equal(t, uint64(0), r.FastSyncMetrics().ReplayPipelineRebasesStarted)
}

func TestFrozenPivotRecoveryDoesNotRebaseToUntrustedTarget(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	pivotSeq := uint32(100)
	targetSeq := pivotSeq + seqHashRetain + 1
	targetHash := [32]byte{0x24}
	trackCatchupPeer(r, 7, targetSeq, targetHash)
	r.recordCatchupTarget(targetSeq, targetHash, 7)
	r.standardReplay = policyReplayState(3, pivotSeq, targetSeq)

	assert.False(t, r.maybeRebaseForMissingReplayAncestry(time.Unix(101, 0)))
	assert.True(t, r.standardReplay.active)
	assert.True(t, r.standardReplay.pivotReady)
	assert.Equal(t, uint64(0), r.FastSyncMetrics().ReplayPipelineRebasesStarted)
}

func TestFrozenPivotRecoveryNoPeerPreservesSession(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	pivotSeq := uint32(100)
	targetSeq := pivotSeq + seqHashRetain + 1
	targetHash := [32]byte{0x25}
	r.recordValidationCatchupTarget(targetSeq, targetHash, 0, catchupSourceQuorum)
	r.standardReplay = policyReplayState(3, pivotSeq, targetSeq)

	assert.False(t, r.maybeRebaseForMissingReplayAncestry(time.Unix(101, 0)))
	assert.True(t, r.standardReplay.active)
	assert.True(t, r.standardReplay.pivotReady)
	assert.Equal(t, pivotSeq, r.standardReplay.pivotSeq)
	assert.Equal(t, uint64(0), r.FastSyncMetrics().ReplayPipelineRebasesStarted)
}

func TestFrozenPivotRecoveryIgnoresStalePendingRebase(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	pivotSeq := uint32(100)
	targetSeq := pivotSeq + 10
	targetHash := [32]byte{0x26}
	trackCatchupPeer(r, 7, targetSeq, targetHash)
	r.recordValidationCatchupTarget(targetSeq, targetHash, 7, catchupSourceQuorum)
	r.standardReplay = policyReplayState(3, pivotSeq, targetSeq)
	r.standardReplay.rebasePending = true
	r.standardReplay.rebaseGeneration = 2
	r.standardReplay.rebaseAnchorSeq = pivotSeq
	r.standardReplay.rebaseTargetSeq = targetSeq
	r.standardReplay.rebaseTargetHash = targetHash

	assert.False(t, r.rebootstrapFrozenPivotIfStalled(time.Unix(101, 0)))
	assert.True(t, r.standardReplay.active)
	assert.Equal(t, pivotSeq, r.standardReplay.pivotSeq)
	assert.True(t, r.standardReplay.rebasePending)
}

func TestReplayConvergenceSampler(t *testing.T) {
	base := replayConvergenceSample{generation: 4, anchorSeq: 100, targetSeq: 140, at: time.Unix(100, 0)}
	tests := []struct {
		name         string
		current      replayConvergenceSample
		valid        bool
		nonShrinking bool
		replayRate   float64
		headRate     float64
		etaAvailable bool
	}{
		{
			name:         "replay outruns head",
			current:      replayConvergenceSample{generation: 4, anchorSeq: 120, targetSeq: 150, at: time.Unix(110, 0)},
			valid:        true,
			nonShrinking: false,
			replayRate:   2,
			headRate:     1,
			etaAvailable: true,
		},
		{
			name:         "equal movement",
			current:      replayConvergenceSample{generation: 4, anchorSeq: 110, targetSeq: 150, at: time.Unix(110, 0)},
			valid:        true,
			nonShrinking: true,
			replayRate:   1,
			headRate:     1,
		},
		{
			name:         "head outruns replay",
			current:      replayConvergenceSample{generation: 4, anchorSeq: 105, targetSeq: 150, at: time.Unix(110, 0)},
			valid:        true,
			nonShrinking: true,
			replayRate:   .5,
			headRate:     1,
		},
		{
			name:    "zero duration",
			current: replayConvergenceSample{generation: 4, anchorSeq: 110, targetSeq: 150, at: base.at},
			valid:   false,
		},
		{
			name:    "generation changed",
			current: replayConvergenceSample{generation: 5, anchorSeq: 110, targetSeq: 150, at: time.Unix(110, 0)},
			valid:   false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := sampleReplayConvergence(base, test.current)
			assert.Equal(t, test.valid, observation.valid)
			if !test.valid {
				return
			}
			assert.Equal(t, test.nonShrinking, observation.nonShrinking)
			assert.InDelta(t, test.replayRate, observation.replayRate, .0001)
			assert.InDelta(t, test.headRate, observation.headRate, .0001)
			assert.Equal(t, test.etaAvailable, observation.etaAvailable)
		})
	}
}

func TestPendingReplayRebaseConsumedAfterApplyCommit(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	pivotSeq := uint32(100)
	targetSeq := pivotSeq + 10
	targetHash := [32]byte{0x27}
	trackCatchupPeer(r, 7, targetSeq, targetHash)
	r.recordValidationCatchupTarget(targetSeq, targetHash, 7, catchupSourceQuorum)
	r.standardReplay = policyReplayState(3, pivotSeq, targetSeq)
	r.standardReplay.applying = true
	r.standardReplay.rebasePending = true
	r.standardReplay.rebaseGeneration = 3
	r.standardReplay.rebaseAnchorSeq = pivotSeq
	r.standardReplay.rebaseTargetSeq = targetSeq
	r.standardReplay.rebaseTargetHash = targetHash

	require.True(t, r.consumePendingReplayRebaseAfterCommit(time.Unix(101, 0)))
	assert.True(t, r.standardReplay.active)
	assert.False(t, r.standardReplay.pivotReady)
	assert.Equal(t, targetSeq, r.standardReplay.pivotSeq)
}

func TestReplayConvergenceRequiresTwoNonShrinkingWindows(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	targetHash := [32]byte{0x28}
	trackCatchupPeer(r, 7, 200, targetHash)
	r.recordValidationCatchupTarget(200, targetHash, 7, catchupSourceQuorum)
	r.standardReplay = policyReplayState(3, 100, 200)
	r.standardReplay.progressSampleAt = time.Unix(100, 0)
	r.standardReplay.sampleAnchorSeq = 100
	r.standardReplay.sampleTargetSeq = 200

	r.acquisitionMu.Lock()
	r.standardReplay.anchorSeq = 100
	r.standardReplay.targetSeq = 205
	r.acquisitionMu.Unlock()
	r.recordValidationCatchupTarget(205, targetHash, 7, catchupSourceQuorum)
	r.evaluateStandardReplayConvergence(time.Unix(105, 0))
	assert.Equal(t, uint8(1), r.standardReplay.stalledSamples)
	assert.False(t, r.standardReplay.rebasePending)

	r.acquisitionMu.Lock()
	r.standardReplay.anchorSeq = 101
	r.standardReplay.targetSeq = 210
	r.acquisitionMu.Unlock()
	r.recordValidationCatchupTarget(210, targetHash, 7, catchupSourceQuorum)
	r.evaluateStandardReplayConvergence(time.Unix(110, 0))
	assert.Equal(t, uint8(2), r.standardReplay.stalledSamples)
	assert.True(t, r.standardReplay.rebasePending)

	// A later window in which replay gains ground clears the hysteresis
	// counter; it cannot create another pending request by itself.
	r.acquisitionMu.Lock()
	r.standardReplay.anchorSeq = 112
	r.standardReplay.targetSeq = 211
	r.acquisitionMu.Unlock()
	r.recordValidationCatchupTarget(211, targetHash, 7, catchupSourceQuorum)
	r.evaluateStandardReplayConvergence(time.Unix(115, 0))
	assert.Zero(t, r.standardReplay.stalledSamples)
}

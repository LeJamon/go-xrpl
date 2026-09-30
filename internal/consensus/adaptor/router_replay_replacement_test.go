package adaptor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplayReplacementRetainsVerifiedProgressWhileAcquiring(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	completeStandardReplayTestLink(t, r, links[2])
	completeStandardReplayTestLink(t, r, links[0])
	c := r.catchupReplay
	generation := c.standardReplay.generation
	anchorSeq, anchorHash := c.standardReplay.anchorSeq, c.standardReplay.anchorHash
	suffix := c.standardReplay.entries[links[2].seq]
	c.recordValidationCatchupTarget(links[2].seq, links[2].hash, 7, catchupSourceQuorum)
	c.standardReplay.stalledSamples = standardReplayStallWindows
	require.True(t, c.retargetFrozenPivot(generation, anchorSeq, c.standardReplay.pivotSeq, frozenPivotRetargetStalled, time.Now()))
	assert.True(t, c.standardReplay.active)
	assert.True(t, c.standardReplay.pivotReady)
	assert.Equal(t, generation, c.standardReplay.generation)
	assert.Equal(t, anchorSeq, c.standardReplay.anchorSeq)
	assert.Equal(t, anchorHash, c.standardReplay.anchorHash)
	assert.Same(t, suffix, c.standardReplay.entries[links[2].seq])
	held, err := svc.GetLedgerByHash(anchorHash)
	require.NoError(t, err)
	require.NotNil(t, held)
	_, err = held.StateMapSnapshot()
	require.NoError(t, err)
}

func TestReplayReplacementAdvancesOnlyAfterVerificationAndResumesSuffix(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	completeStandardReplayTestLink(t, r, links[2])
	completeStandardReplayTestLink(t, r, links[0])
	c := r.catchupReplay
	generation := c.standardReplay.generation
	require.True(t, c.reserveStandardReplayReplacement(generation, links[1].seq, links[1].hash, 7, time.Now()))
	acquired := c.fetchTracker.Find(links[1].hash)
	require.NotNil(t, acquired)
	require.False(t, acquired.TransactionOnly())
	assert.Equal(t, links[0].hash, c.standardReplay.anchorHash)
	assert.Equal(t, links[0].seq, c.standardReplay.anchorSeq)
	completeIssue1863FullStatePivot(t, r, links[1])
	assert.Nil(t, c.standardReplay.replacement)
	assert.Equal(t, generation, c.standardReplay.generation)
	drainStandardReplayTestPipeline(t, r)
	for _, link := range links {
		held, err := svc.GetLedgerByHash(link.hash)
		require.NoError(t, err)
		require.NotNil(t, held)
		assert.Equal(t, link.ledger.Header().AccountHash, held.Header().AccountHash)
	}
}

func TestReplayReplacementTimeoutRetainsAnchorAndRetriesAfterCooldown(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	completeStandardReplayTestLink(t, r, links[0])
	c := r.catchupReplay
	generation, anchor := c.standardReplay.generation, c.standardReplay.anchorHash
	require.True(t, c.reserveStandardReplayReplacement(generation, links[1].seq, links[1].hash, 7, time.Now()))
	old := c.fetchTracker.Find(links[1].hash)
	require.NotNil(t, old)
	c.failInboundAcquisition(old)
	assert.True(t, c.standardReplay.active)
	assert.Equal(t, anchor, c.standardReplay.anchorHash)
	assert.Equal(t, generation, c.standardReplay.generation)
	require.NotNil(t, c.standardReplay.replacement)
	assert.Nil(t, c.standardReplay.replacement.acquisition)
	c.retryStandardReplayReplacement(time.Now())
	assert.Nil(t, c.fetchTracker.Find(links[1].hash))
	c.retryStandardReplayReplacement(c.standardReplay.replacement.retryAt)
	fresh := c.fetchTracker.Find(links[1].hash)
	require.NotNil(t, fresh)
	require.NotSame(t, old, fresh)
	c.failInboundAcquisition(old)
	assert.Same(t, fresh, c.fetchTracker.Find(links[1].hash))
	assert.Same(t, fresh, c.standardReplay.replacement.acquisition)
}

func TestReplayReplacementRejectsStaleCancellation(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 2)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	c := r.catchupReplay
	before := c.standardReplayIdentityLocked()
	require.True(t, c.reserveStandardReplayReplacement(before.generation, links[0].seq, links[0].hash, 7, time.Now()))
	acquisition := c.fetchTracker.Find(links[0].hash)
	require.NotNil(t, acquisition)
	_, canceled := c.cancelStandardReplayPipelineIdentity(before, "stale_retarget")
	assert.False(t, canceled)
	assert.True(t, c.standardReplay.active)
	assert.Same(t, acquisition, c.standardReplay.replacement.acquisition)
	current := c.standardReplayIdentityLocked()
	_, canceled = c.cancelStandardReplayPipelineIdentity(current, "current_retarget")
	require.True(t, canceled)
	assert.Nil(t, c.standardReplay.replacement)
	assert.Nil(t, c.fetchTracker.Find(links[0].hash))
	c.failInboundAcquisition(acquisition)
	assert.False(t, c.standardReplay.active)
}

func TestReplayCompletionRetiresUnneededReplacement(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	completeStandardReplayTestLink(t, r, links[2])
	completeStandardReplayTestLink(t, r, links[1])
	c := r.catchupReplay
	require.True(t, c.reserveStandardReplayReplacement(c.standardReplay.generation, links[2].seq, links[2].hash, 7, time.Now()))
	acquisition := c.fetchTracker.Find(links[2].hash)
	require.NotNil(t, acquisition)
	completeStandardReplayTestLink(t, r, links[0])
	drainStandardReplayTestPipeline(t, r)
	assert.Nil(t, c.standardReplay.replacement)
	assert.Nil(t, c.fetchTracker.Find(links[2].hash))
	assert.Equal(t, uint64(3), c.replayPipelineApplied.Load())
}

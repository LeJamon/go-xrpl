package adaptor

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"

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
	assert.NotEqual(t, links[0].hash, c.consensusRecovery.stepHash)
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

func TestReplayRecoveryRetains177AppliedLedgersAndPreparedSuffix(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 181)
	armStandardReplayTestPipeline(t, r, a, sender, links[:3])
	c := r.catchupReplay
	target := links[len(links)-1]
	c.recordValidationCatchupTarget(target.seq, target.hash, 7, catchupSourceQuorum)
	require.True(t, c.continueFrozenPivotRecovery(target.seq, target.hash, 7))
	generation := c.standardReplay.generation
	for _, link := range links[:177] {
		completeStandardReplayTestLink(t, r, link)
		drainStandardReplayTestPipeline(t, r)
	}
	require.Equal(t, uint64(177), c.replayPipelineApplied.Load())
	completeStandardReplayTestLink(t, r, links[178])
	completeStandardReplayTestLink(t, r, links[180])
	future := c.fetchTracker.Find(links[179].hash)
	require.NotNil(t, future)
	c.failInboundAcquisition(future)
	head := c.fetchTracker.Find(links[177].hash)
	require.NotNil(t, head)
	c.failInboundAcquisition(head)
	for attempt := uint8(0); attempt < standardReplayAvailabilityRetryLimit; attempt++ {
		sender.mu.Lock()
		sender.acquisitionPeers = []uint64{uint64(8 + attempt)}
		sender.mu.Unlock()
		c.retryStandardReplayAvailability(time.Now())
		entry := c.standardReplay.entries[links[177].seq]
		require.Equal(t, standardReplayAvailabilityRetryStarted, c.retryStandardReplayAvailability(entry.availabilityNextRetryAt))
		retry := c.fetchTracker.Find(links[177].hash)
		require.NotNil(t, retry)
		c.failInboundAcquisition(retry)
	}
	c.maintenanceTick()
	require.NotNil(t, c.standardReplay.replacement)
	assert.Equal(t, generation, c.standardReplay.generation)
	assert.Equal(t, links[176].hash, c.standardReplay.anchorHash)
	assert.Equal(t, uint64(177), c.replayPipelineApplied.Load())
	assert.False(t, c.standardReplay.entries[links[178].seq].readyAt.IsZero())
	assert.False(t, c.standardReplay.entries[links[180].seq].readyAt.IsZero())
	assert.True(t, c.standardReplay.entries[links[179].seq].availabilityPending)
	assert.LessOrEqual(t, len(c.standardReplay.entries), standardReplayPreparedLimit)
	completeIssue1863FullStatePivot(t, r, links[177])
	drainStandardReplayTestPipeline(t, r)
	require.Equal(t, links[178].hash, c.standardReplay.anchorHash)
	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{11}
	sender.mu.Unlock()
	c.retryStandardReplayAvailability(time.Now())
	entry := c.standardReplay.entries[links[179].seq]
	require.Equal(t, standardReplayAvailabilityRetryStarted, c.retryStandardReplayAvailability(entry.availabilityNextRetryAt))
	completeStandardReplayTestLink(t, r, links[179])
	drainStandardReplayTestPipeline(t, r)
	assert.False(t, c.standardReplay.active)
	assert.Equal(t, uint64(180), c.replayPipelineApplied.Load())
	assert.Equal(t, generation, c.standardReplay.generation)
	for _, link := range []standardReplayTestLink{links[176], links[177], links[180]} {
		held, err := svc.GetLedgerByHash(link.hash)
		require.NoError(t, err)
		require.NotNil(t, held)
		assert.Equal(t, link.ledger.Header().AccountHash, held.Header().AccountHash)
	}
}

func TestReplayReplacementAdoptsInFlightAncestorAndKeepsTarget(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 5)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	c := r.catchupReplay
	completeStandardReplayTestLink(t, r, links[0])
	for _, link := range links[3:] {
		completeStandardReplayTestLink(t, r, link)
		c.recordAcquiredSeqHash(link.seq, link.hash, link.ledger.ParentHash())
	}
	target, pivot := links[4], links[2]
	c.recordValidationCatchupTarget(target.seq, target.hash, 7, catchupSourceQuorum)
	old := c.fetchTracker.Find(pivot.hash)
	require.NotNil(t, old)
	c.acquisitionMu.Lock()
	require.True(t, c.discardInboundAcquisitionLocked(old))
	admission := c.startFrozenPivotReplacementLocked(pivot.seq, pivot.hash, 7)
	c.acquisitionMu.Unlock()
	c.retireLegacyAcquisitions([]*inbound.Ledger{old})
	require.Equal(t, fullStateAdmissionStarted, admission.outcome)
	generation := c.standardReplay.generation
	require.True(t, c.reserveStandardReplayReplacement(generation, links[1].seq, links[1].hash, 7, time.Now()))
	require.NotNil(t, c.standardReplay.replacement)
	assert.Same(t, admission.acquisition, c.standardReplay.replacement.acquisition)
	assert.Equal(t, pivot.hash, c.standardReplay.replacement.hash)
	assert.Equal(t, target.hash, c.standardReplay.targetHash)
	assert.Equal(t, links[0].hash, c.standardReplay.anchorHash)
	completeIssue1863FullStatePivot(t, r, pivot)
	drainStandardReplayTestPipeline(t, r)
	assert.Equal(t, generation, c.standardReplay.generation)
	assert.False(t, c.standardReplay.active)
	held, err := svc.GetLedgerByHash(target.hash)
	require.NoError(t, err)
	require.NotNil(t, held)
}

func TestReplayRetryReservesResidentCapacityDuringDelayedPivot(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 14)
	armStandardReplayTestPipeline(t, r, a, sender, links[:3])
	c := r.catchupReplay
	target := links[len(links)-1]
	c.recordValidationCatchupTarget(target.seq, target.hash, 7, catchupSourceQuorum)
	require.True(t, c.continueFrozenPivotRecovery(target.seq, target.hash, 7))
	c.standardReplay.pivotReady = false
	completeStandardReplayTestLink(t, r, links[0])
	c.standardReplay.entries[links[0].seq].durable = true
	failed := c.fetchTracker.Find(links[1].hash)
	require.NotNil(t, failed)
	c.failInboundAcquisition(failed)
	require.True(t, c.refillStandardReplayCollector(7))
	c.standardReplay.pivotReady = true
	c.drainStandardReplayPipeline()
	require.Equal(t, links[0].hash, c.standardReplay.anchorHash)
	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{7}
	sender.mu.Unlock()
	now := time.Now()
	require.Equal(t, standardReplayAvailabilityRetryWaiting, c.retryStandardReplayAvailability(now))
	require.Equal(t, standardReplayAvailabilityRetryStarted, c.retryStandardReplayAvailability(now.Add(time.Second)))
	assert.LessOrEqual(t, c.standardReplayResidentCountLocked(), standardReplayPipelineWindow)
	active := 0
	for _, il := range c.fetchTracker.Active() {
		if il.TransactionOnly() {
			active++
		}
	}
	assert.LessOrEqual(t, active, standardReplayPipelineWindow)
}

func TestReplayReplacementCancellationClearsStandaloneStep(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links[:2])
	c := r.catchupReplay
	require.True(t, c.reserveStandardReplayReplacement(c.standardReplay.generation, links[2].seq, links[2].hash, 7, time.Now()))
	require.Equal(t, links[2].hash, c.consensusRecovery.stepHash)
	require.Nil(t, c.standardReplay.entries[links[2].seq])
	_, canceled := c.cancelStandardReplayPipelineIdentity(c.standardReplayIdentityLocked(), "test_cancel")
	require.True(t, canceled)
	assert.Zero(t, c.consensusRecovery.stepHash)
	assert.Nil(t, c.fetchTracker.Find(links[2].hash))
}

func TestReplayReplacementRemainsOwnedWhenTrustedTargetAdvances(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 6)
	armStandardReplayTestPipeline(t, r, a, sender, links[:2])
	c := r.catchupReplay
	require.True(t, c.reserveStandardReplayReplacement(c.standardReplay.generation, links[2].seq, links[2].hash, 7, time.Now()))
	replacement := c.fetchTracker.Find(links[2].hash)
	require.NotNil(t, replacement)
	target := links[5]
	c.recordValidationCatchupTarget(target.seq, target.hash, 7, catchupSourceQuorum)
	require.NoError(t, a.RequestLedger(consensus.LedgerID(target.hash)))
	require.Equal(t, target.hash, c.standardReplay.targetHash)
	now := time.Now()
	replacement.RearmTimer(now)
	for range 2 {
		now = now.Add(4 * time.Second)
		require.Equal(t, inbound.TimerEscalate, replacement.OnTimer(now))
		replacement.RearmTimer(now)
	}
	c.onLedgerFullyValidated(target.seq, target.hash)
	assert.Same(t, replacement, c.fetchTracker.Find(links[2].hash))
	require.NotNil(t, c.standardReplay.replacement)
	assert.Same(t, replacement, c.standardReplay.replacement.acquisition)
	assert.Equal(t, target.hash, c.standardReplay.targetHash)
}

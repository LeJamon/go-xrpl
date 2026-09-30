package adaptor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type standardReplayTestLink struct {
	response *message.ReplayDeltaResponse
	ledger   *ledger.Ledger
	hash     [32]byte
	seq      uint32
}

func buildStandardReplayTestChain(t *testing.T, r *Router, parent *ledger.Ledger, count int) []standardReplayTestLink {
	t.Helper()
	links := make([]standardReplayTestLink, 0, count)
	for range count {
		response, child, hash, seq := buildSuccessorAgainstParent(t, parent)
		r.catchupReplay.recordSeqHash(seq, hash, parent.Hash(), true)
		links = append(links, standardReplayTestLink{response: response, ledger: child, hash: hash, seq: seq})
		parent = child
	}
	return links
}

func buildAlternativeReplaySuccessor(t *testing.T, parent *ledger.Ledger, salt time.Duration) standardReplayTestLink {
	t.Helper()
	closeTime := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC).
		Add(time.Duration(parent.Sequence()) * time.Second).
		Add(salt)
	child, err := ledger.NewOpen(parent, closeTime)
	require.NoError(t, err)
	require.NoError(t, child.Close(closeTime, 0))
	h := child.Header()
	return standardReplayTestLink{
		response: &message.ReplayDeltaResponse{
			LedgerHash:   h.Hash[:],
			LedgerHeader: header.AddRaw(h, false),
		},
		ledger: child,
		hash:   h.Hash,
		seq:    h.LedgerIndex,
	}
}

func completeStandardReplayTestLink(t *testing.T, r *Router, link standardReplayTestLink) {
	t.Helper()
	il := r.catchupReplay.fetchTracker.Find(link.hash)
	require.NotNil(t, il)
	require.True(t, il.TransactionOnly())
	require.NoError(t, il.GotBase([]message.LedgerNode{
		{NodeData: link.response.LedgerHeader},
		{NodeData: []byte{1}},
	}))
	require.True(t, il.IsComplete())
	r.catchupReplay.completeInboundLedger(il)
}

func drainStandardReplayTestPipeline(t *testing.T, r *Router) {
	t.Helper()
	maxDrains := len(r.catchupReplay.standardReplay.entries) + 1
	for drains := 0; r.catchupReplay.standardReplay.applying; drains++ {
		require.Less(t, drains, maxDrains, "replay drain did not finish")
		select {
		case <-r.catchupReplay.standardReplayDrainWake:
			r.catchupReplay.drainStandardReplayPipeline()
		default:
			require.FailNow(t, "replay apply batch did not reschedule through the router loop")
		}
	}
}

func armStandardReplayTestPipeline(
	t *testing.T,
	r *Router,
	a *Adaptor,
	sender *recordingSender,
	links []standardReplayTestLink,
) {
	t.Helper()
	require.NotEmpty(t, links)
	sender.mu.Lock()
	sender.peerSupportsReplay = false
	sender.mu.Unlock()
	trackCatchupPeer(r, 7, links[len(links)-1].seq, links[len(links)-1].hash)
	require.NoError(t, a.RequestLedger(consensus.LedgerID(links[len(links)-1].hash)))
}

func failStandardReplayAvailabilityAt(t *testing.T, c *catchupReplayCoordinator, il *inbound.Ledger, at time.Time) {
	t.Helper()
	retirement, _, removed := c.removeInboundAcquisitionWithSession(il, il.Snapshot(), false)
	require.True(t, removed)
	c.retireStandardReplay(retirement)
	require.True(t, c.failStandardReplayPipelineEntryAt(il, standardReplayFailureAvailability, at))
}

func TestStandardReplayPipelineAppliesReadySuccessorsInOrder(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	require.Len(t, sender.legacyCalls(), 3)
	require.NoError(t, a.RequestLedger(consensus.LedgerID(links[len(links)-1].hash)))
	assert.Equal(t, links[0].hash, r.catchupReplay.consensusRecovery.stepHash)

	completeStandardReplayTestLink(t, r, links[2])
	completeStandardReplayTestLink(t, r, links[1])
	for _, link := range links {
		stored, _ := svc.GetLedgerByHash(link.hash)
		assert.Nil(t, stored)
	}
	metrics := r.FastSyncMetrics()
	assert.Equal(t, uint32(2), metrics.ReplayPipelineReadyDepth)
	assert.Equal(t, links[0].seq, metrics.ReplayPipelineHeadSeq)
	assert.False(t, r.catchupReplay.standardReplay.headBlockedAt.IsZero())

	completeStandardReplayTestLink(t, r, links[0])
	drainStandardReplayTestPipeline(t, r)
	for _, link := range links {
		stored, lookupErr := svc.GetLedgerByHash(link.hash)
		require.NoError(t, lookupErr)
		require.NotNil(t, stored)
		assert.Equal(t, link.seq, stored.Sequence())
	}
	metrics = r.FastSyncMetrics()
	assert.Equal(t, uint64(3), metrics.ReplayPipelineReady)
	assert.Equal(t, uint64(3), metrics.ReplayPipelineApplied)
	assert.Zero(t, metrics.ReplayPipelineDepth)
	assert.Zero(t, metrics.ReplayPipelineReadyDepth)
}

func TestStandardReplayReplacesFullStateSuccessor(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	old, created := r.catchupReplay.fetchTracker.GetOrCreate(links[0].hash, func() *inbound.Ledger {
		return inbound.New(links[0].hash, links[0].seq, 7, r.logger, r.catchupReplay.acquisitionOpts()...)
	})
	require.True(t, created)
	require.False(t, old.TransactionOnly())
	armStandardReplayTestPipeline(t, r, a, sender, links)
	replacement := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, replacement)
	require.NotSame(t, old, replacement)
	require.True(t, replacement.TransactionOnly())
	// A late completion/removal from the retired walker cannot erase replay.
	require.False(t, r.catchupReplay.fetchTracker.DiscardExpected(old))
	for _, link := range links {
		completeStandardReplayTestLink(t, r, link)
	}
	require.Eventually(t, func() bool {
		return r.catchupReplay.replayPipelineApplied.Load() == 3
	}, 5*time.Second, 10*time.Millisecond)
}

func TestStandardReplayPipelineYieldsAfterBoundedApplyBatch(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), standardReplayApplyBatch+1)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	require.Len(t, sender.legacyCalls(), standardReplayApplyBatch)

	// Make the whole resident window ready before completing its head. The
	// head completion then enters one apply call with a full batch available.
	for i := 1; i < standardReplayApplyBatch; i++ {
		completeStandardReplayTestLink(t, r, links[i])
	}
	completeStandardReplayTestLink(t, r, links[0])

	metrics := r.FastSyncMetrics()
	// The time budget may yield before the count limit on a busy runner.
	require.Positive(t, metrics.ReplayPipelineApplied)
	require.LessOrEqual(t, metrics.ReplayPipelineApplied, uint64(standardReplayApplyBatch))
	require.True(t, r.catchupReplay.standardReplay.active)
	require.True(t, r.catchupReplay.standardReplay.applying)
	require.Len(t, r.catchupReplay.standardReplayDrainWake, 1,
		"a ready replay batch must reschedule through the router loop before continuing")
	require.NotNil(t, r.catchupReplay.fetchTracker.Find(links[standardReplayApplyBatch].hash),
		"collector refill must continue while the applier yields")
}

func TestStandardReplayPipelineBoundsAndRefillsWindow(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), standardReplayPipelineWindow+2)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	require.Len(t, sender.legacyCalls(), standardReplayPipelineWindow)
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(links[standardReplayPipelineWindow].hash))
	metrics := r.FastSyncMetrics()
	assert.Equal(t, uint32(standardReplayPipelineWindow), metrics.ReplayPipelineDepth)
	assert.Equal(t, uint32(standardReplayPipelineWindow), metrics.ReplayPipelineWindow)

	completeStandardReplayTestLink(t, r, links[0])
	require.Len(t, sender.legacyCalls(), standardReplayPipelineWindow+1)
	require.NotNil(t, r.catchupReplay.fetchTracker.Find(links[standardReplayPipelineWindow].hash))
	metrics = r.FastSyncMetrics()
	assert.Equal(t, uint64(standardReplayPipelineWindow+1), metrics.ReplayPipelineRequested)
	assert.Equal(t, uint32(standardReplayPipelineWindow), metrics.ReplayPipelineDepth)
}

func TestStandardReplayPipelineAdvancesPastHeldSuccessor(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	storeRecoveryLedger(t, svc, links[0].ledger)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	require.Equal(t, []legacyBaseCall{
		{peerID: 7, hash: links[1].hash, seq: links[1].seq},
		{peerID: 7, hash: links[2].hash, seq: links[2].seq},
	}, sender.legacyCalls())
	assert.Equal(t, links[0].seq, r.catchupReplay.standardReplay.anchorSeq)
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(links[0].hash))
}

func TestStandardReplayPipelineCompletesAllLocalTargetWithoutNetwork(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	for _, link := range links {
		storeRecoveryLedger(t, svc, link.ledger)
	}
	engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
	r.engine = engine
	r.catchupReplay.engine = r.engine
	armStandardReplayTestPipeline(t, r, a, sender, links)

	assert.Empty(t, sender.legacyCalls())
	assert.False(t, r.catchupReplay.standardReplay.active)
	assert.Equal(t, [32]byte{}, r.catchupReplay.consensusRecovery.targetHash)
	assert.Equal(t, []consensus.LedgerID{consensus.LedgerID(links[2].hash)}, engine.getLedgers())
}

func TestStandardReplayPipelineAcquiresRecoveryHashAtBuildingSequence(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	r.engine = &mockEngine{buildingSeq: links[0].seq}
	r.catchupReplay.engine = r.engine
	armStandardReplayTestPipeline(t, r, a, sender, links)

	require.Len(t, sender.legacyCalls(), len(links))
	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.Equal(t, links[0].hash, r.catchupReplay.consensusRecovery.stepHash)
}

func TestStandardReplayPipelineCancelsSupersededFork(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	oldLinks := buildStandardReplayTestChain(t, r, closed, 3)
	armStandardReplayTestPipeline(t, r, a, sender, oldLinks)
	stale := r.catchupReplay.fetchTracker.Find(oldLinks[0].hash)
	require.NotNil(t, stale)

	newLinks := make([]standardReplayTestLink, 0, 3)
	parent := closed
	for i := 1; i <= 3; i++ {
		link := buildAlternativeReplaySuccessor(t, parent, time.Duration(i)*time.Minute)
		r.catchupReplay.recordSeqHash(link.seq, link.hash, parent.Hash(), true)
		newLinks = append(newLinks, link)
		parent = link.ledger
	}
	trackCatchupPeer(r, 7, newLinks[len(newLinks)-1].seq)
	require.NoError(t, a.RequestLedger(consensus.LedgerID(newLinks[len(newLinks)-1].hash)))

	for _, link := range oldLinks {
		assert.Nil(t, r.catchupReplay.fetchTracker.Find(link.hash))
	}
	for _, link := range newLinks {
		require.NotNil(t, r.catchupReplay.fetchTracker.Find(link.hash))
	}
	require.NoError(t, stale.GotBase([]message.LedgerNode{
		{NodeData: oldLinks[0].response.LedgerHeader},
		{NodeData: []byte{1}},
	}))
	r.catchupReplay.completeInboundLedger(stale)
	stored, _ := svc.GetLedgerByHash(oldLinks[0].hash)
	assert.Nil(t, stored)
	assert.GreaterOrEqual(t, r.FastSyncMetrics().ReplayPipelineDiscarded, uint64(len(oldLinks)))
}

func TestStandardReplayPipelineDefersUnrelatedFullStateAcquisition(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), standardReplayPipelineWindow)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	fullStateHash := [32]byte{0xfa, 0x57}
	r.catchupReplay.acquisitionMu.Lock()
	require.True(t, r.catchupReplay.canAdmitCatchupLocked(fullStateHash, maxConcurrentCatchup))
	r.catchupReplay.startLedgerAcquisitionLegacyLocked(links[len(links)-1].seq+1, fullStateHash, 7)
	r.catchupReplay.acquisitionMu.Unlock()
	fullState := r.catchupReplay.fetchTracker.Find(fullStateHash)
	assert.Nil(t, fullState)
	assert.Len(t, sender.legacyCalls(), len(links))
}

func TestStandardReplayPipelineReplacesRedundantFullStateAcquisition(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	sender.mu.Lock()
	sender.peerSupportsReplay = false
	sender.mu.Unlock()
	trackCatchupPeer(r, 7, links[len(links)-1].seq)

	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.startLedgerAcquisitionLegacyLocked(links[0].seq, links[0].hash, 7)
	r.catchupReplay.acquisitionMu.Unlock()
	require.NoError(t, a.RequestLedger(consensus.LedgerID(links[len(links)-1].hash)))

	head := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, head)
	assert.True(t, head.TransactionOnly())
	assert.True(t, r.catchupReplay.standardReplay.active)
	for _, link := range links[1:] {
		require.NotNil(t, r.catchupReplay.fetchTracker.Find(link.hash))
		assert.True(t, r.catchupReplay.fetchTracker.Find(link.hash).TransactionOnly())
	}
}

func TestStandardReplayPipelineParksUnavailableHeadForBoundedRetries(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	head := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, head)
	r.catchupReplay.acquisitionMu.Lock()
	generation := r.catchupReplay.standardReplay.generation
	anchorSeq := r.catchupReplay.standardReplay.anchorSeq
	anchorHash := r.catchupReplay.standardReplay.anchorHash
	r.catchupReplay.acquisitionMu.Unlock()
	now := time.Now()
	for range 6 {
		now = now.Add(4 * time.Second)
		require.Equal(t, inbound.TimerEscalate, head.OnTimer(now))
		r.catchupReplay.escalateAcquisition(head, now)
	}
	now = now.Add(4 * time.Second)
	require.Equal(t, inbound.TimerFailed, head.OnTimer(now))
	r.catchupReplay.failInboundAcquisition(head)

	assert.Nil(t, r.catchupReplay.fetchTracker.Find(links[0].hash))
	r.catchupReplay.acquisitionMu.Lock()
	entry := r.catchupReplay.standardReplay.entries[links[0].seq]
	require.NotNil(t, entry)
	assert.True(t, entry.availabilityPending)
	assert.False(t, entry.availabilityExhausted)
	assert.False(t, entry.failed)
	assert.Equal(t, generation, r.catchupReplay.standardReplay.generation)
	assert.Equal(t, anchorSeq, r.catchupReplay.standardReplay.anchorSeq)
	assert.Equal(t, anchorHash, r.catchupReplay.standardReplay.anchorHash)
	r.catchupReplay.acquisitionMu.Unlock()
	for _, link := range links[1:] {
		assert.NotNil(t, r.catchupReplay.fetchTracker.Find(link.hash))
	}
	metrics := r.FastSyncMetrics()
	assert.Zero(t, metrics.ReplayPipelineFallbacks)
	assert.Equal(t, uint64(7), metrics.ReplayPipelineRetried)
	assert.Zero(t, metrics.ReplayPipelineDiscarded)
	r.catchupReplay.acquisitionMu.Lock()
	entry.availabilityNextRetryAt = time.Time{}
	r.catchupReplay.acquisitionMu.Unlock()
	assert.Equal(t, standardReplayAvailabilityRetryWaiting,
		r.catchupReplay.retryStandardReplayAvailability(time.Now()),
		"peer scarcity must park the head without consuming an attempt")
	r.catchupReplay.acquisitionMu.Lock()
	assert.Zero(t, entry.availabilityRetries)
	r.catchupReplay.acquisitionMu.Unlock()

	for i, peerID := range []uint64{8, 9, 10} {
		sender.mu.Lock()
		sender.acquisitionPeers = []uint64{peerID}
		sender.mu.Unlock()
		r.catchupReplay.acquisitionMu.Lock()
		entry = r.catchupReplay.standardReplay.entries[links[0].seq]
		entry.availabilityNextRetryAt = time.Time{}
		r.catchupReplay.acquisitionMu.Unlock()
		require.Equal(t, standardReplayAvailabilityRetryStarted,
			r.catchupReplay.retryStandardReplayAvailability(time.Now()))
		retry := r.catchupReplay.fetchTracker.Find(links[0].hash)
		require.NotNil(t, retry)
		require.True(t, retry.TransactionOnly())
		r.catchupReplay.failInboundAcquisition(retry)
		r.catchupReplay.acquisitionMu.Lock()
		entry = r.catchupReplay.standardReplay.entries[links[0].seq]
		if i < 2 {
			assert.True(t, entry.availabilityPending)
			assert.False(t, entry.availabilityExhausted)
		} else {
			assert.False(t, entry.availabilityPending)
			assert.True(t, entry.availabilityExhausted)
		}
		r.catchupReplay.acquisitionMu.Unlock()
	}

	seq, hash, exhausted := r.catchupReplay.standardReplayAvailabilityExhausted()
	assert.True(t, exhausted)
	assert.Equal(t, links[0].seq, seq)
	assert.Equal(t, links[0].hash, hash)
	assert.Zero(t, r.FastSyncMetrics().ReplayPipelineFallbacks)
	assert.Equal(t, generation, r.catchupReplay.standardReplay.generation)
	assert.Equal(t, anchorSeq, r.catchupReplay.standardReplay.anchorSeq)
	assert.Equal(t, anchorHash, r.catchupReplay.standardReplay.anchorHash)
	for _, link := range links[1:] {
		assert.NotNil(t, r.catchupReplay.fetchTracker.Find(link.hash))
	}
}

func TestStandardReplayAvailabilityRetryPrioritizesActionableHead(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	// A future timeout is parked while the first successor is still the
	// actionable head. Completing a prepared future does not make it eligible
	// for a retry ahead of that head.
	completeStandardReplayTestLink(t, r, links[1])
	future := r.catchupReplay.fetchTracker.Find(links[2].hash)
	require.NotNil(t, future)
	r.catchupReplay.failInboundAcquisition(future)
	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{8}
	sender.mu.Unlock()
	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.standardReplay.entries[links[2].seq].availabilityNextRetryAt = time.Time{}
	r.catchupReplay.acquisitionMu.Unlock()
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(links[2].hash))
	assert.Equal(t, standardReplayAvailabilityRetryNone,
		r.catchupReplay.retryStandardReplayAvailability(time.Now()))
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(links[2].hash))

	head := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, head)
	r.catchupReplay.failInboundAcquisition(head)
	r.catchupReplay.acquisitionMu.Lock()
	entry := r.catchupReplay.standardReplay.entries[links[0].seq]
	entry.availabilityDeadlineAt = time.Now().Add(standardReplayAvailabilityWaitWindow)
	entry.availabilityNextRetryAt = time.Time{}
	r.catchupReplay.acquisitionMu.Unlock()
	assert.Equal(t, standardReplayAvailabilityRetryStarted,
		r.catchupReplay.retryStandardReplayAvailability(time.Now()))
	retriedHead := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, retriedHead)
	assert.True(t, retriedHead.TransactionOnly())
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(links[2].hash))
	r.catchupReplay.acquisitionMu.Lock()
	assert.True(t, r.catchupReplay.standardReplay.entries[links[2].seq].availabilityPending)
	r.catchupReplay.acquisitionMu.Unlock()
}

func TestStandardReplayFutureAvailabilityWaitStartsWhenHeadIsActionable(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	future := r.catchupReplay.fetchTracker.Find(links[2].hash)
	require.NotNil(t, future)
	r.catchupReplay.failInboundAcquisition(future)
	r.catchupReplay.acquisitionMu.Lock()
	futureEntry := r.catchupReplay.standardReplay.entries[links[2].seq]
	assert.True(t, futureEntry.availabilityPending)
	assert.Zero(t, futureEntry.availabilityDeadlineAt)
	r.catchupReplay.acquisitionMu.Unlock()

	late := time.Now().Add(2 * standardReplayAvailabilityWaitWindow)
	assert.Equal(t, standardReplayAvailabilityRetryNone,
		r.catchupReplay.retryStandardReplayAvailability(late))
	r.catchupReplay.acquisitionMu.Lock()
	assert.True(t, futureEntry.availabilityPending)
	assert.False(t, futureEntry.availabilityExhausted)
	assert.Zero(t, futureEntry.availabilityDeadlineAt)
	r.catchupReplay.acquisitionMu.Unlock()

	head := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, head)
	r.catchupReplay.failInboundAcquisition(head)
	assert.Equal(t, standardReplayAvailabilityRetryWaiting,
		r.catchupReplay.retryStandardReplayAvailability(late))
	r.catchupReplay.acquisitionMu.Lock()
	headEntry := r.catchupReplay.standardReplay.entries[links[0].seq]
	assert.True(t, headEntry.availabilityPending)
	assert.False(t, headEntry.availabilityExhausted)
	assert.True(t, late.Before(headEntry.availabilityDeadlineAt))
	r.catchupReplay.acquisitionMu.Unlock()
}

func TestStandardReplayAvailabilityRetryReusesTriedPeerAfterFreshChoice(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	head := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, head)
	r.catchupReplay.failInboundAcquisition(head)

	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{7, 8}
	sender.mu.Unlock()
	r.catchupReplay.acquisitionMu.Lock()
	entry := r.catchupReplay.standardReplay.entries[links[0].seq]
	now := time.Now()
	entry.availabilityDeadlineAt = now.Add(standardReplayAvailabilityWaitWindow)
	entry.availabilityNextRetryAt = now.Add(-time.Second)
	r.catchupReplay.acquisitionMu.Unlock()
	require.Equal(t, standardReplayAvailabilityRetryStarted,
		r.catchupReplay.retryStandardReplayAvailability(now))
	retry := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, retry)
	assert.Equal(t, uint64(8), retry.PeerID(), "a fresh peer should be preferred")
	r.catchupReplay.failInboundAcquisition(retry)

	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{8}
	sender.mu.Unlock()
	r.catchupReplay.acquisitionMu.Lock()
	entry = r.catchupReplay.standardReplay.entries[links[0].seq]
	now = time.Now()
	entry.availabilityNextRetryAt = now.Add(-time.Second)
	r.catchupReplay.acquisitionMu.Unlock()
	require.Equal(t, standardReplayAvailabilityRetryStarted,
		r.catchupReplay.retryStandardReplayAvailability(now))
	retry = r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, retry)
	assert.Equal(t, uint64(8), retry.PeerID(), "a previously tried peer remains a bounded fallback")
}

func TestStandardReplayAvailabilityRetryBackoffBoundaries(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	head := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, head)
	r.catchupReplay.failInboundAcquisition(head)
	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{8}
	sender.mu.Unlock()

	t0 := time.Now()
	assert.Equal(t, standardReplayAvailabilityRetryWaiting,
		r.catchupReplay.retryStandardReplayAvailability(t0))
	r.catchupReplay.acquisitionMu.Lock()
	entry := r.catchupReplay.standardReplay.entries[links[0].seq]
	firstDue := entry.availabilityNextRetryAt
	assert.Equal(t, t0.Add(time.Second), firstDue)
	r.catchupReplay.acquisitionMu.Unlock()
	assert.Equal(t, standardReplayAvailabilityRetryWaiting,
		r.catchupReplay.retryStandardReplayAvailability(firstDue.Add(-time.Nanosecond)))
	assert.Equal(t, standardReplayAvailabilityRetryStarted,
		r.catchupReplay.retryStandardReplayAvailability(firstDue))
	first := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, first)
	failStandardReplayAvailabilityAt(t, r.catchupReplay, first, firstDue)

	r.catchupReplay.acquisitionMu.Lock()
	entry = r.catchupReplay.standardReplay.entries[links[0].seq]
	secondDue := entry.availabilityNextRetryAt
	assert.Equal(t, firstDue.Add(2*time.Second), secondDue)
	r.catchupReplay.acquisitionMu.Unlock()
	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{9}
	sender.mu.Unlock()
	assert.Equal(t, standardReplayAvailabilityRetryWaiting,
		r.catchupReplay.retryStandardReplayAvailability(secondDue.Add(-time.Nanosecond)))
	assert.Equal(t, standardReplayAvailabilityRetryStarted,
		r.catchupReplay.retryStandardReplayAvailability(secondDue))
	second := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, second)
	failStandardReplayAvailabilityAt(t, r.catchupReplay, second, secondDue)

	r.catchupReplay.acquisitionMu.Lock()
	entry = r.catchupReplay.standardReplay.entries[links[0].seq]
	thirdDue := entry.availabilityNextRetryAt
	assert.Equal(t, secondDue.Add(4*time.Second), thirdDue)
	r.catchupReplay.acquisitionMu.Unlock()
	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{10}
	sender.mu.Unlock()
	assert.Equal(t, standardReplayAvailabilityRetryWaiting,
		r.catchupReplay.retryStandardReplayAvailability(thirdDue.Add(-time.Nanosecond)))
	assert.Equal(t, standardReplayAvailabilityRetryStarted,
		r.catchupReplay.retryStandardReplayAvailability(thirdDue))
	third := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, third)
	failStandardReplayAvailabilityAt(t, r.catchupReplay, third, thirdDue)

	r.catchupReplay.acquisitionMu.Lock()
	entry = r.catchupReplay.standardReplay.entries[links[0].seq]
	assert.True(t, entry.availabilityExhausted)
	assert.Zero(t, entry.availabilityNextRetryAt)
	r.catchupReplay.acquisitionMu.Unlock()
}

func TestStandardReplayAvailabilityRetryRejectsStaleFailure(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	old := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, old)
	r.catchupReplay.failInboundAcquisition(old)
	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{8}
	sender.mu.Unlock()
	r.catchupReplay.acquisitionMu.Lock()
	entry := r.catchupReplay.standardReplay.entries[links[0].seq]
	entry.availabilityDeadlineAt = time.Now().Add(standardReplayAvailabilityWaitWindow)
	entry.availabilityNextRetryAt = time.Time{}
	r.catchupReplay.acquisitionMu.Unlock()
	require.Equal(t, standardReplayAvailabilityRetryStarted,
		r.catchupReplay.retryStandardReplayAvailability(time.Now()))

	r.catchupReplay.acquisitionMu.Lock()
	entry = r.catchupReplay.standardReplay.entries[links[0].seq]
	retry := entry.acquisition
	require.NotNil(t, retry)
	assert.NotSame(t, old, retry)
	r.catchupReplay.acquisitionMu.Unlock()
	assert.False(t, r.catchupReplay.failStandardReplayPipelineEntry(old, standardReplayFailureAvailability))
	r.catchupReplay.acquisitionMu.Lock()
	assert.Same(t, retry, r.catchupReplay.standardReplay.entries[links[0].seq].acquisition)
	assert.True(t, r.catchupReplay.standardReplay.entries[links[0].seq].availabilityRetrying)
	r.catchupReplay.acquisitionMu.Unlock()
}

func TestReplayFaultBlocksMismatchFallback(t *testing.T) {
	for _, mode := range []string{"pipeline", "standard", "delta"} {
		t.Run(mode, func(t *testing.T) {
			r, a, sender, svc := makeRouter(t)
			_, err := svc.AcceptLedger(context.Background())
			require.NoError(t, err)
			parent := svc.GetClosedLedger()
			_, child, _, _ := buildSuccessorAgainstParent(t, parent)
			state, err := child.StateMapSnapshot()
			require.NoError(t, err)
			state, err = state.SnapshotMutable()
			require.NoError(t, err)
			// Model a peer state change that the local transaction replay cannot derive.
			require.NoError(t, state.Put([32]byte{0x42}, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}))
			h := child.Header()
			h.AccountHash, err = state.Hash()
			require.NoError(t, err)
			h.Hash = header.CalculateHash(h)
			child, err = ledger.NewFromHeader(h, state, shamap.New(shamap.TypeTransaction), parent.Fees())
			require.NoError(t, err)
			first := standardReplayTestLink{
				response: &message.ReplayDeltaResponse{LedgerHash: h.Hash[:], LedgerHeader: header.AddRaw(h, false)},
				ledger:   child, hash: h.Hash, seq: h.LedgerIndex,
			}
			r.catchupReplay.recordSeqHash(first.seq, first.hash, parent.Hash(), true)
			links := append([]standardReplayTestLink{first}, buildStandardReplayTestChain(t, r, child, 2)...)
			var pipelineFallbacks uint64
			switch mode {
			case "pipeline":
				armStandardReplayTestPipeline(t, r, a, sender, links)
				completeStandardReplayTestLink(t, r, first)
				pipelineFallbacks = 1
			case "standard":
				armStandardReplayTestPipeline(t, r, a, sender, links[:1])
				completeStandardReplayTestLink(t, r, first)
			case "delta":
				trackCatchupPeer(r, 7, first.seq, first.hash)
				require.NoError(t, a.RequestLedger(consensus.LedgerID(first.hash)))
				payload, err := message.Encode(first.response)
				require.NoError(t, err)
				r.handleReplayDeltaResponse(&peermanagement.InboundMessage{
					PeerID: 7, Type: message.TypeReplayDeltaResponse, Payload: payload,
				})
			}
			sender.mu.Lock()
			sender.peerSupportsReplay = false
			sender.mu.Unlock()
			trackCatchupPeer(r, 7, links[2].seq, links[2].hash)
			require.NoError(t, a.RequestLedger(consensus.LedgerID(links[2].hash)))
			require.True(t, svc.ReplayBlocked())
			require.Nil(t, r.catchupReplay.fetchTracker.Find(first.hash))
			require.Equal(t, pipelineFallbacks, r.FastSyncMetrics().ReplayPipelineFallbacks)
			for range 3 {
				r.catchupReplay.ensureCatchupAcquisition(links[2].seq, links[2].hash, 7)
				r.catchupReplay.armConsensusCatchup()
				require.Nil(t, r.catchupReplay.fetchTracker.Find(first.hash))
				require.True(t, svc.ReplayBlocked())
			}
			require.Same(t, parent, svc.GetClosedLedger())
			_, err = svc.GetLedgerByHash(first.hash)
			require.Error(t, err)
			require.Zero(t, r.FastSyncMetrics().ReplayPipelineApplied)
		})
	}
}

func TestStandardReplayPipelineDefersFailedEntryUntilFrozenPivotReady(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.standardReplay.pivotReady = false
	r.catchupReplay.standardReplay.applying = false
	generation := r.catchupReplay.standardReplay.generation
	pivotSeq := r.catchupReplay.standardReplay.anchorSeq
	r.catchupReplay.acquisitionMu.Unlock()

	// The first successor may be ready while the full-state pivot is still
	// being verified. A failure farther ahead must not wake the drain yet.
	completeStandardReplayTestLink(t, r, links[0])
	failed := r.catchupReplay.fetchTracker.Find(links[2].hash)
	require.NotNil(t, failed)
	r.catchupReplay.failInboundAcquisition(failed)

	r.catchupReplay.acquisitionMu.Lock()
	require.True(t, r.catchupReplay.standardReplay.active)
	assert.Equal(t, generation, r.catchupReplay.standardReplay.generation)
	assert.Equal(t, pivotSeq, r.catchupReplay.standardReplay.anchorSeq)
	assert.False(t, r.catchupReplay.standardReplay.applying)
	assert.False(t, r.catchupReplay.standardReplay.entries[links[0].seq].readyAt.IsZero())
	assert.True(t, r.catchupReplay.standardReplay.entries[links[2].seq].availabilityPending)
	assert.False(t, r.catchupReplay.standardReplay.entries[links[2].seq].failed)
	identity := r.catchupReplay.standardReplayIdentityLocked()
	r.catchupReplay.acquisitionMu.Unlock()

	assert.Zero(t, r.FastSyncMetrics().ReplayPipelineApplied)
	assert.Zero(t, r.FastSyncMetrics().ReplayPipelineFallbacks)
	_, canceled := r.catchupReplay.cancelStandardReplayPipelineIdentity(identity, "test_retarget")
	require.True(t, canceled)
	r.catchupReplay.ensureCatchupAcquisition(links[2].seq, links[2].hash, 7)
	fallback := r.catchupReplay.fetchTracker.Find(links[2].hash)
	require.NotNil(t, fallback)
	require.True(t, fallback.TransactionOnly())
	for _, link := range links[:2] {
		completeStandardReplayTestLink(t, r, link)
	}
	require.Same(t, fallback, r.catchupReplay.fetchTracker.Find(links[2].hash))
}

func TestStandardReplayPipelineFallsBackWhenPersistenceFails(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	head := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, head)
	r.catchupReplay.handleAcquisitionWorkResult(acquisitionWorkResult{
		ledger: head, complete: true, persistenceErr: errors.New("persistence failed"),
	})

	fallback := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, fallback)
	assert.False(t, fallback.TransactionOnly())
	for _, link := range links[1:] {
		assert.Nil(t, r.catchupReplay.fetchTracker.Find(link.hash))
	}
	metrics := r.FastSyncMetrics()
	assert.Equal(t, uint64(1), metrics.ReplayPipelineFallbacks)
	assert.GreaterOrEqual(t, metrics.ReplayPipelineDiscarded, uint64(len(links)))
}

func TestStandardReplayPipelineFallsBackWhenAcquisitionDataIsRejected(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	head := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, head)
	r.catchupReplay.handleAcquisitionWorkResult(acquisitionWorkResult{
		ledger: head, remove: true, haveSnapshot: true, snapshot: head.Snapshot(),
		err: errors.New("invalid SHAMap node"),
	})

	fallback := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, fallback)
	assert.False(t, fallback.TransactionOnly())
	for _, link := range links[1:] {
		assert.Nil(t, r.catchupReplay.fetchTracker.Find(link.hash))
	}
	assert.Equal(t, uint64(1), r.FastSyncMetrics().ReplayPipelineFallbacks)
}

func TestStandardReplayPipelineFallbackRespectsProtectedLimit(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	r.catchupReplay.acquisitionMu.Lock()
	for i := range maxConcurrentCatchup {
		hash := [32]byte{0xf0, byte(i + 1)}
		admission := r.catchupReplay.startFrozenPivotReplacementLocked(links[len(links)-1].seq+uint32(i)+1, hash, 7)
		require.Equal(t, fullStateAdmissionStarted, admission.outcome)
	}
	r.catchupReplay.acquisitionMu.Unlock()
	require.Equal(t, maxConcurrentCatchup, r.catchupReplay.protectedCatchupInFlight())

	head := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, head)
	now := time.Now()
	for range 6 {
		now = now.Add(4 * time.Second)
		require.Equal(t, inbound.TimerEscalate, head.OnTimer(now))
	}
	now = now.Add(4 * time.Second)
	require.Equal(t, inbound.TimerFailed, head.OnTimer(now))
	r.catchupReplay.failInboundAcquisition(head)

	assert.Nil(t, r.catchupReplay.fetchTracker.Find(links[0].hash))
	assert.Equal(t, maxConcurrentCatchup, r.catchupReplay.protectedCatchupInFlight())
}

func TestStandardReplayPipelineDoesNotFallbackAfterGossipTargetAdvances(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	sender.mu.Lock()
	sender.peerSupportsReplay = false
	sender.mu.Unlock()
	trackCatchupPeer(r, 7, links[len(links)-1].seq, links[len(links)-1].hash)
	r.catchupReplay.recordCatchupTarget(links[len(links)-1].seq, links[len(links)-1].hash, 7)
	r.catchupReplay.armCatchupTowardTarget()
	require.True(t, r.catchupReplay.standardReplay.active)
	require.Equal(t, [32]byte{}, r.catchupReplay.consensusRecovery.targetHash)

	head := r.catchupReplay.fetchTracker.Find(links[0].hash)
	require.NotNil(t, head)
	r.catchupReplay.recordCatchupTarget(links[len(links)-1].seq+1, [32]byte{0xee}, 8)
	now := time.Now()
	for range 6 {
		now = now.Add(4 * time.Second)
		require.Equal(t, inbound.TimerEscalate, head.OnTimer(now))
	}
	now = now.Add(4 * time.Second)
	require.Equal(t, inbound.TimerFailed, head.OnTimer(now))
	r.catchupReplay.failInboundAcquisition(head)

	assert.Nil(t, r.catchupReplay.fetchTracker.Find(links[0].hash))
	assert.Zero(t, r.catchupReplay.protectedCatchupInFlight())
}

func TestStandardReplayPipelineStaleFailureKeepsReplacementDrain(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	replacement := &standardReplayEntry{generation: 2, seq: 11, hash: [32]byte{0x11}}
	r.catchupReplay.standardReplay = standardReplayPipeline{
		generation: 2,
		active:     true,
		applying:   true,
		anchorSeq:  10,
		entries:    map[uint32]*standardReplayEntry{replacement.seq: replacement},
	}
	stale := &standardReplayEntry{generation: 1, seq: 10, hash: [32]byte{0x10}}

	r.catchupReplay.acquisitionMu.Lock()
	retired, _, current := r.catchupReplay.discardStandardReplayHeadLocked(stale, stale.generation)
	r.catchupReplay.acquisitionMu.Unlock()

	assert.Empty(t, retired)
	assert.False(t, current)
	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.True(t, r.catchupReplay.standardReplay.applying)
	assert.Same(t, replacement, r.catchupReplay.standardReplay.entries[replacement.seq])
}

func TestStandardReplayPipelineStaleCancellationKeepsReplacement(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	replacement := &standardReplayEntry{generation: 2, seq: 11, hash: [32]byte{0x11}}
	r.catchupReplay.standardReplay = standardReplayPipeline{
		generation: 2,
		active:     true,
		anchorSeq:  10,
		targetSeq:  replacement.seq,
		targetHash: replacement.hash,
		entries:    map[uint32]*standardReplayEntry{replacement.seq: replacement},
	}
	stale := standardReplayIdentity{
		generation: 2,
		active:     true,
		anchorSeq:  9,
		targetSeq:  11,
		targetHash: replacement.hash,
	}

	_, current := r.catchupReplay.cancelStandardReplayPipelineIdentity(stale, "test_stale_identity")

	assert.False(t, current)
	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.Same(t, replacement, r.catchupReplay.standardReplay.entries[replacement.seq])
}

func TestStandardReplayCancellationWaitsBeforeInvalidatingGeneration(t *testing.T) {
	r := newTestRouter(nil, nil, make(chan *peermanagement.InboundMessage))
	r.catchupReplay.standardReplay = standardReplayPipeline{
		generation: 7,
		active:     true,
		entries:    make(map[uint32]*standardReplayEntry),
	}

	r.catchupReplay.replayCommitMu.Lock()
	done := make(chan struct{})
	go func() {
		r.StopAcquisitions()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("cancellation passed an active replay commit")
	case <-time.After(25 * time.Millisecond):
	}
	require.True(t, r.catchupReplay.standardReplay.active)
	require.Equal(t, uint64(7), r.catchupReplay.standardReplay.generation)

	r.catchupReplay.replayCommitMu.Unlock()
	require.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	require.False(t, r.catchupReplay.standardReplay.active)
	require.Equal(t, uint64(8), r.catchupReplay.standardReplay.generation)
}

func TestStandardReplayHandoffKeepsSessionWhenTargetAdvances(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)

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
	r.catchupReplay.engine = r.engine
	armStandardReplayTestPipeline(t, r, a, sender, links[:2])
	generation := r.catchupReplay.standardReplay.generation
	pivotHash := r.catchupReplay.standardReplay.pivotHash
	completeStandardReplayTestLink(t, r, links[0])
	drainStandardReplayTestPipeline(t, r)

	final := r.catchupReplay.fetchTracker.Find(links[1].hash)
	require.NotNil(t, final)
	require.NoError(t, final.GotBase([]message.LedgerNode{
		{NodeData: links[1].response.LedgerHeader},
		{NodeData: []byte{1}},
	}))
	require.True(t, final.IsComplete())
	done := make(chan struct{})
	go func() {
		r.catchupReplay.completeInboundLedger(final)
		close(done)
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("final replay did not reach the handoff barrier")
	}
	trackCatchupPeer(r, 7, links[2].seq, links[2].hash)
	require.NoError(t, a.RequestLedger(consensus.LedgerID(links[2].hash)))
	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.Equal(t, generation, r.catchupReplay.standardReplay.generation)
	assert.Equal(t, pivotHash, r.catchupReplay.standardReplay.pivotHash)
	assert.Equal(t, links[2].seq, r.catchupReplay.standardReplay.targetSeq)
	next := r.catchupReplay.fetchTracker.Find(links[2].hash)
	require.NotNil(t, next)
	assert.True(t, next.TransactionOnly())
	for _, acquisition := range r.catchupReplay.fetchTracker.Active() {
		assert.True(t, acquisition.TransactionOnly())
	}

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("final replay did not leave the handoff barrier")
	}
}

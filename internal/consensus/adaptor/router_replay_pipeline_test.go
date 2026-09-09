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
		r.recordSeqHash(seq, hash, parent.Hash(), true)
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
	il := r.fetchTracker.Find(link.hash)
	require.NotNil(t, il)
	require.True(t, il.TransactionOnly())
	require.NoError(t, il.GotBase([]message.LedgerNode{
		{NodeData: link.response.LedgerHeader},
		{NodeData: []byte{1}},
	}))
	require.True(t, il.IsComplete())
	r.completeInboundLedger(il)
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

func TestStandardReplayPipelineAppliesReadySuccessorsInOrder(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	require.Len(t, sender.legacyCalls(), 3)
	require.NoError(t, a.RequestLedger(consensus.LedgerID(links[len(links)-1].hash)))
	assert.Equal(t, links[0].hash, r.consensusRecovery.stepHash)

	completeStandardReplayTestLink(t, r, links[2])
	completeStandardReplayTestLink(t, r, links[1])
	for _, link := range links {
		stored, _ := svc.GetLedgerByHash(link.hash)
		assert.Nil(t, stored)
	}
	metrics := r.FastSyncMetrics()
	assert.Equal(t, uint32(2), metrics.ReplayPipelineReadyDepth)
	assert.Equal(t, links[0].seq, metrics.ReplayPipelineHeadSeq)
	assert.False(t, r.standardReplay.headBlockedAt.IsZero())

	completeStandardReplayTestLink(t, r, links[0])
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
	require.True(t, r.standardReplay.active)
	require.True(t, r.standardReplay.applying)
	require.Len(t, r.standardReplayDrainWake, 1,
		"a ready replay batch must reschedule through the router loop before continuing")
	require.NotNil(t, r.fetchTracker.Find(links[standardReplayApplyBatch].hash),
		"collector refill must continue while the applier yields")
}

func TestStandardReplayPipelineBoundsAndRefillsWindow(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), standardReplayPipelineWindow+2)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	require.Len(t, sender.legacyCalls(), standardReplayPipelineWindow)
	assert.Nil(t, r.fetchTracker.Find(links[standardReplayPipelineWindow].hash))
	metrics := r.FastSyncMetrics()
	assert.Equal(t, uint32(standardReplayPipelineWindow), metrics.ReplayPipelineDepth)
	assert.Equal(t, uint32(standardReplayPipelineWindow), metrics.ReplayPipelineWindow)

	completeStandardReplayTestLink(t, r, links[0])
	require.Len(t, sender.legacyCalls(), standardReplayPipelineWindow+1)
	require.NotNil(t, r.fetchTracker.Find(links[standardReplayPipelineWindow].hash))
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
	assert.Equal(t, links[0].seq, r.standardReplay.anchorSeq)
	assert.Nil(t, r.fetchTracker.Find(links[0].hash))
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
	armStandardReplayTestPipeline(t, r, a, sender, links)

	assert.Empty(t, sender.legacyCalls())
	assert.False(t, r.standardReplay.active)
	assert.Equal(t, [32]byte{}, r.consensusRecovery.targetHash)
	assert.Equal(t, []consensus.LedgerID{consensus.LedgerID(links[2].hash)}, engine.getLedgers())
}

func TestStandardReplayPipelineAcquiresRecoveryHashAtBuildingSequence(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	r.engine = &mockEngine{buildingSeq: links[0].seq}
	armStandardReplayTestPipeline(t, r, a, sender, links)

	require.Len(t, sender.legacyCalls(), len(links))
	assert.True(t, r.standardReplay.active)
	assert.Equal(t, links[0].hash, r.consensusRecovery.stepHash)
}

func TestStandardReplayPipelineCancelsSupersededFork(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	oldLinks := buildStandardReplayTestChain(t, r, closed, 3)
	armStandardReplayTestPipeline(t, r, a, sender, oldLinks)
	stale := r.fetchTracker.Find(oldLinks[0].hash)
	require.NotNil(t, stale)

	newLinks := make([]standardReplayTestLink, 0, 3)
	parent := closed
	for i := 1; i <= 3; i++ {
		link := buildAlternativeReplaySuccessor(t, parent, time.Duration(i)*time.Minute)
		r.recordSeqHash(link.seq, link.hash, parent.Hash(), true)
		newLinks = append(newLinks, link)
		parent = link.ledger
	}
	trackCatchupPeer(r, 7, newLinks[len(newLinks)-1].seq)
	require.NoError(t, a.RequestLedger(consensus.LedgerID(newLinks[len(newLinks)-1].hash)))

	for _, link := range oldLinks {
		assert.Nil(t, r.fetchTracker.Find(link.hash))
	}
	for _, link := range newLinks {
		require.NotNil(t, r.fetchTracker.Find(link.hash))
	}
	require.NoError(t, stale.GotBase([]message.LedgerNode{
		{NodeData: oldLinks[0].response.LedgerHeader},
		{NodeData: []byte{1}},
	}))
	r.completeInboundLedger(stale)
	stored, _ := svc.GetLedgerByHash(oldLinks[0].hash)
	assert.Nil(t, stored)
	assert.GreaterOrEqual(t, r.FastSyncMetrics().ReplayPipelineDiscarded, uint64(len(oldLinks)))
}

func TestStandardReplayPipelineLeavesFullStateSlotAvailable(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), standardReplayPipelineWindow)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	fullStateHash := [32]byte{0xfa, 0x57}
	r.acquisitionMu.Lock()
	require.True(t, r.canAdmitCatchupLocked(fullStateHash, maxConcurrentCatchup))
	r.startLedgerAcquisitionLegacyLocked(links[len(links)-1].seq+1, fullStateHash, 7)
	r.acquisitionMu.Unlock()
	fullState := r.fetchTracker.Find(fullStateHash)
	require.NotNil(t, fullState)
	assert.False(t, fullState.TransactionOnly())
}

func TestStandardReplayPipelineDoesNotClaimFullStateAcquisition(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	sender.mu.Lock()
	sender.peerSupportsReplay = false
	sender.mu.Unlock()
	trackCatchupPeer(r, 7, links[len(links)-1].seq)

	r.acquisitionMu.Lock()
	r.startLedgerAcquisitionLegacyLocked(links[0].seq, links[0].hash, 7)
	r.acquisitionMu.Unlock()
	require.NoError(t, a.RequestLedger(consensus.LedgerID(links[len(links)-1].hash)))

	head := r.fetchTracker.Find(links[0].hash)
	require.NotNil(t, head)
	assert.False(t, head.TransactionOnly())
	assert.False(t, r.standardReplay.active)
	for _, link := range links[1:] {
		assert.Nil(t, r.fetchTracker.Find(link.hash))
	}
}

func TestStandardReplayPipelineFallsBackWhenHeadFails(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	head := r.fetchTracker.Find(links[0].hash)
	require.NotNil(t, head)
	now := time.Now()
	for range 6 {
		now = now.Add(4 * time.Second)
		require.Equal(t, inbound.TimerEscalate, head.OnTimer(now))
		r.escalateAcquisition(head, now)
	}
	now = now.Add(4 * time.Second)
	require.Equal(t, inbound.TimerFailed, head.OnTimer(now))
	r.failInboundAcquisition(head)

	fallback := r.fetchTracker.Find(links[0].hash)
	require.NotNil(t, fallback)
	assert.False(t, fallback.TransactionOnly())
	for _, link := range links[1:] {
		assert.Nil(t, r.fetchTracker.Find(link.hash))
	}
	metrics := r.FastSyncMetrics()
	assert.Equal(t, uint64(1), metrics.ReplayPipelineFallbacks)
	assert.Equal(t, uint64(7), metrics.ReplayPipelineRetried)
	assert.GreaterOrEqual(t, metrics.ReplayPipelineDiscarded, uint64(len(links)))
}

func TestStandardReplayPipelineDefersFailedEntryUntilFrozenPivotReady(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	r.acquisitionMu.Lock()
	r.standardReplay.pivotReady = false
	r.standardReplay.applying = false
	generation := r.standardReplay.generation
	pivotSeq := r.standardReplay.anchorSeq
	r.acquisitionMu.Unlock()

	// The first successor may be ready while the full-state pivot is still
	// being verified. A failure farther ahead must not wake the drain yet.
	completeStandardReplayTestLink(t, r, links[0])
	failed := r.fetchTracker.Find(links[2].hash)
	require.NotNil(t, failed)
	r.failInboundAcquisition(failed)

	r.acquisitionMu.Lock()
	require.True(t, r.standardReplay.active)
	assert.Equal(t, generation, r.standardReplay.generation)
	assert.Equal(t, pivotSeq, r.standardReplay.anchorSeq)
	assert.False(t, r.standardReplay.applying)
	assert.False(t, r.standardReplay.entries[links[0].seq].readyAt.IsZero())
	assert.True(t, r.standardReplay.entries[links[2].seq].failed)
	r.acquisitionMu.Unlock()

	assert.Zero(t, r.FastSyncMetrics().ReplayPipelineApplied)
	assert.Zero(t, r.FastSyncMetrics().ReplayPipelineFallbacks)
}

func TestStandardReplayPipelineFallsBackWhenPersistenceFails(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	head := r.fetchTracker.Find(links[0].hash)
	require.NotNil(t, head)
	r.handleAcquisitionWorkResult(acquisitionWorkResult{
		ledger: head, complete: true, persistenceErr: errors.New("persistence failed"),
	})

	fallback := r.fetchTracker.Find(links[0].hash)
	require.NotNil(t, fallback)
	assert.False(t, fallback.TransactionOnly())
	for _, link := range links[1:] {
		assert.Nil(t, r.fetchTracker.Find(link.hash))
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

	head := r.fetchTracker.Find(links[0].hash)
	require.NotNil(t, head)
	r.handleAcquisitionWorkResult(acquisitionWorkResult{
		ledger: head, remove: true, haveSnapshot: true, snapshot: head.Snapshot(),
		err: errors.New("invalid SHAMap node"),
	})

	fallback := r.fetchTracker.Find(links[0].hash)
	require.NotNil(t, fallback)
	assert.False(t, fallback.TransactionOnly())
	for _, link := range links[1:] {
		assert.Nil(t, r.fetchTracker.Find(link.hash))
	}
	assert.Equal(t, uint64(1), r.FastSyncMetrics().ReplayPipelineFallbacks)
}

func TestStandardReplayPipelineFallbackRespectsProtectedLimit(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	r.acquisitionMu.Lock()
	for i := range maxConcurrentCatchup {
		hash := [32]byte{0xf0, byte(i + 1)}
		r.startLedgerAcquisitionLegacyLocked(links[len(links)-1].seq+uint32(i)+1, hash, 7)
	}
	r.acquisitionMu.Unlock()
	require.Equal(t, maxConcurrentCatchup, r.protectedCatchupInFlight())

	head := r.fetchTracker.Find(links[0].hash)
	require.NotNil(t, head)
	now := time.Now()
	for range 6 {
		now = now.Add(4 * time.Second)
		require.Equal(t, inbound.TimerEscalate, head.OnTimer(now))
	}
	now = now.Add(4 * time.Second)
	require.Equal(t, inbound.TimerFailed, head.OnTimer(now))
	r.failInboundAcquisition(head)

	assert.Nil(t, r.fetchTracker.Find(links[0].hash))
	assert.Equal(t, maxConcurrentCatchup, r.protectedCatchupInFlight())
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
	r.recordCatchupTarget(links[len(links)-1].seq, links[len(links)-1].hash, 7)
	r.armCatchupTowardTarget()
	require.True(t, r.standardReplay.active)
	require.Equal(t, [32]byte{}, r.consensusRecovery.targetHash)

	head := r.fetchTracker.Find(links[0].hash)
	require.NotNil(t, head)
	r.recordCatchupTarget(links[len(links)-1].seq+1, [32]byte{0xee}, 8)
	now := time.Now()
	for range 6 {
		now = now.Add(4 * time.Second)
		require.Equal(t, inbound.TimerEscalate, head.OnTimer(now))
	}
	now = now.Add(4 * time.Second)
	require.Equal(t, inbound.TimerFailed, head.OnTimer(now))
	r.failInboundAcquisition(head)

	assert.Nil(t, r.fetchTracker.Find(links[0].hash))
	assert.Zero(t, r.protectedCatchupInFlight())
}

func TestStandardReplayPipelineStaleFailureKeepsReplacementDrain(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	replacement := &standardReplayEntry{generation: 2, seq: 11, hash: [32]byte{0x11}}
	r.standardReplay = standardReplayPipeline{
		generation: 2,
		active:     true,
		applying:   true,
		anchorSeq:  10,
		entries:    map[uint32]*standardReplayEntry{replacement.seq: replacement},
	}
	stale := &standardReplayEntry{generation: 1, seq: 10, hash: [32]byte{0x10}}

	r.acquisitionMu.Lock()
	retired, _, current := r.discardStandardReplayHeadLocked(stale, stale.generation)
	r.acquisitionMu.Unlock()

	assert.Empty(t, retired)
	assert.False(t, current)
	assert.True(t, r.standardReplay.active)
	assert.True(t, r.standardReplay.applying)
	assert.Same(t, replacement, r.standardReplay.entries[replacement.seq])
}

func TestStandardReplayPipelineStaleCancellationKeepsReplacement(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	replacement := &standardReplayEntry{generation: 2, seq: 11, hash: [32]byte{0x11}}
	r.standardReplay = standardReplayPipeline{
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

	_, current := r.cancelStandardReplayPipelineIdentity(stale)

	assert.False(t, current)
	assert.True(t, r.standardReplay.active)
	assert.Same(t, replacement, r.standardReplay.entries[replacement.seq])
}

func TestStandardReplayCancellationWaitsBeforeInvalidatingGeneration(t *testing.T) {
	r := newTestRouter(nil, nil, make(chan *peermanagement.InboundMessage))
	r.standardReplay = standardReplayPipeline{
		generation: 7,
		active:     true,
		entries:    make(map[uint32]*standardReplayEntry),
	}

	r.replayCommitMu.Lock()
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
	require.True(t, r.standardReplay.active)
	require.Equal(t, uint64(7), r.standardReplay.generation)

	r.replayCommitMu.Unlock()
	require.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	require.False(t, r.standardReplay.active)
	require.Equal(t, uint64(8), r.standardReplay.generation)
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
	armStandardReplayTestPipeline(t, r, a, sender, links[:2])
	generation := r.standardReplay.generation
	pivotHash := r.standardReplay.pivotHash
	completeStandardReplayTestLink(t, r, links[0])

	final := r.fetchTracker.Find(links[1].hash)
	require.NotNil(t, final)
	require.NoError(t, final.GotBase([]message.LedgerNode{
		{NodeData: links[1].response.LedgerHeader},
		{NodeData: []byte{1}},
	}))
	require.True(t, final.IsComplete())
	done := make(chan struct{})
	go func() {
		r.completeInboundLedger(final)
		close(done)
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("final replay did not reach the handoff barrier")
	}
	trackCatchupPeer(r, 7, links[2].seq, links[2].hash)
	require.NoError(t, a.RequestLedger(consensus.LedgerID(links[2].hash)))
	assert.True(t, r.standardReplay.active)
	assert.Equal(t, generation, r.standardReplay.generation)
	assert.Equal(t, pivotHash, r.standardReplay.pivotHash)
	assert.Equal(t, links[2].seq, r.standardReplay.targetSeq)
	next := r.fetchTracker.Find(links[2].hash)
	require.NotNil(t, next)
	assert.True(t, next.TransactionOnly())
	for _, acquisition := range r.fetchTracker.Active() {
		assert.True(t, acquisition.TransactionOnly())
	}

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("final replay did not leave the handoff barrier")
	}
}

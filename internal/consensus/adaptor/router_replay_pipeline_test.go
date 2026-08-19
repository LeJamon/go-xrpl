package adaptor

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
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
	trackCatchupPeer(r, 7, links[len(links)-1].seq)
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

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

func TestStandardReplayReplacesFullStateSuccessor(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	old, created := r.fetchTracker.GetOrCreate(links[0].hash, func() *inbound.Ledger {
		return inbound.New(links[0].hash, links[0].seq, 7, r.logger, r.acquisitionOpts()...)
	})
	require.True(t, created)
	require.False(t, old.TransactionOnly())
	armStandardReplayTestPipeline(t, r, a, sender, links)
	replacement := r.fetchTracker.Find(links[0].hash)
	require.NotNil(t, replacement)
	require.NotSame(t, old, replacement)
	require.True(t, replacement.TransactionOnly())
	// A late completion/removal from the retired walker cannot erase replay.
	require.False(t, r.fetchTracker.DiscardExpected(old))
	for _, link := range links {
		completeStandardReplayTestLink(t, r, link)
	}
	require.Eventually(t, func() bool {
		return r.replayPipelineApplied.Load() == 3
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

func TestStandardReplayPipelineReplacesRedundantFullStateAcquisition(t *testing.T) {
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
	assert.True(t, head.TransactionOnly())
	assert.True(t, r.standardReplay.active)
	for _, link := range links[1:] {
		require.NotNil(t, r.fetchTracker.Find(link.hash))
		assert.True(t, r.fetchTracker.Find(link.hash).TransactionOnly())
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
	for range 3 {
		r.ensureCatchupAcquisition(links[2].seq, links[2].hash, 7)
		require.Same(t, fallback, r.fetchTracker.Find(links[0].hash))
		require.False(t, fallback.TransactionOnly())
	}
	for range 6 {
		now = now.Add(4 * time.Second)
		require.Equal(t, inbound.TimerEscalate, fallback.OnTimer(now))
		r.escalateAcquisition(fallback, now)
	}
	now = now.Add(4 * time.Second)
	require.Equal(t, inbound.TimerFailed, fallback.OnTimer(now))
	r.failInboundAcquisition(fallback)
	r.catchupMu.Lock()
	r.catchupFailures[links[0].hash] = time.Now().Add(-time.Second)
	r.catchupMu.Unlock()
	sender.mu.Lock()
	sender.peerSupportsReplay = true
	sender.mu.Unlock()
	r.armConsensusCatchup()
	retried := r.fetchTracker.Find(links[0].hash)
	require.NotNil(t, retried)
	require.NotSame(t, fallback, retried)
	require.False(t, retried.TransactionOnly())
	require.Empty(t, sender.replayCalls())
}

func TestReplayPreservesMismatchFallbackUntilStored(t *testing.T) {
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
			r.recordSeqHash(first.seq, first.hash, parent.Hash(), true)
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
			fallback := r.fetchTracker.Find(first.hash)
			require.NotNil(t, fallback)
			require.False(t, fallback.TransactionOnly())
			require.Equal(t, pipelineFallbacks, r.FastSyncMetrics().ReplayPipelineFallbacks)
			for range 3 {
				r.ensureCatchupAcquisition(links[2].seq, links[2].hash, 7)
				require.Same(t, fallback, r.fetchTracker.Find(first.hash))
				require.False(t, fallback.TransactionOnly())
			}

			root, err := state.SerializeRoot()
			require.NoError(t, err)
			require.NoError(t, fallback.GotBase([]message.LedgerNode{
				{NodeData: first.response.LedgerHeader}, {NodeData: root},
			}))
			nodes, err := state.WalkWireNodes()
			require.NoError(t, err)
			for _, node := range nodes {
				require.NoError(t, fallback.GotStateNodes([]message.LedgerNode{{NodeID: node.NodeID, NodeData: node.Data}}))
			}
			fallback.CollectMissingRequest(false)
			require.True(t, fallback.IsComplete())
			r.completeInboundLedger(fallback)
			for _, link := range links[1:] {
				completeStandardReplayTestLink(t, r, link)
			}
			stored, err := svc.GetLedgerByHash(links[2].hash)
			require.NoError(t, err)
			require.NotNil(t, stored)
			require.Equal(t, pipelineFallbacks, r.FastSyncMetrics().ReplayPipelineFallbacks)
			require.Equal(t, uint64(2), r.FastSyncMetrics().ReplayPipelineApplied)
		})
	}
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
	identity := r.standardReplayIdentityLocked()
	r.acquisitionMu.Unlock()

	assert.Zero(t, r.FastSyncMetrics().ReplayPipelineApplied)
	assert.Zero(t, r.FastSyncMetrics().ReplayPipelineFallbacks)
	_, canceled := r.cancelStandardReplayPipelineIdentity(identity, "test_retarget")
	require.True(t, canceled)
	r.ensureCatchupAcquisition(links[2].seq, links[2].hash, 7)
	fallback := r.fetchTracker.Find(links[2].hash)
	require.NotNil(t, fallback)
	require.False(t, fallback.TransactionOnly())
	for _, link := range links[:2] {
		completeStandardReplayTestLink(t, r, link)
	}
	require.Same(t, fallback, r.fetchTracker.Find(links[2].hash))
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

	_, current := r.cancelStandardReplayPipelineIdentity(stale, "test_stale_identity")

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

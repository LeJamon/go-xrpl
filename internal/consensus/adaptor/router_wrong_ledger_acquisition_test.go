package adaptor

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdaptorRequestLedgerTracksExactConsensusTarget(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	target := [32]byte{0xA1}
	targetSeq := svc.GetClosedLedgerIndex() + 1
	trackCatchupPeer(r, 7, targetSeq)

	require.NoError(t, a.RequestLedger(consensus.LedgerID(target)))
	il := r.catchupReplay.fetchTracker.Find(target)
	require.NotNil(t, il)
	assert.Equal(t, uint32(0), il.Seq())
	assert.Equal(t, inbound.ReasonConsensus, il.Reason())
	require.Equal(t, []legacyBaseCall{{peerID: 7, hash: target, seq: 0}}, sender.legacyCalls())

	require.NoError(t, a.RequestLedger(consensus.LedgerID(target)))
	assert.Len(t, sender.legacyCalls(), 1)
	assert.Equal(t, 1, r.catchupReplay.catchupInFlight())

	r.handleMessage(&peermanagement.InboundMessage{
		PeerID: 7,
		Type:   message.TypeLedgerData,
		Payload: encodePayload(t, &message.LedgerData{
			LedgerHash: target[:],
			LedgerSeq:  targetSeq,
			InfoType:   message.LedgerInfoBase,
			Nodes:      []message.LedgerNode{{NodeData: []byte{1, 2, 3}}},
		}),
	})
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(target), "matching reply must be consumed by the tracked acquisition")
}

func TestHeldConsensusTargetRequiresEngineAcceptance(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
	r.engine = engine
	r.catchupReplay.engine = r.engine
	target := svc.GetClosedLedger().Hash()
	r.catchupReplay.consensusRecovery.targetHash = target

	require.True(t, r.catchupReplay.armPendingConsensusLedger())
	assert.Equal(t, []consensus.LedgerID{consensus.LedgerID(target)}, engine.getLedgers())
	assert.Equal(t, consensusRecovery{
		anchorHash: target,
		anchorSeq:  svc.GetClosedLedgerIndex(),
	}, r.catchupReplay.consensusRecovery)
}

func TestAdaptorRequestLedgerStartsExactTargetAlongsideActiveTraversal(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	oldHash := [32]byte{0xB1}
	exactHash := [32]byte{0xB2}
	targetSeq := svc.GetClosedLedgerIndex() + 100
	trackCatchupPeer(r, 7, targetSeq)
	active := inbound.New(oldHash, targetSeq-1, 7, serveTestLogger())
	r.catchupReplay.fetchTracker.Track(active)

	require.NoError(t, a.RequestLedger(consensus.LedgerID(exactHash)))

	require.NotNil(t, r.catchupReplay.fetchTracker.Find(oldHash))
	assert.NotNil(t, r.catchupReplay.fetchTracker.Find(exactHash))
	assert.Equal(t, exactHash, r.catchupReplay.consensusRecovery.targetHash)
	assert.Equal(t, 2, r.catchupReplay.catchupInFlight())
	require.Len(t, sender.legacyCalls(), 1)
	assert.Equal(t, exactHash, sender.legacyCalls()[0].hash)
}

func TestConcurrentSpeculativeAdmissionHardCap(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 100
	trackCatchupPeer(r, 7, targetSeq)

	const contenders = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			hash := [32]byte{byte(i + 1), 0xA5}
			r.catchupReplay.startLedgerAcquisition(targetSeq, hash, 7)
		}(i)
	}
	close(start)
	wg.Wait()

	assert.Equal(t, maxConcurrentSpeculativeCatchup, r.catchupReplay.catchupInFlight())
	assert.Len(t, sender.legacyCalls(), maxConcurrentSpeculativeCatchup)
}

func TestExactConsensusAdmissionUsesReservedSlot(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 100
	trackCatchupPeer(r, 7, targetSeq)

	for i := 0; i < maxConcurrentSpeculativeCatchup; i++ {
		hash := [32]byte{byte(0xC0 + i)}
		require.True(t, r.catchupReplay.startLedgerAcquisition(targetSeq, hash, 7))
	}
	exactHash := [32]byte{0xCF}
	require.NoError(t, a.RequestLedger(consensus.LedgerID(exactHash)))

	assert.Equal(t, maxConcurrentCatchup, r.catchupReplay.catchupInFlight())
	assert.NotNil(t, r.catchupReplay.fetchTracker.Find(exactHash))
	assert.Equal(t, exactHash, r.catchupReplay.consensusRecovery.stepHash)
	require.Len(t, sender.legacyCalls(), maxConcurrentCatchup)

	extraHash := [32]byte{0xD0}
	assert.False(t, r.catchupReplay.startLedgerAcquisition(targetSeq, extraHash, 7))
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(extraHash))
	assert.Equal(t, maxConcurrentCatchup, r.catchupReplay.catchupInFlight())
}

func TestAdaptorRequestLedgerKeepsActiveStepAndQueuesLatestTarget(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	firstHash := [32]byte{0xB3}
	intermediateHash := [32]byte{0xB8}
	latestHash := [32]byte{0xB4}
	queuedHash := [32]byte{0xB9}
	targetSeq := svc.GetClosedLedgerIndex() + 100
	trackCatchupPeer(r, 7, targetSeq)

	require.NoError(t, a.RequestLedger(consensus.LedgerID(firstHash)))
	require.NoError(t, a.RequestLedger(consensus.LedgerID(intermediateHash)))
	require.NoError(t, a.RequestLedger(consensus.LedgerID(latestHash)))
	require.NoError(t, a.RequestLedger(consensus.LedgerID(queuedHash)))
	r.catchupReplay.armConsensusCatchup()

	require.NotNil(t, r.catchupReplay.fetchTracker.Find(firstHash))
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(intermediateHash))
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(latestHash))
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(queuedHash))
	assert.Equal(t, firstHash, r.catchupReplay.consensusRecovery.stepHash)
	assert.Equal(t, queuedHash, r.catchupReplay.consensusRecovery.targetHash)
	require.Len(t, sender.legacyCalls(), 1)
	assert.Equal(t, firstHash, sender.legacyCalls()[0].hash)

	first := r.catchupReplay.fetchTracker.Find(firstHash)
	require.True(t, r.catchupReplay.fetchTracker.DiscardExpected(first))
	notify, rearm := r.catchupReplay.finishConsensusRecoveryStep(targetSeq-3, firstHash)
	assert.False(t, notify)
	require.True(t, rearm)
	r.catchupReplay.armConsensusCatchup()

	require.NotNil(t, r.catchupReplay.fetchTracker.Find(queuedHash))
	assert.Equal(t, queuedHash, r.catchupReplay.consensusRecovery.stepHash)
	assert.Equal(t, queuedHash, r.catchupReplay.consensusRecovery.targetHash)
	require.Len(t, sender.legacyCalls(), 2)
	assert.Equal(t, queuedHash, sender.legacyCalls()[1].hash)
}

func TestConsensusRecoveryHonorsCumulativeTimeoutBudgetAcrossIntermittentStalls(t *testing.T) {
	r, a, sender, _ := makeRouter(t)
	source := newWideWorkSource(t, 4)
	ledger, baseNodes := newWantBaseWorkLedger(t, source, []uint64{7})
	require.NoError(t, ledger.GotBase(baseNodes))

	wire, err := source.WalkWireNodes()
	require.NoError(t, err)
	var ancestors, replies []message.LedgerNode
	for _, node := range wire {
		depth := node.NodeID[32]
		ledgerNode := message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data}
		if depth == 1 || depth == 2 {
			ancestors = append(ancestors, ledgerNode)
		} else if depth == 3 && len(replies) < 7 {
			replies = append(replies, ledgerNode)
		}
	}
	added, err := ledger.GotStateNodesUseful(ancestors)
	require.NoError(t, err)
	require.Equal(t, len(ancestors), added)
	require.Len(t, replies, 7)

	activeHash := ledger.Hash()
	latestHash := [32]byte{0xBA}
	r.catchupReplay.fetchTracker.Track(ledger)
	r.catchupReplay.consensusRecovery = consensusRecovery{
		targetHash: activeHash,
		stepHash:   activeHash,
	}
	require.NoError(t, a.RequestLedger(consensus.LedgerID(latestHash)))
	require.Equal(t, latestHash, r.catchupReplay.consensusRecovery.targetHash)

	now := time.Unix(1_700_000_000, 0)
	ledger.RearmTimer(now)
	require.Equal(t, inbound.TimerRefresh, ledger.OnTimer(now.Add(time.Minute)))
	now = now.Add(time.Minute)

	const quietIntervals = 6 // rippled's kLedgerTimeoutRetriesMax
	for i, reply := range replies[:quietIntervals] {
		now = now.Add(time.Minute)
		require.Equal(t, inbound.TimerEscalate, ledger.OnTimer(now), "stall %d", i+1)
		ledger.RearmTimer(now)

		added, err := ledger.GotStateNodesUseful([]message.LedgerNode{reply})
		require.NoError(t, err)
		require.Equal(t, 1, added)

		now = now.Add(time.Minute)
		require.Equal(t, inbound.TimerRefresh, ledger.OnTimer(now), "progress %d", i+1)
		r.catchupReplay.armConsensusCatchup()
		require.Same(t, ledger, r.catchupReplay.fetchTracker.Find(activeHash))
		assert.Nil(t, r.catchupReplay.fetchTracker.Find(latestHash))
		assert.Equal(t, activeHash, r.catchupReplay.consensusRecovery.stepHash)
	}

	now = now.Add(time.Minute)
	require.Equal(t, inbound.TimerFailed, ledger.OnTimer(now), "quiet interval after cumulative budget")
	assert.Equal(t, quietIntervals+1, ledger.Timeouts())
	assert.Equal(t, inbound.StateFailed, ledger.State())
	assert.Empty(t, sender.legacyCalls())
}

func TestSpeculativeAcquisitionDefersDuringConsensusRecovery(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 100
	trackCatchupPeer(r, 7, targetSeq)

	exactHash := [32]byte{0xD4}
	require.NoError(t, a.RequestLedger(consensus.LedgerID(exactHash)))
	require.NotNil(t, r.catchupReplay.fetchTracker.Find(exactHash))

	speculativeHash := [32]byte{0xD5}
	assert.False(t, r.catchupReplay.startLedgerAcquisition(targetSeq+1, speculativeHash, 7))
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(speculativeHash))
	require.Len(t, sender.legacyCalls(), 1)

	r.catchupReplay.recordCatchupTarget(targetSeq+1, speculativeHash, 7)
	seq, hash, peer := r.catchupReplay.bestCatchupTarget()
	assert.Equal(t, targetSeq+1, seq)
	assert.Equal(t, speculativeHash, hash)
	assert.Equal(t, uint64(7), peer)
}

func TestSupersededReplayFailureDoesNotRestartStaleTarget(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	trackCatchupPeer(r, 7, parent.Sequence()+10)

	oldStep := [32]byte{0xD1}
	oldTarget := [32]byte{0xD2}
	newTarget := [32]byte{0xD3}
	require.NoError(t, r.catchupReplay.startReplayDeltaAcquisition(parent.Sequence()+1, oldStep, 7, parent))
	r.catchupReplay.consensusRecovery = consensusRecovery{targetHash: oldTarget, stepHash: oldStep}
	require.NoError(t, a.RequestLedger(consensus.LedgerID(newTarget)))

	r.catchupReplay.replayer.Abandon(oldStep)
	r.catchupReplay.fallbackReplayAcquisition(parent.Sequence()+1, oldStep, 7)

	legacy := sender.legacyCalls()
	require.Len(t, legacy, 1)
	assert.Equal(t, newTarget, legacy[0].hash)
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(oldStep))
	assert.Equal(t, newTarget, r.catchupReplay.consensusRecovery.stepHash)
}

func TestConsensusRecoveryAnchorRequiresCurrentTargetAncestry(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	canonical := [32]byte{0xE1}
	child := [32]byte{0xE2}
	target := [32]byte{0xE3}
	fork := [32]byte{0xEF}
	r.catchupReplay.recordSeqHash(10, canonical, [32]byte{0xE0}, true)
	r.catchupReplay.recordSeqHash(11, child, canonical, true)
	r.catchupReplay.recordSeqHash(12, target, child, true)
	r.catchupReplay.consensusRecovery.targetHash = target

	notify, rearm := r.catchupReplay.finishConsensusRecoveryStep(11, fork)
	assert.False(t, notify)
	assert.True(t, rearm)
	assert.Equal(t, [32]byte{}, r.catchupReplay.consensusRecovery.anchorHash)

	notify, rearm = r.catchupReplay.finishConsensusRecoveryStep(10, canonical)
	assert.False(t, notify)
	assert.True(t, rearm)
	assert.Equal(t, canonical, r.catchupReplay.consensusRecovery.anchorHash)
	assert.Equal(t, uint32(10), r.catchupReplay.consensusRecovery.anchorSeq)
}

func TestConsensusRecoveryExactTargetReplacesHigherAnchor(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	higher := [32]byte{0xF1}
	target := [32]byte{0xF2}
	r.catchupReplay.consensusRecovery = consensusRecovery{
		targetHash: target,
		stepHash:   target,
		anchorHash: higher,
		anchorSeq:  200,
	}

	notify, rearm := r.catchupReplay.finishConsensusRecoveryStep(100, target)
	assert.True(t, notify)
	assert.False(t, rearm)
	assert.Equal(t, consensusRecovery{anchorHash: target, anchorSeq: 100}, r.catchupReplay.consensusRecovery)
}

func TestRouterStopAcquisitionsDrainsBothPaths(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	legacyHash := [32]byte{0xF3}
	replayHash := [32]byte{0xF4}
	require.True(t, r.catchupReplay.startLedgerAcquisition(parent.Sequence()+10, legacyHash, 7))
	require.NoError(t, r.catchupReplay.startReplayDeltaAcquisition(parent.Sequence()+1, replayHash, 8, parent))

	legacy, replay := r.StopAcquisitions()
	assert.Equal(t, 1, legacy)
	assert.Equal(t, 1, replay)
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(legacyHash))
	assert.Zero(t, r.catchupReplay.replayer.Count())
	assert.False(t, r.catchupReplay.startLedgerAcquisition(parent.Sequence()+11, [32]byte{0xF5}, 7))
	_, err := r.catchupReplay.replayer.Acquire([32]byte{0xF6}, 8, parent)
	assert.ErrorIs(t, err, inbound.ErrAcquisitionStopped)
	legacy, replay = r.StopAcquisitions()
	assert.Zero(t, legacy)
	assert.Zero(t, replay)
}

func TestPendingConsensusTargetPrecedesRecordedCatchupTarget(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	activeHash := [32]byte{0xB5}
	pendingHash := [32]byte{0xB6}
	recordedHash := [32]byte{0xB7}
	trackCatchupPeer(r, 7, svc.GetClosedLedgerIndex()+100)

	require.NoError(t, a.RequestLedger(consensus.LedgerID(activeHash)))
	require.NoError(t, a.RequestLedger(consensus.LedgerID(pendingHash)))
	r.catchupReplay.recordCatchupTarget(svc.GetClosedLedgerIndex()+100, recordedHash, 7)
	active := r.catchupReplay.fetchTracker.Find(activeHash)
	require.True(t, r.catchupReplay.fetchTracker.DiscardExpected(active))

	r.catchupReplay.armConsensusCatchup()

	require.NotNil(t, r.catchupReplay.fetchTracker.Find(pendingHash))
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(recordedHash))
	require.Len(t, sender.legacyCalls(), 2)
	assert.Equal(t, pendingHash, sender.legacyCalls()[1].hash)
}

func TestHistoryBackfillWaitsForConsensusCatchup(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	historyHash := [32]byte{0xC1}
	catchupHash := [32]byte{0xC2}
	trackCatchupPeer(r, 7, closed+10)
	r.catchupReplay.startHistoryBackfill(closed-1, historyHash, 7, 0)
	r.catchupReplay.recordCatchupTarget(closed+10, catchupHash, 7)

	r.catchupReplay.armHistoryBackfill()
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(historyHash))
	assert.Empty(t, sender.legacyCalls())

	r.catchupReplay.catchupMu.Lock()
	r.catchupReplay.catchup = catchupTarget{}
	r.catchupReplay.catchupMu.Unlock()
	r.catchupReplay.armHistoryBackfill()
	require.NotNil(t, r.catchupReplay.fetchTracker.Find(historyHash))
	assert.Len(t, sender.legacyCalls(), 1)
}

func TestSupersededHistoryCompletionDoesNotOverwriteNewWalk(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	oldHash := [32]byte{0xC3}
	newHash := [32]byte{0xC4}
	r.catchupReplay.startHistoryBackfill(90, oldHash, 7, 10)
	r.catchupReplay.startHistoryBackfill(190, newHash, 8, 20)

	r.catchupReplay.completeHistoryBackfill(90, oldHash, [32]byte{0xC2}, 7)

	r.catchupReplay.historyMu.Lock()
	defer r.catchupReplay.historyMu.Unlock()
	assert.Equal(t, catchupTarget{seq: 190, hash: newHash, peerID: 8}, r.catchupReplay.history)
	assert.Equal(t, uint32(20), r.catchupReplay.historyFloor)
}

func TestBehindPeerCannotPromoteWhileNetworkTargetIsAhead(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	a.SetOperatingMode(consensus.OpModeTracking)
	closed := svc.GetClosedLedgerIndex()
	targetHash := [32]byte{0xC2}
	r.catchupReplay.recordValidationCatchupTarget(closed+10, targetHash, 7, catchupSourceQuorum)

	lowHash := [32]byte{0xC3}
	r.catchupReplay.checkBehind(1, lowHash, 8)

	assert.Equal(t, consensus.OpModeTracking, a.GetOperatingMode())
	assert.Empty(t, sender.legacyCalls())
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(lowHash))
	seq, hash, peerID := r.catchupReplay.bestCatchupTarget()
	assert.Equal(t, closed+10, seq)
	assert.Equal(t, targetHash, hash)
	assert.Equal(t, uint64(7), peerID)
}

func TestOutlierPeerSequenceCannotBlockPromotion(t *testing.T) {
	r, a, _, svc := makeRouter(t)
	a.SetOperatingMode(consensus.OpModeTracking)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	r.catchupReplay.peersMu.Lock()
	r.catchupReplay.peerStates[7] = &peerLedgerState{LedgerSeq: closed.Sequence(), LedgerHash: closed.Hash()}
	r.catchupReplay.peerStates[9] = &peerLedgerState{LedgerSeq: 106_000_000, LedgerHash: [32]byte{0xee}}
	r.catchupReplay.peersMu.Unlock()

	r.catchupReplay.checkBehind(closed.Sequence(), closed.Hash(), 7)

	assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode())
}

func TestAheadByMoreThanDoesNotWrap(t *testing.T) {
	assert.False(t, aheadByMoreThan(1, 19_263_641, 1))
	assert.False(t, aheadByMoreThan(19_263_641, 19_263_641, 1))
	assert.False(t, aheadByMoreThan(19_263_642, 19_263_641, 1))
	assert.True(t, aheadByMoreThan(19_263_643, 19_263_641, 1))
	assert.False(t, aheadByMoreThan(0, ^uint32(0), 1))
}

type ledgerRequestRecorder struct {
	noopSender
	mu    sync.Mutex
	calls []consensus.LedgerID
}

func (s *ledgerRequestRecorder) RequestLedger(id consensus.LedgerID) error {
	s.mu.Lock()
	s.calls = append(s.calls, id)
	s.mu.Unlock()
	return nil
}

func TestAdaptorRequestLedgerFallsBackWithoutRouter(t *testing.T) {
	sender := &ledgerRequestRecorder{}
	a := New(Config{LedgerService: newTestLedgerService(t), Sender: sender})
	target := consensus.LedgerID{0xD1}

	require.NoError(t, a.RequestLedger(target))
	require.NoError(t, a.RequestLedger(target))

	sender.mu.Lock()
	defer sender.mu.Unlock()
	assert.Equal(t, []consensus.LedgerID{target}, sender.calls)
}

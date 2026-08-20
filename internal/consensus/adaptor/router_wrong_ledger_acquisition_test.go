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
	il := r.fetchTracker.Find(target)
	require.NotNil(t, il)
	assert.Equal(t, uint32(0), il.Seq())
	assert.Equal(t, inbound.ReasonConsensus, il.Reason())
	require.Equal(t, []legacyBaseCall{{peerID: 7, hash: target, seq: 0}}, sender.legacyCalls())

	require.NoError(t, a.RequestLedger(consensus.LedgerID(target)))
	assert.Len(t, sender.legacyCalls(), 1)
	assert.Equal(t, 1, r.catchupInFlight())

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
	assert.Nil(t, r.fetchTracker.Find(target), "matching reply must be consumed by the tracked acquisition")
}

func TestHeldConsensusTargetRequiresEngineAcceptance(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
	r.engine = engine
	target := svc.GetClosedLedger().Hash()
	r.consensusRecovery.targetHash = target

	require.True(t, r.armPendingConsensusLedger())
	assert.Equal(t, []consensus.LedgerID{consensus.LedgerID(target)}, engine.getLedgers())
	assert.Equal(t, consensusRecovery{
		anchorHash: target,
		anchorSeq:  svc.GetClosedLedgerIndex(),
	}, r.consensusRecovery)
}

func TestAdaptorRequestLedgerStartsExactTargetAlongsideActiveTraversal(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	oldHash := [32]byte{0xB1}
	exactHash := [32]byte{0xB2}
	targetSeq := svc.GetClosedLedgerIndex() + 100
	trackCatchupPeer(r, 7, targetSeq)
	active := inbound.New(oldHash, targetSeq-1, 7, serveTestLogger())
	r.fetchTracker.Track(active)

	require.NoError(t, a.RequestLedger(consensus.LedgerID(exactHash)))

	require.NotNil(t, r.fetchTracker.Find(oldHash))
	assert.NotNil(t, r.fetchTracker.Find(exactHash))
	assert.Equal(t, exactHash, r.consensusRecovery.targetHash)
	assert.Equal(t, 2, r.catchupInFlight())
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
			r.startLedgerAcquisition(targetSeq, hash, 7)
		}(i)
	}
	close(start)
	wg.Wait()

	assert.Equal(t, maxConcurrentSpeculativeCatchup, r.catchupInFlight())
	assert.Len(t, sender.legacyCalls(), maxConcurrentSpeculativeCatchup)
}

func TestExactConsensusAdmissionUsesReservedSlot(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 100
	trackCatchupPeer(r, 7, targetSeq)

	for i := 0; i < maxConcurrentSpeculativeCatchup; i++ {
		hash := [32]byte{byte(0xC0 + i)}
		require.True(t, r.startLedgerAcquisition(targetSeq, hash, 7))
	}
	exactHash := [32]byte{0xCF}
	require.NoError(t, a.RequestLedger(consensus.LedgerID(exactHash)))

	assert.Equal(t, maxConcurrentCatchup, r.catchupInFlight())
	assert.NotNil(t, r.fetchTracker.Find(exactHash))
	assert.Equal(t, exactHash, r.consensusRecovery.stepHash)
	require.Len(t, sender.legacyCalls(), maxConcurrentCatchup)

	extraHash := [32]byte{0xD0}
	assert.False(t, r.startLedgerAcquisition(targetSeq, extraHash, 7))
	assert.Nil(t, r.fetchTracker.Find(extraHash))
	assert.Equal(t, maxConcurrentCatchup, r.catchupInFlight())
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
	r.armConsensusCatchup()

	require.NotNil(t, r.fetchTracker.Find(firstHash))
	assert.Nil(t, r.fetchTracker.Find(intermediateHash))
	assert.Nil(t, r.fetchTracker.Find(latestHash))
	assert.Nil(t, r.fetchTracker.Find(queuedHash))
	assert.Equal(t, firstHash, r.consensusRecovery.stepHash)
	assert.Equal(t, queuedHash, r.consensusRecovery.targetHash)
	require.Len(t, sender.legacyCalls(), 1)
	assert.Equal(t, firstHash, sender.legacyCalls()[0].hash)

	first := r.fetchTracker.Find(firstHash)
	require.True(t, r.fetchTracker.DiscardExpected(first))
	notify, rearm := r.finishConsensusRecoveryStep(targetSeq-3, firstHash)
	assert.False(t, notify)
	require.True(t, rearm)
	r.armConsensusCatchup()

	require.NotNil(t, r.fetchTracker.Find(queuedHash))
	assert.Equal(t, queuedHash, r.consensusRecovery.stepHash)
	assert.Equal(t, queuedHash, r.consensusRecovery.targetHash)
	require.Len(t, sender.legacyCalls(), 2)
	assert.Equal(t, queuedHash, sender.legacyCalls()[1].hash)
}

func TestConsensusRecoveryKeepsProgressingStepAcrossIntermittentStalls(t *testing.T) {
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
	r.fetchTracker.Track(ledger)
	r.consensusRecovery = consensusRecovery{
		targetHash: activeHash,
		stepHash:   activeHash,
	}
	require.NoError(t, a.RequestLedger(consensus.LedgerID(latestHash)))
	require.Equal(t, latestHash, r.consensusRecovery.targetHash)

	now := time.Unix(1_700_000_000, 0)
	ledger.RearmTimer(now)
	require.Equal(t, inbound.TimerRefresh, ledger.OnTimer(now.Add(time.Minute)))
	now = now.Add(time.Minute)

	for i, reply := range replies {
		now = now.Add(time.Minute)
		require.Equal(t, inbound.TimerEscalate, ledger.OnTimer(now), "stall %d", i+1)
		ledger.RearmTimer(now)

		added, err := ledger.GotStateNodesUseful([]message.LedgerNode{reply})
		require.NoError(t, err)
		require.Equal(t, 1, added)

		now = now.Add(time.Minute)
		require.Equal(t, inbound.TimerRefresh, ledger.OnTimer(now), "progress %d", i+1)
		r.armConsensusCatchup()
		require.Same(t, ledger, r.fetchTracker.Find(activeHash))
		assert.Nil(t, r.fetchTracker.Find(latestHash))
		assert.Equal(t, activeHash, r.consensusRecovery.stepHash)
	}

	assert.Equal(t, 7, ledger.Timeouts())
	assert.Equal(t, inbound.StateWantState, ledger.State())
	assert.Empty(t, sender.legacyCalls())
}

func TestSpeculativeAcquisitionDefersDuringConsensusRecovery(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 100
	trackCatchupPeer(r, 7, targetSeq)

	exactHash := [32]byte{0xD4}
	require.NoError(t, a.RequestLedger(consensus.LedgerID(exactHash)))
	require.NotNil(t, r.fetchTracker.Find(exactHash))

	speculativeHash := [32]byte{0xD5}
	assert.False(t, r.startLedgerAcquisition(targetSeq+1, speculativeHash, 7))
	assert.Nil(t, r.fetchTracker.Find(speculativeHash))
	require.Len(t, sender.legacyCalls(), 1)

	r.recordCatchupTarget(targetSeq+1, speculativeHash, 7)
	seq, hash, peer := r.bestCatchupTarget()
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
	require.NoError(t, r.startReplayDeltaAcquisition(parent.Sequence()+1, oldStep, 7, parent))
	r.consensusRecovery = consensusRecovery{targetHash: oldTarget, stepHash: oldStep}
	require.NoError(t, a.RequestLedger(consensus.LedgerID(newTarget)))

	r.replayer.Abandon(oldStep)
	r.fallbackReplayAcquisition(parent.Sequence()+1, oldStep, 7)

	legacy := sender.legacyCalls()
	require.Len(t, legacy, 1)
	assert.Equal(t, newTarget, legacy[0].hash)
	assert.Nil(t, r.fetchTracker.Find(oldStep))
	assert.Equal(t, newTarget, r.consensusRecovery.stepHash)
}

func TestConsensusRecoveryAnchorRequiresCurrentTargetAncestry(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	canonical := [32]byte{0xE1}
	child := [32]byte{0xE2}
	target := [32]byte{0xE3}
	fork := [32]byte{0xEF}
	r.recordSeqHash(10, canonical, [32]byte{0xE0}, true)
	r.recordSeqHash(11, child, canonical, true)
	r.recordSeqHash(12, target, child, true)
	r.consensusRecovery.targetHash = target

	notify, rearm := r.finishConsensusRecoveryStep(11, fork)
	assert.False(t, notify)
	assert.True(t, rearm)
	assert.Equal(t, [32]byte{}, r.consensusRecovery.anchorHash)

	notify, rearm = r.finishConsensusRecoveryStep(10, canonical)
	assert.False(t, notify)
	assert.True(t, rearm)
	assert.Equal(t, canonical, r.consensusRecovery.anchorHash)
	assert.Equal(t, uint32(10), r.consensusRecovery.anchorSeq)
}

func TestConsensusRecoveryExactTargetReplacesHigherAnchor(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	higher := [32]byte{0xF1}
	target := [32]byte{0xF2}
	r.consensusRecovery = consensusRecovery{
		targetHash: target,
		stepHash:   target,
		anchorHash: higher,
		anchorSeq:  200,
	}

	notify, rearm := r.finishConsensusRecoveryStep(100, target)
	assert.True(t, notify)
	assert.False(t, rearm)
	assert.Equal(t, consensusRecovery{anchorHash: target, anchorSeq: 100}, r.consensusRecovery)
}

func TestRouterStopAcquisitionsDrainsBothPaths(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	legacyHash := [32]byte{0xF3}
	replayHash := [32]byte{0xF4}
	require.True(t, r.startLedgerAcquisition(parent.Sequence()+10, legacyHash, 7))
	require.NoError(t, r.startReplayDeltaAcquisition(parent.Sequence()+1, replayHash, 8, parent))

	legacy, replay := r.StopAcquisitions()
	assert.Equal(t, 1, legacy)
	assert.Equal(t, 1, replay)
	assert.Nil(t, r.fetchTracker.Find(legacyHash))
	assert.Zero(t, r.replayer.Count())
	assert.False(t, r.startLedgerAcquisition(parent.Sequence()+11, [32]byte{0xF5}, 7))
	_, err := r.replayer.Acquire([32]byte{0xF6}, 8, parent)
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
	r.recordCatchupTarget(svc.GetClosedLedgerIndex()+100, recordedHash, 7)
	active := r.fetchTracker.Find(activeHash)
	require.True(t, r.fetchTracker.DiscardExpected(active))

	r.armConsensusCatchup()

	require.NotNil(t, r.fetchTracker.Find(pendingHash))
	assert.Nil(t, r.fetchTracker.Find(recordedHash))
	require.Len(t, sender.legacyCalls(), 2)
	assert.Equal(t, pendingHash, sender.legacyCalls()[1].hash)
}

func TestHistoryBackfillWaitsForConsensusCatchup(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	historyHash := [32]byte{0xC1}
	catchupHash := [32]byte{0xC2}
	trackCatchupPeer(r, 7, closed+10)
	r.startHistoryBackfill(closed-1, historyHash, 7, 0)
	r.recordCatchupTarget(closed+10, catchupHash, 7)

	r.armHistoryBackfill()
	assert.Nil(t, r.fetchTracker.Find(historyHash))
	assert.Empty(t, sender.legacyCalls())

	r.catchupMu.Lock()
	r.catchup = catchupTarget{}
	r.catchupMu.Unlock()
	r.armHistoryBackfill()
	require.NotNil(t, r.fetchTracker.Find(historyHash))
	assert.Len(t, sender.legacyCalls(), 1)
}

func TestSupersededHistoryCompletionDoesNotOverwriteNewWalk(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	oldHash := [32]byte{0xC3}
	newHash := [32]byte{0xC4}
	r.startHistoryBackfill(90, oldHash, 7, 10)
	r.startHistoryBackfill(190, newHash, 8, 20)

	r.completeHistoryBackfill(90, oldHash, [32]byte{0xC2}, 7)

	r.historyMu.Lock()
	defer r.historyMu.Unlock()
	assert.Equal(t, catchupTarget{seq: 190, hash: newHash, peerID: 8}, r.history)
	assert.Equal(t, uint32(20), r.historyFloor)
}

func TestBehindPeerCannotPromoteWhileNetworkTargetIsAhead(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	a.SetOperatingMode(consensus.OpModeTracking)
	closed := svc.GetClosedLedgerIndex()
	targetHash := [32]byte{0xC2}
	r.recordValidationCatchupTarget(closed+10, targetHash, 7, catchupSourceQuorum)

	lowHash := [32]byte{0xC3}
	r.checkBehind(1, lowHash, 8)

	assert.Equal(t, consensus.OpModeTracking, a.GetOperatingMode())
	assert.Empty(t, sender.legacyCalls())
	assert.Nil(t, r.fetchTracker.Find(lowHash))
	seq, hash, peerID := r.bestCatchupTarget()
	assert.Equal(t, closed+10, seq)
	assert.Equal(t, targetHash, hash)
	assert.Equal(t, uint64(7), peerID)
}

func TestOutlierPeerSequenceCannotBlockPromotion(t *testing.T) {
	r, a, _, svc := makeRouter(t)
	a.SetOperatingMode(consensus.OpModeTracking)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	r.peersMu.Lock()
	r.peerStates[7] = &peerLedgerState{LedgerSeq: closed.Sequence(), LedgerHash: closed.Hash()}
	r.peerStates[9] = &peerLedgerState{LedgerSeq: 106_000_000, LedgerHash: [32]byte{0xee}}
	r.peersMu.Unlock()

	r.checkBehind(closed.Sequence(), closed.Hash(), 7)

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

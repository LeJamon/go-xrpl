package adaptor

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type issue1677RecheckingHistorian struct {
	*stubHistorian
	calls int
}

func (h *issue1677RecheckingHistorian) RecheckFullyValidated(
	ledgerID consensus.LedgerID,
	seq uint32,
) ([]*consensus.Validation, int, bool) {
	h.calls++
	return []*consensus.Validation{{
		LedgerID:  ledgerID,
		LedgerSeq: seq,
		SignTime:  time.Now(),
		Full:      true,
	}}, 1, true
}

type issue1677CanAcceptResult struct {
	acceptable bool
	err        error
}

type issue1677EngineProbe struct {
	*rcl.Engine
	entered chan consensus.LedgerID
	result  chan issue1677CanAcceptResult
}

func (p *issue1677EngineProbe) CanAcceptLedger(id consensus.LedgerID) (bool, error) {
	p.entered <- id
	acceptable, err := p.Engine.CanAcceptLedger(id)
	p.result <- issue1677CanAcceptResult{acceptable: acceptable, err: err}
	return acceptable, err
}

func newIssue1663BackedAcquisition(t *testing.T, seq uint32, peerID uint64) *inbound.Ledger {
	t.Helper()
	source := newWideWorkSource(t, 16)
	rootHash, err := source.Hash()
	require.NoError(t, err)
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)
	hdr := header.LedgerHeader{LedgerIndex: seq, AccountHash: rootHash}
	headerData := header.AddRaw(hdr, false)
	ledgerHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)

	family := backend.NewMemory()
	pack, err := source.WalkFetchPackNodes(1 << 20)
	require.NoError(t, err)
	entries := make([]shamap.FlushEntry, 0, len(pack))
	for _, node := range pack {
		entries = append(entries, shamap.FlushEntry{Hash: node.Hash, Data: node.Data})
	}

	il := inbound.New(ledgerHash, seq, peerID, serveTestLogger(), inbound.WithFamily(family))
	require.NoError(t, il.GotBase([]message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}}))
	require.NoError(t, family.StoreBatch(t.Context(), entries))
	return il
}

func TestRouter_Issue1663CatchupCascadeRecoversToFull(t *testing.T) {
	svc := newTestLedgerService(t)
	a, sender := newRecordingAdaptor(t, svc)
	engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
	r := newTestRouter(engine, a, nil)
	r.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	engine.switchHook = func(id consensus.LedgerID) {
		selected, err := a.GetLedger(id)
		require.NoError(t, err)
		require.NoError(t, a.OnLedgerSwitched(selected))
	}
	a.SetOperatingMode(consensus.OpModeTracking)

	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	nextHash := [32]byte{0x31}
	next2Hash := [32]byte{0x32}
	r.handleStatusChange(statusChangeMessageWithParent(
		t, 7, closed.Sequence()+1, nextHash, closed.Hash(), true,
	))
	r.handleStatusChange(statusChangeMessageWithParent(
		t, 7, closed.Sequence()+2, next2Hash, nextHash, true,
	))

	const outlierBase = uint32(106_400_000)
	for offset := uint32(0); offset < seqHashRetain+10; offset++ {
		r.handleStatusChange(statusChangeMessageWithParent(
			t, 9, outlierBase+offset, [32]byte{0xee}, [32]byte{0xed}, true,
		))
	}
	entry, ok := r.lookupSeqHash(closed.Sequence() + 1)
	require.True(t, ok)
	require.Equal(t, nextHash, entry.hash)
	require.Equal(t, closed.Hash(), entry.parentHash)
	r.fetchTracker.Remove(nextHash, false)
	r.fetchTracker.Remove(next2Hash, false)
	r.replayer.Abandon(nextHash)
	r.replayer.Abandon(next2Hash)

	stale1 := newIssue1663BackedAcquisition(t, closed.Sequence()+1, 7)
	stale2 := newIssue1663BackedAcquisition(t, closed.Sequence()+2, 7)
	r.fetchTracker.Track(stale1)
	r.fetchTracker.Track(stale2)
	require.Equal(t, maxConcurrentSpeculativeCatchup, r.protectedCatchupInFlight())

	base := time.Unix(1_700_000_000, 0)
	stale1.RearmTimer(base)
	require.Equal(t, inbound.TimerRefresh, stale1.OnTimer(base.Add(3900*time.Millisecond)))
	lane := newAcquisitionWorkLane(1)
	lane.process = func(ctx context.Context, ledger *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
		return processAcquisitionWorkWithBudget(ctx, ledger, events, 1)
	}
	ctx, cancel := context.WithCancel(t.Context())
	lane.start(ctx)
	defer func() {
		cancel()
		lane.stop()
	}()
	r.acquisitionWork = lane

	now := base.Add(3900 * time.Millisecond)
	for range 2 {
		require.True(t, lane.submit(stale1, acquisitionWorkEvent{
			kind:  acquisitionWorkLocal,
			fetch: func([32]byte) ([]byte, bool) { return nil, false },
		}))
		yielded := <-lane.results()
		require.True(t, yielded.yielded)

		now = now.Add(3900 * time.Millisecond)
		require.Equal(t, inbound.TimerRefresh, stale1.OnTimer(now))
		now = now.Add(3900 * time.Millisecond)
		require.True(t, lane.submit(stale1, acquisitionWorkEvent{
			kind: acquisitionWorkTimerCheck,
			at:   now,
		}))
		close(yielded.ack)

		escalated := <-lane.results()
		require.True(t, escalated.timerEscalate)
		require.False(t, escalated.yielded)
		close(escalated.ack)
		require.Eventually(t, func() bool { return !lane.has(stale1) }, time.Second, time.Millisecond)
	}
	now = now.Add(3900 * time.Millisecond)
	require.Equal(t, inbound.TimerEscalate, stale1.OnTimer(now))
	require.Equal(t, 2, stale1.ConsecutiveTimeouts())

	stale2.RearmTimer(base)
	require.Equal(t, inbound.TimerRefresh, stale2.OnTimer(base.Add(3900*time.Millisecond)))
	require.Equal(t, inbound.TimerEscalate, stale2.OnTimer(base.Add(7800*time.Millisecond)))
	require.Equal(t, inbound.TimerEscalate, stale2.OnTimer(base.Add(11700*time.Millisecond)))
	require.Equal(t, 2, stale2.ConsecutiveTimeouts())

	rootHash, rootData, wire := buildSelfHealSourceState(t)
	targetSeq := closed.Sequence() + maxForwardDeltaGap + 2
	hdr := header.LedgerHeader{
		LedgerIndex: targetSeq,
		ParentHash:  [32]byte{0x41},
		AccountHash: rootHash,
		CloseTime:   time.Unix(1_700_000_100, 0),
	}
	headerData := header.AddRaw(hdr, false)
	targetHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	r.handleStatusChange(statusChangeMessage(t, 8, targetSeq, targetHash))

	validationTracker := rcl.NewValidationTracker(2)
	trusted := []consensus.NodeID{{1}, {2}}
	validationTracker.SetNow(func() time.Time { return hdr.CloseTime })
	validationTracker.SetTrustedAndQuorum(trusted, 2)
	a.SetValidationHistorian(validationTracker)
	for _, nodeID := range trusted {
		require.True(t, validationTracker.Add(&consensus.Validation{
			LedgerID:  consensus.LedgerID(targetHash),
			LedgerSeq: targetSeq,
			NodeID:    nodeID,
			SignTime:  hdr.CloseTime,
			SeenTime:  hdr.CloseTime,
			Full:      true,
		}))
	}

	r.onLedgerFullyValidated(targetSeq, targetHash)
	require.Nil(t, r.fetchTracker.Find(stale1.Hash()))
	require.Same(t, stale2, r.fetchTracker.Find(stale2.Hash()))
	target := r.fetchTracker.Find(targetHash)
	require.NotNil(t, target)
	require.LessOrEqual(t, r.protectedCatchupInFlight(), maxConcurrentSpeculativeCatchup)
	require.GreaterOrEqual(t, acquireCount(sender), 1)

	require.NoError(t, target.GotBase([]message.LedgerNode{
		{NodeData: headerData},
		{NodeData: rootData},
	}))
	require.NoError(t, target.GotStateNodes(wire))
	target.CollectMissingRequest(false)
	require.True(t, target.IsComplete())
	r.completeInboundLedger(target)

	require.Eventually(t, func() bool {
		return svc.GetClosedLedgerIndex() == targetSeq && svc.GetValidatedLedgerIndex() == targetSeq
	}, time.Second, time.Millisecond)
	r.checkBehind(targetSeq, targetHash, 8)
	assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode())
	assert.Equal(t, "proposing", consensusServerState(a.GetOperatingMode(), consensus.ModeProposing, true))
	assert.LessOrEqual(t, r.protectedCatchupInFlight(), maxConcurrentSpeculativeCatchup)
}

func TestRouter_Issue1668FrozenPivotCollectsAndReplaysMovingHead(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, closed)
	r.recordSeqHash(pivotSeq, pivotHash, [32]byte{}, false)
	links := buildStandardReplayTestChain(t, r, pivot, maxForwardDeltaGap+16)
	sender.mu.Lock()
	sender.peerSupportsReplay = false
	sender.mu.Unlock()

	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.NoError(t, a.RequestLedger(consensus.LedgerID(pivotHash)))
	pivotAcquisition := r.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivotAcquisition)
	require.False(t, pivotAcquisition.TransactionOnly())
	generation := r.standardReplay.generation

	initialHead := links[11]
	trackCatchupPeer(r, 7, initialHead.seq, initialHead.hash)
	a.OnLedgerFullyValidated(consensus.LedgerID(initialHead.hash), initialHead.seq)
	require.Equal(t, initialHead.seq, r.standardReplay.targetSeq)
	require.Equal(t, standardReplayPipelineWindow, r.standardReplayResidentCountLocked())
	for i := range standardReplayPipelineWindow {
		completeStandardReplayTestLink(t, r, links[i])
	}
	for i := range standardReplayPipelineWindow {
		stored, _ := svc.GetLedgerByHash(links[i].hash)
		assert.Nil(t, stored)
	}

	storeRecoveryLedger(t, svc, pivot)
	require.True(t, r.fetchTracker.RemoveExpectedWithSnapshot(
		pivotAcquisition, pivotAcquisition.Snapshot(), true,
	))
	pivotHeader := pivot.Header()
	require.True(t, r.completeFrozenPivotAcquisition(&pivotHeader, true))
	require.True(t, r.standardReplay.initialCandidate)
	for i := range standardReplayPipelineWindow {
		stored, lookupErr := svc.GetLedgerByHash(links[i].hash)
		require.NoError(t, lookupErr)
		require.NotNil(t, stored)
	}
	select {
	case <-r.standardReplayDrainWake:
		r.drainStandardReplayPipeline()
	default:
		require.FailNow(t, "replay apply batch did not reschedule through the router loop")
	}

	completeStandardReplayTestLink(t, r, links[8])
	completeStandardReplayTestLink(t, r, links[9])
	require.Equal(t, links[9].seq, r.standardReplay.anchorSeq)

	movedHead := links[maxForwardDeltaGap+8]
	trackCatchupPeer(r, 7, movedHead.seq, movedHead.hash)
	a.OnLedgerFullyValidated(consensus.LedgerID(movedHead.hash), movedHead.seq)
	assert.Equal(t, generation, r.standardReplay.generation)
	assert.Equal(t, pivotSeq, r.standardReplay.pivotSeq)
	assert.Equal(t, pivotHash, r.standardReplay.pivotHash)
	assert.Equal(t, movedHead.seq, r.standardReplay.targetSeq)
	assert.Equal(t, movedHead.hash, r.standardReplay.targetHash)
	assert.NotNil(t, r.fetchTracker.Find(links[10].hash))

	pivotRequests := 0
	for _, call := range sender.legacyCalls() {
		if call.hash == pivotHash {
			pivotRequests++
		}
	}
	assert.Equal(t, 1, pivotRequests)
}

func TestRouter_Issue1677ConsensusRecoveryCallbackDoesNotReenterEngineLock(t *testing.T) {
	svc := newTestLedgerService(t)
	a, _ := newRecordingAdaptor(t, svc)
	a.SetOperatingMode(consensus.OpModeFull)

	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	historian := &issue1677RecheckingHistorian{stubHistorian: &stubHistorian{}}
	a.closeOffsetNs.Store(int64(time.Minute))

	now := a.Now()
	config := rcl.DefaultConfig()
	config.ManualTick = true
	config.Clock = func() time.Time { return now }
	config.Timing.LedgerMinClose = time.Millisecond
	config.Timing.LedgerMinConsensus = time.Millisecond
	engine := rcl.NewEngine(a, config)
	require.NoError(t, engine.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Stop()) })
	a.SetValidationHistorian(historian)
	probe := &issue1677EngineProbe{
		Engine:  engine,
		entered: make(chan consensus.LedgerID, 1),
		result:  make(chan issue1677CanAcceptResult, 1),
	}
	r := NewRouter(probe, a, nil)
	require.Equal(t, consensus.OpModeFull, a.GetOperatingMode())

	type builtTarget struct {
		seq  uint32
		hash [32]byte
	}
	built := make(chan builtTarget, 1)
	onLedgerBuilt := a.onLedgerBuilt
	a.setOnLedgerBuilt(func(seq uint32, hash [32]byte) {
		r.catchupMu.Lock()
		r.catchup = catchupTarget{seq: seq, hash: hash, source: catchupSourceQuorum}
		r.catchupMu.Unlock()
		r.acquisitionMu.Lock()
		r.standardReplay = standardReplayPipeline{
			active:     true,
			pivotReady: true,
			pivotSeq:   seq,
			pivotHash:  hash,
			anchorSeq:  seq,
			anchorHash: hash,
			targetSeq:  seq,
			targetHash: hash,
			entries:    make(map[uint32]*standardReplayEntry),
		}
		r.acquisitionMu.Unlock()
		built <- builtTarget{seq: seq, hash: hash}
		onLedgerBuilt(seq, hash)
	})

	round := consensus.RoundID{
		Seq:        parent.Sequence() + 1,
		ParentHash: consensus.LedgerID(parent.Hash()),
	}
	require.NoError(t, engine.StartRound(round, false))
	for range 3 {
		now = now.Add(time.Minute)
		engine.TimerEntry()
		if engine.Phase() == consensus.PhaseEstablish {
			break
		}
	}
	require.Equal(t, consensus.PhaseEstablish, engine.Phase())

	now = now.Add(time.Minute)
	done := make(chan error, 1)
	go func() {
		engine.TimerEntry()
		done <- a.StopLedgerAccept()
	}()

	var target builtTarget
	select {
	case target = <-built:
	case <-time.After(time.Second):
		t.Fatal("accepted ledger did not reach the router callback")
	}
	select {
	case entered := <-probe.entered:
		require.Equal(t, consensus.LedgerID(target.hash), entered)
	case <-time.After(time.Second):
		t.Fatal("recovery did not check the completed ledger")
	}
	select {
	case result := <-probe.result:
		require.NoError(t, result.err)
		require.True(t, result.acceptable)
	case <-time.After(time.Second):
		t.Fatal("completed-ledger acceptance check re-entered Engine.mu")
	}
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("accepted-ledger recovery callback did not complete")
	}

	require.Equal(t, consensus.PhaseOpen, engine.Phase())
	require.Equal(t, consensus.ModeProposing, engine.Mode())
	r.acquisitionMu.Lock()
	retryTarget := r.consensusRecovery.targetHash
	replayActive := r.standardReplay.active
	r.acquisitionMu.Unlock()
	assert.Equal(t, target.hash, retryTarget)
	assert.False(t, replayActive)
	assert.Equal(t, 3, historian.calls)
	validated := svc.GetValidatedLedger()
	require.NotNil(t, validated)
	assert.Equal(t, target.seq, validated.Sequence())
	assert.Equal(t, target.hash, validated.Hash())
}

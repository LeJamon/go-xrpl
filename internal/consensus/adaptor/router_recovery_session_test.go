package adaptor

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFrozenPivotRecoveryWaitsForUnknownSuccessorLink(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xa1}
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	generation := r.standardReplay.generation
	require.True(t, r.continueFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	require.True(t, r.standardReplay.active)
	require.Equal(t, generation, r.standardReplay.generation)

	targetSeq := pivotSeq + 2
	targetHash := [32]byte{0xa3}
	require.True(t, r.continueFrozenPivotRecovery(targetSeq, targetHash, 7))
	require.True(t, r.standardReplay.active)
	require.Equal(t, generation, r.standardReplay.generation)
	require.Equal(t, pivotSeq, r.standardReplay.pivotSeq)
	require.Equal(t, targetSeq, r.standardReplay.targetSeq)
	require.Empty(t, r.standardReplay.entries)
	require.Len(t, sender.legacyCalls(), 1)
}

func TestFrozenPivotRecoveryRejectsUnknownSequenceTarget(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xa4}
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	generation := r.standardReplay.generation

	assert.False(t, r.continueFrozenPivotRecovery(0, [32]byte{0xa5}, 7))
	assert.True(t, r.standardReplay.active)
	assert.Equal(t, generation, r.standardReplay.generation)
	assert.Equal(t, pivotSeq, r.standardReplay.targetSeq)
	assert.Equal(t, pivotHash, r.standardReplay.targetHash)
}

func TestFrozenPivotRecoverySurvivesUnknownSequenceConsensusRequest(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xa5}
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	pivotAcquisition := r.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivotAcquisition)
	generation := r.standardReplay.generation

	// Reproduce startup ordering from issue #1668: peer status starts the
	// frozen pivot, then consensus asks for the same hash before lookupSeqForHash
	// can resolve its sequence. The exact request must join the frozen session,
	// not cancel it and replace it with an ordinary seq=0 acquisition.
	require.NoError(t, a.RequestLedger(consensus.LedgerID(pivotHash)))

	assert.True(t, r.standardReplay.active)
	assert.Equal(t, generation, r.standardReplay.generation)
	assert.Equal(t, pivotSeq, r.standardReplay.pivotSeq)
	assert.Equal(t, pivotHash, r.standardReplay.pivotHash)
	assert.Same(t, pivotAcquisition, r.fetchTracker.Find(pivotHash))
	assert.Len(t, sender.legacyCalls(), 1)
}

func TestHashOnlyConsensusAcquisitionPromotesAfterHeaderResolution(t *testing.T) {
	r, _, svc := makeProvisionalWarmRouter(t)
	closed := svc.GetClosedLedgerIndex()
	rootHash, rootData, _ := buildSelfHealSourceState(t)
	pivotHeader := header.LedgerHeader{
		LedgerIndex: closed + maxForwardDeltaGap + 1,
		ParentHash:  [32]byte{0x91},
		AccountHash: rootHash,
		CloseTime:   time.Unix(1_700_000_200, 0),
	}
	pivotHeader.Hash = header.CalculateHash(pivotHeader)

	// Consensus arrives before any hash -> sequence bookkeeping, reproducing
	// the live startup order. This creates one ordinary hash-only acquisition.
	require.NoError(t, r.requestConsensusLedger(consensus.LedgerID(pivotHeader.Hash)))
	pivotAcquisition := r.fetchTracker.Find(pivotHeader.Hash)
	require.NotNil(t, pivotAcquisition)
	require.True(t, pivotAcquisition.SequenceInitiallyUnknown())
	require.Zero(t, pivotAcquisition.Seq())
	require.False(t, r.standardReplay.active)
	require.Len(t, r.fetchTracker.Active(), 1)

	// The verified base header resolves the sequence. The router must promote
	// the same in-flight object to the frozen session without issuing a second
	// full-state request.
	require.True(t, r.handleInboundLedgerData(pivotAcquisition, &message.LedgerData{
		LedgerHash: pivotHeader.Hash[:],
		InfoType:   message.LedgerInfoBase,
		Nodes: []message.LedgerNode{
			{NodeData: header.AddRaw(pivotHeader, false)},
			{NodeData: rootData},
		},
	}, 7))
	require.Equal(t, pivotHeader.LedgerIndex, pivotAcquisition.Seq())
	require.True(t, r.standardReplay.active)
	require.Equal(t, pivotHeader.LedgerIndex, r.standardReplay.pivotSeq)
	require.Equal(t, pivotHeader.Hash, r.standardReplay.pivotHash)
	require.Same(t, pivotAcquisition, r.fetchTracker.Find(pivotHeader.Hash))
	require.Len(t, r.fetchTracker.Active(), 1)

	// A newly observed successor is now collected as transaction-only replay
	// data while the pivot's state walk remains in flight.
	nextSeq := pivotHeader.LedgerIndex + 1
	nextHash := [32]byte{0xa2}
	r.recordSeqHash(nextSeq, nextHash, pivotHeader.Hash, true)
	trackCatchupPeer(r, 7, nextSeq, nextHash)
	require.True(t, r.continueFrozenPivotRecovery(nextSeq, nextHash, 7))
	nextAcquisition := r.fetchTracker.Find(nextHash)
	require.NotNil(t, nextAcquisition)
	assert.True(t, nextAcquisition.TransactionOnly())
}

func TestFrozenPivotRecoveryKeepsExactConsensusTarget(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xa6}
	exactHash := [32]byte{0xa7}
	movingHash := [32]byte{0xa8}
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	require.True(t, r.continueFrozenPivotRecovery(pivotSeq+1, exactHash, 7))
	r.acquisitionMu.Lock()
	r.consensusRecovery.targetHash = exactHash
	r.acquisitionMu.Unlock()

	require.True(t, r.continueFrozenPivotRecovery(pivotSeq+2, movingHash, 7))
	assert.Equal(t, pivotSeq+2, r.standardReplay.targetSeq)
	assert.Equal(t, movingHash, r.standardReplay.targetHash)
	assert.Equal(t, exactHash, r.consensusRecovery.targetHash)
}

func TestFrozenPivotRecoveryAdvancesConsensusTargetOnTrustedEvidence(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xa9}
	exactHash := [32]byte{0xaa}
	validatedHash := [32]byte{0xab}
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	require.True(t, r.continueFrozenPivotRecovery(pivotSeq+1, exactHash, 7))
	r.acquisitionMu.Lock()
	r.consensusRecovery.targetHash = exactHash
	r.acquisitionMu.Unlock()
	r.recordValidationCatchupTarget(
		pivotSeq+2, validatedHash, 7, catchupSourceQuorum,
	)

	require.True(t, r.continueFrozenPivotRecovery(pivotSeq+2, validatedHash, 7))
	assert.Equal(t, validatedHash, r.standardReplay.targetHash)
	assert.Equal(t, validatedHash, r.consensusRecovery.targetHash)
}

func TestFrozenPivotRecoveryCancelsConflictingPivotEvidence(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xb1}
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	generation := r.standardReplay.generation

	require.False(t, r.continueFrozenPivotRecovery(pivotSeq, [32]byte{0xb2}, 7))
	assert.False(t, r.standardReplay.active)
	assert.Greater(t, r.standardReplay.generation, generation)
	assert.Nil(t, r.fetchTracker.Find(pivotHash))
	assert.Len(t, sender.legacyCalls(), 1)
}

func TestFrozenPivotRecoveryFailureReleasesPivotGeneration(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xc1}
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	generation := r.standardReplay.generation
	released := 0
	r.standardReplay.baseRelease = func() { released++ }

	require.True(t, r.failFrozenPivotRecovery(pivotHash))
	assert.False(t, r.standardReplay.active)
	assert.Greater(t, r.standardReplay.generation, generation)
	assert.Nil(t, r.fetchTracker.Find(pivotHash))
	assert.Equal(t, 1, released)
}

func TestFrozenPivotRecoveryDoesNotStartForHeldLedger(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, parent)
	storeRecoveryLedger(t, svc, pivot)

	generation := r.standardReplay.generation
	assert.False(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	assert.Equal(t, generation, r.standardReplay.generation)
	assert.False(t, r.standardReplay.active)
	assert.Nil(t, r.fetchTracker.Find(pivotHash))
	assert.Empty(t, sender.legacyCalls())
}

func TestLegacyFullStateAcquisitionDoesNotStartForHeldLedger(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, parent)
	storeRecoveryLedger(t, svc, pivot)

	r.startLedgerAcquisitionLegacy(pivotSeq, pivotHash, 7)

	assert.Nil(t, r.fetchTracker.Find(pivotHash))
	assert.Empty(t, sender.legacyCalls())
}

func TestLedgerBuiltRetiresFrozenPivotAndContinuesCatchup(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, parent)
	_, successor, successorHash, successorSeq := buildSuccessorAgainstParent(t, pivot)
	_, _, targetHash, targetSeq := buildSuccessorAgainstParent(t, successor)
	trackCatchupPeer(r, 7, targetSeq, targetHash)
	r.recordSeqHash(pivotSeq, pivotHash, parent.Hash(), true)
	r.recordSeqHash(successorSeq, successorHash, pivotHash, true)
	r.recordSeqHash(targetSeq, targetHash, successorHash, true)
	r.recordValidationCatchupTarget(targetSeq, targetHash, 7, catchupSourceQuorum)

	r.acquisitionMu.Lock()
	r.consensusRecovery.targetHash = targetHash
	r.acquisitionMu.Unlock()
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	released := 0
	r.standardReplay.baseRelease = func() { released++ }
	require.NotNil(t, r.fetchTracker.Find(pivotHash))
	require.NoError(t, pivot.SetValidated())
	require.NoError(t, svc.SwitchToPreferredLedger(pivot))

	r.onLedgerBuilt(pivotSeq, pivotHash)

	assert.False(t, r.standardReplay.active)
	assert.Nil(t, r.fetchTracker.Find(pivotHash))
	assert.Equal(t, 1, released)
	assert.Equal(t, targetHash, r.consensusRecovery.targetHash)
	assert.Zero(t, r.consensusRecovery.stepHash)
	assert.True(t, r.isAcquiring(successorHash))
}

func TestLedgerFullyValidatedRetiresHeldFrozenPivot(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, parent)
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	require.NoError(t, pivot.SetValidated())
	require.NoError(t, svc.SwitchToPreferredLedger(pivot))

	r.onLedgerFullyValidated(pivotSeq, pivotHash)

	assert.False(t, r.standardReplay.active)
	assert.Nil(t, r.fetchTracker.Find(pivotHash))
}

func TestConsensusCatchupRetiresLocallySatisfiedFrozenPivot(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, parent)
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	r.recordValidationCatchupTarget(pivotSeq, pivotHash, 7, catchupSourceQuorum)
	r.acquisitionMu.Lock()
	r.consensusRecovery.targetHash = pivotHash
	r.acquisitionMu.Unlock()
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	require.NoError(t, pivot.SetValidated())
	require.NoError(t, svc.SwitchToPreferredLedger(pivot))

	r.armConsensusCatchup()

	assert.False(t, r.standardReplay.active)
	assert.Nil(t, r.fetchTracker.Find(pivotHash))
	assert.Equal(t, consensusRecovery{}, r.consensusRecovery)
}

func TestConsensusCatchupHandsHeldPivotToConsensus(t *testing.T) {
	r, engine, svc := makeRouterWithEngine(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, parent)
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	r.recordValidationCatchupTarget(pivotSeq, pivotHash, 7, catchupSourceQuorum)
	r.acquisitionMu.Lock()
	r.consensusRecovery.targetHash = pivotHash
	r.acquisitionMu.Unlock()
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	storeRecoveryLedger(t, svc, pivot)

	r.armConsensusCatchup()

	assert.False(t, r.standardReplay.active)
	assert.Nil(t, r.fetchTracker.Find(pivotHash))
	assert.Equal(t, []consensus.LedgerID{consensus.LedgerID(pivotHash)}, engine.getLedgers())
	assert.Equal(t, pivotSeq, r.consensusRecovery.anchorSeq)
	assert.Equal(t, pivotHash, r.consensusRecovery.anchorHash)
	assert.Zero(t, r.consensusRecovery.targetHash)
	assert.Zero(t, r.consensusRecovery.stepHash)
}

func TestFrozenPivotFailedStartCannotBeKeptAliveByTargetAdvance(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xc2}
	r.markFailedCatchupAcquisition(pivotHash)

	r.replayCommitMu.Lock()
	done := make(chan bool, 1)
	go func() {
		done <- r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7)
	}()
	require.Eventually(t, func() bool {
		r.acquisitionMu.Lock()
		defer r.acquisitionMu.Unlock()
		return r.standardReplay.active && r.standardReplay.pivotHash == pivotHash
	}, time.Second, time.Millisecond)
	require.Nil(t, r.fetchTracker.Find(pivotHash))

	advancedHash := [32]byte{0xc3}
	require.True(t, r.continueFrozenPivotRecovery(pivotSeq+1, advancedHash, 7))
	r.replayCommitMu.Unlock()

	require.False(t, <-done)
	assert.False(t, r.standardReplay.active)
	assert.Nil(t, r.fetchTracker.Find(pivotHash))
}

func TestFrozenPivotRecoveryRebootstrapsAfterTwoNoProgressWindows(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	started := time.Unix(100, 0)
	targetHash := [32]byte{0xd1}
	trackCatchupPeer(r, 7, 200, targetHash)
	r.recordValidationCatchupTarget(200, targetHash, 7, catchupSourceQuorum)
	r.standardReplay = standardReplayPipeline{
		generation:       3,
		active:           true,
		pivotReady:       true,
		pivotSeq:         100,
		anchorSeq:        100,
		targetSeq:        200,
		targetHash:       targetHash,
		entries:          make(map[uint32]*standardReplayEntry),
		progressSampleAt: started,
		sampleAnchorSeq:  100,
	}

	assert.False(t, r.rebootstrapFrozenPivotIfStalled(started.Add(standardReplayProgressWindow)))
	assert.True(t, r.standardReplay.active)
	assert.Equal(t, uint8(1), r.standardReplay.stalledSamples)

	assert.True(t, r.rebootstrapFrozenPivotIfStalled(started.Add(2*standardReplayProgressWindow)))
	assert.True(t, r.standardReplay.active)
	assert.False(t, r.standardReplay.pivotReady)
	assert.Equal(t, uint64(5), r.standardReplay.generation)
	assert.Equal(t, uint32(200), r.standardReplay.pivotSeq)
	assert.Equal(t, targetHash, r.standardReplay.pivotHash)
	pivot := r.fetchTracker.Find(targetHash)
	require.NotNil(t, pivot)
	assert.False(t, pivot.TransactionOnly())
	assert.Equal(t, uint64(1), r.FastSyncMetrics().ReplayPipelineFallbacks)
}

func TestFrozenPivotRecoveryRebootstrapRetiresObsoleteProvisionalFullState(t *testing.T) {
	r, _, svc := makeProvisionalWarmRouter(t)
	started := time.Unix(100, 0)
	frontierSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	oldPivotHash := [32]byte{0xd1}
	obsoleteSeq := frontierSeq + 50
	obsoleteHash := [32]byte{0xd2}
	targetSeq := obsoleteSeq + 50
	targetHash := [32]byte{0xd3}

	trackCatchupPeer(r, 7, targetSeq, targetHash)
	r.recordValidationCatchupTarget(targetSeq, targetHash, 7, catchupSourceQuorum)
	r.acquisitionMu.Lock()
	r.startLedgerAcquisitionLegacyLocked(obsoleteSeq, obsoleteHash, 7)
	r.standardReplay = standardReplayPipeline{
		generation:       3,
		active:           true,
		pivotReady:       true,
		pivotSeq:         frontierSeq,
		pivotHash:        oldPivotHash,
		anchorSeq:        frontierSeq,
		targetSeq:        targetSeq,
		targetHash:       targetHash,
		entries:          make(map[uint32]*standardReplayEntry),
		progressSampleAt: started,
		sampleAnchorSeq:  frontierSeq,
		stalledSamples:   standardReplayStallWindows - 1,
	}
	r.acquisitionMu.Unlock()
	require.NotNil(t, r.fetchTracker.Find(obsoleteHash))

	require.True(t, r.rebootstrapFrozenPivotIfStalled(started.Add(standardReplayProgressWindow)))

	assert.Nil(t, r.fetchTracker.Find(obsoleteHash))
	replacement := r.fetchTracker.Find(targetHash)
	require.NotNil(t, replacement)
	assert.False(t, replacement.TransactionOnly())
	assert.True(t, r.standardReplay.active)
	assert.False(t, r.standardReplay.pivotReady)
	assert.Equal(t, targetSeq, r.standardReplay.pivotSeq)
	assert.Equal(t, targetHash, r.standardReplay.pivotHash)
	replacementGeneration := r.standardReplay.generation
	assert.False(t, r.completeFrozenPivotAcquisition(&header.LedgerHeader{
		LedgerIndex: frontierSeq,
		Hash:        oldPivotHash,
	}, false))
	assert.Equal(t, replacementGeneration, r.standardReplay.generation)
	assert.Equal(t, targetHash, r.standardReplay.pivotHash)
}

func TestPendingConsensusLedgerReportsBlockedStart(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	targetHash := [32]byte{0xA4}
	r.consensusRecovery.targetHash = targetHash
	r.catchupFailures = make(map[[32]byte]time.Time)
	r.catchupFailures[targetHash] = time.Now().Add(time.Minute)

	require.False(t, r.armPendingConsensusLedger())
	assert.Nil(t, r.fetchTracker.Find(targetHash))
	assert.Equal(t, consensusRecovery{targetHash: targetHash}, r.consensusRecovery)
}

func TestFrozenPivotRecoveryBackpressuresAtPreparedCapacity(t *testing.T) {
	r, _, svc := makeProvisionalWarmRouter(t)
	var logs bytes.Buffer
	r.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xd4}
	r.standardReplay.backpressured = true
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	require.False(t, r.standardReplay.backpressured)
	pivotAcquisition := r.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivotAcquisition)
	generation := r.standardReplay.generation
	parentHash := pivotHash

	for offset := uint32(1); offset <= standardReplayPreparedLimit+1; offset++ {
		seq := pivotSeq + offset
		var hash [32]byte
		hash[0] = 0xd5
		binary.BigEndian.PutUint32(hash[len(hash)-4:], seq)
		r.recordSeqHash(seq, hash, parentHash, true)
		trackCatchupPeer(r, 7, seq, hash)
		r.recordValidationCatchupTarget(seq, hash, 7, catchupSourceQuorum)
		require.True(t, r.continueFrozenPivotRecovery(seq, hash, 7))

		r.acquisitionMu.Lock()
		for _, entry := range r.standardReplay.entries {
			if entry.acquisition == nil {
				continue
			}
			require.True(t, r.fetchTracker.DiscardExpected(entry.acquisition))
			entry.acquisition = nil
			entry.durable = true
		}
		r.acquisitionMu.Unlock()
		parentHash = hash
	}

	assert.False(t, r.rebootstrapFrozenPivotIfStalled(time.Now()))
	assert.True(t, r.standardReplay.active)
	assert.False(t, r.standardReplay.pivotReady)
	assert.Equal(t, generation, r.standardReplay.generation)
	assert.Equal(t, pivotSeq, r.standardReplay.pivotSeq)
	assert.Equal(t, pivotHash, r.standardReplay.pivotHash)
	assert.Same(t, pivotAcquisition, r.fetchTracker.Find(pivotHash))
	assert.Len(t, r.standardReplay.entries, standardReplayPreparedLimit)
	metrics := r.FastSyncMetrics()
	assert.Equal(t, generation, metrics.ReplayPipelineGeneration)
	assert.Equal(t, pivotSeq, metrics.ReplayPipelinePivotSeq)
	assert.Equal(t, pivotSeq+standardReplayPreparedLimit, metrics.ReplayPipelinePreparedTailSeq)
	assert.Equal(t, uint32(standardReplayPreparedLimit), metrics.ReplayPipelinePreparedLimit)
	assert.Equal(t, uint32(standardReplayPreparedLimit), metrics.ReplayPipelineDepth)
	assert.Equal(t, pivotSeq+standardReplayPreparedLimit+1, metrics.ReplayPipelineTrustedHeadSeq)
	assert.Equal(t, uint64(1), metrics.ReplayPipelineBackpressureEvents)
	assert.Zero(t, metrics.ReplayPipelineFallbacks)
	assert.Zero(t, metrics.ReplayPipelineRetargetFailures)
	assert.Contains(t, logs.String(), `"msg":"standard replay collector paused at prepared capacity"`)
	assert.Contains(t, logs.String(), `"prepared_occupancy":2048`)
	assert.NotContains(t, logs.String(), "retargeting frozen recovery")
}

func TestFrozenPivotBackpressureRefillsAsReplayDrains(t *testing.T) {
	r, _, svc := makeProvisionalWarmRouter(t)
	engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
	r.engine = engine
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, closed)
	head := buildStandardReplayTestChain(t, r, pivot, 1)[0]

	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	generation := r.standardReplay.generation
	trackCatchupPeer(r, 7, head.seq, head.hash)
	require.True(t, r.continueFrozenPivotRecovery(head.seq, head.hash, 7))
	completeStandardReplayTestLink(t, r, head)

	r.acquisitionMu.Lock()
	var tailHash [32]byte
	for offset := uint32(2); offset <= standardReplayPreparedLimit; offset++ {
		seq := pivotSeq + offset
		tailHash = [32]byte{0xe1}
		binary.BigEndian.PutUint32(tailHash[len(tailHash)-4:], seq)
		r.standardReplay.entries[seq] = &standardReplayEntry{
			generation: r.standardReplay.generation,
			seq:        seq,
			hash:       tailHash,
			durable:    true,
		}
	}
	r.standardReplay.collectSeq = pivotSeq + standardReplayPreparedLimit
	r.standardReplay.collectHash = tailHash
	r.acquisitionMu.Unlock()

	nextSeq := pivotSeq + standardReplayPreparedLimit + 1
	nextHash := [32]byte{0xe2}
	r.recordSeqHash(nextSeq, nextHash, tailHash, true)
	trackCatchupPeer(r, 7, nextSeq, nextHash)
	r.recordValidationCatchupTarget(nextSeq, nextHash, 7, catchupSourceQuorum)
	require.True(t, r.continueFrozenPivotRecovery(nextSeq, nextHash, 7))
	assert.Nil(t, r.fetchTracker.Find(nextHash))
	assert.Len(t, r.standardReplay.entries, standardReplayPreparedLimit)

	pivotAcquisition := r.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivotAcquisition)
	storeRecoveryLedger(t, svc, pivot)
	require.True(t, r.fetchTracker.RemoveExpectedWithSnapshot(
		pivotAcquisition, pivotAcquisition.Snapshot(), true,
	))
	pivotHeader := pivot.Header()
	require.True(t, r.completeFrozenPivotAcquisition(&pivotHeader, false))

	assert.True(t, r.standardReplay.active)
	assert.True(t, r.standardReplay.pivotReady)
	assert.Equal(t, generation, r.standardReplay.generation)
	assert.Equal(t, pivotSeq, r.standardReplay.pivotSeq)
	assert.Equal(t, head.seq, r.standardReplay.anchorSeq)
	assert.Len(t, r.standardReplay.entries, standardReplayPreparedLimit)
	next := r.standardReplay.entries[nextSeq]
	require.NotNil(t, next)
	assert.NotNil(t, next.acquisition)
	assert.Same(t, next.acquisition, r.fetchTracker.Find(nextHash))
	assert.Equal(t, uint64(1), r.FastSyncMetrics().ReplayPipelineApplied)
}

func TestFrozenPivotRecoveryReplaysToMovingTrustedHead(t *testing.T) {
	r, _, svc := makeProvisionalWarmRouter(t)
	engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
	r.engine = engine
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, closed)
	links := buildStandardReplayTestChain(t, r, pivot, 3)

	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	released := 0
	r.standardReplay.baseRelease = func() { released++ }
	initialHead := links[len(links)-1]
	trackCatchupPeer(r, 7, initialHead.seq, initialHead.hash)
	r.recordValidationCatchupTarget(initialHead.seq, initialHead.hash, 7, catchupSourceQuorum)
	require.True(t, r.continueFrozenPivotRecovery(initialHead.seq, initialHead.hash, 7))
	for _, link := range links {
		completeStandardReplayTestLink(t, r, link)
	}

	movingLinks := buildStandardReplayTestChain(t, r, initialHead.ledger, 2)
	trustedHead := movingLinks[len(movingLinks)-1]
	trackCatchupPeer(r, 7, trustedHead.seq, trustedHead.hash)
	r.recordValidationCatchupTarget(trustedHead.seq, trustedHead.hash, 7, catchupSourceQuorum)
	require.True(t, r.continueFrozenPivotRecovery(trustedHead.seq, trustedHead.hash, 7))
	for _, link := range movingLinks {
		completeStandardReplayTestLink(t, r, link)
	}
	r.acquisitionMu.Lock()
	r.standardReplay.backpressured = true
	r.acquisitionMu.Unlock()

	pivotAcquisition := r.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivotAcquisition)
	storeRecoveryLedger(t, svc, pivot)
	require.True(t, r.fetchTracker.RemoveExpectedWithSnapshot(
		pivotAcquisition, pivotAcquisition.Snapshot(), true,
	))
	pivotHeader := pivot.Header()
	require.True(t, r.completeFrozenPivotAcquisition(&pivotHeader, false))

	assert.False(t, r.standardReplay.active)
	assert.Equal(t, 1, released)
	assert.False(t, r.standardReplay.backpressured)
	assert.Equal(t, uint64(len(links)+len(movingLinks)), r.FastSyncMetrics().ReplayPipelineApplied)
	storedHead, err := svc.GetLedgerByHash(trustedHead.hash)
	require.NoError(t, err)
	require.NotNil(t, storedHead)
	assert.Equal(t, trustedHead.seq, storedHead.Sequence())
	switched := engine.getLedgers()
	require.NotEmpty(t, switched)
	assert.Equal(t, consensus.LedgerID(trustedHead.hash), switched[len(switched)-1])
	assert.Equal(t, consensus.OpModeTracking, r.adaptor.GetOperatingMode())
}

func TestFrozenPivotRecoveryKeepsAdvancingReplayAtMovingTipRate(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	started := time.Unix(200, 0)
	r.standardReplay = standardReplayPipeline{
		generation:       5,
		active:           true,
		pivotReady:       true,
		pivotSeq:         100,
		anchorSeq:        100,
		targetSeq:        200,
		targetHash:       [32]byte{0xe1},
		entries:          make(map[uint32]*standardReplayEntry),
		progressSampleAt: started,
		sampleAnchorSeq:  100,
		stalledSamples:   1,
	}
	r.standardReplay.anchorSeq = 110
	r.standardReplay.targetSeq = 210

	assert.False(t, r.rebootstrapFrozenPivotIfStalled(started.Add(standardReplayProgressWindow)))
	assert.True(t, r.standardReplay.active)
	assert.Zero(t, r.standardReplay.stalledSamples)
	assert.Equal(t, uint64(5), r.standardReplay.generation)
}

func TestFrozenPivotRecoveryDoesNotTimeoutPivotDownload(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	started := time.Unix(300, 0)
	r.standardReplay = standardReplayPipeline{
		generation:       8,
		active:           true,
		pivotReady:       false,
		pivotSeq:         100,
		anchorSeq:        100,
		targetSeq:        300,
		targetHash:       [32]byte{0xf1},
		entries:          make(map[uint32]*standardReplayEntry),
		progressSampleAt: started,
		sampleAnchorSeq:  100,
	}

	assert.False(t, r.rebootstrapFrozenPivotIfStalled(started.Add(24*time.Hour)))
	assert.True(t, r.standardReplay.active)
	assert.Equal(t, uint64(8), r.standardReplay.generation)
}

func TestFrozenPivotBootstrapFailureRearmsTrustedTarget(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivot := completedCatchUpAcquisition(t, svc.GetClosedLedgerIndex()+10)
	pivotSeq, pivotHash := pivot.Seq(), pivot.Hash()
	r.fetchTracker.Track(pivot)
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))

	replacement := completedCatchUpAcquisition(t, pivotSeq+10)
	trackCatchupPeer(r, 7, replacement.Seq(), replacement.Hash())
	r.recordValidationCatchupTarget(
		replacement.Seq(), replacement.Hash(), 7, catchupSourceQuorum,
	)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	r.lifecycleMu.Lock()
	r.lifecycleCtx = canceled
	r.lifecycleMu.Unlock()
	t.Cleanup(func() {
		r.lifecycleMu.Lock()
		r.lifecycleCtx = context.Background()
		r.lifecycleMu.Unlock()
	})

	r.completeInboundLedger(pivot)

	assert.Nil(t, r.fetchTracker.Find(pivotHash))
	assert.True(t, r.standardReplay.active)
	assert.False(t, r.standardReplay.pivotReady)
	assert.Equal(t, replacement.Seq(), r.standardReplay.pivotSeq)
	assert.Equal(t, replacement.Hash(), r.standardReplay.pivotHash)
	rearmed := r.fetchTracker.Find(replacement.Hash())
	require.NotNil(t, rearmed)
	assert.False(t, rearmed.TransactionOnly())
}

func TestFrozenPivotHandoffKeepsSessionWhenTargetAdvances(t *testing.T) {
	r, a, _, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, closed)
	links := buildStandardReplayTestChain(t, r, pivot, 1)
	r.recordSeqHash(pivotSeq, pivotHash, closed.Hash(), true)
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	r.acquisitionMu.Lock()
	r.consensusRecovery.targetHash = pivotHash
	r.acquisitionMu.Unlock()
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	pivotAcquisition := r.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivotAcquisition)
	generation := r.standardReplay.generation

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
	storeRecoveryLedger(t, svc, pivot)
	require.True(t, r.fetchTracker.RemoveExpectedWithSnapshot(
		pivotAcquisition, pivotAcquisition.Snapshot(), true,
	))
	pivotHeader := pivot.Header()
	done := make(chan bool, 1)
	go func() {
		done <- r.completeFrozenPivotAcquisition(&pivotHeader, true)
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("pivot did not reach the handoff barrier")
	}
	trackCatchupPeer(r, 7, links[0].seq, links[0].hash)
	require.NoError(t, a.RequestLedger(consensus.LedgerID(links[0].hash)))
	assert.True(t, r.standardReplay.active)
	assert.Equal(t, generation, r.standardReplay.generation)
	assert.Equal(t, pivotHash, r.standardReplay.pivotHash)
	next := r.fetchTracker.Find(links[0].hash)
	require.NotNil(t, next)
	assert.True(t, next.TransactionOnly())

	close(release)
	select {
	case handled := <-done:
		assert.True(t, handled)
	case <-time.After(time.Second):
		t.Fatal("pivot did not leave the handoff barrier")
	}
}

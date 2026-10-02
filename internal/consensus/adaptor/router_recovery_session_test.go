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
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFrozenPivotRecoveryWaitsForUnknownSuccessorLink(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xa1}
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	generation := r.catchupReplay.standardReplay.generation
	require.True(t, r.catchupReplay.continueFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	require.True(t, r.catchupReplay.standardReplay.active)
	require.Equal(t, generation, r.catchupReplay.standardReplay.generation)

	targetSeq := pivotSeq + 2
	targetHash := [32]byte{0xa3}
	require.True(t, r.catchupReplay.continueFrozenPivotRecovery(targetSeq, targetHash, 7))
	require.True(t, r.catchupReplay.standardReplay.active)
	require.Equal(t, generation, r.catchupReplay.standardReplay.generation)
	require.Equal(t, pivotSeq, r.catchupReplay.standardReplay.pivotSeq)
	require.Equal(t, targetSeq, r.catchupReplay.standardReplay.targetSeq)
	require.Empty(t, r.catchupReplay.standardReplay.entries)
	require.Len(t, sender.legacyCalls(), 1)
}

func TestFrozenPivotRecoveryRejectsUnknownSequenceTarget(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xa4}
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	generation := r.catchupReplay.standardReplay.generation

	assert.False(t, r.catchupReplay.continueFrozenPivotRecovery(0, [32]byte{0xa5}, 7))
	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.Equal(t, generation, r.catchupReplay.standardReplay.generation)
	assert.Equal(t, pivotSeq, r.catchupReplay.standardReplay.targetSeq)
	assert.Equal(t, pivotHash, r.catchupReplay.standardReplay.targetHash)
}

func TestFrozenPivotRecoverySurvivesUnknownSequenceConsensusRequest(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xa5}
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	pivotAcquisition := r.catchupReplay.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivotAcquisition)
	generation := r.catchupReplay.standardReplay.generation

	// Reproduce startup ordering from issue #1668: peer status starts the
	// frozen pivot, then consensus asks for the same hash before lookupSeqForHash
	// can resolve its sequence. The exact request must join the frozen session,
	// not cancel it and replace it with an ordinary seq=0 acquisition.
	require.NoError(t, a.RequestLedger(consensus.LedgerID(pivotHash)))

	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.Equal(t, generation, r.catchupReplay.standardReplay.generation)
	assert.Equal(t, pivotSeq, r.catchupReplay.standardReplay.pivotSeq)
	assert.Equal(t, pivotHash, r.catchupReplay.standardReplay.pivotHash)
	assert.Same(t, pivotAcquisition, r.catchupReplay.fetchTracker.Find(pivotHash))
	assert.Len(t, sender.legacyCalls(), 1)
}

func TestHashOnlyConsensusAcquisitionPromotesAfterHeaderResolution(t *testing.T) {
	r, sender, svc := makeProvisionalWarmRouter(t)
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
	r.setPeerSessionView(&testPeerSessions{connected: map[peermanagement.PeerID]bool{7: true}})
	sender.acquisitionPeers = []uint64{7}
	require.NoError(t, r.catchupReplay.requestConsensusLedger(consensus.LedgerID(pivotHeader.Hash)))
	pivotAcquisition := r.catchupReplay.fetchTracker.Find(pivotHeader.Hash)
	require.NotNil(t, pivotAcquisition)
	require.True(t, pivotAcquisition.SequenceInitiallyUnknown())
	require.Zero(t, pivotAcquisition.Seq())
	require.False(t, r.catchupReplay.standardReplay.active)
	require.Len(t, r.catchupReplay.fetchTracker.Active(), 1)

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
	require.True(t, r.catchupReplay.standardReplay.active)
	require.Equal(t, pivotHeader.LedgerIndex, r.catchupReplay.standardReplay.pivotSeq)
	require.Equal(t, pivotHeader.Hash, r.catchupReplay.standardReplay.pivotHash)
	require.Same(t, pivotAcquisition, r.catchupReplay.fetchTracker.Find(pivotHeader.Hash))
	require.Len(t, r.catchupReplay.fetchTracker.Active(), 1)

	// A newly observed successor is now collected as transaction-only replay
	// data while the pivot's state walk remains in flight.
	nextSeq := pivotHeader.LedgerIndex + 1
	nextHash := [32]byte{0xa2}
	r.catchupReplay.recordSeqHash(nextSeq, nextHash, pivotHeader.Hash, true)
	trackCatchupPeer(r, 7, nextSeq, nextHash)
	require.True(t, r.catchupReplay.continueFrozenPivotRecovery(nextSeq, nextHash, 7))
	nextAcquisition := r.catchupReplay.fetchTracker.Find(nextHash)
	require.NotNil(t, nextAcquisition)
	assert.True(t, nextAcquisition.TransactionOnly())
}

func TestFrozenPivotRecoveryKeepsExactConsensusTarget(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xa6}
	exactHash := [32]byte{0xa7}
	movingHash := [32]byte{0xa8}
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	require.True(t, r.catchupReplay.continueFrozenPivotRecovery(pivotSeq+1, exactHash, 7))
	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.consensusRecovery.targetHash = exactHash
	r.catchupReplay.acquisitionMu.Unlock()

	require.True(t, r.catchupReplay.continueFrozenPivotRecovery(pivotSeq+2, movingHash, 7))
	assert.Equal(t, pivotSeq+2, r.catchupReplay.standardReplay.targetSeq)
	assert.Equal(t, movingHash, r.catchupReplay.standardReplay.targetHash)
	assert.Equal(t, exactHash, r.catchupReplay.consensusRecovery.targetHash)
}

func TestFrozenPivotRecoveryAdvancesConsensusTargetOnTrustedEvidence(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xa9}
	exactHash := [32]byte{0xaa}
	validatedHash := [32]byte{0xab}
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	require.True(t, r.catchupReplay.continueFrozenPivotRecovery(pivotSeq+1, exactHash, 7))
	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.consensusRecovery.targetHash = exactHash
	r.catchupReplay.acquisitionMu.Unlock()
	r.catchupReplay.recordValidationCatchupTarget(
		pivotSeq+2, validatedHash, 7, catchupSourceQuorum,
	)

	require.True(t, r.catchupReplay.continueFrozenPivotRecovery(pivotSeq+2, validatedHash, 7))
	assert.Equal(t, validatedHash, r.catchupReplay.standardReplay.targetHash)
	assert.Equal(t, validatedHash, r.catchupReplay.consensusRecovery.targetHash)
}

func TestFrozenPivotRecoveryCancelsConflictingPivotEvidence(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xb1}
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	generation := r.catchupReplay.standardReplay.generation

	require.False(t, r.catchupReplay.continueFrozenPivotRecovery(pivotSeq, [32]byte{0xb2}, 7))
	assert.False(t, r.catchupReplay.standardReplay.active)
	assert.Greater(t, r.catchupReplay.standardReplay.generation, generation)
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(pivotHash))
	assert.Len(t, sender.legacyCalls(), 1)
}

func TestFrozenPivotRecoveryFailureReleasesPivotGeneration(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xc1}
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	generation := r.catchupReplay.standardReplay.generation
	released := 0
	r.catchupReplay.standardReplay.baseRelease = func() { released++ }

	r.catchupReplay.discardFailedInboundAcquisition(r.catchupReplay.fetchTracker.Find(pivotHash), nil)
	assert.False(t, r.catchupReplay.standardReplay.active)
	assert.Greater(t, r.catchupReplay.standardReplay.generation, generation)
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(pivotHash))
	assert.Equal(t, 1, released)
}

func TestFrozenPivotRecoveryDoesNotStartForHeldLedger(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, parent)
	storeRecoveryLedger(t, svc, pivot)

	generation := r.catchupReplay.standardReplay.generation
	assert.False(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	assert.Equal(t, generation, r.catchupReplay.standardReplay.generation)
	assert.False(t, r.catchupReplay.standardReplay.active)
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(pivotHash))
	assert.Empty(t, sender.legacyCalls())
}

func TestLegacyFullStateAcquisitionDoesNotStartForHeldLedger(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, parent)
	storeRecoveryLedger(t, svc, pivot)

	r.catchupReplay.startLedgerAcquisitionLegacy(pivotSeq, pivotHash, 7)

	assert.Nil(t, r.catchupReplay.fetchTracker.Find(pivotHash))
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
	r.catchupReplay.recordSeqHash(pivotSeq, pivotHash, parent.Hash(), true)
	r.catchupReplay.recordSeqHash(successorSeq, successorHash, pivotHash, true)
	r.catchupReplay.recordSeqHash(targetSeq, targetHash, successorHash, true)
	r.catchupReplay.recordValidationCatchupTarget(targetSeq, targetHash, 7, catchupSourceQuorum)

	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.consensusRecovery.targetHash = targetHash
	r.catchupReplay.acquisitionMu.Unlock()
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	released := 0
	r.catchupReplay.standardReplay.baseRelease = func() { released++ }
	require.NotNil(t, r.catchupReplay.fetchTracker.Find(pivotHash))
	require.NoError(t, pivot.SetValidated())
	require.NoError(t, svc.SwitchToPreferredLedger(pivot))

	r.catchupReplay.onLedgerBuilt(pivotSeq, pivotHash)

	assert.False(t, r.catchupReplay.standardReplay.active)
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(pivotHash))
	assert.Equal(t, 1, released)
	assert.Equal(t, targetHash, r.catchupReplay.consensusRecovery.targetHash)
	assert.Zero(t, r.catchupReplay.consensusRecovery.stepHash)
	assert.True(t, r.catchupReplay.isAcquiring(successorHash))
}

func TestLedgerFullyValidatedRetiresHeldFrozenPivot(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, parent)
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	require.NoError(t, pivot.SetValidated())
	require.NoError(t, svc.SwitchToPreferredLedger(pivot))

	r.catchupReplay.onLedgerFullyValidated(pivotSeq, pivotHash)

	assert.False(t, r.catchupReplay.standardReplay.active)
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(pivotHash))
}

func TestConsensusCatchupRetiresLocallySatisfiedFrozenPivot(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, parent)
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	r.catchupReplay.recordValidationCatchupTarget(pivotSeq, pivotHash, 7, catchupSourceQuorum)
	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.consensusRecovery.targetHash = pivotHash
	r.catchupReplay.acquisitionMu.Unlock()
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	require.NoError(t, pivot.SetValidated())
	require.NoError(t, svc.SwitchToPreferredLedger(pivot))

	r.catchupReplay.armConsensusCatchup()

	assert.False(t, r.catchupReplay.standardReplay.active)
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(pivotHash))
	assert.Equal(t, consensusRecovery{}, r.catchupReplay.consensusRecovery)
}

func TestConsensusCatchupHandsHeldPivotToConsensus(t *testing.T) {
	r, engine, svc := makeRouterWithEngine(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, parent)
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	r.catchupReplay.recordValidationCatchupTarget(pivotSeq, pivotHash, 7, catchupSourceQuorum)
	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.consensusRecovery.targetHash = pivotHash
	r.catchupReplay.acquisitionMu.Unlock()
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	storeRecoveryLedger(t, svc, pivot)

	r.catchupReplay.armConsensusCatchup()

	assert.False(t, r.catchupReplay.standardReplay.active)
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(pivotHash))
	assert.Equal(t, []consensus.LedgerID{consensus.LedgerID(pivotHash)}, engine.getLedgers())
	assert.Equal(t, pivotSeq, r.catchupReplay.consensusRecovery.anchorSeq)
	assert.Equal(t, pivotHash, r.catchupReplay.consensusRecovery.anchorHash)
	assert.Zero(t, r.catchupReplay.consensusRecovery.targetHash)
	assert.Zero(t, r.catchupReplay.consensusRecovery.stepHash)
}

func TestFrozenPivotRecoveryWaitsForRetryCooldownWithoutGenerationChurn(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xc2}
	r.catchupReplay.markFailedCatchupAcquisition(pivotHash)

	generation := r.catchupReplay.standardReplay.generation
	assert.False(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	r.catchupReplay.acquisitionMu.Lock()
	assert.Equal(t, generation, r.catchupReplay.standardReplay.generation)
	assert.False(t, r.catchupReplay.standardReplay.active)
	assert.Equal(t, frozenPivotPendingIntent{seq: pivotSeq, hash: pivotHash, peerID: 7}, r.catchupReplay.pendingFrozenPivot)
	r.catchupReplay.acquisitionMu.Unlock()
	require.Nil(t, r.catchupReplay.fetchTracker.Find(pivotHash))
}

func TestFrozenPivotRecoveryRebootstrapsAfterTwoNoProgressWindows(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	started := time.Unix(100, 0)
	targetHash := [32]byte{0xd1}
	trackCatchupPeer(r, 7, 200, targetHash)
	r.catchupReplay.recordValidationCatchupTarget(200, targetHash, 7, catchupSourceQuorum)
	r.catchupReplay.standardReplay = standardReplayPipeline{
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

	assert.False(t, r.catchupReplay.rebootstrapFrozenPivotIfStalled(started.Add(standardReplayProgressWindow)))
	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.Equal(t, uint8(1), r.catchupReplay.standardReplay.stalledSamples)

	assert.True(t, r.catchupReplay.rebootstrapFrozenPivotIfStalled(started.Add(2*standardReplayProgressWindow)))
	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.True(t, r.catchupReplay.standardReplay.pivotReady)
	assert.Equal(t, uint64(3), r.catchupReplay.standardReplay.generation)
	assert.Equal(t, uint32(100), r.catchupReplay.standardReplay.pivotSeq)
	require.NotNil(t, r.catchupReplay.standardReplay.replacement)
	assert.Equal(t, targetHash, r.catchupReplay.standardReplay.replacement.hash)
	pivot := r.catchupReplay.fetchTracker.Find(targetHash)
	require.NotNil(t, pivot)
	assert.False(t, pivot.TransactionOnly())
	assert.Equal(t, uint64(1), r.FastSyncMetrics().ReplayPipelineFallbacks)
}

func TestFrozenPivotRecoveryRebootstrapWaitsForProvisionalFullState(t *testing.T) {
	r, _, svc := makeProvisionalWarmRouter(t)
	started := time.Unix(100, 0)
	_, anchor, oldPivotHash, frontierSeq := buildSuccessorAgainstParent(t, svc.GetClosedLedger())
	storeRecoveryLedger(t, svc, anchor)
	obsoleteSeq := frontierSeq + 50
	obsoleteHash := [32]byte{0xd2}
	targetSeq := obsoleteSeq + 50
	targetHash := [32]byte{0xd3}

	trackCatchupPeer(r, 7, targetSeq, targetHash)
	r.catchupReplay.recordValidationCatchupTarget(targetSeq, targetHash, 7, catchupSourceQuorum)
	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.startLedgerAcquisitionLegacyLocked(obsoleteSeq, obsoleteHash, 7)
	r.catchupReplay.standardReplay = standardReplayPipeline{
		generation:       3,
		active:           true,
		pivotReady:       true,
		pivotSeq:         frontierSeq,
		pivotHash:        oldPivotHash,
		anchorSeq:        frontierSeq,
		anchorHash:       oldPivotHash,
		targetSeq:        targetSeq,
		targetHash:       targetHash,
		entries:          make(map[uint32]*standardReplayEntry),
		progressSampleAt: started,
		sampleAnchorSeq:  frontierSeq,
		stalledSamples:   standardReplayStallWindows - 1,
	}
	r.catchupReplay.acquisitionMu.Unlock()
	require.NotNil(t, r.catchupReplay.fetchTracker.Find(obsoleteHash))

	require.True(t, r.catchupReplay.rebootstrapFrozenPivotIfStalled(started.Add(standardReplayProgressWindow)))

	blocker := r.catchupReplay.fetchTracker.Find(obsoleteHash)
	require.NotNil(t, blocker)
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(targetHash))
	assert.Equal(t, uint64(3), r.catchupReplay.standardReplay.generation)
	r.catchupReplay.failInboundAcquisition(blocker)
	r.catchupReplay.retryStandardReplayReplacement(time.Now())
	replacement := r.catchupReplay.fetchTracker.Find(targetHash)
	require.NotNil(t, replacement)
	assert.False(t, replacement.TransactionOnly())
	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.True(t, r.catchupReplay.standardReplay.pivotReady)
	assert.Equal(t, frontierSeq, r.catchupReplay.standardReplay.pivotSeq)
	assert.Equal(t, oldPivotHash, r.catchupReplay.standardReplay.pivotHash)
	replacementGeneration := r.catchupReplay.standardReplay.generation
	assert.False(t, r.completeFrozenPivotAcquisition(&header.LedgerHeader{
		LedgerIndex: frontierSeq,
		Hash:        oldPivotHash,
	}, false))
	assert.Equal(t, replacementGeneration, r.catchupReplay.standardReplay.generation)
	assert.Equal(t, oldPivotHash, r.catchupReplay.standardReplay.pivotHash)
}

func TestPendingConsensusLedgerReportsBlockedStart(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	targetHash := [32]byte{0xA4}
	r.catchupReplay.consensusRecovery.targetHash = targetHash
	r.catchupReplay.catchupFailures = make(map[[32]byte]time.Time)
	r.catchupReplay.catchupFailures[targetHash] = time.Now().Add(time.Minute)

	require.False(t, r.catchupReplay.armPendingConsensusLedger())
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(targetHash))
	assert.Equal(t, consensusRecovery{targetHash: targetHash}, r.catchupReplay.consensusRecovery)
}

func TestFrozenPivotRecoveryBackpressuresAtPreparedCapacity(t *testing.T) {
	r, _, svc := makeProvisionalWarmRouter(t)
	var logs bytes.Buffer
	r.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	r.catchupReplay.logger = r.logger
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xd4}
	r.catchupReplay.standardReplay.backpressured = true
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	require.False(t, r.catchupReplay.standardReplay.backpressured)
	pivotAcquisition := r.catchupReplay.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivotAcquisition)
	generation := r.catchupReplay.standardReplay.generation
	parentHash := pivotHash

	for offset := uint32(1); offset <= standardReplayPreparedLimit+1; offset++ {
		seq := pivotSeq + offset
		var hash [32]byte
		hash[0] = 0xd5
		binary.BigEndian.PutUint32(hash[len(hash)-4:], seq)
		r.catchupReplay.recordSeqHash(seq, hash, parentHash, true)
		trackCatchupPeer(r, 7, seq, hash)
		r.catchupReplay.recordValidationCatchupTarget(seq, hash, 7, catchupSourceQuorum)
		require.True(t, r.catchupReplay.continueFrozenPivotRecovery(seq, hash, 7))

		r.catchupReplay.acquisitionMu.Lock()
		for _, entry := range r.catchupReplay.standardReplay.entries {
			if entry.acquisition == nil {
				continue
			}
			require.True(t, r.catchupReplay.fetchTracker.DiscardExpected(entry.acquisition))
			entry.acquisition = nil
			entry.durable = true
		}
		r.catchupReplay.acquisitionMu.Unlock()
		parentHash = hash
	}

	assert.False(t, r.catchupReplay.rebootstrapFrozenPivotIfStalled(time.Now()))
	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.False(t, r.catchupReplay.standardReplay.pivotReady)
	assert.Equal(t, generation, r.catchupReplay.standardReplay.generation)
	assert.Equal(t, pivotSeq, r.catchupReplay.standardReplay.pivotSeq)
	assert.Equal(t, pivotHash, r.catchupReplay.standardReplay.pivotHash)
	assert.Same(t, pivotAcquisition, r.catchupReplay.fetchTracker.Find(pivotHash))
	assert.Len(t, r.catchupReplay.standardReplay.entries, standardReplayPreparedLimit)
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
	r.catchupReplay.engine = r.engine
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, closed)
	head := buildStandardReplayTestChain(t, r, pivot, 1)[0]

	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	generation := r.catchupReplay.standardReplay.generation
	trackCatchupPeer(r, 7, head.seq, head.hash)
	require.True(t, r.catchupReplay.continueFrozenPivotRecovery(head.seq, head.hash, 7))
	completeStandardReplayTestLink(t, r, head)

	r.catchupReplay.acquisitionMu.Lock()
	var tailHash [32]byte
	for offset := uint32(2); offset <= standardReplayPreparedLimit; offset++ {
		seq := pivotSeq + offset
		tailHash = [32]byte{0xe1}
		binary.BigEndian.PutUint32(tailHash[len(tailHash)-4:], seq)
		r.catchupReplay.standardReplay.entries[seq] = &standardReplayEntry{
			generation: r.catchupReplay.standardReplay.generation,
			seq:        seq,
			hash:       tailHash,
			durable:    true,
		}
	}
	r.catchupReplay.standardReplay.collectSeq = pivotSeq + standardReplayPreparedLimit
	r.catchupReplay.standardReplay.collectHash = tailHash
	r.catchupReplay.acquisitionMu.Unlock()

	nextSeq := pivotSeq + standardReplayPreparedLimit + 1
	nextHash := [32]byte{0xe2}
	r.catchupReplay.recordSeqHash(nextSeq, nextHash, tailHash, true)
	trackCatchupPeer(r, 7, nextSeq, nextHash)
	r.catchupReplay.recordValidationCatchupTarget(nextSeq, nextHash, 7, catchupSourceQuorum)
	require.True(t, r.catchupReplay.continueFrozenPivotRecovery(nextSeq, nextHash, 7))
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(nextHash))
	assert.Len(t, r.catchupReplay.standardReplay.entries, standardReplayPreparedLimit)

	pivotAcquisition := r.catchupReplay.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivotAcquisition)
	storeRecoveryLedger(t, svc, pivot)
	require.True(t, r.catchupReplay.fetchTracker.RemoveExpectedWithSnapshot(
		pivotAcquisition, pivotAcquisition.Snapshot(), true,
	))
	pivotHeader := pivot.Header()
	require.True(t, r.completeFrozenPivotAcquisition(&pivotHeader, false))

	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.True(t, r.catchupReplay.standardReplay.pivotReady)
	assert.Equal(t, generation, r.catchupReplay.standardReplay.generation)
	assert.Equal(t, pivotSeq, r.catchupReplay.standardReplay.pivotSeq)
	assert.Equal(t, head.seq, r.catchupReplay.standardReplay.anchorSeq)
	assert.Len(t, r.catchupReplay.standardReplay.entries, standardReplayPreparedLimit)
	next := r.catchupReplay.standardReplay.entries[nextSeq]
	require.NotNil(t, next)
	assert.NotNil(t, next.acquisition)
	assert.Same(t, next.acquisition, r.catchupReplay.fetchTracker.Find(nextHash))
	assert.Equal(t, uint64(1), r.FastSyncMetrics().ReplayPipelineApplied)
}

func TestFrozenPivotRecoveryReplaysToMovingTrustedHead(t *testing.T) {
	r, _, svc := makeProvisionalWarmRouter(t)
	engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
	r.engine = engine
	r.catchupReplay.engine = r.engine
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, closed)
	links := buildStandardReplayTestChain(t, r, pivot, 3)

	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	released := 0
	r.catchupReplay.standardReplay.baseRelease = func() { released++ }
	initialHead := links[len(links)-1]
	trackCatchupPeer(r, 7, initialHead.seq, initialHead.hash)
	r.catchupReplay.recordValidationCatchupTarget(initialHead.seq, initialHead.hash, 7, catchupSourceQuorum)
	require.True(t, r.catchupReplay.continueFrozenPivotRecovery(initialHead.seq, initialHead.hash, 7))
	for _, link := range links {
		completeStandardReplayTestLink(t, r, link)
	}

	movingLinks := buildStandardReplayTestChain(t, r, initialHead.ledger, 2)
	trustedHead := movingLinks[len(movingLinks)-1]
	trackCatchupPeer(r, 7, trustedHead.seq, trustedHead.hash)
	r.catchupReplay.recordValidationCatchupTarget(trustedHead.seq, trustedHead.hash, 7, catchupSourceQuorum)
	require.True(t, r.catchupReplay.continueFrozenPivotRecovery(trustedHead.seq, trustedHead.hash, 7))
	for _, link := range movingLinks {
		completeStandardReplayTestLink(t, r, link)
	}
	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.standardReplay.backpressured = true
	r.catchupReplay.acquisitionMu.Unlock()

	pivotAcquisition := r.catchupReplay.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivotAcquisition)
	storeRecoveryLedger(t, svc, pivot)
	require.True(t, r.catchupReplay.fetchTracker.RemoveExpectedWithSnapshot(
		pivotAcquisition, pivotAcquisition.Snapshot(), true,
	))
	pivotHeader := pivot.Header()
	require.True(t, r.completeFrozenPivotAcquisition(&pivotHeader, false))

	wantApplied := uint64(len(links) + len(movingLinks))
	for r.FastSyncMetrics().ReplayPipelineApplied < wantApplied {
		select {
		case <-r.catchupReplay.standardReplayDrainWake:
			r.catchupReplay.drainStandardReplayPipeline()
		default:
			require.FailNow(t, "replay apply batch did not reschedule through the router loop")
		}
	}

	assert.False(t, r.catchupReplay.standardReplay.active)
	assert.Equal(t, 1, released)
	assert.False(t, r.catchupReplay.standardReplay.backpressured)
	assert.Equal(t, wantApplied, r.FastSyncMetrics().ReplayPipelineApplied)
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
	r.catchupReplay.standardReplay = standardReplayPipeline{
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
	r.catchupReplay.standardReplay.anchorSeq = 110
	r.catchupReplay.standardReplay.targetSeq = 210

	assert.False(t, r.catchupReplay.rebootstrapFrozenPivotIfStalled(started.Add(standardReplayProgressWindow)))
	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.Zero(t, r.catchupReplay.standardReplay.stalledSamples)
	assert.Equal(t, uint64(5), r.catchupReplay.standardReplay.generation)
}

func TestFrozenPivotRecoveryDoesNotTimeoutPivotDownload(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	pivotHash := [32]byte{0xf1}
	trackCatchupPeer(r, 7, 100, pivotHash)
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(100, pivotHash, 7))
	generation := r.catchupReplay.standardReplay.generation
	pivot := r.catchupReplay.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivot)

	assert.False(t, r.catchupReplay.rebootstrapFrozenPivotIfStalled(time.Now().Add(24*time.Hour)))
	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.Equal(t, generation, r.catchupReplay.standardReplay.generation)
	assert.Same(t, pivot, r.catchupReplay.fetchTracker.Find(pivotHash))
}

func TestFrozenPivotBootstrapFailureRearmsTrustedTarget(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	base := svc.GetValidatedLedger()
	svc.SetValidatedLedgerAgeClock(func() time.Time { return base.CloseTime().Add(90 * time.Second) })
	pivot := completedCatchUpAcquisition(t, svc.GetClosedLedgerIndex()+10)
	pivotSeq, pivotHash := pivot.Seq(), pivot.Hash()
	r.catchupReplay.fetchTracker.Track(pivot)
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))

	replacement := completedCatchUpAcquisition(t, pivotSeq+10)
	trackCatchupPeer(r, 7, replacement.Seq(), replacement.Hash())
	r.catchupReplay.recordValidationCatchupTarget(
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

	r.catchupReplay.completeInboundLedger(pivot)

	assert.Nil(t, r.catchupReplay.fetchTracker.Find(pivotHash))
	assert.False(t, r.catchupReplay.standardReplay.active)
	assert.Equal(t, replacement.Seq(), r.catchupReplay.catchup.seq)
	assert.Equal(t, replacement.Hash(), r.catchupReplay.catchup.hash)
	require.Len(t, rs.headerRequests(), 1)
	assert.Equal(t, replacement.Seq(), rs.headerRequests()[0].seq)
	assert.Equal(t, replacement.Hash(), rs.headerRequests()[0].hash)
	assert.Empty(t, rs.legacyCalls())
}

func TestFrozenPivotHandoffKeepsSessionWhenTargetAdvances(t *testing.T) {
	r, a, _, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	_, pivot, pivotHash, pivotSeq := buildSuccessorAgainstParent(t, closed)
	links := buildStandardReplayTestChain(t, r, pivot, 1)
	r.catchupReplay.recordSeqHash(pivotSeq, pivotHash, closed.Hash(), true)
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.consensusRecovery.targetHash = pivotHash
	r.catchupReplay.acquisitionMu.Unlock()
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	pivotAcquisition := r.catchupReplay.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivotAcquisition)
	generation := r.catchupReplay.standardReplay.generation

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
	storeRecoveryLedger(t, svc, pivot)
	require.True(t, r.catchupReplay.fetchTracker.RemoveExpectedWithSnapshot(
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
	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.Equal(t, generation, r.catchupReplay.standardReplay.generation)
	assert.Equal(t, pivotHash, r.catchupReplay.standardReplay.pivotHash)
	next := r.catchupReplay.fetchTracker.Find(links[0].hash)
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

func (r *Router) completeFrozenPivotAcquisition(h *header.LedgerHeader, initialCandidate bool) bool {
	if h == nil {
		return false
	}
	r.catchupReplay.acquisitionMu.Lock()
	acquisition := r.catchupReplay.fetchTracker.Find(h.Hash)
	if acquisition == nil {
		acquisition = inbound.New(h.Hash, h.LedgerIndex, 0, r.logger)
	}
	handoff, claimed := r.catchupReplay.claimStandardReplayPivotHandoffLocked(acquisition)
	r.catchupReplay.acquisitionMu.Unlock()
	return claimed && r.catchupReplay.completeFrozenPivotAcquisitionOwned(h, initialCandidate, handoff)
}

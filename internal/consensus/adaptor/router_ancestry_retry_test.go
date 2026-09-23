package adaptor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHeaderDiscoveryRepairsEmptyReplayPipelineWithoutAdvancingBase(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	base := svc.GetClosedLedger()
	first := buildAlternativeReplaySuccessor(t, base, time.Second)
	second := buildAlternativeReplaySuccessor(t, first.ledger, time.Second)
	newer := buildAlternativeReplaySuccessor(t, second.ledger, time.Second)
	trackCatchupPeer(r, 8, newer.seq)
	r.standardReplay = standardReplayPipeline{
		active: true, pivotReady: true, generation: 9,
		pivotSeq: base.Sequence(), pivotHash: base.Hash(),
		anchorSeq: base.Sequence(), anchorHash: base.Hash(),
		collectSeq: base.Sequence(), collectHash: base.Hash(),
		targetSeq: second.seq, targetHash: second.hash,
		entries:          make(map[uint32]*standardReplayEntry),
		progressSampleAt: time.Now().Add(-3 * time.Minute),
		sampleAnchorSeq:  base.Sequence(), stalledSamples: standardReplayStallWindows,
	}
	startTestHeaderDiscovery(t, r, base.Sequence(), second, 7, catchupSourceQuorum)
	r.headerDiscovery.deadline = time.Now().Add(-time.Second)
	r.tickHeaderDiscovery(time.Now())
	require.True(t, r.headerDiscovery.terminal)
	require.False(t, r.headerDiscovery.repairAfter.IsZero())
	// The stall watchdog must let bounded linkage repair finish first.
	require.False(t, r.rebootstrapFrozenPivotIfStalled(time.Now()))
	require.EqualValues(t, 9, r.standardReplay.generation)
	require.Empty(t, sender.legacyCalls())

	r.recordValidationCatchupTarget(newer.seq, newer.hash, 8, catchupSourceQuorum)
	require.True(t, r.startHeaderParentDiscovery(base, newer.seq, newer.hash, 8, catchupSourceQuorum))
	require.Len(t, sender.headerRequests(), 1, "cooldown must prevent a request storm")
	r.headerDiscovery.repairAfter = time.Now().Add(-time.Second)
	r.tickHeaderDiscovery(time.Now())
	require.False(t, r.headerDiscovery.terminal)
	require.EqualValues(t, 1, r.headerDiscovery.repairRound)
	require.Equal(t, second.hash, r.headerDiscovery.targetHash, "repair must not chase the moving head")
	peer := r.headerDiscovery.peerID
	require.NotZero(t, peer)
	sendTestHeaderReply(t, r, peer, second)
	sendTestHeaderReply(t, r, peer, first)
	entry, found := r.lookupSeqHash(first.seq)
	require.True(t, found)
	require.Equal(t, base.Hash(), entry.parentHash)
	acquisition := r.fetchTracker.Find(first.hash)
	require.NotNil(t, acquisition, "repaired ancestry must refill the empty replay pipeline")
	require.True(t, acquisition.TransactionOnly())
	require.EqualValues(t, 9, r.standardReplay.generation)
	completeStandardReplayTestLink(t, r, first)
	completeStandardReplayTestLink(t, r, second)
	stored, err := svc.GetLedgerByHash(second.hash)
	require.NoError(t, err)
	require.NotNil(t, stored, "recovery must actually apply the repaired successors")
	require.Equal(t, second.hash, stored.Hash())
	require.Zero(t, r.FastSyncMetrics().ReplayPipelineFallbacks)
}

func TestHeaderDiscoveryRepairBudgetCannotBeResetByMovingTarget(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	first := buildAlternativeReplaySuccessor(t, base, time.Second)
	newer := buildAlternativeReplaySuccessor(t, first.ledger, time.Second)
	trackCatchupPeer(r, 8, newer.seq)
	startTestHeaderDiscovery(t, r, base.Sequence(), first, 7, catchupSourceQuorum)
	r.recordValidationCatchupTarget(newer.seq, newer.hash, 8, catchupSourceQuorum)
	for round := 0; round <= headerDiscoveryMaxRepairs; round++ {
		r.headerDiscovery.deadline = time.Now().Add(-time.Second)
		r.tickHeaderDiscovery(time.Now())
		require.True(t, r.headerDiscovery.terminal)
		if round == headerDiscoveryMaxRepairs {
			require.True(t, r.headerDiscovery.repairAfter.IsZero())
			require.False(t, r.startHeaderParentDiscovery(base, newer.seq, newer.hash, 8, catchupSourceQuorum))
			require.False(t, r.headerDiscoveryRepairPending(time.Now()))
			break
		}
		r.headerDiscovery.repairAfter = time.Now().Add(-time.Second)
		require.True(t, r.startHeaderParentDiscovery(base, newer.seq, newer.hash, 8, catchupSourceQuorum))
		require.Equal(t, first.hash, r.headerDiscovery.targetHash)
	}
	require.Len(t, sender.headerRequests(), 1+headerDiscoveryMaxRepairs)
}

func TestHeaderDiscoveryConflictDoesNotReceiveRepairBudget(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	target := buildAlternativeReplaySuccessor(t, base, time.Second)
	startTestHeaderDiscovery(t, r, base.Sequence(), target, 7, catchupSourceQuorum)
	r.failHeaderDiscovery(r.headerDiscovery.generation, errHeaderDiscoveryConflict, 7, errHeaderDiscoveryConflict)
	require.True(t, r.headerDiscovery.repairAfter.IsZero())
	require.False(t, r.startHeaderParentDiscovery(base, target.seq, target.hash, 7, catchupSourceQuorum))
	require.False(t, r.headerDiscoveryRepairPending(time.Now()))
}

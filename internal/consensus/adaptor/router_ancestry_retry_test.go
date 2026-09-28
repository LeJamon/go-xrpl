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
	r.catchupReplay.standardReplay = standardReplayPipeline{
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
	r.catchupReplay.headerDiscovery.deadline = time.Now().Add(-time.Second)
	r.catchupReplay.tickHeaderDiscovery(time.Now())
	require.True(t, r.catchupReplay.headerDiscovery.terminal)
	require.False(t, r.catchupReplay.headerDiscovery.repairAfter.IsZero())
	// The stall watchdog must let bounded linkage repair finish first.
	require.False(t, r.catchupReplay.rebootstrapFrozenPivotIfStalled(time.Now()))
	require.EqualValues(t, 9, r.catchupReplay.standardReplay.generation)
	require.Empty(t, sender.legacyCalls())

	r.catchupReplay.recordValidationCatchupTarget(newer.seq, newer.hash, 8, catchupSourceQuorum)
	require.True(t, r.catchupReplay.startHeaderParentDiscovery(base, newer.seq, newer.hash, 8, catchupSourceQuorum))
	require.Len(t, sender.headerRequests(), 1, "cooldown must prevent a request storm")
	r.catchupReplay.headerDiscovery.repairAfter = time.Now().Add(-time.Second)
	r.catchupReplay.tickHeaderDiscovery(time.Now())
	require.False(t, r.catchupReplay.headerDiscovery.terminal)
	require.EqualValues(t, 1, r.catchupReplay.headerDiscovery.repairRound)
	require.Equal(t, second.hash, r.catchupReplay.headerDiscovery.targetHash, "repair must not chase the moving head")
	peer := r.catchupReplay.headerDiscovery.peerID
	require.NotZero(t, peer)
	sendTestHeaderReply(t, r, peer, second)
	sendTestHeaderReply(t, r, peer, first)
	entry, found := r.catchupReplay.lookupSeqHash(first.seq)
	require.True(t, found)
	require.Equal(t, base.Hash(), entry.parentHash)
	acquisition := r.catchupReplay.fetchTracker.Find(first.hash)
	require.NotNil(t, acquisition, "repaired ancestry must refill the empty replay pipeline")
	require.True(t, acquisition.TransactionOnly())
	require.EqualValues(t, 9, r.catchupReplay.standardReplay.generation)
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
	r.catchupReplay.recordValidationCatchupTarget(newer.seq, newer.hash, 8, catchupSourceQuorum)
	for round := 0; round <= headerDiscoveryMaxRepairs; round++ {
		r.catchupReplay.headerDiscovery.deadline = time.Now().Add(-time.Second)
		r.catchupReplay.tickHeaderDiscovery(time.Now())
		require.True(t, r.catchupReplay.headerDiscovery.terminal)
		if round == headerDiscoveryMaxRepairs {
			require.True(t, r.catchupReplay.headerDiscovery.repairAfter.IsZero())
			require.False(t, r.catchupReplay.startHeaderParentDiscovery(base, newer.seq, newer.hash, 8, catchupSourceQuorum))
			require.False(t, r.catchupReplay.headerDiscoveryRepairPending(time.Now()))
			break
		}
		r.catchupReplay.headerDiscovery.repairAfter = time.Now().Add(-time.Second)
		require.True(t, r.catchupReplay.startHeaderParentDiscovery(base, newer.seq, newer.hash, 8, catchupSourceQuorum))
		require.Equal(t, first.hash, r.catchupReplay.headerDiscovery.targetHash)
	}
	require.Len(t, sender.headerRequests(), 1+headerDiscoveryMaxRepairs)
}

func TestHeaderDiscoveryConflictDoesNotReceiveRepairBudget(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	target := buildAlternativeReplaySuccessor(t, base, time.Second)
	startTestHeaderDiscovery(t, r, base.Sequence(), target, 7, catchupSourceQuorum)
	r.catchupReplay.failHeaderDiscovery(r.catchupReplay.headerDiscovery.generation, errHeaderDiscoveryConflict, 7, errHeaderDiscoveryConflict)
	require.True(t, r.catchupReplay.headerDiscovery.repairAfter.IsZero())
	require.False(t, r.catchupReplay.startHeaderParentDiscovery(base, target.seq, target.hash, 7, catchupSourceQuorum))
	require.False(t, r.catchupReplay.headerDiscoveryRepairPending(time.Now()))
}

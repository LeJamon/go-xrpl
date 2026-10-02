package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPendingFrozenPivotWaitsForConnectedPeerAfterCapacityDeferral(t *testing.T) {
	for _, tc := range []struct {
		name        string
		peerID      peermanagement.PeerID
		admitStatus bool
	}{
		{name: "original peer reconnects", peerID: 7, admitStatus: true},
		{name: "alternative peer connects", peerID: 8, admitStatus: true},
		{name: "alternative peer connects before status", peerID: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, sender, svc := makeProvisionalWarmRouter(t)
			c := r.catchupReplay
			sessions := &testPeerSessions{connected: map[peermanagement.PeerID]bool{7: true}}
			r.setPeerSessionView(sessions)
			occupiedSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
			occupiedHash, targetHash := [32]byte{0xa1}, [32]byte{0xa2}
			targetSeq := occupiedSeq + 2
			trackCatchupPeer(r, 7, targetSeq, targetHash)
			c.startLedgerAcquisitionLegacy(occupiedSeq, occupiedHash, 7)
			require.NotNil(t, c.fetchTracker.Find(occupiedHash))
			c.recordValidationCatchupTarget(targetSeq, targetHash, 7, catchupSourceQuorum)
			generation := c.standardReplay.generation
			require.False(t, c.beginFrozenPivotRecovery(targetSeq, targetHash, 7))
			intent := frozenPivotPendingIntent{seq: targetSeq, hash: targetHash, peerID: 7}
			require.Equal(t, intent, c.pendingFrozenPivot)
			require.Len(t, sender.legacyCalls(), 1)

			sessions.set(7, false)
			r.HandlePeerDisconnect(7)
			c.fetchTracker.Remove(occupiedHash, false)
			require.Empty(t, c.fetchTracker.Active())
			for range 20 {
				r.maintenanceTick()
				require.Empty(t, c.fetchTracker.Active())
				require.Equal(t, generation, c.standardReplay.generation)
				require.False(t, c.standardReplay.active)
				require.Equal(t, intent, c.pendingFrozenPivot)
				require.False(t, c.catchupRetryBlocked(targetHash, time.Now()))
			}
			assert.Len(t, sender.legacyCalls(), 1)
			assert.Empty(t, sender.replayCalls())

			sessions.set(tc.peerID, true)
			sender.mu.Lock()
			sender.acquisitionPeers = []uint64{uint64(tc.peerID)}
			sender.mu.Unlock()
			r.handlePeerConnect(tc.peerID)
			if tc.admitStatus {
				trackCatchupPeer(r, tc.peerID, targetSeq, targetHash)
			} else {
				require.Empty(t, c.peerStates)
			}
			r.maintenanceTick()

			require.True(t, c.standardReplay.active)
			assert.Equal(t, generation+1, c.standardReplay.generation)
			assert.Equal(t, targetHash, c.standardReplay.pivotHash)
			assert.Empty(t, c.pendingFrozenPivot)
			require.NotNil(t, c.fetchTracker.Find(targetHash))
			calls := sender.legacyCalls()
			require.Len(t, calls, 2)
			assert.Equal(t, uint64(tc.peerID), calls[1].peerID)
			assert.Equal(t, targetHash, calls[1].hash)
			assert.Equal(t, targetSeq, calls[1].seq)
		})
	}
}

func TestHashOnlyConsensusAcquisitionUsesPeerWithoutAdmittedStatus(t *testing.T) {
	for _, tc := range []struct {
		name          string
		receiveStatus bool
	}{
		{name: "before status"},
		{name: "status awaiting corroboration", receiveStatus: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, sender, svc := makeRouter(t)
			c := r.catchupReplay
			r.setPeerSessionView(&testPeerSessions{connected: map[peermanagement.PeerID]bool{7: true}})
			sender.acquisitionPeers = []uint64{7}
			r.handlePeerConnect(7)
			hash := [32]byte{0xa4}
			if tc.receiveStatus {
				seq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
				r.handleMessage(statusChangeMessage(t, 7, seq, hash))
				require.Contains(t, c.peerStatusCandidates, peermanagement.PeerID(7))
			}
			require.Empty(t, c.peerStates)
			require.NoError(t, c.requestConsensusLedger(consensus.LedgerID(hash)))

			acquisition := c.fetchTracker.Find(hash)
			require.NotNil(t, acquisition)
			assert.True(t, acquisition.SequenceInitiallyUnknown())
			assert.Zero(t, acquisition.Seq())
			assert.Equal(t, hash, c.consensusRecovery.targetHash)
			assert.Equal(t, hash, c.consensusRecovery.stepHash)
			assert.False(t, c.standardReplay.active)
			assert.Equal(t, []legacyBaseCall{{peerID: 7, hash: hash}}, sender.legacyCalls())
		})
	}
}

func TestPendingFrozenPivotJoinsExistingAcquisitionWithoutPeers(t *testing.T) {
	r, sender, svc := makeProvisionalWarmRouter(t)
	c := r.catchupReplay
	sessions := &testPeerSessions{connected: map[peermanagement.PeerID]bool{7: true}}
	r.setPeerSessionView(sessions)
	seq, hash := svc.GetClosedLedgerIndex()+maxForwardDeltaGap+1, [32]byte{0xa3}
	c.startLedgerAcquisitionLegacy(seq, hash, 7)
	existing := c.fetchTracker.Find(hash)
	require.NotNil(t, existing)
	c.acquisitionMu.Lock()
	c.rememberFrozenPivotPendingLocked(seq, hash, 7)
	c.acquisitionMu.Unlock()
	generation := c.standardReplay.generation
	sessions.set(7, false)
	r.HandlePeerDisconnect(7)

	require.True(t, c.retryPendingFrozenPivot())
	assert.Same(t, existing, c.fetchTracker.Find(hash))
	assert.Equal(t, generation+1, c.standardReplay.generation)
	assert.True(t, c.standardReplay.active)
	assert.Equal(t, hash, c.standardReplay.pivotHash)
	assert.Empty(t, c.pendingFrozenPivot)
	assert.Len(t, sender.legacyCalls(), 1)
}

func TestPendingFrozenPivotUsesCompleteLocalLedgerWithoutPeers(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	c := r.catchupReplay
	r.setPeerSessionView(&testPeerSessions{connected: make(map[peermanagement.PeerID]bool)})
	_, candidate, hash, seq := buildSuccessorAgainstParent(t, svc.GetClosedLedger())
	storeRecoveryLedger(t, svc, candidate)
	_, _, _, complete := c.localReplayReplacementCandidate(seq, hash)
	require.True(t, complete)
	c.acquisitionMu.Lock()
	c.rememberFrozenPivotPendingLocked(seq, hash, 7)
	c.acquisitionMu.Unlock()
	generation := c.standardReplay.generation

	assert.False(t, c.retryPendingFrozenPivot())
	assert.Empty(t, c.pendingFrozenPivot)
	assert.Empty(t, c.fetchTracker.Active())
	assert.Equal(t, generation, c.standardReplay.generation)
	assert.Empty(t, sender.legacyCalls())
	assert.Empty(t, sender.replayCalls())
}

package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sendReplayAvailabilityResponse(t *testing.T, r *Router, peerID uint64, hash [32]byte, reply message.ReplyError) {
	t.Helper()
	payload, err := message.Encode(&message.ReplayDeltaResponse{
		LedgerHash: hash[:],
		Error:      reply,
	})
	require.NoError(t, err)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  peermanagement.PeerID(peerID),
		Type:    message.TypeReplayDeltaResponse,
		Payload: payload,
	})
}

func TestRouter_ReplayDeltaAvailabilityRetriesSuitablePeer(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{8}
	sender.mu.Unlock()

	target := [32]byte{0xA1}
	seq := parent.Sequence() + 1
	require.NoError(t, r.catchupReplay.startReplayDeltaAcquisition(seq, target, 7, parent))
	sendReplayAvailabilityResponse(t, r, 7, target, message.ReplyErrorNoLedger)

	assert.Equal(t, []replayDeltaCall{
		{peerID: 7, hash: target},
		{peerID: 8, hash: target},
	}, sender.replayCalls())
	assert.Empty(t, sender.legacyCalls(), "a suitable replay peer should be retried before standard replay")
	assert.True(t, r.catchupReplay.replayer.Has(target), "the alternative peer should own the replay acquisition")
	r.catchupReplay.acquisitionMu.Lock()
	_, retained := r.catchupReplay.replayAvailabilityRetries[target]
	r.catchupReplay.acquisitionMu.Unlock()
	assert.True(t, retained, "bounded retry state should survive while the alternative is in flight")

	r.catchupReplay.replayer.Abandon(target)
	r.catchupReplay.expireReplayAvailabilityRetries()
	r.catchupReplay.acquisitionMu.Lock()
	_, retained = r.catchupReplay.replayAvailabilityRetries[target]
	r.catchupReplay.acquisitionMu.Unlock()
	assert.False(t, retained, "retry state should be reaped after the replay is abandoned")
}

func TestRouter_ReplayDeltaAvailabilitySkipsPeerWithoutReplaySupport(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{8, 9}
	sender.peerReplaySupport = map[uint64]bool{8: false, 9: true}
	sender.mu.Unlock()

	target := [32]byte{0xA5}
	seq := parent.Sequence() + 1
	require.NoError(t, r.catchupReplay.startReplayDeltaAcquisition(seq, target, 7, parent))
	sendReplayAvailabilityResponse(t, r, 7, target, message.ReplyErrorNoLedger)

	assert.Equal(t, []replayDeltaCall{
		{peerID: 7, hash: target},
		{peerID: 9, hash: target},
	}, sender.replayCalls())
	assert.Empty(t, sender.legacyCalls())
}

func TestRouter_ReplayDeltaAvailabilityIgnoresDelayedPriorPeer(t *testing.T) {
	r, sender := makeRouterWithBadDataRecorder(t)
	svc := r.adaptor.LedgerService()
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	resp, target, seq := buildEmptyClosedSuccessorResponse(t, svc)

	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{8}
	sender.mu.Unlock()
	require.NoError(t, r.catchupReplay.startReplayDeltaAcquisition(seq, target, 7, parent))
	sendReplayAvailabilityResponse(t, r, 7, target, message.ReplyErrorNoLedger)

	assert.Equal(t, []replayDeltaCall{
		{peerID: 7, hash: target},
		{peerID: 8, hash: target},
	}, sender.replayCalls())

	// The first peer's delayed availability reply must not displace or
	// abandon the active replacement acquisition.
	sendReplayAvailabilityResponse(t, r, 7, target, message.ReplyErrorNoNode)
	assert.Equal(t, []replayDeltaCall{
		{peerID: 7, hash: target},
		{peerID: 8, hash: target},
	}, sender.replayCalls())
	assert.True(t, r.catchupReplay.replayer.Has(target))
	charges := []badDataCall{
		{peerID: 7, reason: "replay-delta-verify"},
		{peerID: 7, reason: "replay-delta-verify"},
	}
	assert.Equal(t, charges, sender.getBadDataCalls())

	payload, err := message.Encode(resp)
	require.NoError(t, err)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  8,
		Type:    message.TypeReplayDeltaResponse,
		Payload: payload,
	})

	assert.Zero(t, r.catchupReplay.replayer.Count(), "the replacement peer's valid response must remain usable")
	stored, err := svc.GetLedgerByHash(target)
	require.NoError(t, err)
	assert.NotNil(t, stored)
	assert.Equal(t, charges, sender.getBadDataCalls())
}

func TestRouter_ReplayDeltaAvailabilityFallsBackToTransactionReplayAfterBound(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{8, 9, 10, 11}
	sender.mu.Unlock()

	target := [32]byte{0xA2}
	seq := parent.Sequence() + 1
	require.NoError(t, r.catchupReplay.startReplayDeltaAcquisition(seq, target, 7, parent))
	for _, peerID := range []uint64{7, 8, 9, 10} {
		sendReplayAvailabilityResponse(t, r, peerID, target, message.ReplyErrorNoNode)
	}

	assert.Equal(t, []replayDeltaCall{
		{peerID: 7, hash: target},
		{peerID: 8, hash: target},
		{peerID: 9, hash: target},
		{peerID: 10, hash: target},
	}, sender.replayCalls(), "availability retries must remain bounded")
	legacy := sender.legacyCalls()
	require.NotEmpty(t, legacy)
	for _, call := range legacy {
		assert.Equal(t, target, call.hash)
		assert.Equal(t, seq, call.seq)
	}
	il := r.catchupReplay.fetchTracker.Find(target)
	require.NotNil(t, il)
	assert.True(t, il.TransactionOnly(), "a verified parent permits tx-only replay")
	assert.Zero(t, r.catchupReplay.replayer.Count())
	r.catchupReplay.acquisitionMu.Lock()
	_, retained := r.catchupReplay.replayAvailabilityRetries[target]
	r.catchupReplay.acquisitionMu.Unlock()
	assert.False(t, retained, "retry state must be cleared after bounded fallback")
}

func TestRouter_ReplayDeltaAvailabilityExpiredBudgetDoesNotRestart(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{8}
	sender.mu.Unlock()

	target := [32]byte{0xA4}
	seq := parent.Sequence() + 1
	require.NoError(t, r.catchupReplay.startReplayDeltaAcquisition(seq, target, 7, parent))
	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.replayAvailabilityRetries = map[[32]byte]replayAvailabilityRetryState{
		target: {
			peers:     []uint64{7},
			retries:   1,
			expiresAt: time.Now().Add(-time.Second),
		},
	}
	r.catchupReplay.acquisitionMu.Unlock()

	// An active replacement keeps its expired marker until the response
	// arrives, preventing maintenance from opening a fresh retry budget.
	r.catchupReplay.expireReplayAvailabilityRetries()
	r.catchupReplay.acquisitionMu.Lock()
	_, retained := r.catchupReplay.replayAvailabilityRetries[target]
	r.catchupReplay.acquisitionMu.Unlock()
	require.True(t, retained)

	sendReplayAvailabilityResponse(t, r, 7, target, message.ReplyErrorNoLedger)

	assert.Equal(t, []replayDeltaCall{{peerID: 7, hash: target}}, sender.replayCalls(), "an expired budget must not issue another replay request")
	legacy := sender.legacyCalls()
	require.NotEmpty(t, legacy)
	il := r.catchupReplay.fetchTracker.Find(target)
	require.NotNil(t, il)
	assert.True(t, il.TransactionOnly())
	r.catchupReplay.acquisitionMu.Lock()
	_, retained = r.catchupReplay.replayAvailabilityRetries[target]
	r.catchupReplay.acquisitionMu.Unlock()
	assert.False(t, retained)
}

func TestRouter_ReplayDeltaAvailabilityChargesPeerAndPreservesRecovery(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply message.ReplyError
	}{
		{name: "no node", reply: message.ReplyErrorNoNode},
		{name: "no ledger", reply: message.ReplyErrorNoLedger},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, sender := makeRouterWithBadDataRecorder(t)
			svc := r.adaptor.LedgerService()
			parent := svc.GetClosedLedger()
			require.NotNil(t, parent)

			target := [32]byte{0xA3}
			seq := parent.Sequence() + 1
			require.NoError(t, r.catchupReplay.startReplayDeltaAcquisition(seq, target, 7, parent))
			sendReplayAvailabilityResponse(t, r, 7, target, tc.reply)

			assert.Equal(t, []badDataCall{{peerID: 7, reason: "replay-delta-verify"}}, sender.getBadDataCalls())
			legacy := sender.legacyCalls()
			require.Len(t, legacy, 1)
			il := r.catchupReplay.fetchTracker.Find(target)
			require.NotNil(t, il)
			assert.True(t, il.TransactionOnly())
		})
	}
}

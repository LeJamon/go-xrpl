package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func trackCatchupPeer(r *Router, peerID peermanagement.PeerID, seq uint32) {
	r.peersMu.Lock()
	r.peerStates[peerID] = &peerLedgerState{LedgerSeq: seq}
	r.peersMu.Unlock()
}

func TestRouter_CatchupUsesSameTargetReplacementPeer(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 40
	targetHash := [32]byte{0xD1}

	trackCatchupPeer(r, 1, targetSeq)
	sender.mu.Lock()
	sender.legacyBaseErr = peermanagement.ErrPeerNotFound
	sender.mu.Unlock()
	r.ensureCatchupAcquisition(targetSeq, targetHash, 1)
	require.NotNil(t, r.fetchTracker.Find(targetHash))

	r.HandlePeerDisconnect(1)
	_, _, preferred := r.bestCatchupTarget()
	assert.Zero(t, preferred)

	sender.mu.Lock()
	sender.legacyBaseErr = nil
	sender.mu.Unlock()
	trackCatchupPeer(r, 2, targetSeq)
	r.ensureCatchupAcquisition(targetSeq, targetHash, 2)

	calls := sender.legacyCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, uint64(1), calls[0].peerID)
	assert.Equal(t, uint64(2), calls[1].peerID)
	seq, hash, preferred := r.bestCatchupTarget()
	assert.Equal(t, targetSeq, seq)
	assert.Equal(t, targetHash, hash)
	assert.Equal(t, uint64(2), preferred)
}

func TestRouter_CatchupWaitsWithoutConnectedPeers(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 40
	targetHash := [32]byte{0xD2}
	r.recordCatchupTarget(targetSeq, targetHash, 7)

	for range 20 {
		r.maintenanceTick()
	}

	assert.Empty(t, sender.legacyCalls())
	assert.Empty(t, sender.replayCalls())
	assert.Nil(t, r.fetchTracker.Find(targetHash))
}

func TestRouter_CatchupBaseRequestFailureUsesInboundRetryTimer(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 40
	targetHash := [32]byte{0xD3}
	trackCatchupPeer(r, 7, targetSeq)
	sender.mu.Lock()
	sender.legacyBaseErr = peermanagement.ErrPeerNotFound
	sender.mu.Unlock()

	r.ensureCatchupAcquisition(targetSeq, targetHash, 7)
	il := r.fetchTracker.Find(targetHash)
	require.NotNil(t, il)
	require.Equal(t, inbound.StateWantBase, il.State())

	for range 20 {
		r.maintenanceTick()
	}
	require.Len(t, sender.legacyCalls(), 1)

	now := time.Now().Add(4 * time.Second)
	require.Equal(t, inbound.TimerEscalate, il.OnTimer(now))
	r.escalateAcquisition(il, now)
	require.Len(t, sender.legacyCalls(), 2)
	assert.Equal(t, 1, il.Timeouts())

	for timeout := 2; timeout <= 6; timeout++ {
		now = now.Add(4 * time.Second)
		require.Equal(t, inbound.TimerEscalate, il.OnTimer(now))
		r.escalateAcquisition(il, now)
	}
	now = now.Add(4 * time.Second)
	require.Equal(t, inbound.TimerFailed, il.OnTimer(now))
	r.failInboundAcquisition(il)
	require.Len(t, sender.legacyCalls(), 7)
	assert.Nil(t, r.fetchTracker.Find(targetHash))
	seq, _, _ := r.bestCatchupTarget()
	assert.Equal(t, targetSeq, seq)
	assert.True(t, r.catchupRetryBlocked(targetHash, time.Now()))

	for range 20 {
		r.maintenanceTick()
	}
	assert.Len(t, sender.legacyCalls(), 7)

	sender.mu.Lock()
	sender.legacyBaseErr = nil
	sender.mu.Unlock()
	r.ensureCatchupAcquisition(targetSeq, targetHash, 8)
	assert.Len(t, sender.legacyCalls(), 7)
	assert.Zero(t, r.catchupInFlight())

	r.catchupMu.Lock()
	r.catchupFailures[targetHash] = time.Now().Add(-time.Second)
	r.catchupMu.Unlock()
	r.ensureCatchupAcquisition(targetSeq, targetHash, 8)
	calls := sender.legacyCalls()
	require.Len(t, calls, 8)
	assert.Equal(t, uint64(8), calls[7].peerID)
	assert.False(t, r.catchupRetryBlocked(targetHash, time.Now()))
	assert.Equal(t, 1, r.catchupInFlight())
}

func TestRouter_CatchupUsesReplacementValidationPeer(t *testing.T) {
	r, a, sender, _ := makeRouter(t)
	trusted, err := a.GetValidatorKey()
	require.NoError(t, err)
	v := &consensus.Validation{
		NodeID:    trusted,
		LedgerSeq: 99999,
		LedgerID:  consensus.LedgerID([32]byte{0xD4}),
	}
	sender.mu.Lock()
	sender.legacyBaseErr = peermanagement.ErrPeerNotFound
	sender.mu.Unlock()

	r.maybeAcquireFromValidation(v, 7)
	r.maybeAcquireFromValidation(v, 8)

	calls := sender.legacyCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, uint64(7), calls[0].peerID)
	assert.Equal(t, uint64(8), calls[1].peerID)
	assert.Equal(t, 1, r.catchupInFlight())
}

func TestRouter_CatchupFailureCooldownAppliesToDirectAcquisition(t *testing.T) {
	r, _, sender, _ := makeRouter(t)
	targetHash := [32]byte{0xD5}
	r.markFailedCatchupAcquisition(targetHash)

	r.startLedgerAcquisition(99999, targetHash, 7)

	assert.Empty(t, sender.legacyCalls())
	assert.Empty(t, sender.replayCalls())
	assert.Zero(t, r.catchupInFlight())
}

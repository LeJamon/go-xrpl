package adaptor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testPeerSessions struct {
	mu        sync.RWMutex
	connected map[peermanagement.PeerID]bool
}

func (s *testPeerSessions) IsPeerConnected(peerID peermanagement.PeerID) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.connected[peerID]
}

func (s *testPeerSessions) set(peerID peermanagement.PeerID, connected bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected[peerID] = connected
}

type disconnectingPeerSession struct {
	checks atomic.Uint32
}

func (s *disconnectingPeerSession) IsPeerConnected(peermanagement.PeerID) bool {
	return s.checks.Add(1) == 1
}

func trackCatchupPeer(r *Router, peerID peermanagement.PeerID, seq uint32, hashes ...[32]byte) {
	var hash [32]byte
	if len(hashes) > 0 {
		hash = hashes[0]
	}
	r.peersMu.Lock()
	r.peerStates[peerID] = &peerLedgerState{LedgerSeq: seq, LedgerHash: hash}
	r.peersMu.Unlock()
	if hash != ([32]byte{}) {
		r.adaptor.UpdatePeerLCL(uint64(peerID), consensus.LedgerID(hash))
	}
}

func TestRouter_DropsQueuedStatusAfterPeerDisconnect(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	sessions := &testPeerSessions{connected: map[peermanagement.PeerID]bool{1: true, 2: true}}
	r.setPeerSessionView(sessions)
	targetSeq := svc.GetClosedLedgerIndex() + 40
	targetHash := [32]byte{0xD0}
	queued := statusChangeMessage(t, 1, targetSeq, targetHash)

	sessions.set(1, false)
	r.HandlePeerDisconnect(1)
	trackCatchupPeer(r, 2, targetSeq)
	r.handleMessage(queued)

	r.peersMu.RLock()
	_, restored := r.peerStates[1]
	r.peersMu.RUnlock()
	assert.False(t, restored)
	assert.Empty(t, a.PeerReportedLedgers())
	assert.Empty(t, sender.legacyCalls())
	assert.Empty(t, sender.replayCalls())
	seq, hash, preferred := r.bestCatchupTarget()
	assert.Zero(t, seq)
	assert.Zero(t, hash)
	assert.Zero(t, preferred)
}

func TestRouter_CleansStatusWhenDisconnectRacesDispatch(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	r.setPeerSessionView(&disconnectingPeerSession{})
	targetSeq := svc.GetClosedLedgerIndex() + 40
	targetHash := [32]byte{0xCF}

	r.handleMessage(statusChangeMessage(t, 1, targetSeq, targetHash))

	r.peersMu.RLock()
	_, restored := r.peerStates[1]
	r.peersMu.RUnlock()
	assert.False(t, restored)
	assert.Empty(t, a.PeerReportedLedgers())
	assert.Empty(t, sender.legacyCalls())
	assert.Empty(t, sender.replayCalls())
}

func TestRouter_QueuesDisconnectCleanupOffOverlayLoop(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 40
	targetHash := [32]byte{0xCE}
	il := inbound.New(targetHash, targetSeq, 1, serveTestLogger())
	r.fetchTracker.Track(il)
	for peerID := peermanagement.PeerID(1); peerID <= 128; peerID++ {
		trackCatchupPeer(r, peerID, targetSeq)
	}

	r.peersMu.Lock()
	queued := make(chan struct{})
	go func() {
		for peerID := peermanagement.PeerID(1); peerID <= 128; peerID++ {
			r.queuePeerDisconnect(peerID)
		}
		close(queued)
	}()
	select {
	case <-queued:
	case <-time.After(time.Second):
		r.peersMu.Unlock()
		t.Fatal("queuePeerDisconnect blocked on peer cleanup")
	}
	r.peersMu.Unlock()

	ctx, cancel := context.WithCancel(t.Context())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		r.Run(ctx)
	}()
	require.Eventually(t, func() bool {
		r.peersMu.RLock()
		remaining := len(r.peerStates)
		r.peersMu.RUnlock()
		return remaining == 0 && !containsPeer(il.Peers(), 1)
	}, time.Second, time.Millisecond)
	cancel()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("router did not join peer disconnect cleanup")
	}
}

func TestRouter_CatchupRevalidatesPeerHint(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 40
	targetHash := [32]byte{0xD1}
	sessions := &testPeerSessions{connected: map[peermanagement.PeerID]bool{1: false, 2: true}}
	r.setPeerSessionView(sessions)

	trackCatchupPeer(r, 1, targetSeq, targetHash)
	trackCatchupPeer(r, 2, targetSeq, targetHash)
	r.ensureCatchupAcquisition(targetSeq, targetHash, 1)

	require.Equal(t, []legacyBaseCall{{peerID: 2, hash: targetHash, seq: targetSeq}}, sender.legacyCalls())
	il := r.fetchTracker.Find(targetHash)
	require.NotNil(t, il)
	assert.Equal(t, []uint64{2}, il.Peers())
	seq, hash, preferred := r.bestCatchupTarget()
	assert.Equal(t, targetSeq, seq)
	assert.Equal(t, targetHash, hash)
	assert.Equal(t, uint64(2), preferred)
}

func TestRouter_CatchupDisconnectErrorRetargetsImmediately(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "peer not found", err: peermanagement.ErrPeerNotFound},
		{name: "connection closed", err: peermanagement.ErrConnectionClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, sender, svc := makeRouter(t)
			targetSeq := svc.GetClosedLedgerIndex() + 40
			targetHash := [32]byte{0xD2}
			sessions := &testPeerSessions{connected: map[peermanagement.PeerID]bool{1: true, 2: true}}
			r.setPeerSessionView(sessions)
			trackCatchupPeer(r, 1, targetSeq, targetHash)
			trackCatchupPeer(r, 2, targetSeq, targetHash)
			sender.mu.Lock()
			sender.legacyBaseErrs = map[uint64]error{1: tc.err}
			sender.mu.Unlock()

			r.ensureCatchupAcquisition(targetSeq, targetHash, 1)

			calls := sender.legacyCalls()
			require.Len(t, calls, 2)
			assert.Equal(t, uint64(1), calls[0].peerID)
			assert.Equal(t, uint64(2), calls[1].peerID)
			il := r.fetchTracker.Find(targetHash)
			require.NotNil(t, il)
			assert.Equal(t, []uint64{2}, il.Peers())
			r.peersMu.RLock()
			_, stale := r.peerStates[1]
			r.peersMu.RUnlock()
			assert.False(t, stale)
		})
	}
}

func TestRouter_LegacyAcquisitionSeedsFivePeers(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 40
	targetHash := [32]byte{0xE1}
	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{8, 9, 10, 11, 12}
	sender.mu.Unlock()

	r.startLedgerAcquisitionLegacy(targetSeq, targetHash, 7)

	calls := sender.legacyCalls()
	require.Len(t, calls, acquisitionPeerStart)
	assert.ElementsMatch(t, []uint64{7, 8, 9, 10, 11}, []uint64{
		calls[0].peerID, calls[1].peerID, calls[2].peerID, calls[3].peerID, calls[4].peerID,
	})
	il := r.fetchTracker.Find(targetHash)
	require.NotNil(t, il)
	assert.ElementsMatch(t, []uint64{7, 8, 9, 10, 11}, il.Peers())
}

func TestRouter_LegacyAcquisitionBroadensOnEachNoProgressInterval(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 40
	targetHash := [32]byte{0xE4}
	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{8, 9, 10, 11}
	sender.mu.Unlock()
	r.startLedgerAcquisitionLegacy(targetSeq, targetHash, 7)
	il := r.fetchTracker.Find(targetHash)
	require.NotNil(t, il)
	require.Len(t, il.Peers(), acquisitionPeerStart)

	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{12, 13, 14, 15}
	sender.mu.Unlock()
	r.broadenAcquisitionPeers(il)
	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{15, 16, 17}
	sender.mu.Unlock()
	r.broadenAcquisitionPeers(il)

	assert.ElementsMatch(t, []uint64{7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17}, il.Peers())
}

func TestRouter_LegacyAcquisitionKeepsSuccessfulPeers(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 40
	targetHash := [32]byte{0xE2}
	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{8, 9, 10, 11}
	sender.legacyBaseErrs = map[uint64]error{
		8:  peermanagement.ErrPeerNotFound,
		10: peermanagement.ErrConnectionClosed,
	}
	sender.mu.Unlock()

	r.startLedgerAcquisitionLegacy(targetSeq, targetHash, 7)

	require.Len(t, sender.legacyCalls(), acquisitionPeerStart)
	il := r.fetchTracker.Find(targetHash)
	require.NotNil(t, il)
	assert.ElementsMatch(t, []uint64{7, 9, 11}, il.Peers())
}

func TestRouter_LegacyAcquisitionWaitsWhenEveryPeerDisconnects(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 40
	targetHash := [32]byte{0xE3}
	sender.mu.Lock()
	sender.acquisitionPeers = []uint64{8, 9, 10, 11}
	sender.legacyBaseErrs = map[uint64]error{
		7:  peermanagement.ErrPeerNotFound,
		8:  peermanagement.ErrPeerNotFound,
		9:  peermanagement.ErrPeerNotFound,
		10: peermanagement.ErrPeerNotFound,
		11: peermanagement.ErrPeerNotFound,
	}
	sender.mu.Unlock()

	r.startLedgerAcquisitionLegacy(targetSeq, targetHash, 7)

	require.Len(t, sender.legacyCalls(), acquisitionPeerStart)
	il := r.fetchTracker.Find(targetHash)
	require.NotNil(t, il)
	assert.Empty(t, il.Peers())
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

func TestRouter_CatchupPeerNotFoundWaitsWithoutReplacement(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 40
	targetHash := [32]byte{0xD6}
	sessions := &testPeerSessions{connected: map[peermanagement.PeerID]bool{1: true}}
	r.setPeerSessionView(sessions)
	trackCatchupPeer(r, 1, targetSeq, targetHash)
	sender.mu.Lock()
	sender.legacyBaseErrs = map[uint64]error{1: peermanagement.ErrPeerNotFound}
	sender.mu.Unlock()

	r.ensureCatchupAcquisition(targetSeq, targetHash, 1)
	il := r.fetchTracker.Find(targetHash)
	require.NotNil(t, il)
	assert.Empty(t, il.Peers())
	for range 20 {
		r.maintenanceTick()
	}
	require.Len(t, sender.legacyCalls(), 1)
}

func TestRouter_DisconnectRemovesPeerFromActiveAcquisitions(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	targetHash := [32]byte{0xD7}
	il := inbound.New(targetHash, svc.GetClosedLedgerIndex()+40, 1, serveTestLogger())
	for peerID := uint64(2); peerID <= 8; peerID++ {
		require.True(t, il.AddPeer(peerID))
	}
	r.fetchTracker.Track(il)

	r.HandlePeerDisconnect(1)

	assert.NotContains(t, il.Peers(), uint64(1))
	assert.Len(t, il.Peers(), 7)
	assert.True(t, il.AddPeer(9))
}

func TestRouter_HistoryPeerNotFoundDoesNotHotLoop(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 40
	targetHash := [32]byte{0xD8}
	sessions := &testPeerSessions{connected: map[peermanagement.PeerID]bool{1: true}}
	r.setPeerSessionView(sessions)
	trackCatchupPeer(r, 1, targetSeq, targetHash)
	sender.mu.Lock()
	sender.legacyBaseErrs = map[uint64]error{1: peermanagement.ErrPeerNotFound}
	sender.mu.Unlock()
	r.startHistoryBackfill(targetSeq, targetHash, 1, svc.GetClosedLedgerIndex())

	for range 20 {
		r.maintenanceTick()
	}

	require.Len(t, sender.legacyCalls(), 1)
	il := r.fetchTracker.Find(targetHash)
	require.NotNil(t, il)
	assert.Empty(t, il.Peers())
	r.historyMu.Lock()
	preferred := r.history.peerID
	r.historyMu.Unlock()
	assert.Zero(t, preferred)
}

func TestRouter_GenericAcquisitionRetargetsDisconnectedPeer(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "peer not found", err: peermanagement.ErrPeerNotFound},
		{name: "connection closed", err: peermanagement.ErrConnectionClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, sender, svc := makeRouter(t)
			targetSeq := svc.GetClosedLedgerIndex() + 40
			targetHash := [32]byte{0xD9}
			r.setPeerSessionView(&testPeerSessions{connected: map[peermanagement.PeerID]bool{1: true, 2: true}})
			trackCatchupPeer(r, 1, targetSeq, targetHash)
			trackCatchupPeer(r, 2, targetSeq-1)
			sender.mu.Lock()
			sender.legacyBaseErrs = map[uint64]error{1: tc.err}
			sender.mu.Unlock()

			snap, started, reference := r.RequestLedger(targetHash, targetSeq)

			require.True(t, started)
			assert.False(t, reference)
			require.NotNil(t, snap)
			calls := sender.legacyCalls()
			require.Len(t, calls, 2)
			assert.Equal(t, uint64(1), calls[0].peerID)
			assert.Equal(t, uint64(2), calls[1].peerID)
			il := r.fetchTracker.Find(targetHash)
			require.NotNil(t, il)
			assert.Equal(t, inbound.ReasonGeneric, il.Reason())
			assert.Equal(t, []uint64{2}, il.Peers())
		})
	}
}

func TestRouter_GenericAcquisitionWaitsForReplacementPeer(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 40
	targetHash := [32]byte{0xDA}
	sessions := &testPeerSessions{connected: map[peermanagement.PeerID]bool{1: true}}
	r.setPeerSessionView(sessions)
	trackCatchupPeer(r, 1, targetSeq, targetHash)
	sender.mu.Lock()
	sender.legacyBaseErrs = map[uint64]error{1: peermanagement.ErrPeerNotFound}
	sender.mu.Unlock()

	snap, started, _ := r.RequestLedger(targetHash, targetSeq)
	require.True(t, started)
	require.NotNil(t, snap)
	il := r.fetchTracker.Find(targetHash)
	require.NotNil(t, il)
	assert.Empty(t, il.Peers())
	require.Len(t, sender.legacyCalls(), 1)

	joined, joinedStarted, _ := r.RequestLedger(targetHash, targetSeq)
	assert.True(t, joinedStarted)
	assert.NotNil(t, joined)
	assert.Len(t, sender.legacyCalls(), 1)

	sessions.set(2, true)
	trackCatchupPeer(r, 2, targetSeq, targetHash)
	r.escalateAcquisition(il, time.Now().Add(4*time.Second))

	calls := sender.legacyCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, uint64(2), calls[1].peerID)
	assert.Equal(t, []uint64{2}, il.Peers())
}

func TestRouter_PeerlessAcquisitionSkipsFetchPackEscalation(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	child := svc.GetClosedLedger()
	require.NotNil(t, child)
	parent, err := svc.GetLedgerByHash(child.ParentHash())
	require.NoError(t, err)
	require.NotNil(t, parent)
	il := inbound.NewGeneric(parent.Hash(), parent.Sequence(), 1, serveTestLogger())
	require.NoError(t, il.GotBase(r.buildLedgerBaseNodes(parent)))
	require.True(t, il.RemovePeer(1))

	r.escalateAcquisition(il, time.Now().Add(4*time.Second))

	assert.False(t, il.FetchPackRequested())
}

func containsPeer(peers []uint64, peerID uint64) bool {
	for _, candidate := range peers {
		if candidate == peerID {
			return true
		}
	}
	return false
}

func TestRouter_CatchupBaseRequestFailureUsesInboundRetryTimer(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + 40
	targetHash := [32]byte{0xD3}
	trackCatchupPeer(r, 7, targetSeq, targetHash)
	sender.mu.Lock()
	sender.legacyBaseErr = errors.New("temporary send failure")
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
	sender.legacyBaseErrs = map[uint64]error{7: peermanagement.ErrPeerNotFound}
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

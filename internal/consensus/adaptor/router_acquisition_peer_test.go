package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound/inboundtest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type ledgerAdvertisement struct {
	first, last uint32
	hash        [32]byte
}

type availabilitySender struct {
	*recordingSender
	advertisements map[uint64]ledgerAdvertisement
}

func (s *availabilitySender) PeerLedgerAvailability(peerID uint64, hash [32]byte, seq uint32) (bool, uint32, uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ad := s.advertisements[peerID]
	has := hash != ([32]byte{}) && hash == ad.hash || seq != 0 && ad.first != 0 && seq >= ad.first && seq <= ad.last
	return has, ad.first, ad.last
}

func setPeerAdvertisements(r *Router, sender *recordingSender, advertisements map[uint64]ledgerAdvertisement) {
	network := &availabilitySender{recordingSender: sender, advertisements: advertisements}
	r.acquisition = network
	r.catchupReplay.acquisition = network
}

func TestRouter_ValidationRelayAvailability(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "standard serving peer", true: "replay serving peer"}[replay], func(t *testing.T) {
			r, a, sender, svc := makeRouter(t)
			parent := svc.GetClosedLedger()
			_, _, hash, seq := buildSuccessorAgainstParent(t, parent)
			trackCatchupPeer(r, 7, parent.Sequence())
			trackCatchupPeer(r, 8, seq, hash)
			setPeerAdvertisements(r, sender, map[uint64]ledgerAdvertisement{
				7: {first: 1, last: parent.Sequence()},
				8: {first: 1, last: seq, hash: hash},
			})
			sender.peerReplaySupport = map[uint64]bool{7: true, 8: replay}
			sender.availableReplayPeers = []uint64{7}
			sender.acquisitionPeers = []uint64{7, 8}
			r.catchupReplay.recordAcquiredSeqHash(seq, hash, parent.Hash())
			trusted, err := a.GetValidatorKey()
			require.NoError(t, err)
			v := &consensus.Validation{NodeID: trusted, LedgerSeq: seq, LedgerID: consensus.LedgerID(hash)}
			r.catchupReplay.maybeAcquireFromValidation(v, 7)
			r.catchupReplay.maybeAcquireFromValidation(v, 7)
			if replay {
				require.Equal(t, []replayDeltaCall{{peerID: 8, hash: hash}}, sender.replayCalls())
				assert.Empty(t, sender.legacyCalls())
			} else {
				assert.Empty(t, sender.replayCalls())
				require.NotEmpty(t, sender.legacyCalls())
				assert.Equal(t, uint64(8), sender.legacyCalls()[0].peerID)
				il := r.catchupReplay.fetchTracker.Find(hash)
				require.NotNil(t, il)
				assert.True(t, il.TransactionOnly())
			}
		})
	}
}

func TestRouter_AcquisitionAvailabilityRanking(t *testing.T) {
	hash := [32]byte{0xb1}
	for _, tc := range []struct {
		name string
		ads  map[uint64]ledgerAdvertisement
		want uint64
	}{
		{"lagging preferred", map[uint64]ledgerAdvertisement{7: {first: 1, last: 99}, 8: {first: 1, last: 100}}, 8},
		{"pruned preferred", map[uint64]ledgerAdvertisement{7: {first: 101, last: 110}, 8: {first: 1, last: 100}}, 8},
		{"recent hash outside range", map[uint64]ledgerAdvertisement{7: {first: 1, last: 99, hash: hash}, 8: {first: 1, last: 100}}, 7},
		{"unknown preferred", map[uint64]ledgerAdvertisement{8: {first: 1, last: 100}}, 8},
		{"unknown beats lagging", map[uint64]ledgerAdvertisement{7: {first: 1, last: 99}}, 8},
		{"all lagging remain usable", map[uint64]ledgerAdvertisement{7: {first: 1, last: 99}, 8: {first: 1, last: 98}}, 7},
		{"unknown remain usable", nil, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, sender, _ := makeRouter(t)
			sender.acquisitionPeers = []uint64{7, 8}
			setPeerAdvertisements(r, sender, tc.ads)
			peer, ok := r.catchupReplay.resolveAcquisitionPeer(100, hash, 7)
			require.True(t, ok)
			assert.Equal(t, tc.want, peer)
		})
	}
}

func TestRouter_LaggingAdvertisementsAllowVerifiedRecovery(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	link := buildStandardReplayTestChain(t, r, parent, 1)[0]
	trackCatchupPeer(r, 7, parent.Sequence(), parent.Hash())
	setPeerAdvertisements(r, sender, map[uint64]ledgerAdvertisement{7: {first: 1, last: parent.Sequence()}})
	trusted, err := a.GetValidatorKey()
	require.NoError(t, err)
	r.catchupReplay.maybeAcquireFromValidation(&consensus.Validation{
		NodeID: trusted, LedgerSeq: link.seq, LedgerID: consensus.LedgerID(link.hash),
	}, 7)
	assert.Empty(t, sender.replayCalls())
	require.NotEmpty(t, sender.legacyCalls())
	completeStandardReplayTestLink(t, r, link)
	drainStandardReplayTestPipeline(t, r)
	stored, err := svc.GetLedgerByHash(link.hash)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, link.hash, stored.Hash())
	assert.Zero(t, r.catchupReplay.catchupInFlight())
}

func TestRouter_AvailabilityMissRemembersNewHeadsAndRangeAdvance(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	seq, hash := parent.Sequence()+1, [32]byte{0xb2}
	ads := map[uint64]ledgerAdvertisement{}
	setPeerAdvertisements(r, sender, ads)
	sender.acquisitionPeers = []uint64{7, 8}
	require.True(t, r.catchupReplay.startLedgerAcquisition(seq, hash, 7))
	sendReplayAvailabilityResponse(t, r, 7, hash, message.ReplyErrorNoLedger)
	require.Equal(t, []replayDeltaCall{{peerID: 7, hash: hash}, {peerID: 8, hash: hash}}, sender.replayCalls())
	for next := seq + 1; next <= seq+5; next++ {
		peer, ok := r.catchupReplay.resolveReplayPeer(next, [32]byte{byte(next)}, 7)
		require.True(t, ok)
		assert.Equal(t, uint64(8), peer, "a new ledger must retain the failed relay's availability penalty")
	}
	sender.mu.Lock()
	ads[7] = ledgerAdvertisement{first: 1, last: seq + 5}
	sender.mu.Unlock()
	peer, ok := r.catchupReplay.resolveReplayPeer(seq+5, [32]byte{0xb3}, 7)
	require.True(t, ok)
	assert.Equal(t, uint64(7), peer)
	r.catchupReplay.peersMu.RLock()
	assert.Empty(t, r.catchupReplay.peerAvailabilityMisses)
	r.catchupReplay.peersMu.RUnlock()
}

func TestRouter_AvailabilityMissBounds(t *testing.T) {
	r, _, sender, _ := makeRouter(t)
	c := r.catchupReplay
	now := time.Now()
	hash := [32]byte{0xb4}
	for id := uint64(1); id <= peerAvailabilityLimit+10; id++ {
		c.recordPeerAvailabilityMiss(id, 100, hash, message.ReplyErrorNoLedger, now.Add(time.Duration(id)))
	}
	require.Len(t, c.peerAvailabilityMisses, peerAvailabilityLimit)
	assert.NotContains(t, c.peerAvailabilityMisses, uint64(1))
	assert.Equal(t, peerUnavailable, c.acquisitionPeerRank(11, 101, [32]byte{0xb5}, now))
	assert.Equal(t, peerUnknown, c.acquisitionPeerRank(11, 101, [32]byte{0xb5}, now.Add(peerAvailabilityTTL+time.Second)))
	c.recordPeerAvailabilityMiss(12, 100, hash, message.ReplyErrorNoNode, now)
	assert.Equal(t, peerUnknown, c.acquisitionPeerRank(12, 101, [32]byte{0xb5}, now), "a missing node does not imply missing newer ledgers")
	c.handlePeerDisconnect(peermanagement.PeerID(12))
	assert.NotContains(t, c.peerAvailabilityMisses, uint64(12))
	sender.acquisitionPeers = []uint64{13}
	peer, ok := c.resolveAcquisitionPeer(101, [32]byte{0xb5}, 13)
	require.True(t, ok, "availability penalties must not block standard recovery")
	assert.Equal(t, uint64(13), peer)
	_, ok = c.resolveReplayPeer(101, [32]byte{0xb5}, 13)
	assert.False(t, ok, "a recent miss uses standard recovery until availability changes or expires")
}

func TestRouter_AvailabilityMissReconsidersNewHashAdvertisement(t *testing.T) {
	r, _, sender, _ := makeRouter(t)
	hash := [32]byte{0xb7}
	ads := map[uint64]ledgerAdvertisement{}
	setPeerAdvertisements(r, sender, ads)
	now := time.Now()
	r.catchupReplay.recordPeerAvailabilityMiss(7, 100, hash, message.ReplyErrorNoLedger, now)
	assert.Equal(t, peerUnavailable, r.catchupReplay.acquisitionPeerRank(7, 100, hash, now))
	older := [32]byte{0x90}
	ads[7] = ledgerAdvertisement{hash: older}
	assert.Equal(t, peerAdvertised, r.catchupReplay.acquisitionPeerRank(7, 90, older, now))
	assert.Equal(t, peerUnavailable, r.catchupReplay.acquisitionPeerRank(7, 101, [32]byte{0xb8}, now))
	r.catchupReplay.recordPeerAvailabilityMiss(7, 90, older, message.ReplyErrorNoNode, now)
	assert.Equal(t, peerUnavailable, r.catchupReplay.acquisitionPeerRank(7, 101, [32]byte{0xb8}, now))
	ads[7] = ledgerAdvertisement{hash: hash}
	assert.Equal(t, peerAdvertised, r.catchupReplay.acquisitionPeerRank(7, 100, hash, now))
	r.catchupReplay.recordPeerAvailabilityMiss(7, 100, hash, message.ReplyErrorNoLedger, now)
	assert.Equal(t, peerUnavailable, r.catchupReplay.acquisitionPeerRank(7, 100, hash, now), "unchanged advertisements must not erase a later miss")
}

func TestRouter_AvailabilityFallbackPrefersServingStandardPeer(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	seq, hash := parent.Sequence()+1, [32]byte{0xb6}
	sender.acquisitionPeers = []uint64{8, 9}
	sender.peerReplaySupport = map[uint64]bool{8: false, 9: true}
	setPeerAdvertisements(r, sender, map[uint64]ledgerAdvertisement{8: {first: 1, last: seq}})
	require.NoError(t, r.catchupReplay.startReplayDeltaAcquisition(seq, hash, 7, parent))
	sendReplayAvailabilityResponse(t, r, 7, hash, message.ReplyErrorNoLedger)
	require.Len(t, sender.replayCalls(), 1)
	require.NotEmpty(t, sender.legacyCalls())
	assert.Equal(t, uint64(8), sender.legacyCalls()[0].peerID)
	il := r.catchupReplay.fetchTracker.Find(hash)
	require.NotNil(t, il)
	assert.True(t, il.TransactionOnly())
}

func TestRouter_AvailabilityFallbackAfterLocalAcceptance(t *testing.T) {
	for _, beforeReply := range []bool{false, true} {
		t.Run(map[bool]string{false: "fallback before accept", true: "accept before fallback"}[beforeReply], func(t *testing.T) {
			r, _, sender, svc := makeRouter(t)
			parent := svc.GetClosedLedger()
			_, child, hash, seq := buildSuccessorAgainstParent(t, parent)
			require.NoError(t, r.catchupReplay.setAcquisitionMemoryBudget(64<<20, nil))
			require.True(t, r.catchupReplay.startLedgerAcquisition(seq, hash, 7))
			if !beforeReply {
				sendReplayAvailabilityResponse(t, r, 7, hash, message.ReplyErrorNoLedger)
				il := r.catchupReplay.fetchTracker.Find(hash)
				require.NotNil(t, il)
				require.True(t, il.TransactionOnly())
			}
			require.NoError(t, svc.SwitchToPreferredLedger(child))
			r.catchupReplay.onLedgerBuilt(seq, hash)
			requests := len(sender.legacyCalls())
			sendReplayAvailabilityResponse(t, r, 7, hash, message.ReplyErrorNoLedger)
			r.catchupReplay.fallbackReplayAvailability(seq, hash, 7, parent, nil)
			assert.Len(t, sender.legacyCalls(), requests, "late fallback must not create another acquisition")
			assert.Equal(t, hash, svc.GetClosedLedger().Hash())
			assert.Nil(t, r.catchupReplay.fetchTracker.Find(hash))
			assert.Zero(t, r.catchupReplay.replayer.Count())
			require.True(t, r.catchupReplay.closeAcquisitionMemoryBudget(), "local retirement releases acquisition ownership")
		})
	}
}

func TestRouter_ReplayTimeoutUsesServingStandardPeerPromptly(t *testing.T) {
	for name, servingPeer := range map[string]uint64{"untried standard peer": 8, "tried replay peer": 7} {
		t.Run(name, func(t *testing.T) {
			r, _, sender, svc := makeRouter(t)
			parent := svc.GetClosedLedger()
			seq, hash := parent.Sequence()+1, [32]byte{0xb8}
			clock := inboundtest.NewFakeClock(time.Now())
			r.catchupReplay.replayer.SetClock(clock)
			require.NoError(t, r.catchupReplay.startReplayDeltaAcquisition(seq, hash, 7, parent))
			sender.acquisitionPeers = []uint64{servingPeer, 9}
			sender.availableReplayPeers = []uint64{9}
			sender.peerReplaySupport = map[uint64]bool{7: true, 8: false, 9: true}
			setPeerAdvertisements(r, sender, map[uint64]ledgerAdvertisement{servingPeer: {first: 1, last: seq}})
			clock.Advance(300 * time.Millisecond)
			r.maintenanceTick()
			assert.Len(t, sender.replayCalls(), 1)
			require.NotEmpty(t, sender.legacyCalls())
			assert.Equal(t, servingPeer, sender.legacyCalls()[0].peerID)
			il := r.catchupReplay.fetchTracker.Find(hash)
			require.NotNil(t, il)
			assert.True(t, il.TransactionOnly())
			assert.Zero(t, r.catchupReplay.replayer.Count())
			assert.Equal(t, uint64(1), r.FastSyncMetrics().ReplayDeltaAvailabilityFallbacks)
		})
	}
}

func TestRouter_ReplayTimeoutDoesNotReviveSupersededTarget(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	seq, hash := parent.Sequence()+1, [32]byte{0xb9}
	clock := inboundtest.NewFakeClock(time.Now())
	r.catchupReplay.replayer.SetClock(clock)
	require.NoError(t, r.catchupReplay.startReplayDeltaAcquisition(seq, hash, 7, parent))
	sender.acquisitionPeers = []uint64{8}
	sender.peerReplaySupport = map[uint64]bool{8: false}
	setPeerAdvertisements(r, sender, map[uint64]ledgerAdvertisement{8: {first: 1, last: seq}})
	r.catchupReplay.consensusRecovery.targetHash = [32]byte{0xba}
	clock.Advance(300 * time.Millisecond)
	r.maintenanceTick()
	for _, call := range sender.legacyCalls() {
		assert.NotEqual(t, hash, call.hash, "superseded replay must pass the existing recovery admission checks")
	}
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(hash))
	assert.Zero(t, r.catchupReplay.replayer.Count())
}

// Copyright (c) 2024-2026. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package adaptor

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statusChangeMessage builds a wire-framed TMStatusChange for the given
// seq/hash and returns the InboundMessage the Router's dispatch expects.
func statusChangeMessage(t *testing.T, peerID peermanagement.PeerID, seq uint32, hash [32]byte) *peermanagement.InboundMessage {
	return statusChangeMessageWithParent(t, peerID, seq, hash, [32]byte{}, false)
}

func statusChangeMessageWithParent(
	t *testing.T,
	peerID peermanagement.PeerID,
	seq uint32,
	hash, parentHash [32]byte,
	haveParent bool,
) *peermanagement.InboundMessage {
	t.Helper()
	sc := &message.StatusChange{
		NewStatus:  message.NodeStatus(0),
		NewEvent:   message.NodeEventClosingLedger,
		LedgerSeq:  seq,
		LedgerHash: hash[:],
	}
	if haveParent {
		sc.LedgerHashPrevious = parentHash[:]
	}
	encoded, err := message.Encode(sc)
	require.NoError(t, err)
	return &peermanagement.InboundMessage{
		PeerID:  peerID,
		Type:    message.TypeStatusChange,
		Payload: encoded,
	}
}

func TestRouter_HashDivergenceAtSameSeq_RecordsWithoutAcquiring(t *testing.T) {
	r, adaptor, sender, svc := makeRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	ourHash := closed.Hash()
	ourSeq := closed.Sequence()

	// Construct a peer hash that deliberately differs from ours at the
	// same seq — simulating a peer on the real network fork.
	var peerHash [32]byte
	copy(peerHash[:], ourHash[:])
	peerHash[0] ^= 0xFF

	msg := statusChangeMessage(t, peermanagement.PeerID(7), ourSeq, peerHash)
	r.handleMessage(msg)

	assert.Empty(t, sender.replayCalls())
	assert.Empty(t, sender.legacyCalls())
	assert.False(t, r.isAcquiring(peerHash))
	r.peersMu.RLock()
	state := r.peerStates[peermanagement.PeerID(7)]
	r.peersMu.RUnlock()
	require.NotNil(t, state)
	assert.Equal(t, ourSeq, state.LedgerSeq)
	assert.Equal(t, peerHash, state.LedgerHash)
	reported := adaptor.PeerReportedLedgers()
	require.Len(t, reported, 1)
	assert.Equal(t, consensus.LedgerID(peerHash), reported[0])
}

// TestRouter_SameHashAtSameSeq_NoAcquisition verifies the negative
// case: when the peer's hash matches ours at the same seq, no
// acquisition fires. Otherwise every status-change heartbeat would
// trigger redundant acquisition requests.
func TestRouter_SameHashAtSameSeq_NoAcquisition(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	msg := statusChangeMessage(t, peermanagement.PeerID(7), closed.Sequence(), closed.Hash())
	r.handleMessage(msg)

	assert.Empty(t, rs.replayCalls(), "no replay-delta request when hashes agree")
	assert.Empty(t, rs.legacyCalls(), "no legacy request when hashes agree")
	assert.Equal(t, 0, r.replayer.Count())
	assert.Nil(t, r.fetchTracker.Find(closed.Hash()))
}

func TestRouter_StatusWithoutLedgerHashCannotSteerCatchup(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	peerID := peermanagement.PeerID(7)
	r.peersMu.Lock()
	r.peerStates[peerID] = &peerLedgerState{LedgerSeq: svc.GetClosedLedgerIndex() + 1, LedgerHash: [32]byte{0xA1}}
	r.peersMu.Unlock()

	sc := &message.StatusChange{
		NewStatus: message.NodeStatus(0),
		NewEvent:  message.NodeEventClosingLedger,
		LedgerSeq: svc.GetClosedLedgerIndex() + 100,
	}
	encoded, err := message.Encode(sc)
	require.NoError(t, err)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  peerID,
		Type:    message.TypeStatusChange,
		Payload: encoded,
	})

	assert.Empty(t, sender.replayCalls())
	assert.Empty(t, sender.legacyCalls())
	r.peersMu.RLock()
	_, tracked := r.peerStates[peerID]
	r.peersMu.RUnlock()
	assert.False(t, tracked)
}

func TestRouter_LostSyncClearsPeerLedgerWithoutAcquiringAdvertisedHash(t *testing.T) {
	r, adaptor, sender, svc := makeRouter(t)
	peerID := peermanagement.PeerID(7)
	currentHash := [32]byte{0xA1}
	r.handleMessage(statusChangeMessage(t, peerID, svc.GetClosedLedgerIndex()+1, currentHash))
	replayBefore := len(sender.replayCalls())
	legacyBefore := len(sender.legacyCalls())

	staleHash := [32]byte{0xB2}
	sc := &message.StatusChange{
		NewEvent:   message.NodeEventLostSync,
		LedgerSeq:  15750,
		LedgerHash: staleHash[:],
	}
	encoded, err := message.Encode(sc)
	require.NoError(t, err)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  peerID,
		Type:    message.TypeStatusChange,
		Payload: encoded,
	})

	r.peersMu.RLock()
	_, tracked := r.peerStates[peerID]
	r.peersMu.RUnlock()
	assert.False(t, tracked)
	assert.Empty(t, adaptor.PeerReportedLedgers())
	assert.Equal(t, replayBefore, len(sender.replayCalls()))
	assert.Equal(t, legacyBefore, len(sender.legacyCalls()))
	_, recorded := r.lookupSeqHash(sc.LedgerSeq)
	assert.False(t, recorded)
}

// TestRouter_CheckBehindArmsAcquisition verifies the checkBehind fix:
// when a peer is far ahead, the router must arm a real acquisition
// (via startLedgerAcquisition), not just broadcast an unresponded
// mtGET_LEDGER. The pre-fix path used a direct hash-and-sequence broadcast
// which broadcasts without arming an InboundLedger, so responses
// arrived with has_inbound=false and got dropped.
func TestRouter_CheckBehindArmsAcquisition(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	// Peer reports a seq way ahead of ours; none of the earlier
	// branches (NeedsInitialSync, Full-behind, !Full-behind-by-1)
	// fire because the service has finished initial sync in this
	// test setup and we're not in Full mode. checkBehind is the
	// final branch and must arm the acquisition.
	r.adaptor.operatingMode = 1 // OpModeTracking — not Full, not initial
	var peerHash [32]byte
	peerHash[0] = 0xAB

	msg := statusChangeMessage(t, peermanagement.PeerID(9), closed.Sequence()+100, peerHash)
	r.handleMessage(msg)

	replayCalls := rs.replayCalls()
	legacyCalls := rs.legacyCalls()
	totalCalls := len(replayCalls) + len(legacyCalls)
	require.GreaterOrEqual(t, totalCalls, 1,
		"checkBehind must arm an acquisition when peer is far ahead")
}

func TestRouter_FullNodeBehindPeerLeavesFullBeforeCatchup(t *testing.T) {
	r, a, rs, svc := makeRouter(t)
	a.SetOperatingMode(consensus.OpModeFull)

	peerSeq := svc.GetClosedLedgerIndex() + 3
	r.handleMessage(statusChangeMessage(t, peermanagement.PeerID(7), peerSeq, [32]byte{0xB1}))
	r.handleMessage(statusChangeMessage(t, peermanagement.PeerID(9), peerSeq, [32]byte{0xB1}))

	assert.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())
	require.GreaterOrEqual(t, acquireCount(rs), 1)
}

func TestRouter_FullNodeAcquiresPreferredPeerTwoLedgersAhead(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	a.SetOperatingMode(consensus.OpModeFull)
	peerHash := [32]byte{}
	for i := range peerHash {
		peerHash[i] = 0xff
	}

	r.handleMessage(statusChangeMessage(
		t,
		peermanagement.PeerID(7),
		svc.GetClosedLedgerIndex()+2,
		peerHash,
	))

	assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode())
	assert.True(t, r.isAcquiring(peerHash))
	assert.Equal(t, 1, acquireCount(sender))
}

func TestRouter_AheadPreferredPeerTargetPreventsFullPromotion(t *testing.T) {
	r, a, _, svc := makeRouter(t)
	a.SetOperatingMode(consensus.OpModeTracking)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	targetSeq := closed.Sequence() + 2
	targetHash := [32]byte{}
	for i := range targetHash {
		targetHash[i] = 0xff
	}

	r.peersMu.Lock()
	r.peerStates[7] = &peerLedgerState{LedgerSeq: targetSeq, LedgerHash: targetHash}
	r.peersMu.Unlock()
	a.UpdatePeerLCL(7, consensus.LedgerID(targetHash))
	r.catchupMu.Lock()
	r.catchup = catchupTarget{seq: targetSeq, hash: targetHash, peerID: 7}
	r.catchupMu.Unlock()
	r.fetchTracker.Track(inbound.New(targetHash, targetSeq, 7, r.logger))

	r.checkBehind(closed.Sequence()+1, closed.Hash(), 8)

	assert.Equal(t, consensus.OpModeTracking, a.GetOperatingMode())
}

func TestRouter_NonPreferredPeerTargetIsNotRearmed(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	seq := svc.GetClosedLedgerIndex() + 3
	staleHash := [32]byte{0x80}
	preferredHash := [32]byte{0xff}
	recordPreferredPeerCatchupTarget(r, 7, seq, staleHash)

	r.peersMu.Lock()
	r.peerStates[8] = &peerLedgerState{LedgerSeq: seq, LedgerHash: preferredHash}
	r.peerStates[9] = &peerLedgerState{LedgerSeq: seq, LedgerHash: preferredHash}
	r.peersMu.Unlock()
	a.UpdatePeerLCL(8, consensus.LedgerID(preferredHash))
	a.UpdatePeerLCL(9, consensus.LedgerID(preferredHash))

	r.armCatchupTowardTarget()

	assert.Zero(t, acquireCount(sender))
	assert.False(t, r.isAcquiring(staleHash))
}

func TestRouter_CurrentPeerMajorityReplacesSameSequenceFrontier(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	seq := svc.GetClosedLedgerIndex() + 3
	staleHash := [32]byte{0x80}
	preferredHash := [32]byte{}
	for i := range preferredHash {
		preferredHash[i] = 0xff
	}

	r.handleMessage(statusChangeMessage(t, 7, seq, staleHash))
	r.handleMessage(statusChangeMessage(t, 8, seq, preferredHash))
	r.handleMessage(statusChangeMessage(t, 9, seq, preferredHash))

	entry, ok := r.lookupSeqHash(seq)
	require.True(t, ok)
	assert.Equal(t, preferredHash, entry.hash)
	r.catchupMu.Lock()
	frontier := r.catchup
	r.catchupMu.Unlock()
	assert.Equal(t, seq, frontier.seq)
	assert.Equal(t, preferredHash, frontier.hash)
	assert.Equal(t, catchupSourcePeer, frontier.source)
	assert.True(t, r.isObsoleteRecoveryCompletion(seq, staleHash))
	assert.False(t, r.shouldSwitchConsensusLedger(seq, staleHash))
	assert.True(t, r.shouldSwitchConsensusLedger(seq, preferredHash))

	r.completeStoredConsensusRecovery(seq, staleHash, [32]byte{0x70}, false)
	entry, ok = r.lookupSeqHash(seq)
	require.True(t, ok)
	assert.Equal(t, preferredHash, entry.hash)
}

func TestRouter_CorroboratedFarPeerTipReplacesSameSequenceFrontier(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	seq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 10
	staleHash := [32]byte{0x80}
	preferredHash := [32]byte{0xff}

	r.handleMessage(statusChangeMessage(t, 7, seq, staleHash))
	r.handleMessage(statusChangeMessage(t, 8, seq, staleHash))
	r.handleMessage(statusChangeMessage(t, 9, seq, preferredHash))
	r.handleMessage(statusChangeMessage(t, 10, seq, preferredHash))

	r.catchupMu.Lock()
	frontier := r.catchup
	r.catchupMu.Unlock()
	assert.Equal(t, seq, frontier.seq)
	assert.Equal(t, preferredHash, frontier.hash)
	assert.Equal(t, catchupSourcePeer, frontier.source)
}

func TestRouter_PeerTargetCannotReplaceTrustedFrontier(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	trustedSeq := svc.GetClosedLedgerIndex() + 3
	trustedHash := [32]byte{0xc1}
	r.recordValidationCatchupTarget(trustedSeq, trustedHash, 7, catchupSourceValidation)

	r.recordCatchupTarget(trustedSeq+10, [32]byte{0xff}, 8)

	assert.Equal(t, catchupTarget{
		seq: trustedSeq, hash: trustedHash, peerID: 7, source: catchupSourceValidation,
	}, r.catchup)
}

func TestRouter_FullNodeDoesNotAcquireNonPreferredPeerTip(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	a.SetOperatingMode(consensus.OpModeFull)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	peerHash := closed.Hash()
	for i := len(peerHash) - 1; i >= 0; i-- {
		if peerHash[i] > 0 {
			peerHash[i]--
			break
		}
		peerHash[i] = 0xff
	}
	require.NotEqual(t, [32]byte{}, peerHash)

	r.handleMessage(statusChangeMessage(
		t,
		peermanagement.PeerID(7),
		closed.Sequence()+100,
		peerHash,
	))

	assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode())
	assert.Zero(t, acquireCount(sender))
	assert.False(t, r.isAcquiring(peerHash))
}

func TestRouter_TrustedValidationReplacesStatusSequenceTarget(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	fakeSeq := closed.Sequence() + 100

	r.handleMessage(statusChangeMessage(t, peermanagement.PeerID(7), fakeSeq, closed.Hash()))
	assert.Equal(t, catchupTarget{}, r.catchup)
	assert.Zero(t, acquireCount(sender))

	peerHash := [32]byte{}
	for i := range peerHash {
		peerHash[i] = 0xff
	}
	r.handleMessage(statusChangeMessage(t, peermanagement.PeerID(8), fakeSeq, peerHash))
	assert.Equal(t, catchupTarget{seq: fakeSeq, hash: peerHash, peerID: 8}, r.catchup)

	trusted, err := a.GetValidatorKey()
	require.NoError(t, err)
	trustedSeq := closed.Sequence() + 3
	trustedHash := consensus.LedgerID{0xCA, 0x14}
	r.maybeAcquireFromValidation(&consensus.Validation{
		NodeID:    trusted,
		LedgerSeq: trustedSeq,
		LedgerID:  trustedHash,
	}, 9)

	assert.Equal(t, catchupTarget{
		seq:    trustedSeq,
		hash:   [32]byte(trustedHash),
		peerID: 9,
		source: catchupSourceValidation,
	}, r.catchup)
	assert.True(t, r.isAcquiring([32]byte(trustedHash)))
}

func TestRouter_TrustedCatchupTargetDoesNotRegress(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	aheadHash := [32]byte{0xD1}
	laggingHash := [32]byte{0xD2}

	r.ensureValidationCatchupAcquisition(
		closed+10,
		aheadHash,
		7,
	)
	r.ensureValidationCatchupAcquisition(
		closed+3,
		laggingHash,
		8,
	)

	assert.Equal(t, catchupTarget{
		seq:    closed + 10,
		hash:   aheadHash,
		peerID: 7,
		source: catchupSourceValidation,
	}, r.catchup)
	assert.True(t, r.isAcquiring(aheadHash))
	assert.False(t, r.isAcquiring(laggingHash),
		"a lower validation must not start a second acquisition after the frontier is set")
}

func TestRouter_QuorumUpgradePreservesTrustedTargetPeer(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	seq := svc.GetClosedLedgerIndex() + 3
	hash := [32]byte{0xD3}
	r.recordValidationCatchupTarget(seq, hash, 7, catchupSourceValidation)
	r.recordValidationCatchupTarget(seq, hash, 0, catchupSourceQuorum)

	r.catchupMu.Lock()
	target := r.catchup
	r.catchupMu.Unlock()
	require.Equal(t, catchupTarget{
		seq:    seq,
		hash:   hash,
		peerID: 7,
		source: catchupSourceQuorum,
	}, target)
}

// acquireCount totals the acquisition requests the router emitted via either
// the replay-delta or the legacy GET_LEDGER path.
func acquireCount(rs *recordingSender) int {
	return len(rs.replayCalls()) + len(rs.legacyCalls())
}

// Issue #724: maybeAcquireFromValidation mirrors rippled checkAccept(hash,
// seq), invoked on every trusted current validation (RCLValidations.cpp:208 →
// LedgerMaster.cpp:904-919). The tests below pin each gate of that acquire.

// A trusted validation for a future ledger we don't hold must arm exactly one
// acquisition for that (seq, hash) — the edge that breaks the wrongLedger
// chase loop when the node is below quorum.
func TestRouter_TrustedValidation_FutureUnknownLedger_Acquires(t *testing.T) {
	r, a, rs, _ := makeRouter(t)
	trusted, err := a.GetValidatorKey()
	require.NoError(t, err)

	hash := [32]byte{0xCA, 0xFE}
	v := &consensus.Validation{
		NodeID:    trusted,
		LedgerSeq: 99999, // far ahead → no local parent → legacy path
		LedgerID:  consensus.LedgerID(hash),
	}

	r.maybeAcquireFromValidation(v, 7)

	require.Equal(t, 1, acquireCount(rs), "trusted validation for an unknown future ledger must arm one acquisition")
	calls := rs.legacyCalls()
	require.Len(t, calls, 1, "no local parent → legacy GET_LEDGER path")
	assert.Equal(t, hash, calls[0].hash)
	assert.Equal(t, uint32(99999), calls[0].seq)
	assert.Equal(t, uint64(7), calls[0].peerID, "the validating peer is used as the acquisition hint")
}

func TestRouter_TrustedValidation_SameSequenceUnknownLedger_Acquires(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	trusted, err := a.GetValidatorKey()
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	_, err = svc.AcceptConsensusResult(context.Background(), closed, nil, nil, time.Now(), true)
	require.NoError(t, err)
	closed = svc.GetClosedLedger()
	hash := consensus.LedgerID{0xCA, 0x13}

	r.handleMessage(statusChangeMessage(t, peermanagement.PeerID(7), closed.Sequence(), [32]byte{0xBA, 0xD0}))
	assert.Zero(t, acquireCount(sender))

	r.maybeAcquireFromValidation(&consensus.Validation{
		NodeID: trusted, LedgerSeq: closed.Sequence(), LedgerID: hash,
	}, 7)

	assert.Equal(t, 1, acquireCount(sender))
	assert.True(t, r.isAcquiring([32]byte(hash)))
}

func TestRouter_FullyValidatedHashCancelsOnlyConsensusSiblings(t *testing.T) {
	r, a, _, svc := makeRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	seq := closed.Sequence() + 1
	canonical := [32]byte{0xA0}

	legacyHash := [32]byte{0xA1}
	otherSeqHash := [32]byte{0xA2}
	genericHash := [32]byte{0xA3}
	historyHash := [32]byte{0xA4}
	keep := inbound.New(canonical, seq, 1, r.logger)
	legacy := inbound.New(legacyHash, seq, 2, r.logger)
	otherSeq := inbound.New(otherSeqHash, seq+1, 3, r.logger)
	generic := inbound.NewGeneric(genericHash, seq, 4, r.logger)
	history := inbound.NewHistory(historyHash, seq, 5, r.logger)
	for _, acquisition := range []*inbound.Ledger{keep, legacy, otherSeq, generic, history} {
		r.fetchTracker.Track(acquisition)
	}
	r.acquisitionMu.Lock()
	r.consensusRecovery = consensusRecovery{
		targetHash: legacyHash,
		stepHash:   legacyHash,
	}
	r.acquisitionMu.Unlock()
	r.catchupMu.Lock()
	r.catchup = catchupTarget{seq: seq, hash: legacyHash, peerID: 2}
	r.catchupMu.Unlock()

	replayHash := [32]byte{0xB1}
	require.NoError(t, r.startReplayDeltaAcquisition(seq, replayHash, 6, closed))
	require.NoError(t, r.startReplayDeltaAcquisition(seq, canonical, 7, closed))

	a.OnLedgerFullyValidated(consensus.LedgerID(canonical), seq)

	assert.Nil(t, r.fetchTracker.Find(legacyHash))
	assert.NotNil(t, r.fetchTracker.Find(canonical))
	assert.NotNil(t, r.fetchTracker.Find(otherSeqHash))
	assert.NotNil(t, r.fetchTracker.Find(genericHash))
	assert.NotNil(t, r.fetchTracker.Find(historyHash))
	assert.False(t, r.replayer.Has(replayHash))
	assert.True(t, r.replayer.Has(canonical))
	assert.Equal(t, consensusRecovery{}, r.consensusRecovery)
	assert.Equal(t, catchupTarget{
		seq:    seq,
		hash:   canonical,
		source: catchupSourceQuorum,
	}, r.catchup)
	recorded, ok := r.lookupSeqHash(seq)
	require.True(t, ok)
	assert.Equal(t, canonical, recorded.hash)
}

func TestRouter_TrustedTargetSupersedesOneObsoleteFullStateAcquisition(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	firstHash := [32]byte{0x81}
	secondHash := [32]byte{0x82}
	targetHash := [32]byte{0x90}
	first := inbound.New(firstHash, closed+1, 7, r.logger)
	second := inbound.New(secondHash, closed+2, 8, r.logger)
	r.fetchTracker.Track(first)
	r.fetchTracker.Track(second)
	trackCatchupPeer(r, 9, closed+maxForwardDeltaGap+10)

	a.OnLedgerFullyValidated(consensus.LedgerID(targetHash), closed+maxForwardDeltaGap+10)

	assert.Nil(t, r.fetchTracker.Find(firstHash))
	assert.Same(t, second, r.fetchTracker.Find(secondHash))
	assert.NotNil(t, r.fetchTracker.Find(targetHash))
	assert.Equal(t, maxConcurrentSpeculativeCatchup, r.protectedCatchupInFlight())
	assert.NotEmpty(t, sender.legacyCalls())
}

func TestRouter_RecentProgressProtectsNearbyFullStateAcquisition(t *testing.T) {
	r, a, _, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	firstHash := [32]byte{0x81}
	secondHash := [32]byte{0x82}
	targetHash := [32]byte{0x90}
	first := inbound.New(firstHash, closed+1, 7, r.logger)
	second := inbound.New(secondHash, closed+2, 8, r.logger)
	r.fetchTracker.Track(first)
	r.fetchTracker.Track(second)

	a.OnLedgerFullyValidated(consensus.LedgerID(targetHash), closed+10)

	assert.Same(t, first, r.fetchTracker.Find(firstHash))
	assert.Same(t, second, r.fetchTracker.Find(secondHash))
	assert.Nil(t, r.fetchTracker.Find(targetHash))
}

func TestRouter_StalledNearbyFullStateAcquisitionIsSuperseded(t *testing.T) {
	r, a, _, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	firstHash := [32]byte{0x81}
	secondHash := [32]byte{0x82}
	targetHash := [32]byte{0x90}
	first := inbound.New(firstHash, closed+1, 7, r.logger)
	second := inbound.New(secondHash, closed+2, 8, r.logger)
	for _, acquisition := range []*inbound.Ledger{first, second} {
		now := time.Now()
		acquisition.RearmTimer(now)
		for range 2 {
			now = now.Add(4 * time.Second)
			require.Equal(t, inbound.TimerEscalate, acquisition.OnTimer(now))
			acquisition.RearmTimer(now)
		}
		r.fetchTracker.Track(acquisition)
	}
	trackCatchupPeer(r, 9, closed+10)

	a.OnLedgerFullyValidated(consensus.LedgerID(targetHash), closed+10)

	assert.Nil(t, r.fetchTracker.Find(firstHash))
	assert.Same(t, second, r.fetchTracker.Find(secondHash))
	assert.NotNil(t, r.fetchTracker.Find(targetHash))
}

func TestRouter_ExactRecoveryAcquisitionsAreNeverSuperseded(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	targetHash := [32]byte{0x81}
	stepHash := [32]byte{0x82}
	target := inbound.New(targetHash, closed+1, 7, r.logger)
	step := inbound.New(stepHash, closed+2, 8, r.logger)
	r.fetchTracker.Track(target)
	r.fetchTracker.Track(step)
	r.acquisitionMu.Lock()
	r.consensusRecovery = consensusRecovery{targetHash: targetHash, stepHash: stepHash}
	victim := r.obsoleteCatchupVictimLocked(closed + maxForwardDeltaGap + 10)
	r.acquisitionMu.Unlock()

	assert.Nil(t, victim)
}

func TestRouter_TrustedValidationAheadLeavesFullBeforeAcquire(t *testing.T) {
	r, a, rs, svc := makeRouter(t)
	a.SetOperatingMode(consensus.OpModeFull)
	trusted, err := a.GetValidatorKey()
	require.NoError(t, err)

	v := &consensus.Validation{
		NodeID:    trusted,
		LedgerSeq: svc.GetClosedLedgerIndex() + 3,
		LedgerID:  consensus.LedgerID{0xCA, 0x11},
	}
	r.maybeAcquireFromValidation(v, 7)

	assert.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())
	require.GreaterOrEqual(t, acquireCount(rs), 1)
}

func TestRouter_OpenRoundAcquiresTargetLedger(t *testing.T) {
	r, a, rs, svc := makeRouter(t)
	a.SetOperatingMode(consensus.OpModeFull)
	trusted, err := a.GetValidatorKey()
	require.NoError(t, err)
	closed := svc.GetClosedLedgerIndex()
	hash := consensus.LedgerID{0xCA, 0x12}

	r.maybeAcquireFromValidation(&consensus.Validation{
		NodeID: trusted, LedgerSeq: closed + 1, LedgerID: hash,
	}, 7)
	r.armValidatedLedgerAcquisition(closed+1, [32]byte(hash))
	r.armConsensusCatchup()

	assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode())
	require.GreaterOrEqual(t, acquireCount(rs), 1)
	assert.Equal(t, 1, r.catchupInFlight())
	assert.Equal(t, closed, svc.GetClosedLedgerIndex())
}

func TestRouter_ActiveBuildDoesNotAcquireItsTargetLedger(t *testing.T) {
	r, a, rs, svc := makeRouter(t)
	a.SetOperatingMode(consensus.OpModeFull)
	trusted, err := a.GetValidatorKey()
	require.NoError(t, err)
	closed := svc.GetClosedLedgerIndex()
	engine := &mockEngine{buildingSeq: closed + 1}
	r.engine = engine
	hash := consensus.LedgerID{0xCA, 0x12}
	r.peerStates[7] = &peerLedgerState{
		LedgerSeq:  closed + 1,
		LedgerHash: [32]byte(hash),
	}

	r.maybeAcquireFromValidation(&consensus.Validation{
		NodeID: trusted, LedgerSeq: closed + 1, LedgerID: hash,
	}, 7)
	r.armValidatedLedgerAcquisition(closed+1, [32]byte(hash))
	r.armConsensusCatchup()

	assert.Zero(t, acquireCount(rs))
	assert.Zero(t, r.catchupInFlight())
	assert.Equal(t, closed, svc.GetClosedLedgerIndex())

	_, err = svc.AcceptConsensusResult(
		context.Background(),
		svc.GetClosedLedger(),
		nil,
		nil,
		time.Now(),
		true,
	)
	require.NoError(t, err)
	built := svc.GetClosedLedger()
	require.NotNil(t, built)
	engine.buildingSeq = 0
	r.onLedgerBuilt(built.Sequence(), built.Hash())
	require.GreaterOrEqual(t, acquireCount(rs), 1)
}

func TestRouter_LedgerBuiltRearmsQuorumTargetWithoutPeerStatus(t *testing.T) {
	r, a, rs, svc := makeRouter(t)
	a.SetOperatingMode(consensus.OpModeFull)
	trusted, err := a.GetValidatorKey()
	require.NoError(t, err)
	closed := svc.GetClosedLedgerIndex()
	engine := &mockEngine{buildingSeq: closed + 1}
	r.engine = engine
	hash := consensus.LedgerID{0xCA, 0x13}

	r.onLedgerFullyValidated(closed+1, [32]byte(hash))
	r.maybeAcquireFromValidation(&consensus.Validation{
		NodeID: trusted, LedgerSeq: closed + 1, LedgerID: hash,
	}, 7)

	assert.Equal(t, catchupTarget{
		seq:    closed + 1,
		hash:   [32]byte(hash),
		peerID: 7,
		source: catchupSourceQuorum,
	}, r.catchup)
	assert.Empty(t, r.peerStates)
	assert.Zero(t, acquireCount(rs))

	_, err = svc.AcceptConsensusResult(
		context.Background(),
		svc.GetClosedLedger(),
		nil,
		nil,
		time.Now(),
		true,
	)
	require.NoError(t, err)
	built := svc.GetClosedLedger()
	require.NotNil(t, built)
	engine.buildingSeq = 0
	r.onLedgerBuilt(built.Sequence(), built.Hash())

	require.GreaterOrEqual(t, acquireCount(rs), 1)
}

// An UNTRUSTED validator must not steer acquisition (RCLValidations.cpp:194).
func TestRouter_UntrustedValidation_NoAcquire(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	var untrusted consensus.NodeID
	untrusted[0] = 0xFF // not in the trusted set
	v := &consensus.Validation{
		NodeID:    untrusted,
		LedgerSeq: svc.GetValidatedLedgerIndex() + 50,
		LedgerID:  consensus.LedgerID{0x11},
	}
	r.maybeAcquireFromValidation(v, 7)
	assert.Zero(t, acquireCount(rs), "untrusted validator must not trigger acquisition")
}

// seq at or below our validated tip must not acquire (LedgerMaster.cpp:883 gate).
func TestRouter_ValidationAtOrBelowValidated_NoAcquire(t *testing.T) {
	r, a, rs, svc := makeRouter(t)
	trusted, _ := a.GetValidatorKey()
	v := &consensus.Validation{
		NodeID:    trusted,
		LedgerSeq: svc.GetValidatedLedgerIndex(),
		LedgerID:  consensus.LedgerID{0x22},
	}
	r.maybeAcquireFromValidation(v, 7)
	assert.Zero(t, acquireCount(rs), "seq <= validated tip must not acquire")
}

// A ledger we already hold (built or adopted) must not be re-acquired.
func TestRouter_ValidationForHeldLedger_NoAcquire(t *testing.T) {
	r, a, rs, svc := makeRouter(t)
	trusted, _ := a.GetValidatorKey()
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	v := &consensus.Validation{
		NodeID:    trusted,
		LedgerSeq: 99999,
		LedgerID:  consensus.LedgerID(closed.Hash()), // already in history
	}
	r.maybeAcquireFromValidation(v, 7)
	assert.Zero(t, acquireCount(rs), "a ledger already in history must not be re-acquired")
}

// Repeated trusted validations for the same unknown hash share one acquisition.
func TestRouter_RepeatedTrustedValidations_SingleAcquire(t *testing.T) {
	r, a, rs, _ := makeRouter(t)
	trusted, _ := a.GetValidatorKey()
	v := &consensus.Validation{
		NodeID:    trusted,
		LedgerSeq: 99999,
		LedgerID:  consensus.LedgerID([32]byte{0xDE, 0xAD}),
	}
	r.maybeAcquireFromValidation(v, 7)
	r.maybeAcquireFromValidation(v, 8)
	assert.Equal(t, 1, r.catchupInFlight())
	assert.Equal(t, 2, acquireCount(rs), "the replacement peer joins the existing acquisition")
}

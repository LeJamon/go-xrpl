package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #1161 keep-up: gossip-driven catch-up drives a SINGLE bounded target
// (rippled LedgerMaster::doAdvance), not one InboundLedger per gossiped
// status/validation.

// Feeding many trusted validations for ever-higher tips must arm at most
// maxConcurrentCatchup acquisitions — not one per event — while still recording
// the highest tip as the catch-up target to retarget toward.
func TestRouter_CatchupFanoutBoundedByCap(t *testing.T) {
	r, a, rs, svc := makeRouter(t)
	trusted, err := a.GetValidatorKey()
	require.NoError(t, err)

	base := svc.GetValidatedLedgerIndex()
	const n = 60
	var highest uint32
	for i := range n {
		seq := base + 100 + uint32(i) // strictly increasing, all far ahead
		highest = seq
		var hash [32]byte
		hash[0] = byte(0xA0 + i%16)
		hash[1] = byte(i)
		v := &consensus.Validation{
			NodeID:    trusted,
			LedgerSeq: seq,
			LedgerID:  consensus.LedgerID(hash),
		}
		r.maybeAcquireFromValidation(v, uint64(7))
	}

	assert.LessOrEqual(t, acquireCount(rs), maxConcurrentCatchup,
		"feeding %d ever-higher trusted tips must arm at most maxConcurrentCatchup acquisitions, not one per event", n)
	assert.Equal(t, maxConcurrentSpeculativeCatchup, r.catchupInFlight(),
		"gossip catch-up must leave one slot for an exact consensus target")

	tSeq, _, _ := r.bestCatchupTarget()
	assert.Equal(t, highest, tSeq,
		"the recorded catch-up target must be the highest tip seen")
}

// A far-behind completion remains store-only and immediately re-arms toward the
// newest target instead of handing the intermediate ledger to consensus.
func TestRouter_FarCompletionStoresAndRetargets(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closedSeq := svc.GetClosedLedgerIndex()

	// Record a catch-up target well ahead of the tip we're about to adopt.
	targetSeq := closedSeq + 40
	var targetHash [32]byte
	targetHash[0] = 0xC7
	recordPreferredPeerCatchupTarget(r, 7, targetSeq, targetHash)

	// Completion stores the fetched ledger without moving the service frontier.
	il := completedCatchUpAcquisition(t, closedSeq+10)
	r.fetchTracker.Track(il)
	r.completeInboundLedger(il)

	require.Equal(t, closedSeq, svc.GetClosedLedgerIndex())
	stored, err := svc.GetLedgerByHash(il.Hash())
	require.NoError(t, err)
	require.Equal(t, closedSeq+10, stored.Sequence())
	require.Equal(t, 1, acquireCount(rs))
	require.Len(t, rs.legacyCalls(), 1)
	assert.Equal(t, targetHash, rs.legacyCalls()[0].hash)
}

// Completing the recorded target never arms another acquisition from the
// completion callback.
func TestRouter_NoRetargetWhenCaughtUp(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closedSeq := svc.GetClosedLedgerIndex()

	tipSeq := closedSeq + 10
	r.recordCatchupTarget(tipSeq, [32]byte{0x9A}, 7)

	il := completedCatchUpAcquisition(t, tipSeq)
	r.fetchTracker.Track(il)
	r.completeInboundLedger(il)

	require.Equal(t, closedSeq, svc.GetClosedLedgerIndex())
	_, err := svc.GetLedgerByHash(il.Hash())
	require.NoError(t, err)
	assert.Zero(t, acquireCount(rs),
		"once closed reaches the target, completion must not arm another acquisition")
}

// The validated-tip eligibility gate must survive the funnel: a trusted tip at
// or below the validated index must neither arm nor be recorded as a target.
// Bounding CONCURRENCY must not weaken ELIGIBILITY — a closed-based gate
// reintroduces the private-chain fork-stuck bug.
func TestRouter_CatchupEligibilityGate_RejectsAtOrBelowValidated(t *testing.T) {
	r, a, rs, svc := makeRouter(t)
	trusted, err := a.GetValidatorKey()
	require.NoError(t, err)

	v := &consensus.Validation{
		NodeID:    trusted,
		LedgerSeq: svc.GetValidatedLedgerIndex(),
		LedgerID:  consensus.LedgerID([32]byte{0x33}),
	}
	r.maybeAcquireFromValidation(v, 7)

	assert.Zero(t, acquireCount(rs), "a tip at/below the validated index must not arm")
	tSeq, _, _ := r.bestCatchupTarget()
	assert.Zero(t, tSeq, "an ineligible tip must not be recorded as a catch-up target")
}

// The initial-sync path must still arm: the bound collapses the already-synced
// fan-out, it must not stall first boot.
func TestRouter_InitialSyncPathStillArms(t *testing.T) {
	svc := adg_newNonStandaloneService(t)
	require.True(t, svc.NeedsInitialSync(),
		"non-standalone service must start needing initial sync")

	a, rs := newRecordingAdaptor(t, svc)
	inbox := make(chan *peermanagement.InboundMessage, 8)
	r := newTestRouter(nil, a, inbox)

	var peerHash [32]byte
	peerHash[0] = 0x5C
	msg := statusChangeMessage(t, peermanagement.PeerID(7), svc.GetClosedLedgerIndex()+50, peerHash)
	r.handleMessage(msg)

	require.Equal(t, 1, acquireCount(rs),
		"a fresh node in initial sync must still arm a bounded catch-up acquisition")
}

func TestRouter_InitialConsensusAcquisitionStagesClosedLedger(t *testing.T) {
	svc := adg_newNonStandaloneService(t)
	a, _ := newRecordingAdaptor(t, svc)
	r := newTestRouter(nil, a, make(chan *peermanagement.InboundMessage, 8))
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	tipSeq := svc.GetClosedLedgerIndex() + 1
	il := completedCatchUpAcquisition(t, tipSeq)
	r.fetchTracker.Track(il)

	r.completeInboundLedger(il)

	require.True(t, svc.NeedsInitialSync())
	require.Equal(t, closed.Hash(), svc.GetClosedLedger().Hash())
	stored, err := svc.GetLedgerByHash(il.Hash())
	require.NoError(t, err)
	require.Equal(t, tipSeq, stored.Sequence())
}

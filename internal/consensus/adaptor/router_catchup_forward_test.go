package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #1161 keep-up: catch-up WALKS FORWARD one ledger at a time via
// replay-delta against the held parent when behind on the SAME branch, rather
// than jump-adopting the far tip's full state on every hop.

// Same branch, parent known: closed+1 descends from our closed ledger, tip a
// few ahead. The router must issue a replay-delta for closed+1 (parent local),
// NOT a legacy jump-adopt for the far tip.
func TestRouter_ForwardDeltaStep_SameBranch(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	closedHash := svc.GetClosedLedger().Hash()

	var nextHash [32]byte
	nextHash[0] = 0xB1
	r.recordSeqHash(closed+1, nextHash, closedHash, true)

	// A far tip is the recorded catch-up target (the jump-adopt fallback).
	var tipHash [32]byte
	tipHash[0] = 0xF0
	r.recordCatchupTarget(closed+5, tipHash, 7)

	r.armCatchupTowardTarget()

	replays := rs.replayCalls()
	require.Len(t, replays, 1, "same-branch catch-up must issue one forward replay-delta for closed+1")
	assert.Equal(t, nextHash, replays[0].hash)
	assert.Empty(t, rs.legacyCalls(), "must not jump-adopt the far tip when a clean forward step exists")
	assert.True(t, r.replayer.Has(nextHash), "replay-delta acquisition must be in flight for closed+1")
}

// Same branch, parent unknown (validation-only): closed+1 has a hash but no
// recorded parent; the same-branch check falls back to our own closed seq's
// recorded hash. When it matches our closed hash the forward step is still taken.
func TestRouter_ForwardDeltaStep_SameBranchViaClosedSeqProxy(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	closedHash := svc.GetClosedLedger().Hash()

	// A trusted validation for our own closed seq confirms we agree with the
	// network there — equivalent to knowing closed+1's parent.
	r.recordSeqHash(closed, closedHash, [32]byte{}, false)
	var nextHash [32]byte
	nextHash[0] = 0xB1
	r.recordSeqHash(closed+1, nextHash, [32]byte{}, false)

	var tipHash [32]byte
	tipHash[0] = 0xF0
	r.recordCatchupTarget(closed+3, tipHash, 7)

	r.armCatchupTowardTarget()

	replays := rs.replayCalls()
	require.Len(t, replays, 1, "closed-seq linkage must still enable the forward step")
	assert.Equal(t, nextHash, replays[0].hash)
	assert.Empty(t, rs.legacyCalls())
}

// Fork: closed+1's recorded parent differs from our closed hash → divergent
// branch → jump-adopt the far validated tip (legacy full-state), not a forward
// delta.
func TestRouter_ForwardDeltaStep_ForkFallsBackToJumpAdopt(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()

	var nextHash, wrongParent [32]byte
	nextHash[0] = 0xB1
	wrongParent[0] = 0xDE // NOT our closed hash
	r.recordSeqHash(closed+1, nextHash, wrongParent, true)

	var tipHash [32]byte
	tipHash[0] = 0xF0
	r.recordCatchupTarget(closed+5, tipHash, 7)

	r.armCatchupTowardTarget()

	assert.Empty(t, rs.replayCalls(), "a forked forward step must not replay-delta")
	legacy := rs.legacyCalls()
	require.Len(t, legacy, 1, "fork must jump-adopt toward the far tip")
	assert.Equal(t, tipHash, legacy[0].hash)
	assert.Equal(t, closed+5, legacy[0].seq)
}

// Cold: closed+1's hash is unknown (no validation/status for it yet) → the
// router can't prove a clean forward step → jump-adopt the far tip.
func TestRouter_ForwardDeltaStep_UnknownNextFallsBackToJumpAdopt(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()

	var tipHash [32]byte
	tipHash[0] = 0xF0
	r.recordCatchupTarget(closed+5, tipHash, 7)
	// No seqHash entry for closed+1.

	r.armCatchupTowardTarget()

	assert.Empty(t, rs.replayCalls())
	legacy := rs.legacyCalls()
	require.Len(t, legacy, 1, "unknown closed+1 must jump-adopt the far tip")
	assert.Equal(t, tipHash, legacy[0].hash)
	assert.Equal(t, closed+5, legacy[0].seq)
}

// Far/cold gap: closed+1 is a known clean child, but the tip is beyond
// maxForwardDeltaGap → a single jump-adopt is preferred over a long serial walk.
func TestRouter_ForwardDeltaStep_FarGapJumpAdopts(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	closedHash := svc.GetClosedLedger().Hash()

	var nextHash [32]byte
	nextHash[0] = 0xB1
	r.recordSeqHash(closed+1, nextHash, closedHash, true)

	var tipHash [32]byte
	tipHash[0] = 0xF0
	tipSeq := closed + maxForwardDeltaGap + 10
	r.recordCatchupTarget(tipSeq, tipHash, 7)

	r.armCatchupTowardTarget()

	assert.Empty(t, rs.replayCalls(), "beyond the forward bound the router must jump, not walk")
	legacy := rs.legacyCalls()
	require.Len(t, legacy, 1)
	assert.Equal(t, tipSeq, legacy[0].seq)
}

// Forward walk: completing a forward step advances closed by one and re-arms the
// NEXT forward step (closed+2), not a jump — the serial walk that converges.
func TestRouter_ForwardWalk_RearmsNextOnCompletion(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	c := parent.Sequence()

	// A real forward child at c+1 anchored on our closed ledger, completable on
	// GotBase + state nodes alone.
	rootHash, rootData, wire := buildSelfHealSourceState(t)
	hdr := header.LedgerHeader{
		LedgerIndex: c + 1,
		ParentHash:  parent.Hash(),
		AccountHash: rootHash,
	}
	data := header.AddRaw(hdr, false)
	childHash := common.Sha512Half(protocol.HashPrefixLedgerMaster.Bytes(), data)

	il := inbound.New(childHash, c+1, 7, serveTestLogger())
	require.NoError(t, il.GotBase([]message.LedgerNode{{NodeData: data}, {NodeData: rootData}}))
	require.NoError(t, il.GotStateNodes(wire))
	require.True(t, il.IsComplete())
	r.fetchTracker.Track(il)

	// Record the next forward child (c+2) anchored on the child we're about to
	// adopt, plus a far tip so the walk still has somewhere to go.
	var next2 [32]byte
	next2[0] = 0xB2
	r.recordSeqHash(c+2, next2, childHash, true)
	var tipHash [32]byte
	tipHash[0] = 0xF0
	r.recordCatchupTarget(c+10, tipHash, 7)

	r.completeInboundLedger(il)

	require.Equal(t, c+1, svc.GetClosedLedgerIndex(), "forward step advances closed by one")
	require.Equal(t, childHash, svc.GetClosedLedger().Hash())

	replays := rs.replayCalls()
	require.Len(t, replays, 1, "completion must re-arm the next forward step")
	assert.Equal(t, next2, replays[0].hash)
	assert.Empty(t, rs.legacyCalls(), "the re-arm must be a forward delta, not a jump")
}

// maxConcurrentCatchup is preserved: while a forward step is in flight, a second
// arm is suppressed (serial forward walk).
func TestRouter_ForwardWalk_SerialUnderCap(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	closedHash := svc.GetClosedLedger().Hash()

	var nextHash [32]byte
	nextHash[0] = 0xB1
	r.recordSeqHash(closed+1, nextHash, closedHash, true)
	r.recordCatchupTarget(closed+5, [32]byte{0xF0}, 7)

	r.armCatchupTowardTarget()
	require.Equal(t, maxConcurrentCatchup, r.catchupInFlight(), "one forward step in flight")

	// A second arm while the first is in flight must not add another.
	r.armCatchupTowardTarget()
	assert.Equal(t, maxConcurrentCatchup, r.catchupInFlight(),
		"the forward walk stays serial under the concurrency cap")
	assert.Len(t, rs.replayCalls(), 1, "no second acquisition while one is in flight")
}

// The seqHash table is bounded: once it exceeds the trailing seqHashRetain
// window, entries older than (max-seqHashRetain) are pruned on insert so a
// long-running node never grows it unbounded.
func TestRouter_SeqHashTableBounded(t *testing.T) {
	r, _, _, _ := makeRouter(t)

	var h [32]byte
	h[0] = 0x01
	top := uint32(seqHashRetain + 10)
	for seq := uint32(1); seq <= top; seq++ {
		r.recordSeqHash(seq, h, [32]byte{}, false)
	}

	_, lowKept := r.lookupSeqHash(1)
	assert.False(t, lowKept, "an entry older than the retention window must be pruned")
	_, highKept := r.lookupSeqHash(top)
	assert.True(t, highKept, "the newest entry must be retained")

	r.seqHashMu.Lock()
	size := len(r.seqHash)
	r.seqHashMu.Unlock()
	assert.LessOrEqual(t, size, seqHashRetain+1, "the table stays within the retention window")
}

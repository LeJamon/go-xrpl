package adaptor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The maintenance tick must re-arm the recorded catch-up target when nothing
// is in flight (rippled LedgerMaster::doAdvance re-attempts every cycle).
// Without it, a reaped/failed sole acquisition (cap=1) parks catch-up until
// the next gossip event — a stall under a gossip lull.
func TestRouter_MaintenanceTickReArmsCatchupTarget(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closedSeq := svc.GetClosedLedgerIndex()

	targetSeq := closedSeq + 40
	var targetHash [32]byte
	targetHash[0] = 0xD9
	r.recordCatchupTarget(targetSeq, targetHash, 7)
	require.Zero(t, r.catchupInFlight(), "setup: nothing in flight")

	r.maintenanceTick()

	require.Equal(t, 1, acquireCount(rs),
		"the tick must arm exactly one acquisition toward the recorded target")
	calls := rs.legacyCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, targetHash, calls[0].hash)
	assert.Equal(t, targetSeq, calls[0].seq)

	// A second tick with the acquisition now in flight must not stack another.
	r.maintenanceTick()
	assert.Equal(t, 1, acquireCount(rs),
		"an in-flight acquisition must suppress further arming (cap=1)")
}

// A reached target arms nothing: the tick re-arm is a no-op once closed has
// caught up.
func TestRouter_MaintenanceTickNoArmWhenCaughtUp(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	r.recordCatchupTarget(svc.GetClosedLedgerIndex(), [32]byte{0x5D}, 7)

	r.maintenanceTick()

	assert.Zero(t, acquireCount(rs),
		"a target at/below closed must not arm on the tick")
}

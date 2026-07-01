package adaptor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Issue #724: tx-set acquisition must keep re-requesting missing nodes on a
// timer even when no further TMLedgerData arrives. The inbound path
// (handleTxSetData) only fires on an arriving response; if the serving peer
// falls silent mid-acquire, maintenanceTick's retryStalledTxSetAcquires is
// the only thing that re-drives the request — mirroring rippled's
// TransactionAcquire::onTimer.

// When inbound responses stop, the timer re-requests the still-missing nodes
// on each tick while the acquire is incomplete.
func TestTxSetAcquire_TimerRetriggersWhenInboundQuiet(t *testing.T) {
	router, rs := newRetryRouter(t)
	ld, txSetID := rootOnlyTxSetLedgerData(t, 8)

	// MinInterval=0 so each tick is eligible to fire (no production wait).
	withRetryKnobs(router, 0, 20, 3, func() {
		// First inbound response: creates the acquire (root only → incomplete)
		// and issues the inbound missing-nodes request.
		router.handleTxSetData(ld, 4)
		firstN := rs.calledN()
		require.GreaterOrEqual(t, firstN, 1, "inbound path issues the first missing-nodes request")

		// Peer goes silent. The timer must re-request without any new inbound.
		router.maintenanceTick()
		require.Greater(t, rs.calledN(), firstN,
			"timer must re-request missing nodes when inbound responses stop (issue #724)")

		// And it keeps re-driving on subsequent ticks while still incomplete.
		n2 := rs.calledN()
		router.maintenanceTick()
		require.Greater(t, rs.calledN(), n2,
			"timer keeps re-requesting each tick until the acquire completes or hits the cap")

		require.Equal(t, txSetID, rs.lastCall().txSetID, "re-request targets the same tx-set")
		require.NotEmpty(t, rs.lastCall().nodeIDs, "re-request carries the missing node IDs")
	})
}

// The timer respects the MinInterval cadence: an acquire whose inbound path
// just requested (fresh lastRequest) is not re-fired by a tick inside the
// window, mirroring rippled's 250ms TX_ACQUIRE_TIMEOUT spacing.
func TestTxSetAcquire_TimerRespectsMinInterval(t *testing.T) {
	router, rs := newRetryRouter(t)
	ld, _ := rootOnlyTxSetLedgerData(t, 8)

	withRetryKnobs(router, time.Hour, 20, 3, func() {
		router.handleTxSetData(ld, 4)
		afterInbound := rs.calledN()
		// lastRequest was just set; a tick inside the (1h) window must not fire.
		router.maintenanceTick()
		require.Equal(t, afterInbound, rs.calledN(),
			"timer must not re-request inside the MinInterval cadence window")
	})
}

// Once MaxStallTicks consecutive no-progress ticks elapse, the timer stops
// re-requesting and marks the acquire dormant — but KEEPS its partial map,
// unlike the pre-pipelining model which deleted it. The retained map lets a
// consensus re-ask resume from where it left off; the TTL sweep reclaims a
// truly-abandoned entry (rippled's TransactionAcquire keeps mMap across
// retries and fails only after MAX_TIMEOUTS).
func TestTxSetAcquire_TimerGoesDormantAtMaxStallTicks(t *testing.T) {
	router, rs := newRetryRouter(t)
	ld, txSetID := rootOnlyTxSetLedgerData(t, 8)

	withRetryKnobs(router, 0, 3, 3, func() {
		router.handleTxSetData(ld, 4)
		// Drive ticks well past the stall cap; the acquire must go dormant
		// (retained, not deleted) and re-requests must stop.
		for range 10 {
			router.maintenanceTick()
		}
		router.txSetAcquireMu.Lock()
		state, stillTracked := router.txSetAcquire[txSetID]
		dormant := stillTracked && state.dormant
		router.txSetAcquireMu.Unlock()
		require.True(t, stillTracked,
			"acquire must be RETAINED (partial map kept) after going dormant, not deleted")
		require.True(t, dormant, "acquire must be dormant after MaxStallTicks stall ticks")

		// No further re-requests once dormant.
		n := rs.calledN()
		router.maintenanceTick()
		require.Equal(t, n, rs.calledN(), "no re-requests after the acquire goes dormant")
	})
}

// The timer must not compound with an actively-progressing inbound path. While
// the inbound lastRequest is still fresh (inside the cadence window), repeated
// maintenance ticks add ZERO extra missing-nodes requests. This pins the
// anti-compounding invariant the timer relies on: the inbound path keeps
// lastRequest fresh while making progress, and the MinInterval gate keeps the
// timer out until responses actually stop.
func TestTxSetAcquire_TimerStaysOutWhileInboundFresh(t *testing.T) {
	router, rs := newRetryRouter(t)
	ld, _ := rootOnlyTxSetLedgerData(t, 8)

	// Real (large) cadence window: the inbound request just set lastRequest, so
	// every tick below falls inside the window.
	withRetryKnobs(router, time.Hour, 20, 3, func() {
		router.handleTxSetData(ld, 4)
		afterInbound := rs.calledN()
		require.GreaterOrEqual(t, afterInbound, 1, "inbound path issues the first request")

		for range 5 {
			router.maintenanceTick()
		}
		require.Equal(t, afterInbound, rs.calledN(),
			"timer must add zero requests while the inbound lastRequest is fresh "+
				"(anti-compounding invariant)")
	})
}

// A given-up acquire is terminal to stragglers but revivable by consensus:
// after the timer drives it dormant (and latches it done), a straggler
// TMLedgerData for the same tx-set is DROPPED — it must not revive the acquire
// nor trigger a re-request (that recreate/re-request churn is what wedged the
// network). Only a consensus re-ask (MarkTxSetStillNeeded) clears the latch,
// after which the RETAINED partial map resumes — mirroring rippled's failed_
// latch that only stillNeed() clears.
func TestTxSetAcquire_GivenUpAcquireDropsStragglerRevivableByStillNeed(t *testing.T) {
	router, rs := newRetryRouter(t)
	ld, txSetID := rootOnlyTxSetLedgerData(t, 8)

	withRetryKnobs(router, 0, 3, 3, func() {
		// Create the acquire, then drive ticks past the stall cap to dormancy.
		router.handleTxSetData(ld, 4)
		for range 10 {
			router.maintenanceTick()
		}
		router.txSetAcquireMu.Lock()
		state, tracked := router.txSetAcquire[txSetID]
		dormant := tracked && state.dormant
		done := tracked && state.done
		router.txSetAcquireMu.Unlock()
		require.True(t, tracked, "given-up acquire keeps its partial map (not deleted)")
		require.True(t, dormant, "acquire must be dormant after exceeding MaxStallTicks")
		require.True(t, done, "a given-up acquire is latched terminal so stragglers are dropped")

		n := rs.calledN()

		// A straggler reply for the given-up set must be dropped: no revive, no
		// re-request.
		router.handleTxSetData(ld, 5)
		require.Equal(t, n, rs.calledN(),
			"a straggler for a given-up acquire must be dropped, not re-request")
		router.txSetAcquireMu.Lock()
		state, tracked = router.txSetAcquire[txSetID]
		stillDormant := tracked && state.dormant
		router.txSetAcquireMu.Unlock()
		require.True(t, tracked, "the acquire remains tracked after the dropped straggler")
		require.True(t, stillDormant, "a straggler must not revive a given-up acquire")

		// Consensus re-asks (stillNeed): the acquire wakes and the next timer
		// tick resumes requesting from the retained partial map.
		router.MarkTxSetStillNeeded(txSetID)
		router.maintenanceTick()
		require.Greater(t, rs.calledN(), n,
			"after a stillNeed re-ask the acquire resumes from its retained map")
		router.txSetAcquireMu.Lock()
		state, tracked = router.txSetAcquire[txSetID]
		revivedDormant := tracked && state.dormant
		router.txSetAcquireMu.Unlock()
		require.True(t, tracked, "the acquire remains tracked after revival")
		require.False(t, revivedDormant, "stillNeed must clear the dormant latch")
	})
}

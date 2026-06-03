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

// Once the attempt cap is reached with no progress, the timer drops the
// acquire instead of spinning forever (mirrors the inbound max-attempts path
// and rippled's MAX_TIMEOUTS).
func TestTxSetAcquire_TimerDropsAtMaxAttempts(t *testing.T) {
	router, rs := newRetryRouter(t)
	ld, txSetID := rootOnlyTxSetLedgerData(t, 8)

	withRetryKnobs(router, 0, 3, 3, func() {
		router.handleTxSetData(ld, 4)
		// Drive ticks well past the cap; the acquire must be dropped and
		// re-requests must stop.
		for i := 0; i < 10; i++ {
			router.maintenanceTick()
		}
		router.txSetAcquireMu.Lock()
		_, stillTracked := router.txSetAcquire[txSetID]
		router.txSetAcquireMu.Unlock()
		require.False(t, stillTracked, "acquire must be dropped after exceeding MaxAttempts")

		// No further re-requests once dropped.
		n := rs.calledN()
		router.maintenanceTick()
		require.Equal(t, n, rs.calledN(), "no re-requests after the acquire is dropped")
	})
}

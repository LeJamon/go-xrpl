package adaptor

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/ledger/inbound"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRouter_RequestLedger_TriggersGenericAcquisition covers the ledger_request
// coordinator: with a connected peer, a request for a missing ledger selects
// that peer, issues a base fetch, registers a ReasonGeneric acquisition, and
// reports the in-flight snapshot. A repeat request joins the same acquisition.
func TestRouter_RequestLedger_TriggersGenericAcquisition(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	// Register a peer reporting our own tip — matching hash means no catch-up
	// acquisition fires on its own, so any later base request is ours.
	r.handleMessage(statusChangeMessage(t, peermanagement.PeerID(7), closed.Sequence(), closed.Hash()))
	require.Empty(t, rs.legacyCalls())

	var target [32]byte
	target[0] = 0x42

	snap, started := r.RequestLedger(target, 0)
	require.True(t, started)
	require.NotNil(t, snap)
	assert.Equal(t, false, snap["have_header"])

	calls := rs.legacyCalls()
	require.Len(t, calls, 1, "exactly one base fetch must be issued")
	assert.Equal(t, target, calls[0].hash)
	assert.Equal(t, uint64(7), calls[0].peerID)

	il := r.fetchTracker.Find(target)
	require.NotNil(t, il, "the acquisition must be registered")
	assert.Equal(t, inbound.ReasonGeneric, il.Reason())

	// A second request joins the in-flight acquisition; no duplicate fetch.
	_, started2 := r.RequestLedger(target, 0)
	assert.True(t, started2)
	assert.Len(t, rs.legacyCalls(), 1, "repeat request must not re-issue the fetch")
}

// TestRouter_RequestLedger_NoPeers verifies that with no connected peer the
// coordinator reports it could not start an acquisition.
func TestRouter_RequestLedger_NoPeers(t *testing.T) {
	r, _, rs, _ := makeRouter(t)

	var target [32]byte
	target[0] = 0x99

	snap, started := r.RequestLedger(target, 0)
	assert.False(t, started)
	assert.Nil(t, snap)
	assert.Empty(t, rs.legacyCalls())
	assert.Nil(t, r.fetchTracker.Find(target))
}

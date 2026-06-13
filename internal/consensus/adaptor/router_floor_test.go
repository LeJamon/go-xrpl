package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRouter_RequestLedger_Floor_DeclinesBelowBoundary verifies that the
// ledger_request acquisition path refuses to fetch a ledger below the
// online-delete floor — mirroring rippled's LedgerMaster::shouldAcquire, which
// does not acquire a missing ledger below minimumOnline. No fetch is issued.
func TestRouter_RequestLedger_Floor_DeclinesBelowBoundary(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	r.SetMinimumOnlineFloor(stubFloor(100))

	// Register a peer so the only reason a fetch wouldn't fire is the floor.
	r.handleMessage(statusChangeMessage(t, peermanagement.PeerID(7), closed.Sequence(), closed.Hash()))

	var target [32]byte
	target[0] = 0x42

	snap, started, _ := r.RequestLedger(target, 50) // seq 50 < floor 100
	assert.False(t, started, "acquisition below the floor must not start")
	assert.Nil(t, snap)
	assert.Empty(t, rs.legacyCalls(), "no base fetch may be issued below the floor")
	assert.Empty(t, rs.replayCalls(), "no replay-delta fetch may be issued below the floor")
	assert.Nil(t, r.fetchTracker.Find(target), "no acquisition may be registered below the floor")
}

// TestRouter_RequestLedger_Floor_AllowsAtOrAboveBoundary verifies a request at
// or above the floor proceeds, and that a nil floor leaves the path unchanged.
func TestRouter_RequestLedger_Floor_AllowsAtOrAboveBoundary(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	r.SetMinimumOnlineFloor(stubFloor(50))
	r.handleMessage(statusChangeMessage(t, peermanagement.PeerID(7), closed.Sequence(), closed.Hash()))

	var target [32]byte
	target[0] = 0x42

	_, started, _ := r.RequestLedger(target, 50) // seq 50 == floor 50: allowed
	assert.True(t, started, "acquisition at the floor must proceed")
	require.Len(t, rs.legacyCalls(), 1, "a base fetch must be issued at the floor")
	assert.Equal(t, target, rs.legacyCalls()[0].hash)
}

// TestRouter_RequestLedger_NilFloor_Unchanged verifies the acquisition path is
// unrestricted when no floor is installed (online_delete off / standalone).
func TestRouter_RequestLedger_NilFloor_Unchanged(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	r.handleMessage(statusChangeMessage(t, peermanagement.PeerID(7), closed.Sequence(), closed.Hash()))

	var target [32]byte
	target[0] = 0x42

	_, started, _ := r.RequestLedger(target, 1) // a very low seq, no floor → allowed
	assert.True(t, started, "with no floor any sequence is acquirable")
	require.Len(t, rs.legacyCalls(), 1)
}

// TestRouter_HandleGetLedger_Floor_DeclinesBelowBoundary drives the legacy
// mtGET_LEDGER serve path and verifies the router declines to serve a ledger
// below the floor (no response frame is emitted), while serving one at/above it.
func TestRouter_HandleGetLedger_Floor_DeclinesBelowBoundary(t *testing.T) {
	engine := &mockEngine{}
	adaptor, rs := newTxSetWireAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)
	router := NewRouter(engine, adaptor, inbox)

	l := adaptor.LedgerService().GetClosedLedger()
	require.NotNil(t, l)
	hash := l.Hash()

	// Floor above the served ledger's sequence: it is below the boundary.
	router.SetMinimumOnlineFloor(stubFloor(l.Sequence() + 1))

	ctx := t.Context()
	go router.Run(ctx)

	req := &message.GetLedger{
		InfoType:   message.LedgerInfoBase,
		LedgerHash: hash[:],
		LedgerSeq:  l.Sequence(),
	}
	inbox <- &peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeGetLedger),
		Payload: encodePayload(t, req),
	}

	// Give the router a beat to process; assert it stays silent.
	require.Never(t, func() bool {
		return len(rs.sentTo(7)) > 0
	}, 200*time.Millisecond, 20*time.Millisecond,
		"router must not serve a ledger below the online-delete floor")
}

// TestRouter_HandleGetLedger_Floor_ServesAtOrAboveBoundary verifies the same
// serve path responds normally when the ledger is at or above the floor.
func TestRouter_HandleGetLedger_Floor_ServesAtOrAboveBoundary(t *testing.T) {
	engine := &mockEngine{}
	adaptor, rs := newTxSetWireAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)
	router := NewRouter(engine, adaptor, inbox)

	l := adaptor.LedgerService().GetClosedLedger()
	require.NotNil(t, l)
	hash := l.Hash()

	// Floor at the served ledger's own sequence: not below, must serve.
	router.SetMinimumOnlineFloor(stubFloor(l.Sequence()))

	ctx := t.Context()
	go router.Run(ctx)

	req := &message.GetLedger{
		InfoType:   message.LedgerInfoBase,
		LedgerHash: hash[:],
		LedgerSeq:  l.Sequence(),
	}
	inbox <- &peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeGetLedger),
		Payload: encodePayload(t, req),
	}

	require.Eventually(t, func() bool {
		return len(rs.sentTo(7)) > 0
	}, time.Second, 10*time.Millisecond,
		"router must serve a ledger at or above the floor")
}

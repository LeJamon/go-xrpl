package adaptor

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeRouterWithEngine wires a router against a real adaptor + recording
// sender like makeRouter, but installs a mockEngine so tests can assert
// on engine notifications from the router.
func makeRouterWithEngine(t *testing.T) (*Router, *mockEngine, *service.Service) {
	t.Helper()
	svc := newTestLedgerService(t)
	a, _ := newRecordingAdaptor(t, svc)
	inbox := make(chan *peermanagement.InboundMessage, 8)
	engine := &mockEngine{}
	r := NewRouter(engine, a, nil, inbox)
	return r, engine, svc
}

// TestRouter_AdoptVerifiedLedger_NotifiesEngine pins issue #359. When a
// replay-delta acquisition succeeds and the router adopts the new
// ledger via adoptVerifiedLedger, it MUST notify the consensus engine
// via Engine.OnLedger. Without this notification the engine sits
// indefinitely in ModeWrongLedger after acquisition (rcl/engine.go:801)
// because OnLedger is the recovery primitive that flips back into
// ModeSwitchedLedger and re-enters consensus.
func TestRouter_AdoptVerifiedLedger_NotifiesEngine(t *testing.T) {
	r, engine, svc := makeRouterWithEngine(t)
	resp, expectedHash, seq := buildEmptyClosedSuccessorResponse(t, svc)

	parent := svc.GetClosedLedger()
	require.NoError(t, r.startReplayDeltaAcquisition(seq, expectedHash, 7, parent))

	payload, err := message.Encode(resp)
	require.NoError(t, err)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeReplayDeltaResponse),
		Payload: payload,
	})

	ledgers := engine.getLedgers()
	require.Len(t, ledgers, 1, "router must notify engine of adopted ledger after replay-delta succeeds")
	assert.Equal(t, consensus.LedgerID(expectedHash), ledgers[0])
}

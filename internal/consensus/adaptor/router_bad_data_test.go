package adaptor

import (
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// badDataCall captures one IncPeerBadData invocation from the router.
type badDataCall struct {
	peerID uint64
	reason string
}

// badDataRecordingSender extends recordingSender to observe
// IncPeerBadData calls. Using the same recording shape as the other
// router tests so the seam for assertions is consistent: router is the
// unit under test, the sender is the only surface we inspect.
type badDataRecordingSender struct {
	recordingSender
	badDataMu    sync.Mutex
	badDataCalls []badDataCall
}

func (s *badDataRecordingSender) IncPeerBadData(peerID uint64, reason string) {
	s.badDataMu.Lock()
	defer s.badDataMu.Unlock()
	s.badDataCalls = append(s.badDataCalls, badDataCall{peerID: peerID, reason: reason})
}

func (s *badDataRecordingSender) getBadDataCalls() []badDataCall {
	s.badDataMu.Lock()
	defer s.badDataMu.Unlock()
	out := make([]badDataCall, len(s.badDataCalls))
	copy(out, s.badDataCalls)
	return out
}

// makeRouterWithBadDataRecorder wires a router whose NetworkSender both
// records replay/legacy calls (inherited from recordingSender) AND
// captures IncPeerBadData calls — the only surface the router touches
// to charge a peer for misbehavior.
func makeRouterWithBadDataRecorder(t *testing.T) (*Router, *badDataRecordingSender) {
	t.Helper()
	svc := newTestLedgerService(t)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	rs := &badDataRecordingSender{
		recordingSender: recordingSender{peerSupportsReplay: true},
	}
	a := New(Config{
		LedgerService: svc,
		Sender:        rs,
		Identity:      identity,
	})
	inbox := make(chan *peermanagement.InboundMessage, 8)
	r := NewRouter(nil, a, inbox)
	return r, rs
}

// TestRouter_HandleReplayDeltaResponse_DecodeFailure_ChargesPeer
// verifies the router increments bad-data against the peer when an
// inbound mtREPLAY_DELTA_RESPONSE frame is undecodable. This covers
// the "garbage on the wire" case — a peer that ships random bytes
// should accumulate bad-data credit toward eviction.
func TestRouter_HandleReplayDeltaResponse_DecodeFailure_ChargesPeer(t *testing.T) {
	r, rs := makeRouterWithBadDataRecorder(t)

	// A payload that is not a valid protobuf ReplayDeltaResponse.
	garbage := []byte{0xFF, 0xFE, 0xFD, 0xFC}

	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  42,
		Type:    uint16(message.TypeReplayDeltaResponse),
		Payload: garbage,
	})

	calls := rs.getBadDataCalls()
	require.Len(t, calls, 1,
		"malformed replay-delta response must trigger exactly one IncPeerBadData call")
	assert.Equal(t, uint64(42), calls[0].peerID,
		"bad-data must be attributed to the peer that sent the garbage")
	assert.Equal(t, "replay-delta-resp-decode", calls[0].reason,
		"reason label must identify the failure class for diagnostics")
}

// TestRouter_HandleReplayDeltaResponse_VerifyFailure_ChargesPeer
// verifies the router charges the peer when GotResponse rejects the
// payload (e.g. peer-signaled error code). Mirrors the behavior flow
// that rippled's LedgerDeltaAcquire uses to charge feeInvalidData on
// verification failure.
func TestRouter_HandleReplayDeltaResponse_VerifyFailure_ChargesPeer(t *testing.T) {
	r, rs := makeRouterWithBadDataRecorder(t)

	// Arm an acquisition so the verifier runs on the response.
	target := [32]byte{0xAB}
	svc := r.adaptor.LedgerService()
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	require.NoError(t, r.startReplayDeltaAcquisition(parent.Sequence()+1, target, 7, parent))

	// A response with matching hash but a peer-signaled error — the
	// verifier in GotResponse rejects it.
	bad := &message.ReplayDeltaResponse{
		LedgerHash: target[:],
		Error:      message.ReplyErrorNoLedger,
	}
	payload, err := message.Encode(bad)
	require.NoError(t, err)

	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeReplayDeltaResponse),
		Payload: payload,
	})

	calls := rs.getBadDataCalls()
	require.Len(t, calls, 1,
		"verification failure must trigger exactly one IncPeerBadData call")
	assert.Equal(t, uint64(7), calls[0].peerID)
	assert.Equal(t, "replay-delta-verify", calls[0].reason)
}

// TestRouter_HandleLedgerData_DecodeFailure_ChargesPeer verifies the
// same pattern for mtLEDGER_DATA — a peer that ships malformed
// ledger-data accrues bad-data credit.
func TestRouter_HandleLedgerData_DecodeFailure_ChargesPeer(t *testing.T) {
	r, rs := makeRouterWithBadDataRecorder(t)

	garbage := []byte{0xFF, 0xFE, 0xFD}

	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  99,
		Type:    uint16(message.TypeLedgerData),
		Payload: garbage,
	})

	calls := rs.getBadDataCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, uint64(99), calls[0].peerID)
	assert.Equal(t, "ledger-data-decode", calls[0].reason)
}

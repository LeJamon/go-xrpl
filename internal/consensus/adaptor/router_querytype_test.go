package adaptor

import (
	"bytes"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// querytypeRecorder records both IncPeerBadData and SendToPeer so the
// query_type validation test can assert that a present-but-invalid query_type
// charges the peer and serves nothing, while a valid (qtINDIRECT) or absent
// one serves normally. badDataCall and sentFrame are shared with the other
// router tests in this package.
type querytypeRecorder struct {
	recordingSender
	qmu     sync.Mutex
	badData []badDataCall
	sent    []sentFrame
}

func (s *querytypeRecorder) IncPeerBadData(peerID uint64, reason string) {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	s.badData = append(s.badData, badDataCall{peerID: peerID, reason: reason})
}

func (s *querytypeRecorder) SendToPeer(peerID uint64, frame []byte) error {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	s.sent = append(s.sent, sentFrame{peerID: peerID, frame: append([]byte(nil), frame...)})
	return nil
}

func (s *querytypeRecorder) snapshot() ([]badDataCall, []sentFrame) {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	return append([]badDataCall(nil), s.badData...), append([]sentFrame(nil), s.sent...)
}

func makeRouterWithQueryTypeRecorder(t *testing.T) (*Router, *querytypeRecorder) {
	t.Helper()
	svc := newTestLedgerService(t)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	rs := &querytypeRecorder{recordingSender: recordingSender{peerSupportsReplay: true}}
	a := New(Config{
		LedgerService: svc,
		Sender:        rs,
		Identity:      identity,
	})
	inbox := make(chan *peermanagement.InboundMessage, 8)
	r := NewRouter(nil, a, inbox)
	return r, rs
}

// TestRouter_GetLedger_QueryTypeValidation pins rippled's
// onMessage(TMGetLedger) query_type rule: qtINDIRECT is the only valid value.
// A present-but-different value is invalid data — charge the peer and drop the
// request without disconnecting; an absent or qtINDIRECT value is served. The
// tx-set is served via a cached liTS_CANDIDATE request because that path is
// exempt from load-shedding, so the positive cases are deterministic.
func TestRouter_GetLedger_QueryTypeValidation(t *testing.T) {
	blobs := [][]byte{
		bytes.Repeat([]byte{0x11}, 16),
		bytes.Repeat([]byte{0x55}, 16),
	}

	t.Run("invalid query_type charges peer and serves nothing", func(t *testing.T) {
		r, rs := makeRouterWithQueryTypeRecorder(t)
		ts, err := r.adaptor.BuildTxSet(blobs)
		require.NoError(t, err)
		id := ts.ID()

		invalid := message.LedgerQueryType(7)
		req := &message.GetLedger{
			InfoType:   message.LedgerInfoTsCandidate,
			LedgerHash: id[:],
			QueryType:  &invalid,
		}
		r.handleMessage(&peermanagement.InboundMessage{
			PeerID:  42,
			Type:    uint16(message.TypeGetLedger),
			Payload: encodePayload(t, req),
		})

		bd, sent := rs.snapshot()
		require.Len(t, bd, 1, "invalid query_type must charge bad data exactly once")
		assert.Equal(t, uint64(42), bd[0].peerID)
		assert.Equal(t, "get-ledger-bad-querytype", bd[0].reason)
		assert.Empty(t, sent, "invalid query_type must not serve a response")
	})

	t.Run("qtINDIRECT serves normally", func(t *testing.T) {
		r, rs := makeRouterWithQueryTypeRecorder(t)
		ts, err := r.adaptor.BuildTxSet(blobs)
		require.NoError(t, err)
		id := ts.ID()

		valid := message.QueryTypeIndirect
		req := &message.GetLedger{
			InfoType:   message.LedgerInfoTsCandidate,
			LedgerHash: id[:],
			QueryType:  &valid,
		}
		r.handleMessage(&peermanagement.InboundMessage{
			PeerID:  7,
			Type:    uint16(message.TypeGetLedger),
			Payload: encodePayload(t, req),
		})

		bd, sent := rs.snapshot()
		assert.Empty(t, bd, "qtINDIRECT must not charge bad data")
		require.Len(t, sent, 1, "qtINDIRECT must serve the cached tx-set")
		assert.Equal(t, uint64(7), sent[0].peerID)
	})

	t.Run("absent query_type serves normally", func(t *testing.T) {
		r, rs := makeRouterWithQueryTypeRecorder(t)
		ts, err := r.adaptor.BuildTxSet(blobs)
		require.NoError(t, err)
		id := ts.ID()

		req := &message.GetLedger{
			InfoType:   message.LedgerInfoTsCandidate,
			LedgerHash: id[:],
		}
		r.handleMessage(&peermanagement.InboundMessage{
			PeerID:  9,
			Type:    uint16(message.TypeGetLedger),
			Payload: encodePayload(t, req),
		})

		bd, sent := rs.snapshot()
		assert.Empty(t, bd, "absent query_type must not charge bad data")
		require.Len(t, sent, 1, "absent query_type must serve the cached tx-set")
		assert.Equal(t, uint64(9), sent[0].peerID)
	})
}

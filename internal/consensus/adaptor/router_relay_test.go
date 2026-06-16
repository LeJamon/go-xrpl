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

// relayRecorder is a NetworkSender that records SendToPeer / NotePeerHasTxSet
// and serves configurable PeerWithLedger / PeerWithTxSet answers, so the
// GetLedger relay path can be exercised without a real overlay.
type relayRecorder struct {
	noopSender
	mu          sync.Mutex
	sent        []sentFrame
	noted       []notedTxSet
	lastExclude uint64

	ledgerPeer uint64
	ledgerOK   bool
	txsetPeer  uint64
	txsetOK    bool
}

type notedTxSet struct {
	peerID uint64
	hash   [32]byte
}

func (s *relayRecorder) SendToPeer(peerID uint64, frame []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, sentFrame{peerID: peerID, frame: append([]byte(nil), frame...)})
	return nil
}

func (s *relayRecorder) PeerWithLedger(_ [32]byte, _ uint32, exclude uint64) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastExclude = exclude
	return s.ledgerPeer, s.ledgerOK
}

func (s *relayRecorder) PeerWithTxSet(_ [32]byte, exclude uint64) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastExclude = exclude
	return s.txsetPeer, s.txsetOK
}

func (s *relayRecorder) NotePeerHasTxSet(peerID uint64, hash [32]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noted = append(s.noted, notedTxSet{peerID: peerID, hash: hash})
}

func (s *relayRecorder) sentFrames() []sentFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sentFrame(nil), s.sent...)
}

func (s *relayRecorder) notedTxSets() []notedTxSet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]notedTxSet(nil), s.noted...)
}

func (s *relayRecorder) excludeArg() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastExclude
}

func makeRouterWithRelayRecorder(t *testing.T) (*Router, *relayRecorder) {
	t.Helper()
	svc := newTestLedgerService(t)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	rs := &relayRecorder{}
	a := New(Config{
		LedgerService: svc,
		Sender:        rs,
		Identity:      identity,
	})
	inbox := make(chan *peermanagement.InboundMessage, 8)
	r := NewRouter(nil, a, inbox)
	return r, rs
}

// TestRouter_GetLedger_RelayOnMiss_Ledger pins rippled's getLedger relay:
// when a peer asks for a ledger we don't have and the request is an original
// indirect one (query_type present, no cookie), forward it to a peer that
// advertises the ledger — stamped with request_cookie = the requester's id —
// rather than dropping it.
func TestRouter_GetLedger_RelayOnMiss_Ledger(t *testing.T) {
	r, rs := makeRouterWithRelayRecorder(t)
	rs.ledgerPeer, rs.ledgerOK = 99, true

	hash := bytes.Repeat([]byte{0xAB}, 32)
	qt := message.QueryTypeIndirect
	req := &message.GetLedger{
		InfoType:   message.LedgerInfoBase,
		LedgerHash: hash,
		QueryType:  &qt,
	}
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  42,
		Type:    uint16(message.TypeGetLedger),
		Payload: encodePayload(t, req),
	})

	sent := rs.sentFrames()
	require.Len(t, sent, 1, "miss must relay exactly one frame")
	assert.Equal(t, uint64(99), sent[0].peerID, "relayed to the peer that has the ledger")
	assert.Equal(t, uint64(42), rs.excludeArg(), "selection must exclude the original requester")

	msgType, decoded := decodeFrame(t, sent[0].frame)
	require.Equal(t, message.TypeGetLedger, msgType)
	relayed := decoded.(*message.GetLedger)
	assert.Equal(t, uint64(42), relayed.RequestCookie, "cookie stamped with the requester id")
	assert.Equal(t, hash, relayed.LedgerHash)
	require.NotNil(t, relayed.QueryType)
	assert.Equal(t, message.QueryTypeIndirect, *relayed.QueryType)
}

// TestRouter_GetLedger_NoRelayWhenSeqOnly pins rippled's getLedger: a relay
// is attempted only from the has_ledgerhash() branch (PeerImp.cpp:3165/3175).
// A seq-only request (no ledger_hash) that misses locally is dropped, never
// relayed — even when a peer would cover the seq range.
func TestRouter_GetLedger_NoRelayWhenSeqOnly(t *testing.T) {
	r, rs := makeRouterWithRelayRecorder(t)
	rs.ledgerPeer, rs.ledgerOK = 33, true // would relay if the predicate allowed it

	qt := message.QueryTypeIndirect
	req := &message.GetLedger{
		InfoType:  message.LedgerInfoBase,
		LedgerSeq: 4242,
		QueryType: &qt,
	}
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  42,
		Type:    uint16(message.TypeGetLedger),
		Payload: encodePayload(t, req),
	})

	assert.Empty(t, rs.sentFrames(), "a seq-only request must not be relayed")
}

// TestRouter_GetLedger_RelayOnMiss_TxSet pins rippled's getTxSet relay for
// liTS_CANDIDATE: a tx-set we don't have is forwarded to a peer that
// advertised it (getPeerWithTree), cookie-stamped for reply routing.
func TestRouter_GetLedger_RelayOnMiss_TxSet(t *testing.T) {
	r, rs := makeRouterWithRelayRecorder(t)
	rs.txsetPeer, rs.txsetOK = 7, true

	hash := bytes.Repeat([]byte{0xCD}, 32)
	qt := message.QueryTypeIndirect
	req := &message.GetLedger{
		InfoType:   message.LedgerInfoTsCandidate,
		LedgerHash: hash,
		QueryType:  &qt,
	}
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  21,
		Type:    uint16(message.TypeGetLedger),
		Payload: encodePayload(t, req),
	})

	sent := rs.sentFrames()
	require.Len(t, sent, 1)
	assert.Equal(t, uint64(7), sent[0].peerID)
	assert.Equal(t, uint64(21), rs.excludeArg())

	_, decoded := decodeFrame(t, sent[0].frame)
	relayed := decoded.(*message.GetLedger)
	assert.Equal(t, uint64(21), relayed.RequestCookie)
	assert.Equal(t, message.LedgerInfoTsCandidate, relayed.InfoType)
}

// TestRouter_GetLedger_NoRelayWhenCookieSet pins loop prevention: a request
// that already carries a request_cookie has been relayed once and must never
// be relayed again, even when query_type is present and we lack the tx-set.
func TestRouter_GetLedger_NoRelayWhenCookieSet(t *testing.T) {
	r, rs := makeRouterWithRelayRecorder(t)
	rs.txsetPeer, rs.txsetOK = 7, true // would relay if the predicate allowed it

	hash := bytes.Repeat([]byte{0xCD}, 32)
	qt := message.QueryTypeIndirect
	req := &message.GetLedger{
		InfoType:      message.LedgerInfoTsCandidate,
		LedgerHash:    hash,
		QueryType:     &qt,
		RequestCookie: 555, // already relayed by an upstream peer
	}
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  21,
		Type:    uint16(message.TypeGetLedger),
		Payload: encodePayload(t, req),
	})

	assert.Empty(t, rs.sentFrames(), "a cookied request must not be relayed again")
}

// TestRouter_GetLedger_NoRelayWhenQueryTypeAbsent pins that a direct request
// (no query_type) is never relayed — rippled relays only indirect requests.
func TestRouter_GetLedger_NoRelayWhenQueryTypeAbsent(t *testing.T) {
	r, rs := makeRouterWithRelayRecorder(t)
	rs.txsetPeer, rs.txsetOK = 7, true

	hash := bytes.Repeat([]byte{0xCD}, 32)
	req := &message.GetLedger{
		InfoType:   message.LedgerInfoTsCandidate,
		LedgerHash: hash,
	}
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  21,
		Type:    uint16(message.TypeGetLedger),
		Payload: encodePayload(t, req),
	})

	assert.Empty(t, rs.sentFrames(), "a request without query_type must not be relayed")
}

// TestRouter_GetLedger_NoRelayWhenNoCandidate pins the drop fallback: when no
// peer advertises the requested data, the request is silently dropped.
func TestRouter_GetLedger_NoRelayWhenNoCandidate(t *testing.T) {
	r, rs := makeRouterWithRelayRecorder(t)
	rs.txsetOK = false // no peer has it

	hash := bytes.Repeat([]byte{0xCD}, 32)
	qt := message.QueryTypeIndirect
	req := &message.GetLedger{
		InfoType:   message.LedgerInfoTsCandidate,
		LedgerHash: hash,
		QueryType:  &qt,
	}
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  21,
		Type:    uint16(message.TypeGetLedger),
		Payload: encodePayload(t, req),
	})

	assert.Empty(t, rs.sentFrames(), "no candidate peer means the request is dropped")
}

// TestRouter_LedgerData_RoutedBackByCookie pins rippled's
// onMessage(TMLedgerData) cookie branch: a reply carrying a request_cookie is
// forwarded to the original requester named by the cookie, with the cookie
// cleared, and is not consumed locally.
func TestRouter_LedgerData_RoutedBackByCookie(t *testing.T) {
	r, rs := makeRouterWithRelayRecorder(t)

	hash := bytes.Repeat([]byte{0xEE}, 32)
	ld := &message.LedgerData{
		LedgerHash:    hash,
		LedgerSeq:     12345,
		InfoType:      message.LedgerInfoBase,
		Nodes:         []message.LedgerNode{{NodeData: []byte{0x01, 0x02, 0x03}}},
		RequestCookie: 77, // our local id for the original requester
	}
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  5, // the peer that served the relayed request
		Type:    uint16(message.TypeLedgerData),
		Payload: encodePayload(t, ld),
	})

	sent := rs.sentFrames()
	require.Len(t, sent, 1, "a cookied reply must be routed back exactly once")
	assert.Equal(t, uint64(77), sent[0].peerID, "routed to the original requester (cookie holder)")

	msgType, decoded := decodeFrame(t, sent[0].frame)
	require.Equal(t, message.TypeLedgerData, msgType)
	out := decoded.(*message.LedgerData)
	assert.Equal(t, uint32(0), out.RequestCookie, "cookie cleared before forwarding")
	assert.Equal(t, hash, out.LedgerHash)
	require.Len(t, out.Nodes, 1)
}

// TestRouter_HaveSet_RecordsTxSetAdvertisement pins that an inbound
// mtHAVE_TRANSACTION_SET{tsHAVE} records the (peer, tx-set) pair so a later
// unsatisfiable GetLedger can be relayed to that peer.
func TestRouter_HaveSet_RecordsTxSetAdvertisement(t *testing.T) {
	r, rs := makeRouterWithRelayRecorder(t)

	hash := bytes.Repeat([]byte{0x9A}, 32)
	have := &message.HaveTransactionSet{
		Status: message.TxSetStatusHave,
		Hash:   hash,
	}
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  88,
		Type:    uint16(message.TypeHaveSet),
		Payload: encodePayload(t, have),
	})

	noted := rs.notedTxSets()
	require.Len(t, noted, 1, "a tsHAVE advertisement must be recorded")
	assert.Equal(t, uint64(88), noted[0].peerID)
	assert.Equal(t, [32]byte(hash), noted[0].hash)
}

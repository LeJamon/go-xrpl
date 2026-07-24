package adaptor

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// acqRecordingSender captures the indirect flag passed to the legacy
// inbound-ledger node-fetch builders (RequestStateNodes / RequestTransactionNodes)
// so the requester-side query_type escalation can be pinned. Other
// NetworkSender methods inherit from noopSender.
type acqRecordingSender struct {
	noopSender
	mu       sync.Mutex
	baseInd  []bool
	stateInd []bool
	txInd    []bool
	depths   []uint32
}

func (s *acqRecordingSender) RequestLedgerBaseFromPeer(_ uint64, _ [32]byte, _ uint32, indirect bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.baseInd = append(s.baseInd, indirect)
	return nil
}

func (s *acqRecordingSender) RequestStateNodes(_ uint64, _ [32]byte, _ [][]byte, queryDepth uint32, indirect bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stateInd = append(s.stateInd, indirect)
	s.depths = append(s.depths, queryDepth)
	return nil
}

func (s *acqRecordingSender) RequestTransactionNodes(_ uint64, _ [32]byte, _ [][]byte, _ uint32, indirect bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.txInd = append(s.txInd, indirect)
	return nil
}

func (s *acqRecordingSender) queryDepths() []uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint32(nil), s.depths...)
}

func (s *acqRecordingSender) stateIndirects() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.stateInd...)
}

func (s *acqRecordingSender) baseIndirects() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.baseInd...)
}

func TestRequestAcquisitionBase_QueryTypeEscalation(t *testing.T) {
	svc := newTestLedgerService(t)
	rs := &acqRecordingSender{}
	router := NewRouter(&mockEngine{}, New(Config{LedgerService: svc, Sender: rs}), nil)
	il := inbound.New([32]byte{0xAB}, 42, 7, serveTestLogger())

	router.requestAcquisitionBase(il)
	require.NotEmpty(t, rs.baseIndirects())
	for _, indirect := range rs.baseIndirects() {
		assert.False(t, indirect)
	}

	now := time.Now()
	require.Equal(t, inbound.TimerEscalate, il.OnTimer(now.Add(time.Hour)))
	router.requestAcquisitionBase(il)
	got := rs.baseIndirects()
	require.Greater(t, len(got), len(il.Peers()))
	for _, indirect := range got[len(il.Peers()):] {
		assert.True(t, indirect)
	}
}

func TestEncodeLedgerBaseRequest_QueryType(t *testing.T) {
	for _, tc := range []struct {
		name     string
		indirect bool
	}{
		{name: "direct"},
		{name: "indirect", indirect: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame, err := encodeLedgerBaseRequest([32]byte{0xAB}, 42, tc.indirect)
			require.NoError(t, err)
			header, err := message.DecodeHeader(frame)
			require.NoError(t, err)
			decoded, err := message.Decode(header.MessageType, frame[header.HeaderSize():])
			require.NoError(t, err)
			req := decoded.(*message.GetLedger)
			if tc.indirect {
				require.NotNil(t, req.QueryType)
				assert.Equal(t, message.QueryTypeIndirect, *req.QueryType)
			} else {
				assert.Nil(t, req.QueryType)
			}
		})
	}
}

// TestRequestMissingAcquisitionNodes_QueryTypeEscalation pins issue #977's
// requester-side escalation for the LEGACY inbound-ledger path: a fresh
// acquisition fetches its outstanding state/tx nodes directly (query_type
// absent, non-relayable), and once it has counted a no-progress timeout the
// requests go indirect (query_type=qtINDIRECT) so peers relay them on our
// behalf. This is the legacy-path analogue of rippled's InboundLedger::trigger
// timeouts_ != 0 gate (InboundLedger.cpp:531); the symmetric tx-set path is
// covered by TestTxSetRetry_QueryTypeEscalation.
func TestRequestMissingAcquisitionNodes_QueryTypeEscalation(t *testing.T) {
	svc := newTestLedgerService(t)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	rs := &acqRecordingSender{}
	a := New(Config{
		LedgerService: svc,
		Sender:        rs,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
	router := NewRouter(&mockEngine{}, a, nil)

	// Serve the closed ledger's base (header + state root) into a fresh
	// acquisition so it has outstanding state nodes but an incomplete tree.
	l := svc.GetClosedLedger()
	require.NotNil(t, l)
	il := inbound.New(l.Hash(), l.Sequence(), 7, serveTestLogger())
	require.NoError(t, il.GotBase(router.buildLedgerBaseNodes(l)))
	require.NotEmpty(t, il.NeedsMissingNodeIDs(), "acquisition must have outstanding state nodes")

	// First attempt, before any timeout → direct.
	require.Equal(t, 0, il.Timeouts())
	router.requestMissingAcquisitionNodes(il, 0)
	got := rs.stateIndirects()
	require.Len(t, got, 1, "first attempt must issue one state-node request")
	assert.False(t, got[0],
		"first-attempt state-node request must NOT carry query_type (directly routed)")

	// The useful base reply consumes the next timer interval without counting a
	// timeout. The following no-progress fire latches relayable mode.
	now := time.Now()
	require.Equal(t, inbound.TimerRefresh, il.OnTimer(now.Add(time.Hour)))
	require.Equal(t, inbound.TimerEscalate, il.OnTimer(now.Add(2*time.Hour)))
	require.Greater(t, il.Timeouts(), 0)
	router.requestMissingAcquisitionNodes(il, 0)
	got = rs.stateIndirects()
	require.Len(t, got, 2, "post-timeout attempt must issue a second state-node request")
	assert.True(t, got[1],
		"post-timeout state-node request must carry query_type=qtINDIRECT so peers relay it")
	assert.Equal(t, []uint32{0, 0}, rs.queryDepths(),
		"timeout fanout must request only the named nodes")
}

func TestRequestMissingAcquisitionNodes_ReplyUsesOneLevelFatNodes(t *testing.T) {
	svc := newTestLedgerService(t)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	rs := &acqRecordingSender{}
	a := New(Config{LedgerService: svc, Sender: rs, Identity: identity, Validators: []consensus.NodeID{identity.NodeID}})
	router := NewRouter(&mockEngine{}, a, nil)
	l := svc.GetClosedLedger()
	il := inbound.New(l.Hash(), l.Sequence(), 7, serveTestLogger())
	require.NoError(t, il.GotBase(router.buildLedgerBaseNodes(l)))

	router.requestMissingAcquisitionNodes(il, 7)
	assert.Equal(t, []uint32{1}, rs.queryDepths(),
		"reply-driven requests must ask for one descendant level")
}

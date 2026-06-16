package adaptor

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
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
	stateInd []bool
	txInd    []bool
}

func (s *acqRecordingSender) RequestStateNodes(_ uint64, _ [32]byte, _ [][]byte, indirect bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stateInd = append(s.stateInd, indirect)
	return nil
}

func (s *acqRecordingSender) RequestTransactionNodes(_ uint64, _ [32]byte, _ [][]byte, indirect bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.txInd = append(s.txInd, indirect)
	return nil
}

func (s *acqRecordingSender) stateIndirects() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.stateInd...)
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
	router.requestMissingAcquisitionNodes(il)
	got := rs.stateIndirects()
	require.Len(t, got, 1, "first attempt must issue one state-node request")
	assert.False(t, got[0],
		"first-attempt state-node request must NOT carry query_type (directly routed)")

	// A no-progress timer fire counts a timeout, latching the acquisition into
	// the aggressive (relayable) mode, mirroring rippled's timeouts_ != 0 gate.
	require.Equal(t, inbound.TimerEscalate, il.OnTimer(time.Now().Add(time.Hour)))
	require.Greater(t, il.Timeouts(), 0)
	router.requestMissingAcquisitionNodes(il)
	got = rs.stateIndirects()
	require.Len(t, got, 2, "post-timeout attempt must issue a second state-node request")
	assert.True(t, got[1],
		"post-timeout state-node request must carry query_type=qtINDIRECT so peers relay it")
}

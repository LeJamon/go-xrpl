package adaptor

import (
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

type trackingCheckSender struct {
	noopSender
	mu   sync.Mutex
	seqs []uint32
}

func TestRouter_MalformedMatchingBaseCannotFallThrough(t *testing.T) {
	svc := adg_newNonStandaloneService(t)
	engine := &mockEngine{}
	a := New(Config{LedgerService: svc})
	r := NewRouter(engine, a, make(chan *peermanagement.InboundMessage, 1))
	target := [32]byte{0x72}
	seq := svc.GetClosedLedgerIndex() + 100
	r.fetchTracker.Track(inbound.New(target, seq, 7, nil))

	ld := &message.LedgerData{
		LedgerHash: target[:],
		LedgerSeq:  seq,
		InfoType:   message.LedgerInfoBase,
		Nodes:      []message.LedgerNode{{NodeData: []byte{1, 2, 3}}},
	}
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeLedgerData),
		Payload: encodePayload(t, ld),
	})

	require.Nil(t, r.fetchTracker.Find(target))
	require.True(t, svc.NeedsInitialSync())
	require.Empty(t, engine.getLedgers())
}

func (s *trackingCheckSender) CheckTracking(seq uint32) {
	s.mu.Lock()
	s.seqs = append(s.seqs, seq)
	s.mu.Unlock()
}

func TestRouter_UnmatchedBaseCannotAdoptHeader(t *testing.T) {
	svc := adg_newNonStandaloneService(t)
	engine := &mockEngine{}
	a := New(Config{LedgerService: svc})
	r := NewRouter(engine, a, make(chan *peermanagement.InboundMessage, 1))

	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	closedHash := closed.Hash()
	closedSeq := closed.Sequence()
	target := [32]byte{0x71}

	ld := &message.LedgerData{
		LedgerHash: target[:],
		LedgerSeq:  closedSeq + 100,
		InfoType:   message.LedgerInfoBase,
		Nodes: []message.LedgerNode{
			{NodeData: []byte{1, 2, 3}},
		},
	}
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeLedgerData),
		Payload: encodePayload(t, ld),
	})

	require.True(t, svc.NeedsInitialSync())
	require.Equal(t, closedSeq, svc.GetClosedLedgerIndex())
	require.Equal(t, closedHash, svc.GetClosedLedger().Hash())
	require.Empty(t, engine.getLedgers())
	_, err := a.GetLedger(consensus.LedgerID(target))
	require.Error(t, err)
}

func TestAdaptor_ValidationQuorumChecksPeerTrackingDuringInitialSync(t *testing.T) {
	svc := adg_newNonStandaloneService(t)
	sender := &trackingCheckSender{}
	a := New(Config{LedgerService: svc, Sender: sender})
	target := consensus.LedgerID{0x44}

	a.OnLedgerFullyValidated(target, 10_000)

	sender.mu.Lock()
	require.Equal(t, []uint32{10_000}, sender.seqs)
	sender.mu.Unlock()
	require.True(t, svc.NeedsInitialSync())
	require.Equal(t, uint32(1), svc.GetValidatedLedgerIndex())
}

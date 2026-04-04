package adaptor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockEngine records calls from the router for testing.
type mockEngine struct {
	mu          sync.Mutex
	proposals   []*consensus.Proposal
	validations []*consensus.Validation
	txSets      []consensus.TxSetID
}

func (m *mockEngine) Start(context.Context) error               { return nil }
func (m *mockEngine) Stop() error                               { return nil }
func (m *mockEngine) StartRound(consensus.RoundID, bool) error  { return nil }
func (m *mockEngine) State() *consensus.RoundState              { return nil }
func (m *mockEngine) Mode() consensus.Mode                      { return consensus.ModeObserving }
func (m *mockEngine) Phase() consensus.Phase                    { return consensus.PhaseOpen }
func (m *mockEngine) IsProposing() bool                         { return false }
func (m *mockEngine) Timing() consensus.Timing                  { return consensus.DefaultTiming() }
func (m *mockEngine) GetLastCloseInfo() (int, time.Duration)    { return 0, 0 }
func (m *mockEngine) OnLedger(consensus.LedgerID, []byte) error { return nil }

func (m *mockEngine) OnProposal(p *consensus.Proposal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.proposals = append(m.proposals, p)
	return nil
}

func (m *mockEngine) OnValidation(v *consensus.Validation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.validations = append(m.validations, v)
	return nil
}

func (m *mockEngine) OnTxSet(id consensus.TxSetID, txs [][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.txSets = append(m.txSets, id)
	return nil
}

func (m *mockEngine) getProposals() []*consensus.Proposal {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*consensus.Proposal(nil), m.proposals...)
}

func (m *mockEngine) getValidations() []*consensus.Validation {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*consensus.Validation(nil), m.validations...)
}

func encodePayload(t *testing.T, msg message.Message) []byte {
	t.Helper()
	data, err := message.Encode(msg)
	require.NoError(t, err)
	return data
}

func TestRouterDispatchesProposal(t *testing.T) {
	engine := &mockEngine{}
	adaptor := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 10)

	router := NewRouter(engine, adaptor, nil, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go router.Run(ctx)

	// Create a ProposeSet message
	proposeSet := &message.ProposeSet{
		ProposeSeq:     1,
		CurrentTxHash:  make([]byte, 32),
		NodePubKey:     make([]byte, 33),
		CloseTime:      timeToXrplEpoch(time.Now()),
		Signature:      []byte{0x01, 0x02},
		PreviousLedger: make([]byte, 32),
	}
	proposeSet.NodePubKey[0] = 0x02 // valid compressed key prefix

	inbox <- &peermanagement.InboundMessage{
		PeerID:  1,
		Type:    uint16(message.TypeProposeLedger),
		Payload: encodePayload(t, proposeSet),
	}

	// Wait for processing
	time.Sleep(50 * time.Millisecond)

	proposals := engine.getProposals()
	assert.Len(t, proposals, 1)
	assert.Equal(t, uint32(1), proposals[0].Position)
}

func TestRouterDispatchesValidation(t *testing.T) {
	engine := &mockEngine{}
	adaptor := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 10)

	router := NewRouter(engine, adaptor, nil, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go router.Run(ctx)

	val := &message.Validation{
		Validation: []byte{0x01, 0x02, 0x03},
	}

	inbox <- &peermanagement.InboundMessage{
		PeerID:  2,
		Type:    uint16(message.TypeValidation),
		Payload: encodePayload(t, val),
	}

	time.Sleep(50 * time.Millisecond)

	validations := engine.getValidations()
	assert.Len(t, validations, 1)
}

func TestRouterDispatchesTransaction(t *testing.T) {
	engine := &mockEngine{}
	adaptor := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 10)

	router := NewRouter(engine, adaptor, nil, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go router.Run(ctx)

	txMsg := &message.Transaction{
		RawTransaction:   []byte{0x10, 0x20, 0x30, 0x40},
		Status:           message.TxStatusNew,
		ReceiveTimestamp: uint64(time.Now().UnixNano()),
	}

	inbox <- &peermanagement.InboundMessage{
		PeerID:  3,
		Type:    uint16(message.TypeTransaction),
		Payload: encodePayload(t, txMsg),
	}

	time.Sleep(50 * time.Millisecond)

	// Transaction should be added to the adaptor's pending pool
	txID := computeTxID([]byte{0x10, 0x20, 0x30, 0x40})
	assert.True(t, adaptor.HasTx(txID))
}

func TestRouterIgnoresUnknownMessages(t *testing.T) {
	engine := &mockEngine{}
	adaptor := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 10)

	router := NewRouter(engine, adaptor, nil, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go router.Run(ctx)

	// Send a Ping message — should be silently ignored
	inbox <- &peermanagement.InboundMessage{
		PeerID:  4,
		Type:    uint16(message.TypePing),
		Payload: []byte{0x01},
	}

	time.Sleep(50 * time.Millisecond)

	assert.Empty(t, engine.proposals)
	assert.Empty(t, engine.validations)
}

func TestRouterHandlesMalformedMessage(t *testing.T) {
	engine := &mockEngine{}
	adaptor := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 10)

	router := NewRouter(engine, adaptor, nil, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go router.Run(ctx)

	// Send garbage as a proposal — should not panic
	inbox <- &peermanagement.InboundMessage{
		PeerID:  5,
		Type:    uint16(message.TypeProposeLedger),
		Payload: []byte{0xFF, 0xFF, 0xFF},
	}

	time.Sleep(50 * time.Millisecond)

	assert.Empty(t, engine.proposals)
}

func TestRouterStopsOnContextCancel(t *testing.T) {
	engine := &mockEngine{}
	adaptor := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 10)

	router := NewRouter(engine, adaptor, nil, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		router.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Good — router exited
	case <-time.After(time.Second):
		t.Fatal("router did not stop after context cancel")
	}
}

func TestRouterStopsOnChannelClose(t *testing.T) {
	engine := &mockEngine{}
	adaptor := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 10)

	router := NewRouter(engine, adaptor, nil, inbox)

	done := make(chan struct{})
	go func() {
		router.Run(context.Background())
		close(done)
	}()

	close(inbox)

	select {
	case <-done:
		// Good — router exited
	case <-time.After(time.Second):
		t.Fatal("router did not stop after channel close")
	}
}

func TestConverterProposalRoundTrip(t *testing.T) {
	original := &consensus.Proposal{
		Round: consensus.RoundID{
			Seq:        5,
			ParentHash: [32]byte{0x01},
		},
		NodeID:         consensus.NodeID{0x02, 0x03},
		Position:       3,
		TxSet:          consensus.TxSetID{0x04},
		CloseTime:      time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
		Signature:      []byte{0x05, 0x06},
		PreviousLedger: consensus.LedgerID{0x01},
	}

	msg := ProposalToMessage(original)
	restored := ProposalFromMessage(msg)

	assert.Equal(t, original.Position, restored.Position)
	assert.Equal(t, original.NodeID, restored.NodeID)
	assert.Equal(t, original.TxSet, restored.TxSet)
	assert.Equal(t, original.PreviousLedger, restored.PreviousLedger)
	assert.Equal(t, original.Signature, restored.Signature)
	// CloseTime loses sub-second precision due to XRPL epoch (seconds)
	assert.Equal(t, original.CloseTime.Unix(), restored.CloseTime.Unix())
}

func TestConverterTransactionRoundTrip(t *testing.T) {
	blob := []byte{0x12, 0x00, 0x00, 0x24, 0x00, 0x00, 0x00, 0x01}
	msg := TransactionToMessage(blob)
	restored := TransactionFromMessage(msg)
	assert.Equal(t, blob, restored)
}

func TestConverterHaveSetRoundTrip(t *testing.T) {
	id := consensus.TxSetID{0x01, 0x02, 0x03}
	msg := HaveSetToMessage(id, message.TxSetStatusNeed)
	restoredID, restoredStatus := HaveSetFromMessage(msg)
	assert.Equal(t, id, restoredID)
	assert.Equal(t, message.TxSetStatusNeed, restoredStatus)
}

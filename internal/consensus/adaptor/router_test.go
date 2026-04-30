package adaptor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/protocol"
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

func (m *mockEngine) OnProposal(p *consensus.Proposal, _ uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.proposals = append(m.proposals, p)
	return nil
}

func (m *mockEngine) OnValidation(v *consensus.Validation, _ uint64) error {
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

	// Create a ProposeSet message with sizes inside the bounds
	// validateProposeBounds enforces (post-PR #264 review: 64-72 byte
	// signature, 33-byte pubkey, 32-byte hashes).
	proposeSet := &message.ProposeSet{
		ProposeSeq:     1,
		CurrentTxHash:  make([]byte, 32),
		NodePubKey:     make([]byte, 33),
		CloseTime:      timeToXrplEpoch(time.Now()),
		Signature:      make([]byte, signatureMinLen),
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

	// Build a valid STValidation binary payload.
	testVal := &consensus.Validation{
		Full:      true,
		LedgerSeq: 42,
		SignTime:  time.Unix(protocol.RippleEpochUnix+828618000, 0), // XRPL epoch + offset
		LoadFee:   0,
	}
	copy(testVal.LedgerID[:], []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32})
	copy(testVal.NodeID[:], []byte{0x02, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32})
	testVal.Signature = make([]byte, 70) // dummy signature
	val := &message.Validation{
		Validation: SerializeSTValidation(testVal),
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

// countingSender wraps noopSender with a counter on UpdateRelaySlot
// so router tests can assert how many times the reduce-relay slot
// was fed. B3: also captures the seenPeers argument so tests can
// verify the slot is fed with the full known-haver set from the
// overlay's reverse index, not just the duplicate's originator.
type countingSender struct {
	noopSender
	mu            sync.Mutex
	calls         []countingRelaySlotCall
	peersThatHave map[[32]byte][]uint64
}

type countingRelaySlotCall struct {
	Validator  []byte
	OriginPeer uint64
	SeenPeers  []uint64
}

func (s *countingSender) UpdateRelaySlot(validator []byte, originPeer uint64, seenPeers []uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(validator))
	copy(cp, validator)
	seenCp := append([]uint64(nil), seenPeers...)
	s.calls = append(s.calls, countingRelaySlotCall{Validator: cp, OriginPeer: originPeer, SeenPeers: seenCp})
}

// PeersThatHave returns the preconfigured set for suppressionHash.
// Router tests seed this to simulate the overlay having already
// relayed a message to a known peer set.
func (s *countingSender) PeersThatHave(suppressionHash [32]byte) []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.peersThatHave == nil {
		return nil
	}
	return append([]uint64(nil), s.peersThatHave[suppressionHash]...)
}

func (s *countingSender) setPeersThatHave(suppressionHash [32]byte, peers []uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.peersThatHave == nil {
		s.peersThatHave = make(map[[32]byte][]uint64)
	}
	s.peersThatHave[suppressionHash] = append([]uint64(nil), peers...)
}

func (s *countingSender) getCalls() []countingRelaySlotCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]countingRelaySlotCall(nil), s.calls...)
}

// TestRouter_UpdateRelaySlot_DuplicatesOnly pins the R4.4 rippled
// parity behavior: reduce-relay selection feeds on DUPLICATE arrivals
// only (PeerImp.cpp:1730-1738 fires inside the `!added` branch of
// HashRouter::addSuppressionPeer). Counting first-seen proposals
// would accelerate selection N-fold vs rippled.
//
// Regression guard: a mutation that makes handleProposal call
// UpdateRelaySlot unconditionally (the pre-R4.4 behavior) would
// produce two calls from this two-message sequence, not one.
func TestRouter_UpdateRelaySlot_DuplicatesOnly(t *testing.T) {
	engine := &mockEngine{}

	// Build an adaptor whose trusted set includes the test pubkey so
	// the trust gate doesn't suppress the UpdateRelaySlot call.
	svc := newTestLedgerService(t)
	pubKey := make([]byte, 33)
	pubKey[0] = 0x02
	for i := 1; i < 33; i++ {
		pubKey[i] = byte(i)
	}
	var nodeID consensus.NodeID
	copy(nodeID[:], pubKey)

	sender := &countingSender{}
	adaptor := New(Config{
		LedgerService: svc,
		Sender:        sender,
		Validators:    []consensus.NodeID{nodeID},
	})

	inbox := make(chan *peermanagement.InboundMessage, 10)
	router := NewRouter(engine, adaptor, nil, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go router.Run(ctx)

	// Single canonical proposal payload — same bytes delivered twice
	// from different peers is what rippled considers a "duplicate."
	proposeSet := &message.ProposeSet{
		ProposeSeq:     1,
		CurrentTxHash:  make([]byte, 32),
		NodePubKey:     pubKey,
		CloseTime:      timeToXrplEpoch(time.Unix(1_700_000_000, 0)), // stable
		Signature:      make([]byte, signatureMinLen),
		PreviousLedger: make([]byte, 32),
	}
	payload := encodePayload(t, proposeSet)

	// Peer A delivers it first: first-seen, must NOT fire UpdateRelaySlot.
	inbox <- &peermanagement.InboundMessage{
		PeerID:  1,
		Type:    uint16(message.TypeProposeLedger),
		Payload: payload,
	}
	time.Sleep(30 * time.Millisecond)

	firstRound := sender.getCalls()
	assert.Empty(t, firstRound,
		"first-seen proposal must NOT feed UpdateRelaySlot (rippled fires only on duplicates)")

	// Peer B delivers the same bytes: duplicate, MUST fire UpdateRelaySlot.
	inbox <- &peermanagement.InboundMessage{
		PeerID:  2,
		Type:    uint16(message.TypeProposeLedger),
		Payload: payload,
	}
	time.Sleep(30 * time.Millisecond)

	calls := sender.getCalls()
	require.Len(t, calls, 1,
		"duplicate proposal from a second peer must fire exactly one UpdateRelaySlot call")
	assert.Equal(t, uint64(2), calls[0].OriginPeer,
		"UpdateRelaySlot must be fed with the DUPLICATE peer's ID (the second arrival)")
}

// TestRouter_UpdateRelaySlot_UntrustedValidator pins R5.7: untrusted
// validator duplicates MUST feed the reduce-relay slot — rippled's
// PeerImp.cpp:1730-1748 calls updateSlotAndSquelch before the
// isTrusted branch, so both trusted and untrusted duplicates drive
// selection. Pre-R5.7 gating on IsTrusted under-squelched untrusted
// gossip vs. rippled's behavior.
func TestRouter_UpdateRelaySlot_UntrustedValidator(t *testing.T) {
	engine := &mockEngine{}

	svc := newTestLedgerService(t)

	// Adaptor has NO trusted validators — the test pubkey is
	// therefore untrusted. Rippled still feeds the slot on duplicate
	// arrivals for this validator.
	sender := &countingSender{}
	adaptor := New(Config{
		LedgerService: svc,
		Sender:        sender,
		Validators:    nil, // empty UNL
	})

	inbox := make(chan *peermanagement.InboundMessage, 10)
	router := NewRouter(engine, adaptor, nil, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go router.Run(ctx)

	untrustedPubKey := make([]byte, 33)
	untrustedPubKey[0] = 0x02
	for i := 1; i < 33; i++ {
		untrustedPubKey[i] = byte(0x80 | i) // distinct from the earlier test
	}

	proposeSet := &message.ProposeSet{
		ProposeSeq:     1,
		CurrentTxHash:  make([]byte, 32),
		NodePubKey:     untrustedPubKey,
		CloseTime:      timeToXrplEpoch(time.Unix(1_700_000_001, 0)),
		Signature:      make([]byte, signatureMinLen),
		PreviousLedger: make([]byte, 32),
	}
	payload := encodePayload(t, proposeSet)

	inbox <- &peermanagement.InboundMessage{PeerID: 1, Type: uint16(message.TypeProposeLedger), Payload: payload}
	time.Sleep(30 * time.Millisecond)
	inbox <- &peermanagement.InboundMessage{PeerID: 2, Type: uint16(message.TypeProposeLedger), Payload: payload}
	time.Sleep(30 * time.Millisecond)

	calls := sender.getCalls()
	require.Len(t, calls, 1,
		"untrusted-validator duplicate MUST still fire UpdateRelaySlot (rippled fires regardless of trust)")
	assert.Equal(t, uint64(2), calls[0].OriginPeer)
}

// TestRelay_DuplicateArrivalFeedsAllKnownRelayers pins B3: when a
// duplicate proposal arrives from peer C, and the overlay's reverse
// index already maps the proposal's suppression hash to peers {A, B}
// (from a prior outbound relay), UpdateRelaySlot must be fed with
// the full set {A, B, C} — not just C. Matches rippled's
// overlay_.relay returning haveMessage and PeerImp passing it whole
// to updateSlotAndSquelch (PeerImp.cpp:3010-3017 for proposals).
//
// Regression guard: a mutation that feeds only originPeer — the
// pre-B3 behavior — would register exactly one peer in the seenPeers
// slice (C), under-counting multi-path delivery evidence and slowing
// selection convergence vs. rippled.
func TestRelay_DuplicateArrivalFeedsAllKnownRelayers(t *testing.T) {
	engine := &mockEngine{}
	svc := newTestLedgerService(t)

	pubKey := make([]byte, 33)
	pubKey[0] = 0x02
	for i := 1; i < 33; i++ {
		pubKey[i] = byte(0x40 | i) // distinct from the other B3 tests
	}
	var nodeID consensus.NodeID
	copy(nodeID[:], pubKey)

	sender := &countingSender{}
	adaptor := New(Config{
		LedgerService: svc,
		Sender:        sender,
		Validators:    []consensus.NodeID{nodeID},
	})

	inbox := make(chan *peermanagement.InboundMessage, 10)
	router := NewRouter(engine, adaptor, nil, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go router.Run(ctx)

	proposeSet := &message.ProposeSet{
		ProposeSeq:     1,
		CurrentTxHash:  make([]byte, 32),
		NodePubKey:     pubKey,
		CloseTime:      timeToXrplEpoch(time.Unix(1_700_000_002, 0)),
		Signature:      make([]byte, signatureMinLen),
		PreviousLedger: make([]byte, 32),
	}
	payload := encodePayload(t, proposeSet)

	// First delivery from peer A establishes the dedup entry; no
	// slot-feeding yet (first-seen gate).
	inbox <- &peermanagement.InboundMessage{
		PeerID:  1,
		Type:    uint16(message.TypeProposeLedger),
		Payload: payload,
	}
	time.Sleep(30 * time.Millisecond)
	require.Empty(t, sender.getCalls(), "first-seen proposal must not feed the slot")

	// Seed the overlay reverse index: the proposal's suppression key
	// maps to peers {A=1, B=2} — as if we had already relayed it to
	// them after the first-seen arrival. The proposal's suppression
	// hash is computed from its decoded fields via
	// hashProposalSuppression, so we reconstruct a matching Proposal
	// to produce the same key the router will compute on the
	// duplicate arrival below.
	seedProposal := ProposalFromMessage(proposeSet)
	seedHash := hashProposalSuppression(seedProposal)
	sender.setPeersThatHave(seedHash, []uint64{1, 2})

	// Duplicate delivery from peer C=3: UpdateRelaySlot must fire
	// with originPeer=3 AND seenPeers containing 1 and 2 — the full
	// set of peers the network believes already have this message.
	inbox <- &peermanagement.InboundMessage{
		PeerID:  3,
		Type:    uint16(message.TypeProposeLedger),
		Payload: payload,
	}
	time.Sleep(30 * time.Millisecond)

	calls := sender.getCalls()
	require.Len(t, calls, 1, "duplicate proposal must fire exactly one UpdateRelaySlot call")
	call := calls[0]
	assert.Equal(t, uint64(3), call.OriginPeer,
		"UpdateRelaySlot must be fed with the DUPLICATE peer's ID as originPeer")

	// seenPeers must contain peers 1 and 2 (the known-havers from
	// the reverse index). Order is not fixed (it's a set walk).
	seenSet := make(map[uint64]struct{}, len(call.SeenPeers))
	for _, p := range call.SeenPeers {
		seenSet[p] = struct{}{}
	}
	assert.Contains(t, seenSet, uint64(1), "seenPeers must include peer A (prior known-haver)")
	assert.Contains(t, seenSet, uint64(2), "seenPeers must include peer B (prior known-haver)")
}

// TestRelay_FirstSeenMessageDoesNotFeedSlot pins the other half of
// B3: for a first-seen message — no prior entry in the suppression
// cache — UpdateRelaySlot must NOT fire, regardless of what the
// overlay's reverse index says about `suppressionHash`. Matches
// rippled PeerImp.cpp:1730's `!added` branch: first-seen arrivals go
// to the HashRouter but don't drive the squelch slot.
//
// Regression guard: a mutation that inverted the gate would
// accelerate selection 2x (every first-seen counted as duplicate),
// producing earlier squelches than the rest of the network.
func TestRelay_FirstSeenMessageDoesNotFeedSlot(t *testing.T) {
	engine := &mockEngine{}
	svc := newTestLedgerService(t)

	pubKey := make([]byte, 33)
	pubKey[0] = 0x02
	for i := 1; i < 33; i++ {
		pubKey[i] = byte(0x20 | i) // distinct seed from the other tests
	}
	var nodeID consensus.NodeID
	copy(nodeID[:], pubKey)

	sender := &countingSender{}
	adaptor := New(Config{
		LedgerService: svc,
		Sender:        sender,
		Validators:    []consensus.NodeID{nodeID},
	})

	inbox := make(chan *peermanagement.InboundMessage, 4)
	router := NewRouter(engine, adaptor, nil, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go router.Run(ctx)

	// Construct a proposal and PRE-SEED the overlay's reverse index
	// with a non-empty known-haver set for its suppression hash. If
	// the gate were inverted, this test would exercise a code path
	// that feeds seenPeers into the slot even on a first-seen
	// arrival — exactly the regression we're pinning against.
	proposeSet := &message.ProposeSet{
		ProposeSeq:     7,
		CurrentTxHash:  make([]byte, 32),
		NodePubKey:     pubKey,
		CloseTime:      timeToXrplEpoch(time.Unix(1_700_000_003, 0)),
		Signature:      make([]byte, signatureMinLen),
		PreviousLedger: make([]byte, 32),
	}
	payload := encodePayload(t, proposeSet)

	seedProposal := ProposalFromMessage(proposeSet)
	seedHash := hashProposalSuppression(seedProposal)
	sender.setPeersThatHave(seedHash, []uint64{11, 22, 33})

	// Deliver the message exactly once — from a fresh peer, no prior
	// observation. This is the rippled `!added == false` branch in
	// HashRouter::addSuppressionPeer: entry is CREATED, not matched.
	// Slot must NOT fire.
	inbox <- &peermanagement.InboundMessage{
		PeerID:  99,
		Type:    uint16(message.TypeProposeLedger),
		Payload: payload,
	}
	time.Sleep(30 * time.Millisecond)

	calls := sender.getCalls()
	assert.Empty(t, calls,
		"first-seen message must NOT feed UpdateRelaySlot, even if the reverse index has entries for its suppression hash")
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

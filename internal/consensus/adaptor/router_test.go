package adaptor

import (
	"context"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	ledgerservice "github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	batchtest "github.com/LeJamon/go-xrpl/internal/testing/batch"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockEngine records calls from the router for testing.
type mockEngine struct {
	mu            sync.Mutex
	proposals     []*consensus.Proposal
	validations   []*consensus.Validation
	txSets        []consensus.TxSetID
	ledgers       []consensus.LedgerID
	acquireFailed []consensus.LedgerID
	switchResult  consensus.LedgerSwitchResult
	switchHook    func(consensus.LedgerID)
	buildingSeq   uint32
}

func adaptorHasTx(t *testing.T, a *Adaptor, id consensus.TxID) bool {
	t.Helper()
	exists, err := a.HasTx(id)
	require.NoError(t, err)
	return exists
}

func (m *mockEngine) Start(context.Context) error              { return nil }
func (m *mockEngine) Stop() error                              { return nil }
func (m *mockEngine) StartRound(consensus.RoundID, bool) error { return nil }
func (m *mockEngine) Mode() consensus.Mode                     { return consensus.ModeObserving }
func (m *mockEngine) Phase() consensus.Phase                   { return consensus.PhaseOpen }
func (m *mockEngine) BuildingLedgerSeq() uint32                { return m.buildingSeq }
func (m *mockEngine) IsProposing() bool                        { return false }
func (m *mockEngine) GetLastCloseInfo() (int, time.Duration)   { return 0, 0 }
func (m *mockEngine) GetJSON(bool) map[string]any              { return map[string]any{} }
func (m *mockEngine) Subscribe(consensus.EventSubscriber)      {}

func (m *mockEngine) TrySwitchToLedger(id consensus.LedgerID) (consensus.LedgerSwitchResult, error) {
	m.mu.Lock()
	m.ledgers = append(m.ledgers, id)
	result := m.switchResult
	hook := m.switchHook
	m.mu.Unlock()
	if result == consensus.LedgerSwitchAccepted && hook != nil {
		hook(id)
	}
	return result, nil
}

func (m *mockEngine) CanAcceptLedger(consensus.LedgerID) (bool, error) {
	return true, nil
}

func (m *mockEngine) OnLedgerAcquireFailed(id consensus.LedgerID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acquireFailed = append(m.acquireFailed, id)
}

func (m *mockEngine) getLedgers() []consensus.LedgerID {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]consensus.LedgerID(nil), m.ledgers...)
}

func (m *mockEngine) getAcquireFailed() []consensus.LedgerID {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]consensus.LedgerID(nil), m.acquireFailed...)
}

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

	router := newTestRouter(engine, adaptor, inbox)

	ctx := t.Context()
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
		Type:    message.TypeProposeLedger,
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

	router := newTestRouter(engine, adaptor, inbox)

	ctx := t.Context()
	go router.Run(ctx)

	// Build a valid STValidation binary payload.
	testVal := &consensus.Validation{
		Full:      true,
		LedgerSeq: 42,
		SignTime:  time.Now(),
		LoadFee:   0,
	}
	copy(testVal.LedgerID[:], []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32})
	copy(testVal.SigningPubKey[:], []byte{0x02, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32})
	testVal.NodeID = consensus.CalcNodeID([33]byte(testVal.SigningPubKey))
	testVal.Signature = make([]byte, 70) // dummy signature
	val := &message.Validation{
		Validation: serializeSTValidation(testVal),
	}

	inbox <- &peermanagement.InboundMessage{
		PeerID:  2,
		Type:    message.TypeValidation,
		Payload: encodePayload(t, val),
	}

	time.Sleep(50 * time.Millisecond)

	validations := engine.getValidations()
	assert.Len(t, validations, 1)
}

func TestRouterDispatchesTransaction(t *testing.T) {
	engine := &mockEngine{}
	a := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 10)

	router := newTestRouter(engine, a, inbox)

	ctx := t.Context()
	go router.Run(ctx)

	// Build a real signed payment blob; the open-ledger Submit path
	// rejects un-parseable blobs, so the inbound tx must be a valid
	// XRPL Payment for HasTx to be true after dispatch.
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	master := jtx.MasterAccount()
	alice := jtx.NewAccount("alice")
	txn := payment.Pay(master, alice, 100_000_000).Sequence(1).Build()
	env.SignWith(txn, master)
	txMap, err := txn.Flatten()
	require.NoError(t, err)
	hexStr, err := binarycodec.Encode(txMap)
	require.NoError(t, err)
	blob, err := hex.DecodeString(hexStr)
	require.NoError(t, err)
	txHash, err := tx.ComputeTransactionHash(txn)
	require.NoError(t, err)

	txMsg := &message.Transaction{
		RawTransaction:   blob,
		Status:           message.TxStatusNew,
		ReceiveTimestamp: uint64(time.Now().UnixNano()),
	}

	inbox <- &peermanagement.InboundMessage{
		PeerID:  3,
		Type:    message.TypeTransaction,
		Payload: encodePayload(t, txMsg),
	}

	time.Sleep(50 * time.Millisecond)

	// Transaction should be visible via HasTx now that AddPendingTx
	// routed it through service.SubmitOpenLedgerTx into the persistent
	// open view.
	assert.True(t, adaptorHasTx(t, a, consensus.TxID(txHash)))
}

// TestRouterDispatchesPreDecodedTransaction covers the path taken by
// frames fanned out from a TMTransactions batch: the transaction arrives
// already decoded in InboundMessage.Tx with a nil Payload, so the router
// must accept it without re-decoding. Companion to
// TestRouterDispatchesTransaction (the wire/Payload path).
func TestRouterDispatchesPreDecodedTransaction(t *testing.T) {
	engine := &mockEngine{}
	a := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 10)

	router := newTestRouter(engine, a, inbox)

	ctx := t.Context()
	go router.Run(ctx)

	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	master := jtx.MasterAccount()
	alice := jtx.NewAccount("alice")
	txn := payment.Pay(master, alice, 100_000_000).Sequence(1).Build()
	env.SignWith(txn, master)
	txMap, err := txn.Flatten()
	require.NoError(t, err)
	hexStr, err := binarycodec.Encode(txMap)
	require.NoError(t, err)
	blob, err := hex.DecodeString(hexStr)
	require.NoError(t, err)
	txHash, err := tx.ComputeTransactionHash(txn)
	require.NoError(t, err)

	inbox <- &peermanagement.InboundMessage{
		PeerID: 3,
		Type:   message.TypeTransaction,
		Tx: &message.Transaction{
			RawTransaction:   blob,
			Status:           message.TxStatusNew,
			ReceiveTimestamp: uint64(time.Now().UnixNano()),
		},
	}

	time.Sleep(50 * time.Millisecond)

	assert.True(t, adaptorHasTx(t, a, consensus.TxID(txHash)),
		"router must accept a pre-decoded (batch-fanned) transaction")
}

func TestRouterRelaysQueuedTransactionAsDeferred(t *testing.T) {
	a := newTestAdaptor(t)
	router := newTestRouter(&mockEngine{}, a, nil)

	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	master := jtx.MasterAccount()
	alice := jtx.NewAccount("queued-relay-destination")
	txn := payment.Pay(master, alice, 100_000_000).Fee(1).Sequence(1).Build()
	env.SignWith(txn, master)
	txMap, err := txn.Flatten()
	require.NoError(t, err)
	hexStr, err := binarycodec.Encode(txMap)
	require.NoError(t, err)
	blob, err := hex.DecodeString(hexStr)
	require.NoError(t, err)

	dispatch := router.handleTransaction(&peermanagement.InboundMessage{
		PeerID: 3,
		Type:   message.TypeTransaction,
		Tx: &message.Transaction{
			RawTransaction: blob,
			Status:         message.TxStatusNew,
		},
	})
	require.NoError(t, dispatch.submitError)
	require.Equal(t, openledger.ResultSuccess, dispatch.submitResult)
	require.True(t, dispatch.deferred)
	require.True(t, dispatch.relayed)

	frame, err := message.EncodeFrame(relayTransactionMessage(blob, dispatch.deferred))
	require.NoError(t, err)
	msgType, decoded := decodeFrame(t, frame)
	require.Equal(t, message.TypeTransaction, msgType)
	relayed := decoded.(*message.Transaction)
	require.Equal(t, message.TxStatusCurrent, relayed.Status)
	require.True(t, relayed.Deferred)
}

func TestRouterDropsStandaloneBatchInnerTransaction(t *testing.T) {
	engine := &mockEngine{}
	a := newTestAdaptor(t)
	router := newTestRouter(engine, a, nil)

	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	master := jtx.MasterAccount()
	alice := jtx.NewAccount("batch-inner-destination")
	txn := payment.Pay(master, alice, 100_000_000).Sequence(1).Build()
	env.SignWith(txn, master)
	txn.GetCommon().SetFlags(tx.TfInnerBatchTxn)
	txMap, err := txn.Flatten()
	require.NoError(t, err)
	hexStr, err := binarycodec.Encode(txMap)
	require.NoError(t, err)
	blob, err := hex.DecodeString(hexStr)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(blob), 8)
	require.Equal(t, byte(0x12), blob[0])
	require.Equal(t, byte(0x22), blob[3])
	reorderedBlob := make([]byte, 0, len(blob))
	reorderedBlob = append(reorderedBlob, blob[3:8]...)
	reorderedBlob = append(reorderedBlob, blob[:3]...)
	reorderedBlob = append(reorderedBlob, blob[8:]...)
	require.NotEqual(t, blob, reorderedBlob)
	_, err = binarycodec.DecodeBytes(reorderedBlob)
	require.NoError(t, err)
	parsed, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)
	txHash, err := tx.ComputeTransactionHash(parsed)
	require.NoError(t, err)

	msg := &peermanagement.InboundMessage{
		PeerID: 3,
		Type:   message.TypeTransaction,
		Tx: &message.Transaction{
			RawTransaction: reorderedBlob,
			Status:         message.TxStatusNew,
		},
	}
	dispatch := router.handleTransaction(msg)
	require.Equal(t, resource.FeeModerateBurdenPeer(), dispatch.charge)
	require.Equal(t, "inner batch txn", dispatch.chargeContext)
	require.False(t, dispatch.relayed)
	dispatch = router.handleTransaction(msg)
	require.Equal(t, resource.FeeModerateBurdenPeer(), dispatch.charge)
	require.Equal(t, "inner batch txn", dispatch.chargeContext)
	require.False(t, dispatch.relayed)
	assert.False(t, adaptorHasTx(t, a, consensus.TxID(txHash)))
	shouldProcess, bad := router.txSeen.claim(txHash, 0)
	require.True(t, shouldProcess)
	require.False(t, bad)
}

func TestRouterRelaysSignedDirectBatchSignerOuterOnly(t *testing.T) {
	master := jtx.MasterAccount()
	bob := jtx.NewAccount("batch-relay-bob")
	genesisConfig := genesis.DefaultConfig()
	genesisConfig.Amendments = append(genesisConfig.Amendments, amendment.FeatureBatchV1_1)
	genesisConfig.InitialAccounts = append(genesisConfig.InitialAccounts, genesis.InitialAccount{
		Address:  bob.Address,
		Balance:  uint64(jtx.XRP(1_000)),
		Sequence: 1,
	})
	svc, err := ledgerservice.New(ledgerservice.Config{
		Standalone:    true,
		Startup:       ledgerservice.StartupConfig{Mode: ledgerservice.StartupFresh},
		GenesisConfig: genesisConfig,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	require.True(t, svc.TransactionRules().Enabled(amendment.FeatureBatchV1_1))
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	a := New(Config{LedgerService: svc, Identity: identity})
	router := newTestRouter(&mockEngine{}, a, nil)

	batch := batchtest.NewBatchBuilder(master, 1, 50, batchtx.BatchFlagAllOrNothing).
		AddInnerTx(batchtest.MakeInnerPaymentXRP(master, bob, 1, 2)).
		AddInnerTx(batchtest.MakeInnerPaymentXRP(bob, master, 1, 1)).
		AddSigner(bob).
		MustBuild()
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	env.SignWith(batch, master)
	txMap, err := batch.Flatten()
	require.NoError(t, err)
	hexStr, err := binarycodec.Encode(txMap)
	require.NoError(t, err)
	blob, err := hex.DecodeString(hexStr)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(blob), 8)
	require.Equal(t, byte(0x12), blob[0])
	require.Equal(t, byte(0x22), blob[3])
	reorderedBlob := make([]byte, 0, len(blob))
	reorderedBlob = append(reorderedBlob, blob[3:8]...)
	reorderedBlob = append(reorderedBlob, blob[:3]...)
	reorderedBlob = append(reorderedBlob, blob[8:]...)
	require.NotEqual(t, blob, reorderedBlob)
	_, err = binarycodec.DecodeBytes(reorderedBlob)
	require.NoError(t, err)
	parsed, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)
	parsedBatch, ok := parsed.(*batchtx.Batch)
	require.True(t, ok)
	require.Len(t, parsedBatch.BatchSigners, 1)
	require.NotEmpty(t, parsedBatch.BatchSigners[0].BatchSigner.BatchTxnSignature)
	outerHash, err := tx.ComputeTransactionHash(parsedBatch)
	require.NoError(t, err)
	innerHashes := make([][32]byte, len(parsedBatch.RawTransactions))
	for i := range parsedBatch.RawTransactions {
		innerHashes[i], err = tx.ComputeTransactionHash(parsedBatch.RawTransactions[i].RawTransaction.InnerTx)
		require.NoError(t, err)
	}

	dispatch := router.handleTransaction(&peermanagement.InboundMessage{
		PeerID: 3,
		Type:   message.TypeTransaction,
		Tx: &message.Transaction{
			RawTransaction: reorderedBlob,
			Status:         message.TxStatusNew,
		},
	})
	require.Zero(t, dispatch.charge.Cost())
	require.Empty(t, dispatch.chargeContext)
	require.NoError(t, dispatch.submitError)
	if dispatch.submitResult != openledger.ResultSuccess {
		simulated, simErr := svc.SimulateTransaction(parsedBatch)
		require.NoError(t, simErr)
		t.Fatalf("submit result %v, simulated TER %s (%s)", dispatch.submitResult, simulated.Result, simulated.Message)
	}
	require.Equal(t, openledger.ResultSuccess, dispatch.submitResult)
	require.True(t, dispatch.relayed)
	relayBlob, _, _, ok := svc.TransactionForRelay(outerHash)
	require.True(t, ok)
	require.Equal(t, blob, relayBlob)
	require.True(t, adaptorHasTx(t, a, consensus.TxID(outerHash)))
	for _, innerHash := range innerHashes {
		require.False(t, adaptorHasTx(t, a, consensus.TxID(innerHash)))
	}

	duplicate := router.handleTransaction(&peermanagement.InboundMessage{
		PeerID: 4,
		Type:   message.TypeTransaction,
		Tx: &message.Transaction{
			RawTransaction: blob,
			Status:         message.TxStatusNew,
		},
	})
	require.Zero(t, duplicate.charge.Cost())
	require.Empty(t, duplicate.chargeContext)
	require.NoError(t, duplicate.submitError)
	require.False(t, duplicate.relayed)

	parsedBatch.BatchSigners[0].BatchSigner.BatchTxnSignature = "DEADBEEF"
	badMap, err := parsedBatch.Flatten()
	require.NoError(t, err)
	badHex, err := binarycodec.Encode(badMap)
	require.NoError(t, err)
	badBlob, err := hex.DecodeString(badHex)
	require.NoError(t, err)
	badMessage := &peermanagement.InboundMessage{
		PeerID: 5,
		Type:   message.TypeTransaction,
		Tx: &message.Transaction{
			RawTransaction: badBlob,
			Status:         message.TxStatusNew,
		},
	}
	badDispatch := router.handleTransaction(badMessage)
	require.Error(t, badDispatch.submitError)
	require.Equal(t, resource.FeeInvalidSignature(), badDispatch.charge)
	require.Equal(t, "transaction-invalid-signature", badDispatch.chargeContext)
	require.False(t, badDispatch.relayed)

	knownBad := router.handleTransaction(badMessage)
	require.NoError(t, knownBad.submitError)
	require.Equal(t, resource.FeeUselessData(), knownBad.charge)
	require.Equal(t, "transaction-known-bad", knownBad.chargeContext)
	require.False(t, knownBad.relayed)
	router.txSeen.now = func() time.Time {
		return time.Now().Add(transactionProcessInterval)
	}
	admittedKnownBad := router.handleTransaction(badMessage)
	require.NoError(t, admittedKnownBad.submitError)
	require.Equal(t, resource.FeeInvalidSignature(), admittedKnownBad.charge)
	require.Equal(t, "transaction-known-bad-signature", admittedKnownBad.chargeContext)
	require.False(t, admittedKnownBad.relayed)
	router.txSeen.now = time.Now

	validatedIndex := svc.GetValidatedLedgerIndex()
	require.NotZero(t, validatedIndex)
	expired := uint32(0)
	parsedBatch.LastLedgerSequence = &expired
	expiredMap, err := parsedBatch.Flatten()
	require.NoError(t, err)
	expiredHex, err := binarycodec.Encode(expiredMap)
	require.NoError(t, err)
	expiredBlob, err := hex.DecodeString(expiredHex)
	require.NoError(t, err)
	expiredMessage := &peermanagement.InboundMessage{
		PeerID: 6,
		Type:   message.TypeTransaction,
		Tx: &message.Transaction{
			RawTransaction: expiredBlob,
			Status:         message.TxStatusNew,
		},
	}
	expiredDispatch := router.handleTransaction(expiredMessage)
	require.NoError(t, expiredDispatch.submitError)
	require.Equal(t, resource.FeeUselessData(), expiredDispatch.charge)
	require.Equal(t, "transaction-expired", expiredDispatch.chargeContext)
	require.False(t, expiredDispatch.relayed)

	expiredRepeat := router.handleTransaction(expiredMessage)
	require.NoError(t, expiredRepeat.submitError)
	require.Equal(t, resource.FeeUselessData(), expiredRepeat.charge)
	require.Equal(t, "transaction-known-bad", expiredRepeat.chargeContext)
	require.False(t, expiredRepeat.relayed)
}

func TestRouterIgnoresUnknownMessages(t *testing.T) {
	engine := &mockEngine{}
	adaptor := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 10)

	router := newTestRouter(engine, adaptor, inbox)

	ctx := t.Context()
	go router.Run(ctx)

	// Send a Ping message — should be silently ignored
	inbox <- &peermanagement.InboundMessage{
		PeerID:  4,
		Type:    message.TypePing,
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

	router := newTestRouter(engine, adaptor, inbox)

	ctx := t.Context()
	go router.Run(ctx)

	// Send garbage as a proposal — should not panic
	inbox <- &peermanagement.InboundMessage{
		PeerID:  5,
		Type:    message.TypeProposeLedger,
		Payload: []byte{0xFF, 0xFF, 0xFF},
	}

	time.Sleep(50 * time.Millisecond)

	assert.Empty(t, engine.proposals)
}

func TestRouterStopsOnContextCancel(t *testing.T) {
	engine := &mockEngine{}
	adaptor := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 10)

	router := newTestRouter(engine, adaptor, inbox)

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

type countingSender struct {
	noopSender
	mu      sync.Mutex
	calls   []countingRelaySlotCall
	relayed map[[32]byte]bool
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

func (s *countingSender) MessageRelayedRecently(suppressionHash [32]byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.relayed[suppressionHash]
}

func (s *countingSender) setRelayed(suppressionHash [32]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.relayed == nil {
		s.relayed = make(map[[32]byte]bool)
	}
	s.relayed[suppressionHash] = true
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
	router := newTestRouter(engine, adaptor, inbox)

	ctx := t.Context()
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
		Type:    message.TypeProposeLedger,
		Payload: payload,
	}
	time.Sleep(30 * time.Millisecond)

	firstRound := sender.getCalls()
	assert.Empty(t, firstRound,
		"first-seen proposal must NOT feed UpdateRelaySlot (rippled fires only on duplicates)")
	sender.setRelayed(hashProposalSuppression(proposalFromMessage(proposeSet)))

	// Peer B delivers the same bytes: duplicate, MUST fire UpdateRelaySlot.
	inbox <- &peermanagement.InboundMessage{
		PeerID:  2,
		Type:    message.TypeProposeLedger,
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
	router := newTestRouter(engine, adaptor, inbox)

	ctx := t.Context()
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

	inbox <- &peermanagement.InboundMessage{PeerID: 1, Type: message.TypeProposeLedger, Payload: payload}
	time.Sleep(30 * time.Millisecond)
	sender.setRelayed(hashProposalSuppression(proposalFromMessage(proposeSet)))
	inbox <- &peermanagement.InboundMessage{PeerID: 2, Type: message.TypeProposeLedger, Payload: payload}
	time.Sleep(30 * time.Millisecond)

	calls := sender.getCalls()
	require.Len(t, calls, 1,
		"untrusted-validator duplicate MUST still fire UpdateRelaySlot (rippled fires regardless of trust)")
	assert.Equal(t, uint64(2), calls[0].OriginPeer)
}

func TestRelay_DuplicateAfterRelayFeedsOnlyCurrentSource(t *testing.T) {
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
	router := newTestRouter(engine, adaptor, inbox)

	ctx := t.Context()
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
		Type:    message.TypeProposeLedger,
		Payload: payload,
	}
	time.Sleep(30 * time.Millisecond)
	require.Empty(t, sender.getCalls(), "first-seen proposal must not feed the slot")

	seedProposal := proposalFromMessage(proposeSet)
	seedHash := hashProposalSuppression(seedProposal)
	sender.setRelayed(seedHash)

	inbox <- &peermanagement.InboundMessage{
		PeerID:  3,
		Type:    message.TypeProposeLedger,
		Payload: payload,
	}
	time.Sleep(30 * time.Millisecond)

	calls := sender.getCalls()
	require.Len(t, calls, 1, "duplicate proposal must fire exactly one UpdateRelaySlot call")
	call := calls[0]
	assert.Equal(t, uint64(3), call.OriginPeer,
		"UpdateRelaySlot must be fed with the DUPLICATE peer's ID as originPeer")
	assert.Empty(t, call.SeenPeers,
		"the verified first-source set was counted when relay completed")
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
	router := newTestRouter(engine, adaptor, inbox)

	ctx := t.Context()
	go router.Run(ctx)

	proposeSet := &message.ProposeSet{
		ProposeSeq:     7,
		CurrentTxHash:  make([]byte, 32),
		NodePubKey:     pubKey,
		CloseTime:      timeToXrplEpoch(time.Unix(1_700_000_003, 0)),
		Signature:      make([]byte, signatureMinLen),
		PreviousLedger: make([]byte, 32),
	}
	payload := encodePayload(t, proposeSet)

	seedProposal := proposalFromMessage(proposeSet)
	seedHash := hashProposalSuppression(seedProposal)
	sender.setRelayed(seedHash)

	// Deliver the message exactly once — from a fresh peer, no prior
	// observation. This is the rippled `!added == false` branch in
	// HashRouter::addSuppressionPeer: entry is CREATED, not matched.
	// Slot must NOT fire.
	inbox <- &peermanagement.InboundMessage{
		PeerID:  99,
		Type:    message.TypeProposeLedger,
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

	router := newTestRouter(engine, adaptor, inbox)

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
	signing := consensus.SigningPubKey{0x02, 0x03}
	original := &consensus.Proposal{
		Round: consensus.RoundID{
			Seq:        5,
			ParentHash: [32]byte{0x01},
		},
		SigningPubKey:  signing,
		NodeID:         consensus.CalcNodeID([33]byte(signing)),
		Position:       3,
		TxSet:          consensus.TxSetID{0x04},
		CloseTime:      time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
		Signature:      []byte{0x05, 0x06},
		PreviousLedger: consensus.LedgerID{0x01},
	}

	msg := proposalToMessage(original)
	restored := proposalFromMessage(msg)

	assert.Equal(t, original.Position, restored.Position)
	assert.Equal(t, original.SigningPubKey, restored.SigningPubKey)
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
	restored := transactionFromMessage(msg)
	assert.Equal(t, blob, restored)
}

func TestConverterHaveSetRoundTrip(t *testing.T) {
	id := consensus.TxSetID{0x01, 0x02, 0x03}
	msg := HaveSetToMessage(id, message.TxSetStatusNeed)
	restoredID, restoredStatus, err := haveSetFromMessage(msg)
	require.NoError(t, err)
	assert.Equal(t, id, restoredID)
	assert.Equal(t, message.TxSetStatusNeed, restoredStatus)
}

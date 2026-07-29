package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdoptLedgerWithState_FiresOnLedgerClosedHook pins F3: peer-adopted
// ledgers must fire hooks.OnLedgerClosed so WebSocket `ledger` stream
// subscribers see a ledger-closed event for every ledger the node adopts
// from peers. Without this, the `ledger` stream silently skips every
// peer-adopted ledger — an observable divergence from rippled where
// pubLedger fires for both consensus-closed and sync-adopted ledgers.
func TestAdoptLedgerWithState_PublishesLedgerEvent(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	// Capture OnLedgerClosed invocations.
	var (
		mu              sync.Mutex
		callCount       int
		capturedInfo    *LedgerInfo
		capturedTxCount int
	)
	done := make(chan struct{}, 1)

	setEventSinkFunc(svc, func(event *LedgerAcceptedEvent) {
		mu.Lock()
		callCount++
		capturedInfo = event.LedgerInfo
		capturedTxCount = len(event.TransactionResults)
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	})

	// Build a tx map with 2 txs so txCount assertion is meaningful.
	txMap := shamap.New(shamap.TypeTransaction)
	blob1, id1 := makeTxMetaBlobForTest(t, []byte("hook-tx-blob-A-padding-padpad"), 0)
	blob2, id2 := makeTxMetaBlobForTest(t, []byte("hook-tx-blob-B-padding-padpad"), 1)
	require.NoError(t, txMap.PutWithNodeType(id1, blob1, shamap.NodeTypeTransactionWithMeta))
	require.NoError(t, txMap.PutWithNodeType(id2, blob2, shamap.NodeTypeTransactionWithMeta))
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	stateMap := shamap.New(shamap.TypeState)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	var adoptedHash [32]byte
	adoptedHash[0] = 0xF3
	adoptedSeq := svc.GetClosedLedgerIndex() + 1
	hdr := &header.LedgerHeader{
		LedgerIndex: adoptedSeq,
		TxHash:      txRoot,
		AccountHash: stateRoot,
		CloseTime:   time.Unix(1700000000, 0),
	}
	hdr.Hash = header.CalculateHash(*hdr)
	adoptedHash = hdr.Hash

	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdr, stateMap, txMap))
	svc.SetValidatedLedger(adoptedSeq, adoptedHash)

	// Wait for the goroutine-dispatched hook to fire.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnLedgerClosed hook never fired for adopted ledger")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, callCount, "OnLedgerClosed must fire exactly once")
	require.NotNil(t, capturedInfo, "OnLedgerClosed must receive a non-nil LedgerInfo")
	assert.Equal(t, adoptedSeq, capturedInfo.Sequence, "LedgerInfo.Sequence must match adopted ledger seq")
	assert.Equal(t, adoptedHash, capturedInfo.Hash, "LedgerInfo.Hash must match adopted ledger hash")
	assert.Equal(t, 2, capturedTxCount, "txCount must match the number of txs in the adopted tx map")
}

// TestAdoptLedgerWithState_FiresOnTransactionHook pins F3: peer-adopted
// ledgers must fire hooks.OnTransaction for every tx in the installed tx
// map so WebSocket `transactions` stream subscribers see every adopted
// tx. Matches rippled's pubValidatedTransactions which emits for every tx
// in a newly-published ledger regardless of whether it was consensus-
// closed locally or adopted from a peer.
func TestAdoptLedgerWithState_PublishesTransactions(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	var txCallCount int32
	seenHashes := &sync.Map{}
	done := make(chan struct{}, 4)

	setEventSinkFunc(svc, func(event *LedgerAcceptedEvent) {
		for _, result := range event.TransactionResults {
			atomic.AddInt32(&txCallCount, 1)
			seenHashes.Store(result.TxHash, struct{}{})
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})

	txMap := shamap.New(shamap.TypeTransaction)
	blob1, id1 := makeTxMetaBlobForTest(t, []byte("hook-onTx-blob-A-padding-padp"), 0)
	blob2, id2 := makeTxMetaBlobForTest(t, []byte("hook-onTx-blob-B-padding-padp"), 1)
	require.NoError(t, txMap.PutWithNodeType(id1, blob1, shamap.NodeTypeTransactionWithMeta))
	require.NoError(t, txMap.PutWithNodeType(id2, blob2, shamap.NodeTypeTransactionWithMeta))
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	stateMap := shamap.New(shamap.TypeState)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	var adoptedHash [32]byte
	adoptedHash[0] = 0xF4
	hdr := &header.LedgerHeader{
		LedgerIndex: svc.GetClosedLedgerIndex() + 1,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}
	hdr.Hash = header.CalculateHash(*hdr)
	adoptedHash = hdr.Hash
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdr, stateMap, txMap))
	svc.SetValidatedLedger(hdr.LedgerIndex, adoptedHash)

	// Wait for both tx dispatches.
	deadline := time.After(2 * time.Second)
	for range 2 {
		select {
		case <-done:
		case <-deadline:
			t.Fatalf("OnTransaction did not fire for all adopted txs (got %d of 2)", atomic.LoadInt32(&txCallCount))
		}
	}

	assert.Equal(t, int32(2), atomic.LoadInt32(&txCallCount),
		"OnTransaction must fire exactly once per adopted tx")
	for _, id := range [][32]byte{id1, id2} {
		_, ok := seenHashes.Load(id)
		assert.Truef(t, ok, "OnTransaction must fire for tx %x", id[:4])
	}
}

// An adopted ledger is published only after trusted validation confirms its
// hash. The pending event is drained exactly once.
func TestAdoptLedgerWithState_StashesEventUntilValidated(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	var (
		mu            sync.Mutex
		callbackCount int
		lastEvent     *LedgerAcceptedEvent
	)
	done := make(chan struct{}, 1)

	setEventSinkFunc(svc, func(event *LedgerAcceptedEvent) {
		mu.Lock()
		callbackCount++
		lastEvent = event
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	})

	txMap := shamap.New(shamap.TypeTransaction)
	blob1, id1 := makeTxMetaBlobForTest(t, []byte("stash-tx-blob-A-padding-padpd"), 0)
	require.NoError(t, txMap.PutWithNodeType(id1, blob1, shamap.NodeTypeTransactionWithMeta))
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	stateMap := shamap.New(shamap.TypeState)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	var adoptedHash [32]byte
	adoptedHash[0] = 0xF5
	adoptedSeq := svc.GetClosedLedgerIndex() + 1
	hdr := &header.LedgerHeader{
		LedgerIndex: adoptedSeq,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}
	hdr.Hash = header.CalculateHash(*hdr)
	adoptedHash = hdr.Hash
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdr, stateMap, txMap))

	// Give any erroneously-dispatched callback a chance to run.
	select {
	case <-done:
		t.Fatal("event sink must not fire at adopt time: ledger is not trust-validated")
	case <-time.After(100 * time.Millisecond):
	}

	mu.Lock()
	assert.Equal(t, 0, callbackCount,
		"event sink must not fire at adopt time")
	mu.Unlock()

	// The event must be stashed keyed by hash so SetValidatedLedger can drain it.
	svc.mu.RLock()
	_, stashed := svc.pendingValidation[adoptedHash]
	svc.mu.RUnlock()
	assert.True(t, stashed,
		"adopt must stash a LedgerAcceptedEvent keyed by the adopted ledger hash")

	// Trusted validation drains the stashed event exactly once.
	svc.SetValidatedLedger(adoptedSeq, adoptedHash)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("event sink did not fire after validation drained the stashed event")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, callbackCount,
		"event sink must fire exactly once after validation")
	require.NotNil(t, lastEvent)
	require.NotNil(t, lastEvent.LedgerInfo)
	assert.Equal(t, adoptedSeq, lastEvent.LedgerInfo.Sequence,
		"drained event must carry the adopted ledger's seq")
	assert.Equal(t, adoptedHash, lastEvent.LedgerInfo.Hash,
		"drained event must carry the adopted ledger's hash")
	assert.True(t, lastEvent.LedgerInfo.Validated,
		"drained event must reflect the validation transition")
	require.NotNil(t, lastEvent.Ledger)
	assert.True(t, lastEvent.Ledger.IsValidated())
	assert.Len(t, lastEvent.TransactionResults, 1,
		"drained event must carry the adopted tx results")
	assert.True(t, lastEvent.TransactionResults[0].Validated,
		"drained transaction results must reflect the validation transition")

	// The stash must be empty after drain.
	svc.mu.RLock()
	_, stillStashed := svc.pendingValidation[adoptedHash]
	svc.mu.RUnlock()
	assert.False(t, stillStashed,
		"SetValidatedLedger must remove the stashed event after firing")
}

func TestAdoptLedgerWithState_NoEventSinkInstalled_IsQuiet(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	txMap := shamap.New(shamap.TypeTransaction)
	stateMap := shamap.New(shamap.TypeState)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	var adoptedHash [32]byte
	adoptedHash[0] = 0xF6
	hdr := &header.LedgerHeader{
		LedgerIndex: svc.GetClosedLedgerIndex() + 1,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}
	hdr.Hash = header.CalculateHash(*hdr)
	adoptedHash = hdr.Hash
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdr, stateMap, txMap),
		"adopt must succeed without an event sink")

	// Without an event sink there is nothing to publish later.
	svc.mu.RLock()
	_, stashed := svc.pendingValidation[adoptedHash]
	svc.mu.RUnlock()
	assert.False(t, stashed,
		"no event sink means nothing to stash")
}

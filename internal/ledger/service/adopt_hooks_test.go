package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdoptLedgerWithState_FiresOnLedgerClosedHook pins F3: peer-adopted
// ledgers must fire hooks.OnLedgerClosed so WebSocket `ledger` stream
// subscribers see a ledger-closed event for every ledger the node adopts
// from peers. Without this, the `ledger` stream silently skips every
// peer-adopted ledger — an observable divergence from rippled where
// pubLedger fires for both consensus-closed and sync-adopted ledgers.
func TestAdoptLedgerWithState_FiresOnLedgerClosedHook(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	// Capture OnLedgerClosed invocations.
	var (
		mu               sync.Mutex
		callCount        int
		capturedInfo     *LedgerInfo
		capturedTxCount  int
		capturedValRange string
	)
	done := make(chan struct{}, 1)

	hooks := DefaultEventHooks()
	hooks.OnLedgerClosed = func(info *LedgerInfo, txCount int, validatedLedgers string) {
		mu.Lock()
		callCount++
		capturedInfo = info
		capturedTxCount = txCount
		capturedValRange = validatedLedgers
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	}
	svc.SetEventHooks(hooks)

	// Build a tx map with 2 txs so txCount assertion is meaningful.
	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	blob1, id1 := makeTxMetaBlobForTest(t, []byte("hook-tx-blob-A-padding-padpad"), 0)
	blob2, id2 := makeTxMetaBlobForTest(t, []byte("hook-tx-blob-B-padding-padpad"), 1)
	require.NoError(t, txMap.PutWithNodeType(id1, blob1, shamap.NodeTypeTransactionWithMeta))
	require.NoError(t, txMap.PutWithNodeType(id2, blob2, shamap.NodeTypeTransactionWithMeta))
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	var adoptedHash [32]byte
	adoptedHash[0] = 0xF3
	adoptedSeq := svc.GetClosedLedgerIndex() + 1
	hdr := &header.LedgerHeader{
		LedgerIndex: adoptedSeq,
		Hash:        adoptedHash,
		TxHash:      txRoot,
		AccountHash: stateRoot,
		CloseTime:   time.Unix(1700000000, 0),
	}

	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdr, stateMap, txMap))

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
	assert.NotEmpty(t, capturedValRange, "validatedLedgers range must be populated")
}

// TestAdoptLedgerWithState_FiresOnTransactionHook pins F3: peer-adopted
// ledgers must fire hooks.OnTransaction for every tx in the installed tx
// map so WebSocket `transactions` stream subscribers see every adopted
// tx. Matches rippled's pubValidatedTransactions which emits for every tx
// in a newly-published ledger regardless of whether it was consensus-
// closed locally or adopted from a peer.
func TestAdoptLedgerWithState_FiresOnTransactionHook(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	var txCallCount int32
	seenHashes := &sync.Map{}
	done := make(chan struct{}, 4)

	hooks := DefaultEventHooks()
	hooks.OnTransaction = func(txi TransactionInfo, result TxResult, seq uint32, hash [32]byte, closeTime time.Time) {
		atomic.AddInt32(&txCallCount, 1)
		seenHashes.Store(txi.Hash, struct{}{})
		select {
		case done <- struct{}{}:
		default:
		}
	}
	svc.SetEventHooks(hooks)

	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	blob1, id1 := makeTxMetaBlobForTest(t, []byte("hook-onTx-blob-A-padding-padp"), 0)
	blob2, id2 := makeTxMetaBlobForTest(t, []byte("hook-onTx-blob-B-padding-padp"), 1)
	require.NoError(t, txMap.PutWithNodeType(id1, blob1, shamap.NodeTypeTransactionWithMeta))
	require.NoError(t, txMap.PutWithNodeType(id2, blob2, shamap.NodeTypeTransactionWithMeta))
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	var adoptedHash [32]byte
	adoptedHash[0] = 0xF4
	hdr := &header.LedgerHeader{
		LedgerIndex: svc.GetClosedLedgerIndex() + 1,
		Hash:        adoptedHash,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdr, stateMap, txMap))

	// Wait for both tx dispatches.
	deadline := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
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

// TestAdoptLedgerWithState_StashesLegacyEventUntilValidated pins F3: the
// legacy eventCallback must NOT fire at adopt time (the adopted ledger
// isn't yet trust-validated), but the event must be stashed so a later
// SetValidatedLedger for the same hash drains it exactly once. Matches
// the consensus-close path's stash/drain pattern.
func TestAdoptLedgerWithState_StashesLegacyEventUntilValidated(t *testing.T) {
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

	svc.SetEventCallback(func(event *LedgerAcceptedEvent) {
		mu.Lock()
		callbackCount++
		lastEvent = event
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	})

	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	blob1, id1 := makeTxMetaBlobForTest(t, []byte("stash-tx-blob-A-padding-padpd"), 0)
	require.NoError(t, txMap.PutWithNodeType(id1, blob1, shamap.NodeTypeTransactionWithMeta))
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	var adoptedHash [32]byte
	adoptedHash[0] = 0xF5
	adoptedSeq := svc.GetClosedLedgerIndex() + 1
	hdr := &header.LedgerHeader{
		LedgerIndex: adoptedSeq,
		Hash:        adoptedHash,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdr, stateMap, txMap))

	// Give any erroneously-dispatched callback a chance to run.
	select {
	case <-done:
		t.Fatal("eventCallback must NOT fire at adopt time — adopted ledger is not yet trust-validated")
	case <-time.After(100 * time.Millisecond):
	}

	mu.Lock()
	assert.Equal(t, 0, callbackCount,
		"eventCallback must NOT fire at adopt time")
	mu.Unlock()

	// The event must be stashed keyed by hash so SetValidatedLedger can drain it.
	svc.mu.RLock()
	_, stashed := svc.pendingValidation[adoptedHash]
	svc.mu.RUnlock()
	assert.True(t, stashed,
		"adopt must stash a LedgerAcceptedEvent keyed by the adopted ledger hash")

	// When the validation tracker confirms this ledger, SetValidatedLedger
	// must drain the stashed event and fire eventCallback exactly once.
	svc.SetValidatedLedger(adoptedSeq, adoptedHash)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("eventCallback did not fire after SetValidatedLedger drained the stashed event")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, callbackCount,
		"eventCallback must fire exactly once after SetValidatedLedger")
	require.NotNil(t, lastEvent)
	require.NotNil(t, lastEvent.LedgerInfo)
	assert.Equal(t, adoptedSeq, lastEvent.LedgerInfo.Sequence,
		"drained event must carry the adopted ledger's seq")
	assert.Equal(t, adoptedHash, lastEvent.LedgerInfo.Hash,
		"drained event must carry the adopted ledger's hash")
	assert.Len(t, lastEvent.TransactionResults, 1,
		"drained event must carry the adopted tx results")

	// The stash must be empty after drain.
	svc.mu.RLock()
	_, stillStashed := svc.pendingValidation[adoptedHash]
	svc.mu.RUnlock()
	assert.False(t, stillStashed,
		"SetValidatedLedger must remove the stashed event after firing")
}

// TestAdoptLedgerWithState_NoHooksInstalled_IsQuiet verifies that the
// adopt path doesn't panic or otherwise misbehave when neither hooks nor
// eventCallback are installed. The production wiring installs both, but
// tests and embedders may run without either; the helper must tolerate
// that.
func TestAdoptLedgerWithState_NoHooksInstalled_IsQuiet(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	// Deliberately: no SetEventHooks, no SetEventCallback.

	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	var adoptedHash [32]byte
	adoptedHash[0] = 0xF6
	hdr := &header.LedgerHeader{
		LedgerIndex: svc.GetClosedLedgerIndex() + 1,
		Hash:        adoptedHash,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdr, stateMap, txMap),
		"adopt must succeed even when no hooks or eventCallback are installed")

	// No pending stash entry should exist when there is no eventCallback.
	svc.mu.RLock()
	_, stashed := svc.pendingValidation[adoptedHash]
	svc.mu.RUnlock()
	assert.False(t, stashed,
		"no eventCallback means nothing to stash")
}

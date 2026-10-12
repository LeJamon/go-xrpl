package service

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestValidationEvictsColdHistoryWithoutTransactionReads(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LedgerCacheSize = 64
	serviceFamily := backend.NewMemory()
	t.Cleanup(func() { require.NoError(t, serviceFamily.Close()) })
	cfg.SHAMapFamily = serviceFamily
	cfg.Execution = newTestExecutionStore(t, serviceFamily)
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	parent := svc.GetClosedLedger()
	old, err := ledger.NewOpen(parent, parent.CloseTime().Add(time.Second))
	require.NoError(t, err)
	t.Cleanup(old.Discard)
	txHash := [32]byte{0x51}
	movedTxHash := [32]byte{0x52}
	require.NoError(t, old.AddTransactionWithMeta(txHash, make([]byte, 16)))
	require.NoError(t, old.AddTransactionWithMeta(movedTxHash, make([]byte, 16)))
	require.NoError(t, old.Close(parent.CloseTime().Add(time.Second), 0))
	txMap, err := old.TxMapSnapshot()
	require.NoError(t, err)
	memory := backend.NewMemory()
	t.Cleanup(func() { require.NoError(t, memory.Close()) })
	copyBudget := &relayTestMemory{limit: 512 << 20}
	require.NoError(t, txMap.StoreAllBatched(t.Context(), copyBudget, func(entries []shamap.FlushEntry) error {
		return memory.StoreBatch(t.Context(), entries)
	}))
	state, err := old.StateMapSnapshot()
	require.NoError(t, err)
	require.NoError(t, state.StoreAllBatched(t.Context(), copyBudget, func(entries []shamap.FlushEntry) error {
		return memory.StoreBatch(t.Context(), entries)
	}))
	root, err := txMap.Hash()
	require.NoError(t, err)
	family := &acceptanceBlockingFamily{Family: memory, entered: make(chan struct{}), release: make(chan struct{})}
	defer family.unblock()
	coldTx, err := shamap.NewFromRootHash(shamap.TypeTransaction, root, family)
	require.NoError(t, err)
	coldExecution := newTestExecutionStore(t, family)
	coldOld, err := ledger.NewFromHeader(old.Header(), state, coldTx, old.Fees(), coldExecution)
	require.NoError(t, err)
	t.Cleanup(coldOld.Discard)

	latest := old
	for range cfg.LedgerCacheSize {
		next, buildErr := ledger.NewOpen(latest, latest.CloseTime().Add(time.Second))
		require.NoError(t, buildErr)
		require.NoError(t, next.Close(latest.CloseTime().Add(time.Second), 0))
		if latest != old {
			latest.Discard()
		}
		latest = next
	}
	t.Cleanup(latest.Discard)
	svc.mu.Lock()
	svc.historyComponent.mu.Lock()
	svc.putHistoryLocked(coldOld)
	svc.putHistoryLocked(latest)
	svc.txIndex[txHash] = coldOld.Sequence()
	svc.txPositionIndex[txHash] = 0
	svc.txIndex[movedTxHash] = latest.Sequence()
	svc.txPositionIndex[movedTxHash] = 1
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()

	family.armed.Store(true)
	validated := make(chan struct{})
	go func() {
		svc.SetValidatedLedger(latest.Sequence(), latest.Hash())
		close(validated)
	}()
	select {
	case <-family.entered:
		t.Fatal("validation eviction read a cold transaction tree")
	case <-validated:
	case <-time.After(5 * time.Second):
		t.Fatal("validation did not finish with cold history")
	}
	require.Equal(t, latest.Hash(), svc.GetValidatedLedger().Hash())
	require.False(t, svc.openLedgerMu.Snapshot().Held)
	svc.historyComponent.mu.RLock()
	_, retained := svc.ledgerHistory[coldOld.Sequence()]
	_, indexed := svc.txIndex[txHash]
	_, positioned := svc.txPositionIndex[txHash]
	movedSeq, movedPosition := svc.txIndex[movedTxHash], svc.txPositionIndex[movedTxHash]
	svc.historyComponent.mu.RUnlock()
	require.False(t, retained)
	require.False(t, indexed)
	require.False(t, positioned)
	require.Equal(t, latest.Sequence(), movedSeq)
	require.EqualValues(t, 1, movedPosition)
}

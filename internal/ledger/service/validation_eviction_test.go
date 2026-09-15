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
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	parent := svc.GetClosedLedger()
	old, err := ledger.NewOpen(parent, parent.CloseTime().Add(time.Second))
	require.NoError(t, err)
	txHash := [32]byte{0x51}
	movedTxHash := [32]byte{0x52}
	require.NoError(t, old.AddTransactionWithMeta(txHash, make([]byte, 16)))
	require.NoError(t, old.AddTransactionWithMeta(movedTxHash, make([]byte, 16)))
	require.NoError(t, old.Close(parent.CloseTime().Add(time.Second), 0))
	txMap, err := old.TxMapSnapshot()
	require.NoError(t, err)
	memory := backend.NewMemory()
	require.NoError(t, txMap.StoreDirty(func(entries []shamap.FlushEntry) error {
		return memory.StoreBatch(t.Context(), entries)
	}))
	root, err := txMap.Hash()
	require.NoError(t, err)
	family := &acceptanceBlockingFamily{Family: memory, entered: make(chan struct{}), release: make(chan struct{})}
	defer family.unblock()
	coldTx, err := shamap.NewFromRootHash(shamap.TypeTransaction, root, family)
	require.NoError(t, err)
	state, err := old.StateMapSnapshot()
	require.NoError(t, err)
	coldOld, err := ledger.NewFromHeader(old.Header(), state, coldTx, old.Fees())
	require.NoError(t, err)

	latest := old
	for range cfg.LedgerCacheSize {
		next, buildErr := ledger.NewOpen(latest, latest.CloseTime().Add(time.Second))
		require.NoError(t, buildErr)
		require.NoError(t, next.Close(latest.CloseTime().Add(time.Second), 0))
		latest = next
	}
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

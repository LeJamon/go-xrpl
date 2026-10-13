package service

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestLedgerReadLeaseSurvivesHistoryEvictionAndRotation(t *testing.T) {
	runtime, database, family := newBoundedRetentionRuntime(t)
	t.Cleanup(func() { runtime.close(t) })
	require.NoError(t, runtime.service.Start())
	key := keylet.Keylet{Key: [32]byte{0x71, 0x33}}
	want := []byte("leased historical value")
	require.True(t, runtime.service.openLedgerView.Modify(func(l *ledger.Ledger) bool {
		require.NoError(t, l.Insert(key, want))
		return true
	}))
	_, err := runtime.service.AcceptLedger(t.Context())
	require.NoError(t, err)
	lease, err := runtime.service.AcquireLedgerRead(t.Context(), "closed")
	require.NoError(t, err)
	defer lease.Close()
	root := lease.Ledger.Header().AccountHash
	require.True(t, runtime.service.openLedgerView.Modify(func(l *ledger.Ledger) bool {
		require.NoError(t, l.Erase(key))
		return true
	}))
	for range 4 {
		for range 4 {
			_, err := runtime.service.AcceptLedger(t.Context())
			require.NoError(t, err)
		}
		seq, guard, err := runtime.service.PrepareStateRetention(t.Context(), runtime.service.GetValidatedLedger().Sequence(), nil)
		require.NoError(t, err)
		rotated, err := database.RotateGenerationWithRetention(t.Context(), seq, 1, guard, family.BeginPrune)
		require.NoError(t, err)
		require.True(t, rotated)
	}
	require.Nil(t, runtime.service.historyComponent.ledgerBySequence(lease.Ledger.Sequence()))
	node, err := family.FetchDurable(t.Context(), root)
	require.NoError(t, err)
	require.NotNil(t, node, "leased historical root was retired")
	got, err := lease.Ledger.Read(key)
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.NoError(t, lease.Close())
	require.NoError(t, lease.Close())
	for _, held := range runtime.execution.Roots() {
		require.NotEqual(t, root, held.Hash)
	}
}

func TestLedgerReadLeaseDrainsBeforeServiceShutdown(t *testing.T) {
	runtime, _, _ := newBoundedRetentionRuntime(t)
	t.Cleanup(func() { runtime.close(t) })
	require.NoError(t, runtime.service.Start())
	before := runtime.execution.Stats().RootBorrowers
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := runtime.service.AcquireLedgerRead(canceled, "closed")
	require.ErrorIs(t, err, context.Canceled)
	_, err = runtime.service.AcquireLedgerRead(t.Context(), "invalid selector")
	require.Error(t, err)
	require.Equal(t, before, runtime.execution.Stats().RootBorrowers)
	lease, err := runtime.service.AcquireLedgerRead(t.Context(), "closed")
	require.NoError(t, err)
	defer lease.Close()
	stopped := make(chan struct{})
	go func() { runtime.service.Stop(); close(stopped) }()
	require.Eventually(t, func() bool {
		runtime.service.lifecycleMu.Lock()
		defer runtime.service.lifecycleMu.Unlock()
		return runtime.service.lifecycleState == serviceStopping
	}, time.Second, time.Millisecond)
	select {
	case <-stopped:
		t.Fatal("shutdown passed an active read lease")
	default:
	}
	require.NoError(t, lease.Close())
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not drain a closed read lease")
	}
}

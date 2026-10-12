package service

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/stretchr/testify/require"
)

func TestPrepareSuccessorAccountsForTargetTransactionBytes(t *testing.T) {
	const targetBytes = 128 << 10
	budgetLimit := uint64(preferredTransitionBaseBytes) + 1
	budget := &preferredMemoryBudget{
		limit:    budgetLimit,
		capacity: budgetLimit,
		changed:  make(chan struct{}),
	}
	cfg := DefaultConfig()
	cfg.PersistenceMemory = budget
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	parent := svc.GetClosedLedger()
	closeTime := parent.CloseTime().Add(time.Second)
	target, err := ledger.NewOpenForBuild(parent, closeTime)
	require.NoError(t, err)
	require.NoError(t, target.AddTransactionWithMeta([32]byte{1}, bytes.Repeat([]byte{0xa5}, targetBytes)))
	require.NoError(t, target.Close(closeTime, 0))

	prepared, err := svc.prepareSuccessor(context.Background(), target, successorOptions{
		preferred: true,
		admitted:  true,
	})
	if prepared != nil {
		prepared.Release()
	}
	require.ErrorIs(t, err, ErrPreferredTransitionMemory)
	require.Zero(t, budget.Used())
}

func TestPrepareSuccessorAccountsForRetryTransactionBytes(t *testing.T) {
	const retryBytes = 2 << 20
	budgetLimit := uint64(preferredTransitionBaseBytes) + retryBytes - 1
	budget := &preferredMemoryBudget{
		limit:    budgetLimit,
		capacity: budgetLimit,
		changed:  make(chan struct{}),
	}
	cfg := DefaultConfig()
	cfg.PersistenceMemory = budget
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	prepared, err := svc.prepareSuccessor(ctx, svc.GetClosedLedger(), successorOptions{
		admitted: true,
		retries:  []openledger.PendingTx{{Blob: bytes.Repeat([]byte{0x5a}, retryBytes)}},
	})
	if prepared != nil {
		_ = prepared.Wait(ctx)
		prepared.Release()
	}
	require.ErrorIs(t, err, ErrPreferredTransitionMemory)
	require.Zero(t, budget.Used())
}

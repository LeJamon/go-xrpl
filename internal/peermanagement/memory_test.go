package peermanagement

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testMemoryAccount struct {
	mu      sync.Mutex
	limit   uint64
	used    uint64
	wake    chan struct{}
	waiting chan struct{}
}

func (b *testMemoryAccount) TryReserve(n uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.limit-b.used {
		return false
	}
	b.used += n
	return true
}

func (b *testMemoryAccount) Release(n uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.used {
		panic("test account release exceeds reservation")
	}
	b.used -= n
	if b.wake != nil {
		close(b.wake)
		b.wake = nil
	}
}

func (b *testMemoryAccount) Changed() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.wake == nil {
		b.wake = make(chan struct{})
	}
	if b.waiting != nil {
		close(b.waiting)
		b.waiting = nil
	}
	return b.wake
}

func TestReadBudgetWakesOnParentRelease(t *testing.T) {
	waiting := make(chan struct{})
	parent := &testMemoryAccount{limit: 8, waiting: waiting}
	require.True(t, parent.TryReserve(8))
	budget := newReadBudget(8)
	budget.parent = parent
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- budget.acquire(ctx, nil, 4) }()
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal("reader did not subscribe to parent capacity changes")
	}
	parent.Release(8)
	require.NoError(t, <-result)
	reservation := newInboundReservation(budget, 4)
	retained := reservation.retain()
	reservation.release()
	require.Equal(t, uint64(4), parent.used)
	retained.release()
	require.Zero(t, parent.used)
}

func TestReadBudgetParentWaitCancels(t *testing.T) {
	parent := &testMemoryAccount{limit: 4}
	require.True(t, parent.TryReserve(4))
	budget := newReadBudget(4)
	budget.parent = parent
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, budget.acquire(ctx, nil, 1), context.Canceled)
	require.Zero(t, budget.used)
	parent.Release(4)
}

func TestOutboundBudgetProtectsParentCriticalReservation(t *testing.T) {
	general := &testMemoryAccount{limit: 16}
	critical := &testMemoryAccount{limit: outboundCriticalByteReserve}
	budget := newOutboundBudget(2*outboundCriticalByteReserve, 1)
	budget.parent = general
	budget.criticalParent = critical
	account := budget.attach()
	require.True(t, account.reserve(16, false))
	require.False(t, account.reserve(1, false))
	require.True(t, account.reserve(16, true))
	require.Equal(t, uint64(16), critical.used)
	account.close()
	require.Zero(t, general.used)
	require.Zero(t, critical.used)
	account.close()
}

func TestOutboundBudgetParentRollbackAndCriticalOverflow(t *testing.T) {
	general := &testMemoryAccount{limit: 32}
	critical := &testMemoryAccount{limit: outboundCriticalByteReserve}
	budget := newOutboundBudget(2*outboundCriticalByteReserve, 1)
	budget.parent = general
	budget.criticalParent = critical
	account := budget.attach()
	require.True(t, critical.TryReserve(outboundCriticalByteReserve))
	require.False(t, account.reserve(outboundCriticalByteReserve+8, true))
	require.Zero(t, general.used, "failed critical reservation must unwind its general portion")
	critical.Release(outboundCriticalByteReserve)
	require.True(t, account.reserve(outboundCriticalByteReserve+8, true))
	require.Equal(t, uint64(8), general.used)
	account.release(16, true)
	require.Zero(t, general.used)
	require.Equal(t, uint64(outboundCriticalByteReserve-8), critical.used)
	account.close()
	require.Zero(t, critical.used)
}

func TestOverlayMemoryBudgetsRequireUnusedLifecycle(t *testing.T) {
	overlay := &Overlay{
		inboundReadBudget: newReadBudget(8),
		outboundBudget:    newOutboundBudget(2*outboundCriticalByteReserve, 1),
	}
	parent := &testMemoryAccount{limit: 8}
	require.NoError(t, overlay.SetMemoryBudgets(parent, parent, parent))
	account := overlay.outboundBudget.attach()
	require.Error(t, overlay.SetMemoryBudgets(parent, parent, parent))
	account.close()
	overlay.lifecycleState = overlayLifecycleRunning
	require.Error(t, overlay.SetMemoryBudgets(parent, parent, parent))
}

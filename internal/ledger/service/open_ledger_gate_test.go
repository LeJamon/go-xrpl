package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPriorityGateRunsPriorityFIFOBeforeIngress(t *testing.T) {
	var gate priorityGate
	gate.LockRole(openLedgerTransition)

	acquired := make(chan openLedgerRole, 3)
	lock := func(role openLedgerRole) {
		go func() {
			gate.LockRole(role)
			acquired <- role
			gate.Unlock()
		}()
	}

	lock(openLedgerIngress)
	require.Eventually(t, func() bool {
		return gate.Snapshot().QueuedIngress == 1
	}, time.Second, time.Millisecond)
	lock(openLedgerConsensus)
	require.Eventually(t, func() bool {
		return gate.Snapshot().QueuedPriority == 1
	}, time.Second, time.Millisecond)
	lock(openLedgerValidation)
	require.Eventually(t, func() bool {
		return gate.Snapshot().QueuedPriority == 2
	}, time.Second, time.Millisecond)
	require.False(t, gate.TryLock(), "TryLock must not bypass queued work")

	gate.Unlock()
	for _, want := range []openLedgerRole{
		openLedgerConsensus,
		openLedgerValidation,
		openLedgerIngress,
	} {
		select {
		case got := <-acquired:
			require.Equal(t, want, got)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s admission", want)
		}
	}
}

func TestPriorityGateRecordsWaitAndHold(t *testing.T) {
	var gate priorityGate
	gate.LockRole(openLedgerTransition)

	acquired := make(chan openLedgerGateWait, 1)
	go func() { acquired <- gate.LockRole(openLedgerConsensus) }()
	require.Eventually(t, func() bool {
		return gate.Snapshot().QueuedPriority == 1
	}, time.Second, time.Millisecond)

	gate.Unlock()
	wait := <-acquired
	snapshot := gate.Snapshot()
	require.Equal(t, openLedgerConsensus, snapshot.Owner)
	require.True(t, snapshot.Held)
	require.Positive(t, wait.Wait)
	require.Positive(t, snapshot.HeldFor)
	gate.Unlock()
	require.False(t, gate.Snapshot().Held)
}

func TestPriorityGateRateLimitsSlowOwnerLogs(t *testing.T) {
	var gate priorityGate
	var events []openLedgerGateSlowEvent
	gate.setSlowLogger(func(event openLedgerGateSlowEvent) {
		_ = gate.Snapshot()
		events = append(events, event)
	})
	for range 2 {
		gate.LockRole(openLedgerIngress)
		gate.mu.Lock()
		gate.ownerSince = time.Now().Add(-2 * openLedgerGateSlowHold)
		gate.mu.Unlock()
		gate.Unlock()
	}
	require.Len(t, events, 1)
	require.Equal(t, openLedgerIngress, events[0].Role)
	require.GreaterOrEqual(t, events[0].Hold, openLedgerGateSlowHold)
}

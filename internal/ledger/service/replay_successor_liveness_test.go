package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func TestReplaySuccessorPreparationCancellationPreservesFrontier(t *testing.T) {
	svc, repaired, family := replaySuccessorLivenessFixture(t)
	closed := svc.GetClosedLedger()
	queue := svc.txQueue

	blob, txHash := startupPaymentBlob(t, "replay-successor-cancel", 1)
	result, err := svc.SubmitOpenLedgerTxDetailed(blob, true)
	require.NoError(t, err)
	require.True(t, result.Applied)
	open := svc.openLedgerView.Current()
	fault := latchReplaySuccessorFault(t, svc, repaired)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- svc.restoreReplayParentAt(ctx, repaired, [32]byte{}) }()

	select {
	case <-family.started:
	case <-time.After(5 * time.Second):
		t.Fatal("replay successor preparation did not reach the storage barrier")
	}

	reads := make(chan struct{})
	go func() {
		_, _ = svc.GetLedgerBySequence(closed.Sequence())
		_ = svc.GetClosedLedger()
		_ = svc.GetValidatedLedger()
		_ = svc.TxQMetrics()
		close(reads)
	}()
	select {
	case <-reads:
	case <-time.After(time.Second):
		t.Fatal("replay successor preparation held service or history locks")
	}
	require.False(t, svc.openLedgerMu.Snapshot().Held, "replay preparation retained the service gate")

	cancel()
	family.unblock()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Same(t, closed, svc.GetClosedLedger())
	require.Same(t, repaired, svc.GetValidatedLedger())
	require.Same(t, open, svc.openLedgerView.Current())
	present, err := open.TxExists(txHash)
	require.NoError(t, err)
	require.True(t, present, "canceled repair must retain local ingress")
	require.Same(t, queue, svc.txQueue)
	require.True(t, svc.ReplayBlocked())
	require.NotEmpty(t, fault.ID)
}

func TestReplaySuccessorRepairPublishesAndRetainsLocalTransaction(t *testing.T) {
	svc := replayFaultService(t, "")
	closed := svc.GetClosedLedger()
	blob, txHash := startupPaymentBlob(t, "replay-successor-publish", 1)
	result, err := svc.SubmitOpenLedgerTxDetailed(blob, true)
	require.NoError(t, err)
	require.True(t, result.Applied)

	state, err := closed.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := closed.TxMapSnapshot()
	require.NoError(t, err)
	repaired, err := ledger.NewFromHeader(closed.Header(), state, txMap, closed.Fees())
	require.NoError(t, err)
	require.NotSame(t, closed, repaired)
	fault := latchReplaySuccessorFault(t, svc, repaired)

	require.NoError(t, svc.restoreReplayParentAt(t.Context(), repaired, closed.Hash()))
	require.True(t, svc.ReplayBlocked(), "repair publication must not clear the replay fault itself")
	require.Same(t, repaired, svc.GetClosedLedger())
	current := svc.openLedgerView.Current()
	require.Equal(t, repaired.Hash(), current.ParentHash())
	present, err := current.TxExists(txHash)
	require.NoError(t, err)
	require.True(t, present, "published repaired successor must retain local ingress")
	require.NotEmpty(t, fault.ID)
}

func TestReplaySuccessorRepairRejectsStaleFaultAndClosedIdentity(t *testing.T) {
	svc := replayFaultService(t, "")
	closed := svc.GetClosedLedger()
	state, err := closed.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := closed.TxMapSnapshot()
	require.NoError(t, err)
	repaired, err := ledger.NewFromHeader(closed.Header(), state, txMap, closed.Fees())
	require.NoError(t, err)
	latchReplaySuccessorFault(t, svc, repaired)

	require.ErrorIs(t, svc.RevalidateReplayFault(t.Context(), "changed-fault-id"), replayfault.ErrFaultIDMismatch)
	frontier := svc.GetClosedLedger()
	err = svc.restoreReplayParentAt(t.Context(), repaired, [32]byte{0x99})
	require.ErrorContains(t, err, "closed ledger changed")
	require.Same(t, frontier, svc.GetClosedLedger())
	require.True(t, svc.ReplayBlocked())
}

type replaySuccessorBlockingFamily struct {
	shamap.Family
	started     chan struct{}
	release     chan struct{}
	once        sync.Once
	releaseOnce sync.Once
}

func (f *replaySuccessorBlockingFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.once.Do(func() { close(f.started) })
	select {
	case <-f.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return f.Family.Fetch(ctx, hash)
}

func (f *replaySuccessorBlockingFamily) unblock() {
	f.releaseOnce.Do(func() { close(f.release) })
}

func replaySuccessorLivenessFixture(t *testing.T) (*Service, *ledger.Ledger, *replaySuccessorBlockingFamily) {
	t.Helper()
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	repaired, backing := coldAcceptanceParent(t, svc.GetClosedLedger())
	family := &replaySuccessorBlockingFamily{
		Family:  backing.Family,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	repaired.SetStateMapFamily(family)
	require.NoError(t, repaired.SetValidated())
	svc.mu.Lock()
	svc.historyComponent.mu.Lock()
	svc.putHistoryLocked(repaired)
	svc.validatedLedger = repaired
	svc.historyComponent.mu.Unlock()
	svc.mu.Unlock()
	t.Cleanup(family.unblock)
	return svc, repaired, family
}

func latchReplaySuccessorFault(t *testing.T, svc *Service, target *ledger.Ledger) replayfault.Fault {
	t.Helper()
	h := target.Header()
	fault := replayfault.Fault{
		Class:      replayfault.MissingState,
		ParentHash: h.ParentHash,
		TargetHash: h.Hash,
		Sequence:   h.LedgerIndex,
	}
	require.NoError(t, recordTestReplayFault(svc.replayFaults, fault))
	stored := svc.replayFaults.Snapshot()
	require.NotNil(t, stored)
	require.True(t, svc.ReplayBlocked())
	return *stored
}

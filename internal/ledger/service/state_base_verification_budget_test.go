package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func TestBackgroundVerificationWorkerBudget(t *testing.T) {
	for _, cpus := range []int{0, 1, 2, 3, 16, 64} {
		workers := backgroundVerificationWorkers(cpus)
		require.Positive(t, workers)
		require.LessOrEqual(t, workers, 2)
		if cpus > 1 {
			require.Less(t, workers, cpus)
		}
	}
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	svc.config.FastLoadWorkers = 64
	require.LessOrEqual(t, svc.backgroundStateVerificationPolicy().workers, 2)
}

func TestBackgroundVerificationYieldResumesAndCancels(t *testing.T) {
	var busy atomic.Bool
	busy.Store(true)
	checked := make(chan struct{}, 1)
	snapshot := func() openLedgerGateSnapshot {
		select {
		case checked <- struct{}{}:
		default:
		}
		if busy.Load() {
			return openLedgerGateSnapshot{QueuedPriority: 1}
		}
		return openLedgerGateSnapshot{}
	}
	done := make(chan error, 1)
	go func() { done <- paceBackgroundVerification(t.Context(), 0, snapshot) }()
	<-checked
	select {
	case <-done:
		t.Fatal("background verifier did not yield to queued foreground work")
	default:
	}
	busy.Store(false)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("background verifier did not resume")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, paceBackgroundVerification(ctx, time.Second, snapshot), context.Canceled)

	busy.Store(true)
	started := time.Now()
	require.NoError(t, paceBackgroundVerification(t.Context(), 0, snapshot))
	require.GreaterOrEqual(t, time.Since(started), backgroundVerificationGateYield)
	require.Less(t, time.Since(started), 2*time.Second, "continuous ingress must not starve checkpoint work")
}

func TestVerificationPolicyPreservesStrictProofAndCancellation(t *testing.T) {
	f := newStateBaseRecertificationFixture(t)
	f.svc.StopStateBaseRecertification()
	var reads atomic.Int32
	policy := storedSHAMapVerificationPolicy{workers: 2, pause: func(ctx context.Context, work time.Duration) error {
		if work == 0 {
			reads.Add(1)
		}
		return ctx.Err()
	}}
	metrics, err := f.svc.verifyStoredSHAMapMeasuredWithPolicy(t.Context(), f.stateRoot, shamap.TypeState, policy)
	require.NoError(t, err)
	require.Positive(t, metrics.nodes)
	require.Positive(t, reads.Load())
	ctx, cancel := context.WithCancel(t.Context())
	policy.pause = func(context.Context, time.Duration) error { cancel(); return ctx.Err() }
	_, err = f.svc.verifyStoredSHAMapMeasuredWithPolicy(ctx, f.stateRoot, shamap.TypeState, policy)
	require.ErrorIs(t, err, context.Canceled)
}

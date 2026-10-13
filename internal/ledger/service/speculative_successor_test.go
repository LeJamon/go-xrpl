package service

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/stretchr/testify/require"
)

func TestFinishedSpeculationReusesMemoryForReplacement(t *testing.T) {
	for _, repair := range []bool{false, true} {
		t.Run(map[bool]string{false: "preferred", true: "repair"}[repair], func(t *testing.T) {
			budget := &preferredMemoryBudget{limit: 8 << 20, capacity: 8 << 20, changed: make(chan struct{})}
			cfg := DefaultConfig()
			cfg.PersistenceMemory = budget
			svc, err := New(cfg)
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			t.Cleanup(svc.Stop)
			blob, hash := startupPaymentBlob(t, "speculative-memory", 1)
			pending, err := openledger.ParsePendingTx(blob)
			require.NoError(t, err)
			parent := svc.GetClosedLedger()
			p := &PreparedPreferredLedger{
				service: svc, ctx: t.Context(), parent: parent,
				openView: svc.openLedgerView, queue: svc.txQueue, validatedAtPrepare: svc.validatedLedger,
				options: successorOptions{retries: []openledger.PendingTx{pending}, anyDisputes: true},
			}
			p.releaseWork, err = svc.acquireLedgerWork(t.Context())
			require.NoError(t, err)
			p.releaseMemory, err = p.reserveMemory(budget.limit)
			require.NoError(t, err)
			svc.mu.Lock()
			svc.scheduleSpeculativeSuccessorLocked(p)
			job := svc.speculativeSuccessor
			job.cancel()
			svc.mu.Unlock()
			select {
			case <-job.done:
			case <-time.After(time.Second):
				t.Fatal("canceled successor did not drain")
			}
			require.Equal(t, budget.limit, budget.Used())

			next, err := ledger.NewOpen(parent, parent.CloseTime().Add(time.Second))
			require.NoError(t, err)
			require.NoError(t, next.Close(next.CloseTime(), 0))
			prepared, err := svc.prepareSuccessor(t.Context(), next, successorOptions{preferred: true, allowReplayFault: repair})
			require.NoError(t, err, "a finished successor must not exhaust its replacement's budget")
			t.Cleanup(prepared.Release)
			require.NoError(t, prepared.Wait(t.Context()))
			require.Equal(t, budget.limit, budget.Used())
			published, err := prepared.TryPublish()
			require.NoError(t, err)
			require.True(t, published)
			prepared.Finish()
			prepared.Release()
			exists, err := svc.OpenLedgerHasTx(hash)
			require.NoError(t, err)
			require.True(t, exists, "the failed successor's input must survive replacement")
			svc.Stop()
			require.Zero(t, budget.Used())
		})
	}
}

func TestSuccessorMemoryAbortedGrowthPreservesOriginalClaim(t *testing.T) {
	budget := &preferredMemoryBudget{limit: 8 << 20, capacity: 8 << 20, changed: make(chan struct{})}
	svc := &Service{config: Config{PersistenceMemory: budget}}
	p := &PreparedPreferredLedger{service: svc, ctx: context.Background()}
	original, err := p.reserveMemory(1 << 20)
	require.NoError(t, err)
	growth, err := p.memory.claim(8<<20, budget.limit)
	require.NoError(t, err)
	require.Equal(t, uint64(8<<20), budget.Used())
	growth()
	growth()
	require.Equal(t, uint64(1<<20), budget.Used())
	original()
	require.Zero(t, budget.Used())
}

func TestSpeculativeSuccessorStopDrainsRetiredAndPendingOwners(t *testing.T) {
	budget := &preferredMemoryBudget{limit: 512 << 20, capacity: 512 << 20, changed: make(chan struct{})}
	cfg := DefaultConfig()
	cfg.PersistenceMemory = budget
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	master := jtx.MasterAccount()
	parent, family := successorPreparationParent(t, svc.GetClosedLedger(), master.AccountID())
	require.NoError(t, svc.SwitchToPreferredLedger(parent))
	blob, _ := startupPaymentBlob(t, "speculative-shutdown", 1)
	local, err := openledger.ParsePendingTx(blob)
	require.NoError(t, err)
	svc.localTxs.PushBack(svc.GetOpenLedger().Sequence(), local)
	family.armed.Store(true)
	_, err = svc.AcceptConsensusResult(t.Context(), parent, nil, nil, parent.CloseTime().Add(time.Second), true)
	require.NoError(t, err)
	awaitSuccessorPreparation(t, family.entered, family.unblock, &family.fetches)
	svc.mu.RLock()
	first := svc.speculativeRunning
	svc.mu.RUnlock()
	require.NotNil(t, first)
	used := budget.Used()
	require.NotZero(t, used)

	parent = svc.GetClosedLedger()
	_, err = svc.AcceptConsensusResult(t.Context(), parent, nil, nil, parent.CloseTime().Add(time.Second), true)
	require.NoError(t, err)
	svc.mu.RLock()
	second := svc.speculativeSuccessor
	require.Same(t, first, svc.speculativeRunning)
	require.NotSame(t, first, second)
	require.False(t, second.started)
	svc.mu.RUnlock()
	require.Greater(t, budget.Used(), used)

	done := make(chan struct{})
	go func() { svc.Stop(); close(done) }()
	require.Eventually(t, func() bool {
		svc.lifecycleMu.Lock()
		defer svc.lifecycleMu.Unlock()
		return svc.lifecycleState == serviceStopping
	}, time.Second, time.Millisecond)
	select {
	case <-done:
		t.Fatal("shutdown returned with a retired worker still reading storage")
	default:
	}
	family.unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not drain speculative ownership")
	}
	require.Zero(t, budget.Used())
	require.Error(t, first.ctx.Err())
	require.Error(t, second.ctx.Err())
}

func TestSpeculativeSuccessorHasMemoryBoundWithoutConfiguredBudget(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	parent := svc.GetClosedLedger()
	prepared, err := svc.prepareSuccessor(t.Context(), parent, successorOptions{
		admitted: true, deferSpeculation: true,
		retries: []openledger.PendingTx{{Blob: bytes.Repeat([]byte{1}, speculativeSuccessorMemoryLimit/2)}},
	})
	if prepared != nil {
		prepared.Release()
	}
	require.ErrorIs(t, err, ErrPreferredTransitionMemory)
	require.Nil(t, prepared)
	require.Same(t, parent, svc.GetClosedLedger())
	require.False(t, svc.preferredTransitionIsActive())
}

package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type validationPersistenceGate struct {
	nodestore.Database
	block       atomic.Bool
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func (g *validationPersistenceGate) Store(ctx context.Context, node *nodestore.Node) error {
	if g.block.Load() {
		g.enteredOnce.Do(func() { close(g.entered) })
		<-g.release
	}
	return g.Database.Store(ctx, node)
}

func (g *validationPersistenceGate) Sync(ctx context.Context) error {
	if g.block.Load() {
		g.enteredOnce.Do(func() { close(g.entered) })
		<-g.release
	}
	return g.Database.Sync(ctx)
}

func (g *validationPersistenceGate) unblock() {
	g.releaseOnce.Do(func() { close(g.release) })
}

func TestValidationProgressesDuringTransactionPersistence(t *testing.T) {
	base := newTestNodeStore(t, 10000)
	gate := &validationPersistenceGate{Database: base, entered: make(chan struct{}), release: make(chan struct{})}
	family := backend.New(gate)
	execution := newTestExecutionStore(t, family)
	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: DefaultConfig().GenesisConfig,
		NodeStore:     gate,
		SHAMapFamily:  family,
		Execution:     execution,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	t.Cleanup(gate.unblock)
	parent := svc.GetClosedLedger()
	blob, _ := startupPaymentBlob(t, "validation-persistence", 1)
	_, err = svc.AcceptConsensusResult(t.Context(), parent, [][]byte{blob}, nil, parent.CloseTime().Add(time.Second), true)
	require.NoError(t, err)
	svc.FlushPersists()
	closed := svc.GetClosedLedger()
	seq, hash := closed.Sequence(), closed.Hash()

	gate.block.Store(true)
	persisted := make(chan error, 1)
	go func() {
		persisted <- svc.persistToNodeStore(t.Context(), closed, seq)
	}()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("transaction persistence did not reach storage")
	}

	validated := make(chan struct{})
	go func() {
		svc.SetValidatedLedger(seq, hash)
		close(validated)
	}()
	select {
	case <-validated:
	case <-time.After(time.Second):
		t.Fatal("validation blocked behind transaction persistence")
	}
	require.Equal(t, hash, svc.GetValidatedLedger().Hash())
	require.True(t, closed.IsValidated())
	require.Equal(t, hash, svc.GetClosedLedger().Hash())
	require.False(t, svc.openLedgerMu.Snapshot().Held)

	admitted := make(chan error, 1)
	go func() {
		lockErr := svc.lockOpenLedgerIfRunning(openLedgerConsensus)
		if lockErr == nil {
			svc.openLedgerMu.Unlock()
		}
		admitted <- lockErr
	}()
	select {
	case err := <-admitted:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("consensus admission blocked behind transaction persistence")
	}
	gate.unblock()
	select {
	case err := <-persisted:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("transaction persistence did not finish")
	}
}

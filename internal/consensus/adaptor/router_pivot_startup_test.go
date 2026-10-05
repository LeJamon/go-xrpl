package adaptor

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

type startupBlockingPivotRecoveryBackend struct {
	started chan struct{}
	once    sync.Once
}

func (b *startupBlockingPivotRecoveryBackend) SelectPivot(ctx context.Context, _ uint32) (service.RecoveryTarget, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return service.RecoveryTarget{}, ctx.Err()
}

func (*startupBlockingPivotRecoveryBackend) Authenticate(context.Context, service.RecoveryTarget) error {
	return nil
}

func (*startupBlockingPivotRecoveryBackend) Acquire(context.Context, service.RecoveryTarget, *ledger.Ledger) (*ledger.Ledger, error) {
	return nil, errors.New("startup test backend does not acquire ledgers")
}

func (*startupBlockingPivotRecoveryBackend) Next(context.Context, header.LedgerHeader) (service.RecoveryTarget, error) {
	return service.RecoveryTarget{}, errors.New("startup test backend does not acquire successors")
}

func (*startupBlockingPivotRecoveryBackend) CaughtUp(context.Context, header.LedgerHeader) bool {
	return false
}

func (*startupBlockingPivotRecoveryBackend) Handoff(context.Context, header.LedgerHeader) error {
	return nil
}

func newStartupRecoveryService(t *testing.T, path string) *service.Service {
	t.Helper()
	svc, err := service.New(service.Config{
		Standalone:      true,
		GenesisConfig:   genesis.DefaultConfig(),
		ReplayFaultPath: path,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	return svc
}

func TestRouterDelaysPersistedPivotRecoveryUntilRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay-fault.json")
	svc := newStartupRecoveryService(t, path)
	parent := svc.GetClosedLedger()
	_, target, _, _ := buildSuccessorAgainstParent(t, parent)
	svc.RecordReplayPreparationFailure(
		context.Background(),
		target.Header(),
		shamap.New(shamap.TypeTransaction),
		parent,
		false,
		errors.New("startup recovery test fault"),
	)
	fault := svc.ReplayFaultStatus().Fault
	require.NotNil(t, fault)

	blocking := &startupBlockingPivotRecoveryBackend{started: make(chan struct{})}
	svc.SetPivotRecoveryBackend(blocking)
	require.NoError(t, svc.StartReplayFaultRecoveryStrategy(fault.ID, string(replayfault.RecoveryStrategyVerifiedPivot)))
	select {
	case <-blocking.started:
	case <-time.After(2 * time.Second):
		t.Fatal("initial recovery did not reach the backend")
	}
	svc.Stop()

	stopped := svc.ReplayFaultStatus()
	require.Equal(t, replayfault.RecoveryStrategyVerifiedPivot, stopped.Recovery.Strategy)
	require.False(t, stopped.Recovery.InFlight)
	require.NotEqual(t, replayfault.RecoveryPhaseNeedsOperator, stopped.Recovery.Phase)

	restarted := newStartupRecoveryService(t, path)
	identity2, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	t.Cleanup(func() { _ = identity2.Close() })
	a2 := New(Config{
		LedgerService:       restarted,
		Identity:            identity2,
		Validators:          []consensus.NodeID{identity2.NodeID},
		ValidatorMasterKeys: [][33]byte{identity2.MasterKey},
	})
	r := newTestRouter(nil, a2, nil)
	require.NotNil(t, r.pivotRecoveryBackend)
	require.False(t, r.pivotRecoveryReady.Load(), "constructor must not resume persisted recovery")
	beforeRun := restarted.ReplayFaultStatus()
	require.False(t, beforeRun.Recovery.InFlight)
	require.NotEqual(t, replayfault.RecoveryPhaseNeedsOperator, beforeRun.Recovery.Phase)

	// Model the real startup order: manifest and trust setup complete before
	// Components.Start launches Router.Run.
	r.SetManifestCache(nil, nil)
	a2.SetTrustedValidators([]consensus.NodeID{identity2.NodeID}, [][33]byte{identity2.MasterKey})
	runCtx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		r.Run(runCtx)
		close(runDone)
	}()
	t.Cleanup(func() {
		cancel()
		<-runDone
	})
	require.Eventually(t, func() bool { return r.pivotRecoveryReady.Load() }, 2*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		return restarted.ReplayFaultStatus().Recovery.InFlight
	}, 2*time.Second, 10*time.Millisecond)
	resumed := restarted.ReplayFaultStatus()
	require.Equal(t, replayfault.RecoveryStrategyVerifiedPivot, resumed.Recovery.Strategy)
	require.NotEqual(t, replayfault.RecoveryPhaseNeedsOperator, resumed.Recovery.Phase)
	require.Empty(t, resumed.Recovery.LastError)

	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("router did not stop after context cancellation")
	}
	restarted.Stop()
}

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

type pivotScenarioBackend struct {
	mu       sync.Mutex
	ledgers  map[uint32]*ledger.Ledger
	pivot    uint32
	live     uint32
	acquired []uint32
	selected int
	handoff  func(header.LedgerHeader) error
}

func (b *pivotScenarioBackend) target(seq uint32) (RecoveryTarget, error) {
	l := b.ledgers[seq]
	if l == nil {
		return RecoveryTarget{}, fmt.Errorf("ledger %d unavailable", seq)
	}
	return RecoveryTarget{Header: l.Header(), Evidence: json.RawMessage(`{"fixture":true}`)}, nil
}
func (b *pivotScenarioBackend) SelectPivot(context.Context, uint32) (RecoveryTarget, error) {
	b.mu.Lock()
	b.selected++
	b.mu.Unlock()
	return b.target(b.pivot)
}
func (b *pivotScenarioBackend) Authenticate(_ context.Context, target RecoveryTarget) error {
	l := b.ledgers[target.Header.LedgerIndex]
	if l == nil || l.Hash() != target.Header.Hash {
		return fmt.Errorf("unauthenticated fixture target")
	}
	return nil
}
func (b *pivotScenarioBackend) Acquire(_ context.Context, target RecoveryTarget, parent *ledger.Ledger) (*ledger.Ledger, error) {
	if err := b.Authenticate(context.Background(), target); err != nil {
		return nil, err
	}
	if parent != nil && (target.Header.ParentHash != parent.Hash() || target.Header.LedgerIndex != parent.Sequence()+1) {
		return nil, fmt.Errorf("acquisition skipped a parent")
	}
	b.mu.Lock()
	b.acquired = append(b.acquired, target.Header.LedgerIndex)
	b.mu.Unlock()
	acquired := b.ledgers[target.Header.LedgerIndex]
	if parent == nil {
		return acquired, nil
	}
	state, err := parent.StateMapSnapshot()
	if err != nil {
		return nil, err
	}
	txs, err := acquired.TxMapSnapshot()
	if err != nil {
		return nil, err
	}
	return ledger.NewFromHeader(acquired.Header(), state, txs, parent.Fees())
}
func (b *pivotScenarioBackend) Next(_ context.Context, parent header.LedgerHeader) (RecoveryTarget, error) {
	return b.target(parent.LedgerIndex + 1)
}
func (b *pivotScenarioBackend) CaughtUp(_ context.Context, h header.LedgerHeader) bool {
	return h.LedgerIndex >= b.live
}
func (b *pivotScenarioBackend) Handoff(_ context.Context, h header.LedgerHeader) error {
	return b.handoff(h)
}

func TestPivotRecoveryExecutesEverySuccessorBeforeActivation(t *testing.T) {
	for _, disagreementAt := range []int{0, 1, 3} {
		t.Run(fmt.Sprintf("disagreement_%d", disagreementAt), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fault.json")
			svc := replayFaultService(t, path)
			old := svc.GetClosedLedger()
			historical := replayFaultTarget(t, old, false)
			evidence, err := json.Marshal(replayEvidence{Parent: old.Header(), Target: historical.Header(), NetworkID: svc.config.NetworkID})
			require.NoError(t, err)
			require.NoError(t, recordTestReplayFault(svc.replayFaults, replayfault.Fault{
				Class: replayfault.ExecutionDisagreement, ParentHash: old.Hash(), TargetHash: historical.Hash(), Sequence: historical.Sequence(), Evidence: evidence, Message: "historical unauthenticated failure",
			}))
			original := svc.replayFaults.Snapshot()
			pivot := replayFaultTarget(t, historical, false)
			b := &pivotScenarioBackend{ledgers: map[uint32]*ledger.Ledger{pivot.Sequence(): pivot}, pivot: pivot.Sequence(), live: pivot.Sequence() + 3}
			parent := pivot
			for i := 1; i <= 3; i++ {
				if disagreementAt == i {
					closeTime := parent.CloseTime().Add(10 * time.Second)
					candidate, createErr := ledger.NewOpen(parent, closeTime)
					require.NoError(t, createErr)
					require.NoError(t, candidate.Insert(keylet.Keylet{Key: [32]byte{0xE1}}, []byte("unexecuted state mutation")))
					require.NoError(t, candidate.Close(closeTime, 0))
					parent = candidate
				} else {
					parent = replayFaultTarget(t, parent, false)
				}
				b.ledgers[parent.Sequence()] = parent
			}
			handedOff := make(chan header.LedgerHeader, 1)
			b.handoff = func(h header.LedgerHeader) error {
				if !svc.ReplayBlocked() {
					return fmt.Errorf("signing gate opened before handoff")
				}
				if err := svc.WithValidatorDuty(func() error { return fmt.Errorf("validator callback executed") }); err != replayfault.ErrBlocked {
					return fmt.Errorf("validator duties were not blocked: %v", err)
				}
				handedOff <- h
				return nil
			}
			svc.SetPivotRecoveryBackend(b)
			require.NoError(t, svc.StartReplayFaultRecoveryStrategy(original.ID, "verified_pivot"))
			require.Eventually(t, func() bool { return !svc.ReplayFaultStatus().Recovery.InFlight }, 10*time.Second, time.Millisecond)
			if disagreementAt != 0 {
				require.True(t, svc.ReplayBlocked())
				require.Same(t, old, svc.GetClosedLedger())
				require.NotEmpty(t, svc.ReplayFaultStatus().Recovery.LastError)
				require.Empty(t, handedOff)
				require.JSONEq(t, string(evidence), string(svc.replayFaults.Snapshot().Evidence))
				failed := svc.ReplayFaultStatus().Recovery
				require.Equal(t, replayfault.RecoveryPhaseExecutionFailed, failed.Phase)
				require.NotEmpty(t, failed.FailureFingerprint)
				svc.Stop()
				restarted := replayFaultService(t, path)
				restarted.SetPivotRecoveryBackend(b)
				require.Never(t, func() bool { return restarted.ReplayFaultStatus().Recovery.InFlight || !restarted.ReplayBlocked() }, 100*time.Millisecond, time.Millisecond)
				require.Equal(t, failed.FailureFingerprint, restarted.ReplayFaultStatus().Recovery.FailureFingerprint)
				require.Equal(t, failed.FailureAttempts, restarted.ReplayFaultStatus().Recovery.FailureAttempts)
				return
			}
			require.False(t, svc.ReplayBlocked(), "%+v", svc.ReplayFaultStatus())
			archive, err := os.ReadFile(path + "." + original.ID + ".superseded")
			require.NoError(t, err)
			var archived replayfault.Fault
			require.NoError(t, json.Unmarshal(archive, &archived))
			require.JSONEq(t, string(original.Evidence), string(archived.Evidence))
			require.Equal(t, original.Class, archived.Class)
			require.NotNil(t, archived.Recovery)
			require.Equal(t, replayfault.RecoveryOutcomeSupersededByPivot, archived.Recovery.Outcome)
			require.Equal(t, pivot.Sequence()+1, archived.Recovery.SuccessorSequence)
			require.Equal(t, parent.Sequence(), archived.Recovery.FrontierSequence)
			require.Equal(t, parent.Hash(), svc.GetClosedLedger().Hash())
			require.Equal(t, parent.Hash(), svc.GetValidatedLedger().Hash())
			require.Len(t, handedOff, 1)
			b.mu.Lock()
			acquired := append([]uint32(nil), b.acquired...)
			b.mu.Unlock()
			require.Equal(t, []uint32{pivot.Sequence(), pivot.Sequence() + 1, pivot.Sequence() + 2, pivot.Sequence() + 3}, acquired)
		})
	}
}

type interruptedPivotBackend struct {
	*pivotScenarioBackend
	interruptAt uint32
	interrupted chan struct{}
}

func (b *interruptedPivotBackend) Acquire(ctx context.Context, target RecoveryTarget, parent *ledger.Ledger) (*ledger.Ledger, error) {
	if target.Header.LedgerIndex == b.interruptAt {
		close(b.interrupted)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return b.pivotScenarioBackend.Acquire(ctx, target, parent)
}

func TestPivotRecoveryRestartUsesVerifiedFrontierAndReplaysPendingTarget(t *testing.T) {
	db := newTestNodeStore(t, 64)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	cfg := DefaultConfig()
	cfg.Standalone = true
	cfg.NodeStore = db
	cfg.SHAMapFamily = backend.New(db)
	cfg.ReplayFaultPath = filepath.Join(t.TempDir(), "fault.json")
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	old := svc.GetClosedLedger()
	historical := replayFaultTarget(t, old, false)
	evidence, err := json.Marshal(replayEvidence{Parent: old.Header(), Target: historical.Header(), NetworkID: cfg.NetworkID})
	require.NoError(t, err)
	require.NoError(t, recordTestReplayFault(svc.replayFaults, replayfault.Fault{Class: replayfault.ExecutionDisagreement, ParentHash: old.Hash(), TargetHash: historical.Hash(), Sequence: historical.Sequence(), Evidence: evidence}))
	fault := svc.replayFaults.Snapshot()
	pivot := replayFaultTarget(t, historical, false)
	b := &pivotScenarioBackend{ledgers: map[uint32]*ledger.Ledger{pivot.Sequence(): pivot}, pivot: pivot.Sequence(), live: pivot.Sequence() + 3, handoff: func(header.LedgerHeader) error { return nil }}
	parent := pivot
	for i := 0; i < 3; i++ {
		parent = replayFaultTarget(t, parent, false)
		b.ledgers[parent.Sequence()] = parent
	}
	interrupted := &interruptedPivotBackend{pivotScenarioBackend: b, interruptAt: pivot.Sequence() + 2, interrupted: make(chan struct{})}
	svc.SetPivotRecoveryBackend(interrupted)
	require.NoError(t, svc.StartReplayFaultRecoveryStrategy(fault.ID, "verified_pivot"))
	select {
	case <-interrupted.interrupted:
	case <-time.After(10 * time.Second):
		t.Fatal("recovery did not reach pending successor")
	}
	svc.Stop()
	require.True(t, svc.ReplayBlocked())
	status := svc.ReplayFaultStatus()
	require.Equal(t, pivot.Sequence()+1, status.Recovery.FrontierSequence)
	require.Zero(t, status.Recovery.FailureAttempts, "cancellation is not execution disagreement")
	require.NotEmpty(t, svc.replayFaults.Snapshot().RecoveryFrontier)
	restarted, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, restarted.Start())
	t.Cleanup(restarted.Stop)
	require.True(t, restarted.ReplayBlocked())
	b.mu.Lock()
	b.acquired = nil
	b.mu.Unlock()
	restarted.SetPivotRecoveryBackend(b)
	require.Eventually(t, func() bool { return !restarted.ReplayBlocked() }, 10*time.Second, time.Millisecond, "status: %+v", restarted.ReplayFaultStatus())
	require.Equal(t, parent.Hash(), restarted.GetValidatedLedger().Hash())
	b.mu.Lock()
	acquired := append([]uint32(nil), b.acquired...)
	b.mu.Unlock()
	require.Equal(t, []uint32{pivot.Sequence() + 2, pivot.Sequence() + 3}, acquired)
}

func TestPivotAdmissionPrefersUsableExactTransition(t *testing.T) {
	for _, divergent := range []bool{false, true} {
		t.Run(fmt.Sprint(divergent), func(t *testing.T) {
			svc := replayFaultService(t, filepath.Join(t.TempDir(), "fault.json"))
			parent := svc.GetClosedLedger()
			target := replayFaultTarget(t, parent, divergent)
			evidence, err := json.Marshal(replayEvidence{Parent: parent.Header(), Target: target.Header(), Authenticated: true, NetworkID: svc.config.NetworkID})
			require.NoError(t, err)
			require.NoError(t, recordTestReplayFault(svc.replayFaults, replayfault.Fault{Class: replayfault.ExecutionDisagreement, ParentHash: parent.Hash(), TargetHash: target.Hash(), Sequence: target.Sequence(), Evidence: evidence}))
			fault := svc.replayFaults.Snapshot()
			backend := &pivotScenarioBackend{}
			svc.SetPivotRecoveryBackend(backend)
			require.NoError(t, svc.StartReplayFaultRecoveryStrategy(fault.ID, "verified_pivot"))
			require.Eventually(t, func() bool { return !svc.ReplayFaultStatus().Recovery.InFlight }, 5*time.Second, time.Millisecond)
			backend.mu.Lock()
			selected := backend.selected
			backend.mu.Unlock()
			require.Zero(t, selected)
			require.Equal(t, divergent, svc.ReplayBlocked())
			if divergent {
				require.Contains(t, svc.ReplayFaultStatus().Recovery.LastError, "failed transition still disagrees")
			} else {
				require.Equal(t, replayfault.RecoveryOutcomeResolvedByReplay, svc.ReplayFaultStatus().Recovery.Outcome)
			}
		})
	}
}

type unavailablePivotBackend struct{ *pivotScenarioBackend }

func (b *unavailablePivotBackend) Acquire(context.Context, RecoveryTarget, *ledger.Ledger) (*ledger.Ledger, error) {
	return nil, ErrRecoveryUnavailable
}

func TestPivotRecoveryPinsProgressBetweenAvailabilityRetries(t *testing.T) {
	db := newTestNodeStore(t, 64)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	cfg := DefaultConfig()
	cfg.Standalone = true
	cfg.NodeStore = db
	cfg.SHAMapFamily = backend.New(db)
	cfg.ReplayFaultPath = filepath.Join(t.TempDir(), "fault.json")
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	parent := svc.GetClosedLedger()
	historical := replayFaultTarget(t, parent, false)
	evidence, err := json.Marshal(replayEvidence{Parent: parent.Header(), Target: historical.Header(), NetworkID: cfg.NetworkID})
	require.NoError(t, err)
	require.NoError(t, recordTestReplayFault(svc.replayFaults, replayfault.Fault{ParentHash: parent.Hash(), TargetHash: historical.Hash(), Sequence: historical.Sequence(), Evidence: evidence}))
	fault := svc.replayFaults.Snapshot()
	pivot := replayFaultTarget(t, historical, false)
	fixture := &pivotScenarioBackend{ledgers: map[uint32]*ledger.Ledger{pivot.Sequence(): pivot}, pivot: pivot.Sequence()}
	svc.SetPivotRecoveryBackend(&unavailablePivotBackend{fixture})
	require.NoError(t, svc.StartReplayFaultRecoveryStrategy(fault.ID, "verified_pivot"))
	require.Eventually(t, func() bool {
		status := svc.ReplayFaultStatus()
		return !status.Recovery.InFlight && status.Recovery.AcquisitionError != ""
	}, time.Second, time.Millisecond)
	require.NotEmpty(t, svc.replayFaults.Snapshot().RecoveryPivot)
	require.True(t, svc.ReplayBlocked())
	deleted := make(chan struct{})
	go func() {
		_, _ = db.DeleteBefore(context.Background(), 1, 1)
		close(deleted)
	}()
	select {
	case <-deleted:
		t.Fatal("online deletion crossed the availability retry gap")
	case <-time.After(50 * time.Millisecond):
	}
	svc.Stop()
	select {
	case <-deleted:
	case <-time.After(time.Second):
		t.Fatal("online deletion remained blocked after shutdown")
	}
}

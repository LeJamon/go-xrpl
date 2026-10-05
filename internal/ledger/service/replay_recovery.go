package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
)

var errReplayRecoveryWorkerRunning = errors.New("replay evidence capture or recovery already running")

// admitReplayRecoveryJob reserves the single replay-recovery worker and the
// service lifecycle wait group before starting it. The worker context belongs
// to the service, so an RPC request ending after admission cannot cancel it.
func (s *Service) admitReplayRecoveryJob(run func(context.Context) error) (<-chan error, error) {
	if run == nil {
		return nil, errors.New("replay recovery worker is required")
	}

	s.lifecycleMu.Lock()
	if s.lifecycleState != serviceRunning {
		s.lifecycleMu.Unlock()
		return nil, errServiceNotRunning
	}
	if !s.replayRecoveryMu.TryLock() {
		s.lifecycleMu.Unlock()
		return nil, errReplayRecoveryWorkerRunning
	}
	workerContext, cancel := context.WithCancel(context.Background())
	s.replayRecoveryCancel = cancel
	s.consensusWG.Add(1)
	s.lifecycleMu.Unlock()

	done := make(chan error, 1)
	go func() {
		defer s.consensusWG.Done()
		defer s.replayRecoveryMu.Unlock()
		defer cancel()
		defer func() {
			s.lifecycleMu.Lock()
			s.replayRecoveryCancel = nil
			s.lifecycleMu.Unlock()
		}()

		var err error
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					err = fmt.Errorf("replay recovery panic: %v", recovered)
				}
			}()
			err = run(workerContext)
		}()
		done <- err
	}()
	return done, nil
}

// StartReplayFaultRecovery admits an explicit recovery attempt for exactly
// id and returns once the store has recorded the attempt as in-flight. The
// verification itself continues under the service lifecycle and reports its
// result through ReplayFaultStatus.
func (s *Service) StartReplayFaultRecovery(id string) error {
	return s.StartReplayFaultRecoveryStrategy(id, string(replayfault.RecoveryStrategyExactTransition))
}

// StartReplayFaultRecoveryStrategy admits an explicit recovery attempt. The
// legacy exact-transition strategy keeps its original verifier; verified-pivot
// is a separate staged worker whose durable journal remains blocked through
// canonical handoff.
func (s *Service) StartReplayFaultRecoveryStrategy(id, strategy string) error {
	if s.replayFaults == nil {
		return replayfault.ErrNoFault
	}
	fault := s.replayFaults.Snapshot()
	if fault == nil {
		return replayfault.ErrNoFault
	}
	if id == "" || fault.ID != id {
		return fmt.Errorf("%w: %q", replayfault.ErrFaultIDMismatch, id)
	}
	recoveryStrategy := replayfault.RecoveryStrategy(strategy)
	if recoveryStrategy != replayfault.RecoveryStrategyExactTransition && recoveryStrategy != replayfault.RecoveryStrategyVerifiedPivot {
		return fmt.Errorf("%w: %q", replayfault.ErrRecoveryStrategy, strategy)
	}
	started := make(chan struct{})
	run := func(ctx context.Context) error {
		current := s.replayFaults.Snapshot()
		if current == nil || current.ID != id {
			return replayfault.ErrFaultIDMismatch
		}
		resumePivot := current.Recovery != nil && current.Recovery.Strategy == replayfault.RecoveryStrategyVerifiedPivot &&
			current.Recovery.Phase != replayfault.RecoveryPhaseComplete && current.Recovery.Phase != replayfault.RecoveryPhaseExecutionFailed &&
			current.Recovery.Phase != replayfault.RecoveryPhaseNeedsOperator && current.Recovery.LastError == ""
		if recoveryStrategy == replayfault.RecoveryStrategyVerifiedPivot && (resumePivot || !s.exactReplayTransitionUsable(ctx, current)) {
			if s.pivotRecoveryBackend() == nil {
				return errors.New("verified-pivot replay recovery backend unavailable")
			}
			release, err := s.AcquireStateAdmission(ctx)
			if err != nil {
				return err
			}
			defer release()
			notify := started
			for {
				err := s.recoverVerifiedPivot(ctx, id, notify)
				if !errors.Is(err, ErrRecoveryUnavailable) {
					return err
				}
				notify = nil
				timer := time.NewTimer(time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
		return s.revalidateReplayFault(ctx, id, func() { close(started) })
	}
	done, err := s.admitReplayRecoveryJob(run)
	if err != nil {
		return err
	}

	select {
	case <-started:
		return nil
	case err := <-done:
		select {
		case <-started:
			return nil
		default:
			return err
		}
	}
}

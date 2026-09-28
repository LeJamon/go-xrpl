package service

import (
	"context"
	"errors"
	"fmt"

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

	started := make(chan struct{})
	done, err := s.admitReplayRecoveryJob(func(ctx context.Context) error {
		return s.revalidateReplayFault(ctx, id, func() {
			close(started)
		})
	})
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

package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/internal/observability"
	"github.com/LeJamon/go-xrpl/shamap"
)

const memoryAdmissionRetryTimeout = 100 * time.Millisecond

type speculativeMemoryError struct {
	requested, used, limit, capacity uint64
	account                          string
	oversized                        bool
	wake                             <-chan struct{}
}

func (e *speculativeMemoryError) Error() string {
	return fmt.Sprintf("%v: account=%s requested_bytes=%d used_bytes=%d limit_bytes=%d capacity_bytes=%d oversized=%t",
		ErrPreferredTransitionMemory, e.account, e.requested, e.used, e.limit, e.capacity, e.oversized)
}

func (e *speculativeMemoryError) Unwrap() error { return ErrPreferredTransitionMemory }

func newSpeculativeMemoryError(budget MemoryBudget, bytes, capacity uint64, oversized bool, wake <-chan struct{}) *speculativeMemoryError {
	err := &speculativeMemoryError{requested: bytes, capacity: capacity, limit: capacity, account: "speculative", oversized: oversized, wake: wake}
	if provider, ok := budget.(interface {
		Snapshot() membudget.AccountSnapshot
	}); ok {
		snapshot := provider.Snapshot()
		err.account, err.used, err.limit = snapshot.Name, snapshot.Used, snapshot.Maximum
	} else if provider, ok := budget.(interface{ Limit() uint64 }); ok {
		err.limit = provider.Limit()
	}
	return err
}

func reserveSpeculativeBytes(budget MemoryBudget, bytes, capacity uint64) error {
	if bytes > capacity {
		return newSpeculativeMemoryError(budget, bytes, capacity, true, nil)
	}
	if budget == nil || bytes == 0 {
		return nil
	}
	var wake <-chan struct{}
	if provider, ok := budget.(interface{ Changed() <-chan struct{} }); ok {
		wake = provider.Changed()
	}
	if !budget.TryReserve(bytes) {
		return newSpeculativeMemoryError(budget, bytes, capacity, false, wake)
	}
	return nil
}

func (s *Service) speculativeMemoryBudget() shamap.DirtyMemoryBudget {
	if s.config.SpeculativeMemory != nil {
		return s.config.SpeculativeMemory
	}
	return s.config.PersistenceMemory
}

type memoryAdmissionRetry struct {
	origin   string
	deadline time.Time
	retries  int
	last     *speculativeMemoryError
}

func (r *memoryAdmissionRetry) wait(ctx context.Context, s *Service, err error) error {
	if !errors.As(err, &r.last) || r.last.oversized {
		return err
	}
	if r.deadline.IsZero() {
		r.deadline = time.Now().Add(memoryAdmissionRetryTimeout)
	}
	remaining := time.Until(r.deadline)
	if remaining <= 0 {
		return err
	}
	s.lifecycleMu.Lock()
	if s.lifecycleState != serviceRunning {
		s.lifecycleMu.Unlock()
		return errServiceNotRunning
	}
	if s.memoryAdmissionStop == nil {
		s.memoryAdmissionStop = make(chan struct{})
	}
	stop := s.memoryAdmissionStop
	s.lifecycleMu.Unlock()
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-stop:
		return errServiceNotRunning
	case <-timer.C:
		return err
	case <-r.last.wake:
		r.retries++
		return nil
	}
}

func (r *memoryAdmissionRetry) finish(s *Service, err error) {
	if r.origin == "" {
		r.origin = "ingress"
	}
	outcome := "admitted"
	switch {
	case err == nil && r.last != nil:
		outcome = "retried"
	case err == nil:
	case r.last == nil:
		return
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, errServiceNotRunning):
		outcome = "canceled"
	case !errors.Is(err, ErrPreferredTransitionMemory):
		outcome = "aborted"
	case r.last.oversized:
		outcome = "oversized"
	default:
		outcome = "exhausted"
	}
	observability.RecordMemoryAdmission(r.origin, outcome)
	if r.last != nil {
		s.logger.Debug("Speculative memory admission",
			"origin", r.origin, "account", r.last.account,
			"requested_bytes", r.last.requested, "used_bytes", r.last.used,
			"limit_bytes", r.last.limit, "capacity_bytes", r.last.capacity,
			"retries", r.retries, "outcome", outcome, "err", err)
	}
}

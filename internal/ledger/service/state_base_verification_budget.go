package service

import (
	"context"
	"runtime"
	"time"
)

const (
	backgroundVerificationMaxWorkers = 2
	backgroundVerificationMaxPause   = 100 * time.Millisecond
	backgroundVerificationGateYield  = 250 * time.Millisecond
	backgroundVerificationPoll       = 10 * time.Millisecond
)

func backgroundVerificationWorkers(cpus int) int {
	return max(1, min(backgroundVerificationMaxWorkers, cpus-1))
}

func (s *Service) backgroundStateVerificationPolicy() storedSHAMapVerificationPolicy {
	return storedSHAMapVerificationPolicy{
		workers: backgroundVerificationWorkers(runtime.GOMAXPROCS(0)),
		pause: func(ctx context.Context, work time.Duration) error {
			return paceBackgroundVerification(ctx, work, s.openLedgerMu.Snapshot)
		},
	}
}

// Pace each bounded read batch, then give contending foreground work a chance
// to drain. Never hold the ledger gate or restart the traversal while yielding.
// A bounded yield guarantees background progress even on a continuously busy
// node; the checkpoint cannot be starved indefinitely by transaction ingress.
func paceBackgroundVerification(ctx context.Context, work time.Duration, snapshot func() openLedgerGateSnapshot) error {
	pause := min(work, backgroundVerificationMaxPause)
	if pause >= time.Millisecond {
		if err := waitBackgroundVerification(ctx, pause); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(backgroundVerificationGateYield)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		gate := snapshot()
		busy := gate.QueuedPriority > 0 || gate.QueuedIngress > 0 ||
			(gate.Held && (gate.Owner != openLedgerIngress || gate.HeldFor >= openLedgerGateSlowWait))
		if !busy || !time.Now().Before(deadline) {
			return nil
		}
		if err := waitBackgroundVerification(ctx, backgroundVerificationPoll); err != nil {
			return err
		}
	}
}

func waitBackgroundVerification(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

package service

import (
	"context"
	"time"

	appconfig "github.com/LeJamon/go-xrpl/config"
)

const defaultNodeStoreSweepInterval = 60 * time.Second

func nodeStoreSweepIntervalForSize(nodeSize string) time.Duration {
	return appconfig.SweepIntervalForNodeSize(nodeSize)
}

type nodeStoreSweeper interface {
	Sweep() error
}

func (s *Service) startNodeStoreSweeper() {
	sweeper, ok := s.shamapFamily.(nodeStoreSweeper)
	if !ok {
		return
	}

	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()
	if s.sweepCancel != nil {
		return
	}
	interval := s.sweepInterval
	if interval <= 0 {
		interval = defaultNodeStoreSweepInterval
	}
	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // cancel is retained and called by Stop
	done := make(chan struct{})
	s.sweepCancel = cancel
	s.sweepDone = done
	go s.runNodeStoreSweeper(ctx, done, interval, sweeper)
}

func (s *Service) runNodeStoreSweeper(
	ctx context.Context,
	done chan<- struct{},
	interval time.Duration,
	sweeper nodeStoreSweeper,
) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := sweeper.Sweep(); err != nil {
				s.logger.Warn("NodeStore cache sweep failed", "err", err)
			}
		}
	}
}

func (s *Service) stopNodeStoreSweeper() {
	s.sweepMu.Lock()
	cancel := s.sweepCancel
	done := s.sweepDone
	s.sweepMu.Unlock()
	if cancel == nil {
		return
	}

	cancel()
	<-done

	s.sweepMu.Lock()
	if s.sweepDone == done {
		s.sweepCancel = nil
		s.sweepDone = nil
	}
	s.sweepMu.Unlock()
}

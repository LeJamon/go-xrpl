package peermanagement

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestServeSchedulerPerPeerFairness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newServeScheduler(ctx, 2, 32, 16, 1)
	runDone := make(chan struct{})
	go func() {
		_ = s.Run(ctx)
		close(runDone)
	}()

	release := make(chan struct{})
	started := make(chan PeerID, 2)
	job := func(peerID PeerID) func(context.Context) {
		return func(ctx context.Context) {
			started <- peerID
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}
	for range 2 {
		if !s.Submit(ctx, 1, job(1)) {
			t.Fatal("peer 1 submission rejected")
		}
	}
	if !s.Submit(ctx, 2, job(2)) {
		t.Fatal("peer 2 submission rejected")
	}
	startedPeers := [2]PeerID{<-started, <-started}
	close(release)
	cancel()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not shut down")
	}
	if !((startedPeers[0] == 1 && startedPeers[1] == 2) ||
		(startedPeers[0] == 2 && startedPeers[1] == 1)) {
		t.Fatalf("started peers = %v, want one job from each peer", startedPeers)
	}
}

func TestServeSchedulerBoundsAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newServeScheduler(ctx, 1, 1, 1, 1)
	started := make(chan struct{})
	block := make(chan struct{})
	if !s.Submit(ctx, 1, func(context.Context) {
		close(started)
		<-block
	}) {
		t.Fatal("first submission rejected")
	}
	if s.Submit(ctx, 1, func(context.Context) {}) {
		t.Fatal("per-peer queue accepted over-limit submission")
	}
	if got := s.Dropped(); got != 1 {
		t.Fatalf("dropped = %d, want 1", got)
	}
	go func() { _ = s.Run(ctx) }()
	<-started
	s.CancelPeer(1)
	close(block)
	cancel()
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not shut down after cancellation")
	}
}

func TestServeSchedulerCancelPeerTracksCompletedTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newServeScheduler(ctx, 2, 8, 8, 2)
	go func() { _ = s.Run(ctx) }()

	startedFirst := make(chan struct{})
	startedSecond := make(chan struct{})
	firstCanceled := make(chan struct{})
	releaseSecond := make(chan struct{})
	if !s.Submit(ctx, 1, func(ctx context.Context) {
		close(startedFirst)
		<-ctx.Done()
		close(firstCanceled)
	}) {
		t.Fatal("first submission rejected")
	}
	if !s.Submit(ctx, 1, func(context.Context) {
		close(startedSecond)
		<-releaseSecond
	}) {
		t.Fatal("second submission rejected")
	}
	select {
	case <-startedFirst:
	case <-time.After(time.Second):
		t.Fatal("first task did not start")
	}
	select {
	case <-startedSecond:
	case <-time.After(time.Second):
		t.Fatal("second task did not start")
	}
	close(releaseSecond)

	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		running := len(s.running[1])
		s.mu.Unlock()
		if running == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second task did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	s.CancelPeer(1)
	select {
	case <-firstCanceled:
	case <-time.After(time.Second):
		t.Fatal("canceling peer did not cancel the remaining task")
	}
	cancel()
}

func TestServeSchedulerCancelPeerRemovesRoundEntry(t *testing.T) {
	ctx := context.Background()
	s := newServeScheduler(ctx, 1, 256, 1, 1)
	for peerID := PeerID(1); peerID <= 128; peerID++ {
		if !s.Submit(ctx, peerID, func(context.Context) {}) {
			t.Fatalf("submission for peer %d rejected", peerID)
		}
		s.CancelPeer(peerID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.round) != 0 {
		t.Fatalf("round retains %d canceled peers", len(s.round))
	}
	if s.pending != 0 {
		t.Fatalf("pending = %d after canceling all peers", s.pending)
	}
}

func TestServeSchedulerWakesEveryIdleWorkerForBurst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const workers = 4
	s := newServeScheduler(ctx, workers, workers, 1, 1)
	done := make(chan struct{})
	go func() {
		_ = s.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for s.idle.Load() != workers {
		if time.Now().After(deadline) {
			t.Fatalf("idle workers = %d, want %d before burst", s.idle.Load(), workers)
		}
		time.Sleep(time.Millisecond)
	}

	started := make(chan struct{}, workers)
	release := make(chan struct{})
	for peerID := PeerID(1); peerID <= workers; peerID++ {
		if !s.Submit(ctx, peerID, func(context.Context) {
			started <- struct{}{}
			<-release
		}) {
			t.Fatalf("submission for peer %d rejected", peerID)
		}
	}

	deadline = time.Now().Add(time.Second)
	for len(started) != workers {
		if time.Now().After(deadline) {
			t.Fatalf("started workers = %d, want %d after burst", len(started), workers)
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after burst")
	}
}

func TestServeSchedulerDiscardsOwnedQueuedTaskOnPeerCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newServeScheduler(ctx, 1, 1, 1, 1)
	var discarded atomic.Int32
	requireSubmitted := s.SubmitOwned(ctx, 1, func(context.Context) {
		t.Fatal("canceled queued task ran")
	}, func() {
		discarded.Add(1)
	})
	if !requireSubmitted {
		t.Fatal("owned task submission rejected")
	}

	s.CancelPeer(1)
	s.close()
	if got := discarded.Load(); got != 1 {
		t.Fatalf("discard count = %d, want 1", got)
	}
}

func TestServeSchedulerRejectedOwnedTaskRemainsCallerOwned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newServeScheduler(ctx, 1, 1, 1, 1)
	if !s.Submit(ctx, 1, func(context.Context) {}) {
		t.Fatal("first submission rejected")
	}
	var discarded atomic.Int32
	if s.SubmitOwned(ctx, 2, func(context.Context) {}, func() { discarded.Add(1) }) {
		t.Fatal("over-limit owned task accepted")
	}
	if got := discarded.Load(); got != 0 {
		t.Fatalf("scheduler discarded caller-owned rejected task %d times", got)
	}
}

func TestServeSchedulerDiscardsOwnedQueuedTaskOnClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newServeScheduler(ctx, 1, 1, 1, 1)
	var discarded atomic.Int32
	if !s.SubmitOwned(ctx, 1, func(context.Context) {
		t.Fatal("closed queued task ran")
	}, func() {
		discarded.Add(1)
	}) {
		t.Fatal("owned task submission rejected")
	}

	s.close()
	s.close()
	if got := discarded.Load(); got != 1 {
		t.Fatalf("discard count = %d, want 1", got)
	}
}

func TestServeSchedulerActiveCancellationUsesRunCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newServeScheduler(ctx, 1, 1, 1, 1)
	go func() { _ = s.Run(ctx) }()
	started := make(chan struct{})
	cleaned := make(chan struct{})
	var discarded atomic.Int32
	if !s.SubmitOwned(ctx, 1, func(ctx context.Context) {
		defer close(cleaned)
		close(started)
		<-ctx.Done()
	}, func() {
		discarded.Add(1)
	}) {
		t.Fatal("owned task submission rejected")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("owned task did not start")
	}

	s.CancelPeer(1)
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("active task did not observe peer cancellation")
	}
	if got := discarded.Load(); got != 0 {
		t.Fatalf("active task discard count = %d, want 0", got)
	}
}

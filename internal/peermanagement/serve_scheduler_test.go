package peermanagement

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
)

func TestServeSchedulerPerPeerFairness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newServeScheduler(ctx, 2, 32, 16, 1)
	go func() {
		_ = s.Run(ctx)
	}()

	var mu sync.Mutex
	order := make([]PeerID, 0, 4)
	var release atomic.Int32
	started := make(chan struct{}, 4)
	job := func(peerID PeerID) func(context.Context) {
		return func(context.Context) {
			mu.Lock()
			order = append(order, peerID)
			mu.Unlock()
			started <- struct{}{}
			for release.Load() == 0 {
				time.Sleep(time.Millisecond)
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
	for range 2 {
		<-started
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("start order = %v, want one job from each peer", order)
	}
	release.Store(1)
	mu.Unlock()
	time.Sleep(10 * time.Millisecond)
	mu.Lock()
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

func TestServeSchedulerRecoversTaskPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newServeScheduler(ctx, 1, 3, 2, 1)
	peer := NewPeer(1, Endpoint{Host: "192.0.2.1", Port: 51235}, false, nil, nil)
	peer.setState(PeerStateConnected)
	overlay := &Overlay{
		peers:          map[PeerID]*Peer{peer.ID(): peer},
		serveScheduler: s,
		ctx:            ctx,
		lifecycleState: overlayLifecycleRunning,
	}
	panicked := make(chan PeerID, 1)
	s.onTaskPanic = func(peerID PeerID) {
		panicked <- peerID
		overlay.closePeerAfterServePanic(peerID)
	}

	discarded := make(chan struct{})
	released := make(chan struct{})
	queuedRan := make(chan struct{}, 1)
	progressed := make(chan struct{})
	if !s.Submit(ctx, peer.ID(), func(context.Context) {
		defer close(released)
		panic("injected serve panic")
	}) {
		t.Fatal("panicking task submission rejected")
	}
	if !s.SubmitOwned(ctx, peer.ID(), func(context.Context) {
		queuedRan <- struct{}{}
	}, func() {
		close(discarded)
	}) {
		t.Fatal("queued peer task submission rejected")
	}
	if !s.Submit(ctx, 2, func(context.Context) {
		close(progressed)
	}) {
		t.Fatal("subsequent peer task submission rejected")
	}

	runDone := make(chan error, 1)
	go func() { runDone <- s.Run(ctx) }()

	select {
	case peerID := <-panicked:
		if peerID != peer.ID() {
			t.Fatalf("panic attributed to peer %d, want %d", peerID, peer.ID())
		}
	case <-time.After(time.Second):
		t.Fatal("serve task panic was not recovered")
	}
	select {
	case <-discarded:
	case <-time.After(time.Second):
		t.Fatal("panicking peer's queued task was not discarded")
	}
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("panicking task did not release owned resources")
	}
	select {
	case <-progressed:
	case <-time.After(time.Second):
		t.Fatal("scheduler worker did not make progress after panic")
	}
	select {
	case <-queuedRan:
		t.Fatal("panicking peer's queued task ran")
	default:
	}

	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		active := len(s.active)
		running := len(s.running)
		s.mu.Unlock()
		if active == 0 && running == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scheduler accounting leaked after panic: active=%d running=%d", active, running)
		}
		time.Sleep(time.Millisecond)
	}
	if !peer.closed.Load() || peer.State() != PeerStateDisconnected {
		t.Fatal("panicking peer was not closed")
	}
	if overlay.submitServeForPeerOwned(
		peer.ID(), resource.NewCharge(0, "test"), func(context.Context) {}, nil,
	) {
		t.Fatal("overlay accepted serve work from the closed peer")
	}

	cancel()
	select {
	case err := <-runDone:
		if err != context.Canceled {
			t.Fatalf("scheduler run error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after cancellation")
	}
}

package peermanagement

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
)

func TestServeSchedulerPerPeerFairness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewServeScheduler(ctx, ServeSchedulerConfig{Workers: 2, MaxPending: 32, PerPeerQueue: 16, PerPeerActive: 1})
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
	var startedPeers [2]PeerID
	for i := range startedPeers {
		select {
		case startedPeers[i] = <-started:
		case <-time.After(time.Second):
			t.Fatal("scheduler did not start one job from each peer")
		}
	}
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
	s := NewServeScheduler(ctx, ServeSchedulerConfig{Workers: 1, MaxPending: 1, PerPeerQueue: 1, PerPeerActive: 1})
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
	s := NewServeScheduler(ctx, ServeSchedulerConfig{Workers: 2, MaxPending: 8, PerPeerQueue: 8, PerPeerActive: 2})
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
	s := NewServeScheduler(ctx, ServeSchedulerConfig{Workers: 1, MaxPending: 256, PerPeerQueue: 1, PerPeerActive: 1})
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
	s := NewServeScheduler(ctx, ServeSchedulerConfig{Workers: workers, MaxPending: workers, PerPeerQueue: 1, PerPeerActive: 1})
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
	s := NewServeScheduler(ctx, ServeSchedulerConfig{Workers: 1, MaxPending: 1, PerPeerQueue: 1, PerPeerActive: 1})
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
	s.Close()
	if got := discarded.Load(); got != 1 {
		t.Fatalf("discard count = %d, want 1", got)
	}
}

func TestServeSchedulerRejectedOwnedTaskRemainsCallerOwned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewServeScheduler(ctx, ServeSchedulerConfig{Workers: 1, MaxPending: 1, PerPeerQueue: 1, PerPeerActive: 1})
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
	s := NewServeScheduler(ctx, ServeSchedulerConfig{Workers: 1, MaxPending: 1, PerPeerQueue: 1, PerPeerActive: 1})
	var discarded atomic.Int32
	if !s.SubmitOwned(ctx, 1, func(context.Context) {
		t.Fatal("closed queued task ran")
	}, func() {
		discarded.Add(1)
	}) {
		t.Fatal("owned task submission rejected")
	}

	s.Close()
	s.Close()
	if got := discarded.Load(); got != 1 {
		t.Fatalf("discard count = %d, want 1", got)
	}
}

func TestServeSchedulerActiveCancellationUsesRunCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewServeScheduler(ctx, ServeSchedulerConfig{Workers: 1, MaxPending: 1, PerPeerQueue: 1, PerPeerActive: 1})
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
	s := NewServeScheduler(ctx, ServeSchedulerConfig{Workers: 1, MaxPending: 3, PerPeerQueue: 2, PerPeerActive: 1})
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

func TestOverlayServeAdmissionSerializesWithPeerClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewServeScheduler(ctx, ServeSchedulerConfig{Workers: 1, MaxPending: 1, PerPeerQueue: 1, PerPeerActive: 1})
	peer := NewPeer(1, Endpoint{Host: "192.0.2.1", Port: 51235}, false, nil, nil)
	peer.setState(PeerStateConnected)
	overlay := &Overlay{
		peers:          map[PeerID]*Peer{peer.ID(): peer},
		serveScheduler: s,
		ctx:            ctx,
		lifecycleState: overlayLifecycleRunning,
	}

	s.mu.Lock()
	submitDone := make(chan bool, 1)
	go func() {
		submitDone <- overlay.submitServeForPeerOwned(
			peer.ID(), resource.NewCharge(0, "test"), func(context.Context) {}, nil,
		)
	}()

	deadline := time.Now().Add(time.Second)
	for peer.sendMu.TryLock() {
		peer.sendMu.Unlock()
		if time.Now().After(deadline) {
			s.mu.Unlock()
			t.Fatal("serve admission did not lock the peer against close")
		}
		time.Sleep(time.Millisecond)
	}

	closeStarted := make(chan struct{})
	closeDone := make(chan error, 1)
	go func() {
		close(closeStarted)
		closeDone <- peer.Close()
	}()
	<-closeStarted
	select {
	case err := <-closeDone:
		s.mu.Unlock()
		t.Fatalf("peer closed before serve admission completed: %v", err)
	case <-time.After(10 * time.Millisecond):
	}

	s.mu.Unlock()
	if accepted := <-submitDone; !accepted {
		t.Fatal("serve admission was rejected before peer close")
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("peer close failed: %v", err)
	}
	s.CancelPeer(peer.ID())
	if pending := s.Pending(); pending != 0 {
		t.Fatalf("pending = %d after peer close cancellation, want 0", pending)
	}
}

func TestServeSchedulerRetainedBytesIncludeActiveTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewServeScheduler(ctx, ServeSchedulerConfig{
		Workers:       1,
		MaxPending:    4,
		PerPeerQueue:  4,
		PerPeerActive: 1,
		MaxBytes:      10,
	})
	runDone := make(chan error, 1)
	go func() { runDone <- s.Run(ctx) }()

	started := make(chan struct{})
	release := make(chan struct{})
	if !s.SubmitOwnedBytes(ctx, 1, 7, func(context.Context) {
		close(started)
		<-release
	}, nil) {
		t.Fatal("active byte-charged task rejected")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("byte-charged task did not start")
	}
	stats := s.Snapshot()
	if stats.Queued != 0 || stats.Active != 1 || stats.RetainedBytes != 7 {
		t.Fatalf("active snapshot = %+v, want queued=0 active=1 retained=7", stats)
	}
	var rejectedDiscarded atomic.Int32
	if s.SubmitOwnedBytes(ctx, 2, 4, func(context.Context) {}, func() {
		rejectedDiscarded.Add(1)
	}) {
		t.Fatal("submission over retained-byte budget accepted")
	}
	if got := rejectedDiscarded.Load(); got != 0 {
		t.Fatalf("rejected task was discarded %d times, want 0", got)
	}
	if stats = s.Snapshot(); stats.RetainedBytes != 7 || stats.Dropped != 1 {
		t.Fatalf("after rejection snapshot = %+v, want retained=7 dropped=1", stats)
	}

	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		stats = s.Snapshot()
		if stats.Active == 0 && stats.RetainedBytes == 0 && stats.Completed == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("completed snapshot = %+v", stats)
		}
		time.Sleep(time.Millisecond)
	}
	secondDone := make(chan struct{})
	if !s.SubmitOwnedBytes(ctx, 2, 10, func(context.Context) {
		close(secondDone)
	}, nil) {
		t.Fatal("byte budget was not released after completion")
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second byte-charged task did not run")
	}
	cancel()
	select {
	case err := <-runDone:
		if err != context.Canceled {
			t.Fatalf("scheduler run error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}

func TestServeSchedulerRejectsInvalidRetainedBytesWithoutOverflow(t *testing.T) {
	s := NewServeScheduler(context.Background(), ServeSchedulerConfig{
		Workers:       1,
		MaxPending:    4,
		PerPeerQueue:  4,
		PerPeerActive: 1,
		MaxBytes:      maxRetainedBytes,
	})
	var discarded atomic.Int32
	if !s.SubmitOwnedBytes(context.Background(), 1, maxRetainedBytes, func(context.Context) {}, func() {
		discarded.Add(1)
	}) {
		t.Fatal("maximum retained-byte task rejected")
	}
	if s.SubmitOwnedBytes(context.Background(), 1, 1, func(context.Context) {}, func() {
		discarded.Add(1)
	}) {
		t.Fatal("retained-byte addition overflow was accepted")
	}
	if s.SubmitOwnedBytes(context.Background(), 1, -1, func(context.Context) {}, func() {
		discarded.Add(1)
	}) {
		t.Fatal("negative retained-byte task accepted")
	}
	if got := discarded.Load(); got != 0 {
		t.Fatalf("rejected tasks were discarded %d times, want 0", got)
	}
	if got := s.Snapshot(); got.Dropped != 2 || got.RetainedBytes != maxRetainedBytes {
		t.Fatalf("rejection snapshot = %+v", got)
	}
	s.Close()
	if got := discarded.Load(); got != 1 {
		t.Fatalf("accepted queued task discarded %d times, want 1", got)
	}
	if got := s.Snapshot(); got.RetainedBytes != 0 || got.Canceled != 1 {
		t.Fatalf("close snapshot = %+v", got)
	}
}

func TestServeSchedulerCancelPeerKeepsActiveRetentionUntilExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewServeScheduler(ctx, ServeSchedulerConfig{
		Workers:       1,
		MaxPending:    4,
		PerPeerQueue:  4,
		PerPeerActive: 1,
		MaxBytes:      10,
	})
	go func() { _ = s.Run(ctx) }()
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	cleaned := make(chan struct{})
	if !s.SubmitOwnedBytes(ctx, 1, 6, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		close(cleaned)
	}, nil) {
		t.Fatal("active task rejected")
	}
	var queuedDiscarded atomic.Int32
	if !s.SubmitOwnedBytes(ctx, 1, 3, func(context.Context) {
		t.Fatal("canceled queued task ran")
	}, func() {
		queuedDiscarded.Add(1)
	}) {
		t.Fatal("queued task rejected")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("active task did not start")
	}
	s.CancelPeer(1)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("active task did not observe cancellation")
	}
	if got := s.Snapshot(); got.Active != 1 || got.Queued != 0 || got.RetainedBytes != 6 {
		t.Fatalf("after peer cancellation snapshot = %+v", got)
	}
	close(release)
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("active task did not observe cancellation")
	}
	deadline := time.Now().Add(time.Second)
	for {
		stats := s.Snapshot()
		if stats.Active == 0 && stats.RetainedBytes == 0 && stats.Canceled == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after active cleanup snapshot = %+v", stats)
		}
		time.Sleep(time.Millisecond)
	}
	if got := queuedDiscarded.Load(); got != 1 {
		t.Fatalf("queued discard count = %d, want 1", got)
	}
	s.Close()
}

func TestServeSchedulerCloseJoinsWorkers(t *testing.T) {
	s := NewServeScheduler(context.Background(), ServeSchedulerConfig{
		Workers:       1,
		MaxPending:    1,
		PerPeerQueue:  1,
		PerPeerActive: 1,
		MaxBytes:      1,
	})
	runDone := make(chan error, 1)
	go func() { runDone <- s.Run(context.Background()) }()
	started := make(chan struct{})
	cleaned := make(chan struct{})
	if !s.SubmitOwnedBytes(context.Background(), 1, 1, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(cleaned)
	}, nil) {
		t.Fatal("active task rejected")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("active task did not start")
	}
	s.Close()
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("active task did not observe close")
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("scheduler run error = %v, want nil after direct close", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not join workers after direct close")
	}
	if got := s.Snapshot(); got.Active != 0 || got.RetainedBytes != 0 || got.Canceled != 1 {
		t.Fatalf("close snapshot = %+v", got)
	}
}

func TestServeSchedulerSnapshotTimingIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewServeScheduler(ctx, ServeSchedulerConfig{
		Workers:       1,
		MaxPending:    1,
		PerPeerQueue:  1,
		PerPeerActive: 1,
	})
	runDone := make(chan error, 1)
	go func() { runDone <- s.Run(ctx) }()
	finished := make(chan struct{})
	if !s.Submit(ctx, 1, func(context.Context) {
		close(finished)
	}) {
		t.Fatal("timed task rejected")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("timed task did not finish")
	}
	deadline := time.Now().Add(time.Second)
	var stats ServeSchedulerStats
	for {
		stats = s.Snapshot()
		if stats.QueueWait.Count == 1 && stats.Service.Count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timing snapshot = %+v", stats)
		}
		time.Sleep(time.Millisecond)
	}
	if stats.QueueWait.Sum < 0 || stats.Service.Sum < 0 {
		t.Fatalf("negative timing sum: %+v", stats)
	}
	for _, distribution := range []ServeDurationStats{stats.QueueWait, stats.Service} {
		previous := uint64(0)
		for i, count := range distribution.Buckets {
			if count < previous || count > distribution.Count {
				t.Fatalf("non-cumulative timing bucket %d: count=%d previous=%d total=%d", i, count, previous, distribution.Count)
			}
			previous = count
		}
	}
	wantBounds := [8]float64{1e-6, 1e-5, 1e-4, 1e-3, 1e-2, 1e-1, 1, 10}
	if got := ServeDurationBucketBounds(); got != wantBounds {
		t.Fatalf("timing bounds = %v, want %v", got, wantBounds)
	}
	cancel()
	select {
	case err := <-runDone:
		if err != context.Canceled {
			t.Fatalf("scheduler run error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}

func TestServeSchedulerCanceledParentConstruction(t *testing.T) {
	for range 100 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s := NewServeScheduler(ctx, ServeSchedulerConfig{
			Workers:       1,
			MaxPending:    1,
			PerPeerQueue:  1,
			PerPeerActive: 1,
		})
		select {
		case <-s.done:
		case <-time.After(time.Second):
			t.Fatal("scheduler did not close for an already-canceled parent")
		}
		s.Close()
	}
}

func TestServeSchedulerConcurrentCloseJoinsDiscard(t *testing.T) {
	s := NewServeScheduler(context.Background(), ServeSchedulerConfig{
		Workers:       1,
		MaxPending:    1,
		PerPeerQueue:  1,
		PerPeerActive: 1,
	})
	discardStarted := make(chan struct{})
	releaseDiscard := make(chan struct{})
	if !s.SubmitOwned(context.Background(), 1, func(context.Context) {
		t.Fatal("queued task ran while close was disposing it")
	}, func() {
		close(discardStarted)
		<-releaseDiscard
	}) {
		t.Fatal("queued task rejected")
	}
	closeDone := make(chan struct{})
	go func() {
		s.Close()
		close(closeDone)
	}()
	select {
	case <-discardStarted:
	case <-time.After(time.Second):
		t.Fatal("close did not enter discard")
	}
	select {
	case <-closeDone:
		t.Fatal("Close returned before discard completed")
	case <-time.After(20 * time.Millisecond):
	}

	runDone := make(chan error, 1)
	go func() { runDone <- s.Run(context.Background()) }()
	select {
	case err := <-runDone:
		t.Fatalf("Run returned before discard completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseDiscard)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after discard completed")
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run error = %v, want nil after direct close", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not join close disposal")
	}
}

func TestServeSchedulerRetainedBytesStayChargedDuringDiscard(t *testing.T) {
	s := NewServeScheduler(context.Background(), ServeSchedulerConfig{
		Workers:       1,
		MaxPending:    2,
		PerPeerQueue:  1,
		PerPeerActive: 1,
		MaxBytes:      1,
	})
	discardStarted := make(chan struct{})
	releaseDiscard := make(chan struct{})
	var firstDiscarded atomic.Int32
	if !s.SubmitOwnedBytes(context.Background(), 1, 1, func(context.Context) {
		t.Fatal("queued task ran")
	}, func() {
		firstDiscarded.Add(1)
		close(discardStarted)
		<-releaseDiscard
	}) {
		t.Fatal("byte-charged task rejected")
	}
	cancelDone := make(chan struct{})
	go func() {
		s.CancelPeer(1)
		close(cancelDone)
	}()
	select {
	case <-discardStarted:
	case <-time.After(time.Second):
		t.Fatal("peer cancellation did not enter discard")
	}
	stats := s.Snapshot()
	if stats.Queued != 0 || stats.Active != 0 || stats.RetainedBytes != 1 {
		t.Fatalf("during discard snapshot = %+v, want retained byte charge", stats)
	}
	var rejectedDiscarded atomic.Int32
	if s.SubmitOwnedBytes(context.Background(), 2, 1, func(context.Context) {}, func() {
		rejectedDiscarded.Add(1)
	}) {
		t.Fatal("new task admitted before discard released retained bytes")
	}
	if got := rejectedDiscarded.Load(); got != 0 {
		t.Fatalf("rejected task discarded %d times, want 0", got)
	}
	close(releaseDiscard)
	select {
	case <-cancelDone:
	case <-time.After(time.Second):
		t.Fatal("peer cancellation did not finish")
	}
	if got := s.Snapshot(); got.RetainedBytes != 0 {
		t.Fatalf("after discard retained bytes = %d, want 0", got.RetainedBytes)
	}
	var secondDiscarded atomic.Int32
	if !s.SubmitOwnedBytes(context.Background(), 2, 1, func(context.Context) {}, func() {
		secondDiscarded.Add(1)
	}) {
		t.Fatal("byte budget was not released after discard")
	}
	s.Close()
	if firstDiscarded.Load() != 1 || secondDiscarded.Load() != 1 {
		t.Fatalf("discard counts = first %d second %d, want one each", firstDiscarded.Load(), secondDiscarded.Load())
	}
}

func TestServeSchedulerDiscardReleasesCompletedRequests(t *testing.T) {
	for _, shutdown := range []string{"cancel_peer", "close"} {
		for _, outcome := range []string{"return", "panic"} {
			t.Run(shutdown+"/"+outcome, func(t *testing.T) {
				const requestBytes = 1 << 20
				s := NewServeScheduler(context.Background(), ServeSchedulerConfig{
					Workers:       1,
					MaxPending:    2,
					PerPeerQueue:  2,
					PerPeerActive: 1,
					MaxBytes:      requestBytes + 1,
				})
				defer s.Close()
				request := func() weak.Pointer[InboundMessage] {
					msg := &InboundMessage{Payload: make([]byte, requestBytes)}
					if !s.SubmitOwnedBytes(context.Background(), 1, requestBytes, func(context.Context) {
						runtime.KeepAlive(msg)
					}, func() {
						_ = msg.Close()
						if outcome == "panic" {
							panic("discard failure")
						}
					}) {
						t.Fatal("first request rejected")
					}
					return weak.Make(msg)
				}()
				runtime.GC()
				if request.Value() == nil {
					t.Fatal("queued request was not retained")
				}

				discardStarted := make(chan struct{})
				releaseDiscard := make(chan struct{})
				defer close(releaseDiscard)
				if !s.SubmitOwnedBytes(context.Background(), 1, 1, func(context.Context) {}, func() {
					close(discardStarted)
					<-releaseDiscard
				}) {
					t.Fatal("second request rejected")
				}
				discardDone := make(chan struct{})
				go func() {
					defer close(discardDone)
					if shutdown == "cancel_peer" {
						s.CancelPeer(1)
					} else {
						s.Close()
					}
				}()
				t.Cleanup(func() {
					select {
					case <-discardDone:
					case <-time.After(time.Second):
						t.Error("discard did not finish")
					}
				})
				select {
				case <-discardStarted:
				case <-time.After(time.Second):
					t.Fatal("second discard did not start")
				}
				if got := s.Snapshot(); got.RetainedBytes != 1 || got.Queued != 0 || got.Active != 0 {
					t.Fatalf("during second discard snapshot = %+v, want only its byte charge", got)
				}
				runtime.GC()
				if request.Value() != nil {
					t.Fatal("completed request retained after its byte charge was released")
				}
			})
		}
	}
}

func TestServeSchedulerCloseJoinsCancelPeerDiscard(t *testing.T) {
	s := NewServeScheduler(context.Background(), ServeSchedulerConfig{
		Workers:       1,
		MaxPending:    1,
		PerPeerQueue:  1,
		PerPeerActive: 1,
		MaxBytes:      1,
	})
	discardStarted := make(chan struct{})
	releaseDiscard := make(chan struct{})
	if !s.SubmitOwnedBytes(context.Background(), 1, 1, func(context.Context) {
		t.Fatal("queued task ran while peer cancellation was disposing it")
	}, func() {
		close(discardStarted)
		<-releaseDiscard
	}) {
		t.Fatal("byte-charged task rejected")
	}
	cancelDone := make(chan struct{})
	go func() {
		s.CancelPeer(1)
		close(cancelDone)
	}()
	select {
	case <-discardStarted:
	case <-time.After(time.Second):
		t.Fatal("peer cancellation did not enter discard")
	}

	closeDone := make(chan struct{})
	go func() {
		s.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("Close returned before CancelPeer discard completed")
	case <-time.After(20 * time.Millisecond):
	}
	runDone := make(chan error, 1)
	go func() { runDone <- s.Run(context.Background()) }()
	select {
	case err := <-runDone:
		t.Fatalf("Run returned before CancelPeer discard completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseDiscard)
	select {
	case <-cancelDone:
	case <-time.After(time.Second):
		t.Fatal("CancelPeer did not finish after discard completed")
	}
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not join CancelPeer discard")
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run error = %v, want nil after direct close", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not join CancelPeer discard")
	}
	if got := s.Snapshot(); got.RetainedBytes != 0 || got.Queued != 0 || got.Canceled != 1 {
		t.Fatalf("final snapshot = %+v, want no queued retention and one cancellation", got)
	}
}

func TestServeSchedulerPerPeerByteBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewServeScheduler(ctx, ServeSchedulerConfig{
		Workers:       1,
		MaxPending:    4,
		PerPeerQueue:  4,
		PerPeerActive: 1,
		MaxBytes:      10,
		PerPeerBytes:  6,
	})
	runDone := make(chan error, 1)
	go func() { runDone <- s.Run(ctx) }()

	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseTask := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseTask()
	if !s.SubmitOwnedBytes(ctx, 1, 6, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
	}, nil) {
		t.Fatal("peer A byte-charged task rejected")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("peer A task did not start")
	}

	var rejectedDiscarded atomic.Int32
	if s.SubmitOwnedBytes(ctx, 1, 1, func(context.Context) {}, func() {
		rejectedDiscarded.Add(1)
	}) {
		t.Fatal("peer A exceeded its byte budget")
	}
	if got := rejectedDiscarded.Load(); got != 0 {
		t.Fatalf("rejected peer A task was discarded %d times", got)
	}
	bDone := make(chan struct{})
	if !s.SubmitOwnedBytes(ctx, 2, 4, func(context.Context) {
		close(bDone)
	}, nil) {
		t.Fatal("peer B was rejected while peer A used its per-peer budget")
	}
	if got := s.Snapshot(); got.Queued != 1 || got.Active != 1 || got.RetainedBytes != 10 {
		t.Fatalf("saturated snapshot = %+v, want queued=1 active=1 retained=10", got)
	}

	s.CancelPeer(1)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("peer A task did not observe cancellation")
	}
	if s.SubmitOwnedBytes(ctx, 1, 1, func(context.Context) {}, nil) {
		t.Fatal("peer A was admitted before canceled active work released its bytes")
	}
	if got := s.Snapshot(); got.RetainedBytes != 10 || got.Active != 1 || got.Queued != 1 {
		t.Fatalf("canceled snapshot = %+v, want retained=10 active=1 queued=1", got)
	}

	releaseTask()
	select {
	case <-bDone:
	case <-time.After(time.Second):
		t.Fatal("peer B task did not run after peer A exited")
	}
	deadline := time.Now().Add(time.Second)
	for {
		stats := s.Snapshot()
		if stats.Active == 0 && stats.Queued == 0 && stats.RetainedBytes == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cleanup snapshot = %+v", stats)
		}
		time.Sleep(time.Millisecond)
	}
	if !s.SubmitOwnedBytes(ctx, 1, 6, func(context.Context) {}, nil) {
		t.Fatal("peer A byte budget was not released after active cleanup")
	}
	s.Close()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("scheduler run error = %v, want nil after direct close", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}

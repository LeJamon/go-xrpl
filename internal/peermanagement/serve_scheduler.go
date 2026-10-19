package peermanagement

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

const maxRetainedBytes = int64(^uint64(0) >> 1)

var serveDurationBucketBounds = [...]float64{1e-6, 1e-5, 1e-4, 1e-3, 1e-2, 1e-1, 1, 10}

// ServeDurationStats is a bounded cumulative duration distribution. Buckets
// use seconds as their upper bounds; observations above the final bound are
// represented by Count without an additional bucket.
type ServeDurationStats struct {
	Count   uint64
	Sum     time.Duration
	Buckets [8]uint64
}

// ServeDurationBucketBounds returns the upper bounds, in seconds, for
// ServeDurationStats.Buckets.
func ServeDurationBucketBounds() [8]float64 { return serveDurationBucketBounds }

// ServeSchedulerConfig controls a ServeScheduler's worker and retention
// limits. A zero MaxBytes or PerPeerBytes disables that byte limit.
type ServeSchedulerConfig struct {
	Workers       int
	MaxPending    int
	PerPeerQueue  int
	PerPeerActive int
	MaxBytes      int64
	PerPeerBytes  int64
}

// ServeSchedulerStats is a point-in-time view of scheduler occupancy and
// cumulative task outcomes.
type ServeSchedulerStats struct {
	Queued        int
	Active        int
	RetainedBytes int64
	Dropped       uint64
	Completed     uint64
	Canceled      uint64
	QueueWait     ServeDurationStats
	Service       ServeDurationStats
}

// ServeScheduler bounds expensive peer-serving work independently from the
// overlay event loop. Jobs are kept in per-peer queues and selected in
// round-robin order. A per-peer active limit prevents one peer from occupying
// every worker while bounded queues and retained bytes prevent unbounded
// retention.
type ServeScheduler struct {
	mu sync.Mutex

	ctx context.Context

	queues      map[PeerID][]serveTask
	active      map[PeerID]int
	running     map[PeerID]map[uint64]context.CancelFunc
	round       []PeerID
	next        int
	nextID      uint64
	pending     int
	activeCount int

	workers       int
	maxPending    int
	perPeerQueue  int
	perPeerActive int
	maxBytes      int64
	perPeerBytes  int64
	retainedBytes int64
	peerRetained  map[PeerID]int64

	notify      chan struct{}
	done        chan struct{}
	disposeDone chan struct{}
	disposalWG  sync.WaitGroup
	closed      bool
	idle        atomic.Int32

	dropped   atomic.Uint64
	completed atomic.Uint64
	canceled  atomic.Uint64
	queueWait ServeDurationStats
	service   ServeDurationStats

	stopContextHook func() bool

	onTaskPanic func(PeerID)
}

type serveTask struct {
	peerID        PeerID
	id            uint64
	ctx           context.Context
	cancel        context.CancelFunc
	retainedBytes int64
	submittedAt   time.Time
	startedAt     time.Time
	run           func(context.Context)
	discard       func()
}

// NewServeScheduler constructs a scheduler. Workers start when Run is called.
func NewServeScheduler(ctx context.Context, config ServeSchedulerConfig) *ServeScheduler {
	if ctx == nil {
		ctx = context.Background()
	}
	if config.Workers < 1 {
		config.Workers = 1
	}
	if config.MaxPending < 1 {
		config.MaxPending = 1
	}
	if config.PerPeerQueue < 1 {
		config.PerPeerQueue = 1
	}
	if config.PerPeerActive < 1 {
		config.PerPeerActive = 1
	}
	if config.MaxBytes < 0 {
		config.MaxBytes = 0
	}
	if config.PerPeerBytes < 0 {
		config.PerPeerBytes = 0
	}
	s := &ServeScheduler{
		ctx:           ctx,
		queues:        make(map[PeerID][]serveTask),
		active:        make(map[PeerID]int),
		running:       make(map[PeerID]map[uint64]context.CancelFunc),
		workers:       config.Workers,
		maxPending:    config.MaxPending,
		perPeerQueue:  config.PerPeerQueue,
		perPeerActive: config.PerPeerActive,
		maxBytes:      config.MaxBytes,
		perPeerBytes:  config.PerPeerBytes,
		peerRetained:  make(map[PeerID]int64),
		notify:        make(chan struct{}),
		done:          make(chan struct{}),
		disposeDone:   make(chan struct{}),
	}
	// Install the hook while holding the scheduler mutex. An already-canceled
	// context may start the callback before AfterFunc returns.
	s.mu.Lock()
	s.stopContextHook = context.AfterFunc(ctx, s.Close)
	s.mu.Unlock()
	return s
}

// Workers returns the fixed number of workers started by Run.
func (s *ServeScheduler) Workers() int { return s.workers }

// Dropped returns the cumulative number of rejected submissions.
func (s *ServeScheduler) Dropped() uint64 { return s.dropped.Load() }

// Pending returns the number of queued tasks. Active tasks are not pending.
func (s *ServeScheduler) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending
}

// Snapshot returns scheduler occupancy and cumulative task outcomes.
func (s *ServeScheduler) Snapshot() ServeSchedulerStats {
	s.mu.Lock()
	stats := ServeSchedulerStats{
		Queued:        s.pending,
		Active:        s.activeCount,
		RetainedBytes: s.retainedBytes,
		Dropped:       s.dropped.Load(),
		Completed:     s.completed.Load(),
		Canceled:      s.canceled.Load(),
		QueueWait:     s.queueWait,
		Service:       s.service,
	}
	s.mu.Unlock()
	return stats
}

// Submit queues a job without waiting for capacity. The request context is
// derived once so queued work can be canceled on peer disconnect or shutdown.
func (s *ServeScheduler) Submit(ctx context.Context, peerID PeerID, run func(context.Context)) bool {
	return s.SubmitOwned(ctx, peerID, run, nil)
}

// SubmitOwned queues work whose caller retains resources until the task runs
// or is discarded. A rejected submission remains owned by the caller.
func (s *ServeScheduler) SubmitOwned(
	ctx context.Context,
	peerID PeerID,
	run func(context.Context),
	discard func(),
) bool {
	return s.submitOwned(ctx, peerID, 0, run, discard)
}

// SubmitOwnedBytes is SubmitOwned with an explicit retained-byte charge. The
// charge remains reserved until the task or its discard callback returns.
// A rejected submission remains owned by the caller.
func (s *ServeScheduler) SubmitOwnedBytes(
	ctx context.Context,
	peerID PeerID,
	retainedBytes int64,
	run func(context.Context),
	discard func(),
) bool {
	return s.submitOwned(ctx, peerID, retainedBytes, run, discard)
}

func (s *ServeScheduler) submitOwned(
	ctx context.Context,
	peerID PeerID,
	retainedBytes int64,
	run func(context.Context),
	discard func(),
) bool {
	if run == nil {
		return false
	}
	if retainedBytes < 0 {
		s.mu.Lock()
		s.dropped.Add(1)
		s.mu.Unlock()
		return false
	}
	if ctx == nil {
		ctx = s.ctx
	}
	submittedAt := time.Now()
	jobCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.closed || s.pending >= s.maxPending || !s.bytesAvailableLocked(peerID, retainedBytes) {
		s.dropped.Add(1)
		s.mu.Unlock()
		cancel()
		return false
	}
	queue := s.queues[peerID]
	if len(queue) >= s.perPeerQueue {
		s.dropped.Add(1)
		s.mu.Unlock()
		cancel()
		return false
	}
	if len(queue) == 0 {
		s.round = append(s.round, peerID)
	}
	queue = append(queue, serveTask{
		peerID:        peerID,
		ctx:           jobCtx,
		cancel:        cancel,
		retainedBytes: retainedBytes,
		submittedAt:   submittedAt,
		run:           run,
		discard:       discard,
	})
	s.queues[peerID] = queue
	s.pending++
	s.retainedBytes += retainedBytes
	if retainedBytes != 0 {
		s.peerRetained[peerID] += retainedBytes
	}
	s.signalLocked()
	s.mu.Unlock()
	return true
}

func recordServeDuration(stats *ServeDurationStats, elapsed time.Duration) {
	if elapsed < 0 {
		elapsed = 0
	}
	stats.Count++
	stats.Sum += elapsed
	seconds := elapsed.Seconds()
	for i, bound := range serveDurationBucketBounds {
		if seconds <= bound {
			stats.Buckets[i]++
		}
	}
}

func (s *ServeScheduler) bytesAvailableLocked(peerID PeerID, retainedBytes int64) bool {
	if retainedBytes > maxRetainedBytes-s.retainedBytes {
		return false
	}
	if s.maxBytes != 0 && retainedBytes > s.maxBytes-s.retainedBytes {
		return false
	}
	peerRetained := s.peerRetained[peerID]
	if retainedBytes > maxRetainedBytes-peerRetained {
		return false
	}
	return s.perPeerBytes == 0 || retainedBytes <= s.perPeerBytes-peerRetained
}

// CancelPeer disposes queued work and cancels work currently executing for a
// peer. It is safe to call after the peer has already been removed.
func (s *ServeScheduler) CancelPeer(peerID PeerID) {
	s.mu.Lock()
	queue := s.queues[peerID]
	delete(s.queues, peerID)
	s.pending -= len(queue)
	queued := make([]serveTask, len(queue))
	for i, task := range queue {
		queued[i] = task
		queue[i] = serveTask{}
	}
	if len(queue) != 0 {
		s.canceled.Add(uint64(len(queue)))
		s.disposalWG.Add(len(queue))
	}
	// Running tasks are represented by a context in the running map. The
	// scheduler intentionally keeps this separate from active counts so that
	// finishing a canceled task still releases exactly one slot.
	runningCancels := make([]context.CancelFunc, 0, len(s.running[peerID]))
	for _, cancel := range s.running[peerID] {
		runningCancels = append(runningCancels, cancel)
	}
	s.removePeerLocked(peerID)
	s.signalLocked()
	s.mu.Unlock()

	for _, cancel := range runningCancels {
		cancel()
	}
	s.discardQueuedTasks(queued)
}

// Close cancels all queued and running work. Queued ownership is discarded;
// active ownership remains reserved until the task exits and Run has joined
// its worker. It also waits for queued disposal already in progress.
func (s *ServeScheduler) Close() {
	s.mu.Lock()
	if s.closed {
		disposeDone := s.disposeDone
		s.mu.Unlock()
		<-disposeDone
		return
	}
	s.closed = true
	close(s.done)
	queued := make([]serveTask, 0, s.pending)
	for peerID, queue := range s.queues {
		for i, task := range queue {
			queued = append(queued, task)
			queue[i] = serveTask{}
		}
		delete(s.queues, peerID)
	}
	if len(queued) != 0 {
		s.canceled.Add(uint64(len(queued)))
		s.disposalWG.Add(len(queued))
	}
	s.pending = 0
	runningCancels := make([]context.CancelFunc, 0)
	for _, runs := range s.running {
		for _, cancel := range runs {
			runningCancels = append(runningCancels, cancel)
		}
	}
	s.round = nil
	s.next = 0
	stopContextHook := s.stopContextHook
	s.stopContextHook = nil
	close(s.notify)
	s.mu.Unlock()

	if stopContextHook != nil {
		stopContextHook()
	}
	defer close(s.disposeDone)
	for _, cancel := range runningCancels {
		cancel()
	}
	s.discardQueuedTasks(queued)
	s.disposalWG.Wait()
}

// signalLocked wakes every worker currently waiting for scheduler state. A
// fresh channel is installed while holding s.mu, so a worker that observes an
// empty queue cannot miss a concurrent enqueue between its state check and
// entering the wait select.
func (s *ServeScheduler) signalLocked() {
	if s.closed {
		return
	}
	close(s.notify)
	s.notify = make(chan struct{})
}

// Run starts the fixed worker set and joins every worker when the scheduler or
// the run context is canceled. Overlay.Run owns the returned error.
func (s *ServeScheduler) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = s.ctx
	}
	var wg sync.WaitGroup
	wg.Add(s.workers)
	for range s.workers {
		go func() {
			defer wg.Done()
			s.worker(ctx)
		}()
	}
	select {
	case <-ctx.Done():
		s.Close()
	case <-s.done:
		s.Close()
	}
	wg.Wait()
	return ctx.Err()
}

func (s *ServeScheduler) worker(ctx context.Context) {
	for {
		task, ok := s.take(ctx)
		if !ok {
			return
		}
		s.runTask(task)
	}
}

func (s *ServeScheduler) runTask(task serveTask) {
	completed := false
	defer func() {
		s.finish(task, completed)
	}()
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("serve worker panicked", "t", "Overlay", "peer", task.peerID,
				"panic", recovered, "stack", string(debug.Stack()))
			if s.onTaskPanic != nil {
				s.onTaskPanic(task.peerID)
			}
			s.CancelPeer(task.peerID)
		}
	}()
	if task.ctx.Err() != nil {
		task.discardTask()
		return
	}
	task.run(task.ctx)
	completed = task.ctx.Err() == nil
}

func (t serveTask) discardTask() {
	if t.discard != nil {
		t.discard()
	}
}

func (s *ServeScheduler) discardQueuedTask(task serveTask) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("serve discard panicked", "t", "Overlay", "peer", task.peerID,
				"panic", recovered, "stack", string(debug.Stack()))
		}
		s.releaseRetainedBytes(task.peerID, task.retainedBytes)
	}()
	task.cancel()
	task.discardTask()
}

func (s *ServeScheduler) discardQueuedTasks(tasks []serveTask) {
	for _, task := range tasks {
		func() {
			defer s.disposalWG.Done()
			s.discardQueuedTask(task)
		}()
	}
}

func (s *ServeScheduler) releaseRetainedBytes(peerID PeerID, retainedBytes int64) {
	if retainedBytes == 0 {
		return
	}
	s.mu.Lock()
	s.releaseRetainedBytesLocked(peerID, retainedBytes)
	s.mu.Unlock()
}

func (s *ServeScheduler) releaseRetainedBytesLocked(peerID PeerID, retainedBytes int64) {
	s.retainedBytes -= retainedBytes
	peerRetained := s.peerRetained[peerID] - retainedBytes
	if peerRetained == 0 {
		delete(s.peerRetained, peerID)
	} else {
		s.peerRetained[peerID] = peerRetained
	}
}

func (s *ServeScheduler) take(ctx context.Context) (serveTask, bool) {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return serveTask{}, false
		}
		if task, ok := s.takeLocked(); ok {
			s.mu.Unlock()
			return task, true
		}
		wait := s.notify
		s.idle.Add(1)
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			s.idle.Add(-1)
			return serveTask{}, false
		case <-s.done:
			s.idle.Add(-1)
			return serveTask{}, false
		case <-wait:
			s.idle.Add(-1)
		}
	}
}

func (s *ServeScheduler) takeLocked() (serveTask, bool) {
	if len(s.round) == 0 {
		return serveTask{}, false
	}
	for checked := 0; checked < len(s.round); checked++ {
		if s.next >= len(s.round) {
			s.next = 0
		}
		peerID := s.round[s.next]
		s.next++
		queue := s.queues[peerID]
		if len(queue) == 0 || s.active[peerID] >= s.perPeerActive {
			continue
		}
		task := queue[0]
		queue[0] = serveTask{}
		now := time.Now()
		if !task.submittedAt.IsZero() {
			recordServeDuration(&s.queueWait, now.Sub(task.submittedAt))
		}
		task.startedAt = now
		if len(queue) == 1 {
			delete(s.queues, peerID)
			for i, id := range s.round {
				if id == peerID {
					s.round = append(s.round[:i], s.round[i+1:]...)
					if i < s.next {
						s.next--
					}
					break
				}
			}
		} else {
			s.queues[peerID] = queue[1:]
		}
		s.pending--
		s.active[peerID]++
		s.activeCount++
		if s.nextID == 0 {
			s.nextID = 1
		}
		task.id = s.nextID
		s.nextID++
		if s.running[peerID] == nil {
			s.running[peerID] = make(map[uint64]context.CancelFunc)
		}
		s.running[peerID][task.id] = task.cancel
		return task, true
	}
	return serveTask{}, false
}

func (s *ServeScheduler) finish(task serveTask, completed bool) {
	finishedAt := time.Now()
	task.cancel()
	s.mu.Lock()
	count := s.active[task.peerID]
	if count <= 1 {
		delete(s.active, task.peerID)
	} else {
		s.active[task.peerID] = count - 1
	}
	s.activeCount--
	if runs := s.running[task.peerID]; runs != nil {
		delete(runs, task.id)
		if len(runs) == 0 {
			delete(s.running, task.peerID)
		}
	}
	s.releaseRetainedBytesLocked(task.peerID, task.retainedBytes)
	if !task.startedAt.IsZero() {
		recordServeDuration(&s.service, finishedAt.Sub(task.startedAt))
	}
	if completed {
		s.completed.Add(1)
	} else {
		s.canceled.Add(1)
	}
	s.signalLocked()
	s.mu.Unlock()
}

func (s *ServeScheduler) removePeerLocked(peerID PeerID) {
	for i := 0; i < len(s.round); {
		if s.round[i] != peerID {
			i++
			continue
		}
		s.round = append(s.round[:i], s.round[i+1:]...)
		if i < s.next {
			s.next--
		}
	}
	if s.next < 0 {
		s.next = 0
	}
	if s.next > len(s.round) {
		s.next = len(s.round)
	}
}

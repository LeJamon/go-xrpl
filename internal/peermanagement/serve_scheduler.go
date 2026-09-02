package peermanagement

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
)

// serveScheduler bounds expensive peer-serving work independently from the
// overlay event loop. Jobs are kept in per-peer queues and selected in
// round-robin order. A per-peer active limit prevents one peer from occupying
// every worker while a bounded queue prevents unbounded retention.
type serveScheduler struct {
	mu sync.Mutex

	ctx context.Context

	queues  map[PeerID][]serveTask
	active  map[PeerID]int
	running map[PeerID]map[uint64]context.CancelFunc
	round   []PeerID
	next    int
	nextID  uint64
	pending int

	workers       int
	maxPending    int
	perPeerQueue  int
	perPeerActive int

	notify chan struct{}
	done   chan struct{}
	closed bool
	idle   atomic.Int32

	dropped atomic.Uint64

	onTaskPanic func(PeerID)
}

type serveTask struct {
	peerID  PeerID
	id      uint64
	ctx     context.Context
	cancel  context.CancelFunc
	run     func(context.Context)
	discard func()
}

func newServeScheduler(ctx context.Context, workers, maxPending, perPeerQueue, perPeerActive int) *serveScheduler {
	if ctx == nil {
		ctx = context.Background()
	}
	if workers < 1 {
		workers = 1
	}
	if maxPending < 1 {
		maxPending = 1
	}
	if perPeerQueue < 1 {
		perPeerQueue = 1
	}
	if perPeerActive < 1 {
		perPeerActive = 1
	}
	s := &serveScheduler{
		ctx:           ctx,
		queues:        make(map[PeerID][]serveTask),
		active:        make(map[PeerID]int),
		running:       make(map[PeerID]map[uint64]context.CancelFunc),
		workers:       workers,
		maxPending:    maxPending,
		perPeerQueue:  perPeerQueue,
		perPeerActive: perPeerActive,
		notify:        make(chan struct{}),
		done:          make(chan struct{}),
	}
	go func() {
		<-ctx.Done()
		s.close()
	}()
	return s
}

func (s *serveScheduler) Workers() int { return s.workers }

func (s *serveScheduler) Dropped() uint64 { return s.dropped.Load() }

func (s *serveScheduler) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending
}

// Submit queues a job without waiting for capacity. The request context is
// derived once so queued work can be canceled on peer disconnect or shutdown.
func (s *serveScheduler) Submit(ctx context.Context, peerID PeerID, run func(context.Context)) bool {
	return s.SubmitOwned(ctx, peerID, run, nil)
}

// SubmitOwned queues work whose caller retains resources until the task runs
// or is discarded. A rejected submission remains owned by the caller.
func (s *serveScheduler) SubmitOwned(
	ctx context.Context,
	peerID PeerID,
	run func(context.Context),
	discard func(),
) bool {
	if run == nil {
		return false
	}
	if ctx == nil {
		ctx = s.ctx
	}
	jobCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.closed || s.pending >= s.maxPending {
		s.mu.Unlock()
		cancel()
		s.dropped.Add(1)
		return false
	}
	queue := s.queues[peerID]
	if len(queue) >= s.perPeerQueue {
		s.mu.Unlock()
		cancel()
		s.dropped.Add(1)
		return false
	}
	if len(queue) == 0 {
		s.round = append(s.round, peerID)
	}
	queue = append(queue, serveTask{
		peerID:  peerID,
		ctx:     jobCtx,
		cancel:  cancel,
		run:     run,
		discard: discard,
	})
	s.queues[peerID] = queue
	s.pending++
	s.signalLocked()
	s.mu.Unlock()
	return true
}

// CancelPeer disposes queued work and cancels work currently executing for a
// peer. It is safe to call after the peer has already been removed.
func (s *serveScheduler) CancelPeer(peerID PeerID) {
	s.mu.Lock()
	queue := s.queues[peerID]
	delete(s.queues, peerID)
	s.pending -= len(queue)
	for _, task := range queue {
		task.cancel()
	}
	// Running tasks are represented by a context in the activeRuns map. The
	// scheduler intentionally keeps this separate from active counts so that
	// finishing a canceled task still releases exactly one slot.
	if runs := s.running[peerID]; len(runs) != 0 {
		for _, cancel := range runs {
			cancel()
		}
	}
	s.removePeerLocked(peerID)
	s.signalLocked()
	s.mu.Unlock()
	for _, task := range queue {
		task.discardTask()
	}
}

// Close cancels all queued and running work. It is idempotent.
func (s *serveScheduler) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	queued := make([]serveTask, 0, s.pending)
	for peerID, queue := range s.queues {
		for _, task := range queue {
			task.cancel()
		}
		queued = append(queued, queue...)
		delete(s.queues, peerID)
	}
	s.pending = 0
	for _, runs := range s.running {
		for _, cancel := range runs {
			cancel()
		}
	}
	s.round = nil
	s.next = 0
	close(s.notify)
	s.mu.Unlock()
	for _, task := range queued {
		task.discardTask()
	}
}

// signalLocked wakes every worker currently waiting for scheduler state. A
// fresh channel is installed while holding s.mu, so a worker that observes an
// empty queue cannot miss a concurrent enqueue between its state check and
// entering the wait select.
func (s *serveScheduler) signalLocked() {
	if s.closed {
		return
	}
	close(s.notify)
	s.notify = make(chan struct{})
}

// Run starts the fixed worker set and returns when the scheduler context is
// canceled or all workers have exited. Overlay.Run owns the returned error.
func (s *serveScheduler) Run(ctx context.Context) error {
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
	<-ctx.Done()
	s.close()
	wg.Wait()
	return ctx.Err()
}

func (s *serveScheduler) worker(ctx context.Context) {
	for {
		task, ok := s.take(ctx)
		if !ok {
			return
		}
		s.runTask(task)
	}
}

func (s *serveScheduler) runTask(task serveTask) {
	defer s.finish(task)
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
	if task.ctx.Err() == nil {
		task.run(task.ctx)
	} else {
		task.discardTask()
	}
}

func (t serveTask) discardTask() {
	if t.discard != nil {
		t.discard()
	}
}

func (s *serveScheduler) take(ctx context.Context) (serveTask, bool) {
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

func (s *serveScheduler) takeLocked() (serveTask, bool) {
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

func (s *serveScheduler) finish(task serveTask) {
	task.cancel()
	s.mu.Lock()
	if count := s.active[task.peerID]; count <= 1 {
		delete(s.active, task.peerID)
	} else {
		s.active[task.peerID] = count - 1
	}
	if runs := s.running[task.peerID]; runs != nil {
		delete(runs, task.id)
	}
	if len(s.running[task.peerID]) == 0 {
		delete(s.running, task.peerID)
	}
	s.signalLocked()
	s.mu.Unlock()
}

func (s *serveScheduler) removePeerLocked(peerID PeerID) {
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

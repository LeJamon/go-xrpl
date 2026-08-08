package node

import (
	"context"
	"errors"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

const (
	defaultPeerConnectWorkers = 2
	defaultPeerConnectQueue   = 16
)

// peerConnectScheduler owns all outbound connect work requested through RPC.
// Admission is deliberately non-blocking: callers either observe a duplicate
// success, reserve a bounded queue slot, or receive an admission error.
type peerConnectScheduler struct {
	ctx            context.Context
	cancel         context.CancelFunc
	dial           func(context.Context, string) error
	observeFailure func(string, error)

	queue chan string
	wg    sync.WaitGroup

	mu      sync.Mutex
	closed  bool
	pending map[string]struct{}
}

// newPeerConnectScheduler starts a fixed worker set. A nil dial function makes
// the scheduler unavailable and is rejected at admission rather than causing a
// worker panic.
func newPeerConnectScheduler(
	parent context.Context,
	dial func(context.Context, string) error,
	workers, queueSize int,
	observeFailure func(string, error),
) *peerConnectScheduler {
	if parent == nil {
		parent = context.Background()
	}
	if workers <= 0 {
		workers = defaultPeerConnectWorkers
	}
	if queueSize <= 0 {
		queueSize = defaultPeerConnectQueue
	}
	ctx, cancel := context.WithCancel(parent)
	s := &peerConnectScheduler{
		ctx:            ctx,
		cancel:         cancel,
		dial:           dial,
		observeFailure: observeFailure,
		queue:          make(chan string, queueSize),
		pending:        make(map[string]struct{}, workers+queueSize),
	}
	s.wg.Add(workers)
	for range workers {
		go s.worker()
	}
	return s
}

// Enqueue reserves addr for a worker and returns immediately. An address that
// is already queued or running is an idempotent success.
func (s *peerConnectScheduler) Enqueue(addr string) error {
	if s == nil {
		return types.ErrPeerConnectUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return types.ErrPeerConnectClosed
	}
	if s.dial == nil {
		return types.ErrPeerConnectUnavailable
	}
	if _, exists := s.pending[addr]; exists {
		return nil
	}
	s.pending[addr] = struct{}{}
	select {
	case s.queue <- addr:
		return nil
	default:
		delete(s.pending, addr)
		return types.ErrPeerConnectQueueFull
	}
}

func (s *peerConnectScheduler) worker() {
	defer s.wg.Done()
	for {
		if s.ctx.Err() != nil {
			return
		}
		select {
		case <-s.ctx.Done():
			return
		case addr := <-s.queue:
			if s.ctx.Err() != nil {
				return
			}
			err := s.dial(s.ctx, addr)
			s.complete(addr)
			if err != nil && s.ctx.Err() == nil && !errors.Is(err, context.Canceled) {
				if s.observeFailure != nil {
					s.observeFailure(addr, err)
				}
			}
		}
	}
}

func (s *peerConnectScheduler) complete(addr string) {
	s.mu.Lock()
	delete(s.pending, addr)
	s.mu.Unlock()
}

// Close stops admission, cancels queued/running work, and joins every worker.
// The queue is intentionally not closed: admission and Close can race safely
// while workers select on the cancellation context.
func (s *peerConnectScheduler) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.mu.Lock()
	s.pending = make(map[string]struct{})
	s.mu.Unlock()
}

package adaptor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/shamap"
)

const (
	acquisitionStoreQueueDepth   = 2
	acquisitionStoreDrainTimeout = time.Second
)

type acquisitionStoreJob struct {
	entries []shamap.FlushEntry
	barrier chan error
	owner   *acquisitionStoreScope
}

type acquisitionStoreScope struct {
	lane *acquisitionStoreLane
	id   uint64

	admission sync.Mutex
	mu        sync.Mutex
	err       error
	promoted  bool
	retired   bool
}

func (s *acquisitionStoreScope) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	s.mu.Lock()
	durableOnly := s.promoted || s.retired
	s.mu.Unlock()
	if durableOnly {
		return s.lane.FetchDurable(ctx, hash)
	}
	return s.lane.fetchScope(ctx, s.id, hash)
}

func (s *acquisitionStoreScope) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	return s.lane.FetchDurable(ctx, hash)
}

func (s *acquisitionStoreScope) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	s.admission.Lock()
	defer s.admission.Unlock()

	s.mu.Lock()
	retired := s.retired
	promoted := s.promoted
	s.mu.Unlock()
	if retired {
		return errors.New("acquisition persistence scope retired")
	}
	if promoted {
		return s.lane.base.StoreBatch(ctx, entries)
	}
	return s.lane.storeBatch(ctx, s, entries)
}

func (s *acquisitionStoreScope) Flush(ctx context.Context) error {
	return s.lane.flushScope(ctx, s)
}

func (s *acquisitionStoreScope) Retire(context.Context) error {
	s.mu.Lock()
	s.retired = true
	s.promoted = false
	s.err = nil
	s.mu.Unlock()
	return nil
}

func (s *acquisitionStoreScope) Promote(ctx context.Context) error {
	s.admission.Lock()
	defer s.admission.Unlock()

	s.mu.Lock()
	if s.retired {
		s.mu.Unlock()
		return errors.New("acquisition persistence scope retired")
	}
	s.mu.Unlock()

	if err := s.lane.flushScope(ctx, s); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retired {
		return errors.New("acquisition persistence scope retired")
	}
	s.promoted = true
	s.err = nil
	return nil
}

func (s *acquisitionStoreScope) FullBelowCache() *shamap.FullBelowCache {
	return s.lane.FullBelowCache()
}

func (s *acquisitionStoreScope) recordFailure(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.retired && !s.promoted && s.err == nil {
		s.err = err
	}
}

func (s *acquisitionStoreScope) takeFailure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.err
	s.err = nil
	return err
}

type pendingAcquisitionNode struct {
	data []byte
	refs uint32
}

type pendingAcquisitionKey struct {
	scope uint64
	hash  [32]byte
}

// acquisitionStoreLane keeps verified inbound nodes immediately readable while
// a single bounded worker persists them in arrival order.
type acquisitionStoreLane struct {
	base      shamap.Family
	logger    *slog.Logger
	fullBelow *shamap.FullBelowCache

	lifecycleMu  sync.RWMutex
	jobs         chan acquisitionStoreJob
	done         chan struct{}
	ctx          context.Context
	cancel       context.CancelFunc
	drainTimeout time.Duration
	nextScope    atomic.Uint64
	unscoped     *acquisitionStoreScope

	pendingMu sync.RWMutex
	pending   map[pendingAcquisitionKey]pendingAcquisitionNode
}

func newAcquisitionStoreLane(base shamap.Family, logger *slog.Logger, queueDepth int) *acquisitionStoreLane {
	if base == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	if queueDepth < 0 {
		queueDepth = 0
	}
	cache := shamap.NewFullBelowCache()
	if provider, ok := base.(interface {
		FullBelowCache() *shamap.FullBelowCache
	}); ok && provider.FullBelowCache() != nil {
		cache = provider.FullBelowCache()
	}
	lane := &acquisitionStoreLane{
		base:         base,
		logger:       logger,
		fullBelow:    cache,
		drainTimeout: acquisitionStoreDrainTimeout,
		pending:      make(map[pendingAcquisitionKey]pendingAcquisitionNode),
		jobs:         make(chan acquisitionStoreJob, queueDepth),
	}
	lane.unscoped = &acquisitionStoreScope{lane: lane}
	return lane
}

func (l *acquisitionStoreLane) FullBelowCache() *shamap.FullBelowCache {
	return l.fullBelow
}

func (l *acquisitionStoreLane) scope() shamap.Family {
	return &acquisitionStoreScope{lane: l, id: l.nextScope.Add(1)}
}

func (l *acquisitionStoreLane) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	return l.fetchScope(ctx, 0, hash)
}

func (l *acquisitionStoreLane) fetchScope(ctx context.Context, scope uint64, hash [32]byte) ([]byte, error) {
	key := pendingAcquisitionKey{scope: scope, hash: hash}
	l.pendingMu.RLock()
	pending, ok := l.pending[key]
	if ok {
		data := bytes.Clone(pending.data)
		l.pendingMu.RUnlock()
		return data, nil
	}
	l.pendingMu.RUnlock()
	return l.FetchDurable(ctx, hash)
}

func (l *acquisitionStoreLane) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	if durable, ok := l.base.(interface {
		FetchDurable(context.Context, [32]byte) ([]byte, error)
	}); ok {
		return durable.FetchDurable(ctx, hash)
	}
	return l.base.Fetch(ctx, hash)
}

func (l *acquisitionStoreLane) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	return l.storeBatch(ctx, l.unscoped, entries)
}

func (l *acquisitionStoreLane) storeBatch(ctx context.Context, owner *acquisitionStoreScope, entries []shamap.FlushEntry) error {
	if len(entries) == 0 {
		return nil
	}

	l.lifecycleMu.RLock()
	jobs := l.jobs
	laneCtx := l.ctx
	if l.done == nil {
		l.lifecycleMu.RUnlock()
		return l.base.StoreBatch(ctx, entries)
	}

	started := time.Now()
	job := acquisitionStoreJob{entries: cloneAcquisitionEntries(entries), owner: owner}
	l.addPending(owner.id, job.entries)
	select {
	case jobs <- job:
		if elapsed := time.Since(started); elapsed >= time.Second {
			l.logger.Info("inbound ledger: persistence enqueue delayed", "entries", len(entries), "elapsed", elapsed)
		}
		l.lifecycleMu.RUnlock()
		return nil
	case <-ctx.Done():
		l.removePending(owner.id, job.entries)
		l.lifecycleMu.RUnlock()
		return ctx.Err()
	case <-laneCtx.Done():
		l.removePending(owner.id, job.entries)
		l.lifecycleMu.RUnlock()
		return laneCtx.Err()
	}
}

func (l *acquisitionStoreLane) flush(ctx context.Context) error {
	return l.flushJob(ctx, l.unscoped)
}

func (l *acquisitionStoreLane) flushScope(ctx context.Context, scope *acquisitionStoreScope) error {
	return l.flushJob(ctx, scope)
}

func (l *acquisitionStoreLane) flushJob(ctx context.Context, owner *acquisitionStoreScope) error {
	l.lifecycleMu.RLock()
	if l.done == nil {
		l.lifecycleMu.RUnlock()
		return nil
	}
	laneCtx := l.ctx
	done := make(chan error, 1)
	job := acquisitionStoreJob{owner: owner, barrier: done}
	select {
	case l.jobs <- job:
	case <-ctx.Done():
		l.lifecycleMu.RUnlock()
		return ctx.Err()
	case <-laneCtx.Done():
		l.lifecycleMu.RUnlock()
		return laneCtx.Err()
	}
	select {
	case err := <-done:
		l.lifecycleMu.RUnlock()
		return err
	case <-ctx.Done():
		l.lifecycleMu.RUnlock()
		return ctx.Err()
	case <-laneCtx.Done():
		l.lifecycleMu.RUnlock()
		return laneCtx.Err()
	}
}

func (l *acquisitionStoreLane) start(parent context.Context) {
	l.lifecycleMu.Lock()
	defer l.lifecycleMu.Unlock()
	if l.done != nil {
		return
	}
	l.done = make(chan struct{})
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	l.ctx = ctx
	l.cancel = cancel
	jobs := l.jobs
	done := l.done
	go l.run(ctx, jobs, done)
}

func (l *acquisitionStoreLane) stopDrain() {
	l.lifecycleMu.RLock()
	if l.done == nil {
		l.lifecycleMu.RUnlock()
		return
	}
	done := l.done
	cancel := l.cancel
	l.lifecycleMu.RUnlock()
	drainCtx, stopDrain := context.WithTimeout(context.Background(), l.drainTimeout)
	if err := l.flush(drainCtx); err != nil && !errors.Is(err, context.Canceled) {
		l.logger.Warn("inbound ledger: persistence drain incomplete", "error", err)
	}
	stopDrain()
	cancel()
	<-done
	l.lifecycleMu.Lock()
	if l.done != done {
		l.lifecycleMu.Unlock()
		return
	}
	l.jobs = make(chan acquisitionStoreJob, cap(l.jobs))
	l.done = nil
	l.ctx = nil
	l.cancel = nil
	l.lifecycleMu.Unlock()
	l.pendingMu.Lock()
	l.pending = make(map[pendingAcquisitionKey]pendingAcquisitionNode)
	l.pendingMu.Unlock()
}

func (l *acquisitionStoreLane) run(ctx context.Context, jobs <-chan acquisitionStoreJob, done chan<- struct{}) {
	defer close(done)
	for {
		var job acquisitionStoreJob
		select {
		case <-ctx.Done():
			return
		case job = <-jobs:
		}
		if job.barrier != nil {
			err := job.owner.takeFailure()
			select {
			case job.barrier <- err:
			case <-ctx.Done():
				return
			}
			continue
		}
		if err := l.base.StoreBatch(ctx, job.entries); err != nil {
			l.logger.Warn("inbound ledger: failed to persist verified nodes",
				"entries", len(job.entries), "error", err)
			job.owner.recordFailure(err)
		}
		l.removePending(job.owner.id, job.entries)
	}
}

func (l *acquisitionStoreLane) addPending(scope uint64, entries []shamap.FlushEntry) {
	l.pendingMu.Lock()
	defer l.pendingMu.Unlock()
	for i := range entries {
		key := pendingAcquisitionKey{scope: scope, hash: entries[i].Hash}
		pending := l.pending[key]
		if pending.refs == 0 {
			pending.data = entries[i].Data
		}
		pending.refs++
		l.pending[key] = pending
	}
}

func (l *acquisitionStoreLane) removePending(scope uint64, entries []shamap.FlushEntry) {
	l.pendingMu.Lock()
	defer l.pendingMu.Unlock()
	for i := range entries {
		key := pendingAcquisitionKey{scope: scope, hash: entries[i].Hash}
		pending, ok := l.pending[key]
		if !ok {
			continue
		}
		if pending.refs <= 1 {
			delete(l.pending, key)
			continue
		}
		pending.refs--
		l.pending[key] = pending
	}
}

func cloneAcquisitionEntries(entries []shamap.FlushEntry) []shamap.FlushEntry {
	cloned := make([]shamap.FlushEntry, len(entries))
	for i := range entries {
		cloned[i] = entries[i]
		cloned[i].Data = bytes.Clone(entries[i].Data)
	}
	return cloned
}

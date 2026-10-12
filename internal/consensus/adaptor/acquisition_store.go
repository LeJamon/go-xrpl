package adaptor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/LeJamon/go-xrpl/shamap"
)

const (
	acquisitionStoreQueueDepth   = 2
	acquisitionStoreDrainTimeout = time.Second
)

type acquisitionStoreJob struct {
	entries    []shamap.FlushEntry
	barrier    chan error
	completion chan error
	ctx        context.Context
	owner      *acquisitionStoreScope
	bytes      uint64
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
	promoted, retired := s.promoted, s.retired
	s.mu.Unlock()
	if promoted {
		// Promotion has drained this scope's writes. Descendant live ledgers
		// retain this Family, so ordinary reads must use the normal NodeStore
		// cache, not the acquisition verifier's uncached read path. Placement
		// and explicit durability reads keep their separate rules below.
		return s.lane.base.Fetch(ctx, hash)
	}
	if retired {
		return s.lane.FetchDurable(ctx, hash)
	}
	if data, ok := s.lane.fetchPending(s.id, hash); ok {
		return data, nil
	}
	return s.FetchDurable(ctx, hash)
}

func (s *acquisitionStoreScope) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	if data, err := s.lane.FetchCached(ctx, hash); err != nil || data != nil {
		return data, err
	}
	return s.lane.FetchDurable(ctx, hash)
}

func (s *acquisitionStoreScope) FetchForNodePlacement(ctx context.Context, hash [32]byte) ([]byte, error) {
	s.mu.Lock()
	durableOnly := s.promoted || s.retired
	s.mu.Unlock()
	if !durableOnly {
		if data, ok := s.lane.fetchPending(s.id, hash); ok {
			return data, nil
		}
	}
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
	s.admission.Lock()
	defer s.admission.Unlock()

	if err := s.lane.flushScope(ctx, s); err != nil {
		return err
	}
	return nil
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

func (s *acquisitionStoreScope) AcquireDurableSnapshot(ctx context.Context) ([32]byte, func(), error) {
	return s.lane.AcquireDurableSnapshot(ctx)
}

func (s *acquisitionStoreScope) recordFailure(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.retired && !s.promoted && s.err == nil {
		s.err = err
	}
}

func (s *acquisitionStoreScope) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
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
	memory    *shamap.AcquisitionBudget
	memoryMu  sync.Mutex
	bytes     atomic.Uint64
	peakBytes atomic.Uint64

	lifecycleMu  sync.RWMutex
	jobs         chan acquisitionStoreJob
	done         chan struct{}
	ctx          context.Context
	cancel       context.CancelFunc
	drainTimeout time.Duration
	nextScope    atomic.Uint64
	unscoped     *acquisitionStoreScope

	pendingMu       sync.RWMutex
	pending         map[pendingAcquisitionKey]pendingAcquisitionNode
	pendingReleased uint64
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
		memory:       shamap.NewAcquisitionBudget(0),
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

func (l *acquisitionStoreLane) AcquireDurableSnapshot(ctx context.Context) ([32]byte, func(), error) {
	if durable, ok := l.base.(interface {
		AcquireDurableSnapshot(context.Context) ([32]byte, func(), error)
	}); ok {
		return durable.AcquireDurableSnapshot(ctx)
	}
	return [32]byte{}, nil, errors.ErrUnsupported
}

func (l *acquisitionStoreLane) scope() shamap.Family {
	return &acquisitionStoreScope{lane: l, id: l.nextScope.Add(1)}
}

func (l *acquisitionStoreLane) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	return l.fetchScope(ctx, 0, hash)
}

func (l *acquisitionStoreLane) fetchScope(ctx context.Context, scope uint64, hash [32]byte) ([]byte, error) {
	if data, ok := l.fetchPending(scope, hash); ok {
		return data, nil
	}
	return l.FetchDurable(ctx, hash)
}

func (l *acquisitionStoreLane) fetchPending(scope uint64, hash [32]byte) ([]byte, bool) {
	key := pendingAcquisitionKey{scope: scope, hash: hash}
	l.pendingMu.RLock()
	pending, ok := l.pending[key]
	if ok {
		data := bytes.Clone(pending.data)
		l.pendingMu.RUnlock()
		return data, true
	}
	l.pendingMu.RUnlock()
	return nil, false
}

func (l *acquisitionStoreLane) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	if durable, ok := l.base.(interface {
		FetchDurable(context.Context, [32]byte) ([]byte, error)
	}); ok {
		return durable.FetchDurable(ctx, hash)
	}
	return l.base.Fetch(ctx, hash)
}

func (l *acquisitionStoreLane) FetchCached(ctx context.Context, hash [32]byte) ([]byte, error) {
	if cached, ok := l.base.(interface {
		FetchCached(context.Context, [32]byte) ([]byte, error)
	}); ok {
		return cached.FetchCached(ctx, hash)
	}
	return nil, nil
}

func (l *acquisitionStoreLane) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	return l.storeBatch(ctx, l.unscoped, entries)
}

func (l *acquisitionStoreLane) storeBatch(ctx context.Context, owner *acquisitionStoreScope, entries []shamap.FlushEntry) error {
	if len(entries) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	charge := acquisitionEntryBytes(entries)
	if !l.reserveBytes(charge) {
		if err := l.flushScope(ctx, owner); err != nil {
			return err
		}
		if !l.reserveBytes(charge) {
			return l.storeBorrowed(ctx, owner, entries)
		}
	}

	l.lifecycleMu.RLock()
	jobs := l.jobs
	laneCtx := l.ctx
	if l.done == nil {
		l.lifecycleMu.RUnlock()
		l.releaseBytes(charge)
		return l.storeBorrowed(ctx, owner, entries)
	}

	started := time.Now()
	job := acquisitionStoreJob{entries: cloneAcquisitionEntries(entries), owner: owner, bytes: charge}
	l.addPending(owner.id, job.entries)
	select {
	case jobs <- job:
		if elapsed := time.Since(started); elapsed >= time.Second {
			l.logger.Info("inbound ledger: persistence enqueue delayed", "entries", len(entries), "elapsed", elapsed)
		}
		l.lifecycleMu.RUnlock()
		return nil
	case <-ctx.Done():
		l.releaseBytes(l.removePending(owner.id, job.entries, job.bytes))
		l.lifecycleMu.RUnlock()
		return ctx.Err()
	case <-laneCtx.Done():
		l.releaseBytes(l.removePending(owner.id, job.entries, job.bytes))
		l.lifecycleMu.RUnlock()
		return laneCtx.Err()
	}
}

func (l *acquisitionStoreLane) storeBorrowed(ctx context.Context, owner *acquisitionStoreScope, entries []shamap.FlushEntry) error {
	l.lifecycleMu.RLock()
	if l.done == nil {
		l.lifecycleMu.RUnlock()
		err := l.base.StoreBatch(ctx, entries)
		if err != nil {
			owner.recordFailure(err)
		}
		return err
	}
	done := make(chan error, 1)
	job := acquisitionStoreJob{entries: entries, owner: owner, completion: done, ctx: ctx}
	select {
	case l.jobs <- job:
		l.lifecycleMu.RUnlock()
	case <-ctx.Done():
		l.lifecycleMu.RUnlock()
		return ctx.Err()
	case <-l.ctx.Done():
		err := l.ctx.Err()
		l.lifecycleMu.RUnlock()
		return err
	}
	// The caller owns these slices until the worker finishes or discards them.
	return <-done
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
		return owner.failure()
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
	for len(l.jobs) != 0 {
		job := <-l.jobs
		l.finishJob(job, context.Canceled)
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
	defer func() {
		for {
			select {
			case job := <-jobs:
				l.finishJob(job, ctx.Err())
			default:
				return
			}
		}
	}()
	for {
		var job acquisitionStoreJob
		select {
		case <-ctx.Done():
			return
		case job = <-jobs:
		}
		if job.barrier != nil {
			err := job.owner.failure()
			select {
			case job.barrier <- err:
			case <-ctx.Done():
				return
			}
			continue
		}
		writeCtx := ctx
		cleanup := func() {}
		if job.ctx != nil {
			var cancel context.CancelFunc
			writeCtx, cancel = context.WithCancel(job.ctx)
			stop := context.AfterFunc(ctx, cancel)
			cleanup = func() { stop(); cancel() }
		}
		err := l.base.StoreBatch(writeCtx, job.entries)
		cleanup()
		if err != nil {
			l.logger.Warn("inbound ledger: failed to persist verified nodes",
				"entries", len(job.entries), "error", err)
			job.owner.recordFailure(err)
		}
		if err == nil {
			err = job.owner.failure()
		}
		l.finishJob(job, err)
	}
}

func (l *acquisitionStoreLane) finishJob(job acquisitionStoreJob, err error) {
	if job.bytes != 0 {
		l.releaseBytes(l.removePending(job.owner.id, job.entries, job.bytes))
	}
	if job.completion != nil {
		job.completion <- err
	}
}

func (l *acquisitionStoreLane) reserveBytes(size uint64) bool {
	l.memoryMu.Lock()
	defer l.memoryMu.Unlock()
	used := l.bytes.Load()
	limit := l.memory.Snapshot().Limit / 8
	if size > limit || used > limit-size || !l.memory.TryReserve(size) {
		return false
	}
	l.bytes.Store(used + size)
	if used+size > l.peakBytes.Load() {
		l.peakBytes.Store(used + size)
	}
	return true
}

func (l *acquisitionStoreLane) releaseBytes(size uint64) {
	if size == 0 {
		return
	}
	l.memoryMu.Lock()
	defer l.memoryMu.Unlock()
	used := l.bytes.Load()
	if size > used {
		panic("acquisition persistence reservation underflow")
	}
	l.bytes.Store(used - size)
	l.memory.Release(size)
}

func acquisitionEntryBytes(entries []shamap.FlushEntry) uint64 {
	entryBytes := uint64(unsafe.Sizeof(shamap.FlushEntry{}) + 2*(unsafe.Sizeof(pendingAcquisitionKey{})+unsafe.Sizeof(pendingAcquisitionNode{})))
	var size uint64
	for i := range entries {
		size += entryBytes + 2*uint64(len(entries[i].Data))
	}
	return size
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

func (l *acquisitionStoreLane) removePending(scope uint64, entries []shamap.FlushEntry, charge uint64) uint64 {
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
	// Go maps retain their buckets after deletion. Keep completed jobs charged
	// until the index can be discarded; reaching the staging cap forces a flush.
	l.pendingReleased += charge
	if len(l.pending) != 0 {
		return 0
	}
	l.pending = make(map[pendingAcquisitionKey]pendingAcquisitionNode)
	released := l.pendingReleased
	l.pendingReleased = 0
	return released
}

func cloneAcquisitionEntries(entries []shamap.FlushEntry) []shamap.FlushEntry {
	cloned := make([]shamap.FlushEntry, len(entries))
	for i := range entries {
		cloned[i] = entries[i]
		cloned[i].Data = bytes.Clone(entries[i].Data)
	}
	return cloned
}

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
	// acquisitionStoreQueueDepth is retained as the constructor argument used by
	// the router. Admission is bounded by bytes below rather than by a number of
	// jobs, so a large reply cannot consume the queue disproportionately.
	// A negative depth selects the byte-bounded production queue. Non-negative
	// values remain supported for focused callers that deliberately request the
	// legacy job-count bound.
	acquisitionStoreQueueDepth   = -1
	acquisitionStoreDrainTimeout = time.Second

	// A reply is split before admission so one large peer response cannot hold
	// the lane behind a backend batch that is needlessly large. The metadata
	// charge is deliberately fixed: it covers the FlushEntry and queue/job
	// bookkeeping that remains resident while a write is queued or in flight.
	acquisitionStoreBatchBytes    int64 = 1 << 20
	acquisitionStoreBatchNodes          = 512
	acquisitionStoreQueueBytes    int64 = 32 << 20
	acquisitionStoreEntryMetadata int64 = 64
	acquisitionStoreJobMetadata   int64 = 64
)

type acquisitionStoreJob struct {
	entries       []shamap.FlushEntry
	barrier       chan error
	owner         *acquisitionStoreScope
	accountedByte int64
	legacyDepth   bool
}

type acquisitionStoreWaiter struct {
	job   acquisitionStoreJob
	bytes int64
}

type acquisitionStoreScope struct {
	lane *acquisitionStoreLane
	id   uint64

	admission sync.Mutex
	mu        sync.Mutex
	err       error
	promoted  bool
	retired   bool
	retiredCh chan struct{}
}

func (s *acquisitionStoreScope) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	s.mu.Lock()
	durableOnly := s.promoted || s.retired
	s.mu.Unlock()
	if durableOnly {
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

// StoreBatch keeps the Family contract defensive for callers that retain or
// mutate their entries after this method returns.
func (s *acquisitionStoreScope) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	s.admission.Lock()
	defer s.admission.Unlock()
	return s.storeBatch(ctx, entries, false)
}

// StoreBatchOwned is used only for freshly serialized inbound entries. The
// caller transfers ownership of each Data buffer; the lane consumes it after
// the backend call, including failed or canceled admission.
func (s *acquisitionStoreScope) StoreBatchOwned(ctx context.Context, entries []shamap.FlushEntry) error {
	s.admission.Lock()
	defer s.admission.Unlock()
	return s.storeBatch(ctx, entries, true)
}

func (s *acquisitionStoreScope) storeBatch(ctx context.Context, entries []shamap.FlushEntry, owned bool) error {
	s.mu.Lock()
	retired := s.retired
	promoted := s.promoted
	s.mu.Unlock()
	if retired {
		if owned {
			consumeAcquisitionEntries(entries)
		}
		return errors.New("acquisition persistence scope retired")
	}
	if promoted {
		stored := entries
		if !owned {
			stored = cloneAcquisitionEntries(entries)
		}
		err := s.lane.base.StoreBatch(ctx, stored)
		if owned {
			consumeAcquisitionEntries(entries)
		}
		return err
	}
	return s.lane.storeBatch(ctx, s, entries, owned)
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
	if !s.retired {
		s.retired = true
		s.promoted = false
		s.err = nil
		close(s.retiredCh)
	}
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

// acquisitionStoreSnapshot is a point-in-time view of the lane's bounded
// persistence pipeline. The counters are cumulative except for CurrentBytes.
type acquisitionStoreSnapshot struct {
	PendingBytes   int64
	CurrentBytes   int64
	PeakBytes      int64
	QueueWaits     uint64
	QueueWait      time.Duration
	EntriesWritten uint64
	BytesWritten   uint64
	StoreLatency   time.Duration
	StoreFailures  uint64
}

// acquisitionStoreLane keeps verified inbound nodes immediately readable while
// a single FIFO worker persists independently scoped microbatches. Queue bytes
// include the in-flight batch, so the resident persistence footprint remains
// bounded even when the backend is slow.
type acquisitionStoreLane struct {
	base      shamap.Family
	logger    *slog.Logger
	fullBelow *shamap.FullBelowCache

	lifecycleMu  sync.RWMutex
	done         chan struct{}
	ctx          context.Context
	cancel       context.CancelFunc
	drainTimeout time.Duration
	nextScope    atomic.Uint64
	unscoped     *acquisitionStoreScope

	queueMu       sync.Mutex
	jobs          []acquisitionStoreJob
	waiters       []*acquisitionStoreWaiter
	reservedBytes int64
	legacyJobs    int
	legacyDepth   int
	wake          chan struct{}

	pendingMu sync.RWMutex
	pending   map[pendingAcquisitionKey]pendingAcquisitionNode

	currentBytes   atomic.Int64
	pendingBytes   atomic.Int64
	peakBytes      atomic.Int64
	queueWaits     atomic.Uint64
	queueWaitNanos atomic.Int64
	entriesWritten atomic.Uint64
	bytesWritten   atomic.Uint64
	storeLatencyNs atomic.Int64
	storeFailures  atomic.Uint64
}

func newAcquisitionStoreLane(base shamap.Family, logger *slog.Logger, queueDepth int) *acquisitionStoreLane {
	if base == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
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
		legacyDepth:  queueDepth,
		wake:         make(chan struct{}),
	}
	lane.unscoped = &acquisitionStoreScope{
		lane:      lane,
		retiredCh: make(chan struct{}),
	}
	return lane
}

func (l *acquisitionStoreLane) FullBelowCache() *shamap.FullBelowCache {
	return l.fullBelow
}

func (l *acquisitionStoreLane) scope() shamap.Family {
	return &acquisitionStoreScope{
		lane:      l,
		id:        l.nextScope.Add(1),
		retiredCh: make(chan struct{}),
	}
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
	return l.storeBatch(ctx, l.unscoped, entries, false)
}

// StoreBatchOwned is the lane-level counterpart used by inbound callers that
// have freshly serialized immutable entries and can transfer their buffers.
func (l *acquisitionStoreLane) StoreBatchOwned(ctx context.Context, entries []shamap.FlushEntry) error {
	return l.storeBatch(ctx, l.unscoped, entries, true)
}

func (l *acquisitionStoreLane) storeBatch(ctx context.Context, owner *acquisitionStoreScope, entries []shamap.FlushEntry, owned bool) error {
	if len(entries) == 0 {
		return nil
	}

	if !owned {
		entries = cloneAcquisitionEntries(entries)
	}

	l.lifecycleMu.RLock()
	laneCtx := l.ctx
	running := l.done != nil
	if !running {
		l.lifecycleMu.RUnlock()
		err := l.base.StoreBatch(ctx, entries)
		if owned {
			consumeAcquisitionEntries(entries)
		}
		return err
	}
	l.lifecycleMu.RUnlock()

	l.addPending(owner.id, entries)
	for start := 0; start < len(entries); {
		end := acquisitionStoreBatchEnd(entries, start)
		if err := l.enqueue(ctx, laneCtx, owner, acquisitionStoreJob{
			entries:     entries[start:end],
			owner:       owner,
			legacyDepth: owner == l.unscoped && !owned,
		}, false); err != nil {
			l.removePending(owner.id, entries[start:])
			if owned {
				consumeAcquisitionEntries(entries[start:])
			}
			return err
		}
		start = end
	}
	return nil
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
	err := l.enqueue(ctx, laneCtx, owner, acquisitionStoreJob{owner: owner, barrier: done}, true)
	if err != nil {
		l.lifecycleMu.RUnlock()
		return err
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
	done := l.done
	go l.run(ctx, done)
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
	l.signalQueue()
	<-done
	l.lifecycleMu.Lock()
	if l.done != done {
		l.lifecycleMu.Unlock()
		return
	}
	l.done = nil
	l.ctx = nil
	l.cancel = nil
	l.lifecycleMu.Unlock()
	l.pendingMu.Lock()
	l.pending = make(map[pendingAcquisitionKey]pendingAcquisitionNode)
	l.pendingMu.Unlock()
}

func (l *acquisitionStoreLane) run(ctx context.Context, done chan<- struct{}) {
	defer func() {
		l.dropQueued()
		close(done)
	}()
	for {
		job, ok := l.nextJob(ctx)
		if !ok {
			return
		}
		if job.barrier != nil {
			l.release(job)
			err := job.owner.failure()
			select {
			case job.barrier <- err:
			case <-ctx.Done():
				return
			}
			continue
		}

		started := time.Now()
		err := l.base.StoreBatch(ctx, job.entries)
		elapsed := time.Since(started)
		l.storeLatencyNs.Add(int64(elapsed))
		if err != nil {
			l.storeFailures.Add(1)
			l.logger.Warn("inbound ledger: failed to persist verified nodes",
				"entries", len(job.entries), "error", err)
			job.owner.recordFailure(err)
		} else {
			l.entriesWritten.Add(uint64(len(job.entries)))
			var bytesWritten uint64
			for i := range job.entries {
				bytesWritten += uint64(len(job.entries[i].Data))
			}
			l.bytesWritten.Add(bytesWritten)
		}
		l.release(job)
		l.removePending(job.owner.id, job.entries)
		consumeAcquisitionEntries(job.entries)
	}
}

func (l *acquisitionStoreLane) nextJob(ctx context.Context) (acquisitionStoreJob, bool) {
	for {
		l.queueMu.Lock()
		if len(l.jobs) > 0 {
			if ctx.Err() != nil {
				l.queueMu.Unlock()
				return acquisitionStoreJob{}, false
			}
			job := l.jobs[0]
			l.jobs[0] = acquisitionStoreJob{}
			l.jobs = l.jobs[1:]
			if len(l.jobs) == 0 {
				l.jobs = nil
			}
			l.pendingBytes.Add(-job.accountedByte)
			l.queueMu.Unlock()
			return job, true
		}
		wake := l.wake
		l.queueMu.Unlock()
		select {
		case <-ctx.Done():
			return acquisitionStoreJob{}, false
		case <-wake:
		}
	}
}

func (l *acquisitionStoreLane) enqueue(ctx, laneCtx context.Context, owner *acquisitionStoreScope, job acquisitionStoreJob, allowRetired bool) error {
	job.accountedByte = acquisitionStoreJobBytes(job.entries)
	waiter := &acquisitionStoreWaiter{job: job, bytes: job.accountedByte}
	waitStarted := time.Now()
	waited := false

	l.queueMu.Lock()
	l.waiters = append(l.waiters, waiter)
	for {
		if err := ctx.Err(); err != nil {
			l.removeWaiterLocked(waiter)
			l.queueMu.Unlock()
			if waited {
				l.recordQueueWait(waitStarted)
			}
			return err
		}
		if err := laneCtx.Err(); err != nil {
			l.removeWaiterLocked(waiter)
			l.queueMu.Unlock()
			if waited {
				l.recordQueueWait(waitStarted)
			}
			return err
		}
		if !allowRetired && owner.isRetired() {
			l.removeWaiterLocked(waiter)
			l.queueMu.Unlock()
			if waited {
				l.recordQueueWait(waitStarted)
			}
			return errors.New("acquisition persistence scope retired")
		}
		if len(l.waiters) > 0 && l.waiters[0] == waiter && l.canAdmitLocked(waiter) {
			l.waiters[0] = nil
			l.waiters = l.waiters[1:]
			l.jobs = append(l.jobs, waiter.job)
			l.reservedBytes += waiter.bytes
			l.pendingBytes.Add(waiter.bytes)
			if waiter.job.legacyDepth {
				l.legacyJobs++
			}
			current := l.currentBytes.Add(waiter.bytes)
			updateAtomicMax(&l.peakBytes, current)
			l.signalQueueLocked()
			l.queueMu.Unlock()
			if waited {
				l.recordQueueWait(waitStarted)
			}
			return nil
		}
		waited = true
		wake := l.wake
		l.queueMu.Unlock()
		select {
		case <-ctx.Done():
		case <-laneCtx.Done():
		case <-owner.retiredCh:
		case <-wake:
		}
		l.queueMu.Lock()
	}
}

func (l *acquisitionStoreLane) canAdmitLocked(waiter *acquisitionStoreWaiter) bool {
	if waiter.job.legacyDepth && l.legacyDepth >= 0 && l.legacyJobs >= l.legacyDepth+1 {
		return false
	}
	bytes := waiter.bytes
	if bytes > acquisitionStoreQueueBytes {
		// One oversized entry is admitted only when no other data is resident;
		// this is the escape hatch that prevents a permanent wait on an entry
		// larger than the normal queue budget.
		return l.reservedBytes == 0 && len(l.jobs) == 0
	}
	return l.reservedBytes <= acquisitionStoreQueueBytes-bytes
}

func (l *acquisitionStoreLane) removeWaiterLocked(target *acquisitionStoreWaiter) {
	for i := range l.waiters {
		if l.waiters[i] != target {
			continue
		}
		copy(l.waiters[i:], l.waiters[i+1:])
		l.waiters[len(l.waiters)-1] = nil
		l.waiters = l.waiters[:len(l.waiters)-1]
		l.signalQueueLocked()
		return
	}
}

func (l *acquisitionStoreLane) release(job acquisitionStoreJob) {
	if job.accountedByte == 0 {
		return
	}
	l.queueMu.Lock()
	l.reservedBytes -= job.accountedByte
	if job.legacyDepth {
		l.legacyJobs--
	}
	l.currentBytes.Add(-job.accountedByte)
	l.signalQueueLocked()
	l.queueMu.Unlock()
}

func (l *acquisitionStoreLane) dropQueued() {
	l.queueMu.Lock()
	jobs := l.jobs
	l.jobs = nil
	l.reservedBytes = 0
	l.pendingBytes.Store(0)
	for i := range jobs {
		if jobs[i].legacyDepth {
			l.legacyJobs--
		}
	}
	if l.legacyJobs < 0 {
		l.legacyJobs = 0
	}
	l.currentBytes.Store(0)
	l.signalQueueLocked()
	l.queueMu.Unlock()
	for i := range jobs {
		if jobs[i].barrier == nil {
			l.removePending(jobs[i].owner.id, jobs[i].entries)
			consumeAcquisitionEntries(jobs[i].entries)
		}
	}
}

func (l *acquisitionStoreLane) signalQueue() {
	l.queueMu.Lock()
	l.signalQueueLocked()
	l.queueMu.Unlock()
}

func (l *acquisitionStoreLane) signalQueueLocked() {
	close(l.wake)
	l.wake = make(chan struct{})
}

func (l *acquisitionStoreLane) recordQueueWait(started time.Time) {
	l.queueWaits.Add(1)
	l.queueWaitNanos.Add(int64(time.Since(started)))
}

func (l *acquisitionStoreLane) snapshot() acquisitionStoreSnapshot {
	return acquisitionStoreSnapshot{
		PendingBytes:   l.pendingBytes.Load(),
		CurrentBytes:   l.currentBytes.Load(),
		PeakBytes:      l.peakBytes.Load(),
		QueueWaits:     l.queueWaits.Load(),
		QueueWait:      time.Duration(l.queueWaitNanos.Load()),
		EntriesWritten: l.entriesWritten.Load(),
		BytesWritten:   l.bytesWritten.Load(),
		StoreLatency:   time.Duration(l.storeLatencyNs.Load()),
		StoreFailures:  l.storeFailures.Load(),
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

func acquisitionStoreBatchEnd(entries []shamap.FlushEntry, start int) int {
	bytes := int64(acquisitionStoreJobMetadata)
	end := start
	for end < len(entries) && end-start < acquisitionStoreBatchNodes {
		entryBytes := acquisitionStoreEntryBytes(entries[end])
		if end > start && bytes+entryBytes > acquisitionStoreBatchBytes {
			break
		}
		bytes += entryBytes
		end++
		if entryBytes+int64(acquisitionStoreJobMetadata) > acquisitionStoreBatchBytes {
			break
		}
	}
	return end
}

func acquisitionStoreJobBytes(entries []shamap.FlushEntry) int64 {
	bytes := int64(acquisitionStoreJobMetadata)
	for i := range entries {
		bytes += acquisitionStoreEntryBytes(entries[i])
	}
	return bytes
}

func acquisitionStoreEntryBytes(entry shamap.FlushEntry) int64 {
	return int64(len(entry.Data)) + acquisitionStoreEntryMetadata
}

func cloneAcquisitionEntries(entries []shamap.FlushEntry) []shamap.FlushEntry {
	cloned := make([]shamap.FlushEntry, len(entries))
	for i := range entries {
		cloned[i] = entries[i]
		cloned[i].Data = bytes.Clone(entries[i].Data)
	}
	return cloned
}

func consumeAcquisitionEntries(entries []shamap.FlushEntry) {
	for i := range entries {
		entries[i].Data = nil
	}
}

func (s *acquisitionStoreScope) isRetired() bool {
	s.mu.Lock()
	retired := s.retired
	s.mu.Unlock()
	return retired
}

func updateAtomicMax(dst *atomic.Int64, value int64) {
	for current := dst.Load(); value > current; {
		if dst.CompareAndSwap(current, value) {
			return
		}
		current = dst.Load()
	}
}

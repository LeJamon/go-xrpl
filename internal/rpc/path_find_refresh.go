package rpc

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx/payment/pathfinder"
)

// Persistent path_find computations are deliberately kept off the ordered
// ledger-close callback. The pathfinder does not currently accept a context,
// so a running computation cannot be interrupted; the fixed worker count
// bounds that unavoidable work and generation checks suppress its result.
const (
	pathFindRefreshWorkerCount   = int(types.MaxPathfindsInProgress)
	pathFindRefreshRetryInterval = 10 * time.Millisecond
)

type pathFindRefreshUpdate struct {
	generation uint64
	getView    func() (types.LedgerStateView, error)
	targets    []pathFindUpdateTarget
}

type pathFindRefreshJob struct {
	target     pathFindUpdateTarget
	view       types.LedgerStateView
	generation uint64
}

// pathFindRefreshManager owns the bounded asynchronous refresh pipeline for a
// WebSocketServer. At most one queued job exists per connection. A newer
// ledger generation clears all queued jobs and supersedes in-flight results;
// active pathfinder calls are allowed to finish because they are not
// cooperatively cancellable yet.
type pathFindRefreshManager struct {
	ws *WebSocketServer

	mu         sync.Mutex
	started    bool
	closed     bool
	generation uint64
	fairCursor int
	pending    *pathFindRefreshUpdate
	jobs       map[*websocketConnection]pathFindRefreshJob
	ready      []*websocketConnection
	running    map[*websocketConnection]pathFindRefreshJob

	stop       chan struct{}
	updateWake chan struct{}
	workWake   chan struct{}
	done       sync.WaitGroup
	doneCh     chan struct{}
	remaining  atomic.Int32
	doneOnce   sync.Once
	stopOnce   sync.Once
}

func newPathFindRefreshManager(ws *WebSocketServer) *pathFindRefreshManager {
	return &pathFindRefreshManager{
		ws:         ws,
		jobs:       make(map[*websocketConnection]pathFindRefreshJob),
		running:    make(map[*websocketConnection]pathFindRefreshJob),
		stop:       make(chan struct{}),
		updateWake: make(chan struct{}, 1),
		workWake:   make(chan struct{}, pathFindRefreshWorkerCount),
		doneCh:     make(chan struct{}),
	}
}

func (m *pathFindRefreshManager) start() {
	m.mu.Lock()
	if m.started || m.closed {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.done.Add(1 + pathFindRefreshWorkerCount)
	m.remaining.Store(int32(1 + pathFindRefreshWorkerCount))
	m.mu.Unlock()

	go m.coordinate()
	for range pathFindRefreshWorkerCount {
		go m.worker()
	}
}

func (m *pathFindRefreshManager) goroutineDone() {
	defer m.done.Done()
	if m.remaining.Add(-1) == 0 {
		m.doneOnce.Do(func() { close(m.doneCh) })
	}
}

func (m *pathFindRefreshManager) enqueue(getView func() (types.LedgerStateView, error), targets []pathFindUpdateTarget) {
	if len(targets) == 0 {
		m.invalidate()
		return
	}

	m.start()

	// Sorting makes the round-robin cursor deterministic. Rotating the sorted
	// list between generations prevents a connection at the end of the list
	// from being perpetually superseded by the first workers.
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].connection.ID() < targets[j].connection.ID()
	})
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.generation++
	targets = rotatePathFindTargets(targets, m.fairCursor)
	m.fairCursor++
	m.pending = &pathFindRefreshUpdate{
		generation: m.generation,
		getView:    getView,
		targets:    targets,
	}
	// A newer generation makes all queued work obsolete. In-flight work is
	// checked against generation before and after pathfinding.
	m.jobs = make(map[*websocketConnection]pathFindRefreshJob)
	m.ready = m.ready[:0]
	m.mu.Unlock()
	m.signal(m.updateWake)
}

func rotatePathFindTargets(targets []pathFindUpdateTarget, offset int) []pathFindUpdateTarget {
	if len(targets) == 0 {
		return targets
	}
	offset %= len(targets)
	if offset == 0 {
		return targets
	}
	rotated := make([]pathFindUpdateTarget, 0, len(targets))
	rotated = append(rotated, targets[offset:]...)
	rotated = append(rotated, targets[:offset]...)
	return rotated
}

// invalidate advances the generation without starting workers. This is used
// when a ledger close observes no active sessions, ensuring in-flight work
// from a previous generation cannot publish after all sessions are gone.
func (m *pathFindRefreshManager) invalidate() {
	m.mu.Lock()
	if !m.closed {
		m.generation++
		m.pending = nil
		m.jobs = make(map[*websocketConnection]pathFindRefreshJob)
		m.ready = m.ready[:0]
	}
	m.mu.Unlock()
}

func (m *pathFindRefreshManager) coordinate() {
	defer m.goroutineDone()
	for {
		select {
		case <-m.stop:
			return
		case <-m.updateWake:
		}

		for {
			update := m.takePending()
			if update == nil {
				break
			}

			view, err := getPathFindRefreshView(update.getView)
			if err != nil {
				// A failed view lookup must not poison subsequent generations.
				wsLog().Error("Failed to get ledger view for path_find updates", "err", err)
				continue
			}
			if view == nil || !m.generationCurrent(update.generation) {
				continue
			}

			for _, target := range update.targets {
				if !m.generationCurrent(update.generation) {
					break
				}
				if !target.current() {
					continue
				}
				m.enqueueJob(pathFindRefreshJob{target: target, view: view, generation: update.generation}, update.generation)
			}
		}
	}
}

func getPathFindRefreshView(getView func() (types.LedgerStateView, error)) (view types.LedgerStateView, err error) {
	if getView == nil {
		return nil, nil
	}
	return getView()
}

func (m *pathFindRefreshManager) takePending() *pathFindRefreshUpdate {
	m.mu.Lock()
	defer m.mu.Unlock()
	update := m.pending
	m.pending = nil
	return update
}

func (m *pathFindRefreshManager) enqueueJob(job pathFindRefreshJob, generation uint64) {
	m.mu.Lock()
	if m.closed || m.generation != generation || !job.target.current() {
		m.mu.Unlock()
		return
	}
	if _, exists := m.jobs[job.target.connection]; !exists {
		m.ready = append(m.ready, job.target.connection)
	}
	m.jobs[job.target.connection] = job
	m.mu.Unlock()
	m.signal(m.workWake)
}

func (m *pathFindRefreshManager) worker() {
	defer m.goroutineDone()
	for {
		job, ok := m.takeJob()
		if !ok {
			select {
			case <-m.stop:
				return
			case <-m.workWake:
			}
			continue
		}

		m.runJob(job)
	}
}

func (m *pathFindRefreshManager) runJob(job pathFindRefreshJob) {
	defer m.finishJob(job)
	release, admitted := m.waitForPathfindAdmission(job)
	if !admitted {
		return
	}
	defer release()
	if !m.jobCurrent(job) {
		return
	}

	result := m.execute(job)
	if result == nil || !m.jobCurrent(job) {
		return
	}
	status := job.target.session.BuildEvent(result, true)
	event := clonePathFindEvent(status)
	event.Type = "path_find"
	data, err := json.Marshal(event)
	if err != nil {
		wsLog().Error("Failed to marshal path_find update", "err", err)
		return
	}
	m.commit(job, status, data)
}

func (m *pathFindRefreshManager) takeJob() (pathFindRefreshJob, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.ready) != 0 {
		for i, connection := range m.ready {
			if _, running := m.running[connection]; running {
				continue
			}
			job, ok := m.jobs[connection]
			if !ok {
				m.ready = append(m.ready[:i], m.ready[i+1:]...)
				break
			}
			m.ready = append(m.ready[:i], m.ready[i+1:]...)
			delete(m.jobs, connection)
			m.running[connection] = job
			return job, true
		}
		break
	}
	return pathFindRefreshJob{}, false
}

func (m *pathFindRefreshManager) finishJob(job pathFindRefreshJob) {
	m.mu.Lock()
	if running, ok := m.running[job.target.connection]; ok && running.target.session == job.target.session && running.target.generation == job.target.generation && running.generation == job.generation {
		delete(m.running, job.target.connection)
		if !m.closed {
			if _, queued := m.jobs[job.target.connection]; queued {
				m.signal(m.workWake)
			}
		}
	}
	m.mu.Unlock()
}

func (m *pathFindRefreshManager) execute(job pathFindRefreshJob) (result *pathfinder.PathRequestResult) {
	defer func() {
		if rec := recover(); rec != nil {
			wsLog().Error("path_find refresh panic", "conn", job.target.connection.ID(), "err", rec)
			result = nil
		}
	}()
	return job.target.session.Compute(job.view, false)
}

func (m *pathFindRefreshManager) commit(job pathFindRefreshJob, status *PathFindEvent, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.generation != job.generation {
		return
	}
	connection := job.target.connection
	connection.mutex.Lock()
	defer connection.mutex.Unlock()
	if connection.pathFindSession != job.target.session || connection.pathFindGeneration != job.target.generation || connection.Connection == nil {
		return
	}
	job.target.session.commitBuiltEvent(status)
	job.target.connection.TrySend(data)
}

func (m *pathFindRefreshManager) waitForPathfindAdmission(job pathFindRefreshJob) (func(), bool) {
	shedder := m.pathFindShedder()
	if shedder == nil {
		return func() {}, m.jobCurrent(job)
	}
	for {
		if !m.jobCurrent(job) {
			return nil, false
		}
		if shedder.AcquirePathfind() {
			return shedder.ReleasePathfind, true
		}
		timer := time.NewTimer(pathFindRefreshRetryInterval)
		select {
		case <-m.stop:
			if !timer.Stop() {
				<-timer.C
			}
			return nil, false
		case <-timer.C:
		}
	}
}

func (m *pathFindRefreshManager) pathFindShedder() *types.ClientLoadShedder {
	if m.ws == nil || m.ws.services == nil {
		return nil
	}
	return m.ws.services.ClientLoad
}

func (m *pathFindRefreshManager) generationCurrent(generation uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.closed && m.generation == generation
}

func (m *pathFindRefreshManager) jobCurrent(job pathFindRefreshJob) bool {
	return m.generationCurrent(job.generation) && job.target.current()
}

func (m *pathFindRefreshManager) cancel(connection *websocketConnection, session *PathFindSession, generation uint64) {
	m.mu.Lock()
	if job, ok := m.jobs[connection]; ok && job.target.session == session && job.target.generation == generation {
		delete(m.jobs, connection)
		ready := m.ready[:0]
		for _, candidate := range m.ready {
			if candidate != connection {
				ready = append(ready, candidate)
			}
		}
		m.ready = ready
	}
	m.mu.Unlock()
}

func (m *pathFindRefreshManager) shutdown() {
	m.stopOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.pending = nil
		m.jobs = make(map[*websocketConnection]pathFindRefreshJob)
		m.ready = nil
		started := m.started
		m.mu.Unlock()
		if !started {
			m.doneOnce.Do(func() { close(m.doneCh) })
			return
		}
		close(m.stop)
		m.signal(m.updateWake)
		m.signal(m.workWake)
	})
}

func (m *pathFindRefreshManager) wait(ctx context.Context) error {
	m.shutdown()
	select {
	case <-m.doneCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *pathFindRefreshManager) close() {
	_ = m.wait(context.Background())
}

func (m *pathFindRefreshManager) signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

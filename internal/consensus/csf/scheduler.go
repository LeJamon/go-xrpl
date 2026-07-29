// Package csf provides a Consensus Simulation Framework: a deterministic
// discrete-event scheduler, a simulated peer-to-peer network, a trust graph
// and a ledger oracle for exercising the production consensus engine without
// real time or sockets.
package csf

import (
	"container/heap"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/protocol"
)

// SimTime represents simulated time as a duration from epoch.
type SimTime time.Duration

// SimDuration is an alias for time.Duration used in simulation.
type SimDuration = time.Duration

// event represents a scheduled event in the simulation.
type event struct {
	when    SimTime
	seq     uint64 // For stable ordering of same-time events
	handler func()
	index   int // Index in the heap
}

// eventHeap implements heap.Interface for events ordered by time.
type eventHeap []*event

func (h eventHeap) Len() int { return len(h) }

func (h eventHeap) Less(i, j int) bool {
	if h[i].when == h[j].when {
		return h[i].seq < h[j].seq
	}
	return h[i].when < h[j].when
}

func (h eventHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *eventHeap) Push(x any) {
	n := len(*h)
	e := x.(*event)
	e.index = n
	*h = append(*h, e)
}

func (h *eventHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = nil
	e.index = -1
	*h = old[0 : n-1]
	return e
}

// Scheduler implements a discrete event scheduler with simulated time.
// Events are processed in time order without any real delays.
type Scheduler struct {
	// driverMu serializes stepping APIs and remains held while handlers run.
	driverMu sync.Mutex
	mu       sync.Mutex
	now      SimTime
	events   eventHeap
	nextSeq  uint64
}

// NewScheduler creates a new discrete event scheduler starting at time 0.
func NewScheduler() *Scheduler {
	s := &Scheduler{
		events: make(eventHeap, 0),
	}
	heap.Init(&s.events)
	return s
}

// Now returns the current simulated time.
func (s *Scheduler) Now() SimTime {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now
}

// NowTime returns the simulated network time used by rippled's CSF peers.
func (s *Scheduler) NowTime() time.Time {
	return time.Unix(
		protocol.RippleEpochUnix,
		int64(24*time.Hour)+int64(s.Now()),
	).UTC()
}

// In schedules a handler to execute after the given duration.
// Returns a cancel function that can be used to cancel the event.
func (s *Scheduler) In(d SimDuration, handler func()) func() {
	if d < 0 {
		panic("csf: cannot schedule an event with a negative delay")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	when := s.now + SimTime(d)
	if when < s.now {
		panic("csf: scheduled event time overflow")
	}
	e := &event{
		when:    when,
		seq:     s.nextSeq,
		handler: handler,
	}
	s.nextSeq++
	heap.Push(&s.events, e)

	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if e.index >= 0 {
			heap.Remove(&s.events, e.index)
		}
	}
}

// At schedules a handler to execute at a specific time.
// Returns a cancel function.
func (s *Scheduler) At(when SimTime, handler func()) func() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if when < s.now {
		panic("csf: cannot schedule an event in the past")
	}
	e := &event{
		when:    when,
		seq:     s.nextSeq,
		handler: handler,
	}
	s.nextSeq++
	heap.Push(&s.events, e)

	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if e.index >= 0 {
			heap.Remove(&s.events, e.index)
		}
	}
}

// StepOne processes a single event if available.
// Returns true if an event was processed, false if queue is empty.
func (s *Scheduler) StepOne() bool {
	s.driverMu.Lock()
	defer s.driverMu.Unlock()
	return s.stepOne()
}

func (s *Scheduler) stepOne() bool {
	s.mu.Lock()
	if s.events.Len() == 0 {
		s.mu.Unlock()
		return false
	}

	e := heap.Pop(&s.events).(*event)
	if e.when < s.now {
		s.mu.Unlock()
		panic("csf: scheduled event would regress simulation time")
	}
	s.now = e.when
	handler := e.handler
	s.mu.Unlock()

	handler()
	return true
}

// Step processes all scheduled events, advancing time to each event in turn.
// Returns the number of events processed.
func (s *Scheduler) Step() int {
	s.driverMu.Lock()
	defer s.driverMu.Unlock()

	count := 0
	for s.stepOne() {
		count++
	}
	return count
}

// StepFor processes events for the given duration of simulated time.
// Returns the number of events processed.
func (s *Scheduler) StepFor(d SimDuration) int {
	if d < 0 {
		panic("csf: cannot step for a negative duration")
	}

	s.driverMu.Lock()
	defer s.driverMu.Unlock()

	s.mu.Lock()
	endTime := s.now + SimTime(d)
	if endTime < s.now {
		s.mu.Unlock()
		panic("csf: step duration overflows simulation time")
	}
	s.mu.Unlock()

	return s.stepUntil(endTime)
}

// StepUntil processes events until the given simulated time.
// Returns the number of events processed.
func (s *Scheduler) StepUntil(until SimTime) int {
	s.driverMu.Lock()
	defer s.driverMu.Unlock()
	return s.stepUntil(until)
}

func (s *Scheduler) stepUntil(until SimTime) int {
	s.mu.Lock()
	if until < s.now {
		s.mu.Unlock()
		panic("csf: cannot step backwards")
	}
	s.mu.Unlock()

	count := 0
	for {
		s.mu.Lock()
		if s.events.Len() == 0 {
			s.now = until
			s.mu.Unlock()
			break
		}
		if s.events[0].when > until {
			s.now = until
			s.mu.Unlock()
			break
		}

		e := heap.Pop(&s.events).(*event)
		s.now = e.when
		handler := e.handler
		s.mu.Unlock()

		handler()
		count++
	}
	return count
}

// StepWhile processes events while the predicate returns true.
// Returns the number of events processed.
func (s *Scheduler) StepWhile(pred func() bool) int {
	s.driverMu.Lock()
	defer s.driverMu.Unlock()

	count := 0
	for pred() {
		if !s.stepOne() {
			break
		}
		count++
	}
	return count
}

// Empty returns true if there are no pending events.
func (s *Scheduler) Empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events.Len() == 0
}

// PendingCount returns the number of pending events.
func (s *Scheduler) PendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events.Len()
}

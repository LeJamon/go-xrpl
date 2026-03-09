// Package csf provides a Consensus Simulation Framework for testing consensus algorithms.
// It is a Go port of rippled's csf test framework, enabling deterministic discrete event
// simulation of consensus behavior without real network or time dependencies.
package csf

import (
	"container/heap"
	"sync"
	"time"
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

func (h *eventHeap) Push(x interface{}) {
	n := len(*h)
	e := x.(*event)
	e.index = n
	*h = append(*h, e)
}

func (h *eventHeap) Pop() interface{} {
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
	mu      sync.Mutex
	now     SimTime
	events  eventHeap
	nextSeq uint64
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

// NowTime returns the current simulated time as time.Time (from Unix epoch).
func (s *Scheduler) NowTime() time.Time {
	return time.Unix(0, int64(s.Now()))
}

// In schedules a handler to execute after the given duration.
// Returns a cancel function that can be used to cancel the event.
func (s *Scheduler) In(d SimDuration, handler func()) func() {
	s.mu.Lock()
	defer s.mu.Unlock()

	e := &event{
		when:    s.now + SimTime(d),
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
	s.mu.Lock()
	if s.events.Len() == 0 {
		s.mu.Unlock()
		return false
	}

	e := heap.Pop(&s.events).(*event)
	s.now = e.when
	handler := e.handler
	s.mu.Unlock()

	handler()
	return true
}

// Step processes all events up to and including the current time.
// Returns the number of events processed.
func (s *Scheduler) Step() int {
	count := 0
	for {
		s.mu.Lock()
		if s.events.Len() == 0 {
			s.mu.Unlock()
			break
		}
		if s.events[0].when > s.now {
			s.mu.Unlock()
			break
		}
		e := heap.Pop(&s.events).(*event)
		handler := e.handler
		s.mu.Unlock()

		handler()
		count++
	}
	return count
}

// StepFor processes events for the given duration of simulated time.
// Returns the number of events processed.
func (s *Scheduler) StepFor(d SimDuration) int {
	s.mu.Lock()
	endTime := s.now + SimTime(d)
	s.mu.Unlock()

	return s.StepUntil(endTime)
}

// StepUntil processes events until the given simulated time.
// Returns the number of events processed.
func (s *Scheduler) StepUntil(until SimTime) int {
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
	count := 0
	for pred() {
		if !s.StepOne() {
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

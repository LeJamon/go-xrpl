package observability

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// IOSLatencyEventName is the event name rippled uses for the
// io_latency_probe sample stream
// (rippled/src/xrpld/app/main/Application.cpp:471). Preserved verbatim
// for wire-format compatibility with metrics scrapers and dashboards
// that already key off rippled's "ios_latency" event.
const IOSLatencyEventName = "ios_latency"

// Collector is a sink for metric events. Mirrors rippled's
// beast::insight::Collector abstraction
// (rippled/include/xrpl/beast/insight/Collector.h): a pluggable
// destination that the rest of goXRPL notifies of named events
// without caring whether the backend is in-process aggregation, a
// StatsD forwarder, or anything else.
//
// Implementations MUST be safe for concurrent use. NotifyEvent is on
// the hot path of the latency sampler and other future sources, so
// implementations should avoid holding locks across the call.
type Collector interface {
	// NotifyEvent records value under the named event. value is
	// converted to ceil-ms by the implementation (matching rippled's
	// ceil<milliseconds>(elapsed) at Application.cpp:130).
	NotifyEvent(name string, value time.Duration)

	// Stats returns a point-in-time snapshot of the named event's
	// aggregates. Returns the zero EventStats if the event has never
	// been notified on this Collector.
	Stats(name string) EventStats
}

// EventStats is a snapshot of one named event's aggregates. Each
// field is loaded independently with an atomic operation, so under
// concurrent writes a snapshot may not represent any single-instant
// consistent view (Count and SumMs may disagree by one notification).
// Each field on its own is always a value the Collector has actually
// observed.
type EventStats struct {
	// Count is the total number of NotifyEvent calls for this event.
	Count int64
	// SumMs is the sum of ceil-ms values across all notifications.
	// SumMs/Count gives a coarse mean.
	SumMs int64
	// MinMs is the smallest ceil-ms observed. Zero when Count == 0.
	MinMs int64
	// MaxMs is the largest ceil-ms observed.
	MaxMs int64
	// LastMs is the most recently notified ceil-ms value.
	LastMs int64
}

// MemoryCollector is the default in-process Collector. It records
// per-event count/sum/min/max/last-ms aggregates in atomic counters
// so the caller's NotifyEvent path is wait-free under contention,
// and Stats can be read without blocking the sampler.
//
// MemoryCollector is the right default when goXRPL has no remote
// metrics backend configured. When one is added (StatsD, Prometheus,
// OTel), wrap or replace this with a forwarding Collector via
// SetCollector. The aggregates here are useful even alongside a
// forwarder because they let admin RPCs surface the metric without a
// round-trip to the remote backend.
type MemoryCollector struct {
	mu     sync.Mutex
	events map[string]*memEvent
}

// memEvent holds the running atomic aggregates for one named event.
type memEvent struct {
	count  atomic.Int64
	sumMs  atomic.Int64
	minMs  atomic.Int64
	maxMs  atomic.Int64
	lastMs atomic.Int64
}

// NewMemoryCollector returns a fresh MemoryCollector with no recorded
// events. Safe to use concurrently from the moment it is returned.
func NewMemoryCollector() *MemoryCollector {
	return &MemoryCollector{events: make(map[string]*memEvent)}
}

// NotifyEvent records value under name. value is converted to
// ceil-ms before aggregation, matching rippled's
// ceil<milliseconds>(elapsed) at Application.cpp:130. Safe for
// concurrent calls; the only contended path is the first
// notification for a previously-unseen event name (which takes the
// internal mutex briefly to allocate the memEvent).
func (c *MemoryCollector) NotifyEvent(name string, value time.Duration) {
	ms := int64(ceilMs(value))
	e := c.getOrCreate(name)
	e.count.Add(1)
	e.sumMs.Add(ms)
	e.lastMs.Store(ms)

	// Min: initialized to MaxInt64 by getOrCreate so the first
	// notification always wins. Use CAS so concurrent notifies cannot
	// overwrite a smaller min with a larger one.
	for {
		cur := e.minMs.Load()
		if ms >= cur {
			break
		}
		if e.minMs.CompareAndSwap(cur, ms) {
			break
		}
	}

	// Max: same CAS pattern, opposite direction.
	for {
		cur := e.maxMs.Load()
		if ms <= cur {
			break
		}
		if e.maxMs.CompareAndSwap(cur, ms) {
			break
		}
	}
}

// Stats returns a snapshot of the named event's aggregates.
func (c *MemoryCollector) Stats(name string) EventStats {
	c.mu.Lock()
	e, ok := c.events[name]
	c.mu.Unlock()
	if !ok {
		return EventStats{}
	}
	minVal := e.minMs.Load()
	if minVal == math.MaxInt64 {
		// Event was created but no notify completed; treat as no-data.
		minVal = 0
	}
	return EventStats{
		Count:  e.count.Load(),
		SumMs:  e.sumMs.Load(),
		MinMs:  minVal,
		MaxMs:  e.maxMs.Load(),
		LastMs: e.lastMs.Load(),
	}
}

// Reset drops every recorded event. Intended for tests; calling this
// against a live sampler will lose any samples notified between Reset
// and the next NotifyEvent.
func (c *MemoryCollector) Reset() {
	c.mu.Lock()
	c.events = make(map[string]*memEvent)
	c.mu.Unlock()
}

func (c *MemoryCollector) getOrCreate(name string) *memEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.events == nil {
		c.events = make(map[string]*memEvent)
	}
	e, ok := c.events[name]
	if !ok {
		e = &memEvent{}
		e.minMs.Store(math.MaxInt64)
		c.events[name] = e
	}
	return e
}

// collectorBox wraps a Collector so atomic.Value can store
// implementations of different concrete types under one stable
// container type. atomic.Value.Store requires every stored value to
// share the same concrete type; without the box, swapping a
// *MemoryCollector for, say, a forwarding StatsD collector would
// panic at runtime.
type collectorBox struct{ Collector }

// defaultCollector is the package-wide Collector that the latency
// sampler (and any future event sources) notify. atomic.Value gives
// us lock-free reads on the hot path; SetCollector swaps the value
// atomically so a concurrent NotifyEvent always lands on a valid
// Collector — either the old one or the new one, never a torn read.
var defaultCollector atomic.Value

func init() {
	defaultCollector.Store(collectorBox{NewMemoryCollector()})
}

// DefaultCollector returns the active package-wide Collector. The
// returned Collector is the one set by the most recent SetCollector
// call (or the initial MemoryCollector if SetCollector has never
// been called).
func DefaultCollector() Collector {
	return defaultCollector.Load().(collectorBox).Collector
}

// SetCollector swaps the active Collector. Notifications in flight
// during the swap may land on either the old or new Collector;
// callers that need a deterministic baseline (typically tests) should
// quiesce the sampler before swapping.
//
// Passing nil is a no-op preserving the current Collector — callers
// that want to clear should pass NewMemoryCollector() instead.
func SetCollector(c Collector) {
	if c == nil {
		return
	}
	defaultCollector.Store(collectorBox{c})
}

// IOLatencyEventStats returns the aggregated stats for io_latency
// samples that crossed rippled's 10ms event threshold
// (Application.cpp:134-135). Convenience wrapper for callers that
// only care about the io_latency event and don't want to plumb the
// Collector and event name through themselves.
func IOLatencyEventStats() EventStats {
	return DefaultCollector().Stats(IOSLatencyEventName)
}

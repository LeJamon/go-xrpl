package list

import (
	"log/slog"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// changeEvent is an immutable snapshot of one trusted-set transition. The
// callback is captured when the transition is queued so replacing a callback
// cannot reorder or redirect an event that was already computed.
type changeEvent struct {
	callback   func([]consensus.NodeID, [][33]byte)
	validators []consensus.NodeID
	masterKeys [][33]byte
}

// changeDispatcher serializes callback delivery without holding Aggregator.mu.
// New transitions may be queued while a callback is slow or re-enters the
// aggregator; the active drainer picks them up in aggregator lock order.
type changeDispatcher struct {
	mu       sync.Mutex
	events   []changeEvent
	draining bool
}

func (a *Aggregator) dispatchChanges() {
	a.changes.drain(a.logger)
}

func (a *Aggregator) dispatchChangesAsync() {
	if !a.changes.beginDrain() {
		return
	}
	go a.changes.drainOwned(a.logger)
}

func (d *changeDispatcher) enqueue(event changeEvent) {
	if event.callback == nil {
		return
	}
	event.validators = append([]consensus.NodeID(nil), event.validators...)
	event.masterKeys = append([][33]byte(nil), event.masterKeys...)
	d.mu.Lock()
	d.events = append(d.events, event)
	d.mu.Unlock()
}

func (d *changeDispatcher) drain(logger *slog.Logger) {
	if !d.beginDrain() {
		return
	}
	d.drainOwned(logger)
}

func (d *changeDispatcher) beginDrain() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.draining || len(d.events) == 0 {
		return false
	}
	d.draining = true
	return true
}

func (d *changeDispatcher) drainOwned(logger *slog.Logger) {
	for {
		d.mu.Lock()
		if len(d.events) == 0 {
			d.draining = false
			d.mu.Unlock()
			return
		}
		event := d.events[0]
		d.events[0] = changeEvent{}
		d.events = d.events[1:]
		d.mu.Unlock()

		invokeChange(event, logger)
	}
}

func invokeChange(event changeEvent, logger *slog.Logger) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if logger == nil {
				slog.Default().Error("validator-list change callback panicked", "panic", recovered)
				return
			}
			logger.Error("validator-list change callback panicked", "panic", recovered)
		}
	}()

	// The event owns these slices. Give the callback a fresh copy so a
	// callback cannot mutate a queued event that has not been delivered yet.
	validators := append([]consensus.NodeID(nil), event.validators...)
	masterKeys := append([][33]byte(nil), event.masterKeys...)
	event.callback(validators, masterKeys)
}

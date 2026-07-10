package consensus

import "testing"

// TestEventBus_DropsAreCounted verifies the event bus counts events shed when
// its buffer is full instead of losing them silently, so an operator debugging
// a gap on the validations/consensus streams has a signal.
func TestEventBus_DropsAreCounted(t *testing.T) {
	// Buffer of 1, never Start()ed, so nothing drains: the first Publish fills
	// the buffer and every subsequent one is dropped.
	eb := NewEventBus(1)

	const total = 5
	for range total {
		eb.Publish(&TimerFiredEvent{Timer: TimerLedgerClose})
	}

	if got, want := eb.DroppedEvents(), uint64(total-1); got != want {
		t.Errorf("DroppedEvents = %d, want %d", got, want)
	}
}

// TestEventBus_StopIdempotent verifies a second Stop is a no-op rather than a
// close-of-closed panic.
func TestEventBus_StopIdempotent(t *testing.T) {
	eb := NewEventBus(4)
	eb.Start()
	eb.Stop()
	eb.Stop() // must not panic
}

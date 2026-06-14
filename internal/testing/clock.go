package testing

import (
	"sync"
	"time"
)

// ManualClock provides a controllable clock for testing time-dependent behavior.
type ManualClock struct {
	mu      sync.RWMutex
	current time.Time
}

// NewManualClock creates a new ManualClock set to a default time.
// The default is January 1, 2020, 00:00:00 UTC, which is after the Ripple epoch.
func NewManualClock() *ManualClock {
	return &ManualClock{
		current: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// Now returns the current time on the clock.
func (c *ManualClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.current
}

// Advance moves the clock forward by the specified duration.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(d)
}

// Set sets the clock to a specific time.
func (c *ManualClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = t
}

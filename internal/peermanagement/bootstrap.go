package peermanagement

import (
	"sync"
	"sync/atomic"
)

type bootstrapGovernor struct {
	ready atomic.Bool

	mu       sync.Mutex
	reserved bool
}

func (g *bootstrapGovernor) isReady() bool {
	return g.ready.Load()
}

func (g *bootstrapGovernor) tryReserve() (*bootstrapLease, bool) {
	if g.ready.Load() {
		return nil, true
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ready.Load() {
		return nil, true
	}
	if g.reserved {
		return nil, false
	}
	g.reserved = true
	return &bootstrapLease{governor: g}, true
}

type bootstrapLease struct {
	governor *bootstrapGovernor
	released atomic.Bool
}

func (l *bootstrapLease) markReady() {
	if l == nil || l.released.Swap(true) {
		return
	}
	l.governor.ready.Store(true)
	l.governor.mu.Lock()
	l.governor.reserved = false
	l.governor.mu.Unlock()
}

func (l *bootstrapLease) release() {
	if l == nil || l.released.Swap(true) {
		return
	}
	l.governor.mu.Lock()
	l.governor.reserved = false
	l.governor.mu.Unlock()
}

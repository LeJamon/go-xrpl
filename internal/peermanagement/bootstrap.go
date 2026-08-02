package peermanagement

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

const (
	bootstrapSampleAge      = 15 * time.Second
	bootstrapSampleInterval = 5 * time.Second
	bootstrapTargetDuration = 110 * time.Second
	bootstrapPartialRetry   = 10 * time.Minute
)

type bootstrapFrameProgress struct {
	messageType message.MessageType
	wireSize    uint32
	compressed  bool
	bytesRead   uint64
	elapsed     time.Duration
}

type bootstrapProgressObservation struct {
	sampled   bool
	projected time.Duration
	hedge     bool
}

func projectedFrameDuration(wireSize uint32, bytesRead uint64, elapsed time.Duration) time.Duration {
	if wireSize == 0 || bytesRead == 0 || elapsed <= 0 {
		return 0
	}
	return time.Duration(float64(elapsed) * float64(wireSize) / float64(bytesRead))
}

func frameReadRate(bytesRead uint64, elapsed time.Duration) uint64 {
	if elapsed <= 0 {
		return 0
	}
	return uint64(float64(bytesRead) * float64(time.Second) / float64(elapsed))
}

type bootstrapGovernor struct {
	ready atomic.Bool

	mu      sync.Mutex
	active  map[*bootstrapLease]struct{}
	hedging bool
}

func (g *bootstrapGovernor) isReady() bool {
	return g.ready.Load()
}

func (g *bootstrapGovernor) tryReserve() (*bootstrapLease, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	limit := 1
	if !g.ready.Load() && g.hedging {
		limit = 2
	}
	if len(g.active) >= limit {
		return nil, false
	}
	if g.active == nil {
		g.active = make(map[*bootstrapLease]struct{})
	}
	lease := &bootstrapLease{governor: g}
	g.active[lease] = struct{}{}
	return lease, true
}

func (g *bootstrapGovernor) activeCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.active)
}

type bootstrapLease struct {
	governor *bootstrapGovernor
	released atomic.Bool

	progressMu        sync.Mutex
	lastProgressCheck time.Duration
}

func (l *bootstrapLease) markReady() {
	if l == nil || l.released.Swap(true) {
		return
	}
	l.governor.mu.Lock()
	delete(l.governor.active, l)
	l.governor.ready.Store(true)
	l.governor.mu.Unlock()
}

func (l *bootstrapLease) release() {
	if l == nil || l.released.Swap(true) {
		return
	}
	l.governor.mu.Lock()
	delete(l.governor.active, l)
	l.governor.mu.Unlock()
}

func (l *bootstrapLease) observeProgress(progress bootstrapFrameProgress) bootstrapProgressObservation {
	if l == nil || progress.messageType != message.TypeManifests ||
		progress.bytesRead == 0 || progress.elapsed < bootstrapSampleAge {
		return bootstrapProgressObservation{}
	}

	l.progressMu.Lock()
	if l.lastProgressCheck > 0 && progress.elapsed-l.lastProgressCheck < bootstrapSampleInterval {
		l.progressMu.Unlock()
		return bootstrapProgressObservation{}
	}
	l.lastProgressCheck = progress.elapsed
	l.progressMu.Unlock()

	projected := projectedFrameDuration(progress.wireSize, progress.bytesRead, progress.elapsed)
	observation := bootstrapProgressObservation{sampled: true, projected: projected}
	if projected <= bootstrapTargetDuration {
		return observation
	}

	g := l.governor
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ready.Load() || l.released.Load() {
		return observation
	}
	if _, active := g.active[l]; !active || g.hedging {
		return observation
	}
	g.hedging = true
	observation.hedge = true
	return observation
}

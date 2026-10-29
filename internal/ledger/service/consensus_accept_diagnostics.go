package service

import (
	"bytes"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"time"

	xrpllog "github.com/LeJamon/go-xrpl/log"
)

const (
	slowConsensusAcceptance   = 5 * time.Second
	acceptanceProfileCooldown = time.Minute
	maxAcceptanceProfileBytes = 256 << 10
)

type consensusAcceptanceDiagnostics struct {
	// A blocked profiler or log sink retains the only reporting slot.
	mu   sync.Mutex
	next time.Time
}

type consensusAcceptanceObservation struct {
	seq      uint32
	started  time.Time
	complete atomic.Bool
}

func (d *consensusAcceptanceDiagnostics) watch(logger xrpllog.Logger, seq uint32, started time.Time, delay time.Duration) func() {
	observation := &consensusAcceptanceObservation{seq: seq, started: started}
	timer := time.AfterFunc(delay, func() { d.report(logger, observation) })
	return func() {
		observation.complete.Store(true)
		timer.Stop()
	}
}

func (d *consensusAcceptanceDiagnostics) report(logger xrpllog.Logger, observation *consensusAcceptanceObservation) {
	if observation.complete.Load() || !d.mu.TryLock() {
		return
	}
	defer d.mu.Unlock()
	now := time.Now()
	if observation.complete.Load() || now.Before(d.next) {
		return
	}
	d.next = now.Add(acceptanceProfileCooldown)
	var profile acceptanceProfileBuffer
	// Grouped profiles avoid the runtime.Stack(all=true) unwinder path.
	err := pprof.Lookup("goroutine").WriteTo(&profile, 1)
	logger.Warn("Slow consensus acceptance profile",
		"seq", observation.seq, "elapsed_at_capture", now.Sub(observation.started), "completed", observation.complete.Load(),
		"profile_duration", time.Since(now), "profile_truncated", profile.truncated,
		"profile_error", err, "goroutine_profile", profile.buffer.String())
}

type acceptanceProfileBuffer struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *acceptanceProfileBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxAcceptanceProfileBytes - b.buffer.Len()
	if n > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

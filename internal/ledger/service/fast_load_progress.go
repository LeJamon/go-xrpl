package service

import (
	"fmt"
	"sync/atomic"
	"time"

	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/shamap"
)

const (
	storedSHAMapVerificationLogInterval = 15 * time.Second
	// Workers flush local counts in batches to keep the shared atomic off the
	// per-node hot path while retaining exact terminal totals.
	storedSHAMapNodeCountBatch = 256
)

type storedSHAMapVerificationProgress struct {
	logger xrpllog.Logger

	mapType string
	root    string

	startedAt  time.Time
	lastReport time.Time
	interval   time.Duration
	started    bool

	nodesChecked     atomic.Uint64
	branchesComplete atomic.Uint32
	branchesTotal    uint32
}

func newStoredSHAMapVerificationProgress(
	logger xrpllog.Logger,
	root [32]byte,
	mapType shamap.Type,
	startedAt time.Time,
) *storedSHAMapVerificationProgress {
	return &storedSHAMapVerificationProgress{
		logger:     logger,
		mapType:    mapType.String(),
		root:       fmt.Sprintf("%x", root[:8]),
		startedAt:  startedAt,
		lastReport: startedAt,
		interval:   storedSHAMapVerificationLogInterval,
	}
}

func (p *storedSHAMapVerificationProgress) start() {
	if p.started {
		return
	}
	p.started = true
	p.logger.Info("stored SHAMap verification started",
		"map_type", p.mapType,
		"root", p.root,
		"active_branches", p.branchesTotal,
	)
}

func (p *storedSHAMapVerificationProgress) report(at time.Time) {
	if at.Before(p.lastReport.Add(p.interval)) {
		return
	}
	p.lastReport = at
	p.logger.Info("stored SHAMap verification progress", p.fields(at)...)
}

func (p *storedSHAMapVerificationProgress) finish(at time.Time, err error) {
	p.start()
	fields := p.fields(at)
	if err != nil {
		p.logger.Warn("stored SHAMap verification failed", append(fields, "err", err)...)
		return
	}
	p.logger.Info("stored SHAMap verification complete", fields...)
}

func (p *storedSHAMapVerificationProgress) fields(at time.Time) []any {
	elapsed := at.Sub(p.startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	nodesChecked := p.nodesChecked.Load()
	var nodesPerSecond uint64
	if elapsed > 0 {
		nodesPerSecond = uint64(float64(nodesChecked) / elapsed.Seconds())
	}
	return []any{
		"map_type", p.mapType,
		"root", p.root,
		"elapsed", elapsed.String(),
		"nodes_checked", nodesChecked,
		"nodes_per_second", nodesPerSecond,
		"branches_complete", p.branchesComplete.Load(),
		"branches_total", p.branchesTotal,
	}
}

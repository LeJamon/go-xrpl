package service

import (
	"fmt"
	"sync/atomic"
	"time"

	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
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
	lastNodes  uint64
	interval   time.Duration
	started    bool

	nodesChecked     atomic.Uint64
	branchesComplete atomic.Uint32
	branchesTotal    uint32
	workersResolved  uint32
	workersStarted   uint32
	activeWorkers    atomic.Int32
	frontierSize     atomic.Int64

	nodeStore    nodestore.Database
	initialStats nodestore.Statistics
}

func newStoredSHAMapVerificationProgress(
	logger xrpllog.Logger,
	nodeStore nodestore.Database,
	root [32]byte,
	mapType shamap.Type,
	startedAt time.Time,
) *storedSHAMapVerificationProgress {
	progress := &storedSHAMapVerificationProgress{
		logger:     logger,
		nodeStore:  nodeStore,
		mapType:    mapType.String(),
		root:       fmt.Sprintf("%x", root[:8]),
		startedAt:  startedAt,
		lastReport: startedAt,
		interval:   storedSHAMapVerificationLogInterval,
	}
	if nodeStore != nil {
		progress.initialStats = nodeStore.Stats()
	}
	return progress
}

func (p *storedSHAMapVerificationProgress) configureWorkers(
	resolved int,
	started int,
	frontier int,
) {
	p.workersResolved = uint32(resolved)
	p.workersStarted = uint32(started)
	p.frontierSize.Store(int64(frontier))
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
		"workers", p.workersResolved,
		"frontier_size", p.frontierSize.Load(),
		"node_store_reads_before", p.initialStats.Reads,
		"node_store_read_bytes_before", p.initialStats.ReadBytes,
		"node_cache_hits_before", p.initialStats.CacheHits,
		"node_cache_misses_before", p.initialStats.CacheMisses,
	)
}

func (p *storedSHAMapVerificationProgress) report(at time.Time) {
	if at.Before(p.lastReport.Add(p.interval)) {
		return
	}
	fields := p.fields(at)
	p.lastReport = at
	p.logger.Info("stored SHAMap verification progress", fields...)
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
	intervalElapsed := at.Sub(p.lastReport)
	var intervalNodesPerSecond uint64
	if intervalElapsed > 0 {
		intervalNodesPerSecond = uint64(
			float64(nodesChecked-p.lastNodes) / intervalElapsed.Seconds(),
		)
	}
	p.lastNodes = nodesChecked

	activeWorkers := p.activeWorkers.Load()
	idleWorkers := int64(p.workersStarted) - int64(activeWorkers)
	if idleWorkers < 0 {
		idleWorkers = 0
	}
	stats := p.initialStats
	if p.nodeStore != nil {
		stats = p.nodeStore.Stats()
	}
	return []any{
		"map_type", p.mapType,
		"root", p.root,
		"elapsed", elapsed.String(),
		"nodes_checked", nodesChecked,
		"nodes_per_second", nodesPerSecond,
		"interval_nodes_per_second", intervalNodesPerSecond,
		"branches_complete", p.branchesComplete.Load(),
		"branches_total", p.branchesTotal,
		"workers", p.workersResolved,
		"worker_pool_size", p.workersStarted,
		"active_workers", activeWorkers,
		"idle_workers", idleWorkers,
		"frontier_size", p.frontierSize.Load(),
		"node_store_reads_before", p.initialStats.Reads,
		"node_store_reads_after", stats.Reads,
		"node_store_read_bytes_before", p.initialStats.ReadBytes,
		"node_store_read_bytes_after", stats.ReadBytes,
		"node_cache_hits_before", p.initialStats.CacheHits,
		"node_cache_hits_after", stats.CacheHits,
		"node_cache_misses_before", p.initialStats.CacheMisses,
		"node_cache_misses_after", stats.CacheMisses,
	}
}

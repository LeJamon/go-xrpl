package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

const (
	storedSHAMapVerificationLogInterval = 15 * time.Second
	// Worker-local counts avoid a shared atomic operation on every read-only
	// verification fetch. Refresh walks need exact counts for checkpoint gating.
	storedSHAMapNodeCountBatch = 256
)

// onlineDeleteRefreshPromotionMetrics is scoped to one batched refresh.
// It records the backend's returned counters even when a batch returns an
// error; promoted counters describe writes committed before the error.
// No node identities are retained, so memory use stays constant as the refresh grows.
type onlineDeleteRefreshPromotionMetrics struct {
	mu sync.Mutex

	totals onlineDeleteRefreshPromotionTotals
}

type onlineDeleteRefreshPromotionTotals struct {
	requested             uint64
	consumed              uint64
	returned              uint64
	prefetchBytes         uint64
	partialPrefixRetries  uint64
	writableHits          uint64
	writableMisses        uint64
	archiveHits           uint64
	archiveMisses         uint64
	archiveLookups        uint64
	archiveLookupsAvoided uint64
	promoted              uint64
	promotedBytes         uint64
	bufferedBytes         uint64
	versionMismatches     uint64
	retries               uint64
	fallbacks             uint64
	batchWrites           uint64
	batchCalls            uint64
	batchErrors           uint64
	partialPrefixes       uint64
	fetchElapsed          time.Duration
	waitElapsed           time.Duration
}

func (m *onlineDeleteRefreshPromotionMetrics) record(
	waitElapsed time.Duration,
	fetchElapsed time.Duration,
	stats kvstore.PromotionStats,
	returned int,
	err error,
) {
	if waitElapsed < 0 {
		waitElapsed = 0
	}
	if fetchElapsed < 0 {
		fetchElapsed = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.totals.requested += uint64(stats.Requested)
	m.totals.consumed += uint64(stats.Consumed)
	m.totals.returned += uint64(returned)
	m.totals.prefetchBytes += uint64(stats.PrefetchBytes)
	if err == nil && returned > 0 && returned < stats.Requested {
		m.totals.partialPrefixRetries++
	}
	m.totals.writableHits += uint64(stats.WritableHits)
	m.totals.writableMisses += uint64(stats.WritableMisses)
	m.totals.archiveHits += uint64(stats.ArchiveHits)
	m.totals.archiveMisses += uint64(stats.ArchiveMisses)
	m.totals.archiveLookups += uint64(stats.ArchiveLookups)
	m.totals.archiveLookupsAvoided += uint64(stats.ArchiveLookupsAvoided)
	m.totals.promoted += uint64(stats.Promoted)
	m.totals.promotedBytes += uint64(stats.PromotedBytes)
	m.totals.bufferedBytes += uint64(stats.BufferedBytes)
	m.totals.versionMismatches += uint64(stats.VersionMismatches)
	m.totals.retries += uint64(stats.Retries)
	m.totals.fallbacks += uint64(stats.Fallbacks)
	m.totals.batchWrites += uint64(stats.Batches)
	m.totals.batchCalls++
	if err != nil {
		m.totals.batchErrors++
	}
	if stats.Consumed < stats.Requested {
		m.totals.partialPrefixes++
	}
	m.totals.fetchElapsed += fetchElapsed
	m.totals.waitElapsed += waitElapsed
}

func (m *onlineDeleteRefreshPromotionMetrics) recordWait(waitElapsed time.Duration) {
	if waitElapsed < 0 {
		waitElapsed = 0
	}
	m.mu.Lock()
	m.totals.waitElapsed += waitElapsed
	m.mu.Unlock()
}

func (m *onlineDeleteRefreshPromotionMetrics) snapshot() onlineDeleteRefreshPromotionTotals {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.totals
}

func (m *onlineDeleteRefreshPromotionMetrics) fields() []any {
	snapshot := m.snapshot()
	return []any{
		"promotion_requested", snapshot.requested,
		"promotion_consumed", snapshot.consumed,
		"promotion_returned", snapshot.returned,
		"promotion_prefetch_bytes", snapshot.prefetchBytes,
		"promotion_partial_prefix_retries", snapshot.partialPrefixRetries,
		"promotion_writable_hits", snapshot.writableHits,
		"promotion_writable_misses", snapshot.writableMisses,
		"promotion_archive_hits", snapshot.archiveHits,
		"promotion_archive_misses", snapshot.archiveMisses,
		"promotion_archive_lookups", snapshot.archiveLookups,
		"promotion_archive_lookups_avoided", snapshot.archiveLookupsAvoided,
		"promotion_promoted", snapshot.promoted,
		"promotion_promoted_bytes", snapshot.promotedBytes,
		"promotion_buffered_bytes", snapshot.bufferedBytes,
		"promotion_version_mismatches", snapshot.versionMismatches,
		"promotion_retries", snapshot.retries,
		"promotion_fallbacks", snapshot.fallbacks,
		"promotion_batch_writes", snapshot.batchWrites,
		"promotion_batch_calls", snapshot.batchCalls,
		"promotion_batch_errors", snapshot.batchErrors,
		"promotion_partial_prefixes", snapshot.partialPrefixes,
		"promotion_fetch_elapsed", snapshot.fetchElapsed.String(),
		"promotion_wait_elapsed", snapshot.waitElapsed.String(),
	}
}

type storedSHAMapVerificationProgress struct {
	logger             xrpllog.Logger
	operation          string
	reportCancellation bool
	extraFields        []any

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

	promotionMetrics *onlineDeleteRefreshPromotionMetrics
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
		operation:  "stored SHAMap verification",
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

func newOnlineDeleteRefreshProgress(
	logger xrpllog.Logger,
	nodeStore nodestore.Database,
	root [32]byte,
	mapType shamap.Type,
	sequence uint32,
	startedAt time.Time,
) *storedSHAMapVerificationProgress {
	progress := newStoredSHAMapVerificationProgress(logger, nodeStore, root, mapType, startedAt)
	progress.operation = "online delete: live-state refresh"
	progress.reportCancellation = true
	progress.extraFields = []any{"sequence", sequence}
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
	fields := append([]any(nil), p.extraFields...)
	fields = append(fields,
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
	p.logger.Info(p.operation+" started", fields...)
}

func (p *storedSHAMapVerificationProgress) report(at time.Time) {
	if at.Before(p.lastReport.Add(p.interval)) {
		return
	}
	fields := p.fields(at)
	p.lastReport = at
	p.logger.Info(p.operation+" progress", fields...)
}

func (p *storedSHAMapVerificationProgress) finish(at time.Time, err error) {
	p.start()
	fields := p.fields(at)
	if err != nil {
		message := p.operation + " failed"
		if p.reportCancellation && errors.Is(err, context.Canceled) {
			message = p.operation + " canceled"
		}
		p.logger.Warn(message, append(fields, "err", err)...)
		return
	}
	p.logger.Info(p.operation+" complete", fields...)
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
	fields := append([]any(nil), p.extraFields...)
	fields = append(fields,
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
	)
	if p.promotionMetrics != nil {
		fields = append(fields, p.promotionMetrics.fields()...)
	}
	return fields
}

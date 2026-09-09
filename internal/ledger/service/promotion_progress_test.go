package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type promotionBatchProbe struct {
	nodestore.BatchGenerationDatabase
	batchRequests atomic.Int64
	batchCalls    atomic.Int64
}

func (p *promotionBatchProbe) FetchBatchForPromotion(
	ctx context.Context,
	hashes []nodestore.Hash256,
	maxBytes int,
) ([]*nodestore.Node, kvstore.PromotionStats, error) {
	p.batchRequests.Add(int64(len(hashes)))
	p.batchCalls.Add(1)
	return p.BatchGenerationDatabase.FetchBatchForPromotion(ctx, hashes, maxBytes)
}

type promotionProgressLogRecord struct {
	Message               string `json:"msg"`
	Requested             uint64 `json:"promotion_requested"`
	Consumed              uint64 `json:"promotion_consumed"`
	Returned              uint64 `json:"promotion_returned"`
	PrefetchBytes         uint64 `json:"promotion_prefetch_bytes"`
	PartialPrefixRetries  uint64 `json:"promotion_partial_prefix_retries"`
	WritableHits          uint64 `json:"promotion_writable_hits"`
	WritableMisses        uint64 `json:"promotion_writable_misses"`
	ArchiveHits           uint64 `json:"promotion_archive_hits"`
	ArchiveMisses         uint64 `json:"promotion_archive_misses"`
	ArchiveLookups        uint64 `json:"promotion_archive_lookups"`
	ArchiveLookupsAvoided uint64 `json:"promotion_archive_lookups_avoided"`
	Promoted              uint64 `json:"promotion_promoted"`
	PromotedBytes         uint64 `json:"promotion_promoted_bytes"`
	BufferedBytes         uint64 `json:"promotion_buffered_bytes"`
	BatchWrites           uint64 `json:"promotion_batch_writes"`
	BatchCalls            uint64 `json:"promotion_batch_calls"`
	BatchErrors           uint64 `json:"promotion_batch_errors"`
	PartialPrefixes       uint64 `json:"promotion_partial_prefixes"`
	FetchElapsed          string `json:"promotion_fetch_elapsed"`
	WaitElapsed           string `json:"promotion_wait_elapsed"`
	NodeStoreReadsBefore  uint64 `json:"node_store_reads_before"`
	NodeStoreReadsAfter   uint64 `json:"node_store_reads_after"`
}

func decodePromotionProgressLogs(t *testing.T, capture *synchronizedLogBuffer) []promotionProgressLogRecord {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(capture.bytes()))
	var records []promotionProgressLogRecord
	for {
		var record promotionProgressLogRecord
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			return records
		}
		require.NoError(t, err)
		records = append(records, record)
	}
}

func TestOnlineDeleteRefreshPromotionMetricsAggregatesParallelBatches(t *testing.T) {
	const batches = 32
	metrics := &onlineDeleteRefreshPromotionMetrics{}
	var group sync.WaitGroup
	for index := range batches {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			var batchErr error
			if index%3 == 0 {
				batchErr = errors.New("batch failed")
			}
			metrics.record(
				2*time.Millisecond,
				3*time.Millisecond,
				kvstore.PromotionStats{
					Requested:             5,
					Consumed:              3,
					PrefetchBytes:         11,
					WritableHits:          1,
					WritableMisses:        2,
					ArchiveHits:           3,
					ArchiveMisses:         4,
					ArchiveLookups:        5,
					ArchiveLookupsAvoided: 6,
					Promoted:              7,
					PromotedBytes:         8,
					BufferedBytes:         9,
					VersionMismatches:     3,
					Retries:               2,
					Fallbacks:             1,
					Batches:               1,
				},
				3,
				batchErr,
			)
		}(index)
	}
	group.Wait()
	metrics.recordWait(time.Millisecond)

	snapshot := metrics.snapshot()
	require.Equal(t, uint64(batches), snapshot.batchCalls)
	require.Equal(t, uint64(batches/3+1), snapshot.batchErrors)
	require.Equal(t, uint64(batches), snapshot.partialPrefixes)
	require.Equal(t, (batches*2+1)*time.Millisecond, snapshot.waitElapsed)
	require.Equal(t, batches*3*time.Millisecond, snapshot.fetchElapsed)
	require.Equal(t, uint64(batches*5), snapshot.requested)
	require.Equal(t, uint64(batches*3), snapshot.consumed)
	require.Equal(t, uint64(batches*3), snapshot.returned)
	require.Equal(t, uint64(batches*11), snapshot.prefetchBytes)
	require.Equal(t, uint64(batches-(batches/3+1)), snapshot.partialPrefixRetries)
	require.Equal(t, uint64(batches), snapshot.writableHits)
	require.Equal(t, uint64(batches*2), snapshot.writableMisses)
	require.Equal(t, uint64(batches*3), snapshot.archiveHits)
	require.Equal(t, uint64(batches*4), snapshot.archiveMisses)
	require.Equal(t, uint64(batches*5), snapshot.archiveLookups)
	require.Equal(t, uint64(batches*6), snapshot.archiveLookupsAvoided)
	require.Equal(t, uint64(batches*7), snapshot.promoted)
	require.Equal(t, uint64(batches*8), snapshot.promotedBytes)
	require.Equal(t, uint64(batches*9), snapshot.bufferedBytes)
	require.Equal(t, uint64(batches*3), snapshot.versionMismatches)
	require.Equal(t, uint64(batches*2), snapshot.retries)
	require.Equal(t, uint64(batches), snapshot.fallbacks)
	require.Equal(t, uint64(batches), snapshot.batchWrites)
}

func TestOnlineDeleteRefreshPromotionMetricsUseUint64ByteTotals(t *testing.T) {
	const perBatch = 1 << 30
	metrics := &onlineDeleteRefreshPromotionMetrics{}
	for range 3 {
		metrics.record(0, 0, kvstore.PromotionStats{
			PromotedBytes: perBatch,
			BufferedBytes: perBatch,
			PrefetchBytes: perBatch,
		}, 0, nil)
	}

	snapshot := metrics.snapshot()
	want := uint64(3 * perBatch)
	require.Greater(t, want, uint64(1<<31-1))
	require.Equal(t, want, snapshot.promotedBytes)
	require.Equal(t, want, snapshot.bufferedBytes)
	require.Equal(t, want, snapshot.prefetchBytes)
}

func TestOnlineDeleteRefreshPromotionMetricsResetBetweenRefreshes(t *testing.T) {
	svc, db, seq := newRotatingRefreshFixture(t, 64)
	capture, logger := newVerificationLogCapture()
	svc.logger = logger
	root, err := svc.GetValidatedLedger().StateMapHash()
	require.NoError(t, err)

	beforeRequests := db.promotionRequests.Load()
	beforeBatches := db.promotionBatches.Load()
	require.NoError(t, svc.refreshGenerationStateWithBatch(
		t.Context(), root, seq, db, nil, 1, 64, storedSHAMapPromotionBatchBytes,
	))
	firstRequests := db.promotionRequests.Load() - beforeRequests
	firstBatches := db.promotionBatches.Load() - beforeBatches

	beforeRequests = db.promotionRequests.Load()
	beforeBatches = db.promotionBatches.Load()
	require.NoError(t, svc.refreshGenerationStateWithBatch(
		t.Context(), root, seq, db, nil, 1, 64, storedSHAMapPromotionBatchBytes,
	))
	secondRequests := db.promotionRequests.Load() - beforeRequests
	secondBatches := db.promotionBatches.Load() - beforeBatches

	records := decodePromotionProgressLogs(t, capture)
	require.Len(t, records, 4)
	require.Equal(t, "online delete: live-state refresh complete", records[1].Message)
	require.Equal(t, "online delete: live-state refresh complete", records[3].Message)
	// The traversal fetches its root through the scalar path before issuing
	// native batches, so the wrapper's request counter includes one extra key.
	require.EqualValues(t, firstRequests-1, records[1].Requested)
	require.EqualValues(t, secondRequests-1, records[3].Requested)
	require.EqualValues(t, firstBatches, records[1].BatchCalls)
	require.EqualValues(t, secondBatches, records[3].BatchCalls)
	require.Positive(t, records[1].BatchCalls)
	require.Positive(t, records[3].BatchCalls)
	require.NotEqual(t, uint64(firstBatches+secondBatches), records[3].BatchCalls)
}

func TestOnlineDeleteRefreshPromotionMetricsReportPartialPrefixAndSources(t *testing.T) {
	svc, db, seq := newRotatingRefreshFixture(t, 128)
	capture, logger := newVerificationLogCapture()
	root, err := svc.GetValidatedLedger().StateMapHash()
	require.NoError(t, err)
	probe := &promotionBatchProbe{BatchGenerationDatabase: db}
	refresh := &Service{nodeStore: probe, logger: logger}

	require.NoError(t, refresh.refreshGenerationStateWithBatch(
		t.Context(), root, seq, probe, nil, 1, 64, 1,
	))
	firstRequests := probe.batchRequests.Load()
	firstBatches := probe.batchCalls.Load()

	require.NoError(t, refresh.refreshGenerationStateWithBatch(
		t.Context(), root, seq, probe, nil, 1, 64, storedSHAMapPromotionBatchBytes,
	))
	secondRequests := probe.batchRequests.Load() - firstRequests
	secondBatches := probe.batchCalls.Load() - firstBatches

	records := decodePromotionProgressLogs(t, capture)
	require.Len(t, records, 4)
	first := records[1]
	second := records[3]
	require.Equal(t, "online delete: live-state refresh complete", first.Message)
	require.Equal(t, "online delete: live-state refresh complete", second.Message)
	require.Positive(t, first.BatchCalls)
	require.Positive(t, first.PartialPrefixes)
	require.Equal(t, first.PartialPrefixes, first.PartialPrefixRetries)
	require.Equal(t, first.Consumed, first.Returned)
	require.Positive(t, first.PrefetchBytes)
	require.Greater(t, first.Requested, first.Consumed)
	require.Positive(t, first.ArchiveLookups)
	require.Positive(t, first.ArchiveHits)
	require.Positive(t, first.Promoted)
	require.Positive(t, first.PromotedBytes)
	require.Positive(t, first.BufferedBytes)
	require.Positive(t, first.BatchWrites)
	require.Greater(t, first.NodeStoreReadsAfter, first.NodeStoreReadsBefore)
	require.Positive(t, second.BatchCalls)
	require.Zero(t, second.PartialPrefixes)
	require.Zero(t, second.PartialPrefixRetries)
	require.Equal(t, second.Consumed, second.Returned)
	require.Positive(t, second.PrefetchBytes)
	require.Equal(t, second.Requested, second.Consumed)
	require.Positive(t, second.WritableHits)
	require.Positive(t, second.ArchiveLookupsAvoided)
	require.Zero(t, second.ArchiveLookups)
	require.Zero(t, second.ArchiveHits)
	require.Zero(t, second.BatchWrites)

	// The scalar root/frontier walk is outside this probe; promotion totals come
	// only from native batch responses and therefore exclude those reads.
	require.EqualValues(t, firstRequests, first.Requested)
	require.EqualValues(t, firstBatches, first.BatchCalls)
	require.EqualValues(t, secondRequests, second.Requested)
	require.EqualValues(t, secondBatches, second.BatchCalls)
}

func TestOnlineDeleteRefreshPromotionMetricsReportCancellationDuringPersistence(t *testing.T) {
	svc, db, seq := newRotatingRefreshFixture(t, 256)
	root, err := svc.GetValidatedLedger().StateMapHash()
	require.NoError(t, err)
	db.storeBatchStart = make(chan struct{})
	db.storeBatchResume = make(chan struct{})
	db.promotionStart = make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(db.storeBatchResume) }) }
	t.Cleanup(release)

	next := buildLedgerWithState(t, seq+1)
	persistDone := make(chan error, 1)
	go func() {
		persistDone <- svc.persistToNodeStore(t.Context(), next, next.Sequence())
	}()
	<-db.storeBatchStart

	capture, logger := newVerificationLogCapture()
	svc.logger = logger
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- svc.refreshGenerationStateWithBatch(ctx, root, seq, db, nil, 4, 64, 4<<20)
	}()
	<-db.promotionStart
	require.Zero(t, db.promotionBatches.Load())

	cancel()
	select {
	case err := <-refreshDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("refresh cancellation waited for ledger persistence")
	}
	release()
	require.NoError(t, <-persistDone)

	records := decodePromotionProgressLogs(t, capture)
	require.Len(t, records, 2)
	require.Equal(t, "online delete: live-state refresh started", records[0].Message)
	require.Equal(t, "online delete: live-state refresh canceled", records[1].Message)
	require.Zero(t, records[1].BatchCalls)
	waitElapsed, err := time.ParseDuration(records[1].WaitElapsed)
	require.NoError(t, err)
	require.GreaterOrEqual(t, waitElapsed, time.Duration(0))
	require.Equal(t, "0s", records[1].FetchElapsed)
}

func TestOnlineDeleteRefreshPromotionMetricsRetainBackendErrorAndFailureLog(t *testing.T) {
	svc, db, seq := newRotatingRefreshFixture(t, 64)
	root, err := svc.GetValidatedLedger().StateMapHash()
	require.NoError(t, err)
	wantErr := errors.New("promotion batch failed")
	probe := &failingPromotionDatabase{BatchGenerationDatabase: db, failure: wantErr}
	capture, logger := newVerificationLogCapture()
	refresh := &Service{nodeStore: probe, logger: logger}

	err = refresh.refreshGenerationStateWithBatch(
		context.Background(), root, seq, probe, nil, 1, 64, storedSHAMapPromotionBatchBytes,
	)
	require.ErrorIs(t, err, wantErr)
	require.Zero(t, probe.syncCalls.Load())

	records := decodePromotionProgressLogs(t, capture)
	require.Len(t, records, 2)
	require.Equal(t, "online delete: live-state refresh started", records[0].Message)
	require.Equal(t, "online delete: live-state refresh failed", records[1].Message)
	require.Positive(t, records[1].Requested)
	require.Positive(t, records[1].BatchCalls)
	require.Positive(t, records[1].BatchErrors)
	require.Zero(t, records[1].PartialPrefixRetries)
	require.NotEqual(t, "online delete: live-state refresh complete", records[1].Message)
}

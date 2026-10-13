package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"runtime/pprof"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

// StateMemoryBenchmarkConfig is deliberately JSON-shaped so a before/after
// run can be launched by an external process with exactly the same workload.
// The zero value is replaced with a small smoke configuration; production
// measurements should always provide the full config explicitly.
type StateMemoryBenchmarkConfig struct {
	Seed                 uint64 `json:"seed"`
	SeedEntries          int    `json:"seed_entries"`
	ValueBytes           int    `json:"value_bytes"`
	Ledgers              int    `json:"ledgers"`
	WarmupLedgers        int    `json:"warmup_ledgers"`
	ReadsPerLedger       int    `json:"reads_per_ledger"`
	HotWritesPerLedger   int    `json:"hot_writes_per_ledger"`
	DisjointWritesLedger int    `json:"disjoint_writes_per_ledger"`
	RetainedViews        int    `json:"retained_views"`
	ScanEvery            int    `json:"scan_every"`
	ScanEntries          int    `json:"scan_entries"`
	NodeBlockCacheMiB    int    `json:"node_block_cache_mib"`
	NodeCacheItems       int    `json:"node_cache_items"`
	FlatCacheBytes       int64  `json:"flat_cache_bytes"`
	FlatMemTableBytes    int64  `json:"flat_memtable_bytes"`
	FlatBatchBytes       int    `json:"flat_batch_bytes"`
	FlatBatchRecords     int    `json:"flat_batch_records"`
	WorkspaceBytes       uint64 `json:"workspace_bytes"`
	OverlayMemoryBytes   uint64 `json:"overlay_memory_bytes"`
	OverlayDiskBytes     uint64 `json:"overlay_disk_bytes"`
	OverlaySegments      int    `json:"overlay_segments"`
	OverlayMergeFanIn    int    `json:"overlay_merge_fan_in"`
}

func (c StateMemoryBenchmarkConfig) withDefaults() StateMemoryBenchmarkConfig {
	if c.SeedEntries < 1 {
		c.SeedEntries = 256
	}
	if c.Seed == 0 {
		c.Seed = 2050
	}
	if c.ValueBytes == 0 {
		c.ValueBytes = 128
	}
	if c.ValueBytes < 12 {
		c.ValueBytes = 12
	}
	if c.Ledgers == 0 {
		c.Ledgers = 8
	}
	if c.Ledgers < 0 {
		c.Ledgers = 0
	}
	if c.WarmupLedgers < 0 {
		c.WarmupLedgers = 0
	}
	if c.ReadsPerLedger == 0 {
		c.ReadsPerLedger = 32
	}
	if c.ReadsPerLedger < 0 {
		c.ReadsPerLedger = 0
	}
	if c.HotWritesPerLedger == 0 && c.DisjointWritesLedger == 0 {
		c.HotWritesPerLedger = 4
	}
	if c.HotWritesPerLedger < 0 {
		c.HotWritesPerLedger = 0
	}
	if c.DisjointWritesLedger < 0 {
		c.DisjointWritesLedger = 0
	}
	if c.RetainedViews < 1 {
		c.RetainedViews = 4
	}
	if c.NodeBlockCacheMiB == 0 {
		c.NodeBlockCacheMiB = 8
	}
	if c.NodeCacheItems == 0 {
		c.NodeCacheItems = 256
	}
	if c.FlatCacheBytes == 0 {
		c.FlatCacheBytes = 8 << 20
	}
	if c.FlatMemTableBytes == 0 {
		c.FlatMemTableBytes = 4 << 20
	}
	if c.FlatBatchBytes == 0 {
		c.FlatBatchBytes = 1 << 20
	}
	if c.FlatBatchRecords == 0 {
		c.FlatBatchRecords = 1024
	}
	if c.WorkspaceBytes == 0 {
		c.WorkspaceBytes = 64 << 20
	}
	if c.OverlayMemoryBytes == 0 {
		c.OverlayMemoryBytes = 1 << 20
	}
	if c.OverlayDiskBytes == 0 {
		c.OverlayDiskBytes = 64 << 20
	}
	if c.OverlaySegments == 0 {
		c.OverlaySegments = 8
	}
	if c.OverlayMergeFanIn == 0 {
		c.OverlayMergeFanIn = 8
	}
	return c
}

type benchmarkChange struct {
	Key    [32]byte
	Value  []byte
	Delete bool
}

type benchmarkFlatStats struct {
	Active        bool   `json:"active"`
	Advances      uint64 `json:"advances"`
	FlatHits      uint64 `json:"flat_hits"`
	FlatFallbacks uint64 `json:"flat_fallbacks"`
	OutboxCount   int    `json:"outbox_count"`
	OutboxBytes   int64  `json:"outbox_bytes"`
	Failures      uint64 `json:"failures"`
	StorageBytes  int64  `json:"storage_bytes"`
}

type benchmarkFamilyStats struct {
	Fetches        uint64 `json:"family_fetches"`
	FetchBytes     uint64 `json:"family_fetch_bytes"`
	DurableFetches uint64 `json:"family_durable_fetches"`
	DurableBytes   uint64 `json:"family_durable_bytes"`
	StoreBatches   uint64 `json:"family_store_batches"`
	StoreEntries   uint64 `json:"family_store_entries"`
	StoreBytes     uint64 `json:"family_store_bytes"`
	Syncs          uint64 `json:"family_syncs"`
}

type benchmarkMemoryStats struct {
	WorkspacePeak uint64 `json:"workspace_peak_bytes"`
	OverlayPeak   uint64 `json:"overlay_peak_bytes"`
	SpillPeak     uint64 `json:"spill_peak_bytes"`
}

type benchmarkResolvedResources struct {
	NodeStoreMemoryCommitmentBytes uint64 `json:"node_store_memory_commitment_bytes"`
	NodeBlockCacheBytes            uint64 `json:"node_block_cache_bytes"`
	FlatCacheBytes                 uint64 `json:"flat_cache_bytes"`
	FlatMemTableBytes              uint64 `json:"flat_memtable_bytes"`
	FlatMemTableTargetBytes        uint64 `json:"flat_memtable_target_bytes"`
	WorkspaceBytes                 uint64 `json:"workspace_bytes"`
	OverlayMemoryBytes             uint64 `json:"overlay_memory_bytes"`
	OverlayDiskBytes               uint64 `json:"overlay_disk_bytes"`
	WorkspacePoolBytes             uint64 `json:"workspace_pool_bytes"`
}

// stateMemoryBenchmarkRuntime is supplied by the revision-specific adapter.
// Keeping this seam in test code lets one workload source run against both
// revisions without retaining a production fallback implementation.
type stateMemoryBenchmarkRuntime struct {
	family       *benchmarkFamily
	nodePath     string
	flatPath     string
	initialize   func(header.LedgerHeader, *shamap.SHAMap, *shamap.SHAMap) (*Ledger, error)
	persist      func(context.Context, *Ledger) error
	changes      func(context.Context, *Ledger, *Ledger) ([]benchmarkChange, error)
	advance      func(context.Context, *Ledger, *Ledger, []benchmarkChange) error
	reopen       func(context.Context, *Ledger) (*Ledger, error)
	authenticate func(context.Context, *Ledger) error
	flatStats    func() benchmarkFlatStats
	memoryStats  func() benchmarkMemoryStats
	resources    func() benchmarkResolvedResources
	release      func([]*Ledger, *Ledger)
	close        func() error
}

type benchmarkFamily struct {
	inner                                  shamap.Family
	fetches, fetchBytes                    atomic.Uint64
	durableFetches, durableBytes           atomic.Uint64
	storeBatches, storeEntries, storeBytes atomic.Uint64
	syncs                                  atomic.Uint64
}

func (f *benchmarkFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	data, err := f.inner.Fetch(ctx, hash)
	f.fetches.Add(1)
	if err == nil {
		f.fetchBytes.Add(uint64(len(data)))
	}
	return data, err
}

func (f *benchmarkFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	f.storeBatches.Add(1)
	f.storeEntries.Add(uint64(len(entries)))
	for _, entry := range entries {
		f.storeBytes.Add(uint64(len(entry.Data)))
	}
	return f.inner.StoreBatch(ctx, entries)
}

func (f *benchmarkFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	durable, ok := f.inner.(interface {
		FetchDurable(context.Context, [32]byte) ([]byte, error)
	})
	if !ok {
		return f.Fetch(ctx, hash)
	}
	data, err := durable.FetchDurable(ctx, hash)
	f.durableFetches.Add(1)
	if err == nil {
		f.durableBytes.Add(uint64(len(data)))
	}
	return data, err
}

func (f *benchmarkFamily) Sync(ctx context.Context) error {
	syncer, ok := f.inner.(interface{ Sync(context.Context) error })
	if !ok {
		return nil
	}
	f.syncs.Add(1)
	return syncer.Sync(ctx)
}

func (f *benchmarkFamily) stats() benchmarkFamilyStats {
	return benchmarkFamilyStats{
		Fetches: f.fetches.Load(), FetchBytes: f.fetchBytes.Load(),
		DurableFetches: f.durableFetches.Load(), DurableBytes: f.durableBytes.Load(),
		StoreBatches: f.storeBatches.Load(), StoreEntries: f.storeEntries.Load(), StoreBytes: f.storeBytes.Load(),
		Syncs: f.syncs.Load(),
	}
}

func (f *benchmarkFamily) reset() {
	f.fetches.Store(0)
	f.fetchBytes.Store(0)
	f.durableFetches.Store(0)
	f.durableBytes.Store(0)
	f.storeBatches.Store(0)
	f.storeEntries.Store(0)
	f.storeBytes.Store(0)
	f.syncs.Store(0)
}

func (f *benchmarkFamily) close() error { return f.inner.(interface{ Close() error }).Close() }

func benchmarkFlatStatsDelta(stats, baseline benchmarkFlatStats) benchmarkFlatStats {
	stats.Advances -= baseline.Advances
	stats.FlatHits -= baseline.FlatHits
	stats.FlatFallbacks -= baseline.FlatFallbacks
	stats.Failures -= baseline.Failures
	return stats
}

type stateMemoryBenchmarkLedgerSample struct {
	Sequence          uint32 `json:"sequence"`
	TotalNanos        int64  `json:"total_nanos"`
	CloseNanos        int64  `json:"close_nanos"`
	HashNanos         int64  `json:"hash_nanos"`
	PersistNanos      int64  `json:"persist_nanos"`
	AuthenticateNanos int64  `json:"authenticate_nanos"`
	DiffNanos         int64  `json:"diff_nanos"`
	PublishNanos      int64  `json:"publish_nanos"`
	ReadNanos         int64  `json:"read_nanos"`
	WriteNanos        int64  `json:"write_nanos"`
	ScanNanos         int64  `json:"scan_nanos"`
	Reads             int    `json:"reads"`
	Writes            int    `json:"writes"`
	ScanEntries       int    `json:"scan_entries"`
	StateRoot         string `json:"state_root"`
	LedgerHash        string `json:"ledger_hash"`
	ReadDigest        string `json:"read_digest"`
	HeapAllocBytes    uint64 `json:"heap_alloc_bytes"`
	HeapInuseBytes    uint64 `json:"heap_inuse_bytes"`
	NumGC             uint32 `json:"num_gc"`
	FlatHits          uint64 `json:"flat_hits"`
	FlatFallbacks     uint64 `json:"flat_fallbacks"`
	FlatAdvanced      bool   `json:"flat_advanced"`
}

type stateMemoryBenchmarkRuntimeStats struct {
	TotalAllocBytes uint64  `json:"total_alloc_bytes"`
	Mallocs         uint64  `json:"mallocs"`
	NumGC           uint32  `json:"num_gc"`
	PauseTotalNanos uint64  `json:"pause_total_nanos"`
	GCCPUSeconds    float64 `json:"gc_cpu_seconds"`
	HeapAllocBytes  uint64  `json:"heap_alloc_bytes"`
	HeapInuseBytes  uint64  `json:"heap_inuse_bytes"`
	SysBytes        uint64  `json:"sys_bytes"`
}

type stateMemoryBenchmarkResult struct {
	Version        int                                `json:"version"`
	Revision       string                             `json:"revision"`
	Config         StateMemoryBenchmarkConfig         `json:"config"`
	Resolved       StateMemoryBenchmarkConfig         `json:"resolved_config"`
	Samples        []stateMemoryBenchmarkLedgerSample `json:"samples"`
	P50Nanos       int64                              `json:"p50_nanos"`
	P95Nanos       int64                              `json:"p95_nanos"`
	P99Nanos       int64                              `json:"p99_nanos"`
	P50TotalNanos  int64                              `json:"p50_total_nanos"`
	P95TotalNanos  int64                              `json:"p95_total_nanos"`
	P99TotalNanos  int64                              `json:"p99_total_nanos"`
	Runtime        stateMemoryBenchmarkRuntimeStats   `json:"runtime"`
	Family         benchmarkFamilyStats               `json:"family"`
	Memory         benchmarkMemoryStats               `json:"memory"`
	Resources      benchmarkResolvedResources         `json:"resolved_resources"`
	Flat           benchmarkFlatStats                 `json:"flat"`
	NodeStoreBytes int64                              `json:"node_store_bytes"`
	FlatStoreBytes int64                              `json:"flat_store_bytes"`
	Notes          []string                           `json:"notes"`
	Final          map[string]string                  `json:"final"`
}

func TestStateMemoryBenchmark(t *testing.T) {
	if os.Getenv("STATE_BENCH_RUN") != "1" && os.Getenv("STATE_BENCH_CONFIG") == "" {
		t.Skip("set STATE_BENCH_RUN=1 or STATE_BENCH_CONFIG to run the state-memory benchmark")
	}
	c, err := readStateMemoryBenchmarkConfig()
	if err != nil {
		t.Fatal(err)
	}
	c = c.withDefaults()
	rt, err := newStateMemoryBenchmarkRuntime(t, c)
	if err != nil {
		t.Fatal(err)
	}
	var parent *Ledger
	retained := make([]*Ledger, c.RetainedViews)
	defer func() {
		if rt.release != nil {
			rt.release(retained, parent)
		}
		if err := rt.close(); err != nil {
			t.Errorf("close benchmark stores: %v", err)
		}
	}()

	ctx := context.Background()
	initialState, err := shamap.NewBacked(shamap.TypeState, rt.family)
	if err != nil {
		t.Fatal(err)
	}
	initialTx, err := shamap.NewBacked(shamap.TypeTransaction, rt.family)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([][32]byte, c.SeedEntries+(c.WarmupLedgers+c.Ledgers)*c.DisjointWritesLedger+1)
	for i := 0; i < len(keys); i++ {
		keys[i] = stateMemoryBenchmarkKey(c.Seed, i)
	}
	for i := 0; i < c.SeedEntries; i++ {
		if err := initialState.Put(keys[i], stateMemoryBenchmarkValue(c.Seed, i, 0, c.ValueBytes)); err != nil {
			t.Fatalf("seed state %d: %v", i, err)
		}
	}
	initialHeader := header.LedgerHeader{
		LedgerIndex:         1,
		Drops:               1000000000,
		CloseTime:           time.Unix(1700000000, 0).UTC(),
		CloseTimeResolution: 30,
	}
	parent, err = rt.initialize(initialHeader, initialState, initialTx)
	if err != nil {
		t.Fatalf("initialize ledger: %v", err)
	}
	if err := parent.Close(initialHeader.CloseTime, 0); err != nil {
		t.Fatalf("close initial ledger: %v", err)
	}
	if err := rt.persist(ctx, parent); err != nil {
		t.Fatalf("persist initial ledger: %v", err)
	}
	initialChanges, err := benchmarkStateChanges(ctx, parent)
	if err != nil {
		t.Fatalf("collect initial changes: %v", err)
	}
	if err := rt.advance(ctx, nil, parent, initialChanges); err != nil {
		t.Fatalf("publish initial flat state: %v", err)
	}
	if rt.reopen != nil {
		reopened, err := rt.reopen(ctx, parent)
		if err != nil {
			t.Fatalf("reopen persisted initial ledger: %v", err)
		}
		rt.release([]*Ledger{parent}, nil)
		parent = reopened
		if rt.authenticate != nil {
			if err := rt.authenticate(ctx, parent); err != nil {
				t.Fatalf("authenticate reopened initial ledger: %v", err)
			}
		}
	}
	initialState = nil

	closeTime := parent.CloseTime().Add(4 * time.Second)
	for warm := 0; warm < c.WarmupLedgers; warm++ {
		parent, closeTime = runStateMemoryBenchmarkLedger(t, ctx, rt, c, parent, closeTime, warm, keys)
	}
	retained[0] = parent

	if profilePath := os.Getenv("STATE_BENCH_CPU_PROFILE"); profilePath != "" {
		profile, err := os.Create(profilePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.StartCPUProfile(profile); err != nil {
			_ = profile.Close()
			t.Fatal(err)
		}
		defer func() {
			pprof.StopCPUProfile()
			if err := profile.Close(); err != nil {
				t.Errorf("close CPU profile: %v", err)
			}
		}()
	}
	flatBaseline := rt.flatStats()
	rt.family.reset()
	gcCPUStart := readStateMemoryBenchmarkGCCPUSeconds()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	samples := make([]stateMemoryBenchmarkLedgerSample, 0, c.Ledgers)
	readDigest := sha256.New()
	for i := 0; i < c.Ledgers; i++ {
		var sample stateMemoryBenchmarkLedgerSample
		var readNanos, writeNanos, scanNanos int64
		round := c.WarmupLedgers + i
		totalStart := time.Now()
		current, err := NewOpen(parent, closeTime)
		if err != nil {
			t.Fatalf("open ledger %d: %v", i, err)
		}
		readStart := time.Now()
		existingEntries := c.SeedEntries + round*c.DisjointWritesLedger
		for read := 0; read < c.ReadsPerLedger; read++ {
			slot := (round*131 + read*17) % existingEntries
			data, err := current.Read(keylet.Keylet{Key: keys[slot]})
			if err != nil {
				t.Fatalf("read ledger %d key %d: %v", i, slot, err)
			}
			if len(data) != c.ValueBytes {
				t.Fatalf("read ledger %d key %d returned %d bytes, want %d", i, slot, len(data), c.ValueBytes)
			}
			_, _ = readDigest.Write(keys[slot][:])
			_, _ = readDigest.Write(data)
		}
		readNanos = time.Since(readStart).Nanoseconds()
		writeStart := time.Now()
		writes := 0
		for write := 0; write < c.HotWritesPerLedger; write++ {
			slot := write % c.SeedEntries
			if err := current.Update(keylet.Keylet{Key: keys[slot]}, stateMemoryBenchmarkValue(c.Seed, slot, round+1, c.ValueBytes)); err != nil {
				t.Fatalf("hot update ledger %d key %d: %v", i, slot, err)
			}
			writes++
		}
		base := c.SeedEntries + round*c.DisjointWritesLedger
		for write := 0; write < c.DisjointWritesLedger; write++ {
			slot := base + write
			if err := current.Insert(keylet.Keylet{Key: keys[slot]}, stateMemoryBenchmarkValue(c.Seed, slot, round+1, c.ValueBytes)); err != nil {
				t.Fatalf("disjoint insert ledger %d key %d: %v", i, slot, err)
			}
			writes++
		}
		writeNanos = time.Since(writeStart).Nanoseconds()

		closeStart := time.Now()
		if err := current.Close(closeTime, 0); err != nil {
			t.Fatalf("close ledger %d: %v", i, err)
		}
		sample.HashNanos = time.Since(closeStart).Nanoseconds()
		phaseStart := time.Now()
		if err := rt.persist(ctx, current); err != nil {
			t.Fatalf("persist ledger %d: %v", i, err)
		}
		sample.PersistNanos = time.Since(phaseStart).Nanoseconds()
		phaseStart = time.Now()
		if rt.authenticate != nil {
			if err := rt.authenticate(ctx, current); err != nil {
				t.Fatalf("authenticate ledger %d: %v", i, err)
			}
		}
		sample.AuthenticateNanos = time.Since(phaseStart).Nanoseconds()
		phaseStart = time.Now()
		changes, err := rt.changes(ctx, parent, current)
		if err != nil {
			t.Fatalf("prepare changes ledger %d: %v", i, err)
		}
		sample.DiffNanos = time.Since(phaseStart).Nanoseconds()
		phaseStart = time.Now()
		if err := rt.advance(ctx, parent, current, changes); err != nil {
			t.Fatalf("publish flat state ledger %d: %v", i, err)
		}
		sample.PublishNanos = time.Since(phaseStart).Nanoseconds()
		closeNanos := time.Since(closeStart).Nanoseconds()
		if c.ScanEvery > 0 && (i+1)%c.ScanEvery == 0 {
			scanStart := time.Now()
			view, err := current.StateMapSnapshotContext(ctx)
			if err != nil {
				t.Fatalf("snapshot scan ledger %d: %v", i, err)
			}
			seen := 0
			if err := view.ForEachCtx(ctx, func(item *shamap.Item) bool {
				if c.ScanEntries > 0 && seen >= c.ScanEntries {
					return false
				}
				seen++
				return true
			}); err != nil {
				t.Fatalf("scan ledger %d: %v", i, err)
			}
			scanNanos = time.Since(scanStart).Nanoseconds()
			sample.ScanEntries = seen
		}
		totalNanos := time.Since(totalStart).Nanoseconds()
		stats := benchmarkFlatStatsDelta(rt.flatStats(), flatBaseline)
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		oldRetained := retained[i%c.RetainedViews]
		if oldRetained != nil && oldRetained != current {
			rt.release([]*Ledger{oldRetained}, nil)
		}
		retained[i%c.RetainedViews] = current
		parent = current
		closeTime = closeTime.Add(4 * time.Second)
		sample.Sequence = current.Sequence()
		sample.TotalNanos = totalNanos
		sample.CloseNanos = closeNanos
		sample.ReadNanos = readNanos
		sample.WriteNanos = writeNanos
		sample.ScanNanos = scanNanos
		sample.Reads = c.ReadsPerLedger
		sample.Writes = writes
		sample.StateRoot = shortHash(current.Header().AccountHash)
		sample.LedgerHash = shortHash(current.Hash())
		sample.ReadDigest = hex.EncodeToString(readDigest.Sum(nil))
		sample.HeapAllocBytes = mem.HeapAlloc
		sample.HeapInuseBytes = mem.HeapInuse
		sample.NumGC = mem.NumGC
		sample.FlatHits = stats.FlatHits
		sample.FlatFallbacks = stats.FlatFallbacks
		sample.FlatAdvanced = stats.Active
		samples = append(samples, sample)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	gcCPUEnd := readStateMemoryBenchmarkGCCPUSeconds()
	flatResult := benchmarkFlatStatsDelta(rt.flatStats(), flatBaseline)
	flatResult.StorageBytes = directoryBytes(rt.flatPath)

	result := stateMemoryBenchmarkResult{
		Version: 1, Revision: benchmarkRevisionName(), Config: c, Resolved: c,
		Samples:  samples,
		P50Nanos: percentile(samples, 0.50), P95Nanos: percentile(samples, 0.95), P99Nanos: percentile(samples, 0.99),
		P50TotalNanos: percentileTotal(samples, 0.50), P95TotalNanos: percentileTotal(samples, 0.95), P99TotalNanos: percentileTotal(samples, 0.99),
		Runtime: stateMemoryBenchmarkRuntimeStats{
			TotalAllocBytes: after.TotalAlloc - before.TotalAlloc, Mallocs: after.Mallocs - before.Mallocs,
			NumGC: after.NumGC - before.NumGC, PauseTotalNanos: after.PauseTotalNs - before.PauseTotalNs,
			GCCPUSeconds:   gcCPUEnd - gcCPUStart,
			HeapAllocBytes: after.HeapAlloc, HeapInuseBytes: after.HeapInuse, SysBytes: after.Sys,
		},
		Family: rt.family.stats(), Memory: rt.memoryStats(), Resources: rt.resources(), Flat: flatResult,
		NodeStoreBytes: directoryBytes(rt.nodePath), FlatStoreBytes: directoryBytes(rt.flatPath),
		Notes: []string{
			"family fetch/store counters are logical SHAMap operations; process and filesystem metrics are external observations",
			"the baseline adapter folds flat_cache_bytes into its node block-cache allowance; flat memtable and overlay budgets remain revision-specific and are reported in resolved_config and process memory",
			"the measured cold boundary reopens persisted ledger roots with decoded SHAMap state discarded; Pebble memtables, Pebble caches, and the OS page cache are retained",
			"p50_nanos, p95_nanos, and p99_nanos cover close/persist/diff/publication; p50_total_nanos, p95_total_nanos, and p99_total_nanos include successor open, reads, writes, and optional scan",
			"the timed boundary includes ledger Close, node persistence, root diff, flat publication, and outbox acknowledgement; ledger header/index publication is outside this state-only harness",
		},
		Final: finalStateMemoryBenchmarkDigests(parent),
	}
	result.Final["read_digest"] = hex.EncodeToString(readDigest.Sum(nil))
	writeStateMemoryBenchmarkResult(t, result)
}

func runStateMemoryBenchmarkLedger(t *testing.T, ctx context.Context, rt *stateMemoryBenchmarkRuntime, c StateMemoryBenchmarkConfig, parent *Ledger, closeTime time.Time, round int, keys [][32]byte) (*Ledger, time.Time) {
	current, err := NewOpen(parent, closeTime)
	if err != nil {
		t.Fatalf("open warmup ledger %d: %v", round, err)
	}
	for read := 0; read < c.ReadsPerLedger; read++ {
		existingEntries := c.SeedEntries + round*c.DisjointWritesLedger
		slot := (round*131 + read*17) % existingEntries
		data, err := current.Read(keylet.Keylet{Key: keys[slot]})
		if err != nil {
			t.Fatalf("read warmup ledger %d: %v", round, err)
		}
		if len(data) != c.ValueBytes {
			t.Fatalf("read warmup ledger %d key %d returned %d bytes, want %d", round, slot, len(data), c.ValueBytes)
		}
	}
	for write := 0; write < c.HotWritesPerLedger; write++ {
		slot := write % c.SeedEntries
		if err := current.Update(keylet.Keylet{Key: keys[slot]}, stateMemoryBenchmarkValue(c.Seed, slot, round+1, c.ValueBytes)); err != nil {
			t.Fatalf("update warmup ledger %d: %v", round, err)
		}
	}
	base := c.SeedEntries + round*c.DisjointWritesLedger
	for write := 0; write < c.DisjointWritesLedger; write++ {
		slot := base + write
		if err := current.Insert(keylet.Keylet{Key: keys[slot]}, stateMemoryBenchmarkValue(c.Seed, slot, round+1, c.ValueBytes)); err != nil {
			t.Fatalf("insert warmup ledger %d: %v", round, err)
		}
	}
	if err := current.Close(closeTime, 0); err != nil {
		t.Fatalf("close warmup ledger %d: %v", round, err)
	}
	if err := rt.persist(ctx, current); err != nil {
		t.Fatalf("persist warmup ledger %d: %v", round, err)
	}
	if rt.authenticate != nil {
		if err := rt.authenticate(ctx, current); err != nil {
			t.Fatalf("authenticate warmup ledger %d: %v", round, err)
		}
	}
	changes, err := rt.changes(ctx, parent, current)
	if err != nil {
		t.Fatalf("prepare warmup changes %d: %v", round, err)
	}
	if err := rt.advance(ctx, parent, current, changes); err != nil {
		t.Fatalf("publish warmup ledger %d: %v", round, err)
	}
	rt.release([]*Ledger{parent}, nil)
	return current, closeTime.Add(4 * time.Second)
}

func benchmarkStateChanges(ctx context.Context, current *Ledger) ([]benchmarkChange, error) {
	view, err := current.StateMapSnapshotContext(ctx)
	if err != nil {
		return nil, err
	}
	changes := make([]benchmarkChange, 0, view.Size())
	if err := view.ForEachCtx(ctx, func(item *shamap.Item) bool {
		changes = append(changes, benchmarkChange{Key: item.Key(), Value: append([]byte(nil), item.Data()...)})
		return true
	}); err != nil {
		return nil, err
	}
	sort.Slice(changes, func(i, j int) bool { return string(changes[i].Key[:]) < string(changes[j].Key[:]) })
	return changes, nil
}

func stateMemoryBenchmarkKey(seed uint64, slot int) [32]byte {
	var data [16]byte
	binary.BigEndian.PutUint64(data[:8], seed)
	binary.BigEndian.PutUint64(data[8:], uint64(slot))
	return sha256.Sum256(append([]byte("go-xrpl/state-memory/v1"), data[:]...))
}

func stateMemoryBenchmarkValue(seed uint64, slot, version, size int) []byte {
	if size < 12 {
		size = 12
	}
	value := make([]byte, size)
	var input [32]byte
	binary.BigEndian.PutUint64(input[:8], seed)
	binary.BigEndian.PutUint64(input[8:16], uint64(slot))
	binary.BigEndian.PutUint64(input[16:24], uint64(version))
	for block, offset := uint64(0), 0; offset < len(value); block, offset = block+1, offset+sha256.Size {
		binary.BigEndian.PutUint64(input[24:], block)
		hash := sha256.New()
		_, _ = hash.Write([]byte("go-xrpl/state-memory/value/v2"))
		_, _ = hash.Write(input[:])
		digest := hash.Sum(nil)
		copy(value[offset:], digest)
	}
	return value
}

func readStateMemoryBenchmarkConfig() (StateMemoryBenchmarkConfig, error) {
	value := os.Getenv("STATE_BENCH_CONFIG")
	if value == "" {
		return StateMemoryBenchmarkConfig{}, nil
	}
	var config StateMemoryBenchmarkConfig
	if err := json.Unmarshal([]byte(value), &config); err != nil {
		return StateMemoryBenchmarkConfig{}, fmt.Errorf("parse STATE_BENCH_CONFIG: %w", err)
	}
	return config, nil
}

func readStateMemoryBenchmarkGCCPUSeconds() float64 {
	samples := []metrics.Sample{{Name: "/cpu/classes/gc/total:cpu-seconds"}}
	metrics.Read(samples)
	return samples[0].Value.Float64()
}

func percentile(samples []stateMemoryBenchmarkLedgerSample, quantile float64) int64 {
	values := make([]int64, 0, len(samples))
	for _, sample := range samples {
		values = append(values, sample.CloseNanos)
	}
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := int(math.Ceil(quantile*float64(len(values)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}

func percentileTotal(samples []stateMemoryBenchmarkLedgerSample, quantile float64) int64 {
	values := make([]int64, 0, len(samples))
	for _, sample := range samples {
		values = append(values, sample.TotalNanos)
	}
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := int(math.Ceil(quantile*float64(len(values)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}

func shortHash(hash [32]byte) string { return hex.EncodeToString(hash[:]) }

func directoryBytes(path string) int64 {
	if path == "" {
		return 0
	}
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func writeStateMemoryBenchmarkResult(t *testing.T, result stateMemoryBenchmarkResult) {
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	output := os.Getenv("STATE_BENCH_OUTPUT")
	if output == "" {
		t.Log(string(encoded))
		return
	}
	if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		t.Fatalf("create result directory: %v", err)
	}
	if err := os.WriteFile(output, append(encoded, '\n'), 0644); err != nil {
		t.Fatalf("write benchmark result: %v", err)
	}
	t.Logf("state-memory benchmark result: %s", output)
}

func finalStateMemoryBenchmarkDigests(parent *Ledger) map[string]string {
	h := parent.Header()
	ledgerHash := parent.Hash()
	return map[string]string{
		"sequence":    fmt.Sprint(parent.Sequence()),
		"ledger_hash": hex.EncodeToString(ledgerHash[:]),
		"state_root":  hex.EncodeToString(h.AccountHash[:]),
		"tx_root":     hex.EncodeToString(h.TxHash[:]),
	}
}

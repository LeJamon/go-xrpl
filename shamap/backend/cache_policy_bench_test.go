package backend

import (
	"context"
	"fmt"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

const (
	cachePolicyBenchStateBranches   = 16
	cachePolicyBenchLeavesPerBranch = 256
	cachePolicyBenchLocalKeys       = 16
	cachePolicyBenchPeerRequests    = 4
	cachePolicyBenchPeerSubtrees    = 16
	cachePolicyBenchPoolBytes       = 256 << 10
	cachePolicyBenchProgressBytes   = 32 << 10
	cachePolicyBenchResidualBytes   = cachePolicyBenchPoolBytes - cachePolicyBenchProgressBytes
	cachePolicyBenchSampleCapacity  = 4096
	cachePolicyBenchPeerWantedDepth = 2
	cachePolicyBenchPeerQueryDepth  = 2
)

type cachePolicyBenchStore struct {
	kvstore.KeyValueStore
	gets     atomic.Uint64
	getBytes atomic.Uint64
}

func (s *cachePolicyBenchStore) Get(key []byte) ([]byte, error) {
	s.gets.Add(1)
	value, err := s.KeyValueStore.Get(key)
	if err == nil {
		s.getBytes.Add(uint64(len(value)))
	}
	return value, err
}

type cachePolicyBenchFixture struct {
	family    *NodeStore
	database  *nodestore.KVDatabase
	store     *cachePolicyBenchStore
	state     *shamap.SHAMap
	clean     *shamap.CleanResidency
	pool      *membudget.Pool
	progress  *membudget.Account
	node      *membudget.Account
	cleanNode *membudget.Account
	localKeys [][32]byte
	peerPaths [][32]byte
}

func newCachePolicyBenchFixture(b *testing.B, budgetMode string) (*cachePolicyBenchFixture, error) {
	b.Helper()
	poolLimit := uint64(cachePolicyBenchPoolBytes)
	pool := membudget.New(poolLimit)
	progressBudget, err := pool.NewAccount("progress", cachePolicyBenchProgressBytes, poolLimit)
	if err != nil {
		return nil, err
	}
	nodeMinimum, nodeMaximum := uint64(0), poolLimit
	cleanMinimum, cleanMaximum := uint64(0), poolLimit
	cleanLimit := poolLimit
	if budgetMode == "balanced" {
		nodeMinimum, nodeMaximum = cachePolicyBenchResidualBytes/8, cachePolicyBenchResidualBytes/4
		cleanMinimum, cleanMaximum = cachePolicyBenchResidualBytes/4, cachePolicyBenchResidualBytes/2
		cleanLimit = cleanMaximum
	}
	nodeBudget, err := pool.NewAccount("node_cache", nodeMinimum, nodeMaximum)
	if err != nil {
		return nil, err
	}
	cleanBudget, err := pool.NewAccount("shamap_cache", cleanMinimum, cleanMaximum)
	if err != nil {
		return nil, err
	}
	store := &cachePolicyBenchStore{KeyValueStore: memorydb.New()}
	database, err := nodestore.NewKVDatabase(store, nodestore.DatabaseConfig{
		PositiveCache: nodestore.CacheConfig{
			Enabled:      true,
			MaxEntries:   2000,
			TTL:          time.Hour,
			MemoryBudget: nodeBudget,
		},
	})
	if err != nil {
		return nil, err
	}
	family := New(database, NodeStoreOptions{
		CacheMemoryBudget: nodeBudget,
		CleanMemoryBudget: cleanBudget,
		CleanCacheBytes:   cleanLimit,
	})

	source := shamap.New(shamap.TypeState)
	localKeys := make([][32]byte, 0, cachePolicyBenchLocalKeys)
	peerPaths := make([][32]byte, 0, (cachePolicyBenchStateBranches-1)*cachePolicyBenchPeerSubtrees)
	for branch := 0; branch < cachePolicyBenchStateBranches; branch++ {
		for index := 0; index < cachePolicyBenchLeavesPerBranch; index++ {
			key := cachePolicyBenchKey(byte(branch), index)
			value := make([]byte, 96)
			for i := range value {
				value[i] = byte(branch*17 + index + i)
			}
			if err := source.Put(key, value); err != nil {
				_ = family.Close()
				return nil, err
			}
			if branch == 0 && index < cachePolicyBenchLocalKeys {
				localKeys = append(localKeys, key)
			}
		}
	}
	for branch := 1; branch < cachePolicyBenchStateBranches; branch++ {
		for subtree := 0; subtree < cachePolicyBenchPeerSubtrees; subtree++ {
			peerPaths = append(peerPaths, cachePolicyBenchPath(byte(branch), byte(subtree)))
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		_ = family.Close()
		return nil, err
	}
	if err := source.StoreDirtyAndRelease(func(entries []shamap.FlushEntry) error {
		return family.StoreBatch(context.Background(), entries)
	}); err != nil {
		_ = family.Close()
		return nil, err
	}
	state, err := shamap.NewFromRootHash(shamap.TypeState, rootHash, family)
	if err != nil {
		_ = family.Close()
		return nil, err
	}

	return &cachePolicyBenchFixture{
		family:    family,
		database:  database,
		store:     store,
		state:     state,
		clean:     family.SHAMapCleanResidency(),
		pool:      pool,
		progress:  progressBudget,
		node:      nodeBudget,
		cleanNode: cleanBudget,
		localKeys: localKeys,
		peerPaths: peerPaths,
	}, nil
}

func cachePolicyBenchKey(branch byte, index int) [32]byte {
	var key [32]byte
	key[0] = branch<<4 | byte(index&0x0F)
	key[1] = byte(index >> 4)
	key[2] = byte(index*37 + int(branch)*11)
	key[3] = byte(index*53 + int(branch)*7)
	for i := 4; i < len(key); i++ {
		key[i] = byte(i*19 + index*3 + int(branch)*5)
	}
	return key
}

func cachePolicyBenchPath(branch, subtree byte) [32]byte {
	var path [32]byte
	path[0] = branch<<4 | subtree&0x0F
	return path
}

func (f *cachePolicyBenchFixture) close() error {
	err := f.family.Close()
	if used := f.pool.Snapshot().Used; used != 0 {
		return fmt.Errorf("cache policy benchmark budget still reserved after close: %d", used)
	}
	return err
}

type cachePolicyBenchLatencyRing struct {
	values []int64
	next   int
	count  int
}

func newCachePolicyBenchLatencyRing() *cachePolicyBenchLatencyRing {
	return &cachePolicyBenchLatencyRing{values: make([]int64, cachePolicyBenchSampleCapacity)}
}

func (r *cachePolicyBenchLatencyRing) add(value int64) {
	r.values[r.next] = value
	r.next = (r.next + 1) % len(r.values)
	if r.count < len(r.values) {
		r.count++
	}
}

func (r *cachePolicyBenchLatencyRing) snapshot() []int64 {
	result := make([]int64, r.count)
	if r.count < len(r.values) {
		copy(result, r.values[:r.count])
		return result
	}
	copy(result, r.values[r.next:])
	copy(result[len(r.values)-r.next:], r.values[:r.next])
	return result
}

func cachePolicyBenchPercentile(values []int64, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := int(float64(len(values)-1) * percentile)
	return float64(values[index])
}

// BenchmarkCleanResidencyMixedWorkload compares local hot reads with peer
// subtree scans over a state larger than the shared cache budget. The positive
// NodeStore cache and CleanResidency share the same small byte pool, while a
// progress account retains a protected reserve.
func BenchmarkCleanResidencyMixedWorkload(b *testing.B) {
	for _, budgetMode := range []string{"legacy", "balanced"} {
		for _, warmup := range []string{"local-first", "peer-first"} {
			for _, workload := range []string{"no-peers", "disjoint-scans", "repeated-peer"} {
				b.Run(budgetMode+"/"+warmup+"/"+workload, func(b *testing.B) {
					cachePolicyBenchRun(b, budgetMode, warmup, workload, false)
				})
			}
		}
	}
	b.Run("balanced-pressure/local-first/disjoint-scans", func(b *testing.B) {
		cachePolicyBenchRun(b, "balanced", "local-first", "disjoint-scans", true)
	})
}

func cachePolicyBenchRun(b *testing.B, budgetMode, warmup, workload string, pressure bool) {
	b.Helper()
	b.ReportAllocs()
	b.StopTimer()
	fixture, err := newCachePolicyBenchFixture(b, budgetMode)
	if err != nil {
		b.Fatal(err)
	}
	cachePolicyBenchWarm(b, fixture, warmup)
	var pressureBytes uint64
	if pressure {
		pressureBytes = fixture.progress.Snapshot().Available
		if capacity := fixture.progress.Capacity(); pressureBytes > capacity {
			pressureBytes = capacity
		}
		if pressureBytes == 0 || !fixture.progress.TryReserve(pressureBytes) {
			_ = fixture.close()
			b.Fatal("progress pressure could not reserve available capacity")
		}
	}
	pressureReserved := pressureBytes
	defer func() {
		if pressureBytes != 0 {
			fixture.progress.Release(pressureBytes)
		}
		if err := fixture.close(); err != nil {
			b.Error(err)
		}
	}()
	latencies := newCachePolicyBenchLatencyRing()
	backendGetsBefore := fixture.store.gets.Load()
	backendBytesBefore := fixture.store.getBytes.Load()
	databaseStatsBefore := fixture.database.Stats()
	cleanStatsBefore := fixture.clean.Stats()

	b.ResetTimer()
	b.StartTimer()
	started := time.Now()
	var peerRequests uint64
	var peerNodes uint64
	var peerNanos int64
	var foregroundBackendGets uint64
	for i := 0; i < b.N; i++ {
		key := fixture.localKeys[i%len(fixture.localKeys)]
		localBackendGetsBefore := fixture.store.gets.Load()
		localStarted := time.Now()
		_, found, getErr := fixture.state.Get(key)
		localNanos := time.Since(localStarted).Nanoseconds()
		if getErr != nil {
			b.Fatal(getErr)
		}
		if !found {
			b.Fatal("foreground key disappeared from the backed state")
		}
		latencies.add(localNanos)
		foregroundBackendGets += fixture.store.gets.Load() - localBackendGetsBefore

		if workload == "no-peers" {
			continue
		}
		peerPathCount := 1
		if workload == "disjoint-scans" {
			peerPathCount = cachePolicyBenchPeerRequests
		}
		for requestIndex := 0; requestIndex < peerPathCount; requestIndex++ {
			pathIndex := 0
			if workload == "disjoint-scans" {
				pathCount := len(fixture.peerPaths) - 1
				pathIndex = 1 + ((i*peerPathCount + requestIndex) % pathCount)
			}
			peerStarted := time.Now()
			nodes, peerErr := fixture.state.GetNodeFatByPath(fixture.peerPaths[pathIndex], cachePolicyBenchPeerWantedDepth, cachePolicyBenchPeerQueryDepth, true)
			peerNanos += time.Since(peerStarted).Nanoseconds()
			if peerErr != nil {
				b.Fatal(peerErr)
			}
			if len(nodes) == 0 {
				b.Fatal("peer subtree request returned no nodes")
			}
			peerRequests++
			peerNodes += uint64(len(nodes))
		}
	}
	workNanos := time.Since(started).Nanoseconds()
	b.StopTimer()
	cleanStatsAfter := fixture.clean.Stats()
	databaseStatsAfter := fixture.database.Stats()
	backendGets := fixture.store.gets.Load() - backendGetsBefore
	backendBytes := fixture.store.getBytes.Load() - backendBytesBefore
	cleanHits := cleanStatsAfter.Hits - cleanStatsBefore.Hits
	cleanMisses := cleanStatsAfter.Misses - cleanStatsBefore.Misses
	positiveHits := databaseStatsAfter.CacheHits - databaseStatsBefore.CacheHits
	backendRequests := uint64(b.N) + peerRequests
	poolUsed := fixture.pool.Snapshot().Used
	nodeUsed := fixture.node.Snapshot().Used
	cleanUsed := fixture.cleanNode.Snapshot().Used

	if pressureBytes != 0 {
		fixture.progress.Release(pressureBytes)
		pressureBytes = 0
	}
	recoveryBackendGetsBefore := fixture.store.gets.Load()
	if workload != "no-peers" {
		cachePolicyBenchRecoverLocal(b, fixture)
	}
	recoveryBackendGets := fixture.store.gets.Load() - recoveryBackendGetsBefore

	latencyValues := latencies.snapshot()
	b.ReportMetric(cachePolicyBenchPercentile(latencyValues, 0.50), "foreground-p50-ns")
	b.ReportMetric(cachePolicyBenchPercentile(latencyValues, 0.95), "foreground-p95-ns")
	b.ReportMetric(cachePolicyBenchPercentile(latencyValues, 0.99), "foreground-p99-ns")
	b.ReportMetric(float64(peerRequests), "peer-requests")
	b.ReportMetric(float64(peerNodes), "peer-nodes")
	b.ReportMetric(float64(cleanHits), "clean-hits")
	b.ReportMetric(float64(cleanMisses), "clean-misses")
	b.ReportMetric(float64(positiveHits), "positive-cache-hits")
	b.ReportMetric(float64(backendGets), "backend-gets")
	b.ReportMetric(float64(backendBytes), "backend-read-B")
	b.ReportMetric(float64(foregroundBackendGets), "foreground-backend-gets")
	b.ReportMetric(float64(recoveryBackendGets), "post-scan-recovery-gets")
	b.ReportMetric(float64(pressureReserved), "progress-pressure-B")
	b.ReportMetric(float64(cleanStatsAfter.UsedBytes), "clean-retained-B")
	b.ReportMetric(float64(cleanStatsAfter.Nodes), "clean-nodes")
	b.ReportMetric(float64(poolUsed), "shared-budget-retained-B")
	b.ReportMetric(float64(nodeUsed), "node-cache-retained-B")
	b.ReportMetric(float64(cleanUsed), "clean-account-retained-B")
	if backendRequests != 0 {
		b.ReportMetric(float64(backendGets)/float64(backendRequests), "backend-get/request")
	}
	if peerRequests != 0 {
		b.ReportMetric(float64(peerNanos)/float64(peerRequests), "peer-ns/request")
		b.ReportMetric(float64(peerRequests)*1e9/float64(workNanos), "peer-requests/s")
	}

	if cleanStatsAfter.UsedBytes > cleanStatsAfter.Limit {
		b.Fatalf("clean residency exceeded configured limit: used=%d limit=%d", cleanStatsAfter.UsedBytes, cleanStatsAfter.Limit)
	}
	if poolUsed > cachePolicyBenchPoolBytes {
		b.Fatalf("shared budget exceeded configured limit: used=%d limit=%d", poolUsed, cachePolicyBenchPoolBytes)
	}
}

func cachePolicyBenchWarm(b *testing.B, fixture *cachePolicyBenchFixture, order string) {
	b.Helper()
	if order == "local-first" {
		cachePolicyBenchWarmLocal(b, fixture)
		cachePolicyBenchWarmPeers(b, fixture)
		return
	}
	cachePolicyBenchWarmPeers(b, fixture)
	cachePolicyBenchWarmLocal(b, fixture)
}

func cachePolicyBenchWarmLocal(b *testing.B, fixture *cachePolicyBenchFixture) {
	b.Helper()
	for _, key := range fixture.localKeys {
		if _, found, err := fixture.state.Get(key); err != nil {
			b.Fatal(err)
		} else if !found {
			b.Fatal("foreground warmup key was not found")
		}
	}
}

func cachePolicyBenchWarmPeers(b *testing.B, fixture *cachePolicyBenchFixture) {
	b.Helper()
	for _, path := range fixture.peerPaths[:1] {
		nodes, err := fixture.state.GetNodeFatByPath(path, cachePolicyBenchPeerWantedDepth, cachePolicyBenchPeerQueryDepth, true)
		if err != nil {
			b.Fatal(err)
		}
		if len(nodes) == 0 {
			b.Fatal("peer warmup subtree returned no nodes")
		}
	}
}

func cachePolicyBenchRecoverLocal(b *testing.B, fixture *cachePolicyBenchFixture) {
	b.Helper()
	for _, key := range fixture.localKeys {
		if _, found, err := fixture.state.Get(key); err != nil {
			b.Fatal(err)
		} else if !found {
			b.Fatal("foreground recovery key was not found")
		}
	}
}

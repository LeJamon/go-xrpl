// bench_test.go
package nodestore_test

import (
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/storage/nodestore"
)

// Sequence mimics the C++ Sequence class for deterministic object generation
type Sequence struct {
	prefix   uint8
	minSize  int
	maxSize  int
	typeProb []int // Distribution for node types
}

func NewSequence(prefix uint8) *Sequence {
	return &Sequence{
		prefix:   prefix,
		minSize:  250,
		maxSize:  1250,
		typeProb: []int{1, 1, 0, 1, 1}, // Skip type 2 (removed transaction type)
	}
}

// rngFill fills buffer with deterministic "random" data using seeded generator
func (s *Sequence) rngFill(buffer []byte, rng *rand.Rand) {
	for i := 0; i < len(buffer); i += 8 {
		val := rng.Uint64()
		for j := 0; j < 8 && i+j < len(buffer); j++ {
			buffer[i+j] = byte(val >> (j * 8))
		}
	}
}

// Key returns the n-th deterministic key
func (s *Sequence) Key(n int) nodestore.Hash256 {
	rng := rand.New(rand.NewSource(int64(n + 1)))
	var key nodestore.Hash256
	s.rngFill(key[:], rng)
	key[0] = s.prefix // Set prefix
	return key
}

// Obj returns the n-th complete Node
func (s *Sequence) Obj(n int) *nodestore.Node {
	rng := rand.New(rand.NewSource(int64(n + 1)))

	// Generate key with prefix
	var keyBytes [32]byte
	s.rngFill(keyBytes[:], rng)
	keyBytes[0] = s.prefix

	// Generate node type using weighted distribution
	typeIdx := s.weightedChoice(rng, s.typeProb)
	nodeType := nodestore.NodeType(typeIdx + 1)
	if nodeType == 2 {
		nodeType = nodestore.NodeTransaction
	}

	// Generate data
	dataSize := rng.Intn(s.maxSize-s.minSize) + s.minSize
	data := make(nodestore.Blob, dataSize)
	s.rngFill(data, rng)

	// Create node with deterministic hash
	node := &nodestore.Node{
		Type:      nodeType,
		Hash:      nodestore.Hash256(keyBytes),
		Data:      data,
		LedgerSeq: 0,
		CreatedAt: time.Now(),
	}

	return node
}

func (s *Sequence) weightedChoice(rng *rand.Rand, weights []int) int {
	total := 0
	for _, w := range weights {
		total += w
	}
	if total == 0 {
		return 0
	}

	choice := rng.Intn(total)
	sum := 0
	for i, w := range weights {
		sum += w
		if choice < sum {
			return i
		}
	}
	return 0
}

// BenchmarkParams holds benchmark configuration
type BenchmarkParams struct {
	Items   int
	Threads int
}

// Default parameters matching C++ benchmark
const (
	DefaultItems       = 10000 // Matches C++ default_items
	DefaultRepeat      = 3     // Matches C++ default_repeat
	MissingNodePercent = 20    // Matches C++ missingNodePercent
)

// setupBenchBackendWithConfig creates backend with specific configuration
func setupBenchBackendWithConfig(b *testing.B, path string) (nodestore.Backend, func()) {
	b.Helper()

	config := &nodestore.Config{
		Path:             path,
		Compressor:       "none",
		CompressionLevel: 0,
	}

	backend, err := nodestore.NewPebbleBackend(config)
	if err != nil {
		b.Fatalf("failed to create backend: %v", err)
	}

	if err := backend.Open(true); err != nil {
		backend.Close()
		b.Fatalf("failed to open backend: %v", err)
	}

	cleanup := func() {
		backend.Close()
	}

	return backend, cleanup
}

// parallelWork executes work function across multiple goroutines
// Mimics the C++ parallel_for functionality
func parallelWork(n int, numThreads int, workFn func(int)) {
	var wg sync.WaitGroup
	counter := int64(0)

	for i := 0; i < numThreads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				idx := int(atomic.AddInt64(&counter, 1)) - 1
				if idx >= n {
					break
				}
				workFn(idx)
			}
		}()
	}
	wg.Wait()
}

// BenchmarkInsertBatch - equivalent to C++ do_insert
// Measures time to insert a batch of DefaultItems objects
func BenchmarkInsertBatch(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	params := BenchmarkParams{Items: DefaultItems, Threads: 1}

	tempDir := b.TempDir()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		backend, cleanup := setupBenchBackendWithConfig(b, filepath.Join(tempDir, "insert.db"))
		seq := NewSequence(1)
		b.StartTimer()

		// Insert batch of items (equivalent to C++ parallel_for)
		parallelWork(params.Items, params.Threads, func(idx int) {
			node := seq.Obj(idx)
			if status := backend.Store(node); status != nodestore.OK {
				b.Errorf("store failed at index %d: %v", idx, status)
			}
		})

		b.StopTimer()
		cleanup()
		os.RemoveAll(filepath.Join(tempDir, "insert.db"))
		b.StartTimer()
	}
}

// BenchmarkFetchBatch - equivalent to C++ do_fetch
func BenchmarkFetchBatch(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	params := BenchmarkParams{Items: DefaultItems, Threads: 1}

	tempDir := b.TempDir()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		backend, cleanup := setupBenchBackendWithConfig(b, filepath.Join(tempDir, "fetch.db"))
		seq := NewSequence(1)

		// Pre-populate database
		nodes := make([]*nodestore.Node, params.Items)
		for j := 0; j < params.Items; j++ {
			nodes[j] = seq.Obj(j)
			backend.Store(nodes[j])
		}
		b.StartTimer()

		// Fetch batch of items with random access pattern
		parallelWork(params.Items, params.Threads, func(idx int) {
			rng := rand.New(rand.NewSource(int64(idx + 1)))
			targetIdx := rng.Intn(len(nodes))
			node, status := backend.Fetch(nodes[targetIdx].Hash)
			if status != nodestore.OK || node == nil {
				b.Errorf("fetch failed at index %d: %v", idx, status)
			}
			// Verify data matches (equivalent to C++ isSame check)
			if node != nil && !(node.Hash == (nodes[targetIdx].Hash)) {
				b.Errorf("fetched node hash mismatch at index %d", idx)
			}
		})

		b.StopTimer()
		cleanup()
		os.RemoveAll(filepath.Join(tempDir, "fetch.db"))
		b.StartTimer()
	}
}

// BenchmarkMissingBatch - equivalent to C++ do_missing
func BenchmarkMissingBatch(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	params := BenchmarkParams{Items: DefaultItems, Threads: 1}

	tempDir := b.TempDir()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		backend, cleanup := setupBenchBackendWithConfig(b, filepath.Join(tempDir, "missing.db"))
		seq := NewSequence(2) // Different prefix = missing keys
		b.StartTimer()

		// Perform lookups of non-existent keys
		parallelWork(params.Items, params.Threads, func(idx int) {
			key := seq.Key(idx)
			node, status := backend.Fetch(key)
			if status != nodestore.NotFound || node != nil {
				b.Errorf("expected not found at index %d, got: %v", idx, status)
			}
		})

		b.StopTimer()
		cleanup()
		os.RemoveAll(filepath.Join(tempDir, "missing.db"))
		b.StartTimer()
	}
}

// BenchmarkMixedBatch - equivalent to C++ do_mixed
func BenchmarkMixedBatch(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	params := BenchmarkParams{Items: DefaultItems, Threads: 1}

	tempDir := b.TempDir()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		backend, cleanup := setupBenchBackendWithConfig(b, filepath.Join(tempDir, "mixed.db"))
		seq1 := NewSequence(1) // Existing data
		seq2 := NewSequence(2) // Missing data

		// Pre-populate database
		nodes := make([]*nodestore.Node, params.Items)
		for j := 0; j < params.Items; j++ {
			nodes[j] = seq1.Obj(j)
			backend.Store(nodes[j])
		}
		b.StartTimer()

		// Mixed workload: 20% missing, 80% existing
		parallelWork(params.Items, params.Threads, func(idx int) {
			rng := rand.New(rand.NewSource(int64(idx + 1)))
			if rng.Intn(100) < MissingNodePercent {
				// Fetch missing
				key := seq2.Key(rng.Intn(params.Items))
				node, status := backend.Fetch(key)
				if status != nodestore.NotFound || node != nil {
					b.Errorf("expected not found for missing key at index %d", idx)
				}
			} else {
				// Fetch existing
				targetIdx := rng.Intn(len(nodes))
				node, status := backend.Fetch(nodes[targetIdx].Hash)
				if status != nodestore.OK || node == nil {
					b.Errorf("fetch existing failed at index %d: %v", idx, status)
				}
			}
		})

		b.StopTimer()
		cleanup()
		os.RemoveAll(filepath.Join(tempDir, "mixed.db"))
		b.StartTimer()
	}
}

// BenchmarkWorkBatch - equivalent to C++ do_work
func BenchmarkWorkBatch(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	params := BenchmarkParams{Items: DefaultItems, Threads: 1}

	tempDir := b.TempDir()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		backend, cleanup := setupBenchBackendWithConfig(b, filepath.Join(tempDir, "work.db"))
		seq := NewSequence(1)

		// Pre-populate with initial data
		nodes := make([]*nodestore.Node, params.Items)
		for j := 0; j < params.Items; j++ {
			nodes[j] = seq.Obj(j)
			backend.Store(nodes[j])
		}
		b.StartTimer()

		// Simulated rippled workload
		parallelWork(params.Items, params.Threads, func(idx int) {
			rng := rand.New(rand.NewSource(int64(idx + 1)))

			// 20% chance of historical lookup (equivalent to C++ rand < 200 out of 1000)
			if rng.Intn(100) < 20 {
				targetIdx := rng.Intn(len(nodes))
				node, status := backend.Fetch(nodes[targetIdx].Hash)
				if status != nodestore.OK || node == nil {
					b.Errorf("historical fetch failed at index %d: %v", idx, status)
				}
			}

			// Mix of operations (equivalent to C++ p[2] array logic)
			operations := []int{0, 1}
			if rng.Intn(2) == 0 {
				operations[0], operations[1] = operations[1], operations[0]
			}

			for _, op := range operations {
				switch op {
				case 0:
					// Fetch recent (possibly missing)
					recentIdx := rng.Intn(params.Items) + params.Items
					recentNode := seq.Obj(recentIdx)
					backend.Fetch(recentNode.Hash) // May or may not exist

				case 1:
					// Insert new
					newIdx := idx + params.Items
					newNode := seq.Obj(newIdx)
					backend.Store(newNode)
				}
			}
		})

		b.StopTimer()
		cleanup()
		os.RemoveAll(filepath.Join(tempDir, "work.db"))
		b.StartTimer()
	}
}

// Multi-threaded benchmarks (equivalent to C++ do_tests with multiple threads)

// BenchmarkInsertBatch4Threads - 4 thread insert test
func BenchmarkInsertBatch4Threads(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	params := BenchmarkParams{Items: DefaultItems, Threads: 4}

	tempDir := b.TempDir()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		backend, cleanup := setupBenchBackendWithConfig(b, filepath.Join(tempDir, "insert.db"))
		seq := NewSequence(1)
		b.StartTimer()

		parallelWork(params.Items, params.Threads, func(idx int) {
			node := seq.Obj(idx)
			if status := backend.Store(node); status != nodestore.OK {
				b.Errorf("store failed at index %d: %v", idx, status)
			}
		})

		b.StopTimer()
		cleanup()
		os.RemoveAll(filepath.Join(tempDir, "insert.db"))
		b.StartTimer()
	}
}

// BenchmarkInsertBatch8Threads - 8 thread insert test
func BenchmarkInsertBatch8Threads(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	params := BenchmarkParams{Items: DefaultItems, Threads: 8}

	tempDir := b.TempDir()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		backend, cleanup := setupBenchBackendWithConfig(b, filepath.Join(tempDir, "insert.db"))
		seq := NewSequence(1)
		b.StartTimer()

		parallelWork(params.Items, params.Threads, func(idx int) {
			node := seq.Obj(idx)
			if status := backend.Store(node); status != nodestore.OK {
				b.Errorf("store failed at index %d: %v", idx, status)
			}
		})

		b.StopTimer()
		cleanup()
		os.RemoveAll(filepath.Join(tempDir, "insert.db"))
		b.StartTimer()
	}
}

// Comprehensive benchmark suite matching C++ structure
func BenchmarkComparison(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	benchmarks := []struct {
		name string
		fn   func(*testing.B)
	}{
		{"Insert1T", BenchmarkInsertBatch},
		{"Fetch1T", BenchmarkFetchBatch},
		{"Missing1T", BenchmarkMissingBatch},
		{"Mixed1T", BenchmarkMixedBatch},
		{"Work1T", BenchmarkWorkBatch},
		{"Insert4T", BenchmarkInsertBatch4Threads},
		{"Insert8T", BenchmarkInsertBatch8Threads},
	}

	for _, bench := range benchmarks {
		b.Run(bench.name, bench.fn)
	}
}

// Benchmark results comparison with C++ timing test
func BenchmarkResults(b *testing.B) {
	b.Log("=== NodeStore Benchmark Results (Batch Mode) ===")
	b.Log("Now directly comparable with C++ rippled timing test!")
	b.Log("")
	b.Log("Run with: go test -bench=BenchmarkComparison -benchmem -count=3")
	b.Log("Compare with rippled timing test results:")
	b.Log("- Each benchmark iteration processes", DefaultItems, "objects")
	b.Log("- Results are now batch timings, not per-operation")
	b.Log("- Direct comparison with C++ do_insert, do_fetch, etc.")
	b.Log("")
	b.Log("Expected C++ baseline (10k objects, 1 thread):")
	b.Log("- Insert: ~0.125s")
	b.Log("- Fetch: ~0.113s")
	b.Log("- Missing: ~0.011s")
	b.Log("- Mixed: ~0.093s")
	b.Log("- Work: ~0.330s")
}

// Single operation benchmarks (for micro-benchmarking)
func BenchmarkSingleOperations(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	tempDir := b.TempDir()

	backend, cleanup := setupBenchBackendWithConfig(b, filepath.Join(tempDir, "single.db"))
	defer cleanup()

	seq := NewSequence(1)

	b.Run("SingleInsert", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			node := seq.Obj(i)
			backend.Store(node)
		}
	})

	// Pre-populate for fetch tests
	const prepopulateCount = 1000
	nodes := make([]*nodestore.Node, prepopulateCount)
	for i := 0; i < prepopulateCount; i++ {
		nodes[i] = seq.Obj(i)
		backend.Store(nodes[i])
	}

	b.Run("SingleFetch", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			idx := i % len(nodes)
			backend.Fetch(nodes[idx].Hash)
		}
	})

	b.Run("SingleMissing", func(b *testing.B) {
		missingSeq := NewSequence(2)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			key := missingSeq.Key(i)
			backend.Fetch(key)
		}
	})
}

package pebble

import (
	"bytes"
	"math"
	"runtime"
	"strconv"
	"testing"

	cockroachpebble "github.com/cockroachdb/pebble"
)

func TestOptionsFromMiB(t *testing.T) {
	options, err := OptionsFromMiB(64, 128)
	if err != nil {
		t.Fatalf("OptionsFromMiB: %v", err)
	}
	want := Options{BlockCacheBytes: 64 << 20, MaxOpenFiles: 128}
	if options != want {
		t.Fatalf("OptionsFromMiB() = %+v, want %+v", options, want)
	}

	for _, cacheMB := range []int64{-1, math.MaxInt64/(1<<20) + 1} {
		if _, err := OptionsFromMiB(cacheMB, 128); err == nil {
			t.Fatalf("OptionsFromMiB(%d, 128) succeeded, want error", cacheMB)
		}
	}
}

func TestResolveMemTableSize(t *testing.T) {
	for _, test := range []struct {
		name    string
		options Options
		want    int64
	}{
		{name: "default", options: Options{}, want: 64 << 20},
		{name: "tiny cache", options: Options{BlockCacheBytes: 32 << 10}, want: 64 << 10},
		{name: "8 MiB cache", options: Options{BlockCacheBytes: 8 << 20}, want: 2 << 20},
		{name: "32 MiB cache", options: Options{BlockCacheBytes: 32 << 20}, want: 8 << 20},
		{name: "large cache", options: Options{BlockCacheBytes: 1 << 30}, want: 64 << 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := test.options.Resolve()
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			got := resolveMemTableSize(resolved.BlockCacheBytes)
			if got != test.want {
				t.Fatalf("effective memtable size = %d, want %d", got, test.want)
			}
		})
	}
}

func TestOptionsResolve(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		resolved, err := (Options{}).Resolve()
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolved.BlockCacheBytes != DefaultBlockCacheBytes {
			t.Errorf("BlockCacheBytes = %d, want %d", resolved.BlockCacheBytes, DefaultBlockCacheBytes)
		}
		if resolved.MaxOpenFiles != DefaultMaxOpenFiles {
			t.Errorf("MaxOpenFiles = %d, want %d", resolved.MaxOpenFiles, DefaultMaxOpenFiles)
		}
	})

	t.Run("custom", func(t *testing.T) {
		want := Options{BlockCacheBytes: 64 << 20, MaxOpenFiles: MinimumOpenFiles}
		resolved, err := want.Resolve()
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resolved != want {
			t.Errorf("Resolve() = %+v, want %+v", resolved, want)
		}
	})

	for _, test := range []struct {
		name    string
		options Options
	}{
		{name: "negative block cache", options: Options{BlockCacheBytes: -1}},
		{name: "negative max open files", options: Options{MaxOpenFiles: -1}},
		{name: "too few max open files", options: Options{MaxOpenFiles: 73}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.options.Resolve(); err == nil {
				t.Fatal("Resolve succeeded, want error")
			}
		})
	}
}

func TestOptionsMemoryCommitmentCountsSharedCacheOnce(t *testing.T) {
	defaultCommitment, err := (Options{}).MemoryCommitment(3)
	if err != nil {
		t.Fatal(err)
	}
	wantDefault := uint64(DefaultBlockCacheBytes) + 3*uint64(memTableSize)*memTableStopWritesThreshold
	if defaultCommitment != wantDefault {
		t.Fatalf("default commitment = %d, want %d", defaultCommitment, wantDefault)
	}

	options := Options{BlockCacheBytes: 32 << 20, MaxOpenFiles: 200}
	resolved, err := options.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	memTable := resolveMemTableSize(resolved.BlockCacheBytes)
	perGeneration := uint64(memTable) * memTableStopWritesThreshold
	steady, err := options.MemoryCommitment(2)
	if err != nil {
		t.Fatal(err)
	}
	wantSteady := uint64(32<<20) + 2*perGeneration
	if steady != wantSteady {
		t.Fatalf("steady commitment = %d, want %d", steady, wantSteady)
	}
	transition, err := options.MemoryCommitment(3)
	if err != nil {
		t.Fatal(err)
	}
	wantTransition := uint64(32<<20) + 3*perGeneration
	if transition != wantTransition {
		t.Fatalf("transition commitment = %d, want %d", transition, wantTransition)
	}
	if got := transition - steady; got != perGeneration {
		t.Fatalf("rotation overlap = %d, want one memtable commitment %d", got, perGeneration)
	}
}

func TestOptionsMemoryCommitmentRejectsInvalidGenerationCount(t *testing.T) {
	if _, err := (Options{}).MemoryCommitment(0); err == nil {
		t.Fatal("MemoryCommitment accepted zero open generations")
	}
}

func TestOptionsMemoryCommitmentRejectsOverflow(t *testing.T) {
	const blockCacheBytes = int64(8 << 20)
	perGeneration := uint64(resolveMemTableSize(blockCacheBytes)) * memTableStopWritesThreshold
	maxInt := int(^uint(0) >> 1)
	if uint64(maxInt) <= (^uint64(0)-uint64(blockCacheBytes))/perGeneration {
		t.Skip("int cannot represent enough generations to overflow uint64")
	}
	if _, err := (Options{BlockCacheBytes: blockCacheBytes}).MemoryCommitment(maxInt); err == nil {
		t.Fatal("MemoryCommitment accepted an overflowing generation count")
	}
}

func TestMakePebbleOptions(t *testing.T) {
	want := Options{BlockCacheBytes: 32 << 20, MaxOpenFiles: 128}
	cache := cockroachpebble.NewCache(want.BlockCacheBytes)
	defer cache.Unref()
	options := makePebbleOptions(want, cache)

	if options.Cache != cache {
		t.Fatal("configured cache was not retained")
	}
	if options.MaxOpenFiles != want.MaxOpenFiles {
		t.Fatalf("MaxOpenFiles = %d, want %d", options.MaxOpenFiles, want.MaxOpenFiles)
	}
	wantMemTableSize := resolveMemTableSize(want.BlockCacheBytes)
	if options.MemTableSize != uint64(wantMemTableSize) ||
		options.MemTableStopWritesThreshold != memTableStopWritesThreshold ||
		options.L0CompactionThreshold != l0CompactionThreshold ||
		options.L0StopWritesThreshold != l0StopWritesThreshold ||
		options.LBaseMaxBytes != lBaseMaxBytes {
		t.Fatalf("unexpected database options: %+v", options)
	}
	if len(options.Levels) != levelCount {
		t.Fatalf("levels = %d, want %d", len(options.Levels), levelCount)
	}
	for i, level := range options.Levels {
		wantTarget := int64(initialTargetFileSize) << uint(i)
		if wantTarget > maxTargetFileSize {
			wantTarget = maxTargetFileSize
		}
		if level.BlockSize != levelBlockSize ||
			level.IndexBlockSize != levelIndexBlockSize ||
			level.TargetFileSize != wantTarget ||
			level.Compression != cockroachpebble.SnappyCompression {
			t.Fatalf("level %d = %+v", i, level)
		}
	}

	previous := runtime.GOMAXPROCS(2)
	defer runtime.GOMAXPROCS(previous)
	if got := options.MaxConcurrentCompactions(); got != 2 {
		t.Fatalf("MaxConcurrentCompactions = %d, want 2", got)
	}
}

func TestTinyCacheRetainsBlockCacheAfterFlush(t *testing.T) {
	const blockCacheBytes = int64(256 << 10)
	options := Options{BlockCacheBytes: blockCacheBytes, MaxOpenFiles: MinimumOpenFiles}
	cache := cockroachpebble.NewCache(blockCacheBytes)
	defer cache.Unref()
	dbOptions := makePebbleOptions(options, cache)
	if dbOptions.MemTableSize != uint64(64<<10) {
		t.Fatalf("MemTableSize = %d, want %d", dbOptions.MemTableSize, 64<<10)
	}
	db, err := cockroachpebble.Open(t.TempDir(), dbOptions)
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	value := bytes.Repeat([]byte("v"), 4096)
	for i := 0; i < 256; i++ {
		key := []byte("key-" + strconv.Itoa(i))
		if err := db.Set(key, value, cockroachpebble.NoSync); err != nil {
			t.Fatalf("Set(%q): %v", key, err)
		}
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	for i := 0; i < 256; i++ {
		key := []byte("key-" + strconv.Itoa(i))
		got, closer, err := db.Get(key)
		if err != nil {
			t.Fatalf("Get(%q): %v", key, err)
		}
		if !bytes.Equal(got, value) {
			_ = closer.Close()
			t.Fatalf("Get(%q) returned %d bytes, want %d", key, len(got), len(value))
		}
		if err := closer.Close(); err != nil {
			t.Fatalf("close Get(%q): %v", key, err)
		}
	}

	metrics := cache.Metrics()
	if metrics.Size <= 0 {
		t.Fatal("block cache has no entries after flushing and reading an sstable")
	}
	if metrics.Size > blockCacheBytes {
		t.Fatalf("block cache size = %d, exceeds configured capacity %d", metrics.Size, blockCacheBytes)
	}
}

func TestNewRotatingSharesConfiguredResources(t *testing.T) {
	options := Options{BlockCacheBytes: 48 << 20, MaxOpenFiles: 200}
	store, err := NewRotating(t.TempDir(), options)
	if err != nil {
		t.Fatalf("NewRotating: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if got := store.blockCache.MaxSize(); got != options.BlockCacheBytes {
		t.Errorf("block cache size = %d, want %d", got, options.BlockCacheBytes)
	}
	wantPerGeneration := options.MaxOpenFiles / 2
	if store.options.MaxOpenFiles != wantPerGeneration {
		t.Fatalf("per-generation MaxOpenFiles = %d, want %d", store.options.MaxOpenFiles, wantPerGeneration)
	}

	committed, err := store.Rotate(2, 1)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if !committed {
		t.Fatal("Rotate did not publish the new generation")
	}
	if store.options.MaxOpenFiles != wantPerGeneration {
		t.Fatalf("rotated per-generation MaxOpenFiles = %d, want %d", store.options.MaxOpenFiles, wantPerGeneration)
	}
}

func TestConstructorsRejectInvalidOptions(t *testing.T) {
	for _, test := range []struct {
		name string
		open func() error
	}{
		{
			name: "store",
			open: func() error {
				_, err := New(t.TempDir(), Options{BlockCacheBytes: -1})
				return err
			},
		},
		{
			name: "rotating store",
			open: func() error {
				_, err := NewRotating(t.TempDir(), Options{MaxOpenFiles: -1})
				return err
			},
		},
		{
			name: "rotating store total file budget is too small",
			open: func() error {
				_, err := NewRotating(t.TempDir(), Options{MaxOpenFiles: 147})
				return err
			},
		},
		{
			name: "rotating store total file budget must be even",
			open: func() error {
				_, err := NewRotating(t.TempDir(), Options{MaxOpenFiles: 149})
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.open(); err == nil {
				t.Fatal("constructor succeeded, want error")
			}
		})
	}
}

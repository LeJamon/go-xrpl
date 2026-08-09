package pebble

import (
	"math"
	"runtime"
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
	if options.MemTableSize != memTableSize ||
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

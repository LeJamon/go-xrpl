package pebble

import (
	"math"
	"testing"
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

func TestNewAppliesOptions(t *testing.T) {
	want := Options{BlockCacheBytes: 32 << 20, MaxOpenFiles: 128}
	store, err := New(t.TempDir(), want, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if store.options != want {
		t.Errorf("effective options = %+v, want %+v", store.options, want)
	}
	if got := store.cache.MaxSize(); got != want.BlockCacheBytes {
		t.Errorf("block cache size = %d, want %d", got, want.BlockCacheBytes)
	}
}

func TestNewUsesDefaultOptions(t *testing.T) {
	store, err := New(t.TempDir(), Options{}, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	want := Options{
		BlockCacheBytes: DefaultBlockCacheBytes,
		MaxOpenFiles:    DefaultMaxOpenFiles,
	}
	if store.options != want {
		t.Errorf("effective options = %+v, want %+v", store.options, want)
	}
	if got := store.cache.MaxSize(); got != DefaultBlockCacheBytes {
		t.Errorf("block cache size = %d, want %d", got, DefaultBlockCacheBytes)
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
	if store.writable.cache != store.blockCache || store.archive.cache != store.blockCache {
		t.Fatal("rotating generations do not share the store block cache")
	}
	wantPerGeneration := options.MaxOpenFiles / 2
	if store.writable.options.MaxOpenFiles != wantPerGeneration {
		t.Errorf(
			"writable MaxOpenFiles = %d, want %d",
			store.writable.options.MaxOpenFiles,
			wantPerGeneration,
		)
	}
	if store.archive.options.MaxOpenFiles != wantPerGeneration {
		t.Errorf(
			"archive MaxOpenFiles = %d, want %d",
			store.archive.options.MaxOpenFiles,
			wantPerGeneration,
		)
	}

	committed, err := store.Rotate(2, 1)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if !committed {
		t.Fatal("Rotate did not publish the new generation")
	}
	if store.writable.cache != store.blockCache || store.archive.cache != store.blockCache {
		t.Fatal("rotated generations do not share the store block cache")
	}
	if store.writable.options.MaxOpenFiles != wantPerGeneration ||
		store.archive.options.MaxOpenFiles != wantPerGeneration {
		t.Fatalf(
			"rotated MaxOpenFiles = writable %d, archive %d; want %d each",
			store.writable.options.MaxOpenFiles,
			store.archive.options.MaxOpenFiles,
			wantPerGeneration,
		)
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
				_, err := New(t.TempDir(), Options{BlockCacheBytes: -1}, false)
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

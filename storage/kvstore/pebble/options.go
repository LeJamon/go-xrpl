package pebble

import (
	"errors"
	"fmt"
	"math"
	"runtime"

	cockroachpebble "github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
)

const (
	// DefaultBlockCacheBytes is the block-cache capacity used when none is configured.
	DefaultBlockCacheBytes int64 = 256 << 20
	// DefaultMaxOpenFiles is the open-file soft limit used when none is configured.
	DefaultMaxOpenFiles = 500
	// MinimumOpenFiles is the smallest soft limit Pebble can honor for one
	// database: 64 table-cache entries plus 10 non-table files.
	MinimumOpenFiles = 74
	// MinimumRotatingOpenFiles is the smallest steady-state total for two generations.
	MinimumRotatingOpenFiles = 2 * MinimumOpenFiles

	memTableSize                = 64 << 20
	memTableStopWritesThreshold = 4
	l0CompactionThreshold       = 4
	l0StopWritesThreshold       = 20
	lBaseMaxBytes               = 256 << 20
	levelCount                  = 7
	levelBlockSize              = 32 << 10
	levelIndexBlockSize         = 256 << 10
	filterBitsPerKey            = 10
	initialTargetFileSize       = 8 << 20
	maxTargetFileSize           = 256 << 20
)

// Options configures a Pebble store's resource limits.
// Zero values select the package defaults.
type Options struct {
	// BlockCacheBytes is the block-cache capacity. A rotating store shares this
	// cache across both generations.
	BlockCacheBytes int64
	// MaxOpenFiles is the open-file soft limit. A rotating store divides this
	// steady-state total evenly across both generations.
	MaxOpenFiles int
}

// OptionsFromMiB validates MiB-based input and resolves package defaults.
func OptionsFromMiB(blockCacheMB int64, maxOpenFiles int) (Options, error) {
	if blockCacheMB < 0 || blockCacheMB > math.MaxInt64/(1<<20) {
		return Options{}, fmt.Errorf("kvstore/pebble: block cache MiB is out of range: %d", blockCacheMB)
	}
	return (Options{
		BlockCacheBytes: blockCacheMB * (1 << 20),
		MaxOpenFiles:    maxOpenFiles,
	}).Resolve()
}

// Resolve validates o and fills in default values.
func (o Options) Resolve() (Options, error) {
	if o.BlockCacheBytes < 0 {
		return Options{}, errors.New("kvstore/pebble: block cache size cannot be negative")
	}
	if o.MaxOpenFiles < 0 {
		return Options{}, errors.New("kvstore/pebble: max open files cannot be negative")
	}
	if o.BlockCacheBytes == 0 {
		o.BlockCacheBytes = DefaultBlockCacheBytes
	}
	if o.MaxOpenFiles == 0 {
		o.MaxOpenFiles = DefaultMaxOpenFiles
	}
	if o.MaxOpenFiles < MinimumOpenFiles {
		return Options{}, fmt.Errorf(
			"kvstore/pebble: max open files must be at least %d, got %d",
			MinimumOpenFiles,
			o.MaxOpenFiles,
		)
	}
	return o, nil
}

func resolveRotatingOptions(options Options) (Options, Options, error) {
	resolved, err := options.Resolve()
	if err != nil {
		return Options{}, Options{}, err
	}
	if resolved.MaxOpenFiles < MinimumRotatingOpenFiles {
		return Options{}, Options{}, fmt.Errorf(
			"kvstore/pebble: rotating store requires at least %d max open files, got %d",
			MinimumRotatingOpenFiles,
			resolved.MaxOpenFiles,
		)
	}
	if resolved.MaxOpenFiles%2 != 0 {
		return Options{}, Options{}, fmt.Errorf(
			"kvstore/pebble: rotating store requires an even max open files value, got %d",
			resolved.MaxOpenFiles,
		)
	}
	perGeneration := resolved
	perGeneration.MaxOpenFiles /= 2
	return resolved, perGeneration, nil
}

func makePebbleOptions(options Options, cache *cockroachpebble.Cache) *cockroachpebble.Options {
	result := &cockroachpebble.Options{
		Cache:                       cache,
		MaxOpenFiles:                options.MaxOpenFiles,
		MemTableSize:                memTableSize,
		MemTableStopWritesThreshold: memTableStopWritesThreshold,
		MaxConcurrentCompactions: func() int {
			return runtime.GOMAXPROCS(0)
		},
		L0CompactionThreshold: l0CompactionThreshold,
		L0StopWritesThreshold: l0StopWritesThreshold,
		LBaseMaxBytes:         lBaseMaxBytes,
		Levels:                make([]cockroachpebble.LevelOptions, levelCount),
	}

	for i := range result.Levels {
		targetFileSize := int64(initialTargetFileSize) << uint(i)
		if targetFileSize > maxTargetFileSize {
			targetFileSize = maxTargetFileSize
		}
		result.Levels[i] = cockroachpebble.LevelOptions{
			BlockSize:      levelBlockSize,
			IndexBlockSize: levelIndexBlockSize,
			FilterPolicy:   bloom.FilterPolicy(filterBitsPerKey),
			FilterType:     cockroachpebble.TableFilter,
			TargetFileSize: targetFileSize,
			Compression:    cockroachpebble.SnappyCompression,
		}
	}
	return result
}

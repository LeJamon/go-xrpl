package pebble

import (
	"errors"
	"fmt"
	"math"
)

const (
	DefaultBlockCacheBytes int64 = 256 << 20
	DefaultMaxOpenFiles = 500
	// MinimumOpenFiles is the smallest soft limit Pebble can honor for one
	// database: 64 table-cache entries plus 10 non-table files.
	MinimumOpenFiles = 74
	MinimumRotatingOpenFiles = 2 * MinimumOpenFiles
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

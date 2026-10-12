// Package overlay provides bounded, versioned state changes over an immutable
// ledger-state reader.
//
// An Overlay owns only changes made above its base. Reads capture the current
// immutable change roots and may be used concurrently with later forks or
// mutations. A Snapshot is an explicit read lease; callers must close it when
// the read is complete.
package overlay

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/membudget"
)

// Reader is the read-only ordered state-view contract implemented by Overlay
// and Snapshot.
//
// Iterate starts at the first key strictly greater than after. A nil after
// starts at the beginning. Callback data is borrowed until the callback
// returns and must not be retained or mutated by the callback.
type Reader interface {
	Get(context.Context, [32]byte) ([]byte, bool, error)
	Iterate(context.Context, *[32]byte, func([32]byte, []byte) bool) error
}

// baseReader is the immutable base contract used by New. A base supplies point
// reads and one pull-based ordered cursor so merged iteration does not reopen a
// callback for every frontier item.
type baseReader interface {
	Get(context.Context, [32]byte) ([]byte, bool, error)
	OpenCursor(context.Context, *[32]byte) (Cursor, error)
}

// Cursor is a pull-based ordered reader. Next obeys the same strict successor
// contract as Reader.Iterate; returned bytes are borrowed until the next call.
// Close is idempotent.
type Cursor interface {
	Next(context.Context) ([32]byte, []byte, bool, error)
	Close() error
}

// Change is one final overlay change. Value is nil when Deleted is true. Values
// passed to callback-based iteration are borrowed until the callback returns;
// callbacks must not retain or mutate them. ChangeIterator.Next returns an
// owned copy.
type Change struct {
	Key     [32]byte
	Value   []byte
	Deleted bool
}

// Config controls memory admission and immutable spill runs.
//
// A zero MaxMemoryBytes delegates the memory ceiling to MemoryBudget when one
// is supplied; with neither set, memory admission is unlimited by this
// package. A zero MaxDiskBytes disables disk spilling. This is intentional:
// scratch files are never created without an explicit disk ceiling.
type Config struct {
	// MemoryBudget and DiskBudget are optional shared admission accounts. The
	// accounts may be the same account when the caller intentionally combines
	// their allowances.
	MemoryBudget *membudget.Account
	DiskBudget   *membudget.Account

	// MaxMemoryBytes bounds the package's managed in-memory state and temporary
	// read workspace. Zero means no package-local ceiling.
	MaxMemoryBytes uint64
	// MaxDiskBytes bounds all live spill-file bytes, including files shared by
	// forks and snapshots. Zero disables spilling.
	MaxDiskBytes uint64
	// SpillDir is the directory for immutable scratch runs. It must be set when
	// MaxDiskBytes is non-zero.
	SpillDir string

	// MaxSegments bounds the number of immutable runs participating in a merge.
	// MaxMergeFanIn bounds one compaction operation. Values below two use the
	// package defaults.
	MaxSegments   int
	MaxMergeFanIn int
}

const (
	defaultMaxSegments   = 8
	defaultMaxMergeFanIn = 8

	// Charges include object headers, pointers, and allocator/index slack. They
	// deliberately over-account to keep admission conservative.
	nodeOverhead    = uint64(128)
	blobOverhead    = uint64(32)
	segmentOverhead = uint64(1024)
	workspaceBase   = uint64(256)
	workspaceStream = uint64(128)
)

var (
	// ErrClosed means the overlay or snapshot no longer owns its state.
	ErrClosed = errors.New("ledger overlay is closed")
	// ErrMemoryBudget is returned when managed memory admission is denied.
	ErrMemoryBudget = errors.New("ledger overlay memory budget exhausted")
	// ErrDiskBudget is returned when a spill or compaction cannot be admitted.
	ErrDiskBudget = errors.New("ledger overlay disk budget exhausted")
	// ErrSpillDisabled is returned when memory pressure requires a spill but no
	// bounded scratch store was configured.
	ErrSpillDisabled = errors.New("ledger overlay spilling is disabled")
	// ErrInvalidConfig identifies a configuration that cannot provide bounded
	// scratch storage.
	ErrInvalidConfig = errors.New("invalid ledger overlay configuration")
	// ErrCorruptSegment identifies an internally generated or externally damaged
	// immutable run.
	ErrCorruptSegment = errors.New("ledger overlay spill segment is corrupt")
	// ErrOrdering identifies a base or segment reader that violates ascending
	// key order.
	ErrOrdering = errors.New("ledger overlay reader is not strictly ordered")
)

func normalizeConfig(cfg Config) (Config, error) {
	if cfg.MaxSegments < 1 {
		cfg.MaxSegments = defaultMaxSegments
	}
	if cfg.MaxMergeFanIn < 2 {
		cfg.MaxMergeFanIn = defaultMaxMergeFanIn
	}
	if cfg.MaxSegments == 1 && cfg.MaxMergeFanIn > 1 {
		// A single retained run still needs a two-way merge to reduce two
		// incoming runs to one.
		cfg.MaxMergeFanIn = 2
	} else if cfg.MaxMergeFanIn > cfg.MaxSegments {
		cfg.MaxMergeFanIn = cfg.MaxSegments
	}
	if cfg.MaxDiskBytes > 0 && cfg.SpillDir == "" {
		return Config{}, fmt.Errorf("%w: MaxDiskBytes requires SpillDir", ErrInvalidConfig)
	}
	return cfg, nil
}

// emptyReader makes a nil base equivalent to an empty immutable state.
type emptyReader struct{}

func (emptyReader) Get(context.Context, [32]byte) ([]byte, bool, error) { return nil, false, nil }

func (emptyReader) OpenCursor(ctx context.Context, _ *[32]byte) (Cursor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return emptyCursor{}, nil
}

type emptyCursor struct{}

func (emptyCursor) Next(ctx context.Context) ([32]byte, []byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return [32]byte{}, nil, false, err
	}
	return [32]byte{}, nil, false, nil
}

func (emptyCursor) Close() error { return nil }

package config

import "fmt"

// ValidationArchiveConfig controls the on-disk validation archive —
// persistence of stale validations pruned from ValidationTracker to the
// relational DB's validations table. Follows rippled's historical
// onStale/doStaleWrite pattern with Go-native batching semantics.
type ValidationArchiveConfig struct {
	// Enabled flips the archive on. When false no writer goroutine runs
	// and OnStale is a no-op, regardless of whether the relational DB
	// is configured. Default: true.
	Enabled bool `toml:"enabled" mapstructure:"enabled"`

	// RetentionLedgers is the number of fully-validated ledgers of
	// history to keep in the archive. Rows with ledger_seq below
	// (currentFullyValidatedSeq - RetentionLedgers) are deleted on
	// each flush tick. 0 disables retention (keep forever).
	RetentionLedgers uint32 `toml:"retention_ledgers" mapstructure:"retention_ledgers"`

	// BatchSize caps how many stale validations the writer accumulates
	// before forcing a commit. Larger values amortize fsync cost across
	// more rows; smaller values bound write latency. Must be >= 1.
	BatchSize int `toml:"batch_size" mapstructure:"batch_size"`

	// FlushIntervalMs is the maximum milliseconds a partial batch may
	// wait before being committed. Guarantees a bounded write latency
	// even when the stale-validation rate is below BatchSize/sec.
	// Must be >= 10.
	FlushIntervalMs int `toml:"flush_interval_ms" mapstructure:"flush_interval_ms"`

	// DeleteBatch caps how many rows a single retention sweep deletes.
	// Bounded so the writer never blocks on a multi-second DELETE scan.
	// Must be >= 1.
	DeleteBatch int `toml:"delete_batch" mapstructure:"delete_batch"`

	// InMemoryLedgers is the ExpireOld trigger. Every time a ledger is
	// fully validated at seq S, the tracker drops validations for
	// ledgers below (S - InMemoryLedgers) — which then stream into the
	// archive via OnStale. Must be >= 1.
	InMemoryLedgers uint32 `toml:"in_memory_ledgers" mapstructure:"in_memory_ledgers"`
}

// DefaultValidationArchiveConfig returns the built-in defaults. Applied
// when the [validation_archive] section is absent from xrpld.toml.
func DefaultValidationArchiveConfig() ValidationArchiveConfig {
	return ValidationArchiveConfig{
		Enabled:          true,
		RetentionLedgers: 10000,
		BatchSize:        128,
		FlushIntervalMs:  1000,
		DeleteBatch:      1000,
		InMemoryLedgers:  256,
	}
}

// WithDefaults returns a copy of c with any zero-valued field replaced by
// the built-in default. RetentionLedgers is left alone because 0 has a
// meaning (disable retention) distinct from "not configured."
func (c ValidationArchiveConfig) WithDefaults() ValidationArchiveConfig {
	d := DefaultValidationArchiveConfig()
	if c.BatchSize == 0 {
		c.BatchSize = d.BatchSize
	}
	if c.FlushIntervalMs == 0 {
		c.FlushIntervalMs = d.FlushIntervalMs
	}
	if c.DeleteBatch == 0 {
		c.DeleteBatch = d.DeleteBatch
	}
	if c.InMemoryLedgers == 0 {
		c.InMemoryLedgers = d.InMemoryLedgers
	}
	return c
}

// Validate returns a non-nil error if any knob is out of range. Called
// by ValidateConfig during startup so operators see all problems at once.
func (c *ValidationArchiveConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.BatchSize < 1 {
		return fmt.Errorf("batch_size must be >= 1, got %d", c.BatchSize)
	}
	if c.FlushIntervalMs < 10 {
		return fmt.Errorf("flush_interval_ms must be >= 10, got %d", c.FlushIntervalMs)
	}
	if c.DeleteBatch < 1 {
		return fmt.Errorf("delete_batch must be >= 1, got %d", c.DeleteBatch)
	}
	if c.InMemoryLedgers < 1 {
		return fmt.Errorf("in_memory_ledgers must be >= 1, got %d", c.InMemoryLedgers)
	}
	return nil
}

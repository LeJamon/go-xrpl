package config

import (
	"fmt"
	"math"
)

const (
	// DefaultAvailableMemoryPercent is the startup share of measured available
	// memory used when resources.memory_mb is zero.
	DefaultAvailableMemoryPercent = 33
	maxResourcesMemoryMB          = math.MaxUint64 / uint64(bytesPerMiB)
)

// ResourcesConfig controls the node-wide managed-memory allowance.
//
// A positive MemoryMB is an explicit fixed allowance. A zero MemoryMB asks
// startup to probe available memory and apply AvailableMemoryPercent. The
// percentage is ignored for a fixed allowance, but remains validated so a
// typo is not hidden by the selected sizing mode.
type ResourcesConfig struct {
	MemoryMB               int64 `toml:"memory_mb" mapstructure:"memory_mb"`
	AvailableMemoryPercent int   `toml:"available_memory_percent" mapstructure:"available_memory_percent"`
}

// IsAutomatic reports whether startup must use a headroom probe.
func (c ResourcesConfig) IsAutomatic() bool {
	return c.MemoryMB == 0
}

// ResolvedAvailableMemoryPercent returns the configured percentage, applying
// the documented default when the field is zero.
func (c ResourcesConfig) ResolvedAvailableMemoryPercent() int {
	if c.AvailableMemoryPercent == 0 {
		return DefaultAvailableMemoryPercent
	}
	return c.AvailableMemoryPercent
}

// Validate checks integer ranges and MiB-to-byte conversion safety.
func (c ResourcesConfig) Validate() error {
	if c.MemoryMB < 0 {
		return fmt.Errorf("memory_mb must be non-negative, got %d", c.MemoryMB)
	}
	if uint64(c.MemoryMB) > maxResourcesMemoryMB {
		return fmt.Errorf("memory_mb is too large: %d MiB overflows bytes", c.MemoryMB)
	}
	if c.AvailableMemoryPercent < 0 || c.AvailableMemoryPercent > 100 {
		return fmt.Errorf("available_memory_percent must be between 0 and 100, got %d", c.AvailableMemoryPercent)
	}
	return nil
}

// Resolve computes the startup allowance. In fixed mode available is ignored;
// in automatic mode it is the measured usable headroom in bytes. The
// calculation is rounded down and checked for overflow.
func (c ResourcesConfig) Resolve(available uint64) (uint64, error) {
	if err := c.Validate(); err != nil {
		return 0, err
	}
	if c.MemoryMB > 0 {
		return uint64(c.MemoryMB) * uint64(bytesPerMiB), nil
	}
	percent := uint64(c.ResolvedAvailableMemoryPercent())
	quotient, remainder := available/100, available%100
	if quotient > math.MaxUint64/percent {
		return 0, fmt.Errorf("available memory calculation overflows: available=%d percent=%d", available, percent)
	}
	result := quotient * percent
	partial := remainder * percent / 100
	if result > math.MaxUint64-partial {
		return 0, fmt.Errorf("available memory calculation overflows: available=%d percent=%d", available, percent)
	}
	return result + partial, nil
}

package config

import (
	"fmt"
	"math"
)

const (
	// DefaultAcquisitionMemoryMB is the bounded acquisition working-set budget
	// used by standalone acquisition constructors without a node-wide parent.
	DefaultAcquisitionMemoryMB = 512
	// MinAcquisitionMemoryMB is the smallest budget accepted for bounded
	// traversal progress plus acquisition-owned reply and persistence headroom.
	MinAcquisitionMemoryMB = 512

	maxAcquisitionMemoryMB = math.MaxUint64 / uint64(bytesPerMiB)
)

// AcquisitionConfig represents the [acquisition] section.
//
// MemoryMB bounds the application-owned acquisition working set, including
// cursor state, replay staging and reply/persistence headroom. It does not
// include the Pebble cache, network buffers, Go runtime, or
// other process and operating-system memory.
type AcquisitionConfig struct {
	MemoryMB int64 `toml:"memory_mb" mapstructure:"memory_mb"`
}

// ResolvedMemoryBytes returns the acquisition ceiling for independent callers.
// The node uses its shared pool when MemoryMB is zero.
// Invalid negative or overflowing values return zero; configuration loading
// and ValidateConfig report those values as errors before this accessor is
// used by the node.
func (c AcquisitionConfig) ResolvedMemoryBytes() uint64 {
	mb := c.MemoryMB
	if mb == 0 {
		mb = DefaultAcquisitionMemoryMB
	}
	if mb < 0 || uint64(mb) > maxAcquisitionMemoryMB {
		return 0
	}
	return uint64(mb) * uint64(bytesPerMiB)
}

// Validate checks the acquisition budget and its MiB-to-byte conversion.
func (c AcquisitionConfig) Validate() error {
	if c.MemoryMB < 0 {
		return fmt.Errorf("memory_mb must be non-negative, got %d", c.MemoryMB)
	}
	if c.MemoryMB > 0 && c.MemoryMB < MinAcquisitionMemoryMB {
		return fmt.Errorf("memory_mb must be 0 or at least %d MiB, got %d", MinAcquisitionMemoryMB, c.MemoryMB)
	}
	if uint64(c.MemoryMB) > maxAcquisitionMemoryMB {
		return fmt.Errorf("memory_mb is too large: %d MiB overflows bytes", c.MemoryMB)
	}
	return nil
}

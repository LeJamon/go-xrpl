package config

import "fmt"

// StateConfig controls bounded execution allowances. These allowances are part
// of resources.memory_mb; scratch_mb is a separate disk ceiling.
type StateConfig struct {
	MemoryMB   int64 `toml:"memory_mb" mapstructure:"memory_mb"`
	ScratchMB  int64 `toml:"scratch_mb" mapstructure:"scratch_mb"`
	CacheMB    int64 `toml:"cache_mb" mapstructure:"cache_mb"`
	MemTableMB int64 `toml:"memtable_mb" mapstructure:"memtable_mb"`
}

// Resolved fills the bounded execution allowances.
func (c StateConfig) Resolved() StateConfig {
	if c.MemoryMB == 0 {
		c.MemoryMB = 64
	}
	if c.ScratchMB == 0 {
		c.ScratchMB = 2048
	}
	if c.CacheMB == 0 {
		c.CacheMB = 16
	}
	if c.MemTableMB == 0 {
		c.MemTableMB = 8
	}
	return c
}

// Validate rejects invalid byte limits and configurations without execution headroom.
func (c StateConfig) Validate() error {
	for _, option := range []struct {
		name  string
		value int64
	}{
		{"memory_mb", c.MemoryMB}, {"scratch_mb", c.ScratchMB},
		{"cache_mb", c.CacheMB}, {"memtable_mb", c.MemTableMB},
	} {
		if option.value < 0 || option.value > (1<<30) {
			return fmt.Errorf("state.%s must be between 0 and 1073741824 MiB", option.name)
		}
	}
	if c.Resolved().MemoryMB < 8 {
		return fmt.Errorf("state.memory_mb must be at least 8 MiB")
	}
	return nil
}

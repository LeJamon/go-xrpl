package config

import "fmt"

// NodeDBConfig represents the [node_db] and [import_db] sections
// Configures the primary persistent datastore for ledger data
type NodeDBConfig struct {
	Type                string `toml:"type" mapstructure:"type"`
	Path                string `toml:"path" mapstructure:"path"`
	CacheSize           int    `toml:"cache_size" mapstructure:"cache_size"`
	CacheAge            int    `toml:"cache_age" mapstructure:"cache_age"`
	FastLoad            bool   `toml:"fast_load" mapstructure:"fast_load"`
	EarliestSeq         int    `toml:"earliest_seq" mapstructure:"earliest_seq"`
	OnlineDelete        int    `toml:"online_delete" mapstructure:"online_delete"`
	AdvisoryDelete      int    `toml:"advisory_delete" mapstructure:"advisory_delete"`
	DeleteBatch         int    `toml:"delete_batch" mapstructure:"delete_batch"`
	BackOffMilliseconds int    `toml:"back_off_milliseconds" mapstructure:"back_off_milliseconds"`
	AgeThresholdSeconds int    `toml:"age_threshold_seconds" mapstructure:"age_threshold_seconds"`
	RecoveryWaitSeconds int    `toml:"recovery_wait_seconds" mapstructure:"recovery_wait_seconds"`
}

// SQLiteConfig represents the [sqlite] section
// Tuning settings for the SQLite databases
type SQLiteConfig struct {
	SafetyLevel      string `toml:"safety_level" mapstructure:"safety_level"`
	JournalMode      string `toml:"journal_mode" mapstructure:"journal_mode"`
	Synchronous      string `toml:"synchronous" mapstructure:"synchronous"`
	TempStore        string `toml:"temp_store" mapstructure:"temp_store"`
	PageSize         int    `toml:"page_size" mapstructure:"page_size"`
	JournalSizeLimit int    `toml:"journal_size_limit" mapstructure:"journal_size_limit"`
}

// Validate performs validation on the NodeDB configuration
func (n *NodeDBConfig) Validate() error {
	// Skip validation if this is an empty config (e.g., import_db not configured)
	if n.Type == "" && n.Path == "" {
		return nil
	}

	// Validate type
	if n.Type == "" {
		return fmt.Errorf("node_db type is required")
	}
	validTypes := []string{"pebble", "Pebble"}
	if !contains_slice(validTypes, n.Type) {
		return fmt.Errorf("invalid node_db type: %s (valid options: pebble)", n.Type)
	}

	// Validate path
	if n.Path == "" {
		return fmt.Errorf("node_db path is required")
	}

	// Validate cache settings
	if n.CacheSize < 0 {
		return fmt.Errorf("cache_size must be non-negative, got %d", n.CacheSize)
	}
	if n.CacheAge < 0 {
		return fmt.Errorf("cache_age must be non-negative, got %d", n.CacheAge)
	}

	// Validate earliest_seq
	if n.EarliestSeq != 0 && n.EarliestSeq < 1 {
		return fmt.Errorf("earliest_seq must be at least 1, got %d", n.EarliestSeq)
	}

	// Validate online_delete
	if n.OnlineDelete != 0 && n.OnlineDelete < 256 {
		return fmt.Errorf("online_delete must be at least 256, got %d", n.OnlineDelete)
	}

	// Validate advisory_delete
	if n.AdvisoryDelete != 0 && n.AdvisoryDelete != 1 {
		return fmt.Errorf("advisory_delete must be 0 or 1, got %d", n.AdvisoryDelete)
	}

	// Validate delete_batch
	if n.DeleteBatch < 0 {
		return fmt.Errorf("delete_batch must be non-negative, got %d", n.DeleteBatch)
	}

	// Validate timing settings
	if n.BackOffMilliseconds < 0 {
		return fmt.Errorf("back_off_milliseconds must be non-negative, got %d", n.BackOffMilliseconds)
	}
	if n.AgeThresholdSeconds < 0 {
		return fmt.Errorf("age_threshold_seconds must be non-negative, got %d", n.AgeThresholdSeconds)
	}
	if n.RecoveryWaitSeconds < 0 {
		return fmt.Errorf("recovery_wait_seconds must be non-negative, got %d", n.RecoveryWaitSeconds)
	}

	return nil
}

// Validate performs validation on the SQLite configuration
func (s *SQLiteConfig) Validate() error {
	// Validate safety_level
	if s.SafetyLevel != "" {
		validSafetyLevels := []string{"high", "low"}
		if !contains_slice(validSafetyLevels, s.SafetyLevel) {
			return fmt.Errorf("invalid safety_level: %s (valid options: high, low)", s.SafetyLevel)
		}

		// If safety_level is set, other settings cannot be combined
		if s.JournalMode != "" || s.Synchronous != "" || s.TempStore != "" {
			return fmt.Errorf("safety_level cannot be combined with journal_mode, synchronous, or temp_store")
		}
	}

	// Validate journal_mode
	if s.JournalMode != "" {
		validJournalModes := []string{"delete", "truncate", "persist", "memory", "wal", "off"}
		if !contains_slice(validJournalModes, s.JournalMode) {
			return fmt.Errorf("invalid journal_mode: %s (valid options: delete, truncate, persist, memory, wal, off)", s.JournalMode)
		}
	}

	// Validate synchronous
	if s.Synchronous != "" {
		validSyncModes := []string{"off", "normal", "full", "extra"}
		if !contains_slice(validSyncModes, s.Synchronous) {
			return fmt.Errorf("invalid synchronous: %s (valid options: off, normal, full, extra)", s.Synchronous)
		}
	}

	// Validate temp_store
	if s.TempStore != "" {
		validTempStores := []string{"default", "file", "memory"}
		if !contains_slice(validTempStores, s.TempStore) {
			return fmt.Errorf("invalid temp_store: %s (valid options: default, file, memory)", s.TempStore)
		}
	}

	// Validate page_size
	if s.PageSize != 0 {
		if s.PageSize < 512 || s.PageSize > 65536 || !isPowerOfTwo(s.PageSize) {
			return fmt.Errorf("page_size must be a power of 2 between 512 and 65536, got %d", s.PageSize)
		}
	}

	// Validate journal_size_limit
	if s.JournalSizeLimit < 0 {
		return fmt.Errorf("journal_size_limit must be non-negative, got %d", s.JournalSizeLimit)
	}

	return nil
}

// GetType returns the normalized database type
func (n *NodeDBConfig) GetType() string {
	switch n.Type {
	case "pebble", "Pebble":
		return "pebble"
	default:
		return n.Type
	}
}

func (n *NodeDBConfig) GetCacheSize() int {
	return n.CacheSize
}

func (n *NodeDBConfig) GetCacheAge() int {
	return n.CacheAge
}

// ShouldCreateCache returns true if cache should be created
func (n *NodeDBConfig) ShouldCreateCache() bool {
	// Cache is not created if online_delete is specified or if both cache settings are 0
	return n.OnlineDelete == 0 && (n.CacheSize != 0 || n.CacheAge != 0)
}

func (n *NodeDBConfig) GetEarliestSeq() int {
	return n.EarliestSeq
}

func (n *NodeDBConfig) IsOnlineDeleteEnabled() bool {
	return n.OnlineDelete > 0
}

func (n *NodeDBConfig) IsAdvisoryDeleteEnabled() bool {
	return n.AdvisoryDelete == 1
}

func (n *NodeDBConfig) GetDeleteBatch() int {
	return n.DeleteBatch
}

func (n *NodeDBConfig) GetBackOffMilliseconds() int {
	return n.BackOffMilliseconds
}

func (n *NodeDBConfig) GetAgeThresholdSeconds() int {
	return n.AgeThresholdSeconds
}

func (n *NodeDBConfig) GetRecoveryWaitSeconds() int {
	return n.RecoveryWaitSeconds
}

// GetEffectiveSettings returns the effective SQLite settings based on safety_level or individual settings
func (s *SQLiteConfig) GetEffectiveSettings() (journalMode, synchronous, tempStore string) {
	if s.SafetyLevel == "low" {
		return "memory", "off", "memory"
	} else if s.SafetyLevel == "high" {
		return "wal", "normal", "file"
	}

	// Use individual settings (required when safety_level is not set)
	return s.JournalMode, s.Synchronous, s.TempStore
}

func (s *SQLiteConfig) GetPageSize() int {
	return s.PageSize
}

func (s *SQLiteConfig) GetJournalSizeLimit() int {
	return s.JournalSizeLimit
}

// contains_slice checks if a slice contains a string
func contains_slice(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// isPowerOfTwo checks if a number is a power of 2
func isPowerOfTwo(n int) bool {
	return n > 0 && (n&(n-1)) == 0
}

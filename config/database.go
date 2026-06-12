package config

import (
	"fmt"
	"slices"
)

// NodeDBConfig represents the [node_db] section.
// Configures the primary persistent datastore for ledger data.
// `type` and `path` are required; the remaining keys are optional —
// zero values mean "use the built-in default".
type NodeDBConfig struct {
	Type                string `toml:"type" mapstructure:"type"`
	Path                string `toml:"path" mapstructure:"path"`
	CacheSize           int    `toml:"cache_size" mapstructure:"cache_size"` // node-object cache entries; 0 = default
	CacheAge            int    `toml:"cache_age" mapstructure:"cache_age"`   // node-object cache age in minutes; 0 = default
	FastLoad            bool   `toml:"fast_load" mapstructure:"fast_load"`
	EarliestSeq         int    `toml:"earliest_seq" mapstructure:"earliest_seq"`
	OnlineDelete        int    `toml:"online_delete" mapstructure:"online_delete"`
	AdvisoryDelete      int    `toml:"advisory_delete" mapstructure:"advisory_delete"`
	DeleteBatch         int    `toml:"delete_batch" mapstructure:"delete_batch"`
	BackOffMilliseconds int    `toml:"back_off_milliseconds" mapstructure:"back_off_milliseconds"`
	AgeThresholdSeconds int    `toml:"age_threshold_seconds" mapstructure:"age_threshold_seconds"`
	RecoveryWaitSeconds int    `toml:"recovery_wait_seconds" mapstructure:"recovery_wait_seconds"`
}

// SQLiteConfig represents the [sqlite] section.
// Tuning settings for the SQLite databases. All keys are optional;
// unset values fall back to the backend defaults (equivalent to
// safety_level = "high"): journal_mode=wal, synchronous=normal,
// temp_store=file.
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
	// Skip validation if this is an empty config
	if n.Type == "" && n.Path == "" {
		return nil
	}

	// Validate type
	if n.Type == "" {
		return fmt.Errorf("node_db type is required")
	}
	validTypes := []string{"pebble", "Pebble"}
	if !slices.Contains(validTypes, n.Type) {
		return fmt.Errorf("invalid node_db type: %s (valid options: pebble)", n.Type)
	}

	// Validate path
	if n.Path == "" {
		return fmt.Errorf("node_db path is required")
	}

	if err := validateNonNegative("cache_size", n.CacheSize); err != nil {
		return err
	}
	if err := validateNonNegative("cache_age", n.CacheAge); err != nil {
		return err
	}
	if err := validateNonNegative("earliest_seq", n.EarliestSeq); err != nil {
		return err
	}

	// Validate online_delete
	if n.OnlineDelete != 0 && n.OnlineDelete < 256 {
		return fmt.Errorf("online_delete must be at least 256, got %d", n.OnlineDelete)
	}

	if err := validateZeroOrOne("advisory_delete", n.AdvisoryDelete); err != nil {
		return err
	}
	if err := validateNonNegative("delete_batch", n.DeleteBatch); err != nil {
		return err
	}
	if err := validateNonNegative("back_off_milliseconds", n.BackOffMilliseconds); err != nil {
		return err
	}
	if err := validateNonNegative("age_threshold_seconds", n.AgeThresholdSeconds); err != nil {
		return err
	}
	if err := validateNonNegative("recovery_wait_seconds", n.RecoveryWaitSeconds); err != nil {
		return err
	}

	return nil
}

// Validate performs validation on the SQLite configuration
func (s *SQLiteConfig) Validate() error {
	// Validate safety_level
	if s.SafetyLevel != "" {
		validSafetyLevels := []string{"high", "low"}
		if !slices.Contains(validSafetyLevels, s.SafetyLevel) {
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
		if !slices.Contains(validJournalModes, s.JournalMode) {
			return fmt.Errorf("invalid journal_mode: %s (valid options: delete, truncate, persist, memory, wal, off)", s.JournalMode)
		}
	}

	// Validate synchronous
	if s.Synchronous != "" {
		validSyncModes := []string{"off", "normal", "full", "extra"}
		if !slices.Contains(validSyncModes, s.Synchronous) {
			return fmt.Errorf("invalid synchronous: %s (valid options: off, normal, full, extra)", s.Synchronous)
		}
	}

	// Validate temp_store
	if s.TempStore != "" {
		validTempStores := []string{"default", "file", "memory"}
		if !slices.Contains(validTempStores, s.TempStore) {
			return fmt.Errorf("invalid temp_store: %s (valid options: default, file, memory)", s.TempStore)
		}
	}

	// Validate page_size
	if s.PageSize != 0 {
		if s.PageSize < 512 || s.PageSize > 65536 || !isPowerOfTwo(s.PageSize) {
			return fmt.Errorf("page_size must be a power of 2 between 512 and 65536, got %d", s.PageSize)
		}
	}

	if err := validateNonNegative("journal_size_limit", s.JournalSizeLimit); err != nil {
		return err
	}

	return nil
}

// IsOnlineDeleteEnabled reports whether online deletion of old ledgers is enabled
// (node_db online_delete > 0).
func (n *NodeDBConfig) IsOnlineDeleteEnabled() bool {
	return n.OnlineDelete > 0
}

// IsAdvisoryDeleteEnabled reports whether online deletion waits for an explicit
// advisory trigger rather than running automatically (node_db advisory_delete).
func (n *NodeDBConfig) IsAdvisoryDeleteEnabled() bool {
	return n.AdvisoryDelete == 1
}

// GetDeleteBatch returns the number of records removed per online-delete batch
// (node_db delete_batch).
func (n *NodeDBConfig) GetDeleteBatch() int {
	return n.DeleteBatch
}

// GetEffectiveSettings returns the effective SQLite tuning based on
// safety_level or the individual settings. Empty strings mean "not
// configured" — the storage backend applies its own defaults.
func (s *SQLiteConfig) GetEffectiveSettings() (journalMode, synchronous, tempStore string) {
	switch s.SafetyLevel {
	case "low":
		return "memory", "off", "memory"
	case "high":
		return "wal", "normal", "file"
	}
	return s.JournalMode, s.Synchronous, s.TempStore
}

// isPowerOfTwo checks if a number is a power of 2
func isPowerOfTwo(n int) bool {
	return n > 0 && (n&(n-1)) == 0
}

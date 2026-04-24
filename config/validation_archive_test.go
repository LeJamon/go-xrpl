package config

import "testing"

func TestValidationArchiveConfig_DefaultsMatchSpec(t *testing.T) {
	c := DefaultValidationArchiveConfig()

	if !c.Enabled {
		t.Error("default should be enabled")
	}
	if c.RetentionLedgers != 10000 {
		t.Errorf("RetentionLedgers default = %d, want 10000", c.RetentionLedgers)
	}
	if c.BatchSize != 128 {
		t.Errorf("BatchSize default = %d, want 128", c.BatchSize)
	}
	if c.FlushIntervalMs != 1000 {
		t.Errorf("FlushIntervalMs default = %d, want 1000", c.FlushIntervalMs)
	}
	if c.DeleteBatch != 1000 {
		t.Errorf("DeleteBatch default = %d, want 1000", c.DeleteBatch)
	}
	if c.InMemoryLedgers != 256 {
		t.Errorf("InMemoryLedgers default = %d, want 256", c.InMemoryLedgers)
	}
}

func TestValidationArchiveConfig_Validate_AcceptsDisabled(t *testing.T) {
	// Zero struct = disabled, should validate even with zero knobs.
	var c ValidationArchiveConfig
	if err := c.Validate(); err != nil {
		t.Fatalf("disabled archive with zero knobs failed to validate: %v", err)
	}
}

func TestValidationArchiveConfig_Validate_RejectsBadKnobs(t *testing.T) {
	cases := []struct {
		name string
		cfg  ValidationArchiveConfig
	}{
		{"batch_size_zero", ValidationArchiveConfig{Enabled: true, FlushIntervalMs: 1000, DeleteBatch: 1, InMemoryLedgers: 1}},
		{"flush_too_small", ValidationArchiveConfig{Enabled: true, BatchSize: 1, FlushIntervalMs: 5, DeleteBatch: 1, InMemoryLedgers: 1}},
		{"delete_batch_zero", ValidationArchiveConfig{Enabled: true, BatchSize: 1, FlushIntervalMs: 1000, InMemoryLedgers: 1}},
		{"in_memory_zero", ValidationArchiveConfig{Enabled: true, BatchSize: 1, FlushIntervalMs: 1000, DeleteBatch: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil {
				t.Fatal("expected validation error, got nil")
			}
		})
	}
}

func TestValidationArchiveConfig_WithDefaults_FillsZeros(t *testing.T) {
	// User asked for it enabled but gave no other knobs.
	c := ValidationArchiveConfig{Enabled: true, RetentionLedgers: 0}
	filled := c.WithDefaults()

	if filled.BatchSize == 0 || filled.FlushIntervalMs == 0 || filled.DeleteBatch == 0 || filled.InMemoryLedgers == 0 {
		t.Fatalf("WithDefaults left zeros: %+v", filled)
	}
	// RetentionLedgers=0 must be preserved — it means "keep forever."
	if filled.RetentionLedgers != 0 {
		t.Errorf("WithDefaults overwrote RetentionLedgers=0; it means 'keep forever': got %d", filled.RetentionLedgers)
	}

	if err := filled.Validate(); err != nil {
		t.Fatalf("WithDefaults output must validate: %v", err)
	}
}

func TestValidationArchiveConfig_WithDefaults_PreservesExplicit(t *testing.T) {
	c := ValidationArchiveConfig{
		Enabled:          true,
		RetentionLedgers: 50,
		BatchSize:        7,
		FlushIntervalMs:  2500,
		DeleteBatch:      99,
		InMemoryLedgers:  512,
	}
	out := c.WithDefaults()
	if out != c {
		t.Errorf("WithDefaults mutated explicit values:\n got  %+v\n want %+v", out, c)
	}
}

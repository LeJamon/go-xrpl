package config

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWatchdogConfigDefaults(t *testing.T) {
	var config WatchdogConfig
	if !config.IsEnabled() {
		t.Fatal("zero watchdog config should be enabled")
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("zero watchdog config should validate: %v", err)
	}
	warn, fatal, abort, err := config.Thresholds()
	if err != nil {
		t.Fatal(err)
	}
	if warn != 10*time.Second || fatal != 90*time.Second || abort != 600*time.Second {
		t.Fatalf("unexpected defaults: %s/%s/%s", warn, fatal, abort)
	}
}

func TestWatchdogConfigDisabled(t *testing.T) {
	config := WatchdogConfig{Disabled: true, WarnSeconds: -1, FatalSeconds: 5}
	if config.IsEnabled() {
		t.Fatal("disabled watchdog reports enabled")
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("disabled watchdog should ignore thresholds: %v", err)
	}
}

func TestWatchdogConfigOverridesAndPartialDefaults(t *testing.T) {
	tests := []struct {
		name               string
		config             WatchdogConfig
		warn, fatal, abort time.Duration
	}{
		{
			name:   "all overrides",
			config: WatchdogConfig{WarnSeconds: 2, FatalSeconds: 4, AbortSeconds: 8},
			warn:   2 * time.Second,
			fatal:  4 * time.Second,
			abort:  8 * time.Second,
		},
		{
			name:   "partial override",
			config: WatchdogConfig{WarnSeconds: 5},
			warn:   5 * time.Second,
			fatal:  90 * time.Second,
			abort:  600 * time.Second,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			warn, fatal, abort, err := test.config.Thresholds()
			if err != nil {
				t.Fatalf("thresholds: %v", err)
			}
			if warn != test.warn || fatal != test.fatal || abort != test.abort {
				t.Fatalf("thresholds = %s/%s/%s", warn, fatal, abort)
			}
		})
	}
}

func TestWatchdogConfigRejectsInvalidThresholds(t *testing.T) {
	tests := []WatchdogConfig{
		{WarnSeconds: -1},
		{WarnSeconds: 90, FatalSeconds: 10, AbortSeconds: 600},
		{WarnSeconds: 10, FatalSeconds: 600, AbortSeconds: 90},
		{WarnSeconds: 10, FatalSeconds: 10, AbortSeconds: 600},
	}
	for index, config := range tests {
		if err := config.Validate(); err == nil {
			t.Errorf("case %d: invalid thresholds accepted", index)
		}
	}
}

func TestWatchdogConfigDurationBounds(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("overflow boundary exceeds 32-bit int")
	}
	maximum := int(maxDurationSeconds)
	valid := WatchdogConfig{
		WarnSeconds:  maximum - 2,
		FatalSeconds: maximum - 1,
		AbortSeconds: maximum,
	}
	if _, _, _, err := valid.Thresholds(); err != nil {
		t.Fatalf("maximum safe thresholds rejected: %v", err)
	}

	tests := []WatchdogConfig{
		{WarnSeconds: maximum + 1, FatalSeconds: maximum + 2, AbortSeconds: maximum + 3},
		{WarnSeconds: 1, FatalSeconds: maximum + 1, AbortSeconds: maximum + 2},
		{WarnSeconds: 1, FatalSeconds: 2, AbortSeconds: maximum + 1},
	}
	for index, config := range tests {
		if _, _, _, err := config.Thresholds(); err == nil || !strings.Contains(err.Error(), "exceeds maximum duration") {
			t.Errorf("case %d: overflow error = %v", index, err)
		}
	}
}

func TestValidateConfigIncludesWatchdog(t *testing.T) {
	config := &Config{Watchdog: WatchdogConfig{WarnSeconds: 100, FatalSeconds: 10, AbortSeconds: 600}}
	err := ValidateConfig(config)
	if err == nil || !strings.Contains(err.Error(), "watchdog:") {
		t.Fatalf("ValidateConfig did not surface watchdog error: %v", err)
	}
}

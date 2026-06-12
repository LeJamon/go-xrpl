package config

import (
	"strings"
	"testing"
)

func TestWatchdogConfig_Defaults(t *testing.T) {
	var w WatchdogConfig // zero value
	if !w.IsEnabled() {
		t.Fatal("zero WatchdogConfig should be enabled")
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("zero WatchdogConfig should validate: %v", err)
	}
	if w.WarnSecondsResolved() != 10 || w.FatalSecondsResolved() != 90 || w.AbortSecondsResolved() != 600 {
		t.Fatalf("unexpected defaults: %d/%d/%d",
			w.WarnSecondsResolved(), w.FatalSecondsResolved(), w.AbortSecondsResolved())
	}
}

func TestWatchdogConfig_Disabled(t *testing.T) {
	// A disabled watchdog validates even with otherwise-broken thresholds.
	w := WatchdogConfig{Disabled: true, WarnSeconds: 99, FatalSeconds: 5}
	if w.IsEnabled() {
		t.Fatal("disabled watchdog reports enabled")
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("disabled watchdog should validate: %v", err)
	}
}

func TestWatchdogConfig_Overrides(t *testing.T) {
	w := WatchdogConfig{WarnSeconds: 2, FatalSeconds: 4, AbortSeconds: 8}
	if err := w.Validate(); err != nil {
		t.Fatalf("valid overrides rejected: %v", err)
	}
	if w.WarnSecondsResolved() != 2 || w.FatalSecondsResolved() != 4 || w.AbortSecondsResolved() != 8 {
		t.Fatalf("overrides not applied")
	}
}

func TestWatchdogConfig_PartialOverrideUsesDefaults(t *testing.T) {
	// Only the warn override set; fatal/abort fall back to defaults.
	w := WatchdogConfig{WarnSeconds: 5}
	if err := w.Validate(); err != nil {
		t.Fatalf("partial override rejected: %v", err)
	}
	if w.WarnSecondsResolved() != 5 || w.FatalSecondsResolved() != 90 || w.AbortSecondsResolved() != 600 {
		t.Fatalf("partial resolve wrong: %d/%d/%d",
			w.WarnSecondsResolved(), w.FatalSecondsResolved(), w.AbortSecondsResolved())
	}
}

func TestWatchdogConfig_RejectsUnordered(t *testing.T) {
	cases := []WatchdogConfig{
		{WarnSeconds: 90, FatalSeconds: 10, AbortSeconds: 600}, // warn > fatal
		{WarnSeconds: 10, FatalSeconds: 600, AbortSeconds: 90}, // fatal > abort
		{WarnSeconds: 10, FatalSeconds: 10, AbortSeconds: 600}, // warn == fatal
	}
	for i, w := range cases {
		if err := w.Validate(); err == nil {
			t.Errorf("case %d: expected ordering error, got nil", i)
		}
	}
}

func TestWatchdogConfig_RejectsNegative(t *testing.T) {
	w := WatchdogConfig{WarnSeconds: -1}
	if err := w.Validate(); err == nil {
		t.Fatal("negative threshold accepted")
	}
}

func TestValidateConfig_IncludesWatchdog(t *testing.T) {
	// ValidateConfig joins all section errors; assert the watchdog error is
	// among them when its thresholds are misordered. Other sections of this
	// bare config also fail, which is fine — we only check ours is surfaced.
	cfg := &Config{Watchdog: WatchdogConfig{WarnSeconds: 100, FatalSeconds: 10, AbortSeconds: 600}}
	err := ValidateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "watchdog:") {
		t.Fatalf("ValidateConfig did not surface the watchdog error: %v", err)
	}
}

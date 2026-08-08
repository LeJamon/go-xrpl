package config

import (
	"fmt"
	"time"
)

// WatchdogConfig represents the [watchdog] section: the out-of-band stall
// detector that mirrors rippled's LoadManager deadlock detector. The zero value
// is the production default — enabled with rippled's 10s/90s/600s thresholds.
// Operators override the thresholds in seconds, or disable the detector
// entirely with `disabled = true`.
type WatchdogConfig struct {
	// Disabled turns the stall watchdog off entirely (kill-switch).
	Disabled bool `toml:"disabled" mapstructure:"disabled"`

	// WarnSeconds, FatalSeconds, AbortSeconds override the rippled-matching
	// 10/90/600 thresholds. Zero means "use the default".
	WarnSeconds  int `toml:"warn_seconds" mapstructure:"warn_seconds"`
	FatalSeconds int `toml:"fatal_seconds" mapstructure:"fatal_seconds"`
	AbortSeconds int `toml:"abort_seconds" mapstructure:"abort_seconds"`
}

// Validate checks the watchdog thresholds. Disabled configs validate trivially.
// Otherwise overrides must be non-negative and strictly ordered
// warn < fatal < abort so escalation is monotonic.
func (w *WatchdogConfig) Validate() error {
	if w.Disabled {
		return nil
	}
	_, _, _, err := w.Thresholds()
	return err
}

// IsEnabled reports whether the watchdog should be armed.
func (w *WatchdogConfig) IsEnabled() bool {
	return !w.Disabled
}

const (
	defaultWatchdogWarnSeconds  = 10
	defaultWatchdogFatalSeconds = 90
	defaultWatchdogAbortSeconds = 600
	maxDurationSeconds          = int64(1<<63-1) / int64(time.Second)
)

// thresholds resolves the configured overrides against the rippled defaults.
func (w *WatchdogConfig) thresholds() (warn, fatal, abort int) {
	warn, fatal, abort = defaultWatchdogWarnSeconds, defaultWatchdogFatalSeconds, defaultWatchdogAbortSeconds
	if w.WarnSeconds > 0 {
		warn = w.WarnSeconds
	}
	if w.FatalSeconds > 0 {
		fatal = w.FatalSeconds
	}
	if w.AbortSeconds > 0 {
		abort = w.AbortSeconds
	}
	return warn, fatal, abort
}

// Thresholds returns the validated effective watchdog thresholds.
func (w *WatchdogConfig) Thresholds() (warn, fatal, abort time.Duration, err error) {
	rawValues := []struct {
		name    string
		seconds int
	}{
		{name: "warn_seconds", seconds: w.WarnSeconds},
		{name: "fatal_seconds", seconds: w.FatalSeconds},
		{name: "abort_seconds", seconds: w.AbortSeconds},
	}
	for _, value := range rawValues {
		if value.seconds < 0 {
			return 0, 0, 0, fmt.Errorf("%s must be non-negative", value.name)
		}
	}
	warnSeconds, fatalSeconds, abortSeconds := w.thresholds()
	values := []struct {
		name    string
		seconds int
	}{
		{name: "warn_seconds", seconds: warnSeconds},
		{name: "fatal_seconds", seconds: fatalSeconds},
		{name: "abort_seconds", seconds: abortSeconds},
	}
	for _, value := range values {
		if int64(value.seconds) > maxDurationSeconds {
			return 0, 0, 0, fmt.Errorf("%s exceeds maximum duration (%d seconds)", value.name, maxDurationSeconds)
		}
	}
	if !(warnSeconds < fatalSeconds && fatalSeconds < abortSeconds) {
		return 0, 0, 0, fmt.Errorf(
			"thresholds must satisfy warn < fatal < abort, got %d < %d < %d",
			warnSeconds,
			fatalSeconds,
			abortSeconds,
		)
	}
	return time.Duration(warnSeconds) * time.Second,
		time.Duration(fatalSeconds) * time.Second,
		time.Duration(abortSeconds) * time.Second,
		nil
}

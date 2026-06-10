package config

import "fmt"

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
	if w.WarnSeconds < 0 || w.FatalSeconds < 0 || w.AbortSeconds < 0 {
		return fmt.Errorf("threshold seconds must be non-negative")
	}
	warn, fatal, abort := w.thresholds()
	if !(warn < fatal && fatal < abort) {
		return fmt.Errorf("thresholds must satisfy warn < fatal < abort, got %d < %d < %d", warn, fatal, abort)
	}
	return nil
}

// IsEnabled reports whether the watchdog should be armed.
func (w *WatchdogConfig) IsEnabled() bool {
	return !w.Disabled
}

const (
	defaultWatchdogWarnSeconds  = 10
	defaultWatchdogFatalSeconds = 90
	defaultWatchdogAbortSeconds = 600
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

// WarnSecondsResolved returns the effective warn threshold in seconds.
func (w *WatchdogConfig) WarnSecondsResolved() int {
	warn, _, _ := w.thresholds()
	return warn
}

// FatalSecondsResolved returns the effective fatal threshold in seconds.
func (w *WatchdogConfig) FatalSecondsResolved() int {
	_, fatal, _ := w.thresholds()
	return fatal
}

// AbortSecondsResolved returns the effective abort threshold in seconds.
func (w *WatchdogConfig) AbortSecondsResolved() int {
	_, _, abort := w.thresholds()
	return abort
}

package config

import "fmt"

// InsightConfig represents the [insight] section
// Configuration for Beast.Insight (in rippled) stats collection module
type InsightConfig struct {
	Server  string `toml:"server" mapstructure:"server"`
	Address string `toml:"address" mapstructure:"address"`
	Prefix  string `toml:"prefix" mapstructure:"prefix"`
}

// PerfConfig represents the [perf] section
// Configuration of performance logging
type PerfConfig struct {
	PerfLog     string `toml:"perf_log" mapstructure:"perf_log"`
	LogInterval int    `toml:"log_interval" mapstructure:"log_interval"`
}

// Validate performs validation on the Insight configuration
func (i *InsightConfig) Validate() error {
	if i.Server == "" {
		// No server specified, stats collection disabled
		return nil
	}

	switch i.Server {
	case "statsd":
		if i.Address == "" {
			return fmt.Errorf("address is required when server=statsd")
		}
		// Validate address format (IP:port)
		if !isValidAddressPort(i.Address) {
			return fmt.Errorf("invalid address format: %s (expected format: IP:port)", i.Address)
		}
	default:
		return fmt.Errorf("unknown insight server type: %s (valid options: statsd)", i.Server)
	}

	return nil
}

// Validate performs validation on the Perf configuration
func (p *PerfConfig) Validate() error {
	if p.PerfLog == "" {
		// Performance logging disabled
		return nil
	}

	// Validate log interval
	if p.LogInterval < 0 {
		return fmt.Errorf("log_interval must be non-negative, got %d", p.LogInterval)
	}

	return nil
}

// IsEnabled returns true if insight stats collection is enabled
func (i *InsightConfig) IsEnabled() bool {
	return i.Server != ""
}

// IsStatsD returns true if the server type is StatsD
func (i *InsightConfig) IsStatsD() bool {
	return i.Server == "statsd"
}

// GetPrefix returns the metric prefix, or empty string if not set
func (i *InsightConfig) GetPrefix() string {
	return i.Prefix
}

// GetAddress returns the StatsD server address
func (i *InsightConfig) GetAddress() string {
	return i.Address
}

// IsEnabled returns true if performance logging is enabled
func (p *PerfConfig) IsEnabled() bool {
	return p.PerfLog != ""
}

// GetLogInterval returns the log interval
func (p *PerfConfig) GetLogInterval() int {
	return p.LogInterval
}

// GetPerfLogPath returns the performance log file path
func (p *PerfConfig) GetPerfLogPath() string {
	return p.PerfLog
}

// isValidAddressPort validates an address:port string
func isValidAddressPort(addr string) bool {
	// Simple validation - should contain at least one colon and valid format
	if addr == "" {
		return false
	}

	// Find the last colon (in case of IPv6)
	lastColon := -1
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			lastColon = i
			break
		}
	}

	if lastColon == -1 {
		return false // No colon found
	}

	// Check if port part is numeric
	portStr := addr[lastColon+1:]
	if portStr == "" {
		return false
	}

	// Simple numeric check
	for _, r := range portStr {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}

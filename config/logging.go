package config

import (
	"fmt"
	"io"
	"os"

	xrpllog "github.com/LeJamon/goXRPLd/log"
)

// LoggingConfig represents the [logging] section of xrpld.toml.
// It mirrors rippled's per-partition severity configuration model.
//
// Example config:
//
//	[logging]
//	level = "info"
//	format = "text"
//	output = "stdout"
//
//	[logging.partitions]
//	Tx = "debug"
//	Pathfinder = "trace"
type LoggingConfig struct {
	// Level is the global minimum log level.
	// Valid values: "trace", "debug", "info", "warn", "error".
	// Defaults to "info".
	Level string `toml:"level" mapstructure:"level"`

	// Format controls the output format.
	// "text" — human-readable key=value pairs (default, good for development)
	// "json" — JSON lines (good for log aggregators like Loki, Datadog)
	Format string `toml:"format" mapstructure:"format"`

	// Output controls where log records are written.
	// "stdout" — standard output (default)
	// "stderr" — standard error
	// any other value is treated as a file path
	Output string `toml:"output" mapstructure:"output"`

	// Partitions allows per-partition level overrides, matching rippled's
	// partition severity model. Keys are partition names (e.g. "Tx", "Flow",
	// "Pathfinder"); values are level strings.
	Partitions map[string]string `toml:"partitions" mapstructure:"partitions"`
}

// Validate returns an error if the LoggingConfig contains invalid values.
func (c *LoggingConfig) Validate() error {
	validLevels := map[string]bool{
		"":        true, // empty = use default
		"trace":   true,
		"debug":   true,
		"info":    true,
		"warn":    true,
		"warning": true,
		"error":   true,
	}
	if !validLevels[c.Level] {
		return fmt.Errorf("invalid logging level %q (valid: trace, debug, info, warn, error)", c.Level)
	}
	if c.Format != "" && c.Format != "text" && c.Format != "json" {
		return fmt.Errorf("invalid logging format %q (valid: text, json)", c.Format)
	}
	for partition, level := range c.Partitions {
		if !validLevels[level] {
			return fmt.Errorf("invalid level %q for logging partition %q", level, partition)
		}
	}
	return nil
}

// ToLogConfig converts a LoggingConfig to a xrpllog.Config.
// debugLogfile is the legacy debug_logfile path from the top-level config;
// if set and no Output is configured, it is used as the log destination.
func (c *LoggingConfig) ToLogConfig(debugLogfile string) xrpllog.Config {
	cfg := xrpllog.DefaultConfig()

	// Level
	if c.Level != "" {
		if level, ok := parseLevelString(c.Level); ok {
			cfg.Level = level
		}
	}

	// Format
	if c.Format != "" {
		cfg.Format = c.Format
	}

	// Output: explicit config wins, then legacy debug_logfile, then stdout
	outputPath := c.Output
	if outputPath == "" {
		outputPath = debugLogfile
	}
	cfg.Output = resolveOutput(outputPath)

	// Per-partition overrides
	if len(c.Partitions) > 0 {
		cfg.Partitions = make(map[string]xrpllog.Level, len(c.Partitions))
		for partition, levelStr := range c.Partitions {
			if level, ok := parseLevelString(levelStr); ok {
				cfg.Partitions[partition] = level
			}
		}
	}

	return cfg
}

// parseLevelString maps a level name string to an xrpllog.Level.
func parseLevelString(s string) (xrpllog.Level, bool) {
	switch s {
	case "trace":
		return xrpllog.LevelTrace, true
	case "debug":
		return xrpllog.LevelDebug, true
	case "info":
		return xrpllog.LevelInfo, true
	case "warn", "warning":
		return xrpllog.LevelWarn, true
	case "error":
		return xrpllog.LevelError, true
	default:
		return xrpllog.LevelInfo, false
	}
}

// resolveOutput maps an output string to an io.Writer.
// "stdout" and "" → os.Stdout; "stderr" → os.Stderr; otherwise treated as file path.
func resolveOutput(output string) io.Writer {
	switch output {
	case "", "stdout":
		return os.Stdout
	case "stderr":
		return os.Stderr
	default:
		f, err := os.OpenFile(output, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			// Fall back to stdout and let the caller notice via startup logs.
			return os.Stdout
		}
		return f
	}
}

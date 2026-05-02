// Package log provides structured logging for goXRPL.
//
// It wraps Go's standard log/slog with:
//   - A Logger interface for easy injection and test isolation (via Discard)
//   - Trace and Fatal levels (slog doesn't define these)
//   - Named sub-loggers matching rippled's journal partition model
//   - Per-partition level overrides via Config.Partitions
//
// Usage:
//
//	// In main / CLI init:
//	cfg := log.Config{Level: log.LevelInfo, Format: "text", Output: os.Stdout}
//	log.SetRoot(log.New(log.NewHandler(cfg), &cfg))
//
//	// In a subsystem:
//	logger := log.Root().Named(log.PartitionTx)
//	logger.Debug("apply", "txType", "Payment", "account", account)
//
//	// In tests:
//	engine := tx.NewEngine(tx.EngineConfig{Logger: log.Discard()})
package log

import (
	"context"
	"log/slog"
	"os"
)

// Level is an alias for slog.Level, extended with Trace and Fatal values.
type Level = slog.Level

// Log levels. Trace and Fatal extend slog's built-in set.
// LevelTrace = -8 matches go-ethereum's convention.
const (
	LevelTrace = slog.Level(-8)
	LevelDebug = slog.LevelDebug // -4
	LevelInfo  = slog.LevelInfo  //  0
	LevelWarn  = slog.LevelWarn  //  4
	LevelError = slog.LevelError //  8
	LevelFatal = slog.Level(12)
)

// Logger is the main logging interface for goXRPL.
// It mirrors rippled's beast::Journal API but uses Go idioms.
// Use Discard() in tests; use New(handler, cfg) in production.
type Logger interface {
	// Trace logs at Trace level (-8). For hot-path internals (offer loops, path steps).
	Trace(msg string, args ...any)
	// Debug logs at Debug level. For tx apply entry/exit, validation failures.
	Debug(msg string, args ...any)
	// Info logs at Info level. For ledger accepted, server start/stop, tx submitted.
	Info(msg string, args ...any)
	// Warn logs at Warn level. For unusual-but-recoverable conditions.
	Warn(msg string, args ...any)
	// Error logs at Error level. For operation failures.
	Error(msg string, args ...any)
	// Fatal logs at Error level then calls os.Exit(1).
	Fatal(msg string, args ...any)

	// With returns a new Logger with the given key-value pairs baked into every record.
	With(args ...any) Logger

	// Named returns a new Logger scoped to the given partition name.
	// Equivalent to rippled's beast::Journal partition system.
	// The partition's configured level is applied so Enabled() is efficient.
	Named(partition string) Logger
}

// root is the global logger. Defaults to Discard so packages that call
// log.Info() before SetRoot() don't panic.
var root Logger = Discard()

// SetRoot sets the global root logger.
func SetRoot(l Logger) { root = l }

// Root returns the global root logger.
func Root() Logger { return root }

// Package-level convenience functions delegate to root.

func Trace(msg string, args ...any) { root.Trace(msg, args...) }
func Debug(msg string, args ...any) { root.Debug(msg, args...) }
func Info(msg string, args ...any)  { root.Info(msg, args...) }
func Warn(msg string, args ...any)  { root.Warn(msg, args...) }
func Error(msg string, args ...any) { root.Error(msg, args...) }
func Fatal(msg string, args ...any) { root.Fatal(msg, args...) }

// With returns a new Logger derived from root with the given fields.
func With(args ...any) Logger { return root.With(args...) }

// Named returns a new Logger derived from root scoped to the given partition.
func Named(partition string) Logger { return root.Named(partition) }

// parseLevel converts a level string to a Level constant.
// Returns LevelInfo and false if the name is unrecognised.
func parseLevel(s string) (Level, bool) {
	switch s {
	case "trace":
		return LevelTrace, true
	case "debug":
		return LevelDebug, true
	case "info":
		return LevelInfo, true
	case "warn", "warning":
		return LevelWarn, true
	case "error":
		return LevelError, true
	case "fatal":
		return LevelFatal, true
	default:
		return LevelInfo, false
	}
}

// ParseLevel is the exported form of parseLevel for use by RPC handlers and
// external callers. Returns (LevelInfo, false) for unrecognised strings.
func ParseLevel(s string) (Level, bool) { return parseLevel(s) }

// LevelName returns a canonical lowercase name for the given level.
func LevelName(l Level) string {
	switch {
	case l <= LevelTrace:
		return "trace"
	case l <= LevelDebug:
		return "debug"
	case l <= LevelInfo:
		return "info"
	case l <= LevelWarn:
		return "warn"
	case l <= LevelError:
		return "error"
	default:
		return "fatal"
	}
}

// rootCfg is the *Config that backs the global root logger.
// Set by SetRootConfig after SetRoot is called in the CLI bootstrap.
var rootCfg *Config

// SetRootConfig registers cfg as the live config for the root logger.
// Call this once after SetRoot so that SetLevel / SetPartitionLevel work.
func SetRootConfig(cfg *Config) { rootCfg = cfg }

// SetLevel changes the global log level at runtime.
// No-op if SetRootConfig has not been called.
func SetLevel(l Level) {
	if rootCfg != nil {
		rootCfg.SetLevel(l)
	}
}

// SetPartitionLevel changes the level for one partition at runtime.
// No-op if SetRootConfig has not been called.
func SetPartitionLevel(partition string, l Level) {
	if rootCfg != nil {
		rootCfg.SetPartitionLevel(partition, l)
	}
}

// GetCurrentLevels returns a point-in-time snapshot of the global level and
// all per-partition overrides. Returns (LevelInfo, nil) if rootCfg is unset.
func GetCurrentLevels() (global Level, partitions map[string]Level) {
	if rootCfg == nil {
		return LevelInfo, nil
	}
	rootCfg.initDyn()
	rootCfg.dyn.mu.RLock()
	defer rootCfg.dyn.mu.RUnlock()
	global = rootCfg.Level
	if len(rootCfg.Partitions) > 0 {
		partitions = make(map[string]Level, len(rootCfg.Partitions))
		for k, v := range rootCfg.Partitions {
			partitions[k] = v
		}
	}
	return
}

// defaultExit is called by Fatal after logging. Replaceable in tests.
var defaultExit = func() { os.Exit(1) }

// bgCtx is a cached background context for log calls.
var bgCtx = context.Background()

package log

import (
	"log/slog"
)

// logger is the concrete Logger implementation backed by *slog.Logger.
type logger struct {
	inner *slog.Logger
	cfg   *Config
}

// New creates a Logger backed by the given slog.Handler and Config.
// cfg is stored by pointer so Named() always reflects the current partition map.
func New(handler slog.Handler, cfg *Config) Logger {
	return &logger{
		inner: slog.New(handler),
		cfg:   cfg,
	}
}

func (l *logger) Trace(msg string, args ...any) {
	l.inner.Log(bgCtx, LevelTrace, msg, args...)
}

func (l *logger) Debug(msg string, args ...any) {
	l.inner.Debug(msg, args...)
}

func (l *logger) Info(msg string, args ...any) {
	l.inner.Info(msg, args...)
}

func (l *logger) Warn(msg string, args ...any) {
	l.inner.Warn(msg, args...)
}

func (l *logger) Error(msg string, args ...any) {
	l.inner.Error(msg, args...)
}

func (l *logger) Fatal(msg string, args ...any) {
	l.inner.Error(msg, args...)
	defaultExit()
}

// With returns a new Logger with the given key-value pairs baked in.
func (l *logger) With(args ...any) Logger {
	return &logger{
		inner: l.inner.With(args...),
		cfg:   l.cfg,
	}
}

// Named returns a new Logger scoped to the given partition.
//
// The partition's effective level is looked up from Config.Partitions.
// A slog.LevelHandler at that level is installed so Enabled() is fast.
// The "t" key (short for "topic", matching rippled's partition display)
// is baked into every record emitted by this logger.
func (l *logger) Named(partition string) Logger {
	level := l.cfg.partitionLevel(partition)
	h := newLevelHandler(level, l.inner.Handler())
	return &logger{
		inner: slog.New(h).With("t", partition),
		cfg:   l.cfg,
	}
}

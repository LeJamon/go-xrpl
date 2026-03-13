package log

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
)

// configDynamic holds the hot-reloadable parts of Config.
// Stored by pointer so Config itself remains copy-safe (no embedded mutex).
type configDynamic struct {
	mu         sync.RWMutex
	globalVar  *slog.LevelVar
	partitions map[string]*slog.LevelVar
}

// Config holds all configuration needed to build a Logger.
type Config struct {
	// Level is the global minimum level. Records below this are dropped
	// unless a partition override specifies a lower (more verbose) level.
	Level Level

	// Format controls the output format: "text" (human-readable) or "json".
	Format string

	// Output is the writer for log records. Defaults to os.Stdout.
	Output io.Writer

	// Partitions maps partition names to their override level.
	// Omitted partitions use the global Level.
	Partitions map[string]Level

	// dyn holds live LevelVars initialised by NewHandler.
	// Nil until NewHandler is called; safe for Config to be copied before that.
	dyn *configDynamic
}

// initDyn lazily initialises the dynamic state from the static Level fields.
// Idempotent — safe to call multiple times.
func (c *Config) initDyn() {
	if c.dyn != nil {
		return
	}
	d := &configDynamic{
		globalVar:  &slog.LevelVar{},
		partitions: make(map[string]*slog.LevelVar, len(c.Partitions)),
	}
	d.globalVar.Set(c.Level)
	for name, lvl := range c.Partitions {
		v := &slog.LevelVar{}
		v.Set(lvl)
		d.partitions[name] = v
	}
	c.dyn = d
}

// SetLevel changes the global log level at runtime.
// All loggers built from this Config reflect the change immediately.
func (c *Config) SetLevel(l Level) {
	c.initDyn()
	c.Level = l
	c.dyn.globalVar.Set(l)
}

// SetPartitionLevel changes the level for one partition at runtime.
// Creates an override entry if the partition did not previously have one.
func (c *Config) SetPartitionLevel(partition string, l Level) {
	c.initDyn()
	if c.Partitions == nil {
		c.Partitions = make(map[string]Level)
	}
	c.Partitions[partition] = l
	c.dyn.mu.Lock()
	defer c.dyn.mu.Unlock()
	if v, ok := c.dyn.partitions[partition]; ok {
		v.Set(l)
	} else {
		v := &slog.LevelVar{}
		v.Set(l)
		c.dyn.partitions[partition] = v
	}
}

// getPartitionLeveler returns the live slog.Leveler for a partition.
// If the partition has an override it returns that LevelVar; otherwise the
// global LevelVar so changes via SetLevel propagate to unoverridden partitions.
func (c *Config) getPartitionLeveler(partition string) slog.Leveler {
	if c == nil {
		return LevelInfo
	}
	c.initDyn()
	c.dyn.mu.RLock()
	v, ok := c.dyn.partitions[partition]
	c.dyn.mu.RUnlock()
	if ok {
		return v
	}
	return c.dyn.globalVar
}

// DefaultConfig returns a Config suitable for development:
// Info level, text format, stdout output.
func DefaultConfig() Config {
	return Config{
		Level:      LevelInfo,
		Format:     "text",
		Output:     os.Stdout,
		Partitions: nil,
	}
}

// partitionLevel returns the effective level for the given partition.
// Falls back to the global Level if no override is set.
func (c *Config) partitionLevel(partition string) Level {
	if c == nil {
		return LevelInfo
	}
	if override, ok := c.Partitions[partition]; ok {
		return override
	}
	return c.Level
}

// NewHandler builds a slog.Handler from cfg.
// Text format uses slog.NewTextHandler; JSON uses slog.NewJSONHandler.
// NewHandler initialises cfg's dynamic LevelVars so that SetLevel /
// SetPartitionLevel calls on the same *Config take effect immediately.
func NewHandler(cfg *Config) slog.Handler {
	if cfg == nil {
		cfg = &Config{Level: LevelInfo, Format: "text", Output: os.Stdout}
	}
	out := cfg.Output
	if out == nil {
		out = os.Stdout
	}

	cfg.initDyn() // ensure globalVar is live

	opts := &slog.HandlerOptions{
		Level:       cfg.dyn.globalVar, // *slog.LevelVar — hot-reloadable
		ReplaceAttr: replaceLevel,
	}

	switch cfg.Format {
	case "json":
		return slog.NewJSONHandler(out, opts)
	default: // "text" and anything unrecognised
		return slog.NewTextHandler(out, opts)
	}
}

// NewMultiHandler returns a slog.Handler that fans records out to all provided
// handlers. Useful for writing to both stdout and a log file simultaneously.
func NewMultiHandler(handlers ...slog.Handler) slog.Handler {
	return &multiHandler{handlers: handlers}
}

// multiHandler fans log records out to multiple handlers.
type multiHandler struct {
	handlers []slog.Handler
}

// Enabled returns true if any child handler is enabled for the given level.
func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle passes the record to all child handlers.
func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

// WithAttrs returns a new multiHandler with each child's WithAttrs applied.
func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: handlers}
}

// WithGroup returns a new multiHandler with each child's WithGroup applied.
func (m *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: handlers}
}

// newLevelHandler returns a slog.Handler that filters records below the given level.
// level may be a static slog.Level or a *slog.LevelVar for live updates.
func newLevelHandler(level slog.Leveler, h slog.Handler) slog.Handler {
	return &levelHandler{level: level, inner: h}
}

// levelHandler wraps a slog.Handler and filters by a minimum level.
// Using slog.Leveler allows the threshold to be a live *slog.LevelVar.
type levelHandler struct {
	level slog.Leveler
	inner slog.Handler
}

func (h *levelHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *levelHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.inner.Handle(ctx, r)
}

func (h *levelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelHandler{level: h.level, inner: h.inner.WithAttrs(attrs)}
}

func (h *levelHandler) WithGroup(name string) slog.Handler {
	return &levelHandler{level: h.level, inner: h.inner.WithGroup(name)}
}

// replaceLevel rewrites the slog level attribute so that Trace appears as
// "TRACE" instead of "DEBUG-4" in text output, matching rippled's conventions.
func replaceLevel(_ []string, a slog.Attr) slog.Attr {
	if a.Key != slog.LevelKey {
		return a
	}
	level, ok := a.Value.Any().(slog.Level)
	if !ok {
		return a
	}
	switch {
	case level <= LevelTrace:
		a.Value = slog.StringValue("TRACE")
	case level <= LevelDebug:
		a.Value = slog.StringValue("DEBUG")
	case level <= LevelInfo:
		a.Value = slog.StringValue("INFO")
	case level <= LevelWarn:
		a.Value = slog.StringValue("WARN")
	default:
		a.Value = slog.StringValue("ERROR")
	}
	return a
}

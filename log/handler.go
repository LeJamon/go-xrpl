package log

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
)

// configDynamic holds the hot-reloadable parts of Config.
// Stored by pointer so Config itself remains copy-safe (no embedded mutex).
type configDynamic struct {
	mu         sync.RWMutex
	globalVar  *slog.LevelVar
	partitions map[string]*slog.LevelVar
}

// Config holds all configuration needed to build a Logger.
//
// Once NewHandler (or any SetLevel / SetPartitionLevel call) has initialised
// the dynamic state, Level and Partitions must only be read or mutated through
// the SetLevel / SetPartitionLevel / GetCurrentLevels API — direct writes to
// the static fields are not race-safe against concurrent logging.
//
// Copying a Config after NewHandler / SetLevel has been called shares the
// dynamic state via the dyn pointer; copy-then-mutate-the-copy is not
// supported. Build a fresh Config instead.
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

	// Async, when set, routes records through a bounded background queue
	// (drop-on-full) instead of writing them on the calling goroutine, so a
	// slow or blocked output can never stall a logging caller — in particular
	// the consensus strand, which logs under its engine lock. Leave unset for
	// tests and library use, where output must be synchronous and deterministic.
	Async bool

	// AsyncQueueDepth bounds the records buffered when Async is set.
	// Zero uses asyncQueueDepth.
	AsyncQueueDepth int

	// dyn holds live LevelVars initialised by initDyn. Stored as
	// atomic.Pointer so concurrent first-touches don't race on the
	// pointer, and so Config itself remains copy-safe (no embedded mutex).
	dyn atomic.Pointer[configDynamic]

	// asyncOut holds the live async writer when Async is set, so Sync can drain
	// it on the abort path. Set once by NewHandler.
	asyncOut atomic.Pointer[asyncWriter]
}

// initDyn lazily initialises the dynamic state from the static Level fields.
// Safe for concurrent first-use: losing writers drop their allocation.
func (c *Config) initDyn() *configDynamic {
	if d := c.dyn.Load(); d != nil {
		return d
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
	if !c.dyn.CompareAndSwap(nil, d) {
		// Lost the race; another goroutine published first. Discard ours.
		return c.dyn.Load()
	}
	return d
}

// SetLevel changes the global log level at runtime.
// All loggers built from this Config reflect the change immediately.
func (c *Config) SetLevel(l Level) {
	d := c.initDyn()
	d.mu.Lock()
	defer d.mu.Unlock()
	c.Level = l
	d.globalVar.Set(l)
}

// SetPartitionLevel changes the level for one partition at runtime.
// Creates an override entry if the partition did not previously have one.
func (c *Config) SetPartitionLevel(partition string, l Level) {
	d := c.initDyn()
	d.mu.Lock()
	defer d.mu.Unlock()
	if c.Partitions == nil {
		c.Partitions = make(map[string]Level)
	}
	c.Partitions[partition] = l
	if v, ok := d.partitions[partition]; ok {
		v.Set(l)
	} else {
		v := &slog.LevelVar{}
		v.Set(l)
		d.partitions[partition] = v
	}
}

// getPartitionLeveler returns the live slog.Leveler for a partition.
// If the partition has an override it returns that LevelVar; otherwise the
// global LevelVar so changes via SetLevel propagate to unoverridden partitions.
func (c *Config) getPartitionLeveler(partition string) slog.Leveler {
	if c == nil {
		return LevelInfo
	}
	d := c.initDyn()
	d.mu.RLock()
	v, ok := d.partitions[partition]
	d.mu.RUnlock()
	if ok {
		return v
	}
	return d.globalVar
}

// DefaultConfig returns a Config suitable for development:
// Info level, text format, stdout output.
//
// Returns a pointer because Config embeds an atomic.Pointer that must not be
// copied by value once the Config is in use.
func DefaultConfig() *Config {
	return &Config{
		Level:      LevelInfo,
		Format:     "text",
		Output:     os.Stdout,
		Partitions: nil,
	}
}

// NewHandler builds a slog.Handler from cfg.
// Text format uses slog.NewTextHandler; JSON uses slog.NewJSONHandler.
// NewHandler initialises cfg's dynamic LevelVars so that SetLevel /
// SetPartitionLevel calls on the same *Config take effect immediately.
// When cfg.Async is set the output is wrapped so writes never block the caller.
func NewHandler(cfg *Config) slog.Handler {
	if cfg == nil {
		cfg = &Config{Level: LevelInfo, Format: "text", Output: os.Stdout}
	}
	out := cfg.Output
	if out == nil {
		out = os.Stdout
	}
	if cfg.Async {
		aw := newAsyncWriter(out, cfg.AsyncQueueDepth)
		cfg.asyncOut.Store(aw)
		out = aw
	}

	d := cfg.initDyn() // ensure globalVar is live

	opts := &slog.HandlerOptions{
		Level:       d.globalVar, // *slog.LevelVar — hot-reloadable
		ReplaceAttr: replaceLevel,
	}

	switch cfg.Format {
	case "json":
		return slog.NewJSONHandler(out, opts)
	default: // "text" and anything unrecognised
		return slog.NewTextHandler(out, opts)
	}
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
	case level <= LevelError:
		a.Value = slog.StringValue("ERROR")
	default:
		a.Value = slog.StringValue("FATAL")
	}
	return a
}

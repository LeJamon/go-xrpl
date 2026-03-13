package log

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// captureLogger returns a new Logger that writes to buf at the given level.
func captureLogger(level Level, format string) (*bytes.Buffer, Logger) {
	var buf bytes.Buffer
	cfg := &Config{Level: level, Format: format, Output: &buf}
	h := NewHandler(cfg)
	return &buf, New(h, cfg)
}

// TestLevels_Text verifies that each level produces the correct label in text output.
func TestLevels_Text(t *testing.T) {
	tests := []struct {
		name      string
		logFn     func(Logger)
		wantLabel string
	}{
		{"trace", func(l Logger) { l.Trace("msg") }, "TRACE"},
		{"debug", func(l Logger) { l.Debug("msg") }, "DEBUG"},
		{"info", func(l Logger) { l.Info("msg") }, "INFO"},
		{"warn", func(l Logger) { l.Warn("msg") }, "WARN"},
		{"error", func(l Logger) { l.Error("msg") }, "ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf, l := captureLogger(LevelTrace, "text")
			tt.logFn(l)
			got := buf.String()
			if !strings.Contains(got, "level="+tt.wantLabel) {
				t.Errorf("want level=%s in %q", tt.wantLabel, got)
			}
		})
	}
}

// TestLevels_JSON verifies that each level produces the correct label in JSON output.
func TestLevels_JSON(t *testing.T) {
	tests := []struct {
		name      string
		logFn     func(Logger)
		wantLabel string
	}{
		{"trace", func(l Logger) { l.Trace("msg") }, "TRACE"},
		{"debug", func(l Logger) { l.Debug("msg") }, "DEBUG"},
		{"info", func(l Logger) { l.Info("msg") }, "INFO"},
		{"warn", func(l Logger) { l.Warn("msg") }, "WARN"},
		{"error", func(l Logger) { l.Error("msg") }, "ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf, l := captureLogger(LevelTrace, "json")
			tt.logFn(l)
			var rec map[string]any
			if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
				t.Fatalf("invalid JSON %q: %v", buf.String(), err)
			}
			if rec["level"] != tt.wantLabel {
				t.Errorf("want level=%s, got %v", tt.wantLabel, rec["level"])
			}
		})
	}
}

// TestLevelFiltering verifies that records below the global level are dropped.
func TestLevelFiltering(t *testing.T) {
	buf, l := captureLogger(LevelInfo, "text")
	l.Trace("secret-trace")
	l.Debug("secret-debug")
	l.Info("visible")

	got := buf.String()
	if strings.Contains(got, "secret") {
		t.Errorf("filtered-out record appeared in output: %q", got)
	}
	if !strings.Contains(got, "visible") {
		t.Errorf("expected Info record missing from output: %q", got)
	}
}

// TestNamed_BakesPartitionKey verifies Named() bakes "t=<partition>" into every record.
func TestNamed_BakesPartitionKey(t *testing.T) {
	buf, l := captureLogger(LevelTrace, "text")
	l.Named("MyPartition").Info("hello")
	if !strings.Contains(buf.String(), "t=MyPartition") {
		t.Errorf("partition key missing from output: %q", buf.String())
	}
}

// TestNamed_PartitionLevelOverride_Verbose verifies a partition set to Debug
// emits Debug records even when the global level is Info.
func TestNamed_PartitionLevelOverride_Verbose(t *testing.T) {
	var buf bytes.Buffer
	cfg := &Config{
		Level:      LevelInfo,
		Format:     "text",
		Output:     &buf,
		Partitions: map[string]Level{"VerbosePart": LevelDebug},
	}
	l := New(NewHandler(cfg), cfg)
	l.Named("VerbosePart").Debug("debug-from-partition")

	if !strings.Contains(buf.String(), "debug-from-partition") {
		t.Errorf("partition Debug override not applied: %q", buf.String())
	}
}

// TestNamed_PartitionLevelOverride_Quiet verifies a partition set to Warn
// silences Debug records even when the global level is Debug.
func TestNamed_PartitionLevelOverride_Quiet(t *testing.T) {
	var buf bytes.Buffer
	cfg := &Config{
		Level:      LevelDebug,
		Format:     "text",
		Output:     &buf,
		Partitions: map[string]Level{"QuietPart": LevelWarn},
	}
	l := New(NewHandler(cfg), cfg)
	named := l.Named("QuietPart")
	named.Debug("should-be-silenced")
	named.Warn("should-appear")

	got := buf.String()
	if strings.Contains(got, "should-be-silenced") {
		t.Errorf("partition Warn override did not silence Debug: %q", got)
	}
	if !strings.Contains(got, "should-appear") {
		t.Errorf("Warn record missing after partition override: %q", got)
	}
}

// TestWith_BakesFields verifies With() bakes key-value pairs into every record.
func TestWith_BakesFields(t *testing.T) {
	buf, l := captureLogger(LevelTrace, "text")
	l.With("mykey", "myval").Info("hello")
	if !strings.Contains(buf.String(), "mykey=myval") {
		t.Errorf("With() field missing from output: %q", buf.String())
	}
}

// TestDiscard_NoPanic verifies that calling all methods on Discard() never panics.
func TestDiscard_NoPanic(t *testing.T) {
	d := Discard()
	d.Trace("x")
	d.Debug("x")
	d.Info("x")
	d.Warn("x")
	d.Error("x")
	_ = d.With("k", "v")
	_ = d.Named("p")
}

// TestDiscard_WithNamed_ReturnEquivalent verifies With/Named on Discard return
// a functionally equivalent no-op logger (empty struct, so value-equal).
func TestDiscard_WithNamed_ReturnEquivalent(t *testing.T) {
	d := Discard()
	if d.With("k", "v") != d {
		t.Error("Discard().With() should return an equivalent discard logger")
	}
	if d.Named("part") != d {
		t.Error("Discard().Named() should return an equivalent discard logger")
	}
}

// TestMultiHandler verifies that NewMultiHandler fans records to all children.
func TestMultiHandler(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	cfg1 := &Config{Level: LevelTrace, Format: "text", Output: &buf1}
	cfg2 := &Config{Level: LevelTrace, Format: "text", Output: &buf2}

	multi := NewMultiHandler(NewHandler(cfg1), NewHandler(cfg2))
	cfgMulti := &Config{Level: LevelTrace}
	l := New(multi, cfgMulti)
	l.Info("fan-out")

	if !strings.Contains(buf1.String(), "fan-out") {
		t.Error("handler 1 did not receive record")
	}
	if !strings.Contains(buf2.String(), "fan-out") {
		t.Error("handler 2 did not receive record")
	}
}

// TestMultiHandler_Enabled verifies Enabled returns true if any child is enabled.
func TestMultiHandler_Enabled(t *testing.T) {
	var buf bytes.Buffer
	lowCfg := &Config{Level: LevelTrace, Format: "text", Output: &buf}
	highCfg := &Config{Level: LevelError, Format: "text", Output: &buf}

	multi := NewMultiHandler(NewHandler(lowCfg), NewHandler(highCfg))
	if !multi.Enabled(bgCtx, LevelDebug) {
		t.Error("multi.Enabled should return true when at least one child accepts the level")
	}
}

// TestSetRoot_Root verifies SetRoot/Root and package-level delegation.
func TestSetRoot_Root(t *testing.T) {
	original := Root()
	defer SetRoot(original)

	buf, l := captureLogger(LevelTrace, "text")
	SetRoot(l)
	if Root() != l {
		t.Error("Root() should return the logger set by SetRoot()")
	}

	Info("root-delegation")
	if !strings.Contains(buf.String(), "root-delegation") {
		t.Errorf("package-level Info() did not delegate to root: %q", buf.String())
	}
}

// TestFatal_LogsAndCallsExit verifies Fatal() logs the message then calls defaultExit.
func TestFatal_LogsAndCallsExit(t *testing.T) {
	exited := false
	old := defaultExit
	defer func() { defaultExit = old }()
	defaultExit = func() { exited = true }

	buf, l := captureLogger(LevelTrace, "text")
	l.Fatal("fatal-msg")

	if !exited {
		t.Error("Fatal() did not call defaultExit")
	}
	if !strings.Contains(buf.String(), "fatal-msg") {
		t.Errorf("Fatal() did not log the message: %q", buf.String())
	}
}

// TestDefaultConfig verifies DefaultConfig() returns sensible defaults.
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Level != LevelInfo {
		t.Errorf("DefaultConfig level: want LevelInfo, got %v", cfg.Level)
	}
	if cfg.Format != "text" {
		t.Errorf("DefaultConfig format: want text, got %q", cfg.Format)
	}
	if cfg.Output == nil {
		t.Error("DefaultConfig output must not be nil")
	}
}

// TestSetLevel_HotReload verifies that cfg.SetLevel propagates to already-created loggers.
func TestSetLevel_HotReload(t *testing.T) {
	var buf bytes.Buffer
	cfg := &Config{Level: LevelInfo, Format: "text", Output: &buf}
	l := New(NewHandler(cfg), cfg)
	named := l.Named("HotPart")
	named.Debug("before-hot-reload")
	if strings.Contains(buf.String(), "before-hot-reload") {
		t.Error("Debug should be suppressed at Info level")
	}
	cfg.SetLevel(LevelDebug)
	named.Debug("after-hot-reload")
	if !strings.Contains(buf.String(), "after-hot-reload") {
		t.Error("Debug should appear after hot-reload to Debug level")
	}
}

// TestSetPartitionLevel_HotReload verifies that cfg.SetPartitionLevel propagates to already-created named loggers.
// The partition must be pre-registered in Config.Partitions so Named() receives its *slog.LevelVar;
// updating an already-known partition's LevelVar is what hot-reload supports.
func TestSetPartitionLevel_HotReload(t *testing.T) {
	var buf bytes.Buffer
	cfg := &Config{
		Level:      LevelInfo,
		Format:     "text",
		Output:     &buf,
		Partitions: map[string]Level{"HotPartition": LevelInfo}, // pre-register at Info
	}
	l := New(NewHandler(cfg), cfg)
	named := l.Named("HotPartition") // gets the partition's *slog.LevelVar
	named.Trace("before-partition-reload")
	if strings.Contains(buf.String(), "before-partition-reload") {
		t.Error("Trace should be suppressed before partition hot-reload")
	}
	cfg.SetPartitionLevel("HotPartition", LevelTrace) // updates the existing *slog.LevelVar
	named.Trace("after-partition-reload")
	if !strings.Contains(buf.String(), "after-partition-reload") {
		t.Error("Trace should appear after partition hot-reload to Trace")
	}
}

// TestPartitionLevel_FallsBackToGlobal verifies that an unknown partition uses the global level.
func TestPartitionLevel_FallsBackToGlobal(t *testing.T) {
	cfg := Config{Level: LevelWarn, Partitions: map[string]Level{"Known": LevelDebug}}
	if got := cfg.partitionLevel("Unknown"); got != LevelWarn {
		t.Errorf("unknown partition should fall back to global level, got %v", got)
	}
	if got := cfg.partitionLevel("Known"); got != LevelDebug {
		t.Errorf("known partition override not returned, got %v", got)
	}
}

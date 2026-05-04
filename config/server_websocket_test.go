package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Pins each Default* constant to its expected value. If any of these
// flip, "[websocket] omitted" no longer yields the contract documented
// on WebSocketConfig.
func TestWebSocketConfig_DefaultsMatchPreRefactorConstants(t *testing.T) {
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"MaxReadSize", DefaultWebSocketMaxReadSize, int64(512 * 1024)},
		{"ReadTimeout", DefaultWebSocketReadTimeout, 90 * time.Second},
		{"WriteTimeout", DefaultWebSocketWriteTimeout, 10 * time.Second},
		{"PingInterval", DefaultWebSocketPingInterval, 30 * time.Second},
		{"PongTimeout", DefaultWebSocketPongTimeout, 90 * time.Second},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("Default%s = %v, want %v (pre-refactor hardcoded value)", c.name, c.got, c.want)
		}
	}
}

func TestWebSocketConfig_WithDefaults_ZeroFieldsFallBack(t *testing.T) {
	out := WebSocketConfig{}.WithDefaults()
	if out.MaxReadSize != DefaultWebSocketMaxReadSize {
		t.Errorf("MaxReadSize = %d, want %d", out.MaxReadSize, DefaultWebSocketMaxReadSize)
	}
	if out.ReadTimeout != DefaultWebSocketReadTimeout {
		t.Errorf("ReadTimeout = %s, want %s", out.ReadTimeout, DefaultWebSocketReadTimeout)
	}
	if out.WriteTimeout != DefaultWebSocketWriteTimeout {
		t.Errorf("WriteTimeout = %s, want %s", out.WriteTimeout, DefaultWebSocketWriteTimeout)
	}
	if out.PingInterval != DefaultWebSocketPingInterval {
		t.Errorf("PingInterval = %s, want %s", out.PingInterval, DefaultWebSocketPingInterval)
	}
	if out.PongTimeout != DefaultWebSocketPongTimeout {
		t.Errorf("PongTimeout = %s, want %s", out.PongTimeout, DefaultWebSocketPongTimeout)
	}
}

func TestWebSocketConfig_WithDefaults_NonZeroFieldsPreserved(t *testing.T) {
	in := WebSocketConfig{
		MaxReadSize:  1024 * 1024,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 5 * time.Second,
		PingInterval: 15 * time.Second,
		PongTimeout:  45 * time.Second,
	}
	out := in.WithDefaults()
	if out != in {
		t.Errorf("WithDefaults overwrote a non-zero field: in=%+v out=%+v", in, out)
	}
}

func TestWebSocketConfig_Validate_AcceptsZeroAndDefaults(t *testing.T) {
	if err := (WebSocketConfig{}).Validate(); err != nil {
		t.Errorf("zero-value WebSocketConfig must validate, got %v", err)
	}
	defaults := WebSocketConfig{}.WithDefaults()
	if err := defaults.Validate(); err != nil {
		t.Errorf("defaulted WebSocketConfig must validate, got %v", err)
	}
}

func TestWebSocketConfig_Validate_RejectsNegatives(t *testing.T) {
	cases := []struct {
		name string
		cfg  WebSocketConfig
		want string
	}{
		{"MaxReadSize", WebSocketConfig{MaxReadSize: -1}, "max_read_size"},
		{"ReadTimeout", WebSocketConfig{ReadTimeout: -1}, "read_timeout"},
		{"WriteTimeout", WebSocketConfig{WriteTimeout: -1}, "write_timeout"},
		{"PingInterval", WebSocketConfig{PingInterval: -1}, "ping_interval"},
		{"PongTimeout", WebSocketConfig{PongTimeout: -1}, "pong_timeout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			if err == nil {
				t.Fatalf("expected error for negative %s, got nil", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q missing field token %q", err, c.want)
			}
		})
	}
}

// Validate must catch unit typos like "30ns" — values that are positive
// but absurdly small would otherwise produce sub-millisecond timers.
func TestWebSocketConfig_Validate_RejectsBelowMinimumDuration(t *testing.T) {
	cases := []struct {
		name  string
		cfg   WebSocketConfig
		field string
	}{
		{"ReadTimeout", WebSocketConfig{ReadTimeout: 30 * time.Nanosecond}, "read_timeout"},
		{"WriteTimeout", WebSocketConfig{WriteTimeout: time.Microsecond}, "write_timeout"},
		{"PingInterval", WebSocketConfig{PingInterval: time.Millisecond}, "ping_interval"},
		{"PongTimeout", WebSocketConfig{PongTimeout: 99 * time.Millisecond}, "pong_timeout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			if err == nil {
				t.Fatalf("expected error for sub-minimum %s, got nil", c.name)
			}
			if !strings.Contains(err.Error(), c.field) {
				t.Errorf("error %q missing field token %q", err, c.field)
			}
		})
	}
}

func TestWebSocketConfig_Validate_AcceptsAtAndAboveMinimum(t *testing.T) {
	cfg := WebSocketConfig{
		MaxReadSize:  minWebSocketReadSize,
		ReadTimeout:  minWebSocketDuration,
		WriteTimeout: minWebSocketDuration,
		PingInterval: minWebSocketDuration,
		PongTimeout:  minWebSocketDuration,
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("values exactly at the configured minimums must validate, got %v", err)
	}
}

// Validate must reject positive-but-trivially-small MaxReadSize values
// that would reject every realistic XRPL command frame at runtime.
func TestWebSocketConfig_Validate_RejectsBelowMinimumReadSize(t *testing.T) {
	for _, size := range []int64{1, 64, minWebSocketReadSize - 1} {
		err := WebSocketConfig{MaxReadSize: size}.Validate()
		if err == nil {
			t.Fatalf("expected error for sub-minimum MaxReadSize=%d, got nil", size)
		}
		if !strings.Contains(err.Error(), "max_read_size") {
			t.Errorf("error %q missing field token %q", err, "max_read_size")
		}
	}
}

// End-to-end check that the [websocket] TOML section round-trips through
// the loader: every field tag must match the documented key and the
// duration decode-hook must convert "120s" / "20s" / etc. into
// time.Duration. A regression here (renamed tag, dropped DecodeHook on a
// future viper bump) would silently fall back to defaults at runtime.
func TestLoadConfig_WebSocketSectionRoundTrips(t *testing.T) {
	tempDir := t.TempDir()
	mainConfigPath := filepath.Join(tempDir, "test_config.toml")

	body := completeTestConfig() + `
[websocket]
max_read_size = 1048576
read_timeout  = "120s"
write_timeout = "15s"
ping_interval = "20s"
pong_timeout  = "60s"
`
	require.NoError(t, os.WriteFile(mainConfigPath, []byte(body), 0644))

	cfg, err := LoadConfig(ConfigPaths{Main: mainConfigPath})
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, int64(1048576), cfg.WebSocket.MaxReadSize)
	assert.Equal(t, 120*time.Second, cfg.WebSocket.ReadTimeout)
	assert.Equal(t, 15*time.Second, cfg.WebSocket.WriteTimeout)
	assert.Equal(t, 20*time.Second, cfg.WebSocket.PingInterval)
	assert.Equal(t, 60*time.Second, cfg.WebSocket.PongTimeout)
}

// Omitting the [websocket] section must yield a zero-value
// WebSocketConfig that defaults correctly via WithDefaults — the
// contract documented on WebSocketConfig.
func TestLoadConfig_WebSocketSectionOmittedYieldsDefaults(t *testing.T) {
	tempDir := t.TempDir()
	mainConfigPath := filepath.Join(tempDir, "test_config.toml")
	require.NoError(t, os.WriteFile(mainConfigPath, []byte(completeTestConfig()), 0644))

	cfg, err := LoadConfig(ConfigPaths{Main: mainConfigPath})
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, WebSocketConfig{}, cfg.WebSocket)

	defaulted := cfg.WebSocket.WithDefaults()
	assert.Equal(t, DefaultWebSocketMaxReadSize, defaulted.MaxReadSize)
	assert.Equal(t, DefaultWebSocketReadTimeout, defaulted.ReadTimeout)
	assert.Equal(t, DefaultWebSocketWriteTimeout, defaulted.WriteTimeout)
	assert.Equal(t, DefaultWebSocketPingInterval, defaulted.PingInterval)
	assert.Equal(t, DefaultWebSocketPongTimeout, defaulted.PongTimeout)
}

// A sub-minimum value in the loaded TOML must surface as a Validate error
// from the loader, not silently fall through to runtime.
func TestLoadConfig_WebSocketSectionRejectsBadValues(t *testing.T) {
	tempDir := t.TempDir()
	mainConfigPath := filepath.Join(tempDir, "test_config.toml")

	body := completeTestConfig() + `
[websocket]
read_timeout = "30ns"
`
	require.NoError(t, os.WriteFile(mainConfigPath, []byte(body), 0644))

	_, err := LoadConfig(ConfigPaths{Main: mainConfigPath})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read_timeout")
}

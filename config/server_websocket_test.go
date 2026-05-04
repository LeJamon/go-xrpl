package config

import (
	"strings"
	"testing"
	"time"
)

// Pins each Default* constant to the value hardcoded in
// internal/rpc/websocket.go before WebSocketConfig was introduced. If any
// of these flip, "[websocket] omitted" no longer reproduces historical
// behavior — which is the contract WebSocketConfig promises.
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
		ReadTimeout:  minWebSocketDuration,
		WriteTimeout: minWebSocketDuration,
		PingInterval: minWebSocketDuration,
		PongTimeout:  minWebSocketDuration,
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("durations exactly at minWebSocketDuration must validate, got %v", err)
	}
}

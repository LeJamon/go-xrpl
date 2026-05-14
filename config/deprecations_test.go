package config

import (
	"strings"
	"testing"
	"time"
)

type captureLogger struct {
	msgs []string
	args [][]any
}

func (c *captureLogger) Warn(msg string, args ...any) {
	c.msgs = append(c.msgs, msg)
	c.args = append(c.args, args)
}

func TestConfig_LogDeprecations_NoOpWhenUnset(t *testing.T) {
	log := &captureLogger{}
	(&Config{}).LogDeprecations(log)
	if len(log.msgs) != 0 {
		t.Fatalf("expected no warnings, got %v", log.msgs)
	}
}

func TestConfig_LogDeprecations_NilSafe(t *testing.T) {
	var c *Config
	c.LogDeprecations(&captureLogger{})

	(&Config{WebsocketPingFrequency: 60}).LogDeprecations(nil)
}

func TestConfig_LogDeprecations_WarnsOnDeprecatedFieldAlone(t *testing.T) {
	log := &captureLogger{}
	(&Config{WebsocketPingFrequency: 60}).LogDeprecations(log)

	if len(log.msgs) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(log.msgs), log.msgs)
	}
	if !strings.Contains(log.msgs[0], "websocket_ping_frequency is deprecated") {
		t.Errorf("unexpected message: %q", log.msgs[0])
	}
	if strings.Contains(log.msgs[0], "overridden") {
		t.Errorf("alone-case must not mention precedence, got %q", log.msgs[0])
	}
}

func TestConfig_LogDeprecations_PrecedenceWarning(t *testing.T) {
	log := &captureLogger{}
	(&Config{
		WebsocketPingFrequency: 60,
		WebSocket:              WebSocketConfig{PingInterval: 20 * time.Second},
	}).LogDeprecations(log)

	if len(log.msgs) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(log.msgs), log.msgs)
	}
	if !strings.Contains(log.msgs[0], "overridden by websocket.ping_interval") {
		t.Errorf("expected precedence wording, got %q", log.msgs[0])
	}
}

func TestConfig_LogDeprecations_PingIntervalAloneIsSilent(t *testing.T) {
	log := &captureLogger{}
	(&Config{
		WebSocket: WebSocketConfig{PingInterval: 20 * time.Second},
	}).LogDeprecations(log)

	if len(log.msgs) != 0 {
		t.Errorf("expected no warning when only the new field is set, got %v", log.msgs)
	}
}

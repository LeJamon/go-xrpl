package config

// warnLogger is the minimal logger surface used by deprecation notices.
// It is satisfied by xrpllog.Logger and by test fakes that only need to
// capture Warn calls.
type warnLogger interface {
	Warn(msg string, args ...any)
}

// LogDeprecations emits a warning for each deprecated knob the operator
// has explicitly set. When the deprecated knob has a replacement that is
// also set, the warning makes the precedence explicit (the new value
// always wins). No-op when c or log is nil so callers can invoke it
// unconditionally.
func (c *Config) LogDeprecations(log warnLogger) {
	if c == nil || log == nil {
		return
	}
	if c.WebsocketPingFrequency != 0 {
		if c.WebSocket.PingInterval > 0 {
			log.Warn(
				"websocket_ping_frequency is deprecated and is overridden by websocket.ping_interval",
				"deprecated_value", c.WebsocketPingFrequency,
				"effective_value", c.WebSocket.PingInterval,
			)
		} else {
			log.Warn(
				"websocket_ping_frequency is deprecated; configure websocket.ping_interval under the [websocket] section instead",
				"value", c.WebsocketPingFrequency,
			)
		}
	}
}

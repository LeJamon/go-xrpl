package config

import "testing"

func TestPortConfig_GRPCProtocolValidates(t *testing.T) {
	p := PortConfig{Port: 50051, IP: "127.0.0.1", Protocol: "grpc"}
	if err := p.Validate(); err != nil {
		t.Fatalf("grpc-only port should validate: %v", err)
	}
	if !p.HasGRPC() {
		t.Error("HasGRPC() = false for a grpc port")
	}
}

func TestPortConfig_GRPCRejectsMixedProtocols(t *testing.T) {
	for _, proto := range []string{"grpc,http", "grpc,ws", "grpc,peer"} {
		p := PortConfig{Port: 50051, IP: "127.0.0.1", Protocol: proto}
		if err := p.Validate(); err == nil {
			t.Errorf("protocol %q should be rejected when combined with grpc", proto)
		}
	}
}

func TestConfig_GetGRPCPortsIsolatesProtocol(t *testing.T) {
	c := &Config{Ports: map[string]PortConfig{
		"port_grpc": {Port: 50051, IP: "127.0.0.1", Protocol: "grpc"},
		"port_http": {Port: 5005, IP: "127.0.0.1", Protocol: "http"},
		"port_ws":   {Port: 6006, IP: "127.0.0.1", Protocol: "ws"},
	}}

	grpcPorts := c.GetGRPCPorts()
	if len(grpcPorts) != 1 {
		t.Fatalf("GetGRPCPorts() returned %d ports, want 1", len(grpcPorts))
	}
	if _, ok := grpcPorts["port_grpc"]; !ok {
		t.Error("GetGRPCPorts() missing port_grpc")
	}
	// A grpc port must not leak into the HTTP or WebSocket getters.
	if _, ok := c.GetHTTPPorts()["port_grpc"]; ok {
		t.Error("grpc port leaked into GetHTTPPorts()")
	}
	if _, ok := c.GetWebSocketPorts()["port_grpc"]; ok {
		t.Error("grpc port leaked into GetWebSocketPorts()")
	}
}

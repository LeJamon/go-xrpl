package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGRPCPort_Present(t *testing.T) {
	cfg := strings.Replace(completeTestConfig(),
		`[server]
ports = ["port_test"]`,
		`[server]
ports = ["port_test", "port_grpc"]

[port_grpc]
port = 50051
ip = "127.0.0.1"
protocol = "grpc"
secure_gateway = ["127.0.0.1"]`,
		1)

	config, err := writeAndLoad(t, cfg)
	require.NoError(t, err)

	name, port, ok := config.GRPCPort()
	require.True(t, ok, "expected GRPCPort to find the [port_grpc] section")
	assert.Equal(t, "port_grpc", name)
	assert.Equal(t, 50051, port.Port)
	assert.Equal(t, "127.0.0.1", port.IP)
	assert.True(t, port.HasGRPC())
	assert.Equal(t, "127.0.0.1:50051", port.BindAddress())

	gw, err := port.ParseSecureGatewayNets()
	require.NoError(t, err)
	assert.Len(t, gw, 1)
}

func TestGRPCPort_AbsentByDefault(t *testing.T) {
	config, err := writeAndLoad(t, completeTestConfig())
	require.NoError(t, err)

	_, _, ok := config.GRPCPort()
	assert.False(t, ok, "gRPC must be disabled when no [port_grpc] section is present")
}

func TestValidateProtocols_GRPC(t *testing.T) {
	t.Run("grpc alone is valid", func(t *testing.T) {
		p := &PortConfig{Port: 50051, IP: "127.0.0.1", Protocol: "grpc"}
		assert.NoError(t, p.Validate())
		assert.True(t, p.HasGRPC())
	})

	t.Run("grpc cannot combine with http", func(t *testing.T) {
		p := &PortConfig{Port: 50051, IP: "127.0.0.1", Protocol: "grpc,http"}
		assert.Error(t, p.Validate())
	})

	t.Run("grpc cannot combine with ws", func(t *testing.T) {
		p := &PortConfig{Port: 50051, IP: "127.0.0.1", Protocol: "grpc,ws"}
		assert.Error(t, p.Validate())
	})
}

func TestValidateGRPCLimit(t *testing.T) {
	for _, limit := range []int{0, 1, 65535} {
		p := &PortConfig{Port: 50051, IP: "127.0.0.1", Protocol: "grpc", Limit: limit}
		if err := p.Validate(); err != nil {
			t.Fatalf("limit %d rejected: %v", limit, err)
		}
	}
	for _, limit := range []int{-1, 65536} {
		p := &PortConfig{Port: 50051, IP: "127.0.0.1", Protocol: "grpc", Limit: limit}
		if err := p.Validate(); err == nil {
			t.Fatalf("limit %d accepted", limit)
		}
	}
}

func TestValidateProtocols_RejectsUnsupportedTLS(t *testing.T) {
	for _, protocol := range []string{"https", "wss"} {
		t.Run(protocol, func(t *testing.T) {
			p := &PortConfig{Port: 5005, IP: "127.0.0.1", Protocol: protocol}
			require.EqualError(t, p.Validate(),
				protocol+" protocol is unsupported because RPC listeners do not terminate TLS")
		})
	}
}

// TestGRPCPort_SecureGatewayExpandsWildcard confirms the rippled-faithful
// expansion of 0.0.0.0 into the IPv4+IPv6 wildcard nets. The grpc server
// wiring (internal/cli) rejects this unspecified entry at startup,
// mirroring rippled GRPCServer.cpp:361-368.
func TestGRPCPort_SecureGatewayExpandsWildcard(t *testing.T) {
	p := &PortConfig{
		Port:          50051,
		IP:            "127.0.0.1",
		Protocol:      "grpc",
		SecureGateway: []string{"0.0.0.0"},
	}
	nets, err := p.ParseSecureGatewayNets()
	require.NoError(t, err)
	require.NotEmpty(t, nets)
}

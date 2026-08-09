package node

import (
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/stretchr/testify/require"
)

func TestNewRPCServiceContainerFreezesCapabilities(t *testing.T) {
	pathMax := 0
	cfg := &config.Config{
		SigningSupport: true,
		PathSearchMax:  &pathMax,
		BetaRPCAPI:     1,
	}

	services := newRPCServiceGraphBuilder(nil, cfg)
	require.True(t, services.Capabilities.SigningEnabled)
	require.Zero(t, services.Capabilities.PathSearchMax)
	require.True(t, services.BetaRPCAPI)
	require.NotNil(t, services.ClientLoad)
	require.NotNil(t, services.RPCDiagnostics)

	cfg.SigningSupport = false
	pathMax = 9
	require.True(t, services.Capabilities.SigningEnabled)
	require.Zero(t, services.Capabilities.PathSearchMax)
}

func TestRPCCapabilitiesDefaults(t *testing.T) {
	services := newRPCServiceGraphBuilder(nil, &config.Config{})
	require.False(t, services.Capabilities.SigningEnabled)
	require.Equal(t, 3, services.Capabilities.PathSearchMax)
}

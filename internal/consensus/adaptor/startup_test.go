package adaptor

import (
	"testing"

	"github.com/LeJamon/goXRPLd/config"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/stretchr/testify/assert"
)

// TestOverlayOptionsFromConfig_PropagatesClusterNodes guards the one-line
// wiring in startup.go that hands [cluster_nodes] from rippled.cfg to
// the Overlay. Without it, the registry stays empty in production
// even when an operator configures cluster peers.
func TestOverlayOptionsFromConfig_PropagatesClusterNodes(t *testing.T) {
	appCfg := &config.Config{
		ClusterNodes: []string{
			"n9MDGCfimuyCmKXUAMcR12rv39PE6PY5YfFpNs75ZjtY3UWt31td primary",
			"nHU75pVH2Tak7adBWNP3H2CU3wcUtSgf45sKrd1uGyFyRcTozXNm",
		},
	}

	cfg := peermanagement.DefaultConfig()
	for _, opt := range OverlayOptionsFromConfig(appCfg) {
		opt(&cfg)
	}

	assert.Equal(t, appCfg.ClusterNodes, cfg.ClusterNodes)
}

func TestOverlayOptionsFromConfig_EmptyClusterNodesEmitsNoOption(t *testing.T) {
	appCfg := &config.Config{}

	cfg := peermanagement.DefaultConfig()
	for _, opt := range OverlayOptionsFromConfig(appCfg) {
		opt(&cfg)
	}

	assert.Empty(t, cfg.ClusterNodes)
}

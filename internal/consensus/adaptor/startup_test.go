package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
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

// TestOverlayOptionsFromConfig_ServerDomainAndPublicIP guards the
// wiring of server_domain and [overlay] public_ip into the handshake
// configuration.
func TestOverlayOptionsFromConfig_ServerDomainAndPublicIP(t *testing.T) {
	appCfg := &config.Config{
		ServerDomain: "example.com",
		Overlay:      config.OverlayConfig{PublicIP: "203.0.113.7"},
	}

	cfg := peermanagement.DefaultConfig()
	for _, opt := range OverlayOptionsFromConfig(appCfg) {
		opt(&cfg)
	}

	assert.Equal(t, "example.com", cfg.ServerDomain)
	assert.Equal(t, "203.0.113.7", cfg.PublicIP.String())
}

func TestOverlayOptionsFromConfig_UnsetDomainAndIPEmitNoOption(t *testing.T) {
	cfg := peermanagement.DefaultConfig()
	for _, opt := range OverlayOptionsFromConfig(&config.Config{}) {
		opt(&cfg)
	}

	assert.Empty(t, cfg.ServerDomain)
	assert.Nil(t, cfg.PublicIP)
}

// TestFeeVoteFromConfig guards the [voting] → FeeVoteStance wiring:
// configured values land on the stance, zero values pass through so
// New() substitutes the defaults.
func TestFeeVoteFromConfig(t *testing.T) {
	got := feeVoteFromConfig(config.VotingConfig{
		ReferenceFee:   12,
		AccountReserve: 25_000_000,
		OwnerReserve:   5_000_000,
	})
	assert.Equal(t, FeeVoteStance{
		BaseFee:          12,
		ReserveBase:      25_000_000,
		ReserveIncrement: 5_000_000,
	}, got)

	assert.Equal(t, FeeVoteStance{}, feeVoteFromConfig(config.VotingConfig{}))
}

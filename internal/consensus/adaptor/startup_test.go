package adaptor

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestOverlayOptionsFromConfig_BootstrapPrecedence(t *testing.T) {
	tests := []struct {
		name          string
		appCfg        *config.Config
		wantBootstrap []string
		wantFixed     []string
	}{
		{
			name: "configured ips",
			appCfg: &config.Config{
				IPs:      []string{"bootstrap.example", "2001:db8::1 51235", "zero.example:0"},
				IPsFixed: []string{"fixed.example 6000"},
			},
			wantBootstrap: []string{"bootstrap.example:2459", "[2001:db8::1]:51235", "zero.example:2459"},
			wantFixed:     []string{"fixed.example:6000"},
		},
		{
			name: "fixed fallback",
			appCfg: &config.Config{
				IPsFixed: []string{
					"fixed.example",
					"2001:db8::2",
					"empty.example:",
					"[2001:db8::3]:0",
					"space-zero.example 0",
					"[2001:db8::4",
				},
			},
			wantBootstrap: []string{
				"fixed.example:2459",
				"[2001:db8::2]:2459",
				"empty.example:2459",
				"[2001:db8::3]:2459",
				"space-zero.example:2459",
				"[2001:db8::4]:2459",
			},
			wantFixed: []string{
				"fixed.example:2459",
				"[2001:db8::2]:2459",
				"empty.example:2459",
				"[2001:db8::3]:2459",
				"space-zero.example:2459",
				"[2001:db8::4]:2459",
			},
		},
		{
			name:   "public defaults",
			appCfg: &config.Config{},
			wantBootstrap: []string{
				"r.ripple.com:51235",
				"sahyadri.isrdc.in:51235",
				"hubs.xrpkuwait.com:51235",
				"hub.xrpl-commons.org:51235",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := peermanagement.DefaultConfig()
			for _, opt := range OverlayOptionsFromConfig(tt.appCfg) {
				opt(&cfg)
			}

			assert.Equal(t, tt.wantBootstrap, cfg.BootstrapPeers)
			assert.Equal(t, tt.wantFixed, cfg.FixedPeers)
		})
	}
}

func TestOverlayOptionsFromConfig_FixedFallbackConnectsThroughBootstrap(t *testing.T) {
	source := startBootstrapFallbackOverlay(t)
	connected := make(chan struct{}, 1)
	source.SetPeerConnectCallback(func(peermanagement.PeerID) {
		select {
		case connected <- struct{}{}:
		default:
		}
	})

	opts := OverlayOptionsFromConfig(&config.Config{
		IPsFixed: []string{source.ListenAddr()},
	})
	opts = append(opts,
		peermanagement.WithListenAddr("127.0.0.1:0"),
		peermanagement.WithMaxPeers(2),
		peermanagement.WithMaxInbound(0),
		peermanagement.WithMaxOutbound(1),
		peermanagement.WithPrivateMode(false),
		peermanagement.WithFixedPeers(),
	)
	client := startBootstrapFallbackOverlay(t, opts...)

	select {
	case <-connected:
		require.Eventually(t, func() bool {
			return client.PeerCount() == 1
		}, time.Second, 10*time.Millisecond)
	case <-time.After(5 * time.Second):
		t.Fatal("fixed-only fallback was not selected through the bootstrap lane")
	}
}

func TestNormalizeAddressesPreservesInvalidForms(t *testing.T) {
	for _, input := range []string{
		"2001:db8::1]",
		"[not-an-ip]",
		"[2001:db8::1 51235",
	} {
		got := normalizeAddresses([]string{input})
		require.Equal(t, []string{input}, got)
		_, err := peermanagement.ParseEndpoint(got[0])
		require.Error(t, err)
	}
}

func TestNormalizeAddressesIPv6WhitespacePorts(t *testing.T) {
	assert.Equal(t,
		[]string{"[2001:db8::1]:51235", "[2001:db8::2]:51235"},
		normalizeAddresses([]string{"2001:db8::1 51235", "[2001:db8::2] 51235"}),
	)
}

func startBootstrapFallbackOverlay(t *testing.T, opts ...peermanagement.Option) *peermanagement.Overlay {
	t.Helper()
	base := []peermanagement.Option{
		peermanagement.WithListenAddr("127.0.0.1:0"),
		peermanagement.WithMaxPeers(2),
		peermanagement.WithMaxInbound(1),
		peermanagement.WithMaxOutbound(0),
		peermanagement.WithPrivateMode(true),
		peermanagement.WithCompression(false),
	}
	overlay, err := peermanagement.New(append(base, opts...)...)
	require.NoError(t, err)

	runDone := make(chan error, 1)
	go func() {
		runDone <- overlay.Run(context.Background())
	}()
	select {
	case <-overlay.ListenerReady():
	case err := <-runDone:
		t.Fatalf("overlay stopped before its listener became ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("overlay listener did not become ready")
	}
	t.Cleanup(func() {
		require.NoError(t, overlay.Stop())
		select {
		case <-runDone:
		case <-time.After(5 * time.Second):
			t.Error("overlay did not stop")
		}
	})
	return overlay
}

func TestFeeVoteFromConfig(t *testing.T) {
	got := feeVoteFromConfig(config.VotingConfig{
		ReferenceFee:      12,
		AccountReserve:    25_000_000,
		OwnerReserve:      5_000_000,
		ReferenceFeeSet:   true,
		AccountReserveSet: true,
		OwnerReserveSet:   true,
	}, nil)
	assert.Equal(t, FeeVoteStance{
		BaseFee:             12,
		ReserveBase:         25_000_000,
		ReserveIncrement:    5_000_000,
		BaseFeeSet:          true,
		ReserveBaseSet:      true,
		ReserveIncrementSet: true,
	}, got)

	assert.Equal(t, FeeVoteStance{}, feeVoteFromConfig(config.VotingConfig{}, nil))

	explicitZero := feeVoteFromConfig(config.VotingConfig{
		ReferenceFeeSet:   true,
		AccountReserveSet: true,
		OwnerReserveSet:   true,
	}, nil)
	assert.Zero(t, explicitZero.BaseFee)
	assert.Zero(t, explicitZero.ReserveBase)
	assert.Zero(t, explicitZero.ReserveIncrement)
	assert.True(t, explicitZero.BaseFeeSet)
	assert.True(t, explicitZero.ReserveBaseSet)
	assert.True(t, explicitZero.ReserveIncrementSet)

	mixed := New(Config{FeeVote: feeVoteFromConfig(config.VotingConfig{
		ReferenceFeeSet: true,
	}, nil)}).feeVote
	assert.Zero(t, mixed.BaseFee)
	assert.EqualValues(t, 10_000_000, mixed.ReserveBase)
	assert.EqualValues(t, 2_000_000, mixed.ReserveIncrement)

	feeDefault := 0
	overridden := feeVoteFromConfig(config.VotingConfig{
		ReferenceFee:    12,
		ReferenceFeeSet: true,
	}, &feeDefault)
	assert.Zero(t, overridden.BaseFee)
	assert.True(t, overridden.BaseFeeSet)
}

package adaptor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/LeJamon/goXRPLd/config"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/rcl"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
)

// Components holds all the consensus/networking components created by NewFromConfig.
type Components struct {
	Overlay     *peermanagement.Overlay
	Engine      consensus.Engine
	Adaptor     *Adaptor
	Router      *Router
	ModeManager *ModeManager

	// cancel functions for background goroutines
	overlayCancel context.CancelFunc
	routerCancel  context.CancelFunc
}

// Start launches all background goroutines (overlay, engine, router).
func (c *Components) Start() error {
	// Start overlay
	overlayCtx, overlayCancel := context.WithCancel(context.Background())
	c.overlayCancel = overlayCancel
	go c.Overlay.Run(overlayCtx) //nolint:errcheck

	// Start consensus engine
	if err := c.Engine.Start(context.Background()); err != nil {
		overlayCancel()
		return fmt.Errorf("start consensus engine: %w", err)
	}

	// Start message router
	routerCtx, routerCancel := context.WithCancel(context.Background())
	c.routerCancel = routerCancel
	go c.Router.Run(routerCtx)

	return nil
}

// Stop gracefully shuts down all components.
func (c *Components) Stop() {
	if c.routerCancel != nil {
		c.routerCancel()
	}
	if c.Engine != nil {
		_ = c.Engine.Stop()
	}
	// Drain any in-flight replay-delta acquisitions. Router is
	// already cancelled above so no new acquisitions can arrive; we
	// just need to clear the map so we don't leak state into a
	// subsequent Start. Log the count for observability.
	if c.Router != nil {
		if remaining := c.Router.StopReplayer(); remaining > 0 {
			slog.Info("replay-delta acquisitions drained at shutdown",
				"t", "Components.Stop", "in_flight_at_stop", remaining)
		}
	}
	if c.overlayCancel != nil {
		c.overlayCancel()
	}
	if c.Overlay != nil {
		_ = c.Overlay.Stop()
	}
}

// NewFromConfig creates and wires all consensus/networking components from the app config.
// Returns nil Components if the node is in standalone mode.
func NewFromConfig(
	appCfg *config.Config,
	ledgerSvc *service.Service,
) (*Components, error) {
	// Build overlay options from app config
	overlayOpts := OverlayOptionsFromConfig(appCfg)

	overlay, err := peermanagement.New(overlayOpts...)
	if err != nil {
		return nil, fmt.Errorf("create overlay: %w", err)
	}

	// Wire the read-side LedgerProvider so the overlay's ledger-sync
	// handler can answer mtREPLAY_DELTA_REQ and mtPROOF_PATH_REQ from
	// peers. Legacy mtGET_LEDGER is NOT routed through this provider
	// — the consensus router's handleGetLedger (router.go) answers
	// mtGET_LEDGER(LedgerInfoBase) requests directly from the ledger
	// service. peermanagement is forbidden from importing
	// internal/ledger, so the adapter installed here lets both layers
	// reach the ledger without breaking that layering boundary.
	overlay.LedgerSync().SetProvider(NewLedgerProvider(ledgerSvc))

	// Create validator identity (nil if not a validator)
	var identity *ValidatorIdentity
	if appCfg.ValidationSeed != "" {
		identity, err = NewValidatorIdentity(appCfg.ValidationSeed)
		if err != nil {
			return nil, fmt.Errorf("create validator identity: %w", err)
		}
	}

	// Load UNL from config
	validators, err := ParseValidatorKeys(appCfg)
	if err != nil {
		return nil, fmt.Errorf("parse validators: %w", err)
	}

	// Create the sender wrapping the overlay
	sender := NewOverlaySender(overlay)

	// Create the adaptor
	adaptor := New(Config{
		LedgerService: ledgerSvc,
		Sender:        sender,
		Identity:      identity,
		Validators:    validators,
	})

	// Create mode manager
	modeManager := NewModeManager(adaptor)

	// Create the RCL consensus engine
	engine := rcl.NewEngine(adaptor, rcl.DefaultConfig())

	// Create the router
	router := NewRouter(engine, adaptor, modeManager, overlay.Messages())

	// Plumb peer disconnect notifications back through the router so
	// per-peer state (peerStates for catch-up, peerLCLs for the
	// getNetworkLedger vote) is cleaned the instant a peer goes away.
	// Without this a disconnected peer's stale LCL keeps influencing
	// consensus convergence.
	overlay.SetPeerDisconnectCallback(router.HandlePeerDisconnect)

	// Wire operating mode into ledger service for server_info.
	// Matches rippled: report "proposing" when both in full operating mode
	// and actively proposing in consensus.
	ledgerSvc.SetServerStateFunc(func() string {
		opMode := adaptor.GetOperatingMode()
		if opMode == consensus.OpModeFull && engine.IsProposing() {
			return "proposing"
		}
		return opMode.String()
	})

	return &Components{
		Overlay:     overlay,
		Engine:      engine,
		Adaptor:     adaptor,
		Router:      router,
		ModeManager: modeManager,
	}, nil
}

// OverlayOptionsFromConfig maps app config fields to overlay options.
func OverlayOptionsFromConfig(appCfg *config.Config) []peermanagement.Option {
	var opts []peermanagement.Option

	// Network ID
	if networkID, err := appCfg.GetNetworkID(); err == nil {
		opts = append(opts, peermanagement.WithNetworkID(uint32(networkID)))
	}

	// Listen address from peer port config
	if _, peerPort, hasPeer := appCfg.GetPeerPort(); hasPeer {
		opts = append(opts, peermanagement.WithListenAddr(peerPort.GetBindAddress()))
	}

	// Bootstrap peers (convert "host port" → "host:port")
	if len(appCfg.IPs) > 0 {
		opts = append(opts, peermanagement.WithBootstrapPeers(normalizeAddresses(appCfg.IPs)...))
	}

	// Fixed peers (convert "host port" → "host:port")
	if len(appCfg.IPsFixed) > 0 {
		opts = append(opts, peermanagement.WithFixedPeers(normalizeAddresses(appCfg.IPsFixed)...))
	}

	// Max peers
	if appCfg.PeersMax > 0 {
		opts = append(opts, peermanagement.WithMaxPeers(appCfg.PeersMax))
	}

	// Private mode
	if appCfg.PeerPrivate > 0 {
		opts = append(opts, peermanagement.WithPrivateMode(true))
	}

	// Compression
	opts = append(opts, peermanagement.WithCompression(appCfg.Compression))

	// Ledger replay (Phase B server + Phase B client). The toml toggle
	// is a 0/1 int to match rippled's [ledger_replay] stanza semantics.
	opts = append(opts, peermanagement.WithLedgerReplay(appCfg.LedgerReplay != 0))

	return opts
}

// ParseValidatorKeys parses validator public keys from the config into NodeIDs.
func ParseValidatorKeys(appCfg *config.Config) ([]consensus.NodeID, error) {
	if len(appCfg.Validators.Validators) == 0 {
		return nil, nil
	}

	var validators []consensus.NodeID
	for _, key := range appCfg.Validators.Validators {
		nodeID, err := DecodeValidatorKey(key)
		if err != nil {
			return nil, fmt.Errorf("invalid validator key %q: %w", key, err)
		}
		validators = append(validators, nodeID)
	}
	return validators, nil
}

// normalizeAddresses converts rippled-style "host port" addresses to "host:port".
func normalizeAddresses(addrs []string) []string {
	out := make([]string, len(addrs))
	for i, addr := range addrs {
		if parts := strings.Fields(addr); len(parts) == 2 && !strings.Contains(addr, ":") {
			out[i] = parts[0] + ":" + parts[1]
		} else {
			out[i] = addr
		}
	}
	return out
}

package adaptor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/LeJamon/goXRPLd/config"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/archive"
	"github.com/LeJamon/goXRPLd/internal/consensus/rcl"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	"github.com/LeJamon/goXRPLd/internal/manifest"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// Components holds all the consensus/networking components created by NewFromConfig.
type Components struct {
	Overlay     *peermanagement.Overlay
	Engine      consensus.Engine
	Adaptor     *Adaptor
	Router      *Router
	ModeManager *ModeManager

	// Manifests is the validator-manifest cache shared by the router
	// (wire ingest), the consensus engine (ephemeral→master
	// translation), and the RPC layer (manifest method). Always
	// non-nil — starts empty and fills as peers gossip manifests.
	Manifests *manifest.Cache

	// Archive is the on-disk validation archive, when enabled.
	// Nil if disabled in config or if no relational DB is configured.
	// The engine owns the lifecycle (drain + Close on Stop), but it's
	// surfaced here so the read-path can plumb it into RPC services
	// without re-resolving from config.
	Archive *archive.Archive

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
//
// validationRepo is optional — pass nil to disable the on-disk validation
// archive. When non-nil and [validation_archive] is enabled in config,
// stale validations are persisted via a batched async writer.
func NewFromConfig(
	appCfg *config.Config,
	ledgerSvc *service.Service,
	validationRepo relationaldb.ValidationRepository,
) (*Components, error) {
	// Create validator identity first (nil if not a validator) so we can
	// pass its pubkey into the overlay for the self-target TMSquelch
	// filter (Task 4.2 / G3: without this a peer could silence our own
	// validator's traffic on the RelayFromValidator path).
	var identity *ValidatorIdentity
	if appCfg.ValidationSeed != "" {
		var err error
		identity, err = NewValidatorIdentity(appCfg.ValidationSeed)
		if err != nil {
			return nil, fmt.Errorf("create validator identity: %w", err)
		}
	}

	// Build overlay options from app config
	overlayOpts := OverlayOptionsFromConfig(appCfg)
	if identity != nil && len(identity.PublicKey) == 33 {
		overlayOpts = append(overlayOpts,
			peermanagement.WithLocalValidatorPubKey(identity.PublicKey))
	}

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

	// Load UNL from config
	validators, err := ParseValidatorKeys(appCfg)
	if err != nil {
		return nil, fmt.Errorf("parse validators: %w", err)
	}

	sender := NewOverlaySender(overlay)

	adaptor := New(Config{
		LedgerService: ledgerSvc,
		Sender:        sender,
		Identity:      identity,
		Validators:    validators,
	})

	modeManager := NewModeManager(adaptor)

	// Validator manifest cache. Shared across the engine (for
	// ephemeral→master translation in ValidationTracker), the router
	// (for ingesting + relaying TMManifests), and the RPC layer (for
	// the `manifest` method). Peers gossip manifests; until one
	// arrives the cache is empty and every ephemeral key round-trips
	// as itself.
	manifestCache := manifest.NewCache()

	engine := rcl.NewEngine(adaptor, rcl.DefaultConfig())

	// Translate ephemeral signing keys → master keys before quorum
	// arithmetic. Mirrors rippled RCLValidations.cpp:165-186's
	// calcNodeID(getTrustedKey(signingKey) ?? signingKey).
	engine.SetManifestResolver(func(nid consensus.NodeID) consensus.NodeID {
		return consensus.NodeID(manifestCache.GetMasterKey([33]byte(nid)))
	})

	// On-disk validation archive. Skipped when the relational DB is
	// unavailable or the operator has disabled the section in TOML —
	// either way the engine runs unchanged with the tracker in pure
	// in-memory mode. When enabled, ExpireOld in the fully-validated
	// callback streams pruned validations into the writer goroutine.
	var validationArchive *archive.Archive
	if validationRepo != nil && appCfg.ValidationArchive.Enabled {
		archCfg := appCfg.ValidationArchive.WithDefaults()
		validationArchive = archive.New(validationRepo, archive.Config{
			RetentionLedgers: archCfg.RetentionLedgers,
			BatchSize:        archCfg.BatchSize,
			FlushInterval:    time.Duration(archCfg.FlushIntervalMs) * time.Millisecond,
			DeleteBatch:      archCfg.DeleteBatch,
		}, slog.Default().With("component", "validation_archive"))
		engine.SetArchive(validationArchive)
		engine.SetInMemoryLedgers(archCfg.InMemoryLedgers)
	}

	engine.SetLedgerAncestryProvider(rcl.NewLedgerProvider(ledgerSvc))

	// Create the router
	router := NewRouter(engine, adaptor, modeManager, overlay.Messages())
	router.SetManifestCache(manifestCache, overlay)

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
		Manifests:   manifestCache,
		Archive:     validationArchive,
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

package adaptor

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/LeJamon/goXRPLd/config"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/archive"
	"github.com/LeJamon/goXRPLd/internal/consensus/rcl"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	"github.com/LeJamon/goXRPLd/internal/manifest"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	validatorlist "github.com/LeJamon/goXRPLd/internal/validator/list"
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

	// ValidatorList is the publisher-trust subsystem. Nil when no
	// validator_list_keys are configured. When non-nil, peer-gossiped
	// TMValidatorList frames feed it via the router and the configured
	// validator_list_sites URLs are polled by ValidatorListPoller.
	ValidatorList *validatorlist.Aggregator

	// ValidatorListPoller drives periodic HTTP fetches of configured
	// validator_list_sites and pipes the results into ValidatorList.
	// Nil iff ValidatorList is nil or no sites are configured.
	ValidatorListPoller *validatorlist.SitePoller

	// StaticTrustedMasterKeys carries the master pubkeys parsed out of
	// the operator's `[validators]` config stanza. Captured here so the
	// `validators` RPC can surface them as `local_static_keys` without
	// having to re-parse config or query a mutable adaptor field that
	// also includes publisher-derived entries.
	StaticTrustedMasterKeys [][33]byte

	// Archive is the on-disk validation archive, when enabled.
	// Nil if disabled in config or if no relational DB is configured.
	// The engine owns the lifecycle (drain + Close on Stop), but it's
	// surfaced here so the read-path can plumb it into RPC services
	// without re-resolving from config.
	Archive *archive.Archive

	// cancel functions for background goroutines
	overlayCancel          context.CancelFunc
	routerCancel           context.CancelFunc
	manifestPeriodicCancel context.CancelFunc
	sitePollerCancel       context.CancelFunc
}

// periodicManifestBroadcastInterval is how often Components.Start
// re-emits the cached aggregate TMManifests frame. Rippled has no
// equivalent loop: its 1-second OverlayImpl timer
// (OverlayImpl.cpp:84-114) handles endpoints / autoConnect / tx
// queue / idle-peer pruning but never emits manifests, and
// getManifestsMessage (OverlayImpl.cpp:1185) is invoked only from
// PeerImp::run after each handshake. That on-connect-only model
// leaves peers who join after our boot burst depending on an
// indirect relay; this loop closes the gap. Duplicate frames are
// wire-compatible — rippled returns Stale via applyManifest
// (Manifest.cpp:399).
const periodicManifestBroadcastInterval = 5 * time.Minute

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

	// Periodic re-emission. Cheap when there's nothing to broadcast:
	// the emission path short-circuits on an empty / unwired cache.
	periodicCtx, periodicCancel := context.WithCancel(context.Background())
	c.manifestPeriodicCancel = periodicCancel
	go c.runPeriodicManifestBroadcast(periodicCtx, periodicManifestBroadcastInterval)

	// Start the publisher-list HTTP poller. Cancellation propagates to
	// per-URL goroutines via the poller's own stop channel.
	if c.ValidatorListPoller != nil {
		pollerCtx, pollerCancel := context.WithCancel(context.Background())
		c.sitePollerCancel = pollerCancel
		c.ValidatorListPoller.Start(pollerCtx)
	}

	return nil
}

func (c *Components) runPeriodicManifestBroadcast(ctx context.Context, interval time.Duration) {
	if c.Router == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n := c.Router.BroadcastLocalManifest(); n > 0 {
				slog.Info("periodic local manifest broadcast",
					"t", "Components.runPeriodicManifestBroadcast", "peers", n)
			}
		}
	}
}

// Stop gracefully shuts down all components.
func (c *Components) Stop() {
	if c.sitePollerCancel != nil {
		c.sitePollerCancel()
	}
	if c.ValidatorListPoller != nil {
		c.ValidatorListPoller.Stop()
	}
	if c.manifestPeriodicCancel != nil {
		c.manifestPeriodicCancel()
	}
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
	identity, err := NewValidatorIdentityFromConfig(appCfg.ValidationSeed, appCfg.ValidatorToken)
	if err != nil {
		return nil, fmt.Errorf("create validator identity: %w", err)
	}

	// Build overlay options from app config
	overlayOpts := OverlayOptionsFromConfig(appCfg)
	if identity != nil {
		overlayOpts = append(overlayOpts,
			peermanagement.WithLocalValidatorPubKey(identity.SigningPubKey()))
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

	// Load UNL from config — retain both NodeID (for trust/quorum
	// maps) and master pubkey (for NegativeUNL voting; sfUNLModifyValidator
	// is the master pubkey, see NegativeUNLVote.cpp:118-120).
	validators, masterKeys, err := ParseValidatorKeysWithMaster(appCfg)
	if err != nil {
		return nil, fmt.Errorf("parse validators: %w", err)
	}

	sender := NewOverlaySender(overlay)

	adaptor := New(Config{
		LedgerService:       ledgerSvc,
		Sender:              sender,
		Identity:            identity,
		Validators:          validators,
		ValidatorMasterKeys: masterKeys,
	})

	modeManager := NewModeManager(adaptor)

	// Validator manifest cache. Shared across the engine (for
	// ephemeral→master translation in ValidationTracker), the router
	// (for ingesting + relaying TMManifests), and the RPC layer (for
	// the `manifest` method). Peers gossip manifests; until one
	// arrives the cache is empty and every ephemeral key round-trips
	// as itself.
	manifestCache := manifest.NewCache()

	// Seed the local validator's manifest into the cache when running
	// in token mode so the post-handshake TMManifests emission walks
	// every cached entry — local + aggregated remote — matching
	// rippled's OverlayImpl::getManifestsMessage which iterates
	// ValidatorManifests::for_each_manifest (Manifest.cpp:1184-1212).
	// In observer / seed-only mode there is nothing to seed and the
	// cache stays cold until peers gossip something.
	if identity != nil && identity.Manifest != nil {
		if d := manifestCache.ApplyManifest(identity.Manifest); d != manifest.Accepted {
			return nil, fmt.Errorf("seed local manifest into cache: disposition=%s", d)
		}
	} else {
		slog.Info("local validator manifest not configured; TMManifests emission limited to peer-gossiped entries",
			"t", "adaptor.NewFromConfig")
	}

	engine := rcl.NewEngine(adaptor, rcl.DefaultConfig())

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

	// Track engine ModeChangedEvent — Full gates startRoundLocked into
	// proposing, so wrongLedger needs to demote opMode.
	engine.Subscribe(modeManager)

	// Create the router
	router := NewRouter(engine, adaptor, modeManager, overlay.Messages())
	router.SetManifestCache(manifestCache, overlay)

	// Build the publisher-list aggregator when validator_list_keys are
	// configured. Lists are then ingested both via peer gossip
	// (TMValidatorList through the router) and via HTTP polling of
	// validator_list_sites. The aggregator pushes its recomputed
	// trusted UNL into adaptor.SetTrustedValidators on every change —
	// the same write path SIGHUP reload uses.
	publisherKeys, err := ParseValidatorListPublisherKeys(appCfg)
	if err != nil {
		return nil, fmt.Errorf("parse validator_list_keys: %w", err)
	}
	var vlAgg *validatorlist.Aggregator
	var vlPoller *validatorlist.SitePoller
	if len(publisherKeys) > 0 {
		pkSlice := make([]validatorlist.PublisherKey, len(publisherKeys))
		for i, k := range publisherKeys {
			pkSlice[i] = validatorlist.PublisherKey(k)
		}
		vlAgg, err = validatorlist.New(validatorlist.Config{
			PublisherKeys: pkSlice,
			SiteURIs:      append([]string(nil), appCfg.Validators.ValidatorListSites...),
			Threshold:     appCfg.Validators.GetValidatorListThreshold(),
			Manifests:     manifestCache,
			Logger:        slog.Default().With("component", "validator-list-aggregator"),
		})
		if err != nil {
			return nil, fmt.Errorf("validator-list aggregator: %w", err)
		}
		// On every publisher-trust recompute, merge the publisher's
		// validators with the operator's static [validators] stanza and
		// push the union into the adaptor. The static list never
		// disappears — operators may want to pin a few validators in
		// addition to whatever a publisher publishes.
		staticValidators := append([]consensus.NodeID(nil), validators...)
		staticMasterKeys := append([][33]byte(nil), masterKeys...)
		vlAgg.OnChange(func(publisherNodes []consensus.NodeID, publisherMasters [][33]byte) {
			merged, mergedMasters := mergeValidators(staticValidators, staticMasterKeys, publisherNodes, publisherMasters)
			adaptor.SetTrustedValidators(merged, mergedMasters)
		})
		router.SetValidatorListAggregator(vlAgg)
		if len(appCfg.Validators.ValidatorListSites) > 0 {
			vlPoller = validatorlist.NewSitePoller(
				append([]string(nil), appCfg.Validators.ValidatorListSites...),
				vlAgg,
				slog.Default().With("component", "validator-list-site-poller"),
			)
		}
	}

	// Plumb peer disconnect notifications back through the router so
	// per-peer state (peerStates for catch-up, peerLCLs for the
	// getNetworkLedger vote) is cleaned the instant a peer goes away.
	// Without this a disconnected peer's stale LCL keeps influencing
	// consensus convergence.
	overlay.SetPeerDisconnectCallback(router.HandlePeerDisconnect)

	// Emit cached validator manifests (local + aggregated remote) the
	// moment a peer's handshake completes. Mirrors rippled
	// PeerImp::doProtocolStart (PeerImp.cpp:851-886) which sends
	// OverlayImpl::getManifestsMessage — i.e. every entry in
	// ValidatorManifests — so the new peer can resolve our ephemeral
	// signing key (and any other validator's) back to its trusted
	// master before any validation it receives. Skip cases (cache
	// empty, no overlay) are absorbed inside SendLocalManifestTo.
	overlay.SetPeerConnectCallback(router.HandlePeerConnect)

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

	staticMastersCopy := append([][33]byte(nil), masterKeys...)
	return &Components{
		Overlay:                 overlay,
		Engine:                  engine,
		Adaptor:                 adaptor,
		Router:                  router,
		ModeManager:             modeManager,
		Manifests:               manifestCache,
		ValidatorList:           vlAgg,
		ValidatorListPoller:     vlPoller,
		StaticTrustedMasterKeys: staticMastersCopy,
		Archive:                 validationArchive,
	}, nil
}

// mergeValidators returns the deduplicated union of two
// (validators, masterKeys) pairs, sorted by master key for
// determinism. Used to combine the static [validators] config (held
// constant across publisher-list churn) with the publisher-derived
// trusted set on every aggregator OnChange callback.
//
// The two inputs are assumed already index-aligned (validators[i]
// derives from masterKeys[i] via consensus.CalcNodeID); the merged
// outputs preserve that invariant.
func mergeValidators(aIDs []consensus.NodeID, aMKs [][33]byte, bIDs []consensus.NodeID, bMKs [][33]byte) ([]consensus.NodeID, [][33]byte) {
	seen := make(map[[33]byte]consensus.NodeID, len(aIDs)+len(bIDs))
	for i, mk := range aMKs {
		if _, ok := seen[mk]; ok {
			continue
		}
		if i < len(aIDs) {
			seen[mk] = aIDs[i]
		} else {
			seen[mk] = consensus.CalcNodeID(mk)
		}
	}
	for i, mk := range bMKs {
		if _, ok := seen[mk]; ok {
			continue
		}
		if i < len(bIDs) {
			seen[mk] = bIDs[i]
		} else {
			seen[mk] = consensus.CalcNodeID(mk)
		}
	}
	masters := make([][33]byte, 0, len(seen))
	for mk := range seen {
		masters = append(masters, mk)
	}
	sort.Slice(masters, func(i, j int) bool {
		return string(masters[i][:]) < string(masters[j][:])
	})
	ids := make([]consensus.NodeID, len(masters))
	for i, mk := range masters {
		ids[i] = seen[mk]
	}
	return ids, masters
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

	// Cluster nodes from [cluster_nodes]. A malformed entry will fail
	// peermanagement.New, matching rippled's Application init which
	// aborts the node when Cluster::load returns false.
	if len(appCfg.ClusterNodes) > 0 {
		opts = append(opts, peermanagement.WithClusterNodes(appCfg.ClusterNodes...))
	}

	return opts
}

// ParseValidatorKeys parses validator public keys from the config into NodeIDs.
func ParseValidatorKeys(appCfg *config.Config) ([]consensus.NodeID, error) {
	validators, _, err := ParseValidatorKeysWithMaster(appCfg)
	return validators, err
}

// ParseValidatorListPublisherKeys decodes the `validator_list_keys`
// config field into 33-byte master public keys suitable for the
// publisher-trust aggregator. Each key is a hex-encoded 33-byte
// compressed pubkey (the form rippled and public list publishers like
// vl.ripple.com use). The leading byte is the key-type prefix —
// 0xED for ed25519 (the common case), 0x02/0x03 for secp256k1.
//
// Returns (nil, nil) when no publisher keys are configured. Returns an
// error if any key is malformed: this is a hard configuration failure
// rather than a silently-disabled publisher, since the operator
// explicitly opted in.
func ParseValidatorListPublisherKeys(appCfg *config.Config) ([][33]byte, error) {
	keys := appCfg.Validators.ValidatorListKeys
	if len(keys) == 0 {
		return nil, nil
	}
	out := make([][33]byte, 0, len(keys))
	for _, k := range keys {
		raw, err := hex.DecodeString(k)
		if err != nil {
			return nil, fmt.Errorf("validator_list_key %q: hex decode: %w", k, err)
		}
		if len(raw) != 33 {
			return nil, fmt.Errorf("validator_list_key %q: expected 33 bytes (66 hex chars), got %d", k, len(raw))
		}
		var pk [33]byte
		copy(pk[:], raw)
		out = append(out, pk)
	}
	return out, nil
}

// ParseValidatorKeysWithMaster parses validator public keys into both
// the NodeID set (for trust/quorum maps) and the 33-byte master pubkey
// list (index-aligned, for NegativeUNL voting). Returns (nil, nil, nil)
// when the [validators] stanza is empty.
func ParseValidatorKeysWithMaster(appCfg *config.Config) ([]consensus.NodeID, [][33]byte, error) {
	if len(appCfg.Validators.Validators) == 0 {
		return nil, nil, nil
	}

	validators := make([]consensus.NodeID, 0, len(appCfg.Validators.Validators))
	masters := make([][33]byte, 0, len(appCfg.Validators.Validators))
	for _, key := range appCfg.Validators.Validators {
		nodeID, master, err := DecodeValidatorKeyWithMaster(key)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid validator key %q: %w", key, err)
		}
		validators = append(validators, nodeID)
		masters = append(masters, master)
	}
	return validators, masters, nil
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

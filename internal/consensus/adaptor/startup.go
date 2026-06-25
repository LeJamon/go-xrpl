package adaptor

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/archive"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
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

	// staticMu guards staticValidators / staticMasterKeys. Both slices
	// hold the operator's most recent [validators] stanza — initially
	// from boot, refreshed on every SIGHUP via ReloadStaticValidators.
	// The publisher-trust OnChange callback reads under staticMu so a
	// SIGHUP removal can never be silently undone by the next publisher
	// event re-merging a boot-time snapshot.
	staticMu         sync.RWMutex
	staticValidators []consensus.NodeID
	staticMasterKeys [][33]byte

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
	vlTickCancel           context.CancelFunc
}

// validatorListTickInterval is how often Components.Start fires
// ValidatorList.Tick to promote future-dated rotations and re-emit
// OnChange. 30s keeps the wake-up cost low while still bounding the lag
// between a rotation's effective time and trusted-set update to half a
// minute in the worst case.
const validatorListTickInterval = 30 * time.Second

// periodicManifestBroadcastInterval is how often Components.Start
// re-emits the cached aggregate TMManifests frame. Manifests are
// otherwise only sent on-connect, which leaves peers who join after
// our boot burst depending on an indirect relay; this loop closes the
// gap. Duplicate frames are harmless — peers treat an already-seen
// manifest as stale and drop it.
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

	// Periodic ValidatorList.Tick promotes future-dated remaining
	// rotations and emits OnChange when the trusted union changes.
	// Without this, a rotation announced during a quiet period (no
	// peer gossip, no site polls) would only land when the next
	// ingest happens.
	if c.ValidatorList != nil {
		tickCtx, tickCancel := context.WithCancel(context.Background())
		c.vlTickCancel = tickCancel
		go c.runValidatorListTick(tickCtx, validatorListTickInterval)
	}

	return nil
}

func (c *Components) runValidatorListTick(ctx context.Context, interval time.Duration) {
	if c.ValidatorList == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.ValidatorList.Tick()
		}
	}
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
	if c.vlTickCancel != nil {
		c.vlTickCancel()
	}
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
// floor is the online-delete retention floor (a *shamapstore.Rotator in
// production). Pass nil when online_delete is off: acquisition and serving are
// then unrestricted, leaving the standalone / feature-disabled path unchanged.
func NewFromConfig(
	appCfg *config.Config,
	ledgerSvc *service.Service,
	validationRepo relationaldb.ValidationRepository,
	floor MinimumOnlineFloor,
) (*Components, error) {
	// Create validator identity first (nil if not a validator) so we can
	// pass its pubkey into the overlay for the self-target TMSquelch
	// filter: without this a peer could silence our own validator's
	// traffic on the RelayFromValidator path.
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
	ledgerProvider := NewLedgerProvider(ledgerSvc)
	ledgerProvider.SetMinimumOnlineFloor(floor)
	overlay.LedgerSync().SetProvider(ledgerProvider)

	// Load UNL from config — retain both NodeID (for trust/quorum
	// maps) and master pubkey (for NegativeUNL voting; sfUNLModifyValidator
	// is the master pubkey).
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
		// Source vote stances from the same amendment table the ledger service
		// resyncs from validated ledgers, so operator veto/upvote ([amendments]
		// config) drives consensus voting.
		AmendmentTable: ledgerSvc.AmendmentTable(),
		// The operator's [voting] stanza. Zero values mean unset —
		// New() substitutes the network defaults.
		FeeVote: feeVoteFromConfig(appCfg.Voting),
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
	// every cached entry — local + aggregated remote. In observer /
	// seed-only mode there is nothing to seed and the cache stays cold
	// until peers gossip something.
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

	engine.SetLedgerAncestryProvider(rcl.NewAncestryProvider(ledgerSvc))

	// Track engine ModeChangedEvent — Full gates startRoundLocked into
	// proposing, so wrongLedger needs to demote opMode.
	engine.Subscribe(modeManager)

	// Create the router. Consensus/acquisition frames arrive on
	// overlay.Messages(); transactions arrive on the separate
	// overlay.TxMessages() lane so a tx flood can't starve them.
	router := NewRouter(engine, adaptor, overlay.Messages())
	router.SetTxInbox(overlay.TxMessages())
	router.SetManifestCache(manifestCache, overlay)
	router.SetMinimumOnlineFloor(floor)

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
		router.SetValidatorListAggregator(vlAgg)
		// On-disk publisher-list cache: accepted lists are persisted
		// under <database_path>/validator-list/cache.<pubHex> after
		// every successful apply, and hydrated on cold start so the
		// trusted UNL is non-empty before the first poll cycle. Failed
		// cache I/O is logged but never blocks startup.
		if appCfg.DatabasePath != "" {
			cacheDir := filepath.Join(appCfg.DatabasePath, "validator-list")
			if err := vlAgg.SetCacheDir(cacheDir); err != nil {
				slog.Default().Warn("validator-list cache disabled",
					"dir", cacheDir, "error", err)
			} else if loaded := vlAgg.LoadCache(); loaded > 0 {
				slog.Default().Info("validator-list cache hydrated",
					"publishers", loaded)
			}
		}
		// Wire the broadcaster so both ingress paths (peer router +
		// HTTP poller) can push accepted lists out through the single
		// aggregator-owned BroadcastLatest entry point. The
		// router-bound constructor plumbs the shared message
		// suppression registry so SendList / SendCollection stamp the
		// (hash, peer) pair, preventing the same list from being echoed
		// back to a peer that already sent it.
		vlAgg.SetBroadcaster(router.NewValidatorListBroadcaster(overlay, sender))
		if len(appCfg.Validators.ValidatorListSites) > 0 {
			vlPoller, err = validatorlist.NewSitePoller(
				append([]string(nil), appCfg.Validators.ValidatorListSites...),
				vlAgg,
				slog.Default().With("component", "validator-list-site-poller"),
			)
			if err != nil {
				return nil, fmt.Errorf("validator-list site poller: %w", err)
			}
		}
	}

	// Plumb peer disconnect notifications back through the router so
	// per-peer state (peerStates for catch-up, peerLCLs for the
	// getNetworkLedger vote) is cleaned the instant a peer goes away.
	// Without this a disconnected peer's stale LCL keeps influencing
	// consensus convergence.
	overlay.SetPeerDisconnectCallback(router.HandlePeerDisconnect)

	// Emit cached validator manifests (local + aggregated remote) the
	// moment a peer's handshake completes, so the new peer can resolve
	// our ephemeral signing key (and any other validator's) back to its
	// trusted master before any validation it receives. Skip cases
	// (cache empty, no overlay) are absorbed inside SendLocalManifestTo.
	overlay.SetPeerConnectCallback(router.HandlePeerConnect)

	// Wire operating mode into ledger service for server_info. Report
	// "proposing" only when both in full operating mode and actively
	// proposing in consensus.
	ledgerSvc.SetServerStateFunc(func() string {
		opMode := adaptor.GetOperatingMode()
		if opMode == consensus.OpModeFull && engine.IsProposing() {
			return "proposing"
		}
		return opMode.String()
	})

	c := &Components{
		Overlay:             overlay,
		Engine:              engine,
		Adaptor:             adaptor,
		Router:              router,
		ModeManager:         modeManager,
		Manifests:           manifestCache,
		ValidatorList:       vlAgg,
		ValidatorListPoller: vlPoller,
		staticValidators:    append([]consensus.NodeID(nil), validators...),
		staticMasterKeys:    append([][33]byte(nil), masterKeys...),
		Archive:             validationArchive,
	}

	// Wire the publisher OnChange to merge against the live static set
	// (held under c.staticMu, refreshed by SIGHUP). Capturing the boot
	// values directly here would let a SIGHUP removal be silently undone
	// by the next publisher event.
	if vlAgg != nil {
		vlAgg.OnChange(func(publisherNodes []consensus.NodeID, publisherMasters [][33]byte) {
			staticV, staticM := c.snapshotStatic()
			merged, mergedMasters := mergeValidators(staticV, staticM, publisherNodes, publisherMasters)
			adaptor.SetTrustedValidators(merged, mergedMasters)
		})
	}

	return c, nil
}

// snapshotStatic returns deep copies of the current static validator
// set under staticMu. Both slices are safe for the caller to retain.
func (c *Components) snapshotStatic() ([]consensus.NodeID, [][33]byte) {
	c.staticMu.RLock()
	defer c.staticMu.RUnlock()
	v := append([]consensus.NodeID(nil), c.staticValidators...)
	m := append([][33]byte(nil), c.staticMasterKeys...)
	return v, m
}

// StaticTrustedMasterKeys returns a snapshot of the operator's static
// [validators] master keys. Reflects the latest ReloadStaticValidators
// call — i.e. SIGHUP-updated state, not just the boot-time stanza.
func (c *Components) StaticTrustedMasterKeys() [][33]byte {
	_, m := c.snapshotStatic()
	return m
}

// ReloadStaticValidators replaces the operator's static [validators]
// stanza atomically and re-pushes the resulting trusted set into the
// adaptor.
//
// When a publisher-trust aggregator is wired, the push is the union of
// the new static set and the aggregator's current trusted set. When no
// aggregator is wired the static set is pushed verbatim (single source
// of truth).
//
// SIGHUP-driven config reload calls this; publisher events do NOT —
// they go through the aggregator's OnChange callback wired in
// NewFromConfig.
func (c *Components) ReloadStaticValidators(validators []consensus.NodeID, masterKeys [][33]byte) {
	c.staticMu.Lock()
	c.staticValidators = append([]consensus.NodeID(nil), validators...)
	c.staticMasterKeys = append([][33]byte(nil), masterKeys...)
	c.staticMu.Unlock()

	if c.Adaptor == nil {
		return
	}
	if c.ValidatorList == nil {
		c.Adaptor.SetTrustedValidators(validators, masterKeys)
		return
	}
	pubNodes, pubMasters := c.ValidatorList.TrustedValidators()
	merged, mergedMasters := mergeValidators(validators, masterKeys, pubNodes, pubMasters)
	c.Adaptor.SetTrustedValidators(merged, mergedMasters)
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
	// peermanagement.New, aborting node startup rather than silently
	// dropping the cluster config.
	if len(appCfg.ClusterNodes) > 0 {
		opts = append(opts, peermanagement.WithClusterNodes(appCfg.ClusterNodes...))
	}

	// Max in-flight TMTransaction frames the overlay will hand to the
	// router before refusing new ones (jq_trans_overflow trigger), from
	// the [max_transactions] stanza. appCfg validation rejects values
	// outside [100, 1000] when set; zero falls through to peermanagement's
	// DefaultMaxTransactions (250).
	if appCfg.MaxTransactions > 0 {
		opts = append(opts, peermanagement.WithMaxTransactions(appCfg.MaxTransactions))
	}

	// Operator domain for the Server-Domain handshake header.
	if appCfg.ServerDomain != "" {
		opts = append(opts, peermanagement.WithServerDomain(appCfg.ServerDomain))
	}

	// [overlay] public_ip drives the Local-IP handshake header and the
	// Remote-IP consistency check. Validated as a parseable IP by
	// config validation.
	if appCfg.Overlay.PublicIP != "" {
		if ip := net.ParseIP(appCfg.Overlay.PublicIP); ip != nil {
			opts = append(opts, peermanagement.WithPublicIP(ip))
		}
	}

	return opts
}

// feeVoteFromConfig maps the operator's [voting] stanza onto the
// adaptor's fee-vote stance. Zero values pass through — New()
// substitutes the network defaults for unset fields.
func feeVoteFromConfig(v config.VotingConfig) FeeVoteStance {
	return FeeVoteStance{
		BaseFee:          uint64(v.ReferenceFee),
		ReserveBase:      uint32(v.AccountReserve),
		ReserveIncrement: uint32(v.OwnerReserve),
	}
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

// DecodeValidatorKeyWithMaster decodes a base58-encoded validator
// public key into both its 20-byte NodeID and the underlying 33-byte
// master pubkey. NegativeUNL voting needs the raw master because the
// UNLModify pseudo-tx carries the master pubkey on the wire
// (sfUNLModifyValidator is the master).
//
// The base58 form operators configure in `[validators]` carries a
// 33-byte master public key; calcNodeID (RIPEMD-160(SHA-256(masterPubKey)))
// keys the trust set identically to the inbound NodeID values the
// consensus router populates.
func DecodeValidatorKeyWithMaster(key string) (nodeID consensus.NodeID, master [33]byte, err error) {
	// Guard against panics in the base58 decoder for malformed input
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("invalid key encoding: %v", r)
		}
	}()

	decoded, decErr := addresscodec.DecodeNodePublicKey(key)
	if decErr != nil {
		return consensus.NodeID{}, [33]byte{}, fmt.Errorf("decode node public key: %w", decErr)
	}
	if len(decoded) != 33 {
		return consensus.NodeID{}, [33]byte{}, fmt.Errorf("unexpected key length: got %d, want 33", len(decoded))
	}
	copy(master[:], decoded)
	return consensus.CalcNodeID(master), master, nil
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

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

	// trustMergeMu serializes source snapshots through their adaptor update.
	// Publisher callbacks already hold the aggregator mutex, so reload must
	// merge from the cached publisher snapshot rather than call back into it.
	trustMergeMu                 sync.Mutex
	staticValidators             []consensus.NodeID
	staticMasterKeys             [][33]byte
	publisherValidators          []consensus.NodeID
	publisherMasterKeys          [][33]byte
	configuredPublisherKeys      [][33]byte
	configuredPublisherSites     []string
	configuredPublisherThreshold int

	// Archive is the on-disk validation archive, when enabled.
	// Nil if disabled in config or if no relational DB is configured.
	// The engine owns the lifecycle (drain + Close on Stop), but it's
	// surfaced here so the read-path can plumb it into RPC services
	// without re-resolving from config.
	Archive *archive.Archive

	// cancel functions for background goroutines
	overlayCancel    context.CancelFunc
	routerCancel     context.CancelFunc
	sitePollerCancel context.CancelFunc
	vlTickCancel     context.CancelFunc

	// routerDone is closed when the Router.Run loop returns, so Stop can join it
	// rather than fire-and-forgetting: an in-process restart cycle would
	// otherwise double-start Run loops, and a still-running loop could touch the
	// engine Stop has already torn down.
	routerDone chan struct{}
}

// validatorListTickInterval is how often Components.Start fires
// ValidatorList.Tick to promote future-dated rotations and re-emit
// OnChange. 30s keeps the wake-up cost low while still bounding the lag
// between a rotation's effective time and trusted-set update to half a
// minute in the worst case.
const validatorListTickInterval = 30 * time.Second

// Start launches all background goroutines (overlay, engine, router).
func (c *Components) Start() error {
	// Start overlay. Capture Run's error so a listener bind failure (a stale
	// process on the peer port, EACCES on a privileged port, a bad [port_peer]
	// address) is loud and fatal at boot instead of leaving the node running
	// deaf with no diagnostics. Run binds the listener before signalling
	// ListenerReady, so a bind failure returns before that signal fires; wait
	// for whichever happens first. A later (post-ready) exit is logged by the
	// goroutine and left buffered so it never blocks.
	overlayCtx, overlayCancel := context.WithCancel(context.Background())
	c.overlayCancel = overlayCancel
	overlayErr := make(chan error, 1)
	go func() {
		err := c.Overlay.Run(overlayCtx)
		if err != nil && overlayCtx.Err() == nil {
			slog.Error("overlay Run exited with error", "t", "consensus", "err", err)
		}
		overlayErr <- err
	}()

	select {
	case <-c.Overlay.ListenerReady():
		// Listener bound (or none configured) — boot can proceed.
	case err := <-overlayErr:
		overlayCancel()
		if err == nil {
			err = fmt.Errorf("overlay exited before the listener was ready")
		}
		return fmt.Errorf("start overlay: %w", err)
	}

	// Start consensus engine
	if err := c.Engine.Start(context.Background()); err != nil {
		overlayCancel()
		return fmt.Errorf("start consensus engine: %w", err)
	}

	// Start message router
	routerCtx, routerCancel := context.WithCancel(context.Background())
	c.routerCancel = routerCancel
	c.routerDone = make(chan struct{})
	go func() {
		defer close(c.routerDone)
		c.Router.Run(routerCtx)
	}()

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
	if c.routerCancel != nil {
		c.routerCancel()
	}
	// Join the router loop before tearing down the engine, so it can't be
	// mid-handleMessage touching an already-stopped engine. Bounded so a wedged
	// handler can't hang shutdown.
	if c.routerDone != nil {
		select {
		case <-c.routerDone:
		case <-time.After(5 * time.Second):
			slog.Warn("router loop did not exit within shutdown budget", "t", "Components.Stop")
		}
	}
	if c.Adaptor != nil {
		c.Adaptor.StopConsensusPhaseDispatcher()
	}
	if c.Engine != nil {
		_ = c.Engine.Stop()
	}
	// Drain both acquisition paths after the router loop has stopped. Components
	// are one-shot; a process restart constructs a fresh Router.
	if c.Router != nil {
		legacy, replay := c.Router.StopAcquisitions()
		if legacy > 0 || replay > 0 {
			slog.Info("ledger acquisitions drained at shutdown",
				"t", "Components.Stop",
				"legacy_in_flight_at_stop", legacy,
				"replay_in_flight_at_stop", replay)
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
	staticValidators, staticMasterKeys, err := ParseValidatorKeysWithMaster(appCfg)
	if err != nil {
		return nil, fmt.Errorf("parse validators: %w", err)
	}
	staticValidators, staticMasterKeys = excludeLocalValidator(staticValidators, staticMasterKeys, identity)
	validators, masterKeys := includeLocalValidator(staticValidators, staticMasterKeys, identity)

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
		Table: ledgerSvc.Table(),
		// The operator's [voting] stanza. Zero values mean unset —
		// New() substitutes the network defaults.
		FeeVote:          feeVoteFromConfig(appCfg.Voting),
		RelayValidations: ParseRelayValidationsPolicy(appCfg.RelayValidations),
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

	// Consensus, transaction, acquisition-reply, and manifest traffic arrive
	// on independent overlay lanes, so expensive verification or a flood on
	// one lane cannot starve the others.
	router := NewRouter(engine, adaptor, overlay.ConsensusMessages())
	router.SetConsensusControlInbox(overlay.ConsensusControlMessages())
	router.SetServiceInbox(overlay.Messages())
	router.SetTxInbox(overlay.TxMessages())
	router.SetAcqInbox(overlay.LedgerDataMessages())
	router.SetManifestInbox(overlay.ManifestMessages())
	router.SetManifestCache(manifestCache, overlay)
	router.SetManifestAdmission(func(master [33]byte) bool {
		nodeID := consensus.CalcNodeID(master)
		return adaptor.IsTrusted(nodeID) || adaptor.IsListed(nodeID)
	})
	router.setPeerSessionView(overlay)
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
			PublisherKeys:        pkSlice,
			SiteURIs:             append([]string(nil), appCfg.Validators.ValidatorListSites...),
			Threshold:            appCfg.Validators.EffectiveListThreshold(),
			StaticValidatorCount: len(masterKeys),
			Manifests:            manifestCache,
			Logger:               slog.Default().With("component", "validator-list-aggregator"),
		})
		if err != nil {
			return nil, fmt.Errorf("validator-list aggregator: %w", err)
		}
		router.SetValidatorListAggregator(vlAgg)
		adaptor.SetUNLBlockedFunc(vlAgg.IsUNLBlocked)
		adaptor.SetQuorumUnavailableFunc(vlAgg.IsQuorumUnavailable)
		adaptor.SetUNLRefreshFunc(vlAgg.Tick)
		// Listed-but-untrusted signers (published below the trust
		// threshold) get their validations stored by the engine so a later
		// trust change promotes what was already seen.
		adaptor.SetListedLookup(vlAgg.IsListed)
		// On-disk publisher-list cache: accepted lists are persisted
		// under <database_path>/validator-list/cache.<pubHex> after
		// every successful apply, and hydrated on cold start so the
		// trusted UNL is non-empty before the first poll cycle. Failed
		// cache I/O is logged but never blocks startup.
		if dataDir := appCfg.LocalStateDir(); dataDir != "" {
			cacheDir := filepath.Join(dataDir, "validator-list")
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

	// Queue peer disconnect notifications for router-owned cleanup so
	// per-peer state (peerStates for catch-up, peerLCLs for the
	// getNetworkLedger vote) is cleaned without blocking the overlay event loop.
	// Without this a disconnected peer's stale LCL keeps influencing
	// consensus convergence.
	overlay.SetPeerDisconnectCallback(router.queuePeerDisconnect)

	// Emit cached validator manifests (local + aggregated remote) the
	// moment a peer's handshake completes, so the new peer can resolve
	// our ephemeral signing key (and any other validator's) back to its
	// trusted master before any validation it receives. Skip cases
	// (cache empty, no overlay) are absorbed inside SendLocalManifestTo.
	overlay.SetPeerConnectCallback(router.HandlePeerConnect)

	// Surface the consensus role in server_info's server_state so an external
	// observer can tell a participating validator from a passive full node.
	// The RPC presentation layer restricts the "proposing"/"validating"
	// aliases to admin callers, matching rippled.
	ledgerSvc.SetServerStateFunc(func() string {
		return consensusServerState(adaptor.GetOperatingMode(), engine.Mode(), engine.IsValidating())
	})

	c := &Components{
		Overlay:                      overlay,
		Engine:                       engine,
		Adaptor:                      adaptor,
		Router:                       router,
		ModeManager:                  modeManager,
		Manifests:                    manifestCache,
		ValidatorList:                vlAgg,
		ValidatorListPoller:          vlPoller,
		staticValidators:             append([]consensus.NodeID(nil), staticValidators...),
		staticMasterKeys:             append([][33]byte(nil), staticMasterKeys...),
		configuredPublisherKeys:      append([][33]byte(nil), publisherKeys...),
		configuredPublisherSites:     append([]string(nil), appCfg.Validators.ValidatorListSites...),
		configuredPublisherThreshold: appCfg.Validators.EffectiveListThreshold(),
		Archive:                      validationArchive,
	}

	// Capturing the boot values directly here would let a SIGHUP removal
	// be silently undone by the next publisher event.
	wireValidatorListTrust(c)

	return c, nil
}

func wireValidatorListTrust(c *Components) {
	if c.ValidatorList == nil {
		return
	}
	c.ValidatorList.OnChange(func(publisherNodes []consensus.NodeID, publisherMasters [][33]byte) {
		c.updatePublisherTrust(publisherNodes, publisherMasters)
	})
	c.ValidatorList.Tick()
	publisherNodes, publisherMasters := c.ValidatorList.TrustedValidators()
	c.updatePublisherTrust(publisherNodes, publisherMasters)
}

// consensusServerState maps the operating mode and consensus role to the
// server_state string, mirroring rippled's strOperatingMode precedence: a FULL
// node that is not on the wrong ledger reports "proposing" when proposing its
// position, else "validating" when issuing validations; otherwise it reports
// the plain operating-mode name.
func consensusServerState(opMode consensus.OperatingMode, mode consensus.Mode, validating bool) string {
	if opMode == consensus.OpModeFull && mode != consensus.ModeWrongLedger {
		switch {
		case mode == consensus.ModeProposing:
			return "proposing"
		case validating:
			return "validating"
		}
	}
	return opMode.String()
}

func (c *Components) snapshotStatic() ([]consensus.NodeID, [][33]byte) {
	c.trustMergeMu.Lock()
	defer c.trustMergeMu.Unlock()
	v := append([]consensus.NodeID(nil), c.staticValidators...)
	m := append([][33]byte(nil), c.staticMasterKeys...)
	return v, m
}

func (c *Components) snapshotEffectiveStatic() ([]consensus.NodeID, [][33]byte) {
	validators, masterKeys := c.snapshotStatic()
	if c.Adaptor == nil {
		return validators, masterKeys
	}
	return includeLocalValidator(validators, masterKeys, c.Adaptor.identity)
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
// the new static set and the latest publisher callback snapshot. When no
// aggregator is wired the static set is pushed verbatim.
//
// SIGHUP-driven config reload calls this; publisher events do NOT —
// they go through the aggregator's OnChange callback wired in
// NewFromConfig.
func (c *Components) ReloadStaticValidators(validators []consensus.NodeID, masterKeys [][33]byte) {
	var identity *ValidatorIdentity
	if c.Adaptor != nil {
		identity = c.Adaptor.identity
	}
	validators, masterKeys = excludeLocalValidator(validators, masterKeys, identity)
	_, effectiveMasterKeys := includeLocalValidator(validators, masterKeys, identity)
	c.trustMergeMu.Lock()
	c.staticValidators = append([]consensus.NodeID(nil), validators...)
	c.staticMasterKeys = append([][33]byte(nil), masterKeys...)
	c.applyMergedTrustLocked()
	c.trustMergeMu.Unlock()

	if c.ValidatorList != nil {
		c.ValidatorList.SetStaticValidatorCount(len(effectiveMasterKeys))
	}
}

func (c *Components) updatePublisherTrust(validators []consensus.NodeID, masterKeys [][33]byte) {
	c.trustMergeMu.Lock()
	defer c.trustMergeMu.Unlock()

	c.publisherValidators = append([]consensus.NodeID(nil), validators...)
	c.publisherMasterKeys = append([][33]byte(nil), masterKeys...)
	c.applyMergedTrustLocked()
}

func (c *Components) applyMergedTrustLocked() {
	if c.Adaptor == nil {
		return
	}
	effectiveValidators, effectiveMasterKeys := includeLocalValidator(
		c.staticValidators,
		c.staticMasterKeys,
		c.Adaptor.identity,
	)
	merged, mergedMasters := mergeValidators(
		effectiveValidators,
		effectiveMasterKeys,
		c.publisherValidators,
		c.publisherMasterKeys,
	)
	c.Adaptor.SetTrustedValidators(merged, mergedMasters)
}

func (c *Components) ValidateValidatorReload(publisherKeys [][33]byte, publisherSites []string, publisherThreshold, staticValidatorCount int) error {
	if !publisherKeyMultisetsEqual(c.configuredPublisherKeys, publisherKeys) {
		return fmt.Errorf("validator_list_keys changes require a node restart")
	}
	if len(publisherKeys) > 0 &&
		(!stringMultisetsEqual(c.configuredPublisherSites, publisherSites) || c.configuredPublisherThreshold != publisherThreshold) {
		return fmt.Errorf("validator list site or threshold changes require a node restart")
	}
	if staticValidatorCount == 0 && len(c.configuredPublisherKeys) == 0 {
		return fmt.Errorf("trusted validator configuration cannot be empty outside standalone mode")
	}
	return nil
}

func stringMultisetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, value := range a {
		counts[value]++
	}
	for _, value := range b {
		if counts[value] == 0 {
			return false
		}
		counts[value]--
	}
	return true
}

func publisherKeyMultisetsEqual(a, b [][33]byte) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[[33]byte]int, len(a))
	for _, key := range a {
		counts[key]++
	}
	for _, key := range b {
		if counts[key] == 0 {
			return false
		}
		counts[key]--
	}
	return true
}

func includeLocalValidator(validators []consensus.NodeID, masterKeys [][33]byte, identity *ValidatorIdentity) ([]consensus.NodeID, [][33]byte) {
	if identity == nil {
		return validators, masterKeys
	}
	return mergeValidators(
		validators,
		masterKeys,
		[]consensus.NodeID{identity.NodeID},
		[][33]byte{identity.MasterKey},
	)
}

func excludeLocalValidator(validators []consensus.NodeID, masterKeys [][33]byte, identity *ValidatorIdentity) ([]consensus.NodeID, [][33]byte) {
	if identity == nil {
		return validators, masterKeys
	}
	n := min(len(validators), len(masterKeys))
	filteredValidators := make([]consensus.NodeID, 0, n)
	filteredMasterKeys := make([][33]byte, 0, n)
	for i := range n {
		if masterKeys[i] == identity.MasterKey {
			continue
		}
		filteredValidators = append(filteredValidators, validators[i])
		filteredMasterKeys = append(filteredMasterKeys, masterKeys[i])
	}
	return filteredValidators, filteredMasterKeys
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
	if networkID, err := appCfg.ResolvedNetworkID(); err == nil {
		opts = append(opts, peermanagement.WithNetworkID(uint32(networkID)))
	}

	// Listen address from peer port config
	_, peerPort, hasPeerPort := appCfg.PeerPort()
	if hasPeerPort {
		opts = append(opts, peermanagement.WithListenAddr(peerPort.BindAddress()))
	} else {
		opts = append(opts, peermanagement.WithListenAddr(""))
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
	maxPeers, maxInbound, maxOutbound := peerLimits(appCfg.PeersMax, hasPeerPort && appCfg.PeerPrivate == 0)
	opts = append(opts,
		peermanagement.WithMaxPeers(maxPeers),
		peermanagement.WithMaxInbound(maxInbound),
		peermanagement.WithMaxOutbound(maxOutbound),
		peermanagement.WithIPLimit(appCfg.Overlay.IPLimit),
	)

	// Private mode
	if appCfg.PeerPrivate > 0 {
		opts = append(opts, peermanagement.WithPrivateMode(true))
	}
	if dataDir := appCfg.LocalStateDir(); dataDir != "" {
		opts = append(opts, peermanagement.WithDataDir(filepath.Join(dataDir, "peers")))
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

	// [overlay] verify_endpoints toggles TMEndpoints gossip validation.
	// Absent leaves the default on; an explicit 0/1 overrides it.
	if appCfg.Overlay.VerifyEndpoints != nil {
		opts = append(opts, peermanagement.WithVerifyEndpoints(*appCfg.Overlay.VerifyEndpoints != 0))
	}

	return opts
}

const (
	defaultMaxPeers     = 21
	minOutboundPeers    = 10
	outboundPeerPercent = 15
)

func peerLimits(maxPeers int, wantIncoming bool) (int, int, int) {
	if maxPeers == 0 {
		maxPeers = defaultMaxPeers
	}
	maxPeers = max(maxPeers, minOutboundPeers)
	if !wantIncoming {
		return maxPeers, 0, maxPeers
	}

	maxOutbound := max((maxPeers*outboundPeerPercent+50)/100, minOutboundPeers)
	return maxPeers, maxPeers - maxOutbound, maxOutbound
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

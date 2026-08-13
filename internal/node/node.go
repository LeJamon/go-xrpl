package node

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/adaptor"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/LeJamon/go-xrpl/storage/relationaldb/postgres"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/LeJamon/go-xrpl/version"
)

type minimumOnlineFloorFunc func() uint32

func (f minimumOnlineFloorFunc) MinimumOnline() uint32 { return f() }

func effectivePeerFetchDepth(fetchDepth uint32, onlineDelete int) uint32 {
	if onlineDelete > 0 && uint64(onlineDelete) < uint64(fetchDepth) {
		return uint32(onlineDelete)
	}
	return fetchDepth
}

func serverInfoConfigSnapshot(cfg *config.Config) types.ServerInfoConfigSnapshot {
	if cfg == nil {
		return types.ServerInfoConfigSnapshot{}
	}

	nodeSize := cfg.NodeSize
	if nodeSize == "" {
		nodeSize = "medium"
	}

	names := append([]string(nil), cfg.Server.Ports...)
	if len(names) == 0 {
		names = make([]string, 0, len(cfg.Ports))
		for name := range cfg.Ports {
			names = append(names, name)
		}
		sort.Strings(names)
	}

	ports := make([]types.ServerInfoPortSnapshot, 0, len(names))
	for _, name := range names {
		port, ok := cfg.Ports[name]
		if !ok {
			continue
		}
		admin := port.AdminUser != "" || port.AdminPassword != ""
		if adminNets, err := port.ParseAdminNets(); err != nil || len(adminNets) != 0 {
			admin = true
		}
		ports = append(ports, types.ServerInfoPortSnapshot{
			Port:     port.Port,
			Protocol: port.Protocol,
			Admin:    admin,
		})
	}

	return types.ServerInfoConfigSnapshot{
		Ports:        ports,
		ServerDomain: cfg.ServerDomain,
		NodeSize:     nodeSize,
		GitHash:      goBuildRevision(),
	}
}

func rpcCapabilities(cfg *config.Config) types.RPCCapabilities {
	if cfg == nil {
		return types.RPCCapabilities{}
	}
	return types.RPCCapabilities{
		SigningEnabled: cfg.SigningSupport,
		PathSearchMax:  cfg.ResolvedPathSearchMax(),
	}
}

func newRPCServiceGraphBuilder(ledger types.LedgerService, cfg *config.Config) *types.ServiceGraphBuilder {
	services := types.NewServiceGraphBuilder(ledger)
	services.ClientLoad = types.NewClientLoadShedder()
	services.RPCDiagnostics = rpc.NewRPCDiagnostics()
	services.ServerInfoConfig = serverInfoConfigSnapshot(cfg)
	services.Capabilities = rpcCapabilities(cfg)
	if cfg != nil {
		services.BetaRPCAPI = cfg.BetaRPCAPI != 0
	}
	return services
}

func goBuildRevision() string {
	info, ok := debug.ReadBuildInfo()
	return resolveBuildRevision(info, ok, version.Version)
}

func resolveBuildRevision(info *debug.BuildInfo, haveBuildInfo bool, fallback string) string {
	if haveBuildInfo && info != nil {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				return setting.Value
			}
		}
	}
	if isFullGitHash(fallback) {
		return fallback
	}
	return ""
}

func isFullGitHash(value string) bool {
	if len(value) != 40 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// Run assembles and starts every node subsystem from the parsed config, then
// blocks until a terminating signal or fatal error. It is the composition root
// extracted from the CLI so flag parsing and node wiring stay separable.
func Run(ctx context.Context, appConfig *config.Config, configPath string, standalone bool, startup service.StartupConfig, rootLogger, serverLog xrpllog.Logger) error {
	return run(ctx, appConfig, configPath, standalone, startup, rootLogger, serverLog, RunOptions{})
}

// RunOptions supplies process-owned lifecycle events to the node runtime.
type RunOptions struct {
	Ready    func()
	Stopping func()
	Reload   <-chan os.Signal
}

func RunWithOptions(
	ctx context.Context,
	appConfig *config.Config,
	configPath string,
	standalone bool,
	startup service.StartupConfig,
	rootLogger, serverLog xrpllog.Logger,
	options RunOptions,
) error {
	return run(ctx, appConfig, configPath, standalone, startup, rootLogger, serverLog, options)
}

// RunWithReady runs the node and calls ready after every configured service has
// started successfully. The callback may start callers' pre-bound endpoints.
func RunWithReady(
	ctx context.Context,
	appConfig *config.Config,
	configPath string,
	standalone bool,
	startup service.StartupConfig,
	rootLogger, serverLog xrpllog.Logger,
	ready func(),
) error {
	return RunWithOptions(ctx, appConfig, configPath, standalone, startup, rootLogger, serverLog, RunOptions{Ready: ready})
}

func run(
	ctx context.Context,
	appConfig *config.Config,
	configPath string,
	standalone bool,
	startup service.StartupConfig,
	rootLogger, serverLog xrpllog.Logger,
	options RunOptions,
) error {
	return newNodeRuntime(
		ctx,
		appConfig,
		configPath,
		standalone,
		startup,
		rootLogger,
		serverLog,
		options,
	).run()
}

func configuredLedgerLoadFees(appConfig *config.Config) drops.Fees {
	standard := genesis.StandardFees()
	fees := drops.Fees{
		Base:      standard.BaseFee,
		Reserve:   standard.ReserveBase,
		Increment: standard.ReserveIncrement,
	}
	if appConfig.Voting.ReferenceFeeSet || appConfig.Voting.ReferenceFee > 0 {
		fees.Base = drops.XRPAmount(appConfig.Voting.ReferenceFee)
	}
	if appConfig.Voting.AccountReserveSet || appConfig.Voting.AccountReserve > 0 {
		fees.Reserve = drops.XRPAmount(appConfig.Voting.AccountReserve)
	}
	if appConfig.Voting.OwnerReserveSet || appConfig.Voting.OwnerReserve > 0 {
		fees.Increment = drops.XRPAmount(appConfig.Voting.OwnerReserve)
	}
	if appConfig.FeeDefault != nil {
		fees.Base = drops.XRPAmount(*appConfig.FeeDefault)
	}
	return fees
}

func validateTrustedValidatorConfig(appConfig *config.Config, standalone bool) error {
	if standalone || len(appConfig.Validators.Validators) > 0 || len(appConfig.Validators.ValidatorListKeys) > 0 {
		return nil
	}
	return errors.New("trusted validator configuration is empty: configure validators or validator_list_keys, or use --standalone")
}

// waitForShutdown blocks until context cancellation, an RPC stop, or a listener
// or consensus-component failure. Validator reloads run on a separate serialized
// worker so they cannot prevent a terminating event from being observed.

func waitForShutdown(
	ctx context.Context,
	log xrpllog.Logger,
	reloadCh <-chan os.Signal,
	shutdownCh chan struct{},
	listenerErrCh chan error,
	componentErrCh <-chan error,
	serviceErrCh <-chan error,
	consensusComponents *adaptor.Components,
	configPath string,
) error {
	reloadCtx, cancelReload := context.WithCancel(ctx)
	reloadDone := make(chan struct{})
	var reloader staticValidatorReloader
	if consensusComponents != nil {
		reloader = consensusComponents
	}
	go func() {
		defer close(reloadDone)
		runValidatorReloadLoop(
			reloadCtx,
			log,
			reloader,
			configPath,
			reloadCh,
			config.LoadConfig,
		)
	}()
	defer func() {
		cancelReload()
		<-reloadDone
	}()

	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-shutdownCh:
			return nil
		case err := <-listenerErrCh:
			log.Error("Listener failed — initiating shutdown", "err", err)
			return err
		case err, ok := <-componentErrCh:
			if !ok {
				componentErrCh = nil
				continue
			}
			if err == nil {
				err = errors.New("consensus component stopped unexpectedly")
			}
			log.Error("Consensus component failed — initiating shutdown", "err", err)
			return err
		case err, ok := <-serviceErrCh:
			if !ok {
				serviceErrCh = nil
				continue
			}
			if err == nil {
				err = errors.New("ledger service stopped unexpectedly")
			}
			log.Error("Ledger service failed — initiating shutdown", "err", err)
			return err
		}
	}
}

// setupStorage initializes the node store (pebble or in-memory) and the
// optional relational DB (PostgreSQL or SQLite, used for transaction indexing)
// from config. An explicitly configured backend must open successfully; an
// empty database path intentionally leaves relational storage disabled.
func setupStorage(
	ctx context.Context,
	cfg *config.Config,
	log xrpllog.Logger,
) (db nodestore.Database, repoManager relationaldb.RepositoryManager, err error) {
	if cause := context.Cause(ctx); cause != nil {
		return nil, nil, cause
	}
	defer func() {
		if err == nil {
			return
		}
		if repoManager != nil {
			err = errors.Join(err, repoManager.Close())
			repoManager = nil
		}
		if db != nil {
			err = errors.Join(err, db.Close())
			db = nil
		}
	}()

	nodestorePath := cfg.NodeDB.Path
	if nodestorePath != "" {
		cacheSize, cacheTTL := nodeStoreCacheParams(cfg.NodeDB, cfg.NodeSize)
		databaseConfig := nodestore.DefaultDatabaseConfig()
		if cacheSize > 0 {
			databaseConfig.PositiveCache.MaxEntries = cacheSize
			databaseConfig.PositiveCache.TTL = cacheTTL
		} else {
			databaseConfig.PositiveCache = nodestore.CacheConfig{}
		}
		pebbleOptions, err := pebbleStoreOptions(cfg.NodeDB)
		if err != nil {
			return nil, nil, err
		}
		hasRotationState, err := kvpebble.HasRotationState(nodestorePath)
		if err != nil {
			return nil, nil, fmt.Errorf("inspect rotating storage state: %w", err)
		}
		if cfg.NodeDB.IsOnlineDeleteEnabled() || hasRotationState {
			store, err := kvpebble.NewRotating(nodestorePath, pebbleOptions)
			if err != nil {
				return nil, nil, fmt.Errorf("rotating storage backend: %w", err)
			}
			db, err = nodestore.NewRotatingKVDatabase(store, databaseConfig)
			if err != nil {
				return nil, nil, errors.Join(err, store.Close())
			}
		} else {
			store, err := kvpebble.New(nodestorePath, pebbleOptions)
			if err != nil {
				return nil, nil, fmt.Errorf("storage backend: %w", err)
			}
			db, err = nodestore.NewKVDatabase(store, databaseConfig)
			if err != nil {
				return nil, nil, errors.Join(err, store.Close())
			}
		}
		log.Info("Storage initialized", "backend", "pebble", "path", nodestorePath,
			"cache_size", cacheSize, "cache_age", cacheTTL,
			"cache_mb", pebbleOptions.BlockCacheBytes/(1<<20),
			"open_files", pebbleOptions.MaxOpenFiles)
	} else {
		log.Info("Storage initialized", "backend", "in-memory")
	}
	if cause := context.Cause(ctx); cause != nil {
		return db, nil, cause
	}

	dbPath := cfg.DatabasePath
	if strings.HasPrefix(dbPath, "postgres://") || strings.HasPrefix(dbPath, "postgresql://") {
		pgConfig := relationaldb.NewConfig()
		pgConfig.ConnectionString = dbPath

		var err error
		repoManager, err = postgres.NewRepositoryManager(ctx, pgConfig)
		if err != nil {
			return db, nil, fmt.Errorf("initialize PostgreSQL database: %w", err)
		}
		log.Info("PostgreSQL connected", "purpose", "transaction indexing")
	} else if dbPath != "" {
		// Default: auto-create SQLite databases at the given directory
		// path, applying the operator's [sqlite] tuning.
		journalMode, synchronous, tempStore := cfg.SQLite.EffectiveSettings()
		var err error
		repoManager, err = sqlitedb.NewRepositoryManager(ctx, dbPath, sqlitedb.Settings{
			JournalMode:      journalMode,
			Synchronous:      synchronous,
			TempStore:        tempStore,
			PageSize:         cfg.SQLite.PageSize,
			JournalSizeLimit: cfg.SQLite.JournalSizeLimit,
		})
		if err != nil {
			return db, nil, fmt.Errorf("initialize SQLite database at %q: %w", dbPath, err)
		}
		log.Info("SQLite connected", "path", dbPath, "purpose", "transaction indexing")
	}
	if cause := context.Cause(ctx); cause != nil {
		return db, repoManager, cause
	}

	return db, repoManager, nil
}

// newTxBroadcaster returns a callback that wire-encodes a raw transaction blob
// and broadcasts it to peers. Shared by the RPC-submit relay (SetTxBroadcaster)
// and the post-LCL recovered-tx relay (SetTxRelay), which are byte-identical.
func newTxBroadcaster(overlay *peermanagement.Overlay) func([]byte) {
	return func(txBlob []byte) {
		txMsg := &message.Transaction{
			RawTransaction: txBlob,
			Status:         message.TxStatusCurrent,
		}
		frame, err := message.EncodeFrame(txMsg)
		if err != nil {
			return
		}
		overlay.Broadcast(frame)
	}
}

// HTTP transport timeouts. httpWriteTimeout must stay strictly greater than
// rpcDispatchTimeout: net/http measures WriteTimeout from the start of the
// handler and covers execution plus the response write, so a request that
// consumes its full dispatch budget still needs headroom to serialize its
// timeout/error envelope instead of writing into a socket net/http has already
// closed (which the client sees as a connection reset, not a clean 503).
// Deriving both from the one dispatch constant keeps them from drifting.
const (
	rpcDispatchTimeout    = 30 * time.Second
	httpReadHeaderTimeout = 10 * time.Second
	httpReadTimeout       = rpcDispatchTimeout + 10*time.Second
	httpWriteTimeout      = rpcDispatchTimeout + 10*time.Second
	httpIdleTimeout       = 60 * time.Second
)

// Node-object cache defaults applied when the operator leaves node_db
// cache_size / cache_age unset.
const (
	defaultNodeCacheSize = 2_097_152
	defaultNodeCacheAge  = 90 * time.Minute
)

func pebbleStoreOptions(n config.NodeDBConfig) (kvpebble.Options, error) {
	resolved, err := kvpebble.OptionsFromMiB(n.CacheMB, n.OpenFiles)
	if err != nil {
		return kvpebble.Options{}, fmt.Errorf("node_db Pebble options: %w", err)
	}
	return resolved, nil
}

// nodeStoreCacheParams maps node_db cache_size (entries) and cache_age
// (minutes) onto the node-object cache parameters, substituting the
// built-in defaults for unset (zero) values.
func nodeStoreCacheParams(n config.NodeDBConfig, nodeSize string) (int, time.Duration) {
	size, age := defaultNodeCacheSize, defaultNodeCacheAge
	switch nodeSize {
	case "tiny":
		size, age = 262_144, 30*time.Minute
	case "small":
		size, age = 524_288, 60*time.Minute
	case "large":
		size, age = 4_194_304, 120*time.Minute
	case "huge":
		size, age = 8_388_608, 900*time.Minute
	}
	if n.CacheSize > 0 {
		size = n.CacheSize
	}
	if n.CacheAge > 0 {
		age = time.Duration(n.CacheAge) * time.Minute
	}
	return size, age
}

// parsePortConfig builds the per-port RPC context (admin and
// secure_gateway nets, connection limits) for a listener of the given
// protocol ("ws" or "http").
func parsePortConfig(protocol, name string, p config.PortConfig) (*rpc.PortContext, error) {
	adminNets, err := p.ParseAdminNets()
	if err != nil {
		return nil, fmt.Errorf("parse admin nets for %s port %q: %w", protocol, name, err)
	}
	secureGW, err := p.ParseSecureGatewayNets()
	if err != nil {
		return nil, fmt.Errorf("parse secure_gateway nets for %s port %q: %w", protocol, name, err)
	}
	allowedOrigins, err := config.NormalizeOrigins(p.AllowedOrigins)
	if err != nil {
		return nil, fmt.Errorf("parse allowed_origins for %s port %q: %w", protocol, name, err)
	}
	return &rpc.PortContext{
		PortName:          name,
		AdminNets:         append([]net.IPNet(nil), adminNets...),
		AdminUser:         p.AdminUser,
		AdminPassword:     p.AdminPassword,
		User:              p.User,
		Password:          p.Password,
		AllowedOrigins:    append([]string(nil), allowedOrigins...),
		SecureGatewayNets: secureGW,
		Limit:             p.Limit,
		SendQueue:         p.SendQueueLimit,
	}, nil
}

// staticValidatorReloader is the writable surface
// reloadTrustedValidators drives on a successful config reload.
// Satisfied by *adaptor.Components, which routes the new static set
// through the component trust-merge lock and latest publisher snapshot
// so a SIGHUP removal is not silently undone by the next OnChange.
type staticValidatorReloader interface {
	ReloadStaticValidators(validators []consensus.NodeID, masterKeys [][33]byte)
	ValidateValidatorReload(publisherKeys [][33]byte, publisherSites []string, publisherThreshold, staticValidatorCount int) error
}

type validatorConfigLoader func(config.Paths) (*config.Config, error)

const validatorReloadTimeout = 5 * time.Second

type stallPinger interface {
	SetStallPing(ping func())
}

// reloadTrustedValidators is the SIGHUP entry point: bridge from the
// production *adaptor.Components down to the pure applyValidatorReload
// helper. Skipped silently when components is nil (standalone mode).
func reloadTrustedValidators(serverLog xrpllog.Logger, components *adaptor.Components, configPath string) {
	if components == nil {
		return
	}
	applyValidatorReload(serverLog, components, configPath)
}

func runValidatorReloadLoop(
	ctx context.Context,
	serverLog xrpllog.Logger,
	reloader staticValidatorReloader,
	configPath string,
	requests <-chan os.Signal,
	load validatorConfigLoader,
) {
	loadGate := make(chan struct{}, 1)
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-requests:
			if !ok {
				return
			}
			if reloader == nil {
				continue
			}
			reloadCtx, cancel := context.WithTimeout(ctx, validatorReloadTimeout)
			applyValidatorReloadContextWithGate(reloadCtx, serverLog, reloader, configPath, load, loadGate)
			cancel()
		}
	}
}

// applyValidatorReload re-reads configPath, re-parses the [validators]
// stanza, and pushes the result into reloader. Errors are logged and
// the previous trusted set is retained — a bad reload must not wedge
// the node.
//
// Skipped silently when configPath is empty (validator config can't
// be re-read from nothing).
func applyValidatorReload(serverLog xrpllog.Logger, reloader staticValidatorReloader, configPath string) {
	applyValidatorReloadContext(context.Background(), serverLog, reloader, configPath, config.LoadConfig)
}

func applyValidatorReloadContext(
	ctx context.Context,
	serverLog xrpllog.Logger,
	reloader staticValidatorReloader,
	configPath string,
	load validatorConfigLoader,
) {
	applyValidatorReloadContextWithGate(ctx, serverLog, reloader, configPath, load, make(chan struct{}, 1))
}

func applyValidatorReloadContextWithGate(
	ctx context.Context,
	serverLog xrpllog.Logger,
	reloader staticValidatorReloader,
	configPath string,
	load validatorConfigLoader,
	loadGate chan struct{},
) {
	if configPath == "" {
		serverLog.Warn("SIGHUP received but no --conf path set; skipping UNL reload")
		return
	}
	if err := context.Cause(ctx); err != nil {
		serverLog.Warn("SIGHUP UNL reload canceled", "err", err)
		return
	}
	type loadResult struct {
		cfg *config.Config
		err error
	}
	select {
	case loadGate <- struct{}{}:
	case <-ctx.Done():
		serverLog.Warn("SIGHUP UNL reload canceled", "err", context.Cause(ctx))
		return
	}
	loaded := make(chan loadResult, 1)
	go func() {
		defer func() { <-loadGate }()
		cfg, err := load(config.Paths{Main: configPath})
		loaded <- loadResult{cfg: cfg, err: err}
	}()

	var result loadResult
	select {
	case <-ctx.Done():
		serverLog.Warn("SIGHUP UNL reload canceled", "err", context.Cause(ctx))
		return
	case result = <-loaded:
	}
	cfg, err := result.cfg, result.err
	if err != nil {
		serverLog.Error("SIGHUP UNL reload: re-load config failed", "err", err)
		return
	}
	publisherKeys, err := adaptor.ParseValidatorListPublisherKeys(cfg)
	if err != nil {
		serverLog.Error("SIGHUP UNL reload: parse validator_list_keys failed", "err", err)
		return
	}
	validators, masterKeys, err := adaptor.ParseValidatorKeysWithMaster(cfg)
	if err != nil {
		serverLog.Error("SIGHUP UNL reload: parse validators failed", "err", err)
		return
	}
	if err := reloader.ValidateValidatorReload(
		publisherKeys,
		cfg.Validators.ValidatorListSites,
		cfg.Validators.EffectiveListThreshold(),
		len(validators),
	); err != nil {
		serverLog.Error("SIGHUP UNL reload rejected", "err", err)
		return
	}
	if err := context.Cause(ctx); err != nil {
		serverLog.Warn("SIGHUP UNL reload canceled", "err", err)
		return
	}
	reloader.ReloadStaticValidators(validators, masterKeys)
	serverLog.Info("SIGHUP UNL reload applied",
		"validators_count", len(validators),
		"master_keys_count", len(masterKeys),
	)
}

// buildTable constructs the live amendment table from the operator's
// [amendments] config and any persisted runtime votes. Config preferences are
// applied first, then persisted votes (from the `feature` RPC) override them so
// runtime changes win across restarts — mirroring rippled, where the FeatureVotes
// DB takes precedence over the config stanzas. Unknown names are logged and
// ignored. The returned table owns operator veto/upvote and the enabled/blocked
// state, and is shared between the ledger service and the consensus adaptor.
func buildTable(ctx context.Context, cfg config.AmendmentsConfig, repo relationaldb.RepositoryManager, log xrpllog.Logger) *amendment.Table {
	t := amendment.NewTable()
	for _, name := range cfg.Upvote {
		f := amendment.FeatureByName(name)
		if f == nil {
			log.Warn("unknown amendment in [amendments].upvote; ignoring", "name", name)
			continue
		}
		t.UpVote(f.ID)
	}
	for _, name := range cfg.Veto {
		f := amendment.FeatureByName(name)
		if f == nil {
			log.Warn("unknown amendment in [amendments].veto; ignoring", "name", name)
			continue
		}
		t.Veto(f.ID)
	}

	if repo == nil || repo.Amendment() == nil {
		return t
	}
	recs, err := repo.Amendment().LoadAmendmentVotes(ctx)
	if err != nil {
		log.Warn("failed to load persisted amendment votes; using config only", "err", err)
		return t
	}
	for _, rec := range recs {
		idBytes, derr := hex.DecodeString(rec.Amendment)
		if derr != nil || len(idBytes) != 32 {
			log.Warn("skipping malformed persisted amendment vote", "amendment", rec.Amendment)
			continue
		}
		var id [32]byte
		copy(id[:], idBytes)
		if rec.Vetoed {
			t.Veto(id)
		} else {
			t.UpVote(id)
		}
	}
	return t
}

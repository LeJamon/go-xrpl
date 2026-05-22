package cli

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/config"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/adaptor"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	"github.com/LeJamon/goXRPLd/internal/observability"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/internal/rpc"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	validatorlist "github.com/LeJamon/goXRPLd/internal/validator/list"
	xrpllog "github.com/LeJamon/goXRPLd/log"
	"github.com/LeJamon/goXRPLd/protocol"
	kvpebble "github.com/LeJamon/goXRPLd/storage/kvstore/pebble"
	"github.com/LeJamon/goXRPLd/storage/nodestore"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
	"github.com/LeJamon/goXRPLd/storage/relationaldb/postgres"
	sqlitedb "github.com/LeJamon/goXRPLd/storage/relationaldb/sqlite"
	"github.com/LeJamon/goXRPLd/version"
	"github.com/spf13/cobra"
)

var (
	standalone bool
)

// serverCmd represents the server command (default action)
var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the XRPL daemon server",
	Long: `Start the goXRPLd server which provides:
- HTTP JSON-RPC API endpoints
- WebSocket server for real-time subscriptions
- Health check endpoint
- All XRPL protocol methods

Requires --conf flag to specify the configuration file.
Use 'xrpld generate-config' to create an initial configuration file.`,
	RunE: runServer,
}

func init() {
	rootCmd.AddCommand(serverCmd)

	// Set server as the default command
	rootCmd.RunE = runServer

	// Server-specific flags — operational concerns only
	serverCmd.Flags().BoolVarP(&standalone, "standalone", "a", false, "run in standalone mode (no peers)")
}

func runServer(cmd *cobra.Command, args []string) (retErr error) {
	if _, err := requireConfig(); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "%v\n", err)
		fmt.Fprintf(cmd.ErrOrStderr(), "  Use 'xrpld generate-config' to create an initial configuration file.\n")
		fmt.Fprintf(cmd.ErrOrStderr(), "  Example: xrpld server --conf /path/to/xrpld.toml\n")
		return err
	}

	// Initialize structured logger from config + CLI flag overrides.
	logCfg := globalConfig.Logging.ToLogConfig(globalConfig.DebugLogfile)
	if debug {
		logCfg.Level = xrpllog.LevelDebug
	}
	if verbose {
		logCfg.Level = xrpllog.LevelTrace
	}
	rootLogger := xrpllog.New(xrpllog.NewHandler(logCfg), logCfg)
	xrpllog.SetRoot(rootLogger)
	xrpllog.SetRootConfig(logCfg)
	serverLog := rootLogger.Named(xrpllog.PartitionServer)

	serverLog.Info("Starting goXRPLd", "version", version.Version)

	// Set GOXRPL_PPROF=:6060 (or any addr:port) to enable pprof. Off by default.
	if addr := os.Getenv("GOXRPL_PPROF"); addr != "" {
		go func() {
			if err := startPProfServer(addr); err != nil {
				serverLog.Warn("pprof server failed", "addr", addr, "err", err)
			}
		}()
		serverLog.Info("pprof enabled", "addr", addr)
	}

	// Pre-declared so the deferred shutdown can clean up whatever the
	// init path managed to populate before any error return. doShutdown
	// tolerates nil components for the partial-init case.
	var (
		db                  nodestore.Database
		repoManager         relationaldb.RepositoryManager
		ledgerService       *service.Service
		consensusComponents *adaptor.Components
		httpSrvs            []*http.Server
		wsSrvs              []*http.Server
		wsServer            *rpc.WebSocketServer
	)
	defer func() {
		doShutdown(httpSrvs, wsSrvs, wsServer, ledgerService, consensusComponents, db, repoManager, serverLog)
	}()

	// Initialize storage from config
	nodestorePath := globalConfig.NodeDB.Path
	if nodestorePath != "" {
		store, err := kvpebble.New(nodestorePath, 256<<20, 500, false)
		if err != nil {
			return fmt.Errorf("storage backend: %w", err)
		}

		db = nodestore.NewKVDatabase(store, "pebble("+nodestorePath+")", 10000, 10*time.Minute)
		serverLog.Info("Storage initialized", "backend", "pebble", "path", nodestorePath)
	} else {
		serverLog.Info("Storage initialized", "backend", "in-memory")
	}

	// Initialize RelationalDB if configured
	dbPath := globalConfig.DatabasePath
	if strings.HasPrefix(dbPath, "postgres://") || strings.HasPrefix(dbPath, "postgresql://") {
		pgConfig := relationaldb.NewConfig()
		pgConfig.ConnectionString = dbPath

		var err error
		repoManager, err = postgres.NewRepositoryManager(pgConfig)
		if err != nil {
			serverLog.Warn("PostgreSQL not available", "err", err)
		} else {
			if err := repoManager.Open(context.Background()); err != nil {
				serverLog.Warn("PostgreSQL connection failed", "err", err)
				repoManager = nil
			} else {
				serverLog.Info("PostgreSQL connected", "purpose", "transaction indexing")
			}
		}
	} else if dbPath != "" {
		// Default: auto-create SQLite databases at the given directory path
		var err error
		repoManager, err = sqlitedb.NewRepositoryManager(dbPath)
		if err != nil {
			serverLog.Warn("SQLite failed to initialize", "path", dbPath, "err", err)
		} else {
			if err := repoManager.Open(context.Background()); err != nil {
				serverLog.Warn("SQLite failed to open", "path", dbPath, "err", err)
				repoManager = nil
			} else {
				serverLog.Info("SQLite connected", "path", dbPath, "purpose", "transaction indexing")
			}
		}
	}

	// Load genesis configuration from config file path (if set)
	genesisFile := globalConfig.GenesisFile
	var genesisConfig genesis.Config
	if genesisFile != "" {
		genesisJSON, err := config.LoadGenesisJSON(genesisFile)
		if err != nil {
			return fmt.Errorf("load genesis file %q: %w", genesisFile, err)
		}
		if err := genesisJSON.Validate(); err != nil {
			return fmt.Errorf("invalid genesis file %q: %w", genesisFile, err)
		}
		genesisCfg, err := genesisJSON.ToGenesisConfig()
		if err != nil {
			return fmt.Errorf("parse genesis configuration %q: %w", genesisFile, err)
		}
		genesisConfig = genesis.Config{
			TotalXRP:            genesisCfg.TotalXRP,
			CloseTimeResolution: genesisCfg.CloseTimeResolution,
			Fees: genesis.DefaultFees{
				BaseFee:          genesisCfg.BaseFee,
				ReserveBase:      genesisCfg.ReserveBase,
				ReserveIncrement: genesisCfg.ReserveIncrement,
			},
			Amendments: genesisCfg.Amendments,
		}
		for _, acc := range genesisCfg.InitialAccounts {
			genesisConfig.InitialAccounts = append(genesisConfig.InitialAccounts, genesis.InitialAccount{
				Address:  acc.Address,
				Balance:  acc.Balance,
				Sequence: acc.Sequence,
				Flags:    acc.Flags,
			})
		}
		serverLog.Info("Genesis config loaded", "path", genesisFile)
	} else {
		genesisConfig = genesis.DefaultConfig()
		if globalConfig.GenesisAmendmentsDisabled {
			genesisConfig.Amendments = nil
		}
		serverLog.Info("Genesis config using built-in defaults")
	}

	// Get network ID from config
	networkID, err := globalConfig.GetNetworkID()
	if err != nil {
		return fmt.Errorf("get network ID: %w", err)
	}

	// Initialize ledger service
	cfg := service.Config{
		Standalone:   standalone,
		NetworkID:    uint32(networkID),
		NodeStore:    db,
		RelationalDB: repoManager,
		Logger:       rootLogger,
	}
	cfg.GenesisConfig = genesisConfig

	ledgerService, err = service.New(cfg)
	if err != nil {
		return fmt.Errorf("create ledger service: %w", err)
	}

	if err := ledgerService.Start(); err != nil {
		return fmt.Errorf("start ledger service: %w", err)
	}

	// Start the goroutine-scheduling-latency sampler. Runs in both
	// standalone and consensus modes; cancelled when runServer returns.
	// Mirrors rippled's beast::io_latency_probe lifetime
	// (rippled/src/xrpld/app/main/Application.cpp:1537).
	samplerCtx, cancelSampler := context.WithCancel(context.Background())
	defer cancelSampler()
	observability.StartSchedLatencySampler(samplerCtx)

	// Wire up RPC services
	ledgerAdapter := rpc.NewLedgerServiceAdapter(ledgerService)
	services := types.NewServiceContainer(ledgerAdapter)

	// TxQ metrics are available in both standalone and consensus modes,
	// so wire the server_info hook before the !standalone branch.
	ledgerSvcRef := ledgerService
	services.TxQMetrics = func() types.TxQServerMetrics {
		m := ledgerSvcRef.GetTxQMetrics()
		return types.TxQServerMetrics{
			ReferenceFeeLevel:     m.ReferenceFeeLevel,
			MinProcessingFeeLevel: m.MinProcessingFeeLevel,
			OpenLedgerFeeLevel:    m.OpenLedgerFeeLevel,
		}
	}

	// Start consensus/networking if not in standalone mode
	if !standalone {
		var compErr error
		var validationRepo relationaldb.ValidationRepository
		if repoManager != nil {
			validationRepo = repoManager.Validation()
		}
		consensusComponents, compErr = adaptor.NewFromConfig(globalConfig, ledgerService, validationRepo)
		if compErr != nil {
			return fmt.Errorf("create consensus components: %w", compErr)
		}

		if err := consensusComponents.Start(); err != nil {
			return fmt.Errorf("start consensus components: %w", err)
		}

		// Wire transaction relay: when a tx is submitted via RPC,
		// broadcast it to peers. LocalTxs holding is handled inside
		// service.SubmitTransaction so the broadcaster only relays.
		overlay := consensusComponents.Overlay

		// Closed-Ledger / Previous-Ledger hints (Handshake.cpp:219-223).
		overlay.SetLedgerHintProvider(func() (peermanagement.LedgerHints, bool) {
			cl := ledgerService.GetClosedLedger()
			if cl == nil {
				return peermanagement.LedgerHints{}, false
			}
			return peermanagement.LedgerHints{Closed: cl.Hash(), Parent: cl.ParentHash()}, true
		})

		overlay.SetValidLedgerProvider(func() (uint32, time.Duration, bool) {
			vl := ledgerService.GetValidatedLedger()
			if vl == nil {
				return 0, 0, false
			}
			age := time.Since(vl.CloseTime())
			return vl.Sequence(), age, true
		})
		ledgerAdapter.SetTxBroadcaster(func(txBlob []byte) {
			txMsg := &message.Transaction{
				RawTransaction: txBlob,
				Status:         message.TxStatusCurrent,
			}
			encoded, err := message.Encode(txMsg)
			if err != nil {
				return
			}
			frame, err := message.BuildWireMessage(message.TypeTransaction, encoded)
			if err != nil {
				return
			}
			overlay.Broadcast(frame)
		})
		// Wire OpenLedger.Accept's relay callback so recovered txs are
		// re-broadcast post-LCL (rippled OpenLedger.cpp:120-150).
		ledgerService.SetTxRelay(func(txBlob []byte) {
			txMsg := &message.Transaction{
				RawTransaction: txBlob,
				Status:         message.TxStatusCurrent,
			}
			encoded, err := message.Encode(txMsg)
			if err != nil {
				return
			}
			frame, err := message.BuildWireMessage(message.TypeTransaction, encoded)
			if err != nil {
				return
			}
			overlay.Broadcast(frame)
		})

		// Expose node identity and consensus stats to RPC handlers.
		services.NodePublicKey = consensusComponents.Overlay.Identity().EncodedPublicKey()
		engine := consensusComponents.Engine
		services.LastCloseInfo = func() (int, int) {
			proposers, convergeTime := engine.GetLastCloseInfo()
			return proposers, int(convergeTime.Milliseconds())
		}
		// Expose the live consensus quorum to the `server_info` RPC so
		// operators see the actual quorum (recomputed by the adaptor
		// from UNL ∖ negative-UNL) instead of the hardcoded "1" that
		// the bootstrap-time field used to return — #451.
		services.ValidationQuorum = consensusComponents.Adaptor.GetQuorum

		// Peer-disconnect counters and the operating-mode state-accounting
		// snapshot need the overlay/adaptor, so they live inside the
		// !standalone branch. (TxQMetrics is wired above; it only needs
		// the ledger service.)
		overlayRef := consensusComponents.Overlay
		services.PeerDisconnects = func() (uint64, uint64) {
			return overlayRef.PeerDisconnects(), overlayRef.PeerDisconnectsResources()
		}
		services.JqTransOverflow = overlayRef.DroppedTransactions
		acctRef := consensusComponents.Adaptor
		services.StateAccounting = func() types.StateAccountingSnapshot {
			snap := acctRef.StateAccounting()
			if len(snap.Modes) == 0 {
				return types.StateAccountingSnapshot{}
			}
			modes := make(map[string]types.StateAccountingEntry, len(snap.Modes))
			for mode, entry := range snap.Modes {
				modes[mode] = types.StateAccountingEntry{
					Transitions: entry.Transitions,
					DurationUs:  entry.DurationUs,
				}
			}
			return types.StateAccountingSnapshot{
				Modes:             modes,
				CurrentDurationUs: snap.CurrentDurationUs,
				InitialSyncUs:     snap.InitialSyncUs,
			}
		}
		services.CloseTimeOffset = acctRef.CloseOffset
		// Expose the validator-manifest cache to the `manifest` RPC.
		// The cache is shared — the router writes inbound manifests,
		// the engine reads for ephemeral→master translation, and this
		// RPC reads for external queries.
		services.Manifests = consensusComponents.Manifests

		// Expose the publisher-list aggregator (when configured) to
		// the `validators` and `validator_list_sites` RPC methods.
		// nil-safe: NewRPCReader returns an inert reader when the
		// aggregator is nil, so the handlers return empty arrays in
		// that case rather than panicking.
		services.ValidatorList = validatorlist.NewRPCReader(consensusComponents.ValidatorList)

		// Expose static config validators, cached signing keys, and the
		// negative-UNL set to the `validators` RPC so it returns the
		// same shape rippled's ValidatorList::getJson does.
		//
		// Bind to the live accessor (not a boot-time copy) so a SIGHUP
		// reload of the [validators] stanza is visible to the RPC.
		componentsRef := consensusComponents
		services.LocalStaticTrustedKeysBase58 = func() []string {
			masters := componentsRef.StaticTrustedMasterKeys()
			out := make([]string, 0, len(masters))
			for _, mk := range masters {
				if enc, err := addresscodec.EncodeNodePublicKey(mk[:]); err == nil {
					out = append(out, enc)
				}
			}
			return out
		}
		if mc := consensusComponents.Manifests; mc != nil {
			// Mirrors rippled getJson at ValidatorList.cpp:1726-1734 —
			// `signing_keys` only surfaces master→signing pairs for
			// masters present in keyListings_, i.e. validators listed
			// by at least one publisher or pinned in the local
			// [validators] stanza. Without this filter we would leak
			// every gossiped manifest, including ones unrelated to any
			// trusted publisher.
			vlAgg := consensusComponents.ValidatorList
			services.SigningKeysBase58 = func() map[string]string {
				snap := mc.MasterToSigning()
				if len(snap) == 0 {
					return nil
				}
				listed := make(map[[33]byte]struct{})
				for _, mk := range componentsRef.StaticTrustedMasterKeys() {
					listed[mk] = struct{}{}
				}
				if vlAgg != nil {
					for _, p := range vlAgg.PublisherSnapshot() {
						for _, mk := range p.Validators {
							listed[mk] = struct{}{}
						}
					}
				}
				if len(listed) == 0 {
					return nil
				}
				out := make(map[string]string, len(listed))
				for master, signing := range snap {
					if _, ok := listed[master]; !ok {
						continue
					}
					mEnc, mErr := addresscodec.EncodeNodePublicKey(master[:])
					sEnc, sErr := addresscodec.EncodeNodePublicKey(signing[:])
					if mErr == nil && sErr == nil {
						out[mEnc] = sEnc
					}
				}
				return out
			}
		}
		adaptorRef := consensusComponents.Adaptor
		services.NegativeUNLBase58 = func() []string {
			masters := adaptorRef.GetNegativeUNLMasters()
			if len(masters) == 0 {
				return nil
			}
			out := make([]string, 0, len(masters))
			for _, mk := range masters {
				if enc, err := addresscodec.EncodeNodePublicKey(mk[:]); err == nil {
					out = append(out, enc)
				}
			}
			return out
		}

		// Expose the local validator's signing key to validator_info.
		// Mirrors rippled's getValidationPublicKey gate: empty means
		// the server is not configured as a validator and the handler
		// returns "not a validator".
		if vid, err := consensusComponents.Adaptor.GetValidatorKey(); err == nil {
			pk := make([]byte, 33)
			copy(pk, vid[:])
			services.ValidatorPublicKey = pk
		}

		isValidator := globalConfig.IsValidator()
		serverLog.Info("Running in consensus mode",
			"validator", isValidator,
			"peers", len(globalConfig.IPs)+len(globalConfig.IPsFixed),
		)
	} else {
		genesisAddr, _ := ledgerService.GetGenesisAccount()
		serverLog.Info("Running in standalone mode",
			"genesisAccount", genesisAddr,
			"validatedLedger", ledgerService.GetValidatedLedgerIndex(),
			"openLedger", ledgerService.GetCurrentLedgerIndex(),
		)
	}

	// Create HTTP JSON-RPC server with 30 second timeout
	httpServer := rpc.NewServer(30*time.Second, services)
	if consensusComponents != nil && consensusComponents.Overlay != nil {
		httpServer.SetPeerSource(consensusComponents.Overlay)
	}

	services.SetDispatcher(httpServer)

	// Create WebSocket server for real-time subscriptions
	wsServer = rpc.NewWebSocketServer(30*time.Second, services)
	wsServer.RegisterAllMethods()
	if consensusComponents != nil && consensusComponents.Overlay != nil {
		wsServer.SetPeerSource(consensusComponents.Overlay)
	}

	// Create a ledger info provider adapter for WebSocket subscribe responses
	wsServer.SetLedgerInfoProvider(&ledgerInfoAdapter{ledgerService: ledgerService})

	publisher := rpc.NewPublisher(wsServer.GetSubscriptionManager())

	// Wire pubPeerStatus → peer_status WebSocket subscription. Mirrors
	// rippled NetworkOPs::pubPeerStatus (NetworkOPs.cpp:2514-2540) which
	// broadcasts to InfoSubs registered for the sPeerStatus stream.
	if consensusComponents != nil && consensusComponents.Overlay != nil {
		consensusComponents.Overlay.SetPeerStatusPublisher(func(u peermanagement.PeerStatusUpdate) {
			publisher.PublishPeerStatus(&rpc.PeerStatusEvent{
				Type:           "peerStatusChange",
				Status:         u.Status,
				Action:         u.Action,
				Date:           u.Date,
				LedgerHash:     u.LedgerHash,
				LedgerIndex:    u.LedgerIndex,
				LedgerIndexMin: u.LedgerIndexMin,
				LedgerIndexMax: u.LedgerIndexMax,
			})
		})
	}

	// Wire up ledger service events to WebSocket broadcasts
	ledgerService.SetEventCallback(func(event *service.LedgerAcceptedEvent) {
		if event == nil || event.LedgerInfo == nil {
			return
		}

		baseFee, reserveBase, reserveInc := ledgerService.GetCurrentFees()

		ledgerTime := uint32(event.LedgerInfo.CloseTime.Unix() - protocol.RippleEpochUnix)

		ledgerCloseEvent := &rpc.LedgerCloseEvent{
			Type:             "ledgerClosed",
			LedgerIndex:      event.LedgerInfo.Sequence,
			LedgerHash:       hex.EncodeToString(event.LedgerInfo.Hash[:]),
			LedgerTime:       ledgerTime,
			FeeBase:          baseFee,
			FeeRef:           baseFee,
			ReserveBase:      reserveBase,
			ReserveInc:       reserveInc,
			TxnCount:         len(event.TransactionResults),
			ValidatedLedgers: "",
		}
		publisher.PublishLedgerClosed(ledgerCloseEvent)

		for _, txResult := range event.TransactionResults {
			// Decode binary tx+meta blob to JSON for the event.
			// TxData is VL-encoded: [VL-length][tx_blob][VL-length][meta_blob]
			txJSON, metaJSON := decodeTxWithMetaToJSON(txResult.TxData)

			txEvent := &rpc.TransactionEvent{
				Type:                "transaction",
				EngineResult:        "tesSUCCESS",
				EngineResultCode:    0,
				EngineResultMessage: "The transaction was applied. Only final in a validated ledger.",
				LedgerIndex:         txResult.LedgerIndex,
				LedgerHash:          hex.EncodeToString(txResult.LedgerHash[:]),
				Transaction:         txJSON,
				Meta:                metaJSON,
				Hash:                hex.EncodeToString(txResult.TxHash[:]),
				Validated:           txResult.Validated,
			}
			publisher.PublishTransaction(txEvent, txResult.AffectedAccounts)
		}

		// Update persistent path_find sessions on ledger close
		wsServer.UpdatePathFindSessions(func() (types.LedgerStateView, error) {
			return services.Ledger.GetClosedLedgerView()
		})

		serverLog.Debug("Broadcasted ledger",
			"sequence", event.LedgerInfo.Sequence,
			"txs", len(event.TransactionResults),
		)
	})

	// Shared connection limiter for all ports
	connLimiter := rpc.NewConnLimiter()
	wsServer.SetConnLimiter(connLimiter)

	// Build the base HTTP mux (shared handler logic, wrapped per-port below)
	httpMux := http.NewServeMux()
	httpMux.Handle("/", httpServer)
	httpMux.Handle("/rpc", httpServer)
	httpMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","service":"goXRPLd"}`))
	})

	// Start listeners from config ports
	httpPorts := globalConfig.GetHTTPPorts()
	wsPorts := globalConfig.GetWebSocketPorts()

	for name, p := range httpPorts {
		serverLog.Info("Port configured", "protocol", "http", "name", name, "addr", p.GetBindAddress())
	}
	for name, p := range wsPorts {
		serverLog.Info("Port configured", "protocol", "ws", "name", name, "addr", p.GetBindAddress())
	}
	if _, peerPort, hasPeer := globalConfig.GetPeerPort(); hasPeer {
		serverLog.Info("Port configured", "protocol", "peer", "addr", peerPort.GetBindAddress())
	}

	// listenerErrCh routes ListenAndServe failures back to the main
	// goroutine so shutdown runs the deferred cleanup chain.
	listenerErrCh := make(chan error, 1+len(wsPorts)+len(httpPorts))

	// Start WebSocket listeners — each port gets its own mux with PortMiddleware
	for name, p := range wsPorts {
		portCfg := p
		adminNets, err := portCfg.ParseAdminNets()
		if err != nil {
			return fmt.Errorf("parse admin nets for ws port %q: %w", name, err)
		}
		secureGW, err := portCfg.ParseSecureGatewayNets()
		if err != nil {
			return fmt.Errorf("parse secure_gateway nets for ws port %q: %w", name, err)
		}
		pc := &rpc.PortContext{
			PortName:          name,
			AdminNets:         adminNets,
			SecureGatewayNets: secureGW,
			Limit:             portCfg.Limit,
			SendQueue:         portCfg.SendQueueLimit,
		}
		mux := http.NewServeMux()
		mux.Handle("/", rpc.PortMiddleware(pc, connLimiter, wsServer))
		srv := &http.Server{Addr: portCfg.GetBindAddress(), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		wsSrvs = append(wsSrvs, srv)
		go func(n string, s *http.Server) {
			serverLog.Info("Listening", "protocol", "ws", "name", n, "addr", s.Addr)
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serverLog.Error("WebSocket server failed", "name", n, "addr", s.Addr, "err", err)
				select {
				case listenerErrCh <- fmt.Errorf("ws %s (%s): %w", n, s.Addr, err):
				default:
				}
			}
		}(name, srv)
	}

	// Start HTTP listeners — each port gets its own mux with PortMiddleware.
	// SecureGatewayNets are scoped per-port via PortContext so XFF trust
	// for one port never bleeds across to another (matches rippled, which
	// passes a single Port& into requestRole / forwardedFor —
	// ServerHandler.cpp:709-734).
	httpPortList := make([]struct {
		name string
		pc   *rpc.PortContext
		addr string
	}, 0, len(httpPorts))
	for name, p := range httpPorts {
		portCfg := p
		adminNets, err := portCfg.ParseAdminNets()
		if err != nil {
			return fmt.Errorf("parse admin nets for http port %q: %w", name, err)
		}
		secureGW, err := portCfg.ParseSecureGatewayNets()
		if err != nil {
			return fmt.Errorf("parse secure_gateway nets for http port %q: %w", name, err)
		}
		pc := &rpc.PortContext{
			PortName:          name,
			AdminNets:         adminNets,
			SecureGatewayNets: secureGW,
			Limit:             portCfg.Limit,
			SendQueue:         portCfg.SendQueueLimit,
		}
		httpPortList = append(httpPortList, struct {
			name string
			pc   *rpc.PortContext
			addr string
		}{name, pc, portCfg.GetBindAddress()})
	}

	if len(httpPortList) == 0 {
		return fmt.Errorf("no HTTP ports configured — at least one HTTP port is required")
	}

	for _, entry := range httpPortList {
		wrappedMux := http.NewServeMux()
		wrappedMux.Handle("/", rpc.PortMiddleware(entry.pc, connLimiter, httpMux))
		srv := &http.Server{
			Addr:         entry.addr,
			Handler:      wrappedMux,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			IdleTimeout:  60 * time.Second,
		}
		httpSrvs = append(httpSrvs, srv)
		go func(n, addr string, s *http.Server) {
			serverLog.Info("Listening", "protocol", "http", "name", n, "addr", addr)
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serverLog.Error("HTTP server failed", "name", n, "addr", addr, "err", err)
				select {
				case listenerErrCh <- fmt.Errorf("http %s (%s): %w", n, addr, err):
				default:
				}
			}
		}(entry.name, entry.addr, srv)
	}

	// Add signal handling and a shared shutdown trigger
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// SIGHUP triggers a UNL reload: re-read the config from --conf and
	// replace the adaptor's trusted validator set. Per-round delta
	// detection in the consensus engine then drives OnUNLChange so
	// newly-added validators get the NegativeUNL grace period.
	// Mirrors the operator-trigger surface of rippled's ValidatorList
	// (applyLists → updateTrusted) without (yet) the publisher-trust
	// subsystem. Buffered so a flurry of HUPs coalesces.
	reloadCh := make(chan os.Signal, 1)
	signal.Notify(reloadCh, syscall.SIGHUP)

	// shutdownCh lets the RPC stop command trigger the same path
	shutdownCh := make(chan struct{}, 1)

	services.SetShutdownFunc(func() {
		serverLog.Info("Shutdown requested via RPC stop command")
		shutdownCh <- struct{}{}
	})

	// Block until signal, RPC stop, or a listener goroutine fails.
	// SIGHUP is non-terminating — handle it in-place and keep waiting.
	for {
		select {
		case sig := <-sigCh:
			serverLog.Info("Received signal, shutting down", "signal", sig)
			return retErr
		case <-shutdownCh:
			return retErr
		case err := <-listenerErrCh:
			serverLog.Error("Listener failed — initiating shutdown", "err", err)
			retErr = err
			return retErr
		case <-reloadCh:
			reloadTrustedValidators(serverLog, consensusComponents)
		}
	}
}

// staticValidatorReloader is the writable surface
// reloadTrustedValidators drives on a successful config reload.
// Satisfied by *adaptor.Components, which routes the new static set
// through staticMu + a merge with the live publisher-trust aggregator
// so a SIGHUP removal is not silently undone by the next OnChange.
type staticValidatorReloader interface {
	ReloadStaticValidators(validators []consensus.NodeID, masterKeys [][33]byte)
}

// reloadTrustedValidators is the SIGHUP entry point: bridge from the
// production *adaptor.Components down to the pure applyValidatorReload
// helper. Skipped silently when components is nil (standalone mode).
func reloadTrustedValidators(serverLog xrpllog.Logger, components *adaptor.Components) {
	if components == nil {
		return
	}
	applyValidatorReload(serverLog, components, configFile)
}

// applyValidatorReload re-reads configPath, re-parses the [validators]
// stanza, and pushes the result into reloader. Errors are logged and
// the previous trusted set is retained — a bad reload must not wedge
// the node.
//
// Skipped silently when configPath is empty (validator config can't
// be re-read from nothing).
func applyValidatorReload(serverLog xrpllog.Logger, reloader staticValidatorReloader, configPath string) {
	if configPath == "" {
		serverLog.Warn("SIGHUP received but no --conf path set; skipping UNL reload")
		return
	}
	cfg, err := config.LoadConfig(config.ConfigPaths{Main: configPath})
	if err != nil {
		serverLog.Error("SIGHUP UNL reload: re-load config failed", "err", err)
		return
	}
	validators, masterKeys, err := adaptor.ParseValidatorKeysWithMaster(cfg)
	if err != nil {
		serverLog.Error("SIGHUP UNL reload: parse validators failed", "err", err)
		return
	}
	reloader.ReloadStaticValidators(validators, masterKeys)
	serverLog.Info("SIGHUP UNL reload applied",
		"validators_count", len(validators),
		"master_keys_count", len(masterKeys),
	)
}

// doShutdown performs graceful shutdown of all server components
func doShutdown(
	httpSrvs, wsSrvs []*http.Server,
	wsServer *rpc.WebSocketServer,
	ledgerService *service.Service,
	consensusComponents *adaptor.Components,
	kvDB nodestore.Database,
	repoManager relationaldb.RepositoryManager,
	logger xrpllog.Logger,
) {
	const drainTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()

	logger.Info("Draining HTTP connections...")
	for _, srv := range httpSrvs {
		_ = srv.Shutdown(ctx)
	}
	for _, srv := range wsSrvs {
		_ = srv.Shutdown(ctx)
	}

	if err := wsServer.Close(ctx); err != nil {
		logger.Warn("WebSocket server shutdown timed out", "err", err)
	}

	// Stop consensus components (if running)
	if consensusComponents != nil {
		consensusComponents.Stop()
		logger.Info("Consensus components stopped")
	}

	// Note: ledgerService has no Stop method; it is garbage collected
	_ = ledgerService
	if kvDB != nil {
		kvDB.Close()
	}
	if repoManager != nil {
		repoManager.Close(context.Background())
	}

	logger.Info("Shutdown complete")
}

// ledgerInfoAdapter adapts the ledger service to the LedgerInfoProvider interface
type ledgerInfoAdapter struct {
	ledgerService *service.Service
}

func (a *ledgerInfoAdapter) GetCurrentLedgerInfo() *types.LedgerSubscribeInfo {
	if a.ledgerService == nil {
		return nil
	}

	validatedLedger := a.ledgerService.GetValidatedLedger()
	if validatedLedger == nil {
		return nil
	}

	baseFee, reserveBase, reserveInc := a.ledgerService.GetCurrentFees()

	ledgerTime := uint32(validatedLedger.CloseTime().Unix() - protocol.RippleEpochUnix)

	hash := validatedLedger.Hash()
	serverInfo := a.ledgerService.GetServerInfo()

	return &types.LedgerSubscribeInfo{
		LedgerIndex:      validatedLedger.Sequence(),
		LedgerHash:       hex.EncodeToString(hash[:]),
		LedgerTime:       ledgerTime,
		FeeBase:          baseFee,
		FeeRef:           baseFee,
		ReserveBase:      reserveBase,
		ReserveInc:       reserveInc,
		ValidatedLedgers: serverInfo.CompleteLedgers,
	}
}

// decodeTxWithMetaToJSON splits a VL-encoded tx+meta binary blob and decodes
// each part to JSON. The blob format is: [VL-length][tx_blob][VL-length][meta_blob].
// Returns (txJSON, metaJSON) as json.RawMessage, or empty JSON objects on error.
func decodeTxWithMetaToJSON(data []byte) (json.RawMessage, json.RawMessage) {
	emptyObj := json.RawMessage("{}")

	if len(data) == 0 {
		return emptyObj, emptyObj
	}

	// Parse first VL field (transaction)
	txLen, txPrefixLen := parseVLLength(data)
	if txPrefixLen == 0 || txPrefixLen+txLen > len(data) {
		return emptyObj, emptyObj
	}
	txBlob := data[txPrefixLen : txPrefixLen+txLen]

	// Parse second VL field (metadata)
	metaStart := txPrefixLen + txLen
	var metaBlob []byte
	if metaStart < len(data) {
		metaLen, metaPrefixLen := parseVLLength(data[metaStart:])
		if metaPrefixLen > 0 && metaStart+metaPrefixLen+metaLen <= len(data) {
			metaBlob = data[metaStart+metaPrefixLen : metaStart+metaPrefixLen+metaLen]
		}
	}

	// Decode transaction binary to JSON
	txHex := hex.EncodeToString(txBlob)
	txMap, err := binarycodec.Decode(txHex)
	if err != nil {
		return emptyObj, emptyObj
	}
	txJSON, err := json.Marshal(txMap)
	if err != nil {
		return emptyObj, emptyObj
	}

	// Decode metadata binary to JSON
	metaJSON := emptyObj
	if len(metaBlob) > 0 {
		metaHex := hex.EncodeToString(metaBlob)
		metaMap, err := binarycodec.Decode(metaHex)
		if err == nil {
			if m, err := json.Marshal(metaMap); err == nil {
				metaJSON = m
			}
		}
	}

	return json.RawMessage(txJSON), metaJSON
}

// parseVLLength parses a variable-length field prefix.
// Returns (length, bytesConsumed).
func parseVLLength(data []byte) (int, int) {
	if len(data) == 0 {
		return 0, 0
	}
	b1 := int(data[0])
	if b1 <= 192 {
		return b1, 1
	}
	if b1 <= 240 {
		if len(data) < 2 {
			return 0, 0
		}
		return 193 + ((b1 - 193) * 256) + int(data[1]), 2
	}
	if len(data) < 3 {
		return 0, 0
	}
	return 12481 + ((b1 - 241) * 65536) + (int(data[1]) * 256) + int(data[2]), 3
}

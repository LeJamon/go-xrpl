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

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/config"
	"github.com/LeJamon/goXRPLd/internal/consensus/adaptor"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	"github.com/LeJamon/goXRPLd/internal/observability"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/internal/rpc"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	xrpllog "github.com/LeJamon/goXRPLd/log"
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
	Run: runServer,
}

func init() {
	rootCmd.AddCommand(serverCmd)

	// Set server as the default command
	rootCmd.Run = runServer

	// Server-specific flags — operational concerns only
	serverCmd.Flags().BoolVarP(&standalone, "standalone", "a", false, "run in standalone mode (no peers)")
}

func runServer(cmd *cobra.Command, args []string) {
	// Require config file
	if globalConfig == nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Error: --conf flag is required to start the server.\n")
		fmt.Fprintf(cmd.ErrOrStderr(), "  Use 'xrpld generate-config' to create an initial configuration file.\n")
		fmt.Fprintf(cmd.ErrOrStderr(), "  Example: xrpld server --conf /path/to/xrpld.toml\n")
		return
	}

	// Initialize structured logger from config + CLI flag overrides.
	logCfg := globalConfig.Logging.ToLogConfig(globalConfig.DebugLogfile)
	if debug {
		logCfg.Level = xrpllog.LevelDebug
	}
	if verbose {
		logCfg.Level = xrpllog.LevelTrace
	}
	rootLogger := xrpllog.New(xrpllog.NewHandler(&logCfg), &logCfg)
	xrpllog.SetRoot(rootLogger)
	xrpllog.SetRootConfig(&logCfg)
	serverLog := rootLogger.Named(xrpllog.PartitionServer)

	serverLog.Info("Starting goXRPLd", "version", version.Version)

	// Initialize storage from config
	var db nodestore.Database
	nodestorePath := globalConfig.NodeDB.Path
	if nodestorePath != "" {
		store, err := kvpebble.New(nodestorePath, 256<<20, 500, false)
		if err != nil {
			serverLog.Fatal("Failed to create storage backend", "err", err)
		}

		db = nodestore.NewKVDatabase(store, "pebble("+nodestorePath+")", 10000, 10*time.Minute)
		serverLog.Info("Storage initialized", "backend", "pebble", "path", nodestorePath)
	} else {
		serverLog.Info("Storage initialized", "backend", "in-memory")
	}

	// Initialize RelationalDB if configured
	var repoManager relationaldb.RepositoryManager
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
			serverLog.Fatal("Failed to load genesis file", "path", genesisFile, "err", err)
		}
		if err := genesisJSON.Validate(); err != nil {
			serverLog.Fatal("Invalid genesis file", "path", genesisFile, "err", err)
		}
		genesisCfg, err := genesisJSON.ToGenesisConfig()
		if err != nil {
			serverLog.Fatal("Failed to parse genesis configuration", "path", genesisFile, "err", err)
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
		serverLog.Fatal("Failed to get network ID", "err", err)
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

	ledgerService, err := service.New(cfg)
	if err != nil {
		serverLog.Fatal("Failed to create ledger service", "err", err)
	}

	if err := ledgerService.Start(); err != nil {
		serverLog.Fatal("Failed to start ledger service", "err", err)
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
	types.InitServices(ledgerAdapter)

	// Start consensus/networking if not in standalone mode
	var consensusComponents *adaptor.Components
	if !standalone {
		var compErr error
		consensusComponents, compErr = adaptor.NewFromConfig(globalConfig, ledgerService, repoManager.Validation())
		if compErr != nil {
			serverLog.Fatal("Failed to create consensus components", "err", compErr)
		}

		if err := consensusComponents.Start(); err != nil {
			serverLog.Fatal("Failed to start consensus components", "err", err)
		}

		// Wire transaction relay: when a tx is submitted via RPC,
		// broadcast it to peers and add to the consensus pending pool.
		overlay := consensusComponents.Overlay
		consensusAdaptor := consensusComponents.Adaptor

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
			consensusAdaptor.AddPendingTx(txBlob)
		})

		// Expose node identity, peer count, and consensus stats to RPC handlers
		types.Services.NodePublicKey = consensusComponents.Overlay.Identity().EncodedPublicKey()
		types.Services.PeerCount = consensusComponents.Overlay.PeerCount
		engine := consensusComponents.Engine
		types.Services.LastCloseInfo = func() (int, int) {
			proposers, convergeTime := engine.GetLastCloseInfo()
			return proposers, int(convergeTime.Milliseconds())
		}
		// Expose the validator-manifest cache to the `manifest` RPC.
		// The cache is shared — the router writes inbound manifests,
		// the engine reads for ephemeral→master translation, and this
		// RPC reads for external queries.
		types.Services.Manifests = consensusComponents.Manifests

		// Expose the local validator's signing key to validator_info.
		// Mirrors rippled's getValidationPublicKey gate: empty means
		// the server is not configured as a validator and the handler
		// returns "not a validator".
		if vid, err := consensusComponents.Adaptor.GetValidatorKey(); err == nil {
			pk := make([]byte, 33)
			copy(pk, vid[:])
			types.Services.ValidatorPublicKey = pk
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
	httpServer := rpc.NewServer(30 * time.Second)
	if consensusComponents != nil && consensusComponents.Overlay != nil {
		httpServer.SetPeerSource(consensusComponents.Overlay)
	}

	types.Services.SetDispatcher(httpServer)

	// Create WebSocket server for real-time subscriptions
	wsServer := rpc.NewWebSocketServer(30 * time.Second)
	wsServer.RegisterAllMethods()

	// Create a ledger info provider adapter for WebSocket subscribe responses
	wsServer.SetLedgerInfoProvider(&ledgerInfoAdapter{ledgerService: ledgerService})

	publisher := rpc.NewPublisher(wsServer.GetSubscriptionManager())

	// Wire up ledger service events to WebSocket broadcasts
	ledgerService.SetEventCallback(func(event *service.LedgerAcceptedEvent) {
		if event == nil || event.LedgerInfo == nil {
			return
		}

		baseFee, reserveBase, reserveInc := ledgerService.GetCurrentFees()

		rippleEpoch := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		ledgerTime := uint32(event.LedgerInfo.CloseTime.Unix() - rippleEpoch.Unix())

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
			return types.Services.Ledger.GetClosedLedgerView()
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

	// Start WebSocket listeners — each port gets its own mux with PortMiddleware
	var wsSrvs []*http.Server
	for name, p := range wsPorts {
		portCfg := p
		adminNets, err := portCfg.ParseAdminNets()
		if err != nil {
			serverLog.Fatal("Failed to parse admin nets for port", "name", name, "err", err)
		}
		pc := &rpc.PortContext{
			PortName:  name,
			AdminNets: adminNets,
			Limit:     portCfg.Limit,
			SendQueue: portCfg.SendQueueLimit,
		}
		mux := http.NewServeMux()
		mux.Handle("/", rpc.PortMiddleware(pc, connLimiter, wsServer))
		srv := &http.Server{Addr: portCfg.GetBindAddress(), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		wsSrvs = append(wsSrvs, srv)
		go func(n string, s *http.Server) {
			serverLog.Info("Listening", "protocol", "ws", "name", n, "addr", s.Addr)
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serverLog.Fatal("WebSocket server failed", "name", n, "addr", s.Addr, "err", err)
			}
		}(name, srv)
	}

	// Start HTTP listeners — each port gets its own mux with PortMiddleware
	httpPortList := make([]struct {
		name string
		pc   *rpc.PortContext
		addr string
	}, 0, len(httpPorts))
	for name, p := range httpPorts {
		portCfg := p
		adminNets, err := portCfg.ParseAdminNets()
		if err != nil {
			serverLog.Fatal("Failed to parse admin nets for port", "name", name, "err", err)
		}
		pc := &rpc.PortContext{
			PortName:  name,
			AdminNets: adminNets,
			Limit:     portCfg.Limit,
			SendQueue: portCfg.SendQueueLimit,
		}
		httpPortList = append(httpPortList, struct {
			name string
			pc   *rpc.PortContext
			addr string
		}{name, pc, portCfg.GetBindAddress()})
	}

	if len(httpPortList) == 0 {
		serverLog.Fatal("No HTTP ports configured — at least one HTTP port is required")
	}

	// Collect HTTP servers into a slice
	var httpSrvs []*http.Server

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
				serverLog.Fatal("HTTP server failed", "name", n, "addr", addr, "err", err)
			}
		}(entry.name, entry.addr, srv)
	}

	// Add signal handling and a shared shutdown trigger
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// shutdownCh lets the RPC stop command trigger the same path
	shutdownCh := make(chan struct{}, 1)

	types.Services.SetShutdownFunc(func() {
		serverLog.Info("Shutdown requested via RPC stop command")
		shutdownCh <- struct{}{}
	})

	// Block until signal or RPC stop
	select {
	case sig := <-sigCh:
		serverLog.Info("Received signal, shutting down", "signal", sig)
	case <-shutdownCh:
	}

	doShutdown(httpSrvs, wsSrvs, wsServer, ledgerService, consensusComponents, db, repoManager, serverLog)
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

	wsServer.Close()

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

	rippleEpoch := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	ledgerTime := uint32(validatedLedger.CloseTime().Unix() - rippleEpoch.Unix())

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

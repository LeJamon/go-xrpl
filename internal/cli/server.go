package cli

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/LeJamon/goXRPLd/config"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	"github.com/LeJamon/goXRPLd/internal/rpc"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	xrpllog "github.com/LeJamon/goXRPLd/log"
	kvpebble "github.com/LeJamon/goXRPLd/storage/kvstore/pebble"
	"github.com/LeJamon/goXRPLd/storage/nodestore"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
	"github.com/LeJamon/goXRPLd/storage/relationaldb/postgres"
	sqlitedb "github.com/LeJamon/goXRPLd/storage/relationaldb/sqlite"
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
	rootLogger := xrpllog.New(xrpllog.NewHandler(logCfg), &logCfg)
	xrpllog.SetRoot(rootLogger)
	serverLog := rootLogger.Named(xrpllog.PartitionServer)

	serverLog.Info("Starting goXRPLd", "version", "0.1.0-dev")

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
			Amendments:    genesisCfg.Amendments,
			UseModernFees: genesisCfg.UseModernFees,
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
		serverLog.Info("Genesis config using built-in defaults")
	}

	// Initialize ledger service
	cfg := service.Config{
		Standalone:   standalone,
		NodeStore:    db,
		RelationalDB: repoManager,
		Logger:       rootLogger,
	}
	if standalone {
		cfg.GenesisConfig = genesisConfig
	}

	ledgerService, err := service.New(cfg)
	if err != nil {
		serverLog.Fatal("Failed to create ledger service", "err", err)
	}

	if err := ledgerService.Start(); err != nil {
		serverLog.Fatal("Failed to start ledger service", "err", err)
	}

	// Wire up RPC services
	types.InitServices(rpc.NewLedgerServiceAdapter(ledgerService))

	if standalone {
		genesisAddr, _ := ledgerService.GetGenesisAccount()
		serverLog.Info("Running in standalone mode",
			"genesisAccount", genesisAddr,
			"validatedLedger", ledgerService.GetValidatedLedgerIndex(),
			"openLedger", ledgerService.GetCurrentLedgerIndex(),
		)
	}

	// Create HTTP JSON-RPC server with 30 second timeout
	httpServer := rpc.NewServer(30 * time.Second)

	types.Services.SetDispatcher(httpServer)

	types.Services.SetShutdownFunc(func() {
		serverLog.Info("Shutdown requested via RPC stop command")
		go func() {
			time.Sleep(100 * time.Millisecond)
			serverLog.Fatal("Server stopped by admin request")
		}()
	})

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
			txEvent := &rpc.TransactionEvent{
				Type:                "transaction",
				EngineResult:        "tesSUCCESS",
				EngineResultCode:    0,
				EngineResultMessage: "The transaction was applied. Only final in a validated ledger.",
				LedgerIndex:         txResult.LedgerIndex,
				LedgerHash:          hex.EncodeToString(txResult.LedgerHash[:]),
				Transaction:         json.RawMessage(txResult.TxData),
				Meta:                json.RawMessage(txResult.MetaData),
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

	// Start listeners based on configured ports
	httpMux := http.NewServeMux()
	httpMux.Handle("/", httpServer)
	httpMux.Handle("/rpc", httpServer)
	httpMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","service":"goXRPLd"}`))
	})

	wsMux := http.NewServeMux()
	wsMux.Handle("/", wsServer)

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

	// Start WebSocket listeners
	for name, p := range wsPorts {
		addr := p.GetBindAddress()
		portName := name
		go func() {
			serverLog.Info("Listening", "protocol", "ws", "name", portName, "addr", addr)
			if err := http.ListenAndServe(addr, wsMux); err != nil {
				serverLog.Fatal("WebSocket server failed", "name", portName, "addr", addr, "err", err)
			}
		}()
	}

	// Start HTTP listeners — use the first one as the blocking listener, rest in goroutines
	httpPortList := make([]struct {
		name string
		addr string
	}, 0, len(httpPorts))
	for name, p := range httpPorts {
		httpPortList = append(httpPortList, struct {
			name string
			addr string
		}{name, p.GetBindAddress()})
	}

	if len(httpPortList) == 0 {
		serverLog.Fatal("No HTTP ports configured — at least one HTTP port is required")
	}

	// Start extra HTTP listeners in goroutines
	for i := 1; i < len(httpPortList); i++ {
		entry := httpPortList[i]
		go func() {
			serverLog.Info("Listening", "protocol", "http", "name", entry.name, "addr", entry.addr)
			if err := http.ListenAndServe(entry.addr, httpMux); err != nil {
				serverLog.Fatal("HTTP server failed", "name", entry.name, "addr", entry.addr, "err", err)
			}
		}()
	}

	// Start the first HTTP listener (blocks)
	first := httpPortList[0]
	serverLog.Info("Listening", "protocol", "http", "name", first.name, "addr", first.addr)
	if err := http.ListenAndServe(first.addr, httpMux); err != nil {
		serverLog.Fatal("HTTP server failed", "name", first.name, "addr", first.addr, "err", err)
	}
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

// getDataDir returns the data directory path from config.
// Uses node_db.path's parent directory.
func getDataDir() string {
	if globalConfig == nil {
		return ""
	}
	return filepath.Dir(globalConfig.NodeDB.Path)
}

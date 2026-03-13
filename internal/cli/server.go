package cli

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/LeJamon/goXRPLd/config"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	"github.com/LeJamon/goXRPLd/internal/rpc"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
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

	if !quiet {
		fmt.Println("Starting goXRPLd - XRPL Node Implementation")
		fmt.Println("=========================================")
	}

	// Initialize storage from config
	var db nodestore.Database
	nodestorePath := globalConfig.NodeDB.Path
	if nodestorePath != "" {
		store, err := kvpebble.New(nodestorePath, 256<<20, 500, false)
		if err != nil {
			log.Fatal("Failed to create storage backend:", err)
		}

		db = nodestore.NewKVDatabase(store, "pebble("+nodestorePath+")", 10000, 10*time.Minute)

		if !quiet {
			fmt.Printf("Storage: %s\n", nodestorePath)
		}
	} else {
		if !quiet {
			fmt.Println("Storage: in-memory only")
		}
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
			if !quiet {
				fmt.Printf("PostgreSQL: not available (%v)\n", err)
			}
		} else {
			if err := repoManager.Open(context.Background()); err != nil {
				if !quiet {
					fmt.Printf("PostgreSQL: connection failed (%v)\n", err)
				}
				repoManager = nil
			} else if !quiet {
				fmt.Println("PostgreSQL: connected for transaction indexing")
			}
		}
	} else if dbPath != "" {
		// Default: auto-create SQLite databases at the given directory path
		var err error
		repoManager, err = sqlitedb.NewRepositoryManager(dbPath)
		if err != nil {
			if !quiet {
				fmt.Printf("SQLite: failed to initialize (%v)\n", err)
			}
		} else {
			if err := repoManager.Open(context.Background()); err != nil {
				if !quiet {
					fmt.Printf("SQLite: failed to open (%v)\n", err)
				}
				repoManager = nil
			} else if !quiet {
				fmt.Printf("SQLite: transaction indexing at %s\n", dbPath)
			}
		}
	}

	// Load genesis configuration from config file path (if set)
	genesisFile := globalConfig.GenesisFile
	var genesisConfig genesis.Config
	if genesisFile != "" {
		genesisJSON, err := config.LoadGenesisJSON(genesisFile)
		if err != nil {
			log.Fatal("Failed to load genesis file:", err)
		}
		if err := genesisJSON.Validate(); err != nil {
			log.Fatal("Invalid genesis file:", err)
		}
		genesisCfg, err := genesisJSON.ToGenesisConfig()
		if err != nil {
			log.Fatal("Failed to parse genesis configuration:", err)
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
		if !quiet {
			fmt.Printf("Genesis: loaded from %s\n", genesisFile)
		}
	} else {
		genesisConfig = genesis.DefaultConfig()
		if !quiet {
			fmt.Println("Genesis: using built-in defaults")
		}
	}

	// Initialize ledger service
	cfg := service.Config{
		Standalone:   standalone,
		NodeStore:    db,
		RelationalDB: repoManager,
	}
	if standalone {
		cfg.GenesisConfig = genesisConfig
	}

	ledgerService, err := service.New(cfg)
	if err != nil {
		log.Fatal("Failed to create ledger service:", err)
	}

	if err := ledgerService.Start(); err != nil {
		log.Fatal("Failed to start ledger service:", err)
	}

	// Wire up RPC services
	types.InitServices(rpc.NewLedgerServiceAdapter(ledgerService))

	if !quiet {
		if standalone {
			fmt.Println("Running in STANDALONE mode")
			genesisAddr, _ := ledgerService.GetGenesisAccount()
			fmt.Printf("  Genesis account: %s\n", genesisAddr)
			fmt.Printf("  Genesis ledger:  %d\n", ledgerService.GetValidatedLedgerIndex())
			fmt.Printf("  Open ledger:     %d\n", ledgerService.GetCurrentLedgerIndex())
			fmt.Println()
		}
	}

	// Create HTTP JSON-RPC server with 30 second timeout
	httpServer := rpc.NewServer(30 * time.Second)

	types.Services.SetDispatcher(httpServer)

	types.Services.SetShutdownFunc(func() {
		log.Println("Server shutdown requested via RPC stop command")
		go func() {
			time.Sleep(100 * time.Millisecond)
			log.Fatal("Server stopped by admin request")
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

		if !quiet {
			log.Printf("Broadcasted ledger %d with %d transactions to WebSocket subscribers",
				event.LedgerInfo.Sequence, len(event.TransactionResults))
		}
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

	if !quiet {
		fmt.Println("Server Configuration:")
		for name, p := range httpPorts {
			fmt.Printf("  - HTTP (%s): http://%s/\n", name, p.GetBindAddress())
		}
		for name, p := range wsPorts {
			fmt.Printf("  - WebSocket (%s): ws://%s/\n", name, p.GetBindAddress())
		}
		if _, _, hasPeer := globalConfig.GetPeerPort(); hasPeer {
			_, peerPort, _ := globalConfig.GetPeerPort()
			fmt.Printf("  - Peer: %s\n", peerPort.GetBindAddress())
		}
		fmt.Println()
	}

	// Start WebSocket listeners with named *http.Server instances
	var wsSrvs []*http.Server
	for name, p := range wsPorts {
		addr := p.GetBindAddress()
		portName := name
		srv := &http.Server{Addr: addr, Handler: wsMux, ReadHeaderTimeout: 10 * time.Second}
		wsSrvs = append(wsSrvs, srv)
		go func(n string, s *http.Server) {
			if !quiet {
				fmt.Printf("Starting WebSocket server (%s) on %s...\n", n, s.Addr)
			}
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("WebSocket server (%s) failed to start on %s: %v", n, s.Addr, err)
			}
		}(portName, srv)
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
		log.Fatal("No HTTP ports configured — at least one HTTP port is required")
	}

	// Collect HTTP servers into a slice
	var httpSrvs []*http.Server

	// Start extra HTTP listeners in goroutines
	for i := 1; i < len(httpPortList); i++ {
		entry := httpPortList[i]
		srv := &http.Server{
			Addr:         entry.addr,
			Handler:      httpMux,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			IdleTimeout:  60 * time.Second,
		}
		httpSrvs = append(httpSrvs, srv)
		go func(n, addr string, s *http.Server) {
			if !quiet {
				fmt.Printf("Starting HTTP server (%s) on %s...\n", n, addr)
			}
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("HTTP server (%s) failed to start on %s: %v", n, addr, err)
			}
		}(entry.name, entry.addr, srv)
	}

	// Start the first HTTP listener (will block until shutdown)
	first := httpPortList[0]
	firstSrv := &http.Server{
		Addr:         first.addr,
		Handler:      httpMux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	httpSrvs = append(httpSrvs, firstSrv)
	if !quiet {
		fmt.Printf("Starting HTTP server (%s) on %s...\n", first.name, first.addr)
	}
	go func() {
		if err := firstSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP server (%s) failed to start on %s: %v", first.name, first.addr, err)
		}
	}()

	// Add signal handling and a shared shutdown trigger
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// shutdownCh lets the RPC stop command trigger the same path
	shutdownCh := make(chan struct{}, 1)

	types.Services.SetShutdownFunc(func() {
		log.Println("Shutdown requested via RPC stop command")
		shutdownCh <- struct{}{}
	})

	// Block until signal or RPC stop
	select {
	case sig := <-sigCh:
		log.Printf("Received signal %s — shutting down", sig)
	case <-shutdownCh:
	}

	doShutdown(httpSrvs, wsSrvs, wsServer, ledgerService, db, repoManager, quiet)
}

// doShutdown performs graceful shutdown of all server components
func doShutdown(
	httpSrvs, wsSrvs []*http.Server,
	wsServer *rpc.WebSocketServer,
	ledgerService *service.Service,
	kvDB nodestore.Database,
	repoManager relationaldb.RepositoryManager,
	quiet bool,
) {
	const drainTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()

	if !quiet {
		log.Println("Draining HTTP connections...")
	}
	for _, srv := range httpSrvs {
		_ = srv.Shutdown(ctx)
	}
	for _, srv := range wsSrvs {
		_ = srv.Shutdown(ctx)
	}

	wsServer.Close()

	// Note: ledgerService has no Stop method; it is garbage collected
	_ = ledgerService
	if kvDB != nil {
		kvDB.Close()
	}
	if repoManager != nil {
		repoManager.Close(context.Background())
	}

	log.Println("Shutdown complete")
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

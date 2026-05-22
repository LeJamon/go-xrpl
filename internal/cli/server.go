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
	"strconv"
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
	"github.com/LeJamon/goXRPLd/internal/manifest"
	"github.com/LeJamon/goXRPLd/internal/observability"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/internal/rpc"
	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
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

		// Wire the tx-set "we have this" announce: BuildTxSet fires
		// onTxSetBuilt → overlay broadcasts TMHaveTransactionSet{tsHAVE}.
		// Mirrors rippled's post-consensus mtHAVE_SET emission so peers
		// acquiring the same set via mtHAVE_SET{tsNEED} can find a
		// source without polling.
		consensusComponents.Adaptor.SetOnTxSetBuilt(func(id consensus.TxSetID) {
			overlay.BroadcastHaveTxSet([32]byte(id))
		})

		// Wire the open-ledger tx lookup used by the tx-reduce-relay
		// reply path (TMGetObjectByHash{otTRANSACTIONS} → TMTransactions
		// reply) and the periodic TMHaveTransactions announce.
		// Feature-gated downstream by Config.EnableTxReduceRelay; the
		// providers themselves are always wired so a flip of the
		// config flag doesn't require a restart-and-rewire.
		overlay.SetTxProvider(ledgerService.OpenLedgerGetTx)
		overlay.SetOpenLedgerHashesProvider(ledgerService.OpenLedgerTxHashes)

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

	// Wire the WebSocket event sources that previously had a publisher
	// helper but no upstream subscriber. Each call mirrors a rippled
	// pubXxx feed (NetworkOPs.cpp); without them the corresponding
	// streams accepted subscribers but never delivered.
	if consensusComponents != nil && consensusComponents.Overlay != nil {
		// pubPeerStatus → peer_status (NetworkOPs.cpp:2514-2540).
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

		// pubManifest → manifests (NetworkOPs.cpp:2234-2261). One sink
		// installed on the cache, fed by every accepted manifest
		// regardless of source (overlay relay, startup, validator-list
		// aggregator, local-manifest emit).
		if consensusComponents.Manifests != nil {
			consensusComponents.Manifests.SetOnAccepted(func(m *manifest.Manifest) {
				publisher.PublishManifest(buildManifestEvent(m))
			})
		}

		// pubValidation + pubConsensus → validations / consensus
		// (NetworkOPs.cpp:2380-2510). One subscriber on the engine's
		// event bus, fanning the typed events out to the publisher.
		if consensusComponents.Engine != nil {
			consensusComponents.Engine.Subscribe(&rpcEventBridge{publisher: publisher})
		}
	}

	// pubProposedTransaction → transactions_proposed / accounts_proposed
	// (NetworkOPs.cpp:2316-2370). Fires on every SubmitTransaction
	// attempt, regardless of standalone vs. consensus mode.
	ledgerService.SetSubmittedTxCallback(func(ev service.SubmittedTxEvent) {
		publisher.PublishProposedTransaction(
			buildProposedTxEvent(ev),
			proposedAccounts(ev.AffectedAccount),
		)
	})

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

		// pubOrderBook → book stream (NetworkOPs.cpp subBook +
		// Subscribe.cpp:231-356). One delivery per (taker_gets, taker_pays)
		// currency pair touched by a tx in this ledger. The
		// subscription manager filters per-connection book matches,
		// so we always emit both sides and let it elide.
		for _, txResult := range event.TransactionResults {
			pairs := extractBookPairsFromTxData(txResult.TxData)
			if len(pairs) == 0 {
				continue
			}
			ledgerHashStr := hex.EncodeToString(txResult.LedgerHash[:])
			for _, pair := range pairs {
				ev := &rpc.OrderBookChangeEvent{
					Type:        "transaction",
					Status:      "closed",
					LedgerIndex: txResult.LedgerIndex,
					LedgerHash:  ledgerHashStr,
					LedgerTime:  ledgerTime,
					Validated:   txResult.Validated,
				}
				publisher.PublishOrderBookChange(ev, pair.takerGets, pair.takerPays)
			}
		}

		// pubBookChanges → book_changes aggregate stream
		// (Subscribe.cpp:139-142 + NetworkOPs.cpp:3160-3174). Reuses the
		// same RPC::computeBookChanges path as the per-ledger RPC; we
		// fetch the now-closed ledger through the same adapter the RPC
		// uses so the metadata walk sees an identical view.
		if reader, err := ledgerAdapter.GetLedgerBySequence(event.LedgerInfo.Sequence); err == nil && reader != nil {
			payload := handlers.ComputeBookChanges(reader)
			if data, err := json.Marshal(payload); err == nil {
				wsServer.GetSubscriptionManager().BroadcastToStream(types.SubBookChanges, data, nil)
			}
		}

		// pubServer → server stream (NetworkOPs.cpp:2316-2370). Fees and
		// reserves are recomputed by the engine on each ledger close, so
		// piggybacking the emit on the same callback gives a
		// rippled-compatible "fires when load / fees move" cadence. The
		// load-factor algebra is shared with the server_info handler via
		// handlers.ComputeServerLoad so the two surfaces agree on every
		// load_factor_* field.
		load := handlers.ComputeServerLoad(services)
		publisher.PublishServerStatus(&rpc.ServerStatusEvent{
			Type:                    "serverStatus",
			BaseFee:                 baseFee,
			LoadBase:                int(load.LoadBase),
			LoadFactor:              int(load.LoadFactor),
			LoadFactorLocal:         int(load.LoadFactorLocal),
			LoadFactorNet:           int(load.LoadFactorNet),
			LoadFactorCluster:       int(load.LoadFactorCluster),
			LoadFactorFeeEscalation: int(load.LoadFactorFeeEscalation),
			LoadFactorFeeQueue:      int(load.LoadFactorFeeQueue),
			LoadFactorServer:        int(load.LoadFactorServer),
			ServerStatus:            "full",
		})

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
		FeeBaseXRP:       float64(baseFee) / 1_000_000,
		FeeRef:           baseFee,
		ReserveBase:      reserveBase,
		ReserveInc:       reserveInc,
		TxnCount:         int(validatedLedger.TxCount()),
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

// rpcEventBridge fans the consensus engine's event bus out to the
// WebSocket subscription publisher. Mirrors NetworkOPs::pubValidation
// and NetworkOPs::pubConsensus (NetworkOPs.cpp:2380-2510): both feeds
// originate from the same engine and share a single bridge subscriber
// so the engine's broadcast goroutine never blocks on a publish.
type rpcEventBridge struct {
	publisher rpc.EventPublisher
}

func (b *rpcEventBridge) OnEvent(event consensus.Event) {
	if b == nil || b.publisher == nil {
		return
	}
	switch e := event.(type) {
	case *consensus.ValidationReceivedEvent:
		if e == nil || e.Validation == nil {
			return
		}
		b.publisher.PublishValidation(buildValidationEvent(e))
	case *consensus.PhaseChangedEvent:
		if e == nil {
			return
		}
		b.publisher.PublishConsensusPhase(consensusPhaseName(e.NewPhase))
	}
}

func consensusPhaseName(p consensus.Phase) string {
	switch p {
	case consensus.PhaseOpen:
		return rpc.ConsensusPhaseOpen
	case consensus.PhaseEstablish:
		return rpc.ConsensusPhaseEstablish
	case consensus.PhaseAccepted:
		return rpc.ConsensusPhaseAccepted
	default:
		return p.String()
	}
}

func buildValidationEvent(e *consensus.ValidationReceivedEvent) *rpc.ValidationEvent {
	v := e.Validation
	masterEnc, _ := addresscodec.EncodeNodePublicKey(v.SigningPubKey[:])
	ev := rpc.NewValidationEvent(
		hex.EncodeToString(v.LedgerID[:]),
		strconv.FormatUint(uint64(v.LedgerSeq), 10),
		masterEnc,
		hex.EncodeToString(v.Signature),
		uint32(v.SignTime.Unix()-protocol.RippleEpochUnix),
		v.Flags,
		v.Full,
	)
	if v.Cookie != 0 {
		ev.Cookie = strconv.FormatUint(v.Cookie, 16)
	}
	if v.LoadFee != 0 {
		ev.LoadFee = v.LoadFee
	}
	if v.BaseFee != 0 {
		ev.BaseFee = v.BaseFee
	} else if v.BaseFeeDrops != 0 {
		ev.BaseFee = v.BaseFeeDrops
	}
	if v.ReserveBase != 0 {
		ev.ReserveBase = uint64(v.ReserveBase)
	} else if v.ReserveBaseDrops != 0 {
		ev.ReserveBase = v.ReserveBaseDrops
	}
	if v.ReserveIncrement != 0 {
		ev.ReserveInc = uint64(v.ReserveIncrement)
	} else if v.ReserveIncrementDrops != 0 {
		ev.ReserveInc = v.ReserveIncrementDrops
	}
	if len(v.Amendments) > 0 {
		ev.Amendments = make([]string, len(v.Amendments))
		for i, a := range v.Amendments {
			ev.Amendments[i] = hex.EncodeToString(a[:])
		}
	}
	if v.ValidatedHash != [32]byte{} {
		ev.ValidatedHash = hex.EncodeToString(v.ValidatedHash[:])
	}
	return ev
}

// bookPair holds a single (takerGets, takerPays) currency pair touched
// by a transaction. Used to fan one tx out to N per-book subscribers.
type bookPair struct {
	takerGets types.CurrencySpec
	takerPays types.CurrencySpec
}

// extractBookPairsFromTxData walks a VL-encoded tx+meta blob and
// returns every distinct (takerGets, takerPays) pair from affected
// Offer nodes. Mirrors rippled's per-tx fan-out in NetworkOPs::pubProposedTx
// which feeds each Offer change into the matching subBook subscribers.
func extractBookPairsFromTxData(data []byte) []bookPair {
	_, metaJSON := decodeTxWithMetaToJSON(data)
	if len(metaJSON) == 0 {
		return nil
	}
	var meta struct {
		AffectedNodes []map[string]json.RawMessage `json:"AffectedNodes"`
	}
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []bookPair
	for _, node := range meta.AffectedNodes {
		for _, raw := range node {
			var nd struct {
				LedgerEntryType string                 `json:"LedgerEntryType"`
				FinalFields     map[string]interface{} `json:"FinalFields"`
			}
			if err := json.Unmarshal(raw, &nd); err != nil {
				continue
			}
			if nd.LedgerEntryType != "Offer" || nd.FinalFields == nil {
				continue
			}
			gets := currencySpecFromAmount(nd.FinalFields["TakerGets"])
			pays := currencySpecFromAmount(nd.FinalFields["TakerPays"])
			key := gets.Currency + "/" + gets.Issuer + "|" + pays.Currency + "/" + pays.Issuer
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, bookPair{takerGets: gets, takerPays: pays})
		}
	}
	return out
}

func currencySpecFromAmount(raw interface{}) types.CurrencySpec {
	switch v := raw.(type) {
	case string:
		return types.CurrencySpec{Currency: "XRP"}
	case map[string]interface{}:
		currency, _ := v["currency"].(string)
		issuer, _ := v["issuer"].(string)
		return types.CurrencySpec{Currency: currency, Issuer: issuer}
	default:
		return types.CurrencySpec{}
	}
}

func buildProposedTxEvent(ev service.SubmittedTxEvent) *rpc.ProposedTransactionEvent {
	txJSON := json.RawMessage("{}")
	if len(ev.RawBlob) > 0 {
		if decoded, err := binarycodec.Decode(hex.EncodeToString(ev.RawBlob)); err == nil {
			if encoded, err := json.Marshal(decoded); err == nil {
				txJSON = encoded
			}
		}
	}
	return rpc.NewProposedTransactionEvent(
		txJSON,
		ev.Result.Name,
		ev.Result.Code,
		ev.Result.Message,
		ev.CurrentLedger,
		ev.AffectedAccount,
	)
}

func proposedAccounts(account string) []string {
	if account == "" {
		return nil
	}
	return []string{account}
}

func buildManifestEvent(m *manifest.Manifest) *rpc.ManifestEvent {
	if m == nil {
		return nil
	}
	masterEnc, _ := addresscodec.EncodeNodePublicKey(m.MasterKey[:])
	var signingEnc string
	if !m.Revoked() {
		signingEnc, _ = addresscodec.EncodeNodePublicKey(m.SigningKey[:])
	}
	return rpc.NewManifestEvent(masterEnc, signingEnc, hex.EncodeToString(m.Serialized), m.Sequence)
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

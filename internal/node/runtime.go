package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/adaptor"
	"github.com/LeJamon/go-xrpl/internal/ledger/cleaner"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/ledger/shamapstore"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/observability"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	rpcadapter "github.com/LeJamon/go-xrpl/internal/rpc/adapter"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
	"github.com/LeJamon/go-xrpl/internal/watchdog"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

type nodeRuntime struct {
	ctx    context.Context
	cancel context.CancelCauseFunc

	appConfig  *config.Config
	configPath string
	standalone bool
	startup    service.StartupConfig
	rootLogger xrpllog.Logger
	serverLog  xrpllog.Logger
	options    RunOptions

	nodeStore  nodestore.Database
	repo       relationaldb.RepositoryManager
	nodeFamily *backend.NodeStore

	ledger        *service.Service
	cleaner       *cleaner.Cleaner
	cleanerSource *ledgerCleanerSource
	rotator       *shamapstore.Rotator

	consensus            *adaptor.Components
	peerConnectScheduler *peerConnectScheduler
	resourceManager      *resource.Manager
	ownsResourceManager  bool

	services      *types.ServiceGraphBuilder
	serviceGraph  *types.ServiceGraph
	ledgerAdapter *rpcadapter.LedgerServiceAdapter
	httpServer    *rpc.Server
	wsServer      *rpc.WebSocketServer
	publisher     *rpc.Publisher
	transports    *boundRPCTransports
	watchdog      *watchdog.Watchdog

	networkID                 uint32
	retentionFloor            func() uint32
	prepareFastLoadCheckpoint func(context.Context) (bool, error)
	shutdownCh                chan struct{}
	shutdowner                types.Shutdowner
	stopSampler               func()
	stopWatchdog              func()
}

func newNodeRuntime(
	ctx context.Context,
	appConfig *config.Config,
	configPath string,
	standalone bool,
	startup service.StartupConfig,
	rootLogger, serverLog xrpllog.Logger,
	options RunOptions,
) *nodeRuntime {
	runtimeCtx, cancel := context.WithCancelCause(ctx)
	shutdownCh := make(chan struct{}, 1)
	return &nodeRuntime{
		ctx:        runtimeCtx,
		cancel:     cancel,
		appConfig:  appConfig,
		configPath: configPath,
		standalone: standalone,
		startup:    startup,
		rootLogger: rootLogger,
		serverLog:  serverLog,
		options:    options,
		shutdownCh: shutdownCh,
		shutdowner: &shutdownController{log: serverLog, ch: shutdownCh},
	}
}

type shutdownController struct {
	log xrpllog.Logger
	ch  chan<- struct{}
}

func (c *shutdownController) RequestShutdown() {
	if c == nil {
		return
	}
	c.log.Info("Shutdown requested via RPC stop command")
	select {
	case c.ch <- struct{}{}:
	default:
	}
}

func (r *nodeRuntime) run() (runErr error) {
	defer r.cancel(nil)
	if err := context.Cause(r.ctx); err != nil {
		return err
	}
	if err := validateTrustedValidatorConfig(r.appConfig, r.standalone); err != nil {
		return err
	}

	defer func() {
		r.stopRuntime()
		if shutdownErr := r.shutdown(); shutdownErr != nil {
			runErr = errors.Join(runErr, shutdownErr)
		}
	}()

	stages := []func() error{
		r.configureStorage,
		r.configureLedger,
		r.configureMaintenance,
		r.configureConsensus,
		r.configureWatchdog,
		r.bindRPC,
		r.bindStreams,
		r.bindTransports,
		r.start,
	}
	for _, stage := range stages {
		if err := stage(); err != nil {
			return err
		}
	}
	return r.wait()
}

func (r *nodeRuntime) configureStorage() error {
	nodeStore, repo, err := setupStorage(r.ctx, r.appConfig, r.serverLog)
	r.nodeStore = nodeStore
	r.repo = repo
	if err != nil {
		return err
	}
	if err := context.Cause(r.ctx); err != nil {
		return err
	}
	if r.nodeStore != nil {
		r.nodeFamily = backend.New(r.nodeStore)
	}
	return nil
}

func (r *nodeRuntime) configureLedger() error {
	ctx := r.ctx
	genesisFile := r.appConfig.GenesisFile
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
		r.serverLog.Info("Genesis config loaded", "path", genesisFile)
	} else {
		genesisConfig = genesis.DefaultConfig()
		if r.appConfig.GenesisAmendmentsDisabled {
			genesisConfig.Amendments = nil
		}
		r.serverLog.Info("Genesis config using built-in defaults")
	}

	networkID, err := r.appConfig.ResolvedNetworkID()
	if err != nil {
		return fmt.Errorf("get network ID: %w", err)
	}
	r.networkID = uint32(networkID)

	// One instance is shared between the ledger service (which folds validated
	// flag ledgers into it) and the consensus adaptor (which sources vote
	// stances from it).
	amendmentTable := buildTable(ctx, r.appConfig.Amendments, r.repo, r.serverLog)
	if err := context.Cause(ctx); err != nil {
		return err
	}

	// Build the transaction-queue config from the operator's
	// [transaction_queue] stanza layered over the rippled defaults.
	txqCfg, err := service.TxQConfigFromTuning(r.appConfig.TransactionQueue, r.standalone)
	if err != nil {
		return err
	}

	cfg := service.Config{
		Standalone:      r.standalone,
		NodeSize:        r.appConfig.NodeSize,
		SweepInterval:   r.appConfig.ResolvedSweepInterval(),
		FetchDepth:      effectivePeerFetchDepth(r.appConfig.GetFetchDepthUint32(), r.appConfig.NodeDB.OnlineDelete),
		LedgerCacheSize: uint32(r.appConfig.ResolvedLedgerCacheSize()),
		NetworkID:       uint32(networkID),
		NodeStore:       r.nodeStore,
		SHAMapFamily:    r.nodeFamily,
		FastLoad:        r.appConfig.NodeDB.FastLoad,
		FastLoadWorkers: r.appConfig.NodeDB.FastLoadWorkers,
		RelationalDB:    r.repo,
		Logger:          r.rootLogger,
		Table:           amendmentTable,
		TxQ:             &txqCfg,
		Startup:         r.startup,
	}
	cfg.GenesisConfig = genesisConfig
	configuredFees := configuredLedgerLoadFees(r.appConfig)
	cfg.ConfiguredFees = &configuredFees

	r.ledger, err = service.New(cfg)
	if err != nil {
		return fmt.Errorf("create ledger service: %w", err)
	}

	if err := r.ledger.Start(); err != nil {
		return fmt.Errorf("start ledger service: %w", err)
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return nil
}

func (r *nodeRuntime) configureMaintenance() error {
	ctx := r.ctx
	sampler := observability.NewSchedLatencySampler()
	if err := sampler.Start(ctx); err != nil {
		return fmt.Errorf("start scheduler latency sampler: %w", err)
	}
	r.stopSampler = sampler.Stop

	r.ledgerAdapter = rpcadapter.NewLedgerServiceAdapter(r.ledger)
	r.services = newRPCServiceGraphBuilder(r.ledgerAdapter, r.appConfig)
	if err := r.configureStandaloneNodeIdentity(); err != nil {
		return err
	}

	// Advisory-delete state (can_delete RPC). Available in both standalone and
	// consensus modes; gated by node_db advisory_delete and persisted under
	// database_path. Mirrors rippled's SHAMapStore advisory-delete state.
	if advisoryStore, asErr := shamapstore.New(
		r.appConfig.NodeDB.IsOnlineDeleteEnabled() && r.appConfig.NodeDB.IsAdvisoryDeleteEnabled(),
		r.appConfig.LocalStateDir(),
	); asErr != nil {
		if r.appConfig.NodeDB.IsOnlineDeleteEnabled() {
			return fmt.Errorf("load online-delete state: %w", asErr)
		}
		r.serverLog.Warn("Failed to load advisory-delete state", "err", asErr)
	} else {
		r.services.AdvisoryDeleteState = advisoryStore

		// Online-delete rotation: when node_db online_delete is set and the
		// node store can enumerate its keyspace, run a background job that
		// reclaims disk by deleting complete ledgers below the rotation
		// boundary. NewRotator returns nil when online_delete is off.
		if r.appConfig.NodeDB.IsOnlineDeleteEnabled() {
			if prunable, ok := r.nodeStore.(shamapstore.NodePruner); ok {
				var relPruner shamapstore.RelationalPruner
				if r.repo != nil {
					relPruner = relationaldb.NewLedgerPruner(r.repo, r.appConfig.NodeDB.DeleteBatch)
				}
				r.rotator = shamapstore.NewRotator(
					advisoryStore,
					prunable,
					relPruner,
					shamapstore.RotationConfig{
						DeleteInterval: uint32(r.appConfig.NodeDB.OnlineDelete),
						DeleteBatch:    r.appConfig.NodeDB.DeleteBatch,
						BackOff:        time.Duration(r.appConfig.NodeDB.BackOffMilliseconds) * time.Millisecond,
					},
					r.serverLog,
				)
				if err := r.rotator.ReconcileGenerationState(); err != nil {
					return fmt.Errorf("reconcile online-delete generation state: %w", err)
				}
				minimumOnline := r.rotator.MinimumOnline()
				if minimumOnline == 0 && r.repo != nil {
					minSeq, minErr := r.repo.Ledger().GetMinLedgerSeq(ctx)
					if minErr != nil {
						return fmt.Errorf("load online-delete minimum ledger: %w", minErr)
					}
					if minSeq != nil {
						minimumOnline = uint32(*minSeq)
						if err := r.rotator.SetMinimumOnlineFloor(minimumOnline); err != nil {
							return fmt.Errorf("persist online-delete minimum ledger: %w", err)
						}
					}
				}
				if minimumOnline > 0 {
					r.nodeFamily.SetMinimumLedgerSeq(minimumOnline)
				}
				r.rotator.SetStateRefresh(
					r.ledger.RefreshValidatedState,
					r.nodeFamily.SetMinimumLedgerSeq,
					func() func() {
						r.ledger.InvalidateFastLoadCheckpointEligibility()
						return r.nodeFamily.BeginPrune()
					},
				)
				if err := context.Cause(ctx); err != nil {
					return err
				}
				r.rotator.Start()
				r.serverLog.Info("Online delete enabled",
					"online_delete", r.appConfig.NodeDB.OnlineDelete,
					"advisory_delete", r.appConfig.NodeDB.IsAdvisoryDeleteEnabled())
			} else {
				r.serverLog.Warn("online_delete configured but node store backend does not support pruning")
			}
		}
	}
	r.retentionFloor = func() uint32 {
		floor := uint32(r.appConfig.NodeDB.EarliestSeq)
		if r.rotator != nil && r.rotator.MinimumOnline() > floor {
			floor = r.rotator.MinimumOnline()
		}
		return floor
	}
	if r.appConfig.NodeDB.EarliestSeq > 0 || r.rotator != nil {
		r.ledger.SetMinimumOnlineFunc(r.retentionFloor)
		if r.nodeFamily != nil {
			r.nodeFamily.SetMinimumLedgerSeq(r.retentionFloor())
		}
	}

	// TxQ metrics are available in both standalone and consensus modes,
	// so wire the server_info hook before the consensus branch.
	ledgerSvcRef := r.ledger
	r.services.TxQMetrics = func() types.TxQServerMetrics {
		m := ledgerSvcRef.TxQMetrics()
		return types.TxQServerMetrics{
			ReferenceFeeLevel:     m.ReferenceFeeLevel,
			MinProcessingFeeLevel: m.MinProcessingFeeLevel,
			OpenLedgerFeeLevel:    m.OpenLedgerFeeLevel,
		}
	}
	r.services.TxQFeeMetrics = func() types.TxQFeeMetrics {
		m := ledgerSvcRef.TxQMetrics()
		return types.TxQFeeMetrics{
			TxCount:               m.TxCount,
			TxQMaxSize:            m.TxQMaxSize,
			TxInLedger:            m.TxInLedger,
			TxPerLedger:           m.TxPerLedger,
			ReferenceFeeLevel:     m.ReferenceFeeLevel,
			MinProcessingFeeLevel: m.MinProcessingFeeLevel,
			MedFeeLevel:           m.MedFeeLevel,
			OpenLedgerFeeLevel:    m.OpenLedgerFeeLevel,
		}
	}
	r.services.QueueAccountTxs = func(account [20]byte) []types.QueuedTxInfo {
		return queuedTxInfos(ledgerSvcRef.QueueAccountTxs(account))
	}
	r.services.QueueAllTxs = func() []types.QueuedTxInfo {
		return queuedTxInfos(ledgerSvcRef.QueueAllTxs())
	}

	// get_counts surfaces node-store I/O counters and locally-held
	// transactions. Available in both standalone and consensus modes since it
	// only needs the ledger service.
	r.services.GetCounts = func() types.CountsResult {
		c := ledgerSvcRef.Counts()
		res := types.CountsResult{
			Standalone: c.Standalone,
			LocalTxs:   c.LocalTxs,
		}
		if c.NodeStore != nil {
			res.NodeStore = &types.NodeStoreCounts{
				Reads:      c.NodeStore.Reads,
				FetchHits:  c.NodeStore.FetchHits,
				Writes:     c.NodeStore.Writes,
				ReadBytes:  c.NodeStore.ReadBytes,
				WriteBytes: c.NodeStore.WriteBytes,
			}
		}
		if c.FullBelow != nil {
			res.FullBelow = &types.FullBelowCounts{
				Size: c.FullBelow.Size, TargetSize: c.FullBelow.TargetSize,
				Hits: c.FullBelow.Hits, Misses: c.FullBelow.Misses,
				Inserts: c.FullBelow.Inserts, Evictions: c.FullBelow.Evictions,
				Sweeps: c.FullBelow.Sweeps,
			}
		}
		return res
	}

	// LoadFactorFees surfaces the local/net/cluster fee factors that
	// drive the admin-only human-mode load_factor_local / load_factor_net /
	// load_factor_cluster emissions (NetworkOPs.cpp:2887-2901). Net here
	// mirrors rippled's "remote" axis — LoadFeeTrack stores it under
	// remoteFee_. The closure re-reads on every server_info call so the
	// hook tracks live tracker state without rewiring.
	r.services.LoadFactorFees = func() types.LoadFactorFees {
		ft := ledgerSvcRef.FeeTrack()
		if ft == nil {
			base := uint32(256)
			return types.LoadFactorFees{Local: base, Net: base, Cluster: base}
		}
		return types.LoadFactorFees{
			Local:   ft.LocalFee(),
			Net:     ft.RemoteFee(),
			Cluster: ft.ClusterFee(),
		}
	}
	r.services.IsLoadedCluster = func() bool {
		ft := ledgerSvcRef.FeeTrack()
		return ft != nil && ft.IsLoadedCluster()
	}
	r.services.IsLoadedLocal = func() bool {
		ft := ledgerSvcRef.FeeTrack()
		return ft != nil && ft.IsLoadedLocal()
	}

	// Background ledger-integrity verification requires the same durable SHAMap
	// family used by the ledger service. A private in-memory fallback cannot
	// verify persisted ledger contents and would report misleading failures.
	if r.nodeFamily != nil {
		r.cleanerSource = &ledgerCleanerSource{svc: ledgerSvcRef, family: r.nodeFamily}
		r.cleaner = cleaner.New(r.cleanerSource, r.rootLogger)
		if err := context.Cause(ctx); err != nil {
			return err
		}
		r.cleaner.Start()

		cleanerRef := r.cleaner
		r.services.LedgerCleanerConfigure = func(p types.LedgerCleanerParams) types.LedgerCleanerStatus {
			return toCleanerStatus(cleanerRef.Clean(cleaner.Params{
				Ledger:     p.Ledger,
				MinLedger:  p.MinLedger,
				MaxLedger:  p.MaxLedger,
				Full:       p.Full,
				CheckNodes: p.CheckNodes,
				FixTxns:    p.FixTxns,
				Stop:       p.Stop,
			}))
		}
	}
	return nil
}

func (r *nodeRuntime) configureStandaloneNodeIdentity() error {
	if !r.standalone {
		return nil
	}
	dataDir := r.appConfig.LocalStateDir()
	if dataDir != "" {
		dataDir = filepath.Join(dataDir, "peers")
	}
	identity, err := peermanagement.LoadOrCreateIdentity(dataDir)
	if err != nil {
		return fmt.Errorf("load node identity: %w", err)
	}
	r.services.NodePublicKey = identity.EncodedPublicKey()
	return nil
}

func (r *nodeRuntime) configureConsensus() error {
	ctx := r.ctx
	if r.standalone {
		r.resourceManager = resource.NewManager(nil, nil)
		r.ownsResourceManager = true
	}
	if !r.standalone {
		var compErr error
		var validationRepo relationaldb.ValidationRepository
		if r.repo != nil {
			validationRepo = r.repo.Validation()
		}
		// Pass the online-delete floor to consensus so acquisition and
		// peer-serving refuse ledgers below the deletion boundary. Keep the
		// interface nil when rotation is off so the disabled path is unchanged
		// (a typed-nil *Rotator would be a non-nil interface).
		var floor adaptor.MinimumOnlineFloor
		if r.appConfig.NodeDB.EarliestSeq > 0 || r.rotator != nil {
			floor = minimumOnlineFloorFunc(r.retentionFloor)
		}
		r.consensus, compErr = adaptor.NewFromConfig(ctx, r.appConfig, r.ledger, validationRepo, floor)
		if compErr != nil {
			return fmt.Errorf("create consensus components: %w", compErr)
		}
		r.resourceManager = r.consensus.Overlay.ResourceManager()
		if r.rotator != nil {
			ageThreshold := 60 * time.Second
			if r.appConfig.NodeDB.AgeThresholdSeconds > 0 {
				ageThreshold = time.Duration(r.appConfig.NodeDB.AgeThresholdSeconds) * time.Second
			}
			recoveryWait := 5 * time.Second
			if r.appConfig.NodeDB.RecoveryWaitSeconds > 0 {
				recoveryWait = time.Duration(r.appConfig.NodeDB.RecoveryWaitSeconds) * time.Second
			}
			r.rotator.SetHealthCheck(func() bool {
				if r.consensus.Adaptor.GetOperatingMode() != consensus.OpModeFull {
					return false
				}
				validated := r.ledger.GetValidatedLedger()
				return validated != nil && time.Since(validated.CloseTime()) <= ageThreshold
			}, recoveryWait)
		}

		// Back inbound acquisitions with the node store before Start launches the
		// router loop, so the family is published before any acquisition reads it
		// (issue #1158).
		if r.nodeFamily != nil {
			if router := r.consensus.Router; router != nil {
				router.SetAcquisitionFamily(r.nodeFamily)
			}
		}

		if router := r.consensus.Router; router != nil && r.cleanerSource != nil {
			r.cleanerSource.SetReacquire(func(ctx context.Context, hash [32]byte, seq uint32) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				_, started, _ := router.RequestLedger(hash, seq)
				if !started {
					return fmt.Errorf("ledger_cleaner: unable to acquire ledger %d", seq)
				}
				return nil
			})
		}

		// Wire transaction relay: when a tx is submitted via RPC,
		// broadcast it to peers. LocalTxs holding is handled inside
		// service.SubmitTransaction so the broadcaster only relays.
		overlay := r.consensus.Overlay

		// Closed-Ledger / Previous-Ledger hints (Handshake.cpp:219-223).
		overlay.SetLedgerHintProvider(func() (peermanagement.LedgerHints, bool) {
			cl := r.ledger.GetClosedLedger()
			if cl == nil {
				return peermanagement.LedgerHints{}, false
			}
			return peermanagement.LedgerHints{Closed: cl.Hash(), Parent: cl.ParentHash()}, true
		})

		overlay.SetValidLedgerProvider(func() (uint32, time.Duration, bool) {
			vl := r.ledger.GetValidatedLedger()
			if vl == nil {
				return 0, 0, false
			}
			age := time.Since(vl.CloseTime())
			return vl.Sequence(), age, true
		})
		broadcastTx := newTxBroadcaster(overlay)
		r.ledgerAdapter.SetTxBroadcaster(broadcastTx)
		// Wire OpenLedger.Accept's relay callback so recovered txs are
		// re-broadcast post-LCL (rippled OpenLedger.cpp:120-150).
		r.ledger.SetTxRelay(broadcastTx)

		// Wire the authoritative transaction-cache lookup used by the
		// tx-reduce-relay reply path (TMGetObjectByHash{otTRANSACTIONS} →
		// TMTransactions reply). It includes queued transactions, not just
		// the current open-ledger view.
		// Feature-gated downstream by Config.EnableTxReduceRelay; the
		// providers themselves are always wired so a flip of the
		// config flag doesn't require a restart-and-rewire.
		overlay.SetTxRecordProvider(func(hash [32]byte) (peermanagement.TxRecord, bool) {
			blob, included, deferred, ok := r.ledger.TransactionForRelay(hash)
			if !ok {
				return peermanagement.TxRecord{}, false
			}
			status := message.TxStatusNew
			if included {
				status = message.TxStatusCurrent
			}
			return peermanagement.TxRecord{
				RawTransaction: blob,
				Status:         status,
				Deferred:       deferred,
			}, true
		})
		overlay.SetOpenLedgerHashesProvider(r.ledger.OpenLedgerTxHashes)

		// Wire the generic node-object lookup used by the
		// TMGetObjectByHash by-hash serve path (PeerImp.cpp:2483-2538).
		// Only wired when a node store is configured; an in-memory
		// deployment leaves the provider nil and the serve path drops
		// the request without charging.
		if r.nodeStore != nil {
			overlay.SetNodeObjectProvider(func(hash [32]byte) ([]byte, bool) {
				node, err := r.nodeStore.Fetch(ctx, nodestore.Hash256(hash))
				if err != nil || node == nil {
					return nil, false
				}
				return node.Data, true
			})
		}

		// LoadFeeTrack ingress + outbound self-load advertisement.
		// Mirrors the rippled wiring split:
		//   - PeerImp.cpp:1193 setClusterFee(median) on inbound TMCluster
		//   - NetworkOPs.cpp:1189-1195 self-entry sources getLocalFee()
		//     only while the validated ledger is at most four minutes old
		if ft := r.ledger.FeeTrack(); ft != nil {
			overlay.SetClusterFeeSink(ft.SetClusterFee)
			overlay.SetLocalLoadFeeProvider(func() (uint32, time.Duration) {
				return ft.LocalFee(), r.ledger.GetValidatedLedgerAge()
			})
		}

		r.services.NodePublicKey = r.consensus.Overlay.NodePublicKey()
		engine := r.consensus.Engine
		r.services.LastCloseInfo = func() (int, int) {
			proposers, convergeTime := engine.GetLastCloseInfo()
			return proposers, int(convergeTime.Milliseconds())
		}
		// Expose live consensus-round state to the `consensus_info` RPC
		// (rippled NetworkOPs::getConsensusInfo → RCLConsensus::getJson).
		r.services.ConsensusInfo = engine.GetJSON
		// Expose the live consensus quorum to the `server_info` RPC so
		// operators see the actual quorum (recomputed by the adaptor
		// from UNL ∖ negative-UNL) instead of the hardcoded "1" that
		// the bootstrap-time field used to return — #451.
		r.services.ValidationQuorum = r.consensus.Adaptor.GetQuorum

		// Peer-disconnect counters and the operating-mode state-accounting
		// snapshot need the overlay/adaptor, so they live inside the
		// consensus branch. (TxQMetrics is wired above; it only needs
		// the ledger service.)
		overlayRef := r.consensus.Overlay
		r.services.PeerDisconnects = func() (uint64, uint64) {
			return overlayRef.PeerDisconnects(), overlayRef.PeerDisconnectsResources()
		}
		// jq_trans_overflow folds the two sequential stages where a
		// saturated inbound transaction is shed: the overlay ingress gate
		// (max_transactions ceiling) and the consensus worker pool
		// (Router.DroppedTxJobs). A frame is shed by at most one stage, so
		// summing the disjoint counts reports the total without double-counting
		// and mirrors rippled's single jq_trans_overflow counter.
		routerRef := r.consensus.Router
		r.services.JqTransOverflow = func() uint64 {
			n := overlayRef.DroppedTransactions()
			if routerRef != nil {
				n += routerRef.DroppedTxJobs()
			}
			return n
		}
		r.services.TxReduceRelayMetrics = func() types.TxReduceRelayMetrics {
			s := overlayRef.TxMetricsSnapshot()
			return types.TxReduceRelayMetrics{
				TxCnt:           s.TxCnt,
				TxSz:            s.TxSz,
				HaveTxCnt:       s.HaveTxCnt,
				HaveTxSz:        s.HaveTxSz,
				GetLedgerCnt:    s.GetLedgerCnt,
				GetLedgerSz:     s.GetLedgerSz,
				LedgerDataCnt:   s.LedgerDataCnt,
				LedgerDataSz:    s.LedgerDataSz,
				TransactionsCnt: s.TransactionsCnt,
				TransactionsSz:  s.TransactionsSz,
				SelectedCnt:     s.SelectedCnt,
				SuppressedCnt:   s.SuppressedCnt,
				NotEnabledCnt:   s.NotEnabledCnt,
				MissingTxFreq:   s.MissingTxFreq,
			}
		}
		// Expose the overlay's peer-reservation table to the admin
		// peer_reservations_* RPCs (nil when no data dir is configured).
		if reservations := overlayRef.PeerReservations(); reservations != nil {
			r.services.PeerReservationAdd = func(nodePublic, description string) (string, bool, error) {
				prev, err := reservations.Insert(&peermanagement.PeerReservation{NodeID: nodePublic, Description: description})
				if prev != nil {
					return prev.Description, true, err
				}
				return "", false, err
			}
			r.services.PeerReservationDel = func(nodePublic string) (string, bool, error) {
				prev, err := reservations.Erase(nodePublic)
				if prev != nil {
					return prev.Description, true, err
				}
				return "", false, err
			}
			r.services.PeerReservationList = func() []types.PeerReservationEntry {
				list := reservations.List()
				out := make([]types.PeerReservationEntry, 0, len(list))
				for _, r := range list {
					out = append(out, types.PeerReservationEntry{NodePublic: r.NodeID, Description: r.Description})
				}
				return out
			}
		}
		r.peerConnectScheduler = newPeerConnectScheduler(
			r.ctx,
			overlayRef.ConnectContext,
			defaultPeerConnectWorkers,
			defaultPeerConnectQueue,
			func(addr string, err error) {
				r.serverLog.Warn("Peer connection attempt failed", "addr", addr, "err", err)
			},
		)
		r.services.PeerConnect = r.peerConnectScheduler.Enqueue
		r.services.ResourceBlacklist = overlayRef.BlacklistJSON
		acctRef := r.consensus.Adaptor
		r.services.StateAccounting = func() types.StateAccountingSnapshot {
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
		r.services.CloseTimeOffset = acctRef.CloseOffset

		// Expose the router's inbound-ledger acquisition tracker to the
		// fetch_info RPC (rippled InboundLedgers). Populated by the live
		// sync path; empty until the node is actively acquiring.
		if router := r.consensus.Router; router != nil {
			r.services.FetchInfo = router.FetchInfo
			r.services.FetchInfoClear = router.ClearFetchInfo
			r.services.RequestLedger = router.RequestLedger
			r.services.FetchPackCacheSize = router.FetchPackCacheSize
			r.services.FastSyncMetrics = func() types.FastSyncMetrics {
				snapshot := router.FastSyncMetrics()
				return types.FastSyncMetrics{
					CompletionRecheckAccepted:            snapshot.CompletionRecheckAccepted,
					CompletionRecheckRejectedNoEvidence:  snapshot.CompletionRecheckRejectedNoEvidence,
					CompletionRecheckRejectedBelowQuorum: snapshot.CompletionRecheckRejectedBelowQuorum,
					CompletionRecheckRejectedUnavailable: snapshot.CompletionRecheckRejectedUnavailable,
					TargetSuperseded:                     snapshot.TargetSuperseded,
					ObsoleteAcquisitionCompleted:         snapshot.ObsoleteAcquisitionCompleted,
					ReplayPipelineRequested:              snapshot.ReplayPipelineRequested,
					ReplayPipelineReady:                  snapshot.ReplayPipelineReady,
					ReplayPipelineApplied:                snapshot.ReplayPipelineApplied,
					ReplayPipelineDiscarded:              snapshot.ReplayPipelineDiscarded,
					ReplayPipelineRetried:                snapshot.ReplayPipelineRetried,
					ReplayPipelineFallbacks:              snapshot.ReplayPipelineFallbacks,
					ReplayPipelineCapacityRetargets:      snapshot.ReplayPipelineCapacityRetargets,
					ReplayPipelineBackpressureEvents:     snapshot.ReplayPipelineBackpressureEvents,
					ReplayPipelineRetargetFailures:       snapshot.ReplayPipelineRetargetFailures,
					ReplayPipelineAcquireUs:              snapshot.ReplayPipelineAcquireUs,
					ReplayPipelineReadyWaitUs:            snapshot.ReplayPipelineReadyWaitUs,
					ReplayPipelineApplyUs:                snapshot.ReplayPipelineApplyUs,
					ReplayPipelinePersistUs:              snapshot.ReplayPipelinePersistUs,
					ReplayPipelineWindow:                 snapshot.ReplayPipelineWindow,
					ReplayPipelineDepth:                  snapshot.ReplayPipelineDepth,
					ReplayPipelineReadyDepth:             snapshot.ReplayPipelineReadyDepth,
					ReplayPipelineHeadSeq:                snapshot.ReplayPipelineHeadSeq,
					ReplayPipelineTargetSeq:              snapshot.ReplayPipelineTargetSeq,
					ReplayPipelinePreparedLimit:          snapshot.ReplayPipelinePreparedLimit,
					ReplayPipelinePivotSeq:               snapshot.ReplayPipelinePivotSeq,
					ReplayPipelinePreparedTailSeq:        snapshot.ReplayPipelinePreparedTailSeq,
					ReplayPipelineTrustedHeadSeq:         snapshot.ReplayPipelineTrustedHeadSeq,
					ReplayPipelineGeneration:             snapshot.ReplayPipelineGeneration,
					ReplayPipelinePivotStateNodesPerSec:  snapshot.ReplayPipelinePivotStateNodesPerSec,
					ReplayPipelineHeadBlockedUs:          snapshot.ReplayPipelineHeadBlockedUs,
				}
			}
		}

		// Expose the validator-manifest cache to the `manifest` RPC.
		// The cache is shared — the router writes inbound manifests,
		// the engine reads for ephemeral→master translation, and this
		// RPC reads for external queries.
		r.services.Manifests = r.consensus.ValidatorManifests

		// Expose the publisher-list aggregator (when configured) to
		// the `validators` and `validator_list_sites` RPC methods.
		// nil-safe: NewRPCReader returns an inert reader when the
		// aggregator is nil, so the handlers return empty arrays in
		// that case rather than panicking.
		r.services.ValidatorList = validatorlist.NewRPCReader(r.consensus.ValidatorList)

		// Surface UNL-blocked state (validator list expired) so conditionMet
		// can return rpcEXPIRED_VALIDATOR_LIST, mirroring rippled's
		// NetworkOPs::isUNLBlocked. Only when a publisher list is configured.
		if r.consensus.ValidatorList != nil {
			r.services.UNLBlocked = r.consensus.ValidatorList.IsUNLBlocked
		}

		// Expose static config validators, cached signing keys, and the
		// negative-UNL set to the `validators` RPC so it returns the
		// same shape rippled's ValidatorList::getJson does.
		//
		// Bind to the live accessor (not a boot-time copy) so a SIGHUP
		// reload of the [validators] stanza is visible to the RPC.
		componentsRef := r.consensus
		adaptorRef := r.consensus.Adaptor
		r.services.LocalStaticTrustedKeysBase58 = func() []string {
			return validatorKeysBase58(componentsRef.StaticTrustedMasterKeys())
		}
		r.services.TrustedValidatorKeysBase58 = func() []string {
			return validatorKeysBase58(adaptorRef.GetTrustedMasterKeys())
		}
		if mc := r.consensus.ValidatorManifests; mc != nil {
			// Mirrors rippled getJson at ValidatorList.cpp:1726-1734 —
			// `signing_keys` only surfaces master→signing pairs for
			// masters present in keyListings_, i.e. validators listed
			// by at least one publisher, pinned in the local
			// [validators] stanza, or used as the local identity. Without
			// this filter we would leak every gossiped manifest, including
			// ones unrelated to any trusted publisher.
			vlAgg := r.consensus.ValidatorList
			r.services.SigningKeysBase58 = func() map[string]string {
				snap := mc.MasterToSigning()
				if len(snap) == 0 {
					return nil
				}
				listed := make(map[[33]byte]struct{})
				for _, mk := range adaptorRef.GetTrustedMasterKeys() {
					listed[mk] = struct{}{}
				}
				if vlAgg != nil {
					for master := range snap {
						if vlAgg.IsMasterListed(master) {
							listed[master] = struct{}{}
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
					mEnc, masterOK := validatorKeyBase58(master)
					sEnc, signingOK := validatorKeyBase58(signing)
					if masterOK && signingOK {
						out[mEnc] = sEnc
					}
				}
				return out
			}
		}
		r.services.NegativeUNLBase58 = func() []string {
			masters := adaptorRef.GetNegativeUNLMasters()
			if len(masters) == 0 {
				return nil
			}
			return validatorKeysBase58(masters)
		}

		// Expose the local validator's 33-byte signing public key to
		// validator_info / server_info. Mirrors rippled's
		// getValidationPublicKey gate: empty means the server is not
		// configured as a validator and the handlers return "not a
		// validator" / "none". GetValidatorKey returns the 20-byte
		// NodeID, NOT the public key — copying it into a 33-byte slice
		// zero-padded the last 13 bytes and produced a bogus key.
		if pk, err := r.consensus.Adaptor.GetValidatorSigningKey(); err == nil {
			r.services.ValidatorPublicKey = append([]byte(nil), pk[:]...)
		}

		isValidator := r.appConfig.IsValidator()
		r.serverLog.Info("Running in consensus mode",
			"validator", isValidator,
			"peers", len(r.appConfig.IPs)+len(r.appConfig.IPsFixed),
		)
	} else {
		genesisAddr, _ := r.ledger.GetGenesisAccount()
		r.serverLog.Info("Running in standalone mode",
			"genesisAccount", genesisAddr,
			"validatedLedger", r.ledger.GetValidatedLedgerIndex(),
			"openLedger", r.ledger.GetCurrentLedgerIndex(),
		)
	}
	return nil
}

func (r *nodeRuntime) bindRPC() error {
	if r.resourceManager == nil {
		r.resourceManager = resource.NewManager(nil, nil)
		r.ownsResourceManager = true
	}
	if r.services == nil {
		return errors.New("bind RPC: service graph builder is unavailable")
	}
	r.services.ResourceBlacklist = r.resourceManager.BlacklistJSON
	var peerSource types.PeerSource
	if r.consensus != nil && r.consensus.Overlay != nil {
		peerSource = r.consensus.Overlay
	}
	manager := subscription.NewManager()
	r.services.Shutdown = r.shutdowner
	r.services.SubscriptionMetrics = manager.Metrics
	ledgerInfo := &ledgerInfoAdapter{ledgerService: r.ledger}
	graph, err := r.services.Build()
	if err != nil {
		return fmt.Errorf("build RPC service graph: %w", err)
	}
	r.serviceGraph = graph
	urlSubscriptions := rpc.NewURLSubscriptionService(manager, graph, ledgerInfo)

	// Create HTTP JSON-RPC server. The dispatch timeout stays strictly below
	// the transport WriteTimeout (see httpWriteTimeout) so a timed-out request
	// can still serialize its error envelope.
	r.httpServer = rpc.NewServer(rpc.ServerOptions{
		Timeout:          rpcDispatchTimeout,
		Services:         graph,
		ResourceManager:  r.resourceManager,
		PeerSource:       peerSource,
		URLSubscriptions: urlSubscriptions,
	})

	pingInterval := time.Duration(r.appConfig.WebsocketPingFrequency) * time.Second
	r.wsServer = rpc.NewWebSocketServer(rpc.WebSocketServerOptions{
		Timeout:             rpcDispatchTimeout,
		Services:            graph,
		ResourceManager:     r.resourceManager,
		PeerSource:          peerSource,
		PingInterval:        pingInterval,
		LedgerInfoProvider:  ledgerInfo,
		SubscriptionManager: manager,
		URLSubscriptions:    urlSubscriptions,
	})

	r.publisher = rpc.NewPublisher(r.wsServer.SubscriptionManager())
	return nil
}

func (r *nodeRuntime) bindStreams() error {
	// Wire each WebSocket event source to its upstream publisher. Each call
	// mirrors a rippled pubXxx feed (NetworkOPs.cpp), so subscribed streams
	// receive the corresponding ledger and network events.
	if r.consensus != nil && r.consensus.Overlay != nil {
		// pubPeerStatus → peer_status (NetworkOPs.cpp:2514-2540).
		r.consensus.Overlay.SetPeerStatusPublisher(func(u peermanagement.PeerStatusUpdate) {
			r.publisher.PublishPeerStatus(&rpc.PeerStatusEvent{
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
		if r.consensus.ValidatorManifests != nil {
			r.consensus.ValidatorManifests.SubscribeAccepted(func(m *manifest.Manifest) {
				publishManifestIfSubscribed(r.publisher, m)
			})
		}

		// pubValidation + pubConsensus → validations / consensus
		// (NetworkOPs.cpp:2380-2510). One subscriber on the engine's
		// event bus, fanning the typed events out to the publisher.
		// The manifest cache feeds master_key resolution for
		// pubValidation (NetworkOPs.cpp:2434-2438).
		if r.consensus.Engine != nil {
			r.consensus.Engine.Subscribe(&rpcEventBridge{
				publisher: r.publisher,
				manifests: r.consensus.ValidatorManifests,
				networkID: r.networkID,
			})
		}
	}

	r.ledger.SetSubmittedTxCallback(func(ev service.SubmittedTxEvent) {
		r.publisher.PublishProposedTransaction(
			buildProposedTxEvent(ev),
			ev.AffectedAccounts,
		)
	})

	serverStatus := newServerStatusPublisher(r.serviceGraph, r.publisher)
	r.ledger.SetServerStatusCallback(serverStatus.publish)
	if feeTrack := r.ledger.FeeTrack(); feeTrack != nil {
		feeTrack.SetOnChange(func() {
			r.ledger.SignalServerStatusPublication(serverStatus.statusPublication(nil))
		})
	}
	if r.consensus != nil && r.consensus.Adaptor != nil {
		r.consensus.Adaptor.SetOnOperatingModeChange(func(mode consensus.OperatingMode) {
			r.ledger.SignalServerStatusPublication(serverStatus.modePublication(mode.String()))
		})
	}

	r.ledger.SetEventSink(service.EventSinkFunc(func(event *service.LedgerAcceptedEvent) error {
		if event == nil || event.LedgerInfo == nil {
			return nil
		}
		publications, bookTransactions, err := prepareAcceptedPublications(event, r.networkID)
		if err != nil {
			return err
		}

		// Drive online-delete rotation off the validated-ledger advance. The
		// callback fires from both the standalone accept path and the
		// consensus SetValidatedLedger path, so the rotator sees every
		// validated sequence. Notify never blocks.
		if r.rotator != nil {
			r.rotator.Notify(event.LedgerInfo.Sequence)
		}

		serverInfo := r.ledger.GetServerInfo()
		ledgerCloseEvent := buildLedgerCloseEvent(event, serverInfo)
		if ledgerCloseEvent == nil {
			return fmt.Errorf("accepted ledger event %d has no source ledger", event.LedgerInfo.Sequence)
		}
		r.publisher.PublishLedgerClosed(ledgerCloseEvent)

		// pubBookChanges → book_changes aggregate stream
		// (Subscribe.cpp:139-142 + NetworkOPs.cpp:3160-3174). Feed the
		// already-closed ledger view directly from the event so a slow
		// adapter store cannot drop the announce when the ledger isn't
		// yet visible to GetLedgerBySequence.
		bookView := newAcceptedLedgerView(*event.LedgerInfo)
		payload, err := handlers.ComputeBookChangesFromTransactionsStrict(bookView, bookTransactions)
		if err != nil {
			return fmt.Errorf("compute accepted ledger book changes: %w", err)
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal accepted ledger book changes: %w", err)
		}
		r.wsServer.SubscriptionManager().BroadcastToStream(types.SubBookChanges, data)

		for _, publication := range publications {
			r.publisher.PublishTransaction(publication.event, publication.projection.affectedAccounts)
			pairs := acceptedOrderBookPairs(publication)
			if len(pairs) != 0 {
				r.publisher.PublishOrderBookChange(publication.event, pairs)
			}
		}

		serverStatus.publish(nil)

		r.wsServer.UpdatePathFindSessions(func() (types.LedgerStateView, error) {
			return r.serviceGraph.LedgerViews().GetClosedLedgerView()
		})

		r.serverLog.Debug("Broadcasted ledger",
			"sequence", event.LedgerInfo.Sequence,
			"txs", len(event.TransactionResults),
		)
		return nil
	}))
	return nil
}

func (r *nodeRuntime) configureWatchdog() error {
	if r.standalone || !r.appConfig.Watchdog.IsEnabled() {
		return nil
	}
	warn, fatal, abort, err := r.appConfig.Watchdog.Thresholds()
	if err != nil {
		return fmt.Errorf("configure watchdog: %w", err)
	}
	wd, err := watchdog.New(warn, fatal, abort, nil)
	if err != nil {
		return fmt.Errorf("configure watchdog: %w", err)
	}
	if r.consensus == nil || r.consensus.Engine == nil {
		return errors.New("configure watchdog: consensus heartbeat is unavailable")
	}
	target, ok := r.consensus.Engine.(stallPinger)
	if !ok {
		return fmt.Errorf("configure watchdog: consensus engine %T does not support stall heartbeats", r.consensus.Engine)
	}
	registration, err := wd.Register("consensus")
	if err != nil {
		return fmt.Errorf("configure watchdog: %w", err)
	}
	target.SetStallPing(registration.Ping)
	r.watchdog = wd
	r.stopWatchdog = func() {
		target.SetStallPing(nil)
		registration.Close()
		wd.Stop()
	}
	return nil
}

func (r *nodeRuntime) bindTransports() error {
	ctx := r.ctx

	if err := context.Cause(ctx); err != nil {
		return err
	}
	transports, err := bindRPCTransports(
		ctx,
		r.serverLog,
		r.appConfig,
		r.httpServer,
		r.wsServer,
		r.ledger,
		r.resourceManager,
		systemListen,
	)
	r.transports = transports
	if err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return nil
}

func (r *nodeRuntime) start() error {
	ctx := r.ctx
	if r.ownsResourceManager {
		r.resourceManager.Start()
	}
	if r.consensus != nil {
		if err := r.consensus.Start(ctx); err != nil {
			return fmt.Errorf("start consensus components: %w", err)
		}
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := r.transports.serve(r.serverLog); err != nil {
		return err
	}
	if r.transports.grpc != nil {
		r.serverLog.Info("gRPC server started", "name", r.transports.grpc.name, "addr", r.transports.grpc.address)
	}

	if r.watchdog != nil {
		if err := r.watchdog.Start(ctx); err != nil {
			return fmt.Errorf("start watchdog: %w", err)
		}
	}
	return nil
}

func (r *nodeRuntime) wait() error {
	ctx := r.ctx
	var componentErrCh <-chan error
	if r.consensus != nil {
		componentErrCh = r.consensus.Errors()
	}
	if r.options.Ready != nil {
		r.options.Ready()
	}
	return waitForShutdown(
		ctx,
		r.serverLog,
		r.options.Reload,
		r.shutdownCh,
		r.transports.errors,
		componentErrCh,
		r.ledger.Errors(),
		r.consensus,
		r.configPath,
	)
}

func (r *nodeRuntime) stopRuntime() {
	if r.stopWatchdog != nil {
		r.stopWatchdog()
	}
	if r.stopSampler != nil {
		r.stopSampler()
	}
	r.cancel(nil)
	if r.options.Stopping != nil {
		r.options.Stopping()
	}
}

const (
	nodeShutdownNonCheckpointGrace = time.Minute
	transportShutdownGrace         = 5 * time.Second
	producerShutdownGrace          = 5 * time.Second
	serviceShutdownGrace           = 10 * time.Second
	storeShutdownGrace             = 5 * time.Second
)

func (r *nodeRuntime) shutdown() error {
	return r.shutdownWithin(nodeShutdownTimeoutFor(r.appConfig.ResolvedCheckpointShutdownGrace()))
}

func nodeShutdownTimeoutFor(checkpointGrace time.Duration) time.Duration {
	const maxDuration = time.Duration(1<<63 - 1)
	if checkpointGrace > maxDuration-nodeShutdownNonCheckpointGrace {
		return maxDuration
	}
	return checkpointGrace + nodeShutdownNonCheckpointGrace
}

func (r *nodeRuntime) shutdownWithin(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var errs []error

	transportsStopped, err := shutdownTransports(ctx, r.transports, r.wsServer, r.serverLog)
	if err != nil {
		errs = append(errs, err)
	}
	if !transportsStopped {
		errs = append(errs, errors.New("shutdown incomplete: dependencies left running because transport handlers did not stop"))
		return errors.Join(errs...)
	}
	if r.consensus != nil && r.consensus.Router != nil {
		legacy, replay := r.consensus.Router.StopAcquisitions()
		if legacy > 0 || replay > 0 {
			r.serverLog.Info("Ledger acquisitions drained before producer shutdown",
				"legacy_in_flight_at_stop", legacy,
				"replay_in_flight_at_stop", replay,
			)
		}
	}

	producersStopped := true
	// Storage-backed producers can be finishing a cold read when canceled.
	// Let them drain within the overall shutdown budget; the old five-second
	// cap could abandon a producer just before it stopped, skipping the durable
	// checkpoint even with a generous configured checkpoint grace.
	if r.rotator != nil {
		completed, err := runShutdownPhase(ctx, "stop online delete rotator", func() error {
			r.rotator.Stop()
			return nil
		})
		producersStopped = producersStopped && completed
		if err != nil {
			errs = append(errs, err)
		} else {
			r.serverLog.Info("Online delete rotator stopped")
		}
	}
	if r.cleaner != nil {
		completed, err := runShutdownPhase(ctx, "stop ledger cleaner", func() error {
			r.cleaner.Stop()
			return nil
		})
		producersStopped = producersStopped && completed
		if err != nil {
			errs = append(errs, err)
		} else {
			r.serverLog.Info("Ledger cleaner stopped")
		}
	}
	if r.peerConnectScheduler != nil {
		completed, err := runShutdownPhaseWithin(ctx, producerShutdownGrace, "stop peer connect scheduler", func() error {
			r.peerConnectScheduler.Close()
			return nil
		})
		producersStopped = producersStopped && completed
		if err != nil {
			errs = append(errs, err)
		} else {
			r.serverLog.Info("Peer connect scheduler stopped")
		}
		if !completed {
			errs = append(errs, errors.New("shutdown incomplete: consensus left running because peer connect scheduler did not stop"))
			return errors.Join(errs...)
		}
	}
	if r.consensus != nil {
		completed, err := runShutdownPhase(ctx, "stop consensus components", r.consensus.Stop)
		producersStopped = producersStopped && completed
		if err != nil {
			errs = append(errs, err)
			if completed && r.consensus.Archive != nil {
				archiveCtx, cancelArchive := context.WithTimeout(context.Background(), producerShutdownGrace)
				terminalErr := r.consensus.Archive.Close(archiveCtx)
				cancelArchive()
				if terminalErr != nil && !errors.Is(err, terminalErr) {
					errs = append(errs, fmt.Errorf("close validation archive: %w", terminalErr))
					r.serverLog.Warn("Validation archive terminal shutdown failed", "err", terminalErr)
				}
			}
		}
		if r.consensus.Archive != nil {
			health := r.consensus.Archive.Health()
			if !health.Healthy {
				r.serverLog.Warn("Validation archive unhealthy at shutdown",
					"enqueued", health.Enqueued,
					"overload_dropped", health.OverloadDropped,
					"closed_dropped", health.ClosedDropped,
					"malformed_dropped", health.MalformedDropped,
					"persistence_dropped", health.PersistenceDropped,
					"write_failures", health.WriteFailures,
					"retention_failures", health.RetentionFailures,
					"last_error", health.LastError,
				)
			}
		}
		if completed {
			r.serverLog.Info("Consensus components stopped")
		}
	}
	if r.ownsResourceManager && r.resourceManager != nil {
		r.resourceManager.Stop()
		r.serverLog.Info("Resource manager stopped")
	}

	if !producersStopped {
		errs = append(errs, errors.New("shutdown incomplete: stores left open because producers did not stop"))
		return errors.Join(errs...)
	}

	serviceStopped := true
	if r.ledger != nil {
		completed, err := runShutdownPhase(ctx, "stop ledger service", func() error {
			r.ledger.Stop()
			return nil
		})
		serviceStopped = completed
		if err != nil {
			errs = append(errs, err)
		} else {
			r.serverLog.Info("Ledger service persistence drained")
		}
	}
	if !serviceStopped {
		errs = append(errs, errors.New("shutdown incomplete: stores left open because ledger service did not stop"))
		return errors.Join(errs...)
	}
	prepareCheckpoint := r.prepareFastLoadCheckpoint
	if prepareCheckpoint == nil && r.ledger != nil {
		prepareCheckpoint = r.ledger.PrepareFastLoadCheckpoint
	}
	if len(errs) == 0 && prepareCheckpoint != nil {
		// Proof collection performs uncached random reads over the shallow
		// SHAMap frontier. On a cold or busy store it can legitimately take
		// longer than the small timeout used to close an already-flushed DB.
		checkpointGrace := r.appConfig.ResolvedCheckpointShutdownGrace()
		checkpointCtx, cancelCheckpoint := context.WithTimeout(ctx, checkpointGrace)
		checkpointDeadline, _ := checkpointCtx.Deadline()
		r.serverLog.Info("Fast-load checkpoint preparation started",
			"grace", checkpointGrace.String(),
			"deadline", checkpointDeadline,
		)
		var prepared bool
		completed, err := runShutdownPhase(checkpointCtx, "prepare fast-load checkpoint", func() error {
			var prepareErr error
			prepared, prepareErr = prepareCheckpoint(checkpointCtx)
			return prepareErr
		})
		cancelCheckpoint()
		if err != nil {
			errs = append(errs, err)
			message := "Fast-load checkpoint preparation failed"
			if errors.Is(err, context.DeadlineExceeded) {
				message = "Fast-load checkpoint preparation expired"
			}
			r.serverLog.Warn(message,
				"grace", checkpointGrace.String(),
				"deadline", checkpointDeadline,
				"err", err,
			)
		}
		if !completed {
			errs = append(errs, errors.New("shutdown incomplete: stores left open because fast-load checkpoint preparation did not stop"))
			return errors.Join(errs...)
		}
		if err == nil {
			if prepared {
				r.serverLog.Info("Fast-load checkpoint preparation complete", "prepared", true)
			} else {
				r.serverLog.Info("Fast-load checkpoint preparation skipped", "prepared", false)
			}
		}
	}

	if r.nodeStore != nil {
		_, err := runShutdownPhaseWithin(ctx, storeShutdownGrace, "close node store", r.nodeStore.Close)
		if err != nil {
			errs = append(errs, err)
		}
	}
	if r.repo != nil {
		_, err := runShutdownPhaseWithin(ctx, storeShutdownGrace, "close relational database", r.repo.Close)
		if err != nil {
			errs = append(errs, err)
		}
	}

	shutdownErr := errors.Join(errs...)
	if shutdownErr != nil {
		r.serverLog.Warn("Shutdown completed with errors", "err", shutdownErr)
	} else {
		r.serverLog.Info("Shutdown complete")
	}
	return shutdownErr
}

func runShutdownPhase(ctx context.Context, name string, stop func() error) (bool, error) {
	if err := context.Cause(ctx); err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	result := make(chan error, 1)
	go func() { result <- stop() }()
	select {
	case err := <-result:
		if err != nil {
			return true, fmt.Errorf("%s: %w", name, err)
		}
		return true, nil
	case <-ctx.Done():
		return false, fmt.Errorf("%s: %w", name, context.Cause(ctx))
	}
}

func runShutdownPhaseWithin(
	ctx context.Context,
	timeout time.Duration,
	name string,
	stop func() error,
) (bool, error) {
	phaseCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return runShutdownPhase(phaseCtx, name, stop)
}

func shutdownTransports(
	ctx context.Context,
	transports *boundRPCTransports,
	wsServer *rpc.WebSocketServer,
	logger xrpllog.Logger,
) (bool, error) {
	var errs []error
	var servers []*http.Server
	if transports != nil {
		transports.stopRequests()
		for _, bound := range transports.http {
			servers = append(servers, bound.server)
		}
		for _, bound := range transports.ws {
			servers = append(servers, bound.server)
		}
	}

	type shutdownResult struct {
		name string
		err  error
	}
	graceCtx, cancelGrace := context.WithTimeout(ctx, transportShutdownGrace)
	defer cancelGrace()

	resultCount := len(servers)
	if wsServer != nil {
		resultCount++
	}
	if transports != nil && transports.grpc != nil {
		resultCount++
	}
	results := make(chan shutdownResult, resultCount)
	for _, server := range servers {
		go func() {
			results <- shutdownResult{
				name: "drain HTTP server " + server.Addr,
				err:  server.Shutdown(graceCtx),
			}
		}()
	}
	if wsServer != nil {
		go func() {
			results <- shutdownResult{name: "close WebSocket sessions", err: wsServer.Close(graceCtx)}
		}()
	}
	if transports != nil && transports.grpc != nil {
		logger.Info("Draining gRPC connections...")
		go func() {
			transports.grpc.server.GracefulStop()
			results <- shutdownResult{name: "drain gRPC server"}
		}()
	}

	graceful := true
	for remaining := resultCount; remaining > 0; remaining-- {
		select {
		case result := <-results:
			if result.err != nil {
				graceful = false
				errs = append(errs, fmt.Errorf("%s: %w", result.name, result.err))
			}
		case <-graceCtx.Done():
			graceful = false
			errs = append(errs, fmt.Errorf("drain transports: %w", context.Cause(graceCtx)))
			remaining = 0
		}
	}

	if !graceful {
		for _, server := range servers {
			if err := server.Close(); err != nil {
				errs = append(errs, fmt.Errorf("force close HTTP server %s: %w", server.Addr, err))
			}
		}
		if transports != nil && transports.grpc != nil {
			transports.grpc.server.Stop()
		}
	}
	if transports != nil {
		if err := transports.closeListeners(); err != nil {
			errs = append(errs, fmt.Errorf("close transport listeners: %w", err))
		}
	}

	type joinResult struct {
		name string
		err  error
	}
	joinCount := 0
	joined := make(chan joinResult, 3)
	joinCtx, cancelJoin := context.WithTimeout(ctx, transportShutdownGrace)
	defer cancelJoin()
	if transports != nil {
		joinCount += 2
		go func() {
			transports.wait()
			joined <- joinResult{name: "join transport servers"}
		}()
		go func() {
			transports.waitRequests()
			joined <- joinResult{name: "join transport handlers"}
		}()
	}
	if wsServer != nil {
		joinCount++
		go func() {
			joined <- joinResult{name: "join WebSocket sessions", err: wsServer.Close(joinCtx)}
		}()
	}
	complete := true
	for remaining := joinCount; remaining > 0; remaining-- {
		select {
		case result := <-joined:
			if result.err != nil {
				complete = false
				errs = append(errs, fmt.Errorf("%s: %w", result.name, result.err))
			}
		case <-joinCtx.Done():
			complete = false
			errs = append(errs, fmt.Errorf("join transports: %w", context.Cause(joinCtx)))
			remaining = 0
		}
	}
	return complete, errors.Join(errs...)
}

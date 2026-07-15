package node

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

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/addresscodec"
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
	"github.com/LeJamon/go-xrpl/internal/rpc"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
	"github.com/LeJamon/go-xrpl/internal/watchdog"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/LeJamon/go-xrpl/storage/relationaldb/postgres"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	googlegrpc "google.golang.org/grpc"
)

type minimumOnlineFloorFunc func() uint32

func (f minimumOnlineFloorFunc) MinimumOnline() uint32 { return f() }

// Run assembles and starts every node subsystem from the parsed config, then
// blocks until a terminating signal or fatal error. It is the composition root
// extracted from the CLI so flag parsing and node wiring stay separable.
func Run(appConfig *config.Config, configPath string, standalone bool, rootLogger, serverLog xrpllog.Logger) error {
	if err := validateTrustedValidatorConfig(appConfig, standalone); err != nil {
		return err
	}

	var err error
	// Pre-declared so the deferred shutdown can clean up whatever the
	// init path managed to populate before any error return. doShutdown
	// tolerates nil components for the partial-init case.
	var (
		db                  nodestore.Database
		repoManager         relationaldb.RepositoryManager
		ledgerService       *service.Service
		ledgerCleaner       *cleaner.Cleaner
		consensusComponents *adaptor.Components
		rotator             *shamapstore.Rotator
		httpSrvs            []*http.Server
		wsSrvs              []*http.Server
		wsServer            *rpc.WebSocketServer
		grpcSrv             *googlegrpc.Server
	)
	defer func() {
		doShutdown(httpSrvs, wsSrvs, wsServer, grpcSrv, ledgerService, ledgerCleaner, consensusComponents, rotator, db, repoManager, serverLog)
	}()

	db, repoManager, err = setupStorage(appConfig, serverLog)
	if err != nil {
		return err
	}
	var nodeFamily *shamap.NodeStoreFamily
	if db != nil {
		nodeFamily = shamap.NewNodeStoreFamily(db)
	}

	// Load genesis configuration from config file path (if set)
	genesisFile := appConfig.GenesisFile
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
		if appConfig.GenesisAmendmentsDisabled {
			genesisConfig.Amendments = nil
		}
		serverLog.Info("Genesis config using built-in defaults")
	}

	// Get network ID from config
	networkID, err := appConfig.ResolvedNetworkID()
	if err != nil {
		return fmt.Errorf("get network ID: %w", err)
	}

	// Build the live amendment table from the operator's [amendments] config.
	// One instance is shared between the ledger service (which folds validated
	// flag ledgers into it) and the consensus adaptor (which sources vote
	// stances from it).
	amendmentTable := buildTable(appConfig.Amendments, repoManager, serverLog)

	// Build the transaction-queue config from the operator's
	// [transaction_queue] stanza layered over the rippled defaults.
	txqCfg, err := service.TxQConfigFromTuning(appConfig.TransactionQueue, standalone)
	if err != nil {
		return err
	}

	// Initialize ledger service
	cfg := service.Config{
		Standalone:   standalone,
		NetworkID:    uint32(networkID),
		NodeStore:    db,
		SHAMapFamily: nodeFamily,
		FastLoad:     appConfig.NodeDB.FastLoad,
		RelationalDB: repoManager,
		Logger:       rootLogger,
		Table:        amendmentTable,
		TxQ:          &txqCfg,
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

	// Gate the beta RPC API (api_version 3) on the operator's beta_rpc_api
	// knob, mirroring rippled Config::BETA_RPC_API.
	services.BetaRPCAPI = appConfig.BetaRPCAPI != 0

	// Advisory-delete state (can_delete RPC). Available in both standalone and
	// consensus modes; gated by node_db advisory_delete and persisted under
	// database_path. Mirrors rippled's SHAMapStore advisory-delete state.
	if advisoryStore, asErr := shamapstore.New(
		appConfig.NodeDB.IsAdvisoryDeleteEnabled(),
		appConfig.LocalStateDir(),
	); asErr != nil {
		if appConfig.NodeDB.IsOnlineDeleteEnabled() {
			return fmt.Errorf("load online-delete state: %w", asErr)
		}
		serverLog.Warn("Failed to load advisory-delete state", "err", asErr)
	} else {
		services.AdvisoryDeleteState = advisoryStore

		// Online-delete rotation: when node_db online_delete is set and the
		// node store can enumerate its keyspace, run a background job that
		// reclaims disk by deleting complete ledgers below the rotation
		// boundary. NewRotator returns nil when online_delete is off.
		if appConfig.NodeDB.IsOnlineDeleteEnabled() {
			if prunable, ok := db.(shamapstore.NodePruner); ok {
				var relPruner shamapstore.RelationalPruner
				if repoManager != nil {
					relPruner = relationaldb.NewLedgerPruner(repoManager, appConfig.NodeDB.DeleteBatch)
				}
				rotator = shamapstore.NewRotator(
					advisoryStore,
					prunable,
					relPruner,
					shamapstore.RotationConfig{
						DeleteInterval: uint32(appConfig.NodeDB.OnlineDelete),
						DeleteBatch:    appConfig.NodeDB.DeleteBatch,
					},
					serverLog,
				)
				minimumOnline := rotator.MinimumOnline()
				if minimumOnline == 0 && repoManager != nil {
					minSeq, minErr := repoManager.Ledger().GetMinLedgerSeq(context.Background())
					if minErr != nil {
						return fmt.Errorf("load online-delete minimum ledger: %w", minErr)
					}
					if minSeq != nil {
						minimumOnline = uint32(*minSeq)
						if err := rotator.SetMinimumOnlineFloor(minimumOnline); err != nil {
							return fmt.Errorf("persist online-delete minimum ledger: %w", err)
						}
					}
				}
				if minimumOnline > 0 {
					nodeFamily.SetMinimumLedgerSeq(minimumOnline)
				}
				rotator.SetStateRefresh(
					ledgerService.RefreshValidatedState,
					nodeFamily.SetMinimumLedgerSeq,
					nodeFamily.BeginPrune,
				)
				rotator.Start()
				serverLog.Info("Online delete enabled",
					"online_delete", appConfig.NodeDB.OnlineDelete,
					"advisory_delete", appConfig.NodeDB.IsAdvisoryDeleteEnabled())
			} else {
				serverLog.Warn("online_delete configured but node store backend does not support pruning")
			}
		}
	}
	retentionFloor := func() uint32 {
		floor := uint32(appConfig.NodeDB.EarliestSeq)
		if rotator != nil && rotator.MinimumOnline() > floor {
			floor = rotator.MinimumOnline()
		}
		return floor
	}
	if appConfig.NodeDB.EarliestSeq > 0 || rotator != nil {
		ledgerService.SetMinimumOnlineFunc(retentionFloor)
		if nodeFamily != nil {
			nodeFamily.SetMinimumLedgerSeq(retentionFloor())
		}
	}

	// TxQ metrics are available in both standalone and consensus modes,
	// so wire the server_info hook before the !standalone branch.
	ledgerSvcRef := ledgerService
	services.TxQMetrics = func() types.TxQServerMetrics {
		m := ledgerSvcRef.TxQMetrics()
		return types.TxQServerMetrics{
			ReferenceFeeLevel:     m.ReferenceFeeLevel,
			MinProcessingFeeLevel: m.MinProcessingFeeLevel,
			OpenLedgerFeeLevel:    m.OpenLedgerFeeLevel,
		}
	}
	services.TxQFeeMetrics = func() types.TxQFeeMetrics {
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
	services.QueueAccountTxs = func(account [20]byte) []types.QueuedTxInfo {
		return queuedTxInfos(ledgerSvcRef.QueueAccountTxs(account))
	}
	services.QueueAllTxs = func() []types.QueuedTxInfo {
		return queuedTxInfos(ledgerSvcRef.QueueAllTxs())
	}

	// get_counts surfaces node-store I/O counters and locally-held
	// transactions. Available in both standalone and consensus modes since it
	// only needs the ledger service.
	services.GetCounts = func() types.CountsResult {
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
		return res
	}

	// LoadFactorFees surfaces the local/net/cluster fee factors that
	// drive the admin-only human-mode load_factor_local / load_factor_net /
	// load_factor_cluster emissions (NetworkOPs.cpp:2887-2901). Net here
	// mirrors rippled's "remote" axis — LoadFeeTrack stores it under
	// remoteFee_. The closure re-reads on every server_info call so the
	// hook tracks live tracker state without rewiring.
	services.LoadFactorFees = func() types.LoadFactorFees {
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

	// Background ledger-integrity verifier (admin ledger_cleaner). rippled keeps
	// this subsystem present in every instance (Application always constructs
	// and starts its LedgerCleaner); mirror that by always wiring it, falling
	// back to an in-memory content-addressed family when no persistent node
	// store is configured (standalone / RPC-only). The RPC's own availability
	// is then gated on network/sync state, as in rippled, not on storage.
	var cleanerFamily shamap.Family
	if nodeFamily != nil {
		cleanerFamily = nodeFamily
	} else {
		memFamily := shamap.NewMemoryNodeStoreFamily()
		cleanerFamily = memFamily
	}
	ledgerCleaner = cleaner.New(&ledgerCleanerSource{svc: ledgerSvcRef, family: cleanerFamily}, rootLogger)
	ledgerCleaner.Start()

	cleanerRef := ledgerCleaner
	services.LedgerCleanerConfigure = func(p types.LedgerCleanerParams) types.LedgerCleanerStatus {
		return toCleanerStatus(cleanerRef.Clean(cleaner.Params{
			Ledger:     p.Ledger,
			MinLedger:  p.MinLedger,
			MaxLedger:  p.MaxLedger,
			Full:       p.Full,
			CheckNodes: p.CheckNodes,
			Stop:       p.Stop,
		}))
	}
	services.LedgerCleanerStatusFn = func() types.LedgerCleanerStatus {
		return toCleanerStatus(cleanerRef.Status())
	}

	// Start consensus/networking if not in standalone mode
	if !standalone {
		var compErr error
		var validationRepo relationaldb.ValidationRepository
		if repoManager != nil {
			validationRepo = repoManager.Validation()
		}
		// Pass the online-delete floor to consensus so acquisition and
		// peer-serving refuse ledgers below the deletion boundary. Keep the
		// interface nil when rotation is off so the disabled path is unchanged
		// (a typed-nil *Rotator would be a non-nil interface).
		var floor adaptor.MinimumOnlineFloor
		if appConfig.NodeDB.EarliestSeq > 0 || rotator != nil {
			floor = minimumOnlineFloorFunc(retentionFloor)
		}
		consensusComponents, compErr = adaptor.NewFromConfig(appConfig, ledgerService, validationRepo, floor)
		if compErr != nil {
			return fmt.Errorf("create consensus components: %w", compErr)
		}
		if rotator != nil {
			ageThreshold := 60 * time.Second
			if appConfig.NodeDB.AgeThresholdSeconds > 0 {
				ageThreshold = time.Duration(appConfig.NodeDB.AgeThresholdSeconds) * time.Second
			}
			recoveryWait := 5 * time.Second
			if appConfig.NodeDB.RecoveryWaitSeconds > 0 {
				recoveryWait = time.Duration(appConfig.NodeDB.RecoveryWaitSeconds) * time.Second
			}
			rotator.SetHealthCheck(func() bool {
				if consensusComponents.Adaptor.GetOperatingMode() != consensus.OpModeFull {
					return false
				}
				validated := ledgerService.GetValidatedLedger()
				return validated != nil && time.Since(validated.CloseTime()) <= ageThreshold
			}, recoveryWait)
		}

		// Back inbound acquisitions with the node store before Start launches the
		// router loop, so the family is published before any acquisition reads it
		// (issue #1158).
		if nodeFamily != nil {
			if router := consensusComponents.Router; router != nil {
				router.SetAcquisitionFamily(nodeFamily)
			}
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
		broadcastTx := newTxBroadcaster(overlay)
		ledgerAdapter.SetTxBroadcaster(broadcastTx)
		// Wire OpenLedger.Accept's relay callback so recovered txs are
		// re-broadcast post-LCL (rippled OpenLedger.cpp:120-150).
		ledgerService.SetTxRelay(broadcastTx)

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

		// Wire the generic node-object lookup used by the
		// TMGetObjectByHash by-hash serve path (PeerImp.cpp:2483-2538).
		// Only wired when a node store is configured; an in-memory
		// deployment leaves the provider nil and the serve path drops
		// the request without charging.
		if db != nil {
			overlay.SetNodeObjectProvider(func(hash [32]byte) ([]byte, bool) {
				node, err := db.Fetch(context.Background(), nodestore.Hash256(hash))
				if err != nil || node == nil {
					return nil, false
				}
				return node.Data, true
			})
		}

		// LoadFeeTrack ingress + outbound self-load advertisement.
		// Mirrors the rippled wiring split:
		//   - PeerImp.cpp:1193 setClusterFee(median) on inbound TMCluster
		//   - NetworkOPs.cpp:1126-1132 self-entry sources getLocalFee()
		if ft := ledgerSvcRef.FeeTrack(); ft != nil {
			overlay.SetClusterFeeSink(ft.SetClusterFee)
			overlay.SetLocalLoadFeeProvider(ft.LocalFee)
		}

		// Expose node identity and consensus stats to RPC handlers.
		services.NodePublicKey = consensusComponents.Overlay.Identity().EncodedPublicKey()
		engine := consensusComponents.Engine
		services.LastCloseInfo = func() (int, int) {
			proposers, convergeTime := engine.GetLastCloseInfo()
			return proposers, int(convergeTime.Milliseconds())
		}
		// Expose live consensus-round state to the `consensus_info` RPC
		// (rippled NetworkOPs::getConsensusInfo → RCLConsensus::getJson).
		services.ConsensusInfo = engine.GetJSON
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
		// jq_trans_overflow folds the two sequential stages where a
		// saturated inbound transaction is shed: the overlay ingress gate
		// (max_transactions ceiling) and the consensus worker pool
		// (Router.DroppedTxJobs). A frame is shed by at most one stage, so
		// summing the disjoint counts reports the total without double-counting
		// and mirrors rippled's single jq_trans_overflow counter.
		routerRef := consensusComponents.Router
		services.JqTransOverflow = func() uint64 {
			n := overlayRef.DroppedTransactions()
			if routerRef != nil {
				n += routerRef.DroppedTxJobs()
			}
			return n
		}
		services.TxReduceRelayMetrics = func() types.TxReduceRelayMetrics {
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
			services.PeerReservationAdd = func(nodePublic, description string) (string, bool, error) {
				prev, err := reservations.Insert(&peermanagement.PeerReservation{NodeID: nodePublic, Description: description})
				if prev != nil {
					return prev.Description, true, err
				}
				return "", false, err
			}
			services.PeerReservationDel = func(nodePublic string) (string, bool, error) {
				prev, err := reservations.Erase(nodePublic)
				if prev != nil {
					return prev.Description, true, err
				}
				return "", false, err
			}
			services.PeerReservationList = func() []types.PeerReservationEntry {
				list := reservations.List()
				out := make([]types.PeerReservationEntry, 0, len(list))
				for _, r := range list {
					out = append(out, types.PeerReservationEntry{NodePublic: r.NodeID, Description: r.Description})
				}
				return out
			}
		}
		services.PeerConnect = overlayRef.Connect
		services.ResourceBlacklist = overlayRef.BlacklistJSON
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

		// Expose the router's inbound-ledger acquisition tracker to the
		// fetch_info RPC (rippled InboundLedgers). Populated by the live
		// sync path; empty until the node is actively acquiring.
		if router := consensusComponents.Router; router != nil {
			services.FetchInfo = router.FetchInfo
			services.FetchInfoClear = router.ClearFetchInfo
			services.RequestLedger = router.RequestLedger
		}

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

		// Surface UNL-blocked state (validator list expired) so conditionMet
		// can return rpcEXPIRED_VALIDATOR_LIST, mirroring rippled's
		// NetworkOPs::isUNLBlocked. Only when a publisher list is configured.
		if consensusComponents.ValidatorList != nil {
			services.UNLBlocked = consensusComponents.ValidatorList.IsUNLBlocked
		}

		// Expose static config validators, cached signing keys, and the
		// negative-UNL set to the `validators` RPC so it returns the
		// same shape rippled's ValidatorList::getJson does.
		//
		// Bind to the live accessor (not a boot-time copy) so a SIGHUP
		// reload of the [validators] stanza is visible to the RPC.
		componentsRef := consensusComponents
		adaptorRef := consensusComponents.Adaptor
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
		services.TrustedValidatorKeysBase58 = func() []string {
			masters := adaptorRef.GetTrustedMasterKeys()
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
			// by at least one publisher, pinned in the local
			// [validators] stanza, or used as the local identity. Without
			// this filter we would leak every gossiped manifest, including
			// ones unrelated to any trusted publisher.
			vlAgg := consensusComponents.ValidatorList
			services.SigningKeysBase58 = func() map[string]string {
				snap := mc.MasterToSigning()
				if len(snap) == 0 {
					return nil
				}
				listed := make(map[[33]byte]struct{})
				for _, mk := range adaptorRef.GetTrustedMasterKeys() {
					listed[mk] = struct{}{}
				}
				if vlAgg != nil {
					for _, p := range vlAgg.PublisherSnapshot() {
						if p.Status != validatorlist.StatusAvailable {
							continue
						}
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

		// Expose the local validator's 33-byte signing public key to
		// validator_info / server_info. Mirrors rippled's
		// getValidationPublicKey gate: empty means the server is not
		// configured as a validator and the handlers return "not a
		// validator" / "none". GetValidatorKey returns the 20-byte
		// NodeID, NOT the public key — copying it into a 33-byte slice
		// zero-padded the last 13 bytes and produced a bogus key.
		if pk, err := consensusComponents.Adaptor.GetValidatorSigningKey(); err == nil {
			services.ValidatorPublicKey = append([]byte(nil), pk[:]...)
		}

		isValidator := appConfig.IsValidator()
		serverLog.Info("Running in consensus mode",
			"validator", isValidator,
			"peers", len(appConfig.IPs)+len(appConfig.IPsFixed),
		)
	} else {
		genesisAddr, _ := ledgerService.GetGenesisAccount()
		serverLog.Info("Running in standalone mode",
			"genesisAccount", genesisAddr,
			"validatedLedger", ledgerService.GetValidatedLedgerIndex(),
			"openLedger", ledgerService.GetCurrentLedgerIndex(),
		)
	}

	// Create HTTP JSON-RPC server. The dispatch timeout stays strictly below
	// the transport WriteTimeout (see httpWriteTimeout) so a timed-out request
	// can still serialize its error envelope.
	httpServer := rpc.NewServer(rpcDispatchTimeout, services)
	if consensusComponents != nil && consensusComponents.Overlay != nil {
		httpServer.SetPeerSource(consensusComponents.Overlay)
	}

	services.SetDispatcher(httpServer)

	// Create WebSocket server for real-time subscriptions
	wsServer = rpc.NewWebSocketServer(rpcDispatchTimeout, services)
	if appConfig.WebsocketPingFrequency > 0 {
		wsServer.SetPingInterval(time.Duration(appConfig.WebsocketPingFrequency) * time.Second)
	}
	wsServer.RegisterAllMethods()
	if consensusComponents != nil && consensusComponents.Overlay != nil {
		wsServer.SetPeerSource(consensusComponents.Overlay)
	}

	// Create a ledger info provider adapter for WebSocket subscribe responses
	wsServer.SetLedgerInfoProvider(&ledgerInfoAdapter{ledgerService: ledgerService})

	publisher := rpc.NewPublisher(wsServer.SubscriptionManager())

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
		// The manifest cache feeds master_key resolution for
		// pubValidation (NetworkOPs.cpp:2434-2438).
		if consensusComponents.Engine != nil {
			consensusComponents.Engine.Subscribe(&rpcEventBridge{
				publisher: publisher,
				manifests: consensusComponents.Manifests,
				networkID: uint32(networkID),
			})
		}
	}

	ledgerService.SetSubmittedTxCallback(func(ev service.SubmittedTxEvent) {
		publisher.PublishProposedTransaction(
			buildProposedTxEvent(ev),
			ev.AffectedAccounts,
		)
	})

	// pubServer cache: rippled gates the serverStatus emit on the
	// ServerFeeSummary changing (NetworkOPs.cpp:3209-3225 reportFeeChange);
	// the server stream is silent in steady state. We track the
	// previous snapshot here so a constant-fee ledger run does not
	// flood subscribers.
	var lastServerSnapshot serverStatusSnapshot

	// Wire up ledger service events to WebSocket broadcasts
	ledgerService.SetEventCallback(func(event *service.LedgerAcceptedEvent) {
		if event == nil || event.LedgerInfo == nil {
			return
		}

		// Drive online-delete rotation off the validated-ledger advance. The
		// callback fires from both the standalone accept path and the
		// consensus SetValidatedLedger path, so the rotator sees every
		// validated sequence. Notify never blocks.
		if rotator != nil {
			rotator.Notify(event.LedgerInfo.Sequence)
		}

		baseFee, reserveBase, reserveInc := ledgerService.GetCurrentFees()

		ledgerTime := uint32(event.LedgerInfo.CloseTime.Unix() - protocol.RippleEpochUnix)

		ledgerCloseEvent := &rpc.LedgerCloseEvent{
			Type:             "ledgerClosed",
			LedgerIndex:      event.LedgerInfo.Sequence,
			LedgerHash:       upperHex(event.LedgerInfo.Hash[:]),
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
			txEvent, engineResult := buildValidatedTransactionEvent(txResult, event, uint32(networkID))
			publisher.PublishTransaction(txEvent, txResult.AffectedAccounts)

			// Per-book delivery is tesSUCCESS-only — rippled gates
			// getOrderBookDB().processTxn on the engine result
			// (NetworkOPs.cpp:3409-3410). Subscribers receive the
			// full tx + meta JSON, matching the transactions-stream
			// payload (rippled fans the same MultiApiJson into both).
			if engineResult != "tesSUCCESS" {
				continue
			}
			pairs := extractBookPairsFromTxData(txResult.TxData)
			if len(pairs) == 0 {
				continue
			}
			publisher.PublishOrderBookChange(txEvent, pairs)
		}

		// pubBookChanges → book_changes aggregate stream
		// (Subscribe.cpp:139-142 + NetworkOPs.cpp:3160-3174). Feed the
		// already-closed ledger view directly from the event so a slow
		// adapter store cannot drop the announce when the ledger isn't
		// yet visible to GetLedgerBySequence.
		bookView := newAcceptedLedgerView(event)
		payload := handlers.ComputeBookChanges(bookView)
		if data, err := json.Marshal(payload); err == nil {
			wsServer.SubscriptionManager().BroadcastToStream(types.SubBookChanges, data, nil)
		}

		// pubServer → server stream (NetworkOPs.cpp:2308-2373 +
		// 3209-3225 reportFeeChange). Diff-check against the previous
		// snapshot so a constant-fee ledger does not flood subscribers.
		// server_status is sourced from the live operating mode (the
		// same value server_info returns), not a hardcoded "full".
		load := handlers.ComputeServerLoad(services)
		serverStatus := "full"
		if info := services.Ledger.GetServerInfo(); info.ServerState != "" {
			serverStatus = info.ServerState
		}
		nextSnap := serverStatusSnapshot{
			baseFee:                 baseFee,
			loadBase:                load.LoadBase,
			loadFactor:              load.LoadFactor,
			loadFactorLocal:         load.LoadFactorLocal,
			loadFactorNet:           load.LoadFactorNet,
			loadFactorCluster:       load.LoadFactorCluster,
			loadFactorFeeEscalation: load.LoadFactorFeeEscalation,
			loadFactorFeeQueue:      load.LoadFactorFeeQueue,
			loadFactorFeeReference:  load.LoadFactorFeeReference,
			loadFactorServer:        load.LoadFactorServer,
			serverStatus:            serverStatus,
		}
		if nextSnap != lastServerSnapshot {
			lastServerSnapshot = nextSnap
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
				LoadFactorFeeReference:  int(load.LoadFactorFeeReference),
				LoadFactorServer:        int(load.LoadFactorServer),
				ServerStatus:            serverStatus,
			})
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

	var listenerErrCh chan error
	if httpSrvs, wsSrvs, listenerErrCh, err = startListeners(serverLog, appConfig, httpServer, wsServer); err != nil {
		return err
	}

	// Start the gRPC XRPLedgerAPIService listener when [port_grpc] is
	// configured. Disabled by default: no section → no listener (mirrors
	// rippled's GRPCServer). The ledger service already satisfies the
	// grpc.LedgerLookup surface the service implementation needs.
	if grpcName, grpcPort, hasGRPC := appConfig.GRPCPort(); hasGRPC {
		srv, addr, err := startGRPCServer(grpcName, grpcPort, ledgerService, serverLog, listenerErrCh)
		if err != nil {
			return fmt.Errorf("start grpc server: %w", err)
		}
		grpcSrv = srv
		serverLog.Info("gRPC server started", "name", grpcName, "addr", addr)
	}

	// Arm the out-of-band stall watchdog now that the server is up and
	// servicing its event loops. Mirrors rippled arming activateStallDetector
	// only at full start, and only outside standalone (ApplicationImp::run:
	// guarded by !config_->standalone()). Standalone closes ledgers solely on
	// the ledger_accept RPC, so an idle node produces no heartbeat and would
	// otherwise self-abort; consensus mode drives a periodic heartbeat. The
	// watchdog runs on its own goroutine and aborts the process if a monitored
	// loop wedges, so a deadlocked node screams and can be restarted instead of
	// going quiet.
	if !standalone && appConfig.Watchdog.IsEnabled() {
		wdCfg := watchdog.ConfigFromSeconds(
			appConfig.Watchdog.WarnSecondsResolved(),
			appConfig.Watchdog.FatalSecondsResolved(),
			appConfig.Watchdog.AbortSecondsResolved(),
		)
		wd := watchdog.New(wdCfg, nil)
		// Monitor only the tick-driven consensus loop: rippled's LoadManager
		// watches loop liveness, never ledger-close progress, so a catching-up
		// node self-heals via checkLedger rather than aborting.
		if consensusComponents != nil {
			if sp, ok := consensusComponents.Engine.(stallPinger); ok {
				sp.SetStallPing(wd.Register("consensus"))
			}
		}
		wdCtx, cancelWatchdog := context.WithCancel(context.Background())
		defer cancelWatchdog()
		go wd.Run(wdCtx)
		serverLog.Info("Stall watchdog armed",
			"warn_s", appConfig.Watchdog.WarnSecondsResolved(),
			"fatal_s", appConfig.Watchdog.FatalSecondsResolved(),
			"abort_s", appConfig.Watchdog.AbortSecondsResolved(),
		)
	}

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
		// Non-blocking: the main loop drains one value and returns, so a
		// second concurrent stop must not park its handler goroutine forever.
		select {
		case shutdownCh <- struct{}{}:
		default:
		}
	})

	return waitForShutdown(serverLog, sigCh, reloadCh, shutdownCh, listenerErrCh, consensusComponents, configPath)
}

func validateTrustedValidatorConfig(appConfig *config.Config, standalone bool) error {
	if standalone || len(appConfig.Validators.Validators) > 0 || len(appConfig.Validators.ValidatorListKeys) > 0 {
		return nil
	}
	return errors.New("trusted validator configuration is empty: configure validators or validator_list_keys, or use --standalone")
}

// waitForShutdown blocks until a terminating event arrives: an OS signal, an
// RPC stop, or a listener goroutine failure. SIGHUP is non-terminating — it
// reloads the trusted validator set in place and keeps waiting. It returns the
// listener error (if any) so the caller's deferred cleanup runs.

func waitForShutdown(
	log xrpllog.Logger,
	sigCh, reloadCh chan os.Signal,
	shutdownCh chan struct{},
	listenerErrCh chan error,
	consensusComponents *adaptor.Components,
	configPath string,
) error {
	for {
		select {
		case sig := <-sigCh:
			log.Info("Received signal, shutting down", "signal", sig)
			return nil
		case <-shutdownCh:
			return nil
		case err := <-listenerErrCh:
			log.Error("Listener failed — initiating shutdown", "err", err)
			return err
		case <-reloadCh:
			reloadTrustedValidators(log, consensusComponents, configPath)
		}
	}
}

// setupStorage initializes the node store (pebble or in-memory) and the
// optional relational DB (PostgreSQL or SQLite, used for transaction indexing)
// from config. A node-store backend failure is fatal; a relational-DB failure
// is logged and leaves indexing disabled (repoManager nil), as before.
func setupStorage(cfg *config.Config, log xrpllog.Logger) (nodestore.Database, relationaldb.RepositoryManager, error) {
	var db nodestore.Database
	nodestorePath := cfg.NodeDB.Path
	if nodestorePath != "" {
		store, err := kvpebble.New(nodestorePath, pebbleBlockCacheBytes, pebbleFileHandles, false)
		if err != nil {
			return nil, nil, fmt.Errorf("storage backend: %w", err)
		}
		cacheSize, cacheTTL := nodeStoreCacheParams(cfg.NodeDB, cfg.NodeSize)
		db = nodestore.NewKVDatabase(store, "pebble("+nodestorePath+")", cacheSize, cacheTTL)
		log.Info("Storage initialized", "backend", "pebble", "path", nodestorePath,
			"cache_size", cacheSize, "cache_age", cacheTTL)
	} else {
		log.Info("Storage initialized", "backend", "in-memory")
	}

	var repoManager relationaldb.RepositoryManager
	dbPath := cfg.DatabasePath
	if strings.HasPrefix(dbPath, "postgres://") || strings.HasPrefix(dbPath, "postgresql://") {
		pgConfig := relationaldb.NewConfig()
		pgConfig.ConnectionString = dbPath

		var err error
		repoManager, err = postgres.NewRepositoryManager(pgConfig)
		if err != nil {
			log.Warn("PostgreSQL not available", "err", err)
		} else {
			if err := repoManager.Open(context.Background()); err != nil {
				log.Warn("PostgreSQL connection failed", "err", err)
				repoManager = nil
			} else {
				log.Info("PostgreSQL connected", "purpose", "transaction indexing")
			}
		}
	} else if dbPath != "" {
		// Default: auto-create SQLite databases at the given directory
		// path, applying the operator's [sqlite] tuning.
		journalMode, synchronous, tempStore := cfg.SQLite.EffectiveSettings()
		var err error
		repoManager, err = sqlitedb.NewRepositoryManagerWithSettings(dbPath, sqlitedb.Settings{
			JournalMode:      journalMode,
			Synchronous:      synchronous,
			TempStore:        tempStore,
			PageSize:         cfg.SQLite.PageSize,
			JournalSizeLimit: cfg.SQLite.JournalSizeLimit,
		})
		if err != nil {
			log.Warn("SQLite failed to initialize", "path", dbPath, "err", err)
		} else {
			if err := repoManager.Open(context.Background()); err != nil {
				log.Warn("SQLite failed to open", "path", dbPath, "err", err)
				repoManager = nil
			} else {
				log.Info("SQLite connected", "path", dbPath, "purpose", "transaction indexing")
			}
		}
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
		encoded, err := message.Encode(txMsg)
		if err != nil {
			return
		}
		frame, err := message.BuildWireMessage(message.TypeTransaction, encoded)
		if err != nil {
			return
		}
		overlay.Broadcast(frame)
	}
}

// startListeners wires the shared HTTP mux (JSON-RPC at "/" and "/rpc", health
// at "/health") and starts one listener per configured HTTP and WebSocket port,
// each wrapped in its own PortMiddleware so admin/secure-gateway trust is scoped
// per port. ListenAndServe failures are funnelled into the returned channel so
// the caller's deferred cleanup runs. On a port-config error the partially
// started listeners are returned so the caller can still drain them.
func startListeners(
	log xrpllog.Logger,
	cfg *config.Config,
	httpServer http.Handler,
	wsServer *rpc.WebSocketServer,
) (httpSrvs, wsSrvs []*http.Server, listenerErrCh chan error, err error) {
	// Shared connection limiter for all ports. Seeded with a bounded process-wide
	// default so an unset per-port limit can't leave WS connections unbounded;
	// [server] max_connections overrides it (negative disables the global cap).
	connLimiter := rpc.NewConnLimiter()
	if cfg.Server.MaxConnections != 0 {
		connLimiter.SetGlobalLimit(cfg.Server.MaxConnections)
	}
	wsServer.SetConnLimiter(connLimiter)

	// Build the base HTTP mux (shared handler logic, wrapped per-port below)
	httpMux := http.NewServeMux()
	httpMux.Handle("/", httpServer)
	httpMux.Handle("/rpc", httpServer)
	httpMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","service":"go-xrpl"}`))
	})

	httpPorts := cfg.HTTPPorts()
	wsPorts := cfg.WebSocketPorts()

	for name, p := range httpPorts {
		log.Info("Port configured", "protocol", "http", "name", name, "addr", p.BindAddress())
	}
	for name, p := range wsPorts {
		log.Info("Port configured", "protocol", "ws", "name", name, "addr", p.BindAddress())
	}
	if _, peerPort, hasPeer := cfg.PeerPort(); hasPeer {
		log.Info("Port configured", "protocol", "peer", "addr", peerPort.BindAddress())
	}
	if _, grpcPort, hasGRPC := cfg.GRPCPort(); hasGRPC {
		log.Info("Port configured", "protocol", "grpc", "addr", grpcPort.BindAddress())
	}

	// listenerErrCh routes ListenAndServe failures back to the main
	// goroutine so shutdown runs the deferred cleanup chain. Sized for
	// every WS/HTTP listener plus the optional gRPC listener.
	listenerErrCh = make(chan error, 2+len(wsPorts)+len(httpPorts))

	// Start WebSocket listeners — each port gets its own mux with PortMiddleware
	for name, p := range wsPorts {
		pc, perr := parsePortConfig("ws", name, p)
		if perr != nil {
			return httpSrvs, wsSrvs, listenerErrCh, perr
		}
		mux := http.NewServeMux()
		mux.Handle("/", rpc.PortMiddleware(pc, connLimiter, wsServer))
		srv := &http.Server{Addr: p.BindAddress(), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		wsSrvs = append(wsSrvs, srv)
		go func(n string, s *http.Server) {
			log.Info("Listening", "protocol", "ws", "name", n, "addr", s.Addr)
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("WebSocket server failed", "name", n, "addr", s.Addr, "err", err)
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
		pc, perr := parsePortConfig("http", name, p)
		if perr != nil {
			return httpSrvs, wsSrvs, listenerErrCh, perr
		}
		httpPortList = append(httpPortList, struct {
			name string
			pc   *rpc.PortContext
			addr string
		}{name, pc, p.BindAddress()})
	}

	if len(httpPortList) == 0 {
		return httpSrvs, wsSrvs, listenerErrCh, fmt.Errorf("no HTTP ports configured — at least one HTTP port is required")
	}

	for _, entry := range httpPortList {
		wrappedMux := http.NewServeMux()
		wrappedMux.Handle("/", rpc.PortMiddleware(entry.pc, connLimiter, httpMux))
		srv := &http.Server{
			Addr:              entry.addr,
			Handler:           wrappedMux,
			ReadHeaderTimeout: httpReadHeaderTimeout,
			ReadTimeout:       httpReadTimeout,
			WriteTimeout:      httpWriteTimeout,
			IdleTimeout:       httpIdleTimeout,
		}
		httpSrvs = append(httpSrvs, srv)
		go func(n, addr string, s *http.Server) {
			log.Info("Listening", "protocol", "http", "name", n, "addr", addr)
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("HTTP server failed", "name", n, "addr", addr, "err", err)
				select {
				case listenerErrCh <- fmt.Errorf("http %s (%s): %w", n, addr, err):
				default:
				}
			}
		}(entry.name, entry.addr, srv)
	}

	return httpSrvs, wsSrvs, listenerErrCh, nil
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

// Pebble-internal tuning with no corresponding config key: the block
// cache for the storage engine itself and the max open file handles.
const (
	pebbleBlockCacheBytes = 256 << 20
	pebbleFileHandles     = 500
)

// Node-object cache defaults applied when the operator leaves node_db
// cache_size / cache_age unset.
const (
	defaultNodeCacheSize = 2_097_152
	defaultNodeCacheAge  = 90 * time.Minute
)

// nodeStoreCacheParams maps node_db cache_size (entries) and cache_age
// (minutes) onto the node-object cache parameters, substituting the
// built-in defaults for unset (zero) values.
func nodeStoreCacheParams(n config.NodeDBConfig, nodeSize string) (int, time.Duration) {
	profiles := map[string]struct {
		size int
		age  time.Duration
	}{
		"tiny":   {262_144, 30 * time.Minute},
		"small":  {524_288, 60 * time.Minute},
		"medium": {2_097_152, 90 * time.Minute},
		"large":  {4_194_304, 120 * time.Minute},
		"huge":   {8_388_608, 900 * time.Minute},
	}
	if nodeSize == "" {
		nodeSize = "medium"
	}
	profile, ok := profiles[nodeSize]
	if !ok {
		profile = profiles["medium"]
	}
	size := profile.size
	if n.CacheSize > 0 {
		size = n.CacheSize
	}
	age := profile.age
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
	return &rpc.PortContext{
		PortName:          name,
		AdminNets:         adminNets,
		AdminUser:         p.AdminUser,
		AdminPassword:     p.AdminPassword,
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

// stallPinger is the optional surface the stall watchdog installs on the
// consensus engine. Kept off the core consensus.Engine interface so test
// mocks and alternative engines need not implement it; *rcl.Engine satisfies
// it. Mirrors the optional-extension pattern of consensus.WireableAdaptor.
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
	cfg, err := config.LoadConfig(config.Paths{Main: configPath})
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
	grpcSrv *googlegrpc.Server,
	ledgerService *service.Service,
	ledgerCleaner *cleaner.Cleaner,
	consensusComponents *adaptor.Components,
	rotator *shamapstore.Rotator,
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

	if wsServer != nil {
		if err := wsServer.Close(ctx); err != nil {
			logger.Warn("WebSocket server shutdown timed out", "err", err)
		}
	}

	if grpcSrv != nil {
		logger.Info("Draining gRPC connections...")
		stopped := make(chan struct{})
		go func() {
			grpcSrv.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-ctx.Done():
			grpcSrv.Stop()
			logger.Warn("gRPC server graceful shutdown timed out; forced stop")
		}
	}

	if rotator != nil {
		rotator.Stop()
		logger.Info("Online delete rotator stopped")
	}

	if ledgerCleaner != nil {
		ledgerCleaner.Stop()
		logger.Info("Ledger cleaner stopped")
	}

	if consensusComponents != nil {
		consensusComponents.Stop()
		logger.Info("Consensus components stopped")
	}

	// Drain and join the persistence worker before closing its stores, so
	// queued ledger persists become durable instead of being
	// abandoned (and so no StoreBatch races kvDB.Close). Ordered after the
	// consensus components stop, so no new ledger closes past this point.
	if ledgerService != nil {
		ledgerService.Stop()
		logger.Info("Ledger service persistence drained")
	}
	if kvDB != nil {
		if err := kvDB.Close(); err != nil {
			logger.Warn("Node store close failed", "err", err)
		}
	}
	if repoManager != nil {
		if err := repoManager.Close(context.Background()); err != nil {
			logger.Warn("Relational DB close failed", "err", err)
		}
	}

	logger.Info("Shutdown complete")
}

// ledgerCleanerSource adapts the ledger service + node store to the
// cleaner.LedgerSource interface the ledger-integrity verifier consumes.

func buildTable(cfg config.AmendmentsConfig, repo relationaldb.RepositoryManager, log xrpllog.Logger) *amendment.Table {
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
	recs, err := repo.Amendment().LoadAmendmentVotes(context.Background())
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

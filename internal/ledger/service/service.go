package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/localtxs"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// Aliases to the canonical sentinels in svcerr — kept so existing
// callers within the service package read naturally; callers from
// outside MUST compare against svcerr.* directly.
var (
	ErrNotStandalone      = svcerr.ErrNotStandalone
	ErrNoOpenLedger       = svcerr.ErrNoOpenLedger
	ErrNoClosedLedger     = svcerr.ErrNoClosedLedger
	ErrLedgerNotFound     = svcerr.ErrLedgerNotFound
	ErrInvalidLedgerIndex = svcerr.ErrInvalidLedgerIndex
	ErrInvalidLedgerHash  = svcerr.ErrInvalidLedgerHash
	ErrTxnNotFound        = svcerr.ErrTxnNotFound
)

// Config holds configuration for the LedgerService
type Config struct {
	// Standalone indicates whether the node is running in standalone mode
	Standalone bool

	// NetworkID is the network identifier for this node.
	// Legacy networks (ID <= 1024) reject transactions that include NetworkID.
	// New networks (ID > 1024) require NetworkID in transactions.
	NetworkID uint32

	// GenesisConfig is the configuration for creating the genesis ledger
	GenesisConfig genesis.Config

	// NodeStore is the persistent storage for ledger nodes (optional, nil for in-memory only)
	NodeStore nodestore.Database

	// RelationalDB is the repository manager for transaction indexing (optional)
	RelationalDB relationaldb.RepositoryManager

	// Logger is the logger for the ledger service.
	// If nil, xrpllog.Discard() is used.
	Logger xrpllog.Logger

	// AmendmentTable, when supplied, is the live amendment table the service
	// folds each validated flag ledger into (enabled set + majority projection +
	// blocked state). Optional — nil disables amendment-table resync.
	AmendmentTable *amendment.AmendmentTable

	// TxQ optionally overrides the transaction-queue configuration
	// (built from the operator's [transaction_queue] stanza via
	// TxQConfigFromTuning). Nil means use txq.DefaultConfig — or
	// txq.StandaloneConfig in standalone mode.
	TxQ *txq.Config
}

// DefaultConfig returns the default service configuration
func DefaultConfig() Config {
	return Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     nil,
		RelationalDB:  nil,
		Logger:        xrpllog.Discard(),
	}
}

// Service manages the ledger lifecycle
type Service struct {
	// mu guards the Service's mutable ledger state. Lock ordering: when a
	// path needs both mu and the TxQ mutex (SubmitTransaction routing
	// through TxQ.Apply, and the consensus-close Accept/ProcessClosedLedger
	// paths), it MUST acquire mu before txQueue's mutex. TxQ methods never
	// reach back into the Service, so this single ordering rule is enough
	// to keep concurrent submit and consensus close deadlock-free.
	mu sync.RWMutex

	config Config
	logger xrpllog.Logger

	// NodeStore for persistent storage (nil if in-memory only)
	nodeStore nodestore.Database

	// RelationalDB for transaction indexing (nil if not configured)
	relationalDB relationaldb.RepositoryManager

	// amendmentTable is the live amendment table folded by each validated flag
	// ledger (nil disables resync). Has its own internal mutex.
	amendmentTable *amendment.AmendmentTable

	// Current open ledger (accepting transactions)
	openLedger *ledger.Ledger

	// Last closed ledger
	closedLedger *ledger.Ledger

	// Validated ledger (highest validated)
	validatedLedger *ledger.Ledger

	// Genesis ledger
	genesisLedger *ledger.Ledger

	// Ledger history (sequence -> ledger) - in-memory cache
	ledgerHistory map[uint32]*ledger.Ledger

	// By-hash index over ledgerHistory (ledger hash -> sequence). Kept in
	// sync exclusively by putHistoryLocked/deleteHistoryLocked.
	ledgerByHash map[[32]byte]uint32

	// Transaction index (hash -> ledger sequence) - in-memory cache
	txIndex map[[32]byte]uint32

	// Transaction position within its ledger (hash -> 0-based index)
	txPositionIndex map[[32]byte]uint32

	// Pending transactions accumulated during the open ledger phase.
	// Re-applied in canonical order at AcceptLedger time.
	// Reference: rippled CanonicalTXSet / retriableTxs
	pendingTxs []pendingTx

	// EventCallback is called when a ledger becomes validated by consensus.
	// Fires at quorum-gate time from SetValidatedLedger, not at close time,
	// so WebSocket subscribers see ledger_index advances in lockstep with
	// server_info.validated_ledger. Matches rippled's pubLedger semantics.
	eventCallback EventCallback

	// pendingValidation stashes LedgerAcceptedEvents by ledger hash at
	// close time so the eventCallback can fire later when the ledger
	// reaches trusted-validation quorum. Bounded — see pendingValidationMaxLen.
	pendingValidation map[[32]byte]*LedgerAcceptedEvent

	// pendingValidationOrder tracks insertion order for LRU eviction.
	pendingValidationOrder [][32]byte

	// pendingLedgerValidations stashes trusted-validation notifications
	// keyed by ledger *sequence* when SetValidatedLedger arrives ahead of
	// the peer-adoption of that seq. On every subsequent insertion into
	// ledgerHistory for a matching seq, the stash is drained and the
	// ledger promoted to validated if the hash matches and the entry
	// has not expired. Distinct from pendingValidation, which is keyed
	// by *hash* and stashes full accepted events — this map stashes
	// validation notifications in the opposite race (validation before
	// close/adopt, not close/adopt before validation).
	pendingLedgerValidations map[uint32]pendingValidationEntry

	// pendingLedgerValidationsOrder tracks insertion order for LRU
	// eviction of pendingLedgerValidations.
	pendingLedgerValidationsOrder []uint32

	// Invoked off-thread when SetValidatedLedger stashes a validation
	// for a seq beyond closed. Mirrors LedgerMaster::checkAccept
	// calling getInboundLedgers().acquire(hash, seq, ...).
	onPendingValidationStashed func(seq uint32, hash [32]byte)

	// heldAdoptions stashes replay-delta adoptions that arrived out of
	// order (child seq before parent seq). Keyed by the *awaited parent
	// seq* so a successful adopt at seq N can pop the child at seq N+1
	// in O(1) and cascade-adopt it without a second external trigger.
	//
	// Flat (single-hop) by design: replay-delta is single-ledger-per-
	// request, so multi-ledger backward walks are out of scope here
	// (tracked separately as D6). Multi-level chains of held children
	// do cascade via recursion at adopt time, bounded by
	// heldAdoptionCascadeMax to cap fork-storm recursion.
	//
	// Distinct from pendingValidation (hash-keyed accepted events) and
	// pendingLedgerValidations (seq-keyed validation notifications) —
	// this map holds the *ledger payload itself* awaiting its parent.
	heldAdoptions map[uint32]*pendingAdopt

	// hooks provides event callbacks for external subscribers
	hooks *EventHooks

	// needsInitialSync is true when the node is in consensus mode
	// and hasn't yet adopted a ledger from peers.
	needsInitialSync bool

	// serverStateFunc optionally provides the operating mode string for server_info.
	// Set by the consensus adaptor after startup.
	serverStateFunc func() string

	// minimumOnlineFunc optionally reports the online-delete retention floor
	// (rippled SHAMapStore::minimumOnline). When set, complete_ledgers is
	// clamped up to it so server_info never advertises ledgers online-delete
	// has reclaimed. Nil when online_delete is off — the range is then the
	// in-memory history window unchanged.
	minimumOnlineFunc func() uint32

	// openLedgerView is the persistent open-ledger view that mirrors
	// rippled's openLedger().current() — the source of truth for the
	// open pool (#407). Built by Start / rebuilt by adopt paths /
	// advanced incrementally by Accept on LCL transitions.
	openLedgerView *openledger.OpenLedger

	// txQueue is the transaction queue (mempool). Both ingress routes —
	// RPC submit (SubmitTransaction) and network relay (SubmitOpenLedgerTx)
	// — route each tx through txQueue.Apply via OpenLedger.SubmitDetailed,
	// which either applies directly to the open view or holds the tx in
	// the queue (terQUEUED). On LCL transitions AcceptConsensusResult
	// calls txQueue.ProcessClosedLedger to update fee metrics, and the
	// modifier passed to OpenLedger.Accept calls txQueue.Accept to promote
	// queued txs into the new open view.
	// Reference: rippled NetworkOPs.cpp:1518, OpenLedger.cpp:113.
	//
	// Lock ordering: txQueue has its own mutex acquired inside its methods.
	// Callers holding s.mu (submit + consensus close) acquire it after
	// s.mu; txQueue never reaches back for s.mu. See the mu field comment.
	txQueue *txq.TxQ

	// localTxs is the held pool of locally-submitted transactions, kept
	// alongside txQueue exactly as rippled keeps LocalTxs alongside TxQ
	// (NetworkOPs.cpp:1518 apply + NetworkOPs.cpp:1677 push_back). Both
	// SubmitTransaction (RPC) and SubmitOpenLedgerTx(local=true) push each
	// non-permanent result into the pool; acceptOpenLedgerViewLocked sweeps
	// stale entries against the new closed ledger and passes
	// localTxs.GetTxSet() as the `locals` argument to OpenLedger.Accept,
	// replaying them on top of every newly rebuilt open view until they
	// apply or age out.
	// Reference: rippled LocalTxs.{h,cpp}, RCLConsensus.cpp:662-674.
	localTxs *localtxs.LocalTxs

	// txRelay re-broadcasts a recovered tx blob to peers. Threaded into
	// OpenLedger.Accept's relay callback so post-LCL replayed txs get
	// re-propagated (rippled OpenLedger.cpp:120-150 calls
	// app.overlay().relay for each non-inner-batch tx surviving the
	// rebuild). Nil when overlay broadcast is unwired (tests).
	txRelay func(blob []byte)

	// submittedTxCallback fires from SubmitTransaction only when the tx
	// applied to the open ledger. Mirrors rippled NetworkOPs::processTransaction
	// (NetworkOPs.cpp:1535-1544) which calls pubProposedTransaction inside
	// the applied branch, feeding the transactions_proposed /
	// accounts_proposed WebSocket streams.
	submittedTxCallback SubmittedTxCallback

	// feeTrack is the local LoadFeeTrack mirror. Always non-nil — New()
	// constructs a fresh tracker so GetAutofillFee and server_info
	// observe the same fee factors as rippled's getCurrentNetworkFee
	// path (TransactionSign.cpp:849-862). Drivers:
	//   - Raise/LowerLocalFee fire once per ledger close via
	//     tickLoadFeeLocked (TxQ-escalation proxy for rippled's
	//     LoadManager.cpp:177-186 JobQueue overload signal).
	//   - SetRemoteFee fires from Adaptor.OnLedgerFullyValidated after
	//     quorum, taking the median across trusted-validation LoadFees
	//     (mirrors LedgerMaster.cpp:977-1006).
	//   - SetClusterFee fires from peermanagement.Overlay's TMCluster
	//     ingress via the clusterFeeSink hook wired in cli/server.go
	//     (mirrors PeerImp.cpp:1175-1193).
	feeTrack *feetrack.LoadFeeTrack

	// lastConsensusRoundTime is the wall-clock duration of the most
	// recent consensus round, populated by the consensus adaptor via
	// SetLastConsensusRoundTime. processClosedLedgerLocked converts
	// it to the TxQ's timeLeap flag (RCLConsensus.cpp:805 →
	// FeeMetrics::update). Zero in standalone or pre-startup.
	lastConsensusRoundTime time.Duration

	// stallPing, when set, fires once per ledger close so the out-of-band
	// stall watchdog can observe that the ledger-processing loop is making
	// progress. Carried via an atomic pointer so it can be installed after
	// construction without taking s.mu; nil disables it.
	stallPing atomic.Pointer[func()]

	// configCacheMu guards the memoised open-ledger ApplyConfig below. The
	// config is a pure function of closedLedger (fees, amendment rules,
	// sequence, parent close time) plus stable Service fields, so it is
	// rebuilt only when closedLedger advances and otherwise served from
	// cache — keeping the per-transaction ingress path off an O(amendments)
	// store-read + binary-parse + Rules allocation on every submit.
	//
	// A dedicated mutex (not s.mu) lets concurrent SubmitOpenLedgerTx
	// callers, which hold only s.mu.RLock, populate the cache without a
	// write lock. Lock order is always s.mu → configCacheMu: the cache is
	// consulted only from applyConfigLocked, whose caller already holds
	// s.mu, which keeps closedLedger stable while the cache is keyed on it.
	configCacheMu     sync.Mutex
	configCacheLedger *ledger.Ledger
	configCache       openledger.ApplyConfig
}

// New creates a new LedgerService
func New(cfg Config) (*Service, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = xrpllog.Discard()
	}
	// Construct the TxQ with rippled-default config (TxQ::Setup defaults).
	// In standalone mode, raise MinimumTxnInLedger so fee escalation
	// stays out of the way of integration tests — same trick rippled
	// uses (TxQ::Setup standalone vs default). The operator's
	// [transaction_queue] stanza, when wired by the caller, overrides both.
	txqCfg := txq.DefaultConfig()
	if cfg.Standalone {
		txqCfg = txq.StandaloneConfig()
	}
	if cfg.TxQ != nil {
		txqCfg = *cfg.TxQ
	}

	s := &Service{
		config:                   cfg,
		logger:                   logger.Named(xrpllog.PartitionLedger),
		nodeStore:                cfg.NodeStore,
		relationalDB:             cfg.RelationalDB,
		amendmentTable:           cfg.AmendmentTable,
		ledgerHistory:            make(map[uint32]*ledger.Ledger),
		ledgerByHash:             make(map[[32]byte]uint32),
		txIndex:                  make(map[[32]byte]uint32),
		txPositionIndex:          make(map[[32]byte]uint32),
		pendingValidation:        make(map[[32]byte]*LedgerAcceptedEvent),
		pendingLedgerValidations: make(map[uint32]pendingValidationEntry),
		heldAdoptions:            make(map[uint32]*pendingAdopt),
		txQueue:                  txq.New(txqCfg),
		localTxs:                 localtxs.New(),
		feeTrack:                 feetrack.New(),
	}

	return s, nil
}

// syncAmendmentTable folds a newly-validated ledger into the live amendment
// table: it enables the ledger's amendment set, refreshes the majority
// projection, and engages amendment-block if an unsupported amendment has
// activated. Gated to flag-ledger windows by NeedValidatedLedger; no-op when no
// table is configured. Mirrors rippled LedgerMaster::doValidatedLedger →
// AmendmentTable::doValidatedLedger.
func (s *Service) syncAmendmentTable(l *ledger.Ledger) {
	if s.amendmentTable == nil || l == nil {
		return
	}
	seq := l.Sequence()
	if !s.amendmentTable.NeedValidatedLedger(seq) {
		return
	}

	enabled := map[[32]byte]bool{}
	majorities := map[[32]byte]uint32{}
	if data, err := l.Read(keylet.Amendments()); err == nil && data != nil {
		sle, perr := pseudo.ParseAmendmentsSLE(data)
		if perr != nil {
			s.logger.Warn("amendment-table resync: failed to parse Amendments SLE",
				"seq", seq, "err", perr)
			return
		}
		for _, id := range sle.Amendments {
			enabled[id] = true
		}
		for _, m := range sle.Majorities {
			majorities[m.Amendment] = m.CloseTime
		}
	}

	s.amendmentTable.DoValidatedLedger(seq, enabled, majorities)
	if s.amendmentTable.IsBlocked() {
		s.logger.Error("amendment blocked: an unsupported amendment has activated; "+
			"node can no longer validate new ledgers", "seq", seq)
	}
}

// AmendmentTable returns the live amendment table shared with the consensus
// adaptor, or nil when none is configured.
func (s *Service) AmendmentTable() *amendment.AmendmentTable {
	return s.amendmentTable
}

// SetAmendmentVote records an operator veto (vetoed=true) or un-veto
// (vetoed=false) for the amendment in the live table and persists it so the
// preference survives restarts. The in-memory change always applies; an error
// is returned only when persistence fails.
//
// vetoed=false maps to UpVote, matching rippled's unVeto: unVeto sets the
// amendment's vote to AmendmentVote::up (AmendmentTable.cpp), and a server votes
// FOR every supported amendment whose vote is up. A VoteDefaultNo amendment's
// registered default is already "down" (the veto-equivalent state), so un-veto
// is exactly how an operator opts into voting for it — it does not abstain.
func (s *Service) SetAmendmentVote(ctx context.Context, id [32]byte, vetoed bool) error {
	if s.amendmentTable == nil {
		return errors.New("amendment table not configured")
	}
	if vetoed {
		s.amendmentTable.Veto(id)
	} else {
		s.amendmentTable.UpVote(id)
	}
	if s.relationalDB == nil || s.relationalDB.Amendment() == nil {
		return nil
	}
	name := ""
	if f := amendment.GetFeature(id); f != nil {
		name = f.Name
	}
	return s.relationalDB.Amendment().SaveAmendmentVote(ctx, &relationaldb.AmendmentVoteRecord{
		Amendment: strings.ToUpper(hex.EncodeToString(id[:])),
		Name:      name,
		Vetoed:    vetoed,
	})
}

// IsAmendmentBlocked reports whether an unsupported amendment has activated,
// blocking the node from validating new ledgers. False when no amendment table
// is configured.
func (s *Service) IsAmendmentBlocked() bool {
	if s.amendmentTable == nil {
		return false
	}
	return s.amendmentTable.IsBlocked()
}

// AmendmentFirstUnsupportedExpected returns the projected activation time (XRPL
// epoch seconds) of the earliest unsupported amendment currently holding
// majority, or (0, false) when none or no table is configured.
func (s *Service) AmendmentFirstUnsupportedExpected() (uint32, bool) {
	if s.amendmentTable == nil {
		return 0, false
	}
	return s.amendmentTable.FirstUnsupportedExpected()
}

// Start initializes the service with a genesis ledger
func (s *Service) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Create genesis ledger
	genesisResult, err := genesis.Create(s.config.GenesisConfig)
	if err != nil {
		return fmt.Errorf("failed to create genesis ledger: %w", err)
	}

	// Convert genesis to Ledger.
	// Fee values are read dynamically from the FeeSettings SLE in the state map
	// by readFeesFromLedger() whenever they are needed.
	genesisLedger := ledger.FromGenesis(
		genesisResult.Header,
		genesisResult.StateMap,
		genesisResult.TxMap,
		drops.Fees{},
	)

	s.genesisLedger = genesisLedger
	s.putHistoryLocked(genesisLedger)

	hash := genesisLedger.Hash()
	s.logger.Info("Genesis ledger created",
		"sequence", genesisLedger.Sequence(),
		"hash", strconv.FormatUint(uint64(hash[0])<<24|uint64(hash[1])<<16|uint64(hash[2])<<8|uint64(hash[3]), 16)+"...",
	)

	if s.config.Standalone {
		// Standalone mode: create ledger 2 locally and start from there.
		// Reference: rippled Application.cpp startGenesisLedger()
		nextLedger, err := ledger.NewOpen(genesisLedger, time.Now())
		if err != nil {
			return fmt.Errorf("failed to create next ledger: %w", err)
		}
		if err := nextLedger.Close(time.Now(), 0); err != nil {
			return fmt.Errorf("failed to close initial ledger: %w", err)
		}
		if err := nextLedger.SetValidated(); err != nil {
			return fmt.Errorf("failed to validate initial ledger: %w", err)
		}
		s.closedLedger = nextLedger
		s.validatedLedger = nextLedger
		s.putHistoryLocked(nextLedger)

		// Create the open ledger (ledger 3)
		openLedger, err := ledger.NewOpen(nextLedger, time.Now())
		if err != nil {
			return fmt.Errorf("failed to create open ledger: %w", err)
		}
		s.openLedger = openLedger
	} else {
		// Consensus mode: do NOT create ledger 2 locally.
		// Stay at genesis (seq 1) and wait to adopt a peer's ledger.
		s.closedLedger = genesisLedger
		s.validatedLedger = genesisLedger
		s.needsInitialSync = true

		// Create open ledger (seq 2) on top of genesis — will be replaced on adoption
		openLedger, err := ledger.NewOpen(genesisLedger, time.Now())
		if err != nil {
			return fmt.Errorf("failed to create open ledger: %w", err)
		}
		s.openLedger = openLedger
	}

	// Reset pending transactions
	s.pendingTxs = nil

	// Initialise the persistent open-ledger view (#407). Anchored on
	// the freshly constructed closedLedger so Current()'s seq matches
	// s.openLedger.
	if err := s.rebuildOpenLedgerViewLocked(); err != nil {
		return err
	}

	s.logger.Info("Ledger service started",
		"standalone", s.config.Standalone,
		"openLedger", s.openLedger.Sequence(),
		"needsInitialSync", s.needsInitialSync,
	)

	return nil
}

// rebuildOpenLedgerViewLocked rebuilds s.openLedgerView from s.closedLedger.
// Clears the field when closedLedger is nil. Caller must hold s.mu (write).
//
// Called from Start and from adopt-from-peer paths where a *new*
// closedLedger replaces the old one. The normal consensus close path
// uses OpenLedger.Accept instead — see AcceptConsensusResult.
func (s *Service) rebuildOpenLedgerViewLocked() error {
	if s.closedLedger == nil {
		s.openLedgerView = nil
		return nil
	}
	ov, err := openledger.New(s.closedLedger, openledger.Config{
		NetworkID: s.config.NetworkID,
		Logger:    s.logger,
	})
	if err != nil {
		return fmt.Errorf("rebuild open-ledger view: %w", err)
	}
	s.openLedgerView = ov
	return nil
}

// closedLedgerCtx implements txq.ClosedLedgerContext over a closed
// *ledger.Ledger. baseFee is the closed ledger's reference base fee in
// drops; we use it to convert per-tx fee values into fee levels for the
// FeeMetrics update.
type closedLedgerCtx struct {
	ledger  *ledger.Ledger
	baseFee uint64
}

func (c *closedLedgerCtx) GetLedgerSequence() uint32 {
	if c.ledger == nil {
		return 0
	}
	return c.ledger.Sequence()
}

func (c *closedLedgerCtx) GetTransactionFeeLevels() []txq.FeeLevel {
	if c.ledger == nil {
		return nil
	}
	var levels []txq.FeeLevel
	_ = c.ledger.ForEachTransaction(func(_ [32]byte, data []byte) bool {
		raw, _, err := tx.SplitTxWithMetaBlob(data)
		if err != nil {
			return true
		}
		parsed, err := tx.ParseFromBinary(raw)
		if err != nil {
			return true
		}
		common := parsed.GetCommon()
		if common == nil {
			return true
		}
		fee, err := strconv.ParseUint(common.Fee, 10, 64)
		if err != nil {
			return true
		}
		levels = append(levels, txq.ToFeeLevel(fee, c.baseFee))
		return true
	})
	return levels
}

// processClosedLedgerLocked updates the TxQ's fee metrics from the
// just-closed ledger. timeLeap mirrors rippled RCLConsensus.cpp:805's
// `roundTime > 5s` flag — when consensus took longer than the
// slow-consensus threshold the metrics window is clamped instead of
// advanced. Caller must hold s.mu.
func (s *Service) processClosedLedgerLocked() {
	if ping := s.stallPing.Load(); ping != nil {
		(*ping)()
	}
	if s.txQueue == nil || s.closedLedger == nil {
		return
	}
	baseFee, _, _ := readFeesFromLedger(s.closedLedger)
	ctx := &closedLedgerCtx{ledger: s.closedLedger, baseFee: baseFee}
	s.txQueue.ProcessClosedLedger(ctx, s.lastConsensusRoundTime > slowConsensusThreshold)
	s.tickLoadFeeLocked()
}

// SetStallPing installs the out-of-band stall watchdog's heartbeat callback,
// fired once per ledger close from processClosedLedgerLocked. Safe to call
// after construction; nil disables it. The callback must be cheap and
// non-blocking — it runs while s.mu is held.
func (s *Service) SetStallPing(ping func()) {
	if ping == nil {
		s.stallPing.Store(nil)
		return
	}
	s.stallPing.Store(&ping)
}

// slowConsensusThreshold matches rippled's `roundTime > 5s` predicate
// at RCLConsensus.cpp:805 — the TxQ treats anything past it as a
// slow-consensus round and freezes the fee-escalation window instead
// of opening it further.
const slowConsensusThreshold = 5 * time.Second

// SetLastConsensusRoundTime is called by the consensus adaptor at the
// end of each round to inform the service how long consensus took.
// processClosedLedgerLocked reads the value to set the TxQ's timeLeap
// flag. Standalone mode never calls this; the field stays zero and
// timeLeap is always false.
func (s *Service) SetLastConsensusRoundTime(d time.Duration) {
	s.mu.Lock()
	s.lastConsensusRoundTime = d
	s.mu.Unlock()
}

// tickLoadFeeLocked drives LoadFeeTrack raise/lower decisions from the
// per-ledger-close heartbeat. Mirrors rippled LoadManager::run
// (LoadManager.cpp:177-186): raise on overload, lower otherwise.
// go-xrpl has no JobQueue equivalent, so we proxy "overload" with TxQ
// fee escalation — when the required fee level has lifted off the
// reference level the open ledger is at or beyond its soft cap, which
// is the same condition that drives loadFactorFeeEscalation in
// server_info. This couples the two signals (LoadFeeTrack and feeEscalation)
// to a single observable, which is acceptable because server_info takes
// max(loadFactorServer, loadFactorFeeEscalation) — they never
// double-count. Caller must hold s.mu.
func (s *Service) tickLoadFeeLocked() {
	if s.feeTrack == nil || s.txQueue == nil || s.openLedger == nil {
		return
	}
	metrics := s.txQueue.GetMetrics(s.openLedger.TxCount())
	if metrics.OpenLedgerFeeLevel > metrics.ReferenceFeeLevel {
		s.feeTrack.RaiseLocalFee()
	} else {
		s.feeTrack.LowerLocalFee()
	}
}

// acceptOpenLedgerViewLocked invokes OpenLedger.Accept on the LCL
// transition from the prior closed ledger to s.closedLedger. No-op
// when the view is uninitialised (pre-Start). closedSeq is passed in
// for log context only.
//
// retries (if non-nil) are the txs left in retry state by the consensus /
// standalone build path — they replay first against the new open view.
// anyDisputes is the retriesFirst flag per rippled RCLConsensus.cpp:667
// (the anyDisputes signal). Caller must hold s.mu.
func (s *Service) acceptOpenLedgerViewLocked(closedSeq uint32, buildRetries []openledger.PendingTx, anyDisputes bool) {
	if s.openLedgerView == nil {
		return
	}
	if s.closedLedger == nil {
		return
	}
	baseFee, reserveBase, reserveIncrement := readFeesFromLedger(s.closedLedger)
	cfg := openledger.ApplyConfig{
		BaseFee:          baseFee,
		ReserveBase:      reserveBase,
		ReserveIncrement: reserveIncrement,
		NetworkID:        s.config.NetworkID,
		ParentCloseTime:  parentCloseTimeRippleEpoch(s.closedLedger),
		Logger:           s.config.Logger,
		Rules:            rulesFromLedger(s.closedLedger, s.logger),
		FeeTrack:         s.feeTrack,
	}
	// Modifier closure mirrors rippled OpenLedger.cpp:113 calling
	// app_.getTxQ().accept(app_, view) after the replay phases — this is
	// where queued candidates get promoted into the new open view.
	modifier := func(view *ledger.Ledger) {
		if s.txQueue == nil || view == nil {
			return
		}
		viewCfg := cfg
		viewCfg.LedgerSequence = view.Sequence()
		adapter := openledger.NewTxqAdapter(view, viewCfg)
		_ = s.txQueue.Accept(adapter)
	}
	// Pass the held local pool as Accept's `locals` argument so entries
	// replay onto the new open view. Mirrors RCLConsensus.cpp:666 passing
	// localTxs_.getTxSet() into openLedger().accept(...). Sweeping happens
	// on the validated path (SetValidatedLedger), matching rippled where
	// LedgerMaster::setValidLedger calls updateLocalTx — not on every
	// consensus close, which can be a fork that gets abandoned.
	var locals []openledger.PendingTx
	if s.localTxs != nil {
		locals = s.localTxs.GetTxSet()
	}
	// Seed retries with the build-pass leftover set. ApplyTxs (called via
	// Accept's retriesFirst phase) will drain this slice up front, then
	// re-fill it with any final-pass Retry classifications produced by
	// the replay itself.
	retries := append([]openledger.PendingTx(nil), buildRetries...)
	relay := s.txRelay
	relayCB := func(_ [32]byte, blob []byte) {
		if relay != nil {
			relay(blob)
		}
	}
	if relay == nil {
		relayCB = nil
	}
	if err := s.openLedgerView.Accept(s.closedLedger, locals, anyDisputes, &retries, cfg, s.txQueue, modifier, relayCB); err != nil {
		s.logger.Error("openLedger.Accept failed", "err", err, "seq", closedSeq)
	}
	if len(retries) > 0 {
		s.logger.Info("openLedger.Accept produced retries",
			"count", len(retries),
			"seq", closedSeq,
		)
	}
}

// applyConfigLocked returns the openledger.ApplyConfig for the current
// closed ledger. The config is memoised per closed ledger (see the
// configCache fields): it is rebuilt only when closedLedger advances and
// otherwise returned from cache, so the per-transaction ingress path does
// not re-read and re-parse the Amendments SLE and re-allocate the Rules
// set on every submit. The returned value is a copy; callers may mutate
// per-submission fields (e.g. ApplyFlags) without affecting the cache.
// Caller must hold s.mu (read lock is sufficient).
func (s *Service) applyConfigLocked() (openledger.ApplyConfig, error) {
	closed := s.closedLedger
	if closed == nil {
		return openledger.ApplyConfig{}, ErrNoClosedLedger
	}

	s.configCacheMu.Lock()
	defer s.configCacheMu.Unlock()
	// Pointer identity is a sufficient key: closed ledgers are immutable and
	// each close installs a fresh *ledger.Ledger. The cache retains the
	// ledger it was built from, so that object stays alive and its address
	// cannot be reused for a different ledger while it remains the key.
	if s.configCacheLedger == closed {
		return s.configCache, nil
	}

	baseFee, reserveBase, reserveIncrement := readFeesFromLedger(closed)
	cfg := openledger.ApplyConfig{
		BaseFee:          baseFee,
		ReserveBase:      reserveBase,
		ReserveIncrement: reserveIncrement,
		LedgerSequence:   closed.Sequence() + 1,
		NetworkID:        s.config.NetworkID,
		ParentCloseTime:  parentCloseTimeRippleEpoch(closed),
		Logger:           s.config.Logger,
		Rules:            rulesFromLedger(closed, s.logger),
		FeeTrack:         s.feeTrack,
	}
	s.configCache = cfg
	s.configCacheLedger = closed
	return cfg, nil
}

// rulesFromLedger derives the amendment.Rules in effect for `parent`'s
// successor ledger by reading parent's on-ledger Amendments SLE. Returns
// EmptyRules when parent is nil or the SLE cannot be read — the caller
// should treat a nil parent as a misconfiguration. Logging the read
// failure rather than propagating keeps the apply path tolerant of
// transient store errors; downstream tx behaviour will simply behave as
// if no amendments are enabled, which is the safe direction (rather
// than the AllSupportedRules() default that masks plumbing bugs).
// Reference: rippled Application::buildLedger threads
// `previousLedger->rules()` through; the rules are loaded from the
// parent's Amendments SLE in Ledger::Rules() at Ledger.cpp.
func rulesFromLedger(parent *ledger.Ledger, logger xrpllog.Logger) *amendment.Rules {
	if parent == nil {
		return amendment.EmptyRules()
	}
	rules, err := ledger.LoadAmendmentsFromLedger(parent)
	if err != nil {
		if logger != nil {
			logger.Warn("failed to load amendments from parent ledger; defaulting to empty rules",
				"parent_seq", parent.Sequence(), "err", err)
		}
		return amendment.EmptyRules()
	}
	return rules
}

// SubmitOpenLedgerTx routes a tx blob through the persistent OpenLedger
// view (#407). Mirrors NetworkOPsImp::apply → openLedger().modify
// (NetworkOPs.cpp:1507). Returns the per-tx classification. Returns
// ResultFailure when called before Start (no view initialised) — the
// nil guard is defensive; callers should not race Start with ingress.
//
// local=true marks the submission as RPC-originated and pushes any
// non-Failure result into the LocalTxs held pool so it survives Submit
// failure / LCL transitions until the sender's AccountRoot.Sequence
// advances past it or it ages out (5 ledgers).
//
// local=false is for relay-originated submissions (from peers): the
// peer manages its own resends, so we don't pin the blob in our held
// pool. Mirrors rippled's NetworkOPsImp::processTrustedProposal vs
// NetworkOPsImp::processTransaction distinction (NetworkOPs.cpp where
// `local` flag flows into `LocalTxs::push_back`).
func (s *Service) SubmitOpenLedgerTx(blob []byte, local bool) (openledger.Result, error) {
	s.mu.RLock()
	ov := s.openLedgerView
	queue := s.txQueue
	pool := s.localTxs
	cfg, cfgErr := s.applyConfigLocked()
	s.mu.RUnlock()

	if ov == nil {
		return openledger.ResultFailure, errors.New("openLedgerView not initialised")
	}
	if cfgErr != nil {
		return openledger.ResultFailure, cfgErr
	}
	ptx, err := openledger.ParsePendingTx(blob)
	if err != nil {
		return openledger.ResultFailure, err
	}
	_, res := ov.Submit(ptx, cfg, queue)

	if local && pool != nil && res != openledger.ResultFailure {
		pool.PushBack(ov.Current().Sequence(), ptx)
	}
	return res, nil
}

// OpenLedgerTxs returns the raw tx blobs currently in the persistent
// open view. Mirrors RCLConsensus.cpp:333-349 reading
// openLedger().current()->txs (an immutable snapshot). Returns nil
// when the view is uninitialised (pre-Start).
//
// The returned slice is memoised inside OpenLedger and shared with
// concurrent callers — it MUST NOT be mutated. Callers (consensus
// adaptor → engine) only read.
func (s *Service) OpenLedgerTxs() [][]byte {
	s.mu.RLock()
	ov := s.openLedgerView
	s.mu.RUnlock()
	if ov == nil {
		return nil
	}
	return ov.CurrentTxs()
}

// OpenLedgerTxHashes returns the tx hashes currently in the persistent
// open view. Drives the periodic TMHaveTransactions announce in the
// peer overlay's tx-reduce-relay outbound path. Allocates a fresh
// slice each call so callers can hold the result past lock release.
// Returns nil pre-Start.
func (s *Service) OpenLedgerTxHashes() [][32]byte {
	s.mu.RLock()
	ov := s.openLedgerView
	s.mu.RUnlock()
	if ov == nil {
		return nil
	}
	view := ov.Current()
	if view == nil {
		return nil
	}
	var hashes [][32]byte
	_ = view.ForEachTransaction(func(hash [32]byte, _ []byte) bool {
		hashes = append(hashes, hash)
		return true
	})
	return hashes
}

// OpenLedgerHasTx reports whether the persistent open view contains
// the tx hash. Used by peer-protocol HasTx replies.
func (s *Service) OpenLedgerHasTx(hash [32]byte) bool {
	s.mu.RLock()
	ov := s.openLedgerView
	s.mu.RUnlock()
	if ov == nil {
		return false
	}
	return ov.Current().TxExists(hash)
}

// OpenLedgerGetTx returns the raw tx blob for hash if present in the
// persistent open view.
func (s *Service) OpenLedgerGetTx(hash [32]byte) ([]byte, bool) {
	s.mu.RLock()
	ov := s.openLedgerView
	s.mu.RUnlock()
	if ov == nil {
		return nil, false
	}
	view := ov.Current()
	data, found, err := view.GetTransaction(hash)
	if err != nil || !found {
		return nil, false
	}
	raw, _, err := tx.SplitTxWithMetaBlob(data)
	if err != nil {
		return nil, false
	}
	return raw, true
}

// GetOpenLedger returns the current open ledger
func (s *Service) GetOpenLedger() *ledger.Ledger {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.openLedger
}

// GetClosedLedger returns the last closed ledger
func (s *Service) GetClosedLedger() *ledger.Ledger {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closedLedger
}

// GetValidatedLedger returns the highest validated ledger
func (s *Service) GetValidatedLedger() *ledger.Ledger {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.validatedLedger
}

// XRPFeesEnabled reports whether the XRPFees amendment is active on the
// validated ledger. The subscribe ack uses it to gate the deprecated
// fee_ref field, mirroring rippled's subLedger.
func (s *Service) XRPFeesEnabled() bool {
	s.mu.RLock()
	validated := s.validatedLedger
	s.mu.RUnlock()
	if validated == nil {
		return false
	}
	rules, err := ledger.LoadAmendmentsFromLedger(validated)
	if err != nil || rules == nil {
		return false
	}
	return rules.XRPFeesEnabled()
}

// GetLedgerBySequence returns a ledger by its sequence number, falling back
// to the open ledger when its sequence matches (mirrors rippled RPCHelpers.cpp:498-508).
func (s *Service) GetLedgerBySequence(seq uint32) (*ledger.Ledger, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if l, ok := s.ledgerHistory[seq]; ok {
		return l, nil
	}
	if s.openLedger != nil && s.openLedger.Sequence() == seq {
		return s.openLedger, nil
	}
	return nil, ErrLedgerNotFound
}

// GetAdoptedLedgerBySequence returns a closed ledger from the adopted
// history (ledgerHistory[seq]) only — unlike GetLedgerBySequence it never
// falls back to the mutable open ledger. The consensus catch-up walk
// requires immutable, parent-hash-chained ledgers; rippled's acquire path
// likewise only ever yields closed/immutable ledgers (RCLValidations.cpp:154-156).
func (s *Service) GetAdoptedLedgerBySequence(seq uint32) (*ledger.Ledger, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if l, ok := s.ledgerHistory[seq]; ok {
		return l, nil
	}
	return nil, ErrLedgerNotFound
}

// GetLedgerByHash returns a ledger by its hash
func (s *Service) GetLedgerByHash(hash [32]byte) (*ledger.Ledger, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if seq, ok := s.ledgerByHash[hash]; ok {
		if l, ok := s.ledgerHistory[seq]; ok {
			return l, nil
		}
	}
	return nil, ErrLedgerNotFound
}

// GetCurrentLedgerIndex returns the current open ledger index
func (s *Service) GetCurrentLedgerIndex() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.openLedger == nil {
		return 0
	}
	return s.openLedger.Sequence()
}

// GetClosedLedgerIndex returns the last closed ledger index
func (s *Service) GetClosedLedgerIndex() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closedLedger == nil {
		return 0
	}
	return s.closedLedger.Sequence()
}

// ledgerHistoryRangeLocked returns the inclusive [min, max] sequence span of
// the in-memory ledger history, or ok=false when it is empty. Caller holds
// s.mu. NB: the span assumes contiguity — fixMismatchLocked purges and backward
// fills can leave gaps, so callers reporting durable availability must layer
// their own floor (see GetServerInfo's online-delete clamp).
func (s *Service) ledgerHistoryRangeLocked() (min, max uint32, ok bool) {
	first := true
	for seq := range s.ledgerHistory {
		if first || seq < min {
			min = seq
		}
		if first || seq > max {
			max = seq
		}
		first = false
	}
	return min, max, !first
}

// AvailableLedgerRange returns the inclusive [min, max] sequence range of
// ledgers held locally (the in-memory history), or ok=false when none are
// available. Used by the ledger-integrity verifier to bound a cleaning run,
// mirroring rippled's getFullValidatedRange.
func (s *Service) AvailableLedgerRange() (min, max uint32, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ledgerHistoryRangeLocked()
}

// GetValidatedLedgerIndex returns the highest validated ledger index
func (s *Service) GetValidatedLedgerIndex() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.validatedLedger == nil {
		return 0
	}
	return s.validatedLedger.Sequence()
}

// getValidatedLedgersRange returns a string representation of validated ledger range
func (s *Service) getValidatedLedgersRange() string {
	minSeq, maxSeq, ok := s.ledgerHistoryRangeLocked()
	if !ok {
		return "empty"
	}
	if minSeq == maxSeq {
		return strconv.FormatUint(uint64(minSeq), 10)
	}
	return formatRange(minSeq, maxSeq)
}

// SetServerStateFunc sets a function that provides the server state string.
func (s *Service) SetServerStateFunc(fn func() string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serverStateFunc = fn
}

// SetMinimumOnlineFunc registers the online-delete retention floor used to
// clamp complete_ledgers in server_info. Pass nil (or leave unset) when
// online_delete is off — complete_ledgers then reflects the in-memory history
// window unchanged.
func (s *Service) SetMinimumOnlineFunc(fn func() uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.minimumOnlineFunc = fn
}

// IsStandalone returns true if running in standalone mode
func (s *Service) IsStandalone() bool {
	return s.config.Standalone
}

// GetGenesisAccount returns the genesis account address
func (s *Service) GetGenesisAccount() (string, error) {
	_, address, err := genesis.GenerateGenesisAccountID()
	return address, err
}

// GetTxQMetrics returns the current TxQ metrics, or the zero value when
// the queue isn't initialised.
func (s *Service) GetTxQMetrics() txq.Metrics {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.txQueue == nil {
		return txq.Metrics{}
	}
	var txInLedger uint32
	if s.openLedger != nil {
		txInLedger = s.openLedger.TxCount()
	}
	return s.txQueue.GetMetrics(txInLedger)
}

// GetQueueAccountTxs returns the TxQ candidates currently queued for one
// account, sorted by SeqProxy. Backs account_info's queue_data
// (rippled TxQ::getAccountTxs). Empty when no TxQ is wired.
func (s *Service) GetQueueAccountTxs(account [20]byte) []*txq.CandidateDetails {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.txQueue == nil {
		return nil
	}
	return s.txQueue.GetAccountTxs(account)
}

// GetQueueAllTxs returns every TxQ candidate, ordered by fee level. Backs the
// ledger method's queue_data dump (rippled TxQ::getTxs). Empty when no TxQ is
// wired.
func (s *Service) GetQueueAllTxs() []*txq.CandidateDetails {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.txQueue == nil {
		return nil
	}
	return s.txQueue.GetAllTxs()
}

// GetServerInfo returns basic server information
func (s *Service) GetServerInfo() ServerInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	serverState := "full"
	if s.serverStateFunc != nil {
		serverState = s.serverStateFunc()
	}

	info := ServerInfo{
		Standalone:      s.config.Standalone,
		ServerState:     serverState,
		CompleteLedgers: "",
		NetworkID:       s.config.NetworkID,
	}

	if s.openLedger != nil {
		info.OpenLedgerSeq = s.openLedger.Sequence()
	}

	if s.closedLedger != nil {
		info.ClosedLedgerSeq = s.closedLedger.Sequence()
		info.ClosedLedgerHash = s.closedLedger.Hash()
		info.ClosedLedgerCloseTime = rippleEpochSeconds(s.closedLedger.CloseTime())
	}

	if s.validatedLedger != nil {
		info.HaveValidated = true
		info.ValidatedLedgerSeq = s.validatedLedger.Sequence()
		info.ValidatedLedgerHash = s.validatedLedger.Hash()
		info.ValidatedLedgerCloseTime = rippleEpochSeconds(s.validatedLedger.CloseTime())
	}

	// Calculate complete ledgers range
	if minSeq, maxSeq, ok := s.ledgerHistoryRangeLocked(); ok {
		// Clamp the lower bound up to the online-delete floor: the in-memory
		// history window is swept independently of the rotator, so after a
		// rotation it can still name ledgers the node store no longer holds.
		// complete_ledgers must report durable availability, not the window.
		if s.minimumOnlineFunc != nil {
			if floor := s.minimumOnlineFunc(); floor > minSeq {
				minSeq = floor
			}
		}
		switch {
		case minSeq > maxSeq:
			// The whole window sits below the floor — nothing durable to advertise.
		case minSeq == maxSeq:
			info.CompleteLedgers = strconv.FormatUint(uint64(minSeq), 10)
		default:
			info.CompleteLedgers = formatRange(minSeq, maxSeq)
		}
	}

	return info
}

// ServerInfo contains basic server status information
type ServerInfo struct {
	Standalone               bool
	ServerState              string // "disconnected", "connected", "syncing", "tracking", "full"
	OpenLedgerSeq            uint32
	ClosedLedgerSeq          uint32
	ClosedLedgerHash         [32]byte
	ClosedLedgerCloseTime    int64 // Ripple-epoch seconds
	HaveValidated            bool  // mirrors rippled LedgerMaster::haveValidated()
	ValidatedLedgerSeq       uint32
	ValidatedLedgerHash      [32]byte
	ValidatedLedgerCloseTime int64 // Ripple-epoch seconds
	CompleteLedgers          string
	NetworkID                uint32
}

// rippleEpochSeconds converts a wall-clock close time to seconds since
// the XRPL epoch (2000-01-01 UTC). Returns 0 for the zero time so a
// genesis-only node reports close_time=0 instead of a negative value.
func rippleEpochSeconds(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	s := t.Unix() - protocol.RippleEpochUnix
	if s < 0 {
		return 0
	}
	return s
}

// GetLedgerInfo returns information about a specific ledger
func (s *Service) GetLedgerInfo(seq uint32) (*LedgerInfo, error) {
	l, err := s.GetLedgerBySequence(seq)
	if err != nil {
		return nil, err
	}

	return &LedgerInfo{
		Sequence:   l.Sequence(),
		Hash:       l.Hash(),
		ParentHash: l.ParentHash(),
		CloseTime:  l.CloseTime(),
		TotalDrops: l.TotalDrops(),
		Validated:  l.IsValidated(),
		Closed:     l.IsClosed(),
	}, nil
}

// LedgerInfo contains information about a ledger
type LedgerInfo struct {
	Sequence   uint32
	Hash       [32]byte
	ParentHash [32]byte
	CloseTime  time.Time
	TotalDrops uint64
	Validated  bool
	Closed     bool
	Header     header.LedgerHeader
}

// GetPendingTxBlobs returns the raw transaction blobs for all pending transactions.
func (s *Service) GetPendingTxBlobs() [][]byte {
	s.mu.RLock()
	defer s.mu.RUnlock()

	blobs := make([][]byte, len(s.pendingTxs))
	for i, ptx := range s.pendingTxs {
		blobs[i] = ptx.Blob
	}
	return blobs
}

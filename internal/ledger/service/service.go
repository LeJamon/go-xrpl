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
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
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
	"github.com/LeJamon/go-xrpl/shamap"
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

// LedgerAcceptedEvent contains information about an accepted ledger and its transactions
type LedgerAcceptedEvent struct {
	// LedgerInfo contains the accepted ledger information
	LedgerInfo *LedgerInfo

	// TransactionResults contains the results of transactions in this ledger
	TransactionResults []TransactionResultEvent
}

// TransactionResultEvent contains transaction details for event broadcasting
type TransactionResultEvent struct {
	// TxHash is the transaction hash
	TxHash [32]byte

	// TxData is the raw transaction data
	TxData []byte

	// MetaData is the transaction metadata (nil if not available)
	MetaData []byte

	// Validated indicates if the transaction is in a validated ledger
	Validated bool

	// LedgerIndex is the ledger sequence containing this transaction
	LedgerIndex uint32

	// LedgerHash is the hash of the ledger containing this transaction
	LedgerHash [32]byte

	// AffectedAccounts lists the accounts affected by this transaction
	AffectedAccounts []string
}

// EventCallback is a function that receives ledger events
type EventCallback func(event *LedgerAcceptedEvent)

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

// SubmittedTxEvent carries the inputs the WebSocket transactions_proposed
// publisher needs from a SubmitTransaction call.
type SubmittedTxEvent struct {
	RawBlob []byte
	TxHash  [32]byte
	// AffectedAccounts is the full mentioned-accounts set so
	// accounts_proposed fans out to every party referenced by the tx
	// (source, destination, regular key, signers, ...). Mirrors
	// rippled STTx::getMentionedAccounts → pubProposedAccountTransaction
	// at NetworkOPs.cpp:3550-3611.
	AffectedAccounts []string
	CurrentLedger    uint32
	Result           Result
}

// Result is a slim mirror of tx.ApplyResult — copied here so the RPC
// layer can consume the event without importing internal/tx.
type Result struct {
	Code    int
	Name    string
	Message string
	Applied bool
}

type SubmittedTxCallback func(SubmittedTxEvent)

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

// SetEventCallback sets the callback function for ledger events
func (s *Service) SetEventCallback(callback EventCallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventCallback = callback
}

// SetSubmittedTxCallback registers a sink fired from SubmitTransaction
// after every apply attempt. Pass nil to unwire. Mirrors rippled's
// pubProposedTransaction subscription wiring (NetworkOPs.cpp:2316-2370).
func (s *Service) SetSubmittedTxCallback(fn SubmittedTxCallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.submittedTxCallback = fn
}

// SetTxRelay registers the per-tx broadcast handler invoked by
// OpenLedger.Accept's relay callback (rippled OpenLedger.cpp:120-150).
// Pass nil to unwire.
func (s *Service) SetTxRelay(fn func(blob []byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.txRelay = fn
}

// SetOnPendingValidationStashed registers a handler invoked off-thread
// when SetValidatedLedger stashes a validation that doesn't match a
// ledger we have. Pass nil to unwire.
func (s *Service) SetOnPendingValidationStashed(handler func(seq uint32, hash [32]byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onPendingValidationStashed = handler
}

// SetEventHooks sets the event hooks for ledger events
// This provides a more structured callback mechanism than SetEventCallback
func (s *Service) SetEventHooks(hooks *EventHooks) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = hooks
}

// GetEventHooks returns the current event hooks (may be nil)
func (s *Service) GetEventHooks() *EventHooks {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hooks
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

// AcceptLedger closes the current open ledger and creates a new one.
// This is the main mechanism for advancing ledgers in standalone mode.
// It corresponds to the "ledger_accept" RPC command.
//
// When pending transactions exist, they are sorted using CanonicalTXSet ordering
// and re-applied from a fresh copy of the LCL, matching rippled's behavior.
// Reference: rippled NetworkOPs::acceptLedgerTransaction / CanonicalTXSet
func (s *Service) AcceptLedger(ctx context.Context) (uint32, error) {
	return s.AcceptLedgerAt(ctx, time.Time{})
}

// AcceptLedgerAt is AcceptLedger with an explicit close_time. A zero
// time.Time falls back to time.Now(). Differential / replay tests use
// an explicit value to keep close_time byte-identical between
// implementations.
func (s *Service) AcceptLedgerAt(ctx context.Context, explicitCloseTime time.Time) (uint32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.config.Standalone {
		return 0, ErrNotStandalone
	}

	if s.openLedger == nil {
		return 0, ErrNoOpenLedger
	}

	if s.closedLedger == nil {
		return 0, ErrNoClosedLedger
	}

	closeTime := explicitCloseTime
	if closeTime.IsZero() {
		closeTime = time.Now()
	}

	// If there are pending transactions, re-apply them in canonical order
	// on a fresh ledger built from the LCL. This matches rippled's behavior
	// where open ledger transactions are re-ordered via CanonicalTXSet.
	var retriableTxs []openledger.PendingTx
	if len(s.pendingTxs) > 0 {
		built, err := s.buildClosedLedgerLocked(s.pendingTxs, closeTime, s.config.Standalone)
		if err != nil {
			return 0, err
		}
		retriableTxs = built
	} else {
		// No pending txs to rebuild through buildClosedLedgerLocked, but a
		// flag-ledger close must still apply the pending NegativeUNL transition.
		if err := s.applyFlagLedgerNegativeUNL(s.openLedger); err != nil {
			return 0, err
		}
	}

	// Reset pending transactions
	s.pendingTxs = nil

	// Close the current open ledger
	if err := s.openLedger.Close(closeTime, 0); err != nil {
		return 0, fmt.Errorf("failed to close ledger: %w", err)
	}

	// In standalone mode, immediately validate
	if err := s.openLedger.SetValidated(); err != nil {
		return 0, fmt.Errorf("failed to validate ledger: %w", err)
	}

	// Persist the closed ledger to storage backends (nodestore and/or relational DB).
	// persistLedger has internal nil guards for each backend.
	//
	// Match rippled: LedgerMaster::setFullLedger -> pendSaveValidated
	// discards the bool return and the chain advance proceeds regardless
	// (rippled/src/xrpld/app/ledger/detail/LedgerMaster.cpp:831,972).
	// Treating SQL persistence failure as fatal here would diverge from
	// rippled and risk forks on transient relational-DB issues.
	if err := s.persistLedger(ctx, s.openLedger); err != nil {
		s.logger.Error("failed to persist closed ledger; chain advance continues",
			"seq", s.openLedger.Sequence(), "err", err)
	}

	// Store the closed ledger in memory cache
	closedSeq := s.openLedger.Sequence()
	closedLedgerHash := s.openLedger.Hash()
	s.closedLedger = s.openLedger
	s.validatedLedger = s.openLedger
	s.putHistoryLocked(s.openLedger)
	s.evictOldHistoryLocked(closedSeq)

	// Standalone validates immediately; fold the validated ledger into the
	// amendment table (enabled set + majority projection + block detection).
	s.syncAmendmentTable(s.validatedLedger)

	// Standalone already promotes to validated above, so any stashed
	// validation at this seq is redundant — but drain it so the entry
	// doesn't linger and accidentally match a later re-close at the
	// same seq. No-op when nothing is stashed.
	s.drainPendingLedgerValidationLocked(closedSeq, s.closedLedger)

	// Collect transaction results for event callbacks/hooks
	var txResults []TransactionResultEvent
	if s.eventCallback != nil || (s.hooks != nil && (s.hooks.OnLedgerClosed != nil || s.hooks.OnTransaction != nil)) {
		txResults = s.collectTransactionResults(s.closedLedger, closedSeq, closedLedgerHash)
	}

	ledgerInfo, validatedLedgers, err := s.advanceToNewOpenLedgerLocked(closedSeq, closedLedgerHash, retriableTxs)
	if err != nil {
		return 0, err
	}

	// Fire structured event hooks for the newly-closed ledger. In the
	// standalone path the ledger is already validated (line above sets
	// s.validatedLedger), so the legacy eventCallback fires immediately
	// rather than being stashed for SetValidatedLedger to drain.
	s.fireLedgerClosedHooksLocked(ledgerInfo, txResults, closeTime, validatedLedgers)

	// Fire legacy event callback for backward compatibility
	if s.eventCallback != nil {
		event := &LedgerAcceptedEvent{
			LedgerInfo:         ledgerInfo,
			TransactionResults: txResults,
		}

		// Call callback in a goroutine to not block ledger operations
		callback := s.eventCallback
		go callback(event)
	}

	s.logger.Info("Ledger accepted",
		"sequence", closedSeq,
		"hash", fmt.Sprintf("%x", closedLedgerHash[:8]),
		"txs", len(txResults),
	)

	return closedSeq, nil
}

// applyFlagLedgerNegativeUNL applies the pending NegativeUNL transition
// (move ValidatorToDisable into DisabledValidators, drop ValidatorToReEnable,
// clear the transition fields) to l when l is a flag ledger (seq % 256 == 0)
// and featureNegativeUNL is enabled on the parent rule set. rippled does this
// on every flag-ledger build before applying transactions, and the
// catchup/replay-delta path mirrors it; omitting it on the local close path
// makes a locally-built flag ledger compute a different account_hash than the
// rest of the network 256 ledgers after any UNLModify set a pending
// transition, forking the chain. Caller must hold s.mu.
func (s *Service) applyFlagLedgerNegativeUNL(l *ledger.Ledger) error {
	if l.Sequence()%256 != 0 {
		return nil
	}
	rules := rulesFromLedger(s.closedLedger, s.logger)
	if rules == nil || !rules.Enabled(amendment.FeatureNegativeUNL) {
		return nil
	}
	if err := l.UpdateNegativeUNL(); err != nil {
		return fmt.Errorf("flag-ledger updateNegativeUNL: %w", err)
	}
	return nil
}

// buildClosedLedgerLocked canonically sorts pending, re-applies it onto a
// fresh ledger built from s.closedLedger, hoists every committed tx into
// s.txIndex, and installs the result as s.openLedger. It returns the txs
// left in retry state after the build passes. Shared by the standalone
// (AcceptLedgerAt) and consensus (AcceptConsensusResult) close paths, which
// differ only in their pending source and whether signature verification is
// skipped. Caller must hold s.mu.
func (s *Service) buildClosedLedgerLocked(pending []pendingTx, closeTime time.Time, skipSigVerify bool) ([]openledger.PendingTx, error) {
	// Salt = SHAMap root of the tx set, matching rippled's consensus-build
	// convention; the local pending pool plays the same role in standalone.
	canonicalSort(pending, computeSalt(pending))

	freshLedger, err := ledger.NewOpen(s.closedLedger, closeTime)
	if err != nil {
		return nil, fmt.Errorf("failed to create fresh ledger for close: %w", err)
	}

	// On a flag ledger, apply the pending NegativeUNL transition before any
	// transactions, matching rippled's BuildLedger ordering.
	if err := s.applyFlagLedgerNegativeUNL(freshLedger); err != nil {
		return nil, err
	}

	baseFee, reserveBase, reserveIncrement := readFeesFromLedger(s.closedLedger)
	applyCfg := openledger.ApplyConfig{
		BaseFee:                   baseFee,
		ReserveBase:               reserveBase,
		ReserveIncrement:          reserveIncrement,
		LedgerSequence:            freshLedger.Sequence(),
		NetworkID:                 s.config.NetworkID,
		ParentCloseTime:           parentCloseTimeRippleEpoch(s.closedLedger),
		Logger:                    s.config.Logger,
		SkipSignatureVerification: skipSigVerify,
		// BuildLedger semantics: tec under certainRetry holds for retry and
		// commits on the final non-retry pass.
		Mode: openledger.BuildLedgerMode,
		// Pull amendments from the parent ledger so amendment-gated behaviour
		// (SLE threading and others) matches the on-chain rule set rather than
		// the all-amendments-on default.
		Rules: rulesFromLedger(s.closedLedger, s.logger),
	}

	var retriableTxs []openledger.PendingTx
	if err := openledger.ApplyTxs(freshLedger, pending, &retriableTxs, applyCfg); err != nil {
		return nil, fmt.Errorf("openledger.ApplyTxs: %w", err)
	}

	// Track every committed tx (tesSUCCESS and tec) by the ledger seq.
	_ = freshLedger.ForEachTransaction(func(txHash [32]byte, _ []byte) bool {
		s.txIndex[txHash] = freshLedger.Sequence()
		return true
	})

	s.openLedger = freshLedger
	return retriableTxs, nil
}

// advanceToNewOpenLedgerLocked opens a fresh ledger on top of the just-closed
// s.closedLedger, refreshes the open-ledger fee metrics, and rebuilds the
// persistent open-ledger view — replaying any retriable txs onto it. It
// returns the closed ledger's info and the validated-range string used for
// hook dispatch. Shared tail of the standalone and consensus close paths.
// Caller must hold s.mu.
func (s *Service) advanceToNewOpenLedgerLocked(closedSeq uint32, closedLedgerHash [32]byte, retriableTxs []openledger.PendingTx) (*LedgerInfo, string, error) {
	newOpen, err := ledger.NewOpen(s.closedLedger, time.Now())
	if err != nil {
		return nil, "", fmt.Errorf("failed to create new open ledger: %w", err)
	}
	s.openLedger = newOpen

	// Refresh fee metrics from the just-closed ledger so the next Accept's
	// modifier sees the right open-ledger fee level.
	s.processClosedLedgerLocked()

	// LCL transition: replay the prior view's txs onto the new open ledger via
	// Accept. retriesFirst is driven by retriableTxs — txs the build pass left
	// in retry state are precisely the ones that need a retries-first replay
	// against the new open view. This is a superset of rippled's disputed set;
	// cleanly-applied txs that get redundantly replayed are harmless (Accept's
	// parent-skip guard short-circuits).
	s.acceptOpenLedgerViewLocked(closedSeq, retriableTxs, len(retriableTxs) > 0)

	ledgerInfo := &LedgerInfo{
		Sequence:   closedSeq,
		Hash:       closedLedgerHash,
		ParentHash: s.closedLedger.ParentHash(),
		CloseTime:  s.closedLedger.CloseTime(),
		TotalDrops: s.closedLedger.TotalDrops(),
		Validated:  s.closedLedger.IsValidated(),
		Closed:     s.closedLedger.IsClosed(),
	}
	return ledgerInfo, s.getValidatedLedgersRange(), nil
}

// fireLedgerClosedHooksLocked fires hooks.OnLedgerClosed and
// hooks.OnTransaction for a ledger that has transitioned to closed.
// Each hook dispatch runs on its own goroutine so subscriber callbacks
// cannot block the ledger service or deadlock against s.mu. Safe to
// call with s.hooks == nil or individual hook fields nil.
//
// Caller must hold s.mu. Shared by the standalone close path and the
// peer-adopt path so WebSocket `ledger` and `transactions` streams see
// every closed ledger regardless of whether it was closed locally or
// adopted from a peer — a silent divergence from rippled before F3
// where peer-adopted ledgers never reached stream subscribers.
func (s *Service) fireLedgerClosedHooksLocked(
	info *LedgerInfo,
	txResults []TransactionResultEvent,
	closeTime time.Time,
	validatedLedgers string,
) {
	if s.hooks == nil {
		return
	}

	if s.hooks.OnLedgerClosed != nil {
		txCount := len(txResults)
		hooks := s.hooks
		capturedInfo := info
		capturedRange := validatedLedgers
		go hooks.OnLedgerClosed(capturedInfo, txCount, capturedRange)
	}

	if s.hooks.OnTransaction != nil {
		hooks := s.hooks
		ledgerSeq := info.Sequence
		ledgerHash := info.Hash
		closeTimeVal := closeTime
		for _, txResult := range txResults {
			txInfo := TransactionInfo{
				Hash:             txResult.TxHash,
				TxBlob:           txResult.TxData,
				AffectedAccounts: txResult.AffectedAccounts,
			}
			result := TxResult{
				Applied:  txResult.Validated,
				Metadata: txResult.MetaData,
				TxIndex:  s.txPositionIndex[txResult.TxHash],
			}
			go hooks.OnTransaction(txInfo, result, ledgerSeq, ledgerHash, closeTimeVal)
		}
	}
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

// collectTransactionResults gathers transaction data from the closed ledger
// and records each transaction's position within the ledger. It also
// populates s.txIndex (hash -> ledger seq) so tx-hash RPC lookups
// resolve to this ledger. For the local-close path s.txIndex is also
// written at Apply time; repeating the write here is idempotent and is
// the sole index population site for the peer-adopt path, which has no
// Apply step.
func (s *Service) collectTransactionResults(l *ledger.Ledger, ledgerSeq uint32, ledgerHash [32]byte) []TransactionResultEvent {
	var results []TransactionResultEvent

	var txIndex uint32
	l.ForEachTransaction(func(txHash [32]byte, txData []byte) bool {
		result := TransactionResultEvent{
			TxHash:      txHash,
			TxData:      txData,
			Validated:   l.IsValidated(),
			LedgerIndex: ledgerSeq,
			LedgerHash:  ledgerHash,
		}
		result.AffectedAccounts = extractAffectedAccounts(txData)

		s.txIndex[txHash] = ledgerSeq
		s.txPositionIndex[txHash] = txIndex
		txIndex++

		results = append(results, result)
		return true
	})

	return results
}

// installAdoptedLedgerLocked writes adopted into ledgerHistory[seq] under
// the validated-precedence rule — mirrors LedgerHistory::insert(ledger,
// validated) at LedgerHistory.cpp:55-74. Returns the canonical entry;
// callers must use the return as s.closedLedger to keep history and
// closed-reference consistent. Holds s.mu write.
func (s *Service) installAdoptedLedgerLocked(seq uint32, adopted *ledger.Ledger) *ledger.Ledger {
	if existing, ok := s.ledgerHistory[seq]; ok {
		existingHash := existing.Hash()
		newHash := adopted.Hash()
		if existingHash != newHash && existing.IsValidated() && !adopted.IsValidated() {
			s.logger.Warn("adopt skip: validated entry already present",
				"seq", seq,
				"existing_hash", fmt.Sprintf("%x", existingHash[:8]),
				"adopt_hash", fmt.Sprintf("%x", newHash[:8]),
			)
			return existing
		}
	}
	s.putHistoryLocked(adopted)
	return adopted
}

// fixMismatchLocked invalidates the tail of ledgerHistory when the
// adopted ledger does not chain to whatever we already have at
// `adopted.Sequence()-1`. Mirrors rippled's setFullLedger parent-hash
// sanity check + fixMismatch() call (LedgerMaster.cpp:749-801, 849-862).
//
// Trigger: prev := ledgerHistory[adoptedSeq-1] exists AND
// prev.Hash() != adopted.ParentHash(). When that happens:
//
//  1. Delete the prev-seq slot (wrong fork at adoptedSeq-1).
//  2. Delete every seq > adoptedSeq — those entries chained to the
//     now-discarded prev or to a sibling of `adopted`, and so their
//     parent lineage no longer resolves.
//  3. Purge s.txIndex / s.txPositionIndex entries for the removed
//     ledgers — otherwise `tx` / `transaction_entry` RPCs keep
//     resolving to a seq whose contents were discarded.
//  4. Clear s.closedLedger if it was pointing at an invalidated slot.
//     AdoptLedgerWithState reassigns closedLedger to `adopted` right
//     after this returns, so the clear is a defense-in-depth belt.
//  5. If the invalidated prev-seq entry was marked validated, log ERROR
//     — silently resetting a validated ledger would mask a serious
//     fork. We do NOT reset s.validatedLedger silently; operator
//     attention is required.
//
// Caller must hold s.mu (write lock). Called from AdoptLedgerWithState
// before the new entry is written. No-op on the happy path (parent
// chain matches or no prev entry exists), so the hot path is a single
// map lookup + hash compare.
//
// Scope note: rippled's fixMismatch walks the LedgerHashes skiplist
// backward further than the immediate parent and tries to "close the
// seam" by finding the deepest still-consistent ancestor. This Go
// implementation only invalidates the immediate prev-seq mismatch and
// the forward orphans — deeper history is left untouched. Rationale:
// the skiplist walk requires hashOfSeq reconstruction against the
// adopted state, which is deferred. The common case (single-ledger
// fork at the tip) is fully covered; multi-ledger divergences lower
// in history will be re-tripped on each subsequent adopt as they
// re-become the prev-seq.
func (s *Service) fixMismatchLocked(adopted *ledger.Ledger) {
	adoptedSeq := adopted.Sequence()
	if adoptedSeq == 0 {
		return
	}

	prev, havePrev := s.ledgerHistory[adoptedSeq-1]
	if !havePrev {
		// No prev-seq entry to mismatch against — nothing to do.
		return
	}
	if prev.Hash() == adopted.ParentHash() {
		// Happy path: the adopted ledger chains correctly.
		return
	}

	// Mismatch. Collect the set of seqs to purge:
	//   (a) the mismatched prev-seq itself,
	//   (b) every seq strictly greater than adoptedSeq (orphaned
	//       forward entries — their ancestry passes through prev-seq
	//       or a sibling of `adopted`, both now invalid).
	//
	// Note: seq == adoptedSeq is also purged implicitly because the
	// caller overwrites that slot with `adopted` right after we return.
	// We still collect any tx-index entries associated with it so
	// orphaned tx-hash lookups from the stale ledger don't linger.
	var toRemove []uint32
	toRemove = append(toRemove, adoptedSeq-1)
	if sameSeq, ok := s.ledgerHistory[adoptedSeq]; ok && sameSeq.Hash() != adopted.Hash() {
		toRemove = append(toRemove, adoptedSeq)
	}
	for seq := range s.ledgerHistory {
		if seq > adoptedSeq {
			toRemove = append(toRemove, seq)
		}
	}

	// Collect diagnostic info before mutation for the WARN log. A
	// fixMismatch hit is rare and operationally significant —
	// operators should be able to reconstruct exactly which history
	// slots were purged from a single log line.
	type purged struct {
		Seq       uint32
		Hash      string
		Validated bool
	}
	purgedDetails := make([]purged, 0, len(toRemove))
	validatedSeqPurged := uint32(0)
	validatedHashPurged := [32]byte{}
	hitValidated := false

	for _, seq := range toRemove {
		l, ok := s.ledgerHistory[seq]
		if !ok {
			continue
		}
		h := l.Hash()
		purgedDetails = append(purgedDetails, purged{
			Seq:       seq,
			Hash:      fmt.Sprintf("%x", h[:8]),
			Validated: l.IsValidated(),
		})
		if l.IsValidated() {
			hitValidated = true
			validatedSeqPurged = seq
			validatedHashPurged = h
		}

		// Drop tx-index entries that resolve to this invalidated seq.
		// Iteration order over a Go map is randomized; that is fine
		// here because we mutate only entries whose value equals `seq`.
		for txHash, txSeq := range s.txIndex {
			if txSeq == seq {
				delete(s.txIndex, txHash)
				delete(s.txPositionIndex, txHash)
			}
		}

		s.deleteHistoryLocked(seq)
	}

	// Defense-in-depth: if closedLedger was pointing at one of the
	// purged slots, clear it. The caller (AdoptLedgerWithState) is
	// about to reassign closedLedger = adopted anyway, but clearing
	// here ensures any intermediate read (e.g., a deferred logger
	// access) does not dereference a ledger we just invalidated.
	if s.closedLedger != nil {
		closedSeq := s.closedLedger.Sequence()
		if _, purged := s.ledgerHistory[closedSeq]; !purged && closedSeq != adoptedSeq {
			// closedLedger points at a seq we removed from history.
			if closedSeq == adoptedSeq-1 || closedSeq > adoptedSeq {
				s.closedLedger = nil
			}
		}
	}

	// Validated-ledger handling: we do NOT silently reset it. A
	// validated ledger getting invalidated by a parent-hash mismatch
	// means the node previously quorum-validated a hash that the
	// peer-adopted chain now contradicts — a serious fork that
	// requires operator attention. Log ERROR and leave the pointer
	// in place; downstream consumers will observe the divergence
	// (e.g., validatedLedger > adoptedSeq) and either re-sync or
	// surface a visible alert.
	if hitValidated {
		s.logger.Error("fixMismatch purged a validated ledger — possible fork detected",
			"adopted_seq", adoptedSeq,
			"adopted_hash", fmt.Sprintf("%x", adopted.Hash()),
			"adopted_parent_hash", fmt.Sprintf("%x", adopted.ParentHash()),
			"prev_seq", adoptedSeq-1,
			"prev_hash", fmt.Sprintf("%x", prev.Hash()),
			"purged_validated_seq", validatedSeqPurged,
			"purged_validated_hash", fmt.Sprintf("%x", validatedHashPurged),
		)
	}

	adoptedHash := adopted.Hash()
	adoptedParent := adopted.ParentHash()
	prevHash := prev.Hash()
	s.logger.Warn("fixMismatch invalidated diverged history tail",
		"adopted_seq", adoptedSeq,
		"adopted_hash", fmt.Sprintf("%x", adoptedHash[:8]),
		"adopted_parent_hash", fmt.Sprintf("%x", adoptedParent[:8]),
		"stored_prev_hash", fmt.Sprintf("%x", prevHash[:8]),
		"purged_count", len(purgedDetails),
		"purged", purgedDetails,
	)
}

// historyWindow caps the in-memory ledgerHistory + tx-index caches to
// a sliding window of recent validated ledgers. Mirrors rippled's
// default ledger-cache capacity (SizedItem::ledgerSize "large" tier =
// 256, see rippled/src/xrpld/core/detail/Config.cpp). Range-style RPC
// lookups for older sequences fall through to the relational DB; hash-
// based GetTransaction lookups beyond the window currently return
// "not found" until a DB fallback lands.
const historyWindow = 256

// evictOldHistoryLocked drops ledgerHistory entries (and their
// associated tx-index entries) with seq <= latestValidatedSeq -
// historyWindow. Caller must hold s.mu.
func (s *Service) evictOldHistoryLocked(latestValidatedSeq uint32) {
	if latestValidatedSeq <= historyWindow {
		return
	}
	cutoff := latestValidatedSeq - historyWindow
	for seq, l := range s.ledgerHistory {
		if seq > cutoff {
			continue
		}
		_ = l.ForEachTransaction(func(txHash [32]byte, _ []byte) bool {
			delete(s.txIndex, txHash)
			delete(s.txPositionIndex, txHash)
			return true
		})
		s.deleteHistoryLocked(seq)
	}
}

// putHistoryLocked installs l into ledgerHistory and keeps the by-hash
// index in sync, dropping a replaced same-sequence entry's hash. Caller
// must hold s.mu.
func (s *Service) putHistoryLocked(l *ledger.Ledger) {
	seq := l.Sequence()
	if old, ok := s.ledgerHistory[seq]; ok {
		delete(s.ledgerByHash, old.Hash())
	}
	s.ledgerHistory[seq] = l
	s.ledgerByHash[l.Hash()] = seq
}

// deleteHistoryLocked removes seq from ledgerHistory and the by-hash
// index. Caller must hold s.mu.
func (s *Service) deleteHistoryLocked(seq uint32) {
	if old, ok := s.ledgerHistory[seq]; ok {
		delete(s.ledgerByHash, old.Hash())
		delete(s.ledgerHistory, seq)
	}
}

// extractAffectedAccounts extracts account addresses affected by a transaction.
// Parses the binary transaction blob and extracts Account (sender),
// Destination (for payments, escrows, checks, etc.), and any other
// account-typed fields present in the transaction.
func extractAffectedAccounts(txData []byte) []string {
	if len(txData) == 0 {
		return nil
	}

	txJSON, err := binarycodec.Decode(hex.EncodeToString(txData))
	if err != nil {
		return nil
	}

	seen := make(map[string]struct{})
	add := func(key string) {
		if v, ok := txJSON[key].(string); ok && v != "" {
			seen[v] = struct{}{}
		}
	}

	// Primary account fields present across transaction types
	add("Account")
	add("Destination")
	add("Authorize")
	add("Unauthorize")
	add("RegularKey")
	add("Owner")
	add("Issuer")

	accounts := make([]string, 0, len(seen))
	for acc := range seen {
		accounts = append(accounts, acc)
	}
	return accounts
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

// AcceptConsensusResult closes the current open ledger using a consensus-agreed
// transaction set and close time. Unlike AcceptLedger (standalone), this method:
//   - Takes the already-agreed tx set and close time as parameters
//   - Does NOT require standalone mode
//   - Does NOT automatically validate (validation comes from the validation tracker)
//
// The parent parameter specifies which ledger to build on top of. When the
// consensus engine switches chains (wrong ledger detection), this may differ
// from s.closedLedger. The service resets its internal state accordingly.
//
// The multi-pass retry logic is the same as AcceptLedger to match rippled's
// BuildLedger behavior.
func (s *Service) AcceptConsensusResult(ctx context.Context, parent *ledger.Ledger, txBlobs [][]byte, closeTime time.Time, closeTimeCorrect bool) (uint32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closedLedger == nil {
		return 0, ErrNoClosedLedger
	}

	// If the parent differs from our closed ledger (chain switch via wrong
	// ledger detection), reset internal state to build on the correct chain.
	//
	// Sibling-fork case (issue #470): a same-seq parent with a different
	// hash is ALSO a chain switch. Skipping it leaves s.openLedger pinned
	// to the local alt's state map — subsequent close()s read the alt's
	// hashes from that map's LedgerHashes SLE and stamp them into the
	// next ledger, propagating the divergent chain in memory even after
	// the canonical sibling was adopted into ledgerHistory.
	if parent != nil && (parent.Sequence() != s.closedLedger.Sequence() || parent.Hash() != s.closedLedger.Hash()) {
		s.closedLedger = parent
		s.putHistoryLocked(parent)
		newOpen, err := ledger.NewOpen(parent, closeTime)
		if err != nil {
			return 0, fmt.Errorf("failed to create open ledger from parent: %w", err)
		}
		s.openLedger = newOpen
		// Chain switch is a clean reset, not an LCL transition: rebuild
		// the open-ledger view from scratch via New rather than Accept.
		if err := s.rebuildOpenLedgerViewLocked(); err != nil {
			return 0, err
		}
	}

	if s.openLedger == nil {
		return 0, ErrNoOpenLedger
	}

	var canonicalTxHashes []string
	var retriableTxs []openledger.PendingTx
	if len(txBlobs) > 0 {
		pending := make([]pendingTx, 0, len(txBlobs))
		for _, blob := range txBlobs {
			ptx, err := parsePendingTx(blob)
			if err != nil {
				continue
			}
			pending = append(pending, ptx)
		}

		built, err := s.buildClosedLedgerLocked(pending, closeTime, false)
		if err != nil {
			return 0, err
		}
		retriableTxs = built

		// buildClosedLedgerLocked sorts pending in place; the round-summary
		// log below reports that canonical order.
		canonicalTxHashes = make([]string, 0, len(pending))
		for _, ptx := range pending {
			canonicalTxHashes = append(canonicalTxHashes, fmt.Sprintf("%x", ptx.Hash[:8]))
		}
	} else {
		// Empty consensus tx set: buildClosedLedgerLocked is skipped, but a
		// flag-ledger close must still apply the pending NegativeUNL transition.
		if err := s.applyFlagLedgerNegativeUNL(s.openLedger); err != nil {
			return 0, err
		}
	}

	// Reset pending transactions
	s.pendingTxs = nil

	// Close the ledger with the consensus-agreed close time. Match
	// rippled's Ledger.cpp:367 — when consensus did not agree on
	// closeTime, set sLCF_NoConsensusTime so the hash matches what
	// rippled produces in the same case (Issue #361).
	var closeFlags uint8
	if !closeTimeCorrect {
		closeFlags = header.LCFNoConsensusTime
	}
	if err := s.openLedger.Close(closeTime, closeFlags); err != nil {
		return 0, fmt.Errorf("failed to close ledger: %w", err)
	}

	// Do NOT auto-validate — validation comes from the consensus validation tracker.

	// Persist. Match rippled's LedgerMaster::setFullLedger ->
	// pendSaveValidated: the bool return is discarded and the chain
	// advance proceeds regardless. Treating persist failure as fatal
	// here would diverge from rippled and risk forks on transient
	// relational-DB issues.
	// Reference: rippled/src/xrpld/app/ledger/detail/LedgerMaster.cpp:831,972
	if err := s.persistLedger(ctx, s.openLedger); err != nil {
		s.logger.Error("failed to persist consensus-closed ledger; chain advance continues",
			"seq", s.openLedger.Sequence(), "err", err)
	}

	closedSeq := s.openLedger.Sequence()
	closedLedgerHash := s.openLedger.Hash()

	// One line per locally-built ledger for diffing against rippled.
	{
		stateRoot, _ := s.openLedger.StateMapHash()
		txRoot, _ := s.openLedger.TxMapHash()
		parentHash := s.openLedger.ParentHash()
		s.logger.Info("local-built ledger round-summary",
			"t", "consensus-build",
			"event", "round-summary",
			"seq", closedSeq,
			"hash", fmt.Sprintf("%x", closedLedgerHash[:8]),
			"parent_hash", fmt.Sprintf("%x", parentHash[:8]),
			"close_time", closeTime.UTC().Format(time.RFC3339Nano),
			"close_time_correct", closeTimeCorrect,
			"close_flags", closeFlags,
			"state_root", fmt.Sprintf("%x", stateRoot[:8]),
			"tx_root", fmt.Sprintf("%x", txRoot[:8]),
			"total_drops", s.openLedger.TotalDrops(),
			"tx_count", len(txBlobs),
			"tx_hashes", canonicalTxHashes,
		)
	}

	// Mirror LedgerHistory::insert(ledger, validated) at
	// LedgerHistory.cpp:55-74 — validated entry wins for the by-seq
	// map. closedLedger reflects the local build so divergence is
	// observable via server_info/ledger_closed.
	if existing, ok := s.ledgerHistory[closedSeq]; ok && existing.Hash() != closedLedgerHash && existing.IsValidated() {
		existingHash := existing.Hash()
		s.logger.Warn("local consensus close diverges from validated ledger; preserving validated in history, keeping local-build as closedLedger reference",
			"seq", closedSeq,
			"local_hash", fmt.Sprintf("%x", closedLedgerHash[:8]),
			"validated_hash", fmt.Sprintf("%x", existingHash[:8]),
		)
		s.closedLedger = s.openLedger
	} else {
		s.closedLedger = s.openLedger
		s.putHistoryLocked(s.openLedger)
	}

	// Drain any validation that arrived before this close (validation
	// tracker leading the consensus close). Fail-safe on expired/mismatch.
	// Capture the return: when drain returns true, the adopted ledger was
	// promoted to validated in-line from the pre-stashed (seq, hash)
	// notification — no later SetValidatedLedger will arrive to fire the
	// legacy eventCallback, so we must fire it inline below (and skip
	// the hash-keyed stash, which would never be drained).
	promotedByDrain := s.drainPendingLedgerValidationLocked(closedSeq, s.closedLedger)

	// Collect transaction results for event callbacks/hooks
	var txResults []TransactionResultEvent
	if s.eventCallback != nil || (s.hooks != nil && (s.hooks.OnLedgerClosed != nil || s.hooks.OnTransaction != nil)) {
		txResults = s.collectTransactionResults(s.closedLedger, closedSeq, closedLedgerHash)
	}

	ledgerInfo, validatedLedgers, err := s.advanceToNewOpenLedgerLocked(closedSeq, closedLedgerHash, retriableTxs)
	if err != nil {
		return 0, err
	}

	// Same hook dispatch as the standalone and peer-adopt paths. The helper
	// reads each tx's real position from s.txPositionIndex (populated by
	// collectTransactionResults above) rather than reporting index 0 to every
	// `transaction` stream subscriber.
	s.fireLedgerClosedHooksLocked(ledgerInfo, txResults, closeTime, validatedLedgers)

	// In the consensus path we do NOT fire eventCallback at close time —
	// the ledger isn't yet validated. Stash the event keyed by hash so
	// SetValidatedLedger can fire it once trusted-validation quorum is
	// reached, keeping WebSocket ledgerClosed events in lockstep with
	// server_info.validated_ledger. Rippled publishes both from the
	// same quorum-gated point (pubLedger / checkAccept).
	//
	// Validation-first race exception: when the drain above promoted
	// validatedLedger in-line, the trusted validation has ALREADY arrived
	// (pre-stashed by an earlier SetValidatedLedger call). No future
	// SetValidatedLedger will land for this hash, so stashing the event
	// would orphan it forever — WebSocket `ledgerClosed` + `transaction`
	// subscribers (wired through SetEventCallback) would miss the ledger.
	// Fire the callback inline instead, matching SetValidatedLedger's own
	// drain-then-dispatch shape.
	if s.eventCallback != nil {
		event := &LedgerAcceptedEvent{
			LedgerInfo:         ledgerInfo,
			TransactionResults: txResults,
		}
		if promotedByDrain {
			// Fire on a goroutine so subscriber callbacks can't reach
			// back into s.mu (which is still held via the deferred
			// Unlock) and deadlock the service.
			callback := s.eventCallback
			go callback(event)
		} else {
			s.stashPendingValidationLocked(closedLedgerHash, event)
		}
	}

	s.logger.Info("Consensus ledger accepted",
		"sequence", closedSeq,
		"hash", fmt.Sprintf("%x", closedLedgerHash[:8]),
		"txs", len(txResults),
	)

	return closedSeq, nil
}

// SetValidatedLedger marks a ledger as validated by consensus and fires
// any stashed eventCallback for that ledger. Called by the consensus
// adaptor when the validation tracker confirms a ledger has received
// trusted-validation quorum.
//
// The expectedHash guards against fork scenarios where peers validated
// a hash different from the one we closed locally at that seq — in that
// case our local ledger is on the wrong fork and must NOT be flipped
// to validated. Matches rippled's checkAccept() which works off the
// validated ledger pointer (hash + seq), not seq alone.
func (s *Service) SetValidatedLedger(seq uint32, expectedHash [32]byte) {
	s.mu.Lock()
	l, ok := s.ledgerHistory[seq]
	// Mirrors LedgerMaster::checkAccept(hash, seq) at LedgerMaster.cpp:
	// 904-918 — hash-keyed in rippled; our seq-keyed map splits into
	// "no entry" or "entry-with-different-hash" (same-height fork).
	// Both stash and arm acquisition.
	if !ok || l.Hash() != expectedHash {
		s.stashPendingLedgerValidationLocked(seq, expectedHash)
		// Capture handler under lock; fire when seq > the last
		// VALIDATED seq (mirrors rippled LedgerMaster::checkAccept's
		// `if (seq < mValidLedgerSeq) return` gate at
		// LedgerMaster.cpp:883). Gating on closedSeq instead silently
		// blocked recovery when a node ran ahead on a private chain:
		// quorum on a divergent canonical seq=N would stash but the
		// arming handler refused to fire because closedSeq >> N.
		var (
			handler func(uint32, [32]byte)
			fire    bool
		)
		if s.onPendingValidationStashed != nil {
			validatedSeq := uint32(0)
			if s.validatedLedger != nil {
				validatedSeq = s.validatedLedger.Sequence()
			}
			if seq > validatedSeq {
				handler = s.onPendingValidationStashed
				fire = true
			}
		}
		s.mu.Unlock()
		if fire {
			go handler(seq, expectedHash)
		}
		return
	}
	_ = l.SetValidated()
	s.validatedLedger = l
	s.evictOldHistoryLocked(seq)

	// Sweep the held local pool against the just-validated ledger.
	// Mirrors LedgerMaster::setValidLedger → app_.getOPs().updateLocalTx(*l)
	// at LedgerMaster.cpp:283. Sweeping here (not on every consensus close)
	// avoids dropping held txs against a ledger consensus later abandons.
	pool := s.localTxs
	event := s.drainPendingValidationLocked(expectedHash)
	callback := s.eventCallback
	s.mu.Unlock()

	// Fold the just-validated ledger into the amendment table outside the lock
	// (the table has its own mutex). Mirrors LedgerMaster::doValidatedLedger.
	s.syncAmendmentTable(l)

	if pool != nil {
		pool.Sweep(l)
	}

	if event != nil && callback != nil {
		go callback(event)
	}
}

// pendingValidationMaxLen caps the pending-validation stash so a node
// that never reaches quorum (misconfigured UNL, network partition) can't
// leak memory. At 3s ledger close, 256 entries ≈ 13 minutes — large
// enough to cover extended catch-up without evicting in-flight quorum
// notifications (issue #395).
const pendingValidationMaxLen = 256

// stashPendingValidationLocked stores an accepted event keyed by hash
// for later eventCallback dispatch once the ledger is fully validated.
// LRU-evicts the oldest entry if the stash would exceed its cap.
// Caller must hold s.mu.
func (s *Service) stashPendingValidationLocked(hash [32]byte, event *LedgerAcceptedEvent) {
	if _, exists := s.pendingValidation[hash]; !exists {
		s.pendingValidationOrder = append(s.pendingValidationOrder, hash)
	}
	s.pendingValidation[hash] = event

	for len(s.pendingValidationOrder) > pendingValidationMaxLen {
		oldest := s.pendingValidationOrder[0]
		s.pendingValidationOrder = s.pendingValidationOrder[1:]
		// Silently losing the oldest pending event when the cap is hit
		// means a LedgerAcceptedEvent never fires for that hash even if
		// it later reaches quorum — a failure mode that doesn't exist
		// in rippled. Log via the service's configured logger at warn
		// level so an operator noticing a stuck-validation issue can
		// see it; keep the cap in place so a node that never reaches
		// quorum (bad UNL, partition) can't leak memory.
		if s.logger != nil {
			s.logger.Warn("pendingValidation LRU drop — event lost for this ledger hash",
				"hash", fmt.Sprintf("%x", oldest[:8]),
				"cap", pendingValidationMaxLen,
			)
		}
		delete(s.pendingValidation, oldest)
	}
}

// drainPendingValidationLocked removes and returns the stashed event
// for the given hash, or nil if none exists. Caller must hold s.mu.
func (s *Service) drainPendingValidationLocked(hash [32]byte) *LedgerAcceptedEvent {
	event, ok := s.pendingValidation[hash]
	if !ok {
		return nil
	}
	delete(s.pendingValidation, hash)
	for i, h := range s.pendingValidationOrder {
		if h == hash {
			s.pendingValidationOrder = append(s.pendingValidationOrder[:i], s.pendingValidationOrder[i+1:]...)
			break
		}
	}
	return event
}

// pendingValidationEntry records a trusted-validation notification that
// arrived for a ledger sequence not yet present in ledgerHistory. The
// `at` timestamp TTL-guards the entry: if the adopt/close path races
// far enough behind the validation tracker that quorum gossip has gone
// stale, the entry is discarded on drain rather than silently promoting.
type pendingValidationEntry struct {
	expectedHash [32]byte
	at           time.Time
}

// pendingValidationTTL bounds how long a stashed validation is
// considered fresh enough to promote on later adopt/close. The
// 10-minute window covers deep-gap catchup, where backward-chain
// adoption walks one hop per peer round-trip — "validation arrived
// for seq N" to "ledger at seq N adopted" can take several minutes.
// pendingValidationMaxLen=256 already bounds memory and the on-drain
// hash check guarantees fork safety, so a generous TTL is safe.
const pendingValidationTTL = 10 * time.Minute

// stashPendingLedgerValidationLocked stores a (seq, expectedHash, at) entry
// for later drain when ledgerHistory[seq] is populated. LRU-evicts the
// oldest entry if the stash would exceed pendingValidationMaxLen.
// Caller must hold s.mu.
func (s *Service) stashPendingLedgerValidationLocked(seq uint32, expectedHash [32]byte) {
	if _, exists := s.pendingLedgerValidations[seq]; !exists {
		s.pendingLedgerValidationsOrder = append(s.pendingLedgerValidationsOrder, seq)
	}
	s.pendingLedgerValidations[seq] = pendingValidationEntry{
		expectedHash: expectedHash,
		at:           time.Now(),
	}

	for len(s.pendingLedgerValidationsOrder) > pendingValidationMaxLen {
		oldest := s.pendingLedgerValidationsOrder[0]
		s.pendingLedgerValidationsOrder = s.pendingLedgerValidationsOrder[1:]
		// Silently losing the oldest pending validation when the cap is
		// hit means a ledger that later adopts at this seq won't be
		// promoted to validated by this (already-delivered) quorum
		// notification. Log via the service's configured logger at warn
		// level so an operator noticing a stuck-validation issue can see
		// it; keep the cap in place so a node where adoption never
		// catches up (disconnected peer, partition) can't leak memory.
		if s.logger != nil {
			s.logger.Warn("pendingLedgerValidations LRU drop — validation lost for this seq",
				"seq", oldest,
				"cap", pendingValidationMaxLen,
			)
		}
		delete(s.pendingLedgerValidations, oldest)
	}
}

// drainPendingLedgerValidationLocked checks for a stashed validation at
// the given seq and, if present, removes it. If the entry matches the
// adopted hash AND has not exceeded pendingValidationTTL, the adopted
// ledger is promoted to validated and the promotion is reflected in
// s.validatedLedger. Returns true when a promotion occurred so callers
// can log / emit events accordingly. Caller must hold s.mu.
//
// Expired or hash-mismatched entries are always deleted — leaving them
// in place would let a later adopt at the same seq accidentally match
// a stale notification.
func (s *Service) drainPendingLedgerValidationLocked(seq uint32, adopted *ledger.Ledger) bool {
	entry, ok := s.pendingLedgerValidations[seq]
	if !ok {
		return false
	}
	delete(s.pendingLedgerValidations, seq)
	for i, q := range s.pendingLedgerValidationsOrder {
		if q == seq {
			s.pendingLedgerValidationsOrder = append(s.pendingLedgerValidationsOrder[:i], s.pendingLedgerValidationsOrder[i+1:]...)
			break
		}
	}

	if time.Since(entry.at) >= pendingValidationTTL {
		// Expired: gossip is too old to trust. A fresh SetValidatedLedger
		// call will re-stash / re-promote if the validation is still
		// current on the trusted-validation tracker's side.
		return false
	}
	if adopted.Hash() != entry.expectedHash {
		// Fork signal: peers validated a different hash at this seq
		// than the one we just adopted. Refuse to promote; the adopted
		// ledger is on the wrong fork from the quorum's perspective.
		return false
	}

	_ = adopted.SetValidated()
	s.validatedLedger = adopted
	s.evictOldHistoryLocked(seq)
	return true
}

// pendingAdopt is the payload of a held replay-delta adoption waiting
// for its parent seq to land. Carries the exact inputs
// AdoptLedgerWithState needs so the cascade can apply the held ledger
// without re-fetching anything.
type pendingAdopt struct {
	header   *header.LedgerHeader
	stateMap *shamap.SHAMap
	txMap    *shamap.SHAMap
	at       time.Time
}

// heldAdoptionTTL bounds how long a held adoption is kept before
// eviction. 5 minutes accommodates a long backward-chain catch-up
// from a divergent local fork — a goxrpl-1 enclave run reproduced
// a wedged node where a 30-ledger fork couldn't recover because
// intermediate held entries TTL-evicted at 60s while the cascade
// was still walking back to a common ancestor. The window is bounded
// to keep a stale fork / disconnected-peer response from lingering
// indefinitely and re-firing against an unrelated adopted ledger.
const heldAdoptionTTL = 5 * time.Minute

// heldAdoptionCascadeMax caps the cascade recursion depth. Real-world
// cascades are 1-2 hops deep (replay-delta is single-ledger-per-
// request). The cap is purely a DoS guard: a malicious peer-stream that
// seeded a deep chain of held orphans pre-adoption would otherwise
// push arbitrary stack depth into the adopt path. 256 is two orders of
// magnitude above any legitimate cascade length.
const heldAdoptionCascadeMax = 256

// SubmitHeldAdoptionResult describes the disposition of a candidate
// ledger passed to SubmitHeldAdoption. When Stashed is true the caller
// should arm a backward acquisition for (ParentSeq, ParentHash) — without
// that, the stash entry will age out at heldAdoptionTTL (issue #397).
type SubmitHeldAdoptionResult struct {
	// Adopted means the awaited parent was already in history at the
	// expected hash and the candidate was fast-pathed into the adopt.
	Adopted bool

	// Stashed means the candidate is parked in the held-adoption stash
	// pending cascade-promotion at the parent seq.
	Stashed bool

	// ParentSeq, ParentHash describe the awaited parent. Set whenever
	// h.LedgerIndex > 1, regardless of outcome.
	ParentSeq  uint32
	ParentHash [32]byte
}

// SubmitHeldAdoption routes a fetched replay-delta either to immediate
// adoption (when the awaited parent seq is already in history and its
// hash matches the supplied ParentHash) or to the held-orphan stash
// (keyed by the awaited parent seq = h.LedgerIndex - 1). Stashed
// entries are cascade-adopted later, from inside AdoptLedgerWithState
// at the parent seq, when the adopted hash matches ParentHash.
//
// Safe to call concurrently. Nil header or nil stateMap is rejected;
// nil txMap is allowed (legacy catchup path — AdoptLedgerWithState
// falls back to the genesis-shaped empty tx map).
//
// Mirrors rippled's tryAdvance cascade shape, flattened to single-hop
// (see comment on heldAdoptions for the scope trade-off).
func (s *Service) SubmitHeldAdoption(ctx context.Context, h *header.LedgerHeader, stateMap *shamap.SHAMap, txMap *shamap.SHAMap) (SubmitHeldAdoptionResult, error) {
	if h == nil {
		return SubmitHeldAdoptionResult{}, errors.New("SubmitHeldAdoption: nil header")
	}
	if stateMap == nil {
		return SubmitHeldAdoptionResult{}, errors.New("SubmitHeldAdoption: nil state map")
	}

	res := SubmitHeldAdoptionResult{}
	if h.LedgerIndex > 1 {
		res.ParentSeq = h.LedgerIndex - 1
		res.ParentHash = h.ParentHash
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Evict stale entries on every submission so an operator that
	// repeatedly submits orphans doesn't keep a stale entry alive.
	s.evictExpiredHeldAdoptionsLocked()

	// Fast path: if the awaited parent is already in history at the
	// expected hash, adopt immediately rather than stashing for a
	// cascade that will never re-fire. Genesis (seq 1) has no parent,
	// so the fast path is skipped for seq <= 1; the adopt itself will
	// error downstream if anything is wrong.
	if h.LedgerIndex > 1 {
		parentSeq := h.LedgerIndex - 1
		if parent, ok := s.ledgerHistory[parentSeq]; ok {
			parentHash := parent.Hash()
			if parentHash == h.ParentHash {
				if err := s.adoptLedgerWithStateLocked(ctx, h, stateMap, txMap, 0); err != nil {
					return res, err
				}
				res.Adopted = true
				return res, nil
			}
			// Parent seq present on a different fork — stash; cascade
			// will adopt when the awaited parent arrives and
			// fixMismatchLocked clears the mismatched tail
			// (LedgerMaster.cpp:749-801 setFullLedger pattern). Never
			// pre-emptively delete without a verified anchor.
			s.logger.Info("SubmitHeldAdoption divergent-parent submission stashed",
				"seq", h.LedgerIndex,
				"parent_seq", parentSeq,
				"parent_have", fmt.Sprintf("%x", parentHash[:8]),
				"parent_want", fmt.Sprintf("%x", h.ParentHash[:8]),
			)
		}
	}

	// Parent not yet present — stash.
	s.heldAdoptions[h.LedgerIndex-1] = &pendingAdopt{
		header:   h,
		stateMap: stateMap,
		txMap:    txMap,
		at:       time.Now(),
	}
	res.Stashed = true
	return res, nil
}

// cascadeHeldAdoptionsLocked promotes a held child whose awaited parent
// seq (h.LedgerIndex for the child's key) just finished adopting. If the
// held entry's ParentHash matches the adopted hash, it is removed from
// the stash and adopted via adoptLedgerWithStateLocked — which itself
// re-invokes cascadeHeldAdoptionsLocked, giving a bounded recursive
// walk through any chain of pre-stashed orphans.
//
// Entries older than heldAdoptionTTL are evicted on every call (not
// just on the matched key) so a pathological peer that seeds a stash
// full of stale forks can't defer eviction forever.
//
// Caller must hold s.mu (write).
func (s *Service) cascadeHeldAdoptionsLocked(ctx context.Context, adopted *ledger.Ledger, depth int) {
	// Purge stale entries first so a single adopt sweeps them all out.
	s.evictExpiredHeldAdoptionsLocked()

	if depth >= heldAdoptionCascadeMax {
		s.logger.Warn("cascadeHeldAdoptions: hit recursion cap — refusing further promotion",
			"cap", heldAdoptionCascadeMax,
			"seq", adopted.Sequence(),
		)
		return
	}

	parentSeq := adopted.Sequence()
	held, ok := s.heldAdoptions[parentSeq]
	if !ok {
		return
	}
	delete(s.heldAdoptions, parentSeq)

	adoptedHash := adopted.Hash()
	if held.header.ParentHash != adoptedHash {
		// The held orphan expected a different parent hash at this seq
		// — it was on a divergent fork. Drop it rather than adopting
		// onto the wrong chain.
		s.logger.Warn("cascadeHeldAdoptions: dropping fork-mismatched held entry",
			"seq", held.header.LedgerIndex,
			"parent_have", fmt.Sprintf("%x", adoptedHash[:8]),
			"parent_want", fmt.Sprintf("%x", held.header.ParentHash[:8]),
		)
		return
	}

	s.logger.Info("cascadeHeldAdoptions: promoting held orphan",
		"seq", held.header.LedgerIndex,
		"hash", fmt.Sprintf("%x", held.header.Hash[:8]),
		"depth", depth+1,
	)
	if err := s.adoptLedgerWithStateLocked(ctx, held.header, held.stateMap, held.txMap, depth+1); err != nil {
		// Adoption of the held entry failed (e.g. persistence error on
		// the cascade hop). Log and stop — the outer adopt already
		// succeeded, so we do not surface the cascade error upwards.
		s.logger.Error("cascadeHeldAdoptions: held-entry adopt failed",
			"seq", held.header.LedgerIndex,
			"err", err,
		)
	}
}

// evictExpiredHeldAdoptionsLocked removes held entries whose `at`
// timestamp is older than heldAdoptionTTL. Caller must hold s.mu.
func (s *Service) evictExpiredHeldAdoptionsLocked() {
	if len(s.heldAdoptions) == 0 {
		return
	}
	now := time.Now()
	for key, held := range s.heldAdoptions {
		if now.Sub(held.at) >= heldAdoptionTTL {
			s.logger.Warn("heldAdoption TTL eviction",
				"parent_seq", key,
				"child_seq", held.header.LedgerIndex,
				"age", now.Sub(held.at),
			)
			delete(s.heldAdoptions, key)
		}
	}
}

// NeedsInitialSync returns true if the node hasn't yet adopted a ledger from peers.
func (s *Service) NeedsInitialSync() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.needsInitialSync
}

// AdoptLedgerHeader adopts a peer's ledger header as our closed ledger.
// Used during initial sync: the node fetches the network's current ledger
// header and starts tracking from there.
// The state map is reused from genesis (valid as long as no transactions
// have changed the state — true for empty ledger sequences).
func (s *Service) AdoptLedgerHeader(h *header.LedgerHeader) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.needsInitialSync {
		return errors.New("not in initial sync mode")
	}

	if s.genesisLedger == nil {
		return errors.New("no genesis ledger available")
	}

	// Snapshot the genesis state map for the adopted ledger
	stateMap, err := s.genesisLedger.StateMapSnapshot()
	if err != nil {
		return fmt.Errorf("failed to snapshot genesis state: %w", err)
	}

	// Update LedgerHashes skiplist so state matches rippled
	if err := ledger.UpdateSkipListOnMap(stateMap, h.LedgerIndex, h.ParentHash); err != nil {
		s.logger.Warn("failed to update skip list during adoption", "error", err)
	}

	// Create empty tx map
	txMap, err := s.genesisLedger.TxMapSnapshot()
	if err != nil {
		return fmt.Errorf("failed to snapshot genesis tx map: %w", err)
	}

	// Create the adopted ledger from the peer's header.
	adopted := ledger.NewFromHeader(*h, stateMap, txMap, drops.Fees{})

	// Update service state. The adopted ledger becomes our closed
	// ledger and joins history, but we do NOT mark it validated —
	// we haven't yet received trusted-validation quorum for this
	// hash ourselves. Matches rippled's sync behavior: a freshly
	// adopted ledger is merely a starting point for tracking;
	// validated_ledger advances later, when the first consensus
	// round whose outcome we can quorum-validate completes.
	//
	// validatedLedger stays at whatever it was before adoption
	// (typically genesis for a first-time sync) until the
	// ValidationTracker fires OnLedgerFullyValidated. Source
	// closedLedger from the install helper's return so the
	// validated-precedence skip keeps closedLedger canonical.
	s.closedLedger = s.installAdoptedLedgerLocked(h.LedgerIndex, adopted)

	// Create new open ledger on top
	openLedger, err := ledger.NewOpen(s.closedLedger, time.Now())
	if err != nil {
		return fmt.Errorf("failed to create open ledger: %w", err)
	}
	s.openLedger = openLedger
	s.needsInitialSync = false

	// Adopt-from-peer is a fresh start, not an LCL transition — rebuild
	// the open-ledger view via New rather than Accept (no prior
	// node-local current view applies to the freshly adopted closed).
	if err := s.rebuildOpenLedgerViewLocked(); err != nil {
		return err
	}

	s.logger.Info("Adopted ledger from peer",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
	)

	return nil
}

// ReAdoptLedgerHeader re-adopts a peer's ledger header while catching up.
// Unlike AdoptLedgerHeader, this works after needsInitialSync has been cleared.
// Used during the catch-up phase when we're still behind the network.
func (s *Service) ReAdoptLedgerHeader(h *header.LedgerHeader) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.genesisLedger == nil {
		return errors.New("no genesis ledger available")
	}

	// Only allow re-adoption if the new sequence is ahead of our current
	if s.closedLedger != nil && h.LedgerIndex <= s.closedLedger.Sequence() {
		return fmt.Errorf("re-adopt seq %d not ahead of current %d", h.LedgerIndex, s.closedLedger.Sequence())
	}

	// Snapshot from the closed ledger so the skiplist accumulates across re-adoptions
	source := s.closedLedger
	if source == nil {
		source = s.genesisLedger
	}
	stateMap, err := source.StateMapSnapshot()
	if err != nil {
		return fmt.Errorf("failed to snapshot state: %w", err)
	}

	// Update LedgerHashes skiplist so state matches rippled
	if err := ledger.UpdateSkipListOnMap(stateMap, h.LedgerIndex, h.ParentHash); err != nil {
		s.logger.Warn("failed to update skip list during re-adoption", "error", err)
	}

	txMap, err := s.genesisLedger.TxMapSnapshot()
	if err != nil {
		return fmt.Errorf("failed to snapshot genesis tx map: %w", err)
	}

	adopted := ledger.NewFromHeader(*h, stateMap, txMap, drops.Fees{})

	// Advance closedLedger to the peer's tip, but do NOT advance
	// validatedLedger here — peers serve us ledgers they themselves
	// closed, and "closed" is not "validated". Rippled's LedgerMaster
	// distinguishes the two, and server_info.validated_ledger is only
	// set after trusted-validation quorum lands. Leaving validatedLedger
	// alone lets the quorum gate in SetValidatedLedger do its job.
	s.closedLedger = s.installAdoptedLedgerLocked(h.LedgerIndex, adopted)

	// Create new open ledger on top
	openLedger, err := ledger.NewOpen(s.closedLedger, time.Now())
	if err != nil {
		return fmt.Errorf("failed to create open ledger: %w", err)
	}
	s.openLedger = openLedger
	s.pendingTxs = nil

	// Re-adopt: fresh start on the peer's tip — rebuild via New.
	if err := s.rebuildOpenLedgerViewLocked(); err != nil {
		return err
	}

	s.logger.Info("Re-adopted ledger from peer",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
	)

	return nil
}

// AdoptLedgerWithState adopts a ledger using a fully-fetched state map from a peer.
// Unlike AdoptLedgerHeader which reuses genesis state, this uses the real state tree
// fetched via the TMGetLedger/TMLedgerData protocol.
//
// txMap is the verified transaction SHAMap when arriving via the
// replay-delta path (rippled LedgerDeltaAcquire installs the peer-
// provided tx-blob tree at LedgerDeltaAcquire.cpp:209). Pass nil for
// header-only state catchup, in which case we reuse genesis's empty
// tx map — matches pre-replay-delta behavior. Dropping the peer-
// provided tx map on replay-delta adoption (the pre-R5.1 bug) left
// `tx`, `tx_history`, `account_tx`, `transaction_entry` RPCs unable
// to answer queries against adopted ledgers, and prevented re-serving
// replay-delta requests for those ledgers to other peers.
func (s *Service) AdoptLedgerWithState(ctx context.Context, h *header.LedgerHeader, stateMap *shamap.SHAMap, txMap *shamap.SHAMap) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adoptLedgerWithStateLocked(ctx, h, stateMap, txMap, 0)
}

// adoptLedgerWithStateLocked is the lock-free core of AdoptLedgerWithState.
// Caller must hold s.mu (write). `cascadeDepth` is the current recursion
// depth of the held-orphan cascade (F6); the public entrypoints pass 0
// and the cascade helper recurses with depth+1 until heldAdoptionCascadeMax.
func (s *Service) adoptLedgerWithStateLocked(
	ctx context.Context,
	h *header.LedgerHeader,
	stateMap *shamap.SHAMap,
	txMap *shamap.SHAMap,
	cascadeDepth int,
) error {
	if s.genesisLedger == nil {
		return errors.New("no genesis ledger available")
	}

	// Use the caller-supplied tx map when available (replay-delta
	// adoption path); fall back to an empty genesis-shaped tx map for
	// the header-only state catchup path that has no per-ledger tx
	// content to install.
	if txMap == nil {
		empty, err := s.genesisLedger.TxMapSnapshot()
		if err != nil {
			return fmt.Errorf("failed to snapshot empty tx map: %w", err)
		}
		txMap = empty
	}

	adopted := ledger.NewFromHeader(*h, stateMap, txMap, drops.Fees{})

	// F5: before installing the adopted ledger into history, check
	// whether it chains to whatever we already have at seq-1. If the
	// parent-hash doesn't match, we're on a divergent fork relative to
	// what the peer served — invalidate the tail (prev-seq + every
	// orphaned forward entry) so subsequent RPCs don't resolve against
	// stale state. Mirrors rippled LedgerMaster::setFullLedger's
	// parent-hash sanity check and fixMismatch() call at
	// LedgerMaster.cpp:849-862.
	s.fixMismatchLocked(adopted)

	// Install into ledgerHistory[seq]; only ADVANCE closedLedger on
	// strict seq increase. Backward-chain cascade fills must not
	// regress the closed-reference pointer.
	canonical := s.installAdoptedLedgerLocked(h.LedgerIndex, adopted)
	advanced := false
	if s.closedLedger == nil || canonical.Sequence() > s.closedLedger.Sequence() {
		s.closedLedger = canonical
		advanced = true
	} else if canonical.Sequence() == s.closedLedger.Sequence() && canonical.Hash() != s.closedLedger.Hash() {
		// Sibling-fork resolution (issue #470): a same-seq adoption with a
		// different hash means the peer's chain replaces our locally-built
		// alt at this tip. closedLedger must point at the adopted entry,
		// otherwise subsequent local builds keep snapshotting the alt's
		// state map (whose LedgerHashes SLE encodes the alt-chain's hashes
		// for ancestors) and emit divergent ledgers forever.
		s.closedLedger = canonical
		advanced = true
	}
	s.needsInitialSync = false

	// Install-skipped: validated entry already at this seq with a
	// different hash. Skip persist/drain/collect/hooks — those ran
	// for the canonical entry.
	if canonical != adopted {
		openLedger, err := ledger.NewOpen(canonical, time.Now())
		if err != nil {
			return fmt.Errorf("failed to create open ledger after adopt-skip: %w", err)
		}
		s.openLedger = openLedger
		if advanced {
			if err := s.rebuildOpenLedgerViewLocked(); err != nil {
				return err
			}
		}
		canonicalHash := canonical.Hash()
		s.logger.Info("Adopted ledger from peer (skip: validated entry kept)",
			"seq", h.LedgerIndex,
			"adopt_hash", fmt.Sprintf("%x", h.Hash[:8]),
			"canonical_hash", fmt.Sprintf("%x", canonicalHash[:8]),
		)
		return nil
	}

	// If a trusted validation for this seq arrived before we got here
	// (validation tracker leading the adopt loop), drain the stash and
	// promote on match. The drain is fail-safe: expired or
	// hash-mismatched entries are deleted without promoting. Capture the
	// return: when drain returns true, the hash-keyed eventCallback stash
	// below must be skipped and the callback fired inline — see the
	// comment at the callback-dispatch block for the full rationale.
	promotedByDrain := s.drainPendingLedgerValidationLocked(h.LedgerIndex, adopted)

	// Persist the adopted ledger exactly as the local close path does so
	// tx/account_tx/tx_history/transaction_entry RPCs can answer queries
	// against it. Matches LedgerMaster::setFullLedger -> pendSaveValidated.
	if err := s.persistLedger(ctx, adopted); err != nil {
		// Degrade gracefully: the in-memory state is still correct and the
		// next consensus close will re-try persistence. Log loudly because
		// a persistent failure breaks tx RPCs silently.
		s.logger.Error("Failed to persist adopted ledger", "seq", h.LedgerIndex, "err", err)
	}

	// Populate the in-memory tx-index and capture per-tx event records
	// so hooks.OnTransaction + stream subscribers see every adopted tx.
	// collectTransactionResults walks the tx map and writes to s.txIndex
	// + s.txPositionIndex as a side effect AND returns the per-tx
	// TransactionResultEvent slice that hook dispatch needs.
	txResults := s.collectTransactionResults(adopted, h.LedgerIndex, h.Hash)

	// Rebuild openLedger only on forward adoption — backward-fills must
	// not regress the engine's open view. Per-seq persist/hooks fire below
	// regardless.
	if advanced {
		openLedger, err := ledger.NewOpen(adopted, time.Now())
		if err != nil {
			return fmt.Errorf("failed to create open ledger: %w", err)
		}
		s.openLedger = openLedger
		// Forward-advance adopt = fresh start on the peer's tip.
		// Rebuild via New so the persistent view re-anchors on adopted.
		if err := s.rebuildOpenLedgerViewLocked(); err != nil {
			return err
		}
	}

	// Fire hooks.OnLedgerClosed + hooks.OnTransaction so WebSocket
	// `ledger` and `transactions` stream subscribers see peer-adopted
	// ledgers. Without this, the streams silently skip every ledger
	// the node catches up to — an observable divergence from rippled,
	// whose pubLedger path fires for both consensus-closed and sync-
	// adopted ledgers.
	ledgerInfo := &LedgerInfo{
		Sequence:   h.LedgerIndex,
		Hash:       h.Hash,
		ParentHash: adopted.ParentHash(),
		CloseTime:  adopted.CloseTime(),
		TotalDrops: adopted.TotalDrops(),
		Validated:  adopted.IsValidated(),
		Closed:     adopted.IsClosed(),
	}
	validatedLedgers := s.getValidatedLedgersRange()
	// Peer-adopted ledgers carry a close time from the adopted header,
	// not from local consensus — use adopted.CloseTime() so downstream
	// subscribers see the network-agreed close time (matches the Header
	// field that was just populated by NewFromHeader).
	s.fireLedgerClosedHooksLocked(ledgerInfo, txResults, adopted.CloseTime(), validatedLedgers)

	// The legacy eventCallback is meant to fire on *validated*, not
	// *closed*. Peer-adopted ledgers advance s.closedLedger but not
	// s.validatedLedger (the quorum gate at SetValidatedLedger owns
	// that transition). Stash the event keyed by hash so the next
	// SetValidatedLedger(seq, hash) for this ledger drains it —
	// the exact same pattern AcceptConsensusResult uses.
	//
	// Validation-first race exception: when the F4 drain above promoted
	// validatedLedger in-line from a pre-stashed (seq, hash) notification,
	// no future SetValidatedLedger will arrive for this hash. Stashing
	// here would orphan the event forever — WebSocket `ledgerClosed` +
	// `transaction` subscribers (wired through SetEventCallback) would
	// silently miss the ledger. Fire the callback inline instead, matching
	// SetValidatedLedger's own drain-then-dispatch shape. Skipping the
	// stash also prevents a double-fire if a late-duplicate
	// SetValidatedLedger arrives for the same hash.
	if s.eventCallback != nil {
		event := &LedgerAcceptedEvent{
			LedgerInfo:         ledgerInfo,
			TransactionResults: txResults,
		}
		if promotedByDrain {
			// Fire on a goroutine so subscriber callbacks can't reach
			// back into s.mu (still held via the caller's defer) and
			// deadlock the service.
			callback := s.eventCallback
			go callback(event)
		} else {
			s.stashPendingValidationLocked(h.Hash, event)
		}
	}

	s.logger.Info("Adopted ledger with full state from peer",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
		"account_hash", fmt.Sprintf("%x", h.AccountHash[:8]),
	)

	// F6: cascade any held adoption that was waiting on this ledger to
	// land. Out-of-order replay-delta completions (seq N+2 arriving
	// before seq N+1) otherwise stall until the inbound loop happens to
	// re-request them. Also evicts entries older than heldAdoptionTTL so
	// the stash doesn't accumulate stale forks across adopt calls.
	s.cascadeHeldAdoptionsLocked(ctx, adopted, cascadeDepth)

	return nil
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

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	appconfig "github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/localtxs"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/tx"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

var (
	ErrConsensusParentMismatch = errors.New("consensus parent does not match the closed ledger")
	ErrPreferredChainSwitch    = errors.New("invalid preferred chain switch")
	ErrInvalidLocalTransaction = errors.New("transaction failed local checks")
)

// SignatureVerificationError retains the immutable rules snapshot used by an
// ingress signature check so callers can scope any admission memo to that
// snapshot.
type SignatureVerificationError struct {
	Rules *amendment.Rules
	Err   error
}

// IngressMetricsSnapshot is the cumulative view of detached private ingress
// work. Durations are sums in nanoseconds represented as time.Duration values;
// callers can derive rates or averages without retaining transaction state.
type IngressMetricsSnapshot struct {
	PrivateAttempts        uint64
	PrivateAttemptDuration time.Duration
	StaleRetries           uint64
	StaleRetryDuration     time.Duration
	Reparses               uint64
	ReparseDuration        time.Duration
}

func (e *SignatureVerificationError) Error() string {
	if e == nil || e.Err == nil {
		return "signature verification failed"
	}
	return e.Err.Error()
}

func (e *SignatureVerificationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func signatureVerificationError(err error, rules *amendment.Rules) error {
	if err == nil {
		return nil
	}
	return &SignatureVerificationError{Rules: rules, Err: err}
}

// Config holds configuration for the LedgerService
type Config struct {
	RelayMemory       MemoryBudget
	PersistenceMemory shamap.DirtyMemoryBudget
	// SpeculativeMemory isolates ingress and successors from persistence's
	// progress reserve. Nil uses PersistenceMemory for embedded callers.
	SpeculativeMemory shamap.DirtyMemoryBudget
	// ReplayVerificationMemory isolates long-running cold reads from ledger
	// persistence. Node wiring must provide a separate shared-pool account so a
	// verifier cannot consume the workspace needed to persist its own parent.
	// Nil retains the legacy PersistenceMemory accounting for embedded callers.
	ReplayVerificationMemory shamap.DirtyMemoryBudget
	ReplayFaultPath          string
	Standalone               bool
	Startup                  StartupConfig
	// NodeSize selects rippled's cache sweep cadence. Empty uses the medium
	// profile, matching the top-level configuration default.
	NodeSize string
	// SweepInterval overrides the node-size sweep cadence when positive.
	SweepInterval time.Duration
	// FetchDepth limits historical ledger serving relative to the closed ledger.
	// Zero leaves serving unrestricted.
	FetchDepth uint32
	// LedgerCacheSize bounds both recent ledger history and persisted-ledger lookups.
	// Zero selects config.DefaultLedgerCacheSize.
	LedgerCacheSize uint32

	// NetworkID is the network identifier for this node.
	// Legacy networks (ID <= 1024) reject transactions that include NetworkID.
	// New networks (ID > 1024) require NetworkID in transactions.
	NetworkID uint32

	GenesisConfig genesis.Config
	// ConfiguredFees supplies the signing fallback and seeds absent FeeSettings fields.
	ConfiguredFees *drops.Fees

	// NodeStore is the persistent storage for ledger nodes (optional, nil for in-memory only)
	NodeStore nodestore.Database

	// SHAMapFamily loads and stores state and transaction tree nodes.
	SHAMapFamily shamap.Family
	// FastLoad restores the newest complete persisted ledger at startup.
	FastLoad bool
	// FastLoadWorkers controls persisted SHAMap verification concurrency.
	// Zero selects an automatic value.
	FastLoadWorkers int
	// RelationalDB is the repository manager for transaction indexing (optional)
	RelationalDB relationaldb.RepositoryManager

	// Logger is the logger for the ledger service.
	// If nil, xrpllog.Discard() is used.
	Logger xrpllog.Logger

	// Table, when supplied, is the live amendment table the service
	// folds each validated flag ledger into (enabled set + majority projection +
	// blocked state). Optional — nil disables amendment-table resync.
	Table *amendment.Table

	// TxQ optionally overrides the transaction-queue configuration
	// (built from the operator's [transaction_queue] stanza via
	// TxQConfigFromTuning). Nil means use txq.DefaultConfig — or
	// txq.StandaloneConfig in standalone mode.
	TxQ *txq.Config
}

type networkLedgerState uint8

const (
	networkLedgerReady networkLedgerState = iota
	networkLedgerNeeded
	networkLedgerFastLoadProvisional
)

func networkLedgerStateFor(enabled bool, state networkLedgerState) networkLedgerState {
	if enabled {
		return state
	}
	return networkLedgerReady
}

type ingressMetrics struct {
	privateAttempts     atomic.Uint64
	privateAttemptNanos atomic.Uint64
	staleRetries        atomic.Uint64
	staleRetryNanos     atomic.Uint64
	reparses            atomic.Uint64
	reparseNanos        atomic.Uint64
}

func (m *ingressMetrics) snapshot() IngressMetricsSnapshot {
	if m == nil {
		return IngressMetricsSnapshot{}
	}
	return IngressMetricsSnapshot{
		PrivateAttempts:        m.privateAttempts.Load(),
		PrivateAttemptDuration: time.Duration(m.privateAttemptNanos.Load()),
		StaleRetries:           m.staleRetries.Load(),
		StaleRetryDuration:     time.Duration(m.staleRetryNanos.Load()),
		Reparses:               m.reparses.Load(),
		ReparseDuration:        time.Duration(m.reparseNanos.Load()),
	}
}

// Service manages the ledger lifecycle
type Service struct {
	replayRepairTarget *ledger.Ledger
	replayAuthenticate func(header.LedgerHeader) bool
	replayAcquiredHash [32]byte
	replayFaults       *replayfault.Store
	replayVerifiedHash [32]byte
	replayRecoveryMu   sync.Mutex
	// replayRecoveryCancel is owned by lifecycleMu. It is set before a
	// recovery worker is launched and canceled when Stop begins so an admitted
	// recovery cannot outlive the service.
	replayRecoveryCancel context.CancelFunc
	replayRepairParent   *ledger.Ledger
	replayAcquireParent  func(uint32, [32]byte) error
	lifecycleMu          sync.Mutex
	lifecycleState       serviceLifecycleState
	stopDone             chan struct{}
	memoryAdmissionStop  chan struct{}
	// Add is serialized with Stop's state transition by lifecycleMu.
	validationWG sync.WaitGroup
	// consensusWG drains detached builds; Add is serialized with Stop by lifecycleMu.
	consensusWG  sync.WaitGroup
	ledgerWorkWG sync.WaitGroup
	// detachedIngress serializes private open-ledger submission while allowing
	// consensus and frontier transitions to use openLedgerMu.
	detachedIngress detachedIngressAdmission
	ingressMetrics  ingressMetrics

	// openLedgerMu serializes open-ledger submission with lifecycle transitions.
	// It is acquired before mu so a transition waiting on transaction application
	// never blocks closed-ledger readers. Priority roles pass queued ingress.
	openLedgerMu priorityGate

	// preferredTransitionMu owns the detached successor transition. Preparation
	// releases openLedgerMu after capturing the published input view, so ingress
	// must wait on this owner separately rather than entering the old view while
	// it is being replaced. The owner is cleared before any post-publication
	// callback or relay work runs.
	preferredTransitionMu     sync.Mutex
	preferredTransitionActive bool
	preferredTransitionWake   chan struct{}
	preferredTransitionCancel context.CancelFunc
	preferredTransitionOwner  *PreparedPreferredLedger

	// mu guards the latest successor and its single execution worker.
	speculativeSuccessor     *speculativeSuccessor
	speculativeRunning       *speculativeSuccessor
	speculativeWorkerRunning bool

	// mu guards the lifecycle frontier and cross-component state. Lock ordering
	// is openLedgerMu, mu, historyComponent.mu, then component-specific locks
	// such as TxQ.
	// History-only query paths take historyComponent.mu without taking mu.
	mu sync.RWMutex

	persistenceWorker
	eventPublisher
	historyComponent

	config         Config
	logger         xrpllog.Logger
	configuredFees drops.Fees

	// NodeStore for persistent storage (nil if in-memory only)
	nodeStore    nodestore.Database
	shamapFamily shamap.Family

	// replayProofMu protects the bounded in-memory certificates used by
	// generation-bound replay admission. The records contain only ledger and
	// durable-generation identities; an active prepared proof also retains its
	// opaque token owner and generation pin until publication activation ends.
	replayProofMu           sync.Mutex
	replayProofs            []replayCompletenessRecord
	replayPreparedProofs    []*replayPreparedProof
	replayPreparedSlotCount int
	replayPreparedProofWake chan struct{}
	// One foreground state verifier at a time. Transaction verification has
	// independent admission and background re-certification yields between batches.
	stateVerificationMu     sync.Mutex
	stateVerificationActive bool
	stateVerificationWake   chan struct{}

	// RelationalDB for transaction indexing (nil if not configured)
	relationalDB relationaldb.RepositoryManager

	// amendmentTable is the live amendment table folded by each validated flag
	// ledger (nil disables resync). Has its own internal mutex.
	amendmentTable *amendment.Table

	// Current open ledger (accepting transactions)
	openLedger *ledger.Ledger

	// Last closed ledger
	closedLedger *ledger.Ledger

	// Validated ledger (highest validated)
	validatedLedger   *ledger.Ledger
	validatedSignTime time.Time
	validatedAgeNow   func() time.Time

	// Published ledger (highest ledger delivered at the ordered validated
	// publication boundary).
	publishedLedgerSeq uint32
	havePublished      bool

	// Genesis ledger
	genesisLedger *ledger.Ledger

	// Pending transactions accumulated during the open ledger phase;
	// re-applied in canonical order at AcceptLedger time. Guarded by
	// openLedgerMu.
	pendingTxs []openledger.PendingTx

	// pendingValidation stashes accepted events by hash at close time so
	// eventSink can fire at quorum. Bounded — see pendingValidationMaxLen.
	pendingValidation map[[32]byte]*LedgerAcceptedEvent

	// pendingValidationOrder tracks insertion order for LRU eviction.
	pendingValidationOrder [][32]byte

	// validationCandidates retain accepted ledgers across history eviction until
	// quorum arrives. They are keyed by sequence so a replacement fork wins.
	validationCandidates     map[uint32]*ledger.Ledger
	validationCandidateOrder []uint32

	// Invoked after the validated tip advances and after mu is released.
	onValidatedLedger func(seq uint32, hash, parentHash [32]byte)

	networkLedgerState networkLedgerState

	startupFastLoadCheckpoint *fastLoadCheckpoint
	fastLoadCheckpointState   atomic.Uint32
	fastLoadStrictNodes       atomic.Uint64
	fastLoadStrictElapsed     atomic.Uint64
	fastLoadBaseStateRoot     [32]byte
	fastLoadBaseFingerprint   [32]byte
	fastLoadBaseVerified      bool

	// recertificationMu owns the background verifier's admission and shutdown.
	recertificationMu      sync.Mutex
	recertificationWake    chan struct{}
	recertificationDone    chan struct{}
	recertificationCancel  context.CancelFunc
	recertificationStopped bool
	stateBaseRetentionMu   sync.RWMutex

	// startupReplay is the one-shot replay staged for the first close and is
	// guarded by mu together with the closed/open ledger frontier.
	startupReplay *inbound.ReplayDelta

	// serverStateFunc optionally provides the operating mode string for server_info.
	// Set by the consensus adaptor after startup.
	serverStateFunc func() string

	// minimumOnlineFunc reports the online-delete retention floor; when set,
	// complete_ledgers is clamped up to it so server_info never advertises
	// reclaimed ledgers. Nil when online_delete is off.
	minimumOnlineFunc func() uint32

	// openLedgerView is the persistent open-ledger view — source of truth for
	// the open pool. Built by Start, rebuilt by adopt paths, advanced by Accept.
	openLedgerView *openledger.OpenLedger

	// txQueue is the transaction queue (mempool). Both ingress routes (RPC
	// submit and network relay) route each tx through it via OpenLedger, which
	// applies to the open view or holds it (terQUEUED). On LCL transitions
	// Accept promotes queued txs into the new view.
	//
	// Lock ordering: txQueue has its own mutex, taken after s.mu; it never
	// reaches back for s.mu. See the mu field comment.
	txQueue *txq.TxQ

	// localTxs is the held pool of locally-submitted transactions. RPC submit
	// and SubmitOpenLedgerTx(local=true) push each parse-valid transaction in;
	// Accept replays the pool onto every rebuilt open view until each entry
	// applies or ages out, with stale entries swept on the validated path.
	localTxs *localtxs.LocalTxs

	// txRelay re-broadcasts a recovered tx blob to peers, threaded into
	// OpenLedger.Accept's relay callback so post-LCL replayed txs re-propagate.
	// Nil when overlay broadcast is unwired (tests).
	txRelay func(blob []byte)

	// relayTxCache retains accepted transaction blobs after they leave the
	// current open ledger or transaction queue. Reduce-relay announcements may
	// arrive during that handoff, so the cache spans the request horizon while
	// remaining explicitly bounded.
	relayTxCacheMu                sync.Mutex
	relayTxCache                  map[[32]byte]relayTxRecord
	relayTxCacheOrder             []relayTxOrderEntry
	relayTxCacheHead              int
	relayTxCacheBytes             int64
	relayTxCacheLimit             int64
	relayTxCacheNext              uint64
	relayTxCacheStructureReserved bool

	// feeTrack is the local LoadFeeTrack mirror, always non-nil. Drivers:
	//   - Raise/LowerLocalFee: per ledger close via tickLoadFeeLocked.
	//   - SetRemoteFee: after validated-ledger promotion, median of trusted LoadFees.
	//   - SetClusterFee: from the Overlay's TMCluster ingress.
	feeTrack *feetrack.LoadFeeTrack

	// lastConsensusRoundTime is the most recent consensus round duration, fed to
	// the TxQ's timeLeap flag by processClosedLedgerLocked. Zero in standalone.
	lastConsensusRoundTime time.Duration

	// configCacheMu guards the memoised open-ledger ApplyConfig below. The config
	// is a function of the closed ledger and the published open view's rule
	// snapshot, keeping per-tx ingress off an O(amendments) parse + Rules
	// allocation per submit. Validation can advance independently of the open
	// view, so the validated frontier is deliberately not a cache key.
	// A dedicated mutex lets RLock-only SubmitOpenLedgerTx callers populate it.
	// Lock order is always s.mu → configCacheMu (the caller holds s.mu, keeping
	// both frontier pointers stable as cache keys).
	configCacheMu     sync.Mutex
	configCacheLedger *ledger.Ledger
	configCacheRules  *amendment.Rules
	configCache       openledger.ApplyConfig
}

const (
	relayTxCacheMaxEntries = 20_000
	relayTxCacheMaxBytes   = 64 * 1024 * 1024
	relayTxCacheTTL        = 5 * time.Minute
)

type relayTxRecord struct {
	blob     []byte
	deferred bool
	seenAt   time.Time
	orderID  uint64
}

type relayTxOrderEntry struct {
	hash [32]byte
	id   uint64
}

type serviceLifecycleState uint8

const (
	serviceCreated serviceLifecycleState = iota
	serviceStarting
	serviceRunning
	serviceFailed
	serviceStopping
	serviceStopped
)

var errServiceNotRunning = errors.New("ledger service is not running")

func (s *Service) lockOpenLedgerIfRunning(role openLedgerRole) error {
	_, err := s.lockOpenLedgerIfRunningTimed(context.Background(), role)
	return err
}

func (s *Service) lockOpenLedgerIfRunningTimed(ctx context.Context, role openLedgerRole) (openLedgerGateWait, error) {
	var totalLifecycleWait time.Duration
	for {
		if err := ctx.Err(); err != nil {
			return openLedgerGateWait{}, err
		}
		lifecycleStarted := time.Now()
		s.lifecycleMu.Lock()
		if s.lifecycleState != serviceRunning {
			s.lifecycleMu.Unlock()
			return openLedgerGateWait{}, errServiceNotRunning
		}
		totalLifecycleWait += time.Since(lifecycleStarted)
		s.lifecycleMu.Unlock()

		waitStarted := time.Now()
		if role == openLedgerIngress || role == openLedgerConsensus {
			if err := s.waitSpeculativeSuccessor(ctx); err != nil {
				return openLedgerGateWait{Role: role, Wait: time.Since(waitStarted)}, err
			}
		}
		if err := s.waitPreferredTransition(ctx); err != nil {
			return openLedgerGateWait{Role: role, Wait: time.Since(waitStarted)}, err
		}
		totalLifecycleWait += time.Since(waitStarted)

		wait, err := s.openLedgerMu.LockRoleContext(ctx, role)
		if err != nil {
			wait.LifecycleWait += totalLifecycleWait
			return wait, err
		}
		// A waiter can have entered the gate before a preferred transition
		// marked its owner. Release that handoff and observe the transition
		// outside the gate; otherwise it could mutate the retired view while
		// detached preparation is in flight.
		if s.preferredTransitionIsActive() {
			s.openLedgerMu.Unlock()
			if err := s.waitPreferredTransition(ctx); err != nil {
				wait.LifecycleWait += totalLifecycleWait
				return wait, err
			}
			continue
		}
		if role == openLedgerIngress || role == openLedgerConsensus {
			s.mu.RLock()
			pending := s.speculativeSuccessor != nil && !s.speculativeSuccessor.finished
			s.mu.RUnlock()
			if pending {
				s.openLedgerMu.Unlock()
				continue
			}
		}
		lifecycleStarted = time.Now()
		s.lifecycleMu.Lock()
		wait.LifecycleWait = totalLifecycleWait + time.Since(lifecycleStarted)
		running := s.lifecycleState == serviceRunning
		s.lifecycleMu.Unlock()
		if !running {
			s.openLedgerMu.Unlock()
			return wait, errServiceNotRunning
		}
		if err := ctx.Err(); err != nil {
			s.openLedgerMu.Unlock()
			return wait, err
		}
		return wait, nil
	}
}

func (s *Service) beginValidatedLedgerUpdate() bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.lifecycleState != serviceRunning {
		return false
	}
	s.validationWG.Add(1)
	return true
}

// New creates a new LedgerService
func New(cfg Config) (*Service, error) {
	if err := cfg.Startup.validateMode(); err != nil {
		return nil, fmt.Errorf("invalid ledger service configuration: %w", err)
	}
	if cfg.LedgerCacheSize == 0 {
		cfg.LedgerCacheSize = appconfig.DefaultLedgerCacheSize
	}
	if cfg.LedgerCacheSize > appconfig.MaxLedgerCacheSize {
		return nil, fmt.Errorf("invalid ledger service configuration: ledger cache size must be between %d and %d, got %d",
			appconfig.MinLedgerCacheSize, appconfig.MaxLedgerCacheSize, cfg.LedgerCacheSize)
	}

	logger := cfg.Logger
	if logger == nil {
		logger = xrpllog.Discard()
	}
	// TxQ defaults; standalone raises MinimumTxnInLedger so fee escalation stays
	// out of integration tests. The [transaction_queue] stanza overrides both.
	txqCfg := txq.DefaultConfig()
	if cfg.Standalone {
		txqCfg = txq.StandaloneConfig()
	}
	if cfg.TxQ != nil {
		txqCfg = *cfg.TxQ
	}
	txQueue, err := txq.New(txqCfg)
	if err != nil {
		return nil, fmt.Errorf("invalid transaction queue configuration: %w", err)
	}
	standardFees := genesis.StandardFees()
	configuredFees := drops.Fees{
		Base:      standardFees.BaseFee,
		Reserve:   standardFees.ReserveBase,
		Increment: standardFees.ReserveIncrement,
	}
	if cfg.ConfiguredFees != nil {
		configuredFees = *cfg.ConfiguredFees
	}

	sweepInterval := cfg.SweepInterval
	if sweepInterval <= 0 {
		sweepInterval = nodeStoreSweepIntervalForSize(cfg.NodeSize)
	}

	s := &Service{
		config:         cfg,
		logger:         logger.Named(xrpllog.PartitionLedger),
		configuredFees: configuredFees,
		nodeStore:      cfg.NodeStore,
		shamapFamily:   cfg.SHAMapFamily,
		relationalDB:   cfg.RelationalDB,
		amendmentTable: cfg.Table,
		persistenceWorker: persistenceWorker{
			validatedPersistJobs: make(map[uint32]*persistJob),
			persistWake:          make(chan struct{}, 1),
		},
		eventPublisher: eventPublisher{
			ledgerEventCandidates: make(map[uint32]*LedgerAcceptedEvent),
			publicationErrors:     make(chan error, 1),
		},
		historyComponent: historyComponent{
			ledgerHistory:        make(map[uint32]*ledger.Ledger),
			ledgerByHash:         make(map[[32]byte]uint32),
			persistedLedgers:     make(map[[32]byte]*ledger.Ledger),
			txIndex:              make(map[[32]byte]uint32),
			txPositionIndex:      make(map[[32]byte]uint32),
			completedLedgers:     newCompleteLedgerSet(),
			completeLedgerHashes: make(map[uint32][32]byte),
			completeLedgerTokens: make(map[uint32]uint64),
			sweepInterval:        sweepInterval,
		},
		pendingValidation:    make(map[[32]byte]*LedgerAcceptedEvent),
		validationCandidates: make(map[uint32]*ledger.Ledger),
		txQueue:              txQueue,
		localTxs:             localtxs.New(),
		relayTxCache:         make(map[[32]byte]relayTxRecord),
		relayTxCacheLimit:    relayTxCacheMaxBytes,
		feeTrack:             feetrack.New(),
		validatedAgeNow:      time.Now,
	}
	s.replayFaults, err = replayfault.Open(cfg.ReplayFaultPath)
	if err != nil {
		s.logger.Error("replay fault journal unavailable; validator duties blocked", "error", err)
	}
	s.openLedgerMu.setSlowLogger(func(event openLedgerGateSlowEvent) {
		s.logger.Warn("open-ledger gate slow",
			"role", event.Role.String(),
			"wait", event.Wait,
			"hold", event.Hold,
			"queued_priority", event.QueuedPriority,
			"queued_ingress", event.QueuedIngress,
		)
	})
	s.persistenceWorker.service = s
	s.eventPublisher.service = s
	s.eventPublisher.publicationLimit = maxPublicationQueue
	return s, nil
}

// syncTable folds a newly-validated ledger into the live amendment
// table (enabled set + majority projection + block detection). Gated to
// flag-ledger windows by NeedValidatedLedger; no-op when no table is configured.
func (s *Service) syncTable(l *ledger.Ledger) {
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

// Table returns the live amendment table shared with the consensus
// adaptor, or nil when none is configured.
func (s *Service) Table() *amendment.Table {
	return s.amendmentTable
}

// SetAmendmentVote records an operator veto (vetoed=true) or un-veto and persists
// it. The in-memory change always applies; an error is returned only on
// persistence failure. vetoed=false maps to UpVote — the server then votes FOR
// the amendment; for a VoteDefaultNo amendment this is how an operator opts in
// (it does not abstain).
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
	// Persist the name rippled's table holds: a supported amendment's own, or
	// the one recorded when a vote added it.
	name, _ := s.amendmentTable.EntryName(id)
	if f := amendment.FeatureByID(id); f != nil && f.IsSupported() {
		name = f.Name
	}
	return s.relationalDB.Amendment().SaveAmendmentVote(ctx, relationaldb.AmendmentVoteRecord{
		Amendment: protocol.Hash256Hex(id),
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

// Start initializes the service with a genesis ledger.
func (s *Service) Start() (err error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	switch s.lifecycleState {
	case serviceRunning:
		return nil
	case serviceStarting:
		return errors.New("ledger service is already starting")
	case serviceFailed:
		return errors.New("ledger service startup previously failed")
	case serviceStopping:
		return errors.New("ledger service is stopping")
	case serviceStopped:
		return errors.New("ledger service has been stopped")
	}
	s.lifecycleState = serviceStarting
	defer func() {
		if err != nil {
			s.lifecycleState = serviceFailed
		}
	}()
	defer func() {
		if err != nil {
			s.mu.Lock()
			s.clearFastLoadBaseLocked()
			s.mu.Unlock()
		}
	}()
	if s.config.FastLoad && s.config.Startup.Mode == StartupNormal {
		s.startupFastLoadCheckpoint, err = s.consumeFastLoadCheckpoint(context.Background())
		if err != nil {
			return err
		}
	}
	defer func() {
		s.startupFastLoadCheckpoint = nil
	}()

	s.openLedgerMu.Lock()
	defer s.openLedgerMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyComponent.mu.Lock()
	defer s.historyComponent.mu.Unlock()

	genesisConfig := s.config.GenesisConfig
	switch s.config.Startup.Mode {
	case StartupFresh:
		if s.amendmentTable != nil {
			genesisConfig.Amendments = s.amendmentTable.Desired()
		}
	default:
		genesisConfig.Amendments = nil
	}
	genesisResult, err := genesis.Create(genesisConfig)
	if err != nil {
		return fmt.Errorf("failed to create genesis ledger: %w", err)
	}

	// Fees are read dynamically from the FeeSettings SLE by readFeesFromLedger.
	genesisLedger, err := ledger.FromGenesis(
		genesisResult.Header,
		genesisResult.StateMap,
		genesisResult.TxMap,
		drops.Fees{},
	)
	if err != nil {
		return fmt.Errorf("failed to construct genesis ledger: %w", err)
	}
	if s.shamapFamily != nil {
		genesisLedger.SetSHAMapFamily(s.shamapFamily)
	}

	s.genesisLedger = genesisLedger
	s.putHistoryLocked(genesisLedger)

	hash := genesisLedger.Hash()
	s.logger.Info("Genesis ledger created",
		"sequence", genesisLedger.Sequence(),
		"hash", strconv.FormatUint(uint64(hash[0])<<24|uint64(hash[1])<<16|uint64(hash[2])<<8|uint64(hash[3]), 16)+"...",
	)

	initialClosed, err := ledger.NewOpen(genesisLedger, time.Now())
	if err != nil {
		return fmt.Errorf("failed to create initial closed ledger: %w", err)
	}
	if err := initialClosed.Close(time.Now(), 0); err != nil {
		return fmt.Errorf("failed to close initial ledger: %w", err)
	}

	selection, err := s.selectStartup(context.Background(), initialClosed)
	if err != nil {
		return fmt.Errorf("select startup ledger: %w", err)
	}
	if selection.validate && !selection.ledger.IsValidated() {
		if err := selection.ledger.SetValidated(); err != nil {
			return fmt.Errorf("validate startup ledger: %w", err)
		}
	}
	if s.config.Startup.Mode == StartupFresh {
		if err := s.persistValidatedLedger(context.Background(), genesisLedger, false); err != nil {
			return fmt.Errorf("persist fresh genesis ledger: %w", err)
		}
		if selection.ledger.IsValidated() {
			if err := s.persistValidatedLedger(context.Background(), selection.ledger, true); err != nil {
				return fmt.Errorf("persist fresh initial ledger: %w", err)
			}
		} else if s.nodeStore != nil {
			if err := s.persistToNodeStore(context.Background(), selection.ledger, selection.ledger.Sequence()); err != nil {
				return fmt.Errorf("persist fresh initial ledger: %w", err)
			}
		}
	}
	if selection.loaded {
		if err := s.installLoadedStartupLocked(selection.ledger, genesisLedger); err != nil {
			return err
		}
		s.syncTable(selection.ledger)
	} else {
		s.closedLedger = selection.ledger
		s.putHistoryLocked(selection.ledger)
		if selection.ledger.IsValidated() {
			s.validatedLedger = selection.ledger
			s.validatedSignTime = selection.ledger.CloseTime()
		}
	}
	s.networkLedgerState = selection.networkState
	if s.networkLedgerState != networkLedgerFastLoadProvisional {
		s.clearFastLoadBaseLocked()
	}
	s.startupReplay = selection.replay

	openLedger, err := ledger.NewOpen(s.closedLedger, time.Now())
	if err != nil {
		return fmt.Errorf("failed to create open ledger: %w", err)
	}
	s.openLedger = openLedger

	s.pendingTxs = nil

	// Initialise the persistent open-ledger view, anchored on closedLedger.
	if err := s.rebuildOpenLedgerViewLocked(); err != nil {
		return err
	}
	if err := s.stageStartupReplayLocked(); err != nil {
		return err
	}

	s.logger.Info("Ledger service started",
		"standalone", s.config.Standalone,
		"openLedger", s.openLedger.Sequence(),
		"ledgerCacheSize", s.config.LedgerCacheSize,
		"persistedLedgerCacheSize", s.config.LedgerCacheSize,
		"needsInitialSync", s.networkLedgerState == networkLedgerNeeded,
		"fastLoadProvisional", s.networkLedgerState == networkLedgerFastLoadProvisional,
	)
	s.startNodeStoreSweeper()
	s.persistenceWorker.start()
	s.eventPublisher.start()
	s.lifecycleState = serviceRunning

	return nil
}

func (s *Service) ledgerCacheSize() uint32 {
	if s.config.LedgerCacheSize == 0 {
		return appconfig.DefaultLedgerCacheSize
	}
	return s.config.LedgerCacheSize
}

// rebuildOpenLedgerViewLocked rebuilds s.openLedgerView from s.closedLedger
// (clears it when nil). Caller must hold s.mu (write). Used by Start and
// adopt-from-peer paths; the normal close path uses OpenLedger.Accept instead.
func (s *Service) rebuildOpenLedgerViewLocked() error {
	if s.closedLedger == nil {
		s.openLedgerView = nil
		return nil
	}
	rules := amendment.EmptyRules()
	if s.validatedLedger != nil {
		if validatedRules := s.validatedLedger.Rules(); validatedRules != nil {
			rules = validatedRules
		}
	}
	ov, err := openledger.New(s.closedLedger, openledger.Config{
		NetworkID: s.config.NetworkID,
		Logger:    s.logger,
		Rules:     rules,
	})
	if err != nil {
		return fmt.Errorf("rebuild open-ledger view: %w", err)
	}
	s.openLedgerView = ov
	return nil
}

// closedLedgerCtx implements txq.ClosedLedgerContext over a closed ledger.
// baseFee converts per-tx fees into fee levels for the FeeMetrics update.
type closedLedgerCtx struct {
	ledger           *ledger.Ledger
	baseFee          uint64
	reserveBase      uint64
	reserveIncrement uint64
	feeLevels        []txq.FeeLevel
	transactions     map[[32]byte]tx.Transaction
}

func (c *closedLedgerCtx) GetLedgerSequence() uint32 {
	if c.ledger == nil {
		return 0
	}
	return c.ledger.Sequence()
}

func (c *closedLedgerCtx) GetTransactionCount() uint32 {
	if c.ledger == nil {
		return 0
	}
	return c.ledger.TxCount()
}

func (c *closedLedgerCtx) feeConfig() tx.EngineConfig {
	return tx.EngineConfig{
		BaseFee:          c.baseFee,
		ReserveBase:      c.reserveBase,
		ReserveIncrement: c.reserveIncrement,
		LedgerSequence:   c.ledger.Sequence(),
		ParentCloseTime:  protocol.ToRippleTime(c.ledger.ParentCloseTime()),
		ParentHash:       c.ledger.ParentHash(),
		Rules:            c.ledger.Rules(),
	}
}

func (c *closedLedgerCtx) GetTransactionFeeLevels() []txq.FeeLevel {
	if c.ledger == nil {
		return nil
	}
	if c.feeLevels != nil {
		return c.feeLevels
	}
	levels := make([]txq.FeeLevel, 0, c.ledger.TxCount())
	config := c.feeConfig()
	_ = c.ledger.ForEachTransaction(func(hash [32]byte, data []byte) bool {
		raw, _, err := tx.SplitTxWithMetaBlob(data)
		if err != nil {
			return true
		}
		parsed, err := c.feeTransaction(hash, raw)
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
		baseFee, err := sign.CalculateBaseFee(parsed, c.ledger, config)
		if err != nil {
			return true
		}
		defaultBaseFee := sign.CalculateDefaultBaseFee(parsed, config)
		levels = append(levels, txq.ToFeeLevelWithDefaultBaseFee(fee, baseFee, defaultBaseFee))
		return true
	})
	return levels
}

func (c *closedLedgerCtx) feeTransaction(hash [32]byte, raw []byte) (tx.Transaction, error) {
	if parsed := c.transactions[hash]; parsed != nil && bytes.Equal(parsed.GetRawBytes(), raw) {
		if matches, err := tx.CurrentFieldsMatchRaw(parsed); err == nil && matches {
			return parsed, nil
		}
	}
	return tx.ParseFromBinary(raw)
}

// The consensus builder owns these transactions until this call returns. Only
// the resulting fee vector crosses into successor preparation.
func consensusFeeLevels(closed *ledger.Ledger, pending []openledger.PendingTx) []txq.FeeLevel {
	baseFee, reserveBase, reserveIncrement := readFeesFromLedger(closed)
	feeContext := closedLedgerCtx{
		ledger: closed, baseFee: baseFee, reserveBase: reserveBase, reserveIncrement: reserveIncrement,
		transactions: make(map[[32]byte]tx.Transaction, len(pending)),
	}
	add := func(transaction tx.Transaction) {
		if transaction == nil {
			return
		}
		if hash, err := tx.ComputeTransactionHash(transaction); err == nil {
			feeContext.transactions[hash] = transaction
		}
	}
	for _, ptx := range pending {
		add(ptx.Parsed)
		if batch, ok := ptx.Parsed.(interface{ InnerTransactions() []tx.Transaction }); ok {
			for _, inner := range batch.InnerTransactions() {
				add(inner)
			}
		}
	}
	return feeContext.GetTransactionFeeLevels()
}

// slowConsensusThreshold: past this round time the TxQ treats consensus as slow
// and freezes the fee-escalation window instead of opening it.
const slowConsensusThreshold = 5 * time.Second

// SetLastConsensusRoundTime records how long the last consensus round took
// (read during open-ledger acceptance for timeLeap). Never called in standalone.
func (s *Service) SetLastConsensusRoundTime(d time.Duration) {
	s.mu.Lock()
	s.lastConsensusRoundTime = d
	s.mu.Unlock()
}

// tickLoadFeeLocked drives LoadFeeTrack raise/lower from the per-close heartbeat:
// raise on overload, lower otherwise. With no JobQueue, "overload" is proxied by
// TxQ fee escalation (open fee level above the reference level). server_info takes
// max(loadFactorServer, feeEscalation) so the shared signal never double-counts.
// Caller must hold s.mu.
func (s *Service) tickLoadFeeLocked() {
	if s.feeTrack == nil || s.txQueue == nil {
		return
	}
	current := s.currentOpenLedgerLocked()
	if current == nil {
		return
	}
	metrics := s.txQueue.Metrics(current.TxCount())
	if metrics.OpenLedgerFeeLevel > metrics.ReferenceFeeLevel {
		s.feeTrack.RaiseLocalFee()
	} else {
		s.feeTrack.LowerLocalFee()
	}
}

// Standalone validates the closed ledger before preparing its successor, but
// publishes the validated frontier only after preparation succeeds.
func (s *Service) acceptStandaloneOpenLedgerLocked(ctx context.Context, closed *ledger.Ledger, retriableTxs []openledger.PendingTx) error {
	pending := s.pendingTxs
	if s.openLedgerView != nil {
		var err error
		pending, err = s.openLedgerView.CurrentTransactions(ctx)
		if err != nil {
			return fmt.Errorf("collect open transactions for retry order: %w", err)
		}
	}
	if s.startupReplay != nil {
		pending = append([]openledger.PendingTx(nil), pending...)
		for _, replayTx := range s.startupReplay.OrderedTxs() {
			pending = append(pending, openledger.PendingTx{Hash: replayTx.Hash, Blob: replayTx.TxBytes})
		}
	}
	salt, err := openledger.ComputeSalt(pending)
	if err != nil {
		return err
	}
	return s.openLedgerAcceptanceForValidatedLocked(ctx, &salt, closed)(closed, retriableTxs, false, nil)
}

func (s *Service) openLedgerAcceptanceForValidatedLocked(
	ctx context.Context,
	retrySalt *[32]byte,
	validatedOverride *ledger.Ledger,
) func(*ledger.Ledger, []openledger.PendingTx, bool, func(func())) error {
	view, queue, localsPool := s.openLedgerView, s.txQueue, s.localTxs
	networkID, logger, feeTrack := s.config.NetworkID, s.config.Logger, s.feeTrack
	validatedLedger := s.validatedLedger
	if validatedOverride != nil {
		validatedLedger = validatedOverride
	}
	relay, slowRound := s.txRelay, s.lastConsensusRoundTime > slowConsensusThreshold
	return func(closed *ledger.Ledger, retriableTxs []openledger.PendingTx, anyDisputes bool, publication func(func())) error {
		if closed == nil {
			return nil
		}
		closedSeq := closed.Sequence()
		baseFee, reserveBase, reserveIncrement := readFeesFromLedger(closed)
		cfg := openledger.ApplyConfig{
			BaseFee:          baseFee,
			ReserveBase:      reserveBase,
			ReserveIncrement: reserveIncrement,
			NetworkID:        networkID,
			ParentCloseTime:  parentCloseTimeRippleEpoch(closed),
			Logger:           logger,
			Rules:            openLedgerRules(validatedLedger, logger),
			FeeTrack:         feeTrack,
			RetrySalt:        retrySalt,
		}
		modifier := func(view *ledger.Ledger) {
			if queue == nil || view == nil {
				return
			}
			viewCfg := cfg
			viewCfg.LedgerSequence = view.Sequence()
			adapter := openledger.NewTxqAdapter(view, viewCfg)
			_ = queue.Accept(adapter)
		}
		// Sweeping happens on the validated path, not every close (which may fork).
		var locals []openledger.PendingTx
		if localsPool != nil {
			locals = localsPool.GetTxSet()
		}
		retries := append([]openledger.PendingTx(nil), retriableTxs...)
		relayCB := func(hash [32]byte, blob []byte) {
			s.rememberRelayTransaction(hash, blob, false)
			if relay != nil {
				relay(blob)
			}
		}
		processClosed := func() {
			if queue != nil {
				queue.ProcessClosedLedger(&closedLedgerCtx{ledger: closed, baseFee: baseFee, reserveBase: reserveBase, reserveIncrement: reserveIncrement}, slowRound)
			}
		}
		if view == nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			processClosed()
			if publication != nil {
				publication(func() {})
			}
			return nil
		}
		if err := view.AcceptWithPrecommitContext(
			ctx,
			closed,
			locals,
			anyDisputes,
			&retries,
			cfg,
			queue,
			processClosed,
			modifier,
			relayCB,
			publication,
		); err != nil {
			return fmt.Errorf("accept open ledger view at sequence %d: %w", closedSeq, err)
		}
		if len(retries) > 0 {
			s.logger.Info("openLedger.Accept produced retries",
				"count", len(retries),
				"seq", closedSeq,
			)
		}
		return nil
	}
}

// applyConfigLocked returns the ApplyConfig for the current published
// open-ledger view, memoised by closed-ledger identity and the view's immutable
// rule snapshot. Validation can advance without publishing a new open view, so
// ingress must continue using the rules captured by that view. The returned
// value is a copy; callers may mutate per-submission fields without affecting
// the cache. Caller must hold s.mu (read).
func (s *Service) applyConfigLocked() (openledger.ApplyConfig, error) {
	closed := s.closedLedger
	if closed == nil {
		return openledger.ApplyConfig{}, svcerr.ErrNoClosedLedger
	}
	rules := s.currentOpenLedgerRulesLocked()

	s.configCacheMu.Lock()
	defer s.configCacheMu.Unlock()
	// Pointer identity is a sufficient key: each frontier update installs a fresh
	// immutable ledger/rule snapshot, while ingress Modify clones preserve the
	// same Rules pointer on the current view.
	if s.configCacheLedger == closed && s.configCacheRules == rules {
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
		Rules:            rules,
		FeeTrack:         s.feeTrack,
	}
	s.configCache = cfg
	s.configCacheLedger = closed
	s.configCacheRules = rules
	return cfg, nil
}

// openLedgerRules selects the rule set for the next open ledger from the last
// validated ledger. A node without a validated ledger has no configured feature
// preset in Go, so it uses the empty set, matching rippled's default config.features.
func openLedgerRules(validated *ledger.Ledger, logger xrpllog.Logger) *amendment.Rules {
	if validated == nil {
		return amendment.EmptyRules()
	}
	return rulesFromLedger(validated, logger)
}

// currentOpenLedgerRulesLocked returns the immutable rule snapshot selected when
// the published open view was built. Caller must hold s.mu (read or write).
func (s *Service) currentOpenLedgerRulesLocked() *amendment.Rules {
	if s.openLedgerView != nil {
		if current := s.openLedgerView.Current(); current != nil {
			if rules := current.Rules(); rules != nil {
				return rules
			}
		}
	}
	return amendment.EmptyRules()
}

// rulesFromLedger derives the amendment.Rules for parent's successor by reading
// parent's Amendments SLE. Returns EmptyRules on nil parent or read failure (and
// logs) — behaving as if no amendments are enabled is the safe direction, unlike
// an AllSupportedRules default that would mask plumbing bugs.
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

// TransactionRules returns the amendment rules used for transactions entering
// the current open ledger.
func (s *Service) TransactionRules() *amendment.Rules {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentOpenLedgerRulesLocked()
}

// SubmitOpenLedgerTx routes a tx blob through the persistent OpenLedger view and
// returns the per-tx classification (ResultFailure before Start).
//
// local=true (RPC-originated) pushes every parse-valid result into the LocalTxs
// held pool so it survives LCL transitions until the sender's sequence advances
// or it ages out. local=false (peer relay) doesn't pin the blob — the peer
// manages its own resends.
func (s *Service) SubmitOpenLedgerTx(blob []byte, local bool) (openledger.Result, error) {
	outcome, err := s.SubmitOpenLedgerTxDetailed(blob, local)
	return outcome.Class, err
}

// SubmitOpenLedgerTxDetailed is SubmitOpenLedgerTx with the queue disposition
// retained for callers that must distinguish applied from deferred transactions.
func (s *Service) SubmitOpenLedgerTxDetailed(blob []byte, local bool) (openledger.SubmitOutcome, error) {
	origin := "peer"
	if local {
		origin = "local"
	}
	return s.submitOpenLedgerTx(blob, local, origin)
}

// LearnOpenLedgerTx admits an acquired transaction without retaining it in the local retry pool.
func (s *Service) LearnOpenLedgerTx(blob []byte) (openledger.SubmitOutcome, error) {
	return s.submitOpenLedgerTx(blob, false, "txset")
}

func (s *Service) submitOpenLedgerTx(blob []byte, local bool, origin string) (openledger.SubmitOutcome, error) {
	failure := openledger.SubmitOutcome{Class: openledger.ResultFailure}
	s.mu.RLock()
	cfg, cfgErr := s.applyConfigLocked()
	haveOpenLedger := s.openLedgerView != nil
	s.mu.RUnlock()

	if !haveOpenLedger {
		return failure, errors.New("openLedgerView not initialised")
	}
	if cfgErr != nil {
		return failure, cfgErr
	}
	releaseAdmission, err := s.detachedIngress.acquire()
	if err != nil {
		return failure, err
	}
	defer releaseAdmission()
	ptx, err := openledger.ParsePendingTx(blob)
	if err != nil {
		return failure, err
	}
	return s.submitOpenLedgerPendingTx(ptx, local, origin, cfg)
}

// SubmitOpenLedgerTxDetailedOwned takes ownership of pending, including Blob and
// Parsed. The caller must not modify Blob or use Parsed during or after this call.
// The first private attempt consumes Parsed; a stale publication retry reparses
// Blob so prior attempt annotations cannot affect the next attempt.
func (s *Service) SubmitOpenLedgerTxDetailedOwned(pending openledger.PendingTx, local bool) (openledger.SubmitOutcome, error) {
	failure := openledger.SubmitOutcome{Class: openledger.ResultFailure}
	s.mu.RLock()
	cfg, cfgErr := s.applyConfigLocked()
	haveOpenLedger := s.openLedgerView != nil
	s.mu.RUnlock()

	if !haveOpenLedger {
		return failure, errors.New("openLedgerView not initialised")
	}
	if cfgErr != nil {
		return failure, cfgErr
	}
	releaseAdmission, err := s.detachedIngress.acquire()
	if err != nil {
		return failure, err
	}
	defer releaseAdmission()
	if !pending.ParsedMatchesCanonical() {
		pending, err = s.reparseDetachedPending(pending.Blob)
		if err != nil {
			return failure, err
		}
	}
	return s.submitOpenLedgerPendingTx(pending, local, ingressOrigin(local), cfg)
}

func ingressOrigin(local bool) string {
	if local {
		return "local"
	}
	return "peer"
}

func (s *Service) submitOpenLedgerPendingTx(
	ptx openledger.PendingTx,
	local bool,
	origin string,
	cfg openledger.ApplyConfig,
) (openledger.SubmitOutcome, error) {
	failure := openledger.SubmitOutcome{Class: openledger.ResultFailure}
	if ptx.Parsed == nil {
		return failure, errors.New("open-ledger ingress: parsed transaction unavailable")
	}
	if ptx.Parsed.GetCommon().GetFlags()&tx.TfInnerBatchTxn != 0 {
		return failure, fmt.Errorf(
			"%w: Batch inner transactions are never considered validly signed.",
			txengine.ErrInvalidSignature,
		)
	}
	initialRules := cfg.Rules
	initialSignatureCheck := !cfg.SkipSignatureVerification
	var signatureErr error
	localReason := tx.TransactionLocalChecksFailureReason(ptx.Parsed)
	// Verify the signature before the short open-ledger capture/fence; the
	// in-strand check then reuses the cached verdict (#1105).
	if initialSignatureCheck {
		signatureErr = signatureVerificationError(
			txengine.PrewarmSignatureWithRules(ptx.Parsed, initialRules), initialRules)
	}
	outcome, _, _, _, submitErr := s.submitDetachedIngressOwned(detachedIngressRequest{
		pendingTx:             ptx,
		origin:                origin,
		ownedParsed:           true,
		initialRules:          initialRules,
		signatureEligible:     true,
		initialSignatureCheck: initialSignatureCheck,
		initialSignatureErr:   signatureErr,
		prepare: func(ctx context.Context) {
			if signatureErr == nil && localReason == "" {
				s.prefetchIngressState(ctx, ptx)
			}
		},
		localReason: localReason,
		verifySignature: func(parsed tx.Transaction, rules *amendment.Rules) error {
			return signatureVerificationError(
				txengine.PrewarmSignatureWithRules(parsed, rules), rules)
		},
		prepareCommit: func(current *ledger.Ledger, _ *ledger.Ledger, _ *txq.TxQ, _ openledger.ApplyConfig, outcome openledger.SubmitOutcome, attempt openledger.PendingTx) detachedIngressCommitData {
			return detachedIngressCommitData{
				proposed: s.prepareProposedTransaction(attempt, attempt.Blob, outcome, current),
			}
		},
		onCommit: func(current *ledger.Ledger, _ *txq.TxQ, localTxs *localtxs.LocalTxs, _ openledger.ApplyConfig, _ openledger.SubmitOutcome, data detachedIngressCommitData, committed openledger.PendingTx) {
			if local && localTxs != nil && current != nil {
				localTxs.PushBack(current.Sequence(), committed)
			}
			s.enqueueProposedTransaction(data.proposed)
		},
	})
	if submitErr != nil {
		var validationErr *detachedIngressValidationError
		if errors.As(submitErr, &validationErr) {
			switch validationErr.kind {
			case detachedIngressSignatureFailure:
				return failure, validationErr.err
			case detachedIngressLocalFailure:
				return failure, fmt.Errorf("%w: %s", ErrInvalidLocalTransaction, validationErr.err)
			}
		}
		return failure, submitErr
	}
	if outcome.Applied {
		s.eventPublisher.dispatchServerStatusEvent()
	}
	return outcome, nil
}

// PrewarmSignaturesContext verifies transaction signatures until ctx is
// canceled.
func (s *Service) PrewarmSignaturesContext(ctx context.Context, blobs [][]byte) {
	if len(blobs) == 0 {
		return
	}
	s.mu.RLock()
	cfg, cfgErr := s.applyConfigLocked()
	s.mu.RUnlock()
	if cfgErr != nil || cfg.SkipSignatureVerification {
		return
	}

	workers := min(runtime.GOMAXPROCS(0), len(blobs))
	work := make(chan []byte, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				select {
				case <-ctx.Done():
					return
				case blob, ok := <-work:
					if !ok {
						return
					}
					if ptx, err := openledger.ParsePendingTx(blob); err == nil {
						txengine.PrewarmSignatureWithRules(ptx.Parsed, cfg.Rules)
					}
				}
			}
		}()
	}
feed:
	for _, blob := range blobs {
		select {
		case <-ctx.Done():
			break feed
		case work <- blob:
		}
	}
	close(work)
	wg.Wait()
}

// OpenLedgerTxs returns the raw tx blobs in the persistent open view (nil
// pre-Start). The slice is memoised and shared with concurrent callers — it MUST
// NOT be mutated.
func (s *Service) OpenLedgerTxs() [][]byte {
	s.mu.RLock()
	ov := s.openLedgerView
	s.mu.RUnlock()
	if ov == nil {
		return nil
	}
	return ov.CurrentTxs()
}

// OpenLedgerTxHashes returns the tx hashes in the persistent open view, driving
// the periodic TMHaveTransactions announce. Allocates fresh each call. Nil
// pre-Start.
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
	_ = view.ForEachTransaction(func(hash [32]byte, data []byte) bool {
		raw, _, err := tx.SplitTxWithMetaBlob(data)
		if err != nil {
			return true
		}
		parsed, err := tx.ParseFromBinary(raw)
		if err != nil || parsed.GetCommon().GetFlags()&tx.TfInnerBatchTxn != 0 {
			return true
		}
		hashes = append(hashes, hash)
		return true
	})
	return hashes
}

// OpenLedgerHasTx reports whether the persistent open view contains
// the tx hash. Used by peer-protocol HasTx replies.
func (s *Service) OpenLedgerHasTx(hash [32]byte) (bool, error) {
	s.mu.RLock()
	ov := s.openLedgerView
	s.mu.RUnlock()
	if ov == nil {
		return false, nil
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
	if view == nil {
		return nil, false
	}
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

// TransactionForRelay looks up a transaction in the authoritative local
// transaction cache used by reduce-relay replies. Only membership in the live
// open-ledger view is current; TxQ and retained-cache fallbacks are new, with
// the cache preserving whether a queued transaction is deferred. The returned
// blob is a private copy.
func (s *Service) TransactionForRelay(hash [32]byte) (blob []byte, included, deferred, ok bool) {
	if blob, ok = s.OpenLedgerGetTx(hash); ok {
		return append([]byte(nil), blob...), true, false, true
	}
	s.mu.RLock()
	queue := s.txQueue
	s.mu.RUnlock()
	if queue == nil {
		return s.relayCacheGet(hash)
	}
	blob, ok = queue.GetTxBlob(hash)
	if ok {
		return blob, false, true, true
	}
	return s.relayCacheGet(hash)
}

func (s *Service) rememberRelayTransaction(hash [32]byte, blob []byte, deferred bool) {
	if hash == ([32]byte{}) || len(blob) == 0 {
		return
	}
	now := time.Now()
	s.relayTxCacheMu.Lock()
	defer s.relayTxCacheMu.Unlock()
	limit := s.relayTxCacheLimit
	if limit <= 0 {
		limit = relayTxCacheMaxBytes
	}
	if int64(len(blob)) > limit {
		return
	}
	if !s.reserveRelayCacheLocked(uint64(len(blob))) {
		return
	}
	if s.relayTxCache == nil {
		s.relayTxCache = make(map[[32]byte]relayTxRecord)
	}
	orderID := uint64(0)
	if existing, exists := s.relayTxCache[hash]; exists {
		s.relayTxCacheBytes -= int64(len(existing.blob))
		s.releaseRelayCachePayloadLocked(uint64(len(existing.blob)))
		orderID = existing.orderID
	} else {
		s.relayTxCacheNext++
		if s.relayTxCacheNext == 0 {
			s.relayTxCacheNext++
		}
		orderID = s.relayTxCacheNext
		s.relayTxCacheOrder = append(s.relayTxCacheOrder, relayTxOrderEntry{hash: hash, id: orderID})
	}
	s.relayTxCache[hash] = relayTxRecord{
		blob:     append([]byte(nil), blob...),
		deferred: deferred,
		seenAt:   now,
		orderID:  orderID,
	}
	s.relayTxCacheBytes += int64(len(blob))
	for (len(s.relayTxCache) > relayTxCacheMaxEntries || s.relayTxCacheBytes > limit) &&
		s.relayTxCacheHead < len(s.relayTxCacheOrder) {
		old := s.relayTxCacheOrder[s.relayTxCacheHead]
		s.relayTxCacheHead++
		if record, stillPresent := s.relayTxCache[old.hash]; stillPresent && record.orderID == old.id {
			s.relayTxCacheBytes -= int64(len(record.blob))
			delete(s.relayTxCache, old.hash)
			s.releaseRelayCachePayloadLocked(uint64(len(record.blob)))
		}
	}
	s.compactRelayCacheOrderLocked()
}

func (s *Service) relayCacheGet(hash [32]byte) (blob []byte, included, deferred, ok bool) {
	s.relayTxCacheMu.Lock()
	defer s.relayTxCacheMu.Unlock()
	record, found := s.relayTxCache[hash]
	if !found {
		return nil, false, false, false
	}
	if time.Since(record.seenAt) >= relayTxCacheTTL {
		delete(s.relayTxCache, hash)
		s.relayTxCacheBytes -= int64(len(record.blob))
		s.releaseRelayCachePayloadLocked(uint64(len(record.blob)))
		s.compactRelayCacheOrderLocked()
		return nil, false, false, false
	}
	// The retained blob is no longer guaranteed to be in the current open
	// ledger. Callers derive tsCURRENT only from the live view above; cache
	// fallback is always a tsNEW-style record.
	return append([]byte(nil), record.blob...), false, record.deferred, true
}

func (s *Service) compactRelayCacheOrderLocked() {
	if len(s.relayTxCache) == 0 {
		s.relayTxCache = nil
		s.relayTxCacheOrder = nil
		s.relayTxCacheHead = 0
		s.relayTxCacheBytes = 0
		if s.relayTxCacheStructureReserved {
			s.config.RelayMemory.Release(relayCacheStructureBytes)
			s.relayTxCacheStructureReserved = false
		}
		return
	}
	activeOrder := len(s.relayTxCacheOrder) - s.relayTxCacheHead
	if !(s.relayTxCacheHead > 1024 && s.relayTxCacheHead*2 > len(s.relayTxCacheOrder)) &&
		activeOrder <= 2*relayTxCacheMaxEntries {
		return
	}
	order := make([]relayTxOrderEntry, 0, len(s.relayTxCache))
	for _, entry := range s.relayTxCacheOrder[s.relayTxCacheHead:] {
		if record, ok := s.relayTxCache[entry.hash]; ok && record.orderID == entry.id {
			order = append(order, entry)
		}
	}
	s.relayTxCacheOrder = order
	s.relayTxCacheHead = 0
}

// GetOpenLedger returns the current open ledger
func (s *Service) GetOpenLedger() *ledger.Ledger {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentOpenLedgerLocked()
}

// currentOpenLedgerLocked reads the published view while preserving openLedger
// as the ledger-frontier identity used by consensus ownership checks.
// Caller must hold s.mu.
func (s *Service) currentOpenLedgerLocked() *ledger.Ledger {
	if s.openLedgerView != nil {
		return s.openLedgerView.Current()
	}
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

// GetValidatedLedgerAge returns the age of the trusted-validation signing-time
// median for the current validated ledger.
func (s *Service) GetValidatedLedgerAge() time.Duration {
	s.mu.RLock()
	signTime := s.validatedSignTime
	now := s.validatedAgeNow
	s.mu.RUnlock()
	if signTime.IsZero() {
		return 14 * 24 * time.Hour
	}
	if now == nil {
		now = time.Now
	}
	current := now()
	current = time.Unix(current.Unix(), 0).UTC()
	signTime = time.Unix(signTime.Unix(), 0).UTC()
	age := current.Sub(signTime)
	if age < 0 {
		return 0
	}
	return age
}

// SetValidatedLedgerAgeClock sets the adjusted close-time clock used for
// validated-ledger freshness checks.
func (s *Service) SetValidatedLedgerAgeClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.validatedAgeNow = now
}

// GetLedgerBySequence returns a ledger by sequence, falling back to the open
// ledger or durable validated history.
func (s *Service) GetLedgerBySequence(seq uint32) (*ledger.Ledger, error) {
	return s.GetLedgerBySequenceContext(context.Background(), seq)
}

// GetLedgerBySequenceContext is GetLedgerBySequence with caller-controlled
// cancellation for cache, storage, and snapshot work.
func (s *Service) GetLedgerBySequenceContext(ctx context.Context, seq uint32) (*ledger.Ledger, error) {
	return s.getLedgerBySequence(ctx, seq)
}

func (s *Service) getLedgerBySequence(ctx context.Context, seq uint32) (*ledger.Ledger, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	s.historyComponent.mu.RLock()
	history := s.ledgerHistory[seq]
	var open *ledger.Ledger
	if current := s.currentOpenLedgerLocked(); current != nil && current.Sequence() == seq {
		open = current
	}
	s.historyComponent.mu.RUnlock()
	s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.completeMu.RLock()
	hash, complete := s.completeLedgerHashes[seq]
	complete = complete && s.completedLedgers != nil && s.completedLedgers.contains(seq)
	s.completeMu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if history != nil && (!complete || history.Hash() == hash) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return history, nil
	}
	if open != nil && (!complete || open.Hash() == hash) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return open, nil
	}
	if !complete || s.nodeStore == nil || s.shamapFamily == nil {
		return nil, svcerr.ErrLedgerNotFound
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.historyComponent.mu.RLock()
	cached := s.persistedLedgers[hash]
	s.historyComponent.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cached != nil && cached.Sequence() == seq {
		if cached.IsValidated() {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return cached, nil
		}
		validated, err := cached.SnapshotContext(ctx)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := validated.SetValidated(); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return validated, nil
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	loaded, err := s.loadStoredLedgerByHash(ctx, hash)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, errStoredLedgerUnavailable) {
			return nil, fmt.Errorf("%w: load ledger %d from nodestore: %v", svcerr.ErrLedgerNotFound, seq, err)
		}
		return nil, fmt.Errorf("load ledger %d from nodestore: %w", seq, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if loaded == nil || loaded.Sequence() != seq {
		return nil, svcerr.ErrLedgerNotFound
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := loaded.SetValidated(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.completeMu.RLock()
	stillComplete := s.completedLedgers != nil &&
		s.completedLedgers.contains(seq) &&
		s.completeLedgerHashes[seq] == hash
	s.completeMu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !stillComplete {
		return nil, svcerr.ErrLedgerNotFound
	}

	s.historyComponent.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.historyComponent.mu.Unlock()
		return nil, err
	}
	s.cachePersistedLedgerLocked(loaded)
	s.historyComponent.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return loaded, nil
}

// AdoptedLedgerBySequence returns a closed ledger from adopted history only,
// never the mutable open ledger — the consensus catch-up walk needs immutable,
// parent-hash-chained ledgers.
func (s *Service) AdoptedLedgerBySequence(seq uint32) (*ledger.Ledger, error) {
	s.historyComponent.mu.RLock()
	defer s.historyComponent.mu.RUnlock()
	if l, ok := s.ledgerHistory[seq]; ok {
		return l, nil
	}
	return nil, svcerr.ErrLedgerNotFound
}

func (s *Service) GetLedgerByHash(hash [32]byte) (*ledger.Ledger, error) {
	return s.getLedgerByHash(context.Background(), hash)
}

func (s *Service) GetLedgerByHashContext(ctx context.Context, hash [32]byte) (*ledger.Ledger, error) {
	return s.getLedgerByHash(ctx, hash)
}

func (s *Service) getLedgerByHash(ctx context.Context, hash [32]byte) (*ledger.Ledger, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.historyComponent.mu.RLock()
	if seq, ok := s.ledgerByHash[hash]; ok {
		if l, ok := s.ledgerHistory[seq]; ok {
			s.historyComponent.mu.RUnlock()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return l, nil
		}
	}
	if l, ok := s.persistedLedgers[hash]; ok {
		s.historyComponent.mu.RUnlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return s.validatedPersistedLedger(ctx, l)
	}
	canLoad := s.nodeStore != nil && s.shamapFamily != nil &&
		s.relationalDB != nil && s.relationalDB.Ledger() != nil
	s.historyComponent.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !canLoad {
		return nil, svcerr.ErrLedgerNotFound
	}

	loaded, err := s.loadPersistedLedgerByHash(ctx, hash)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.historyComponent.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.historyComponent.mu.Unlock()
		return nil, err
	}
	if seq, ok := s.ledgerByHash[hash]; ok {
		if l, ok := s.ledgerHistory[seq]; ok {
			s.historyComponent.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return l, nil
		}
	}
	if l, ok := s.persistedLedgers[hash]; ok {
		s.historyComponent.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return s.validatedPersistedLedger(ctx, l)
	}
	s.cachePersistedLedgerLocked(loaded)
	s.historyComponent.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.validatedPersistedLedger(ctx, loaded)
}

func (s *Service) loadPersistedLedgerByHash(ctx context.Context, hash [32]byte) (*ledger.Ledger, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := s.relationalDB.Ledger().GetLedgerInfoByHash(ctx, relationaldb.Hash(hash))
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if errors.Is(err, relationaldb.ErrLedgerNotFound) {
		return nil, svcerr.ErrLedgerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load ledger %x metadata: %w", hash[:8], err)
	}
	if info == nil {
		return nil, svcerr.ErrLedgerNotFound
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	loaded, err := s.loadStoredLedgerByHash(ctx, hash)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, errStoredLedgerUnavailable) {
			return nil, fmt.Errorf("%w: load ledger %x from nodestore: %v", svcerr.ErrLedgerNotFound, hash[:8], err)
		}
		return nil, fmt.Errorf("load ledger %x from nodestore: %w", hash[:8], err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if loaded == nil {
		return nil, svcerr.ErrLedgerNotFound
	}
	if !storedHeaderMatchesInfo(loaded.Header(), info) {
		return nil, fmt.Errorf("%w: ledger %x header does not match persisted metadata", svcerr.ErrLedgerNotFound, hash[:8])
	}
	return loaded, nil
}

func (s *Service) validatedPersistedLedger(ctx context.Context, l *ledger.Ledger) (*ledger.Ledger, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	validated, err := s.persistedLedgerIsValidated(ctx, l.Hash(), l.Sequence())
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil || !validated {
		return l, err
	}
	if l.IsValidated() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return l, nil
	}
	copy, err := l.SnapshotContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := copy.SetValidated(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return copy, nil
}

func (s *Service) persistedLedgerIsValidated(ctx context.Context, hash [32]byte, seq uint32) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.RLock()
	tip := s.validatedLedger
	s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if tip == nil || seq > tip.Sequence() {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	canonical, ok, err := tip.HashOfSeqContext(ctx, seq)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	if canonicalProofUnavailable(err) {
		return false, nil
	}
	if err != nil || ok {
		return ok && canonical == hash, err
	}
	if seq%256 == 0 {
		return false, nil
	}
	anchor64 := uint64(seq) + uint64(256-seq%256)
	if anchor64 > uint64(tip.Sequence()) {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	anchorHash, ok, err := tip.HashOfSeqContext(ctx, uint32(anchor64))
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	if canonicalProofUnavailable(err) {
		return false, nil
	}
	if err != nil || !ok {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	anchor, err := s.loadPersistedLedgerByHash(ctx, anchorHash)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	if errors.Is(err, svcerr.ErrLedgerNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	canonical, ok, err = anchor.HashOfSeqContext(ctx, seq)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	if canonicalProofUnavailable(err) {
		return false, nil
	}
	return ok && canonical == hash, err
}

func canonicalProofUnavailable(err error) bool {
	return errors.Is(err, shamap.ErrNodeNotInStore) || errors.Is(err, shamap.ErrInvalidNodeData)
}

// GetCurrentLedgerIndex returns the current open ledger index
func (s *Service) GetCurrentLedgerIndex() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	current := s.currentOpenLedgerLocked()
	if current == nil {
		return 0
	}
	return current.Sequence()
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

// MaxPersistedLedgerSeq returns the highest ledger sequence in the relational
// DB, or 0 when none is stored or no relational DB is configured.
func (s *Service) MaxPersistedLedgerSeq(ctx context.Context) uint32 {
	if s.relationalDB == nil || s.relationalDB.Ledger() == nil {
		return 0
	}
	seq, err := s.relationalDB.Ledger().GetMaxLedgerSeq(ctx)
	if err != nil {
		s.logger.Warn("failed to read max persisted ledger seq", "err", err)
		return 0
	}
	if seq == nil {
		return 0
	}
	return uint32(*seq)
}

// AvailableLedgerRange returns the complete contiguous range ending at the
// published ledger, or ok=false before any ledger has been published.
func (s *Service) AvailableLedgerRange() (min, max uint32, ok bool) {
	s.mu.RLock()
	max = s.publishedLedgerSeq
	ok = s.havePublished && max != 0
	s.mu.RUnlock()
	if !ok {
		return 0, 0, false
	}

	min = max
	s.completeMu.RLock()
	if s.completedLedgers != nil {
		if current, found := s.completedLedgers.rangeContaining(max); found && current.start > 0 {
			min = current.start
		}
	}
	s.completeMu.RUnlock()
	return min, max, true
}

// validatedLedgerRange returns the complete range currently safe for indexed
// transaction lookup. Pending persistence is excluded from the full range
// used by ledger availability and cleanup.
func (s *Service) validatedLedgerRange() (min, max uint32, ok bool) {
	min, max, ok = s.AvailableLedgerRange()
	if !ok {
		return 0, 0, false
	}

	var pending []uint32
	s.completeMu.RLock()
	if len(s.completeLedgerTokens) != 0 {
		pending = make([]uint32, 0, len(s.completeLedgerTokens))
		for seq := range s.completeLedgerTokens {
			pending = append(pending, seq)
		}
	}
	s.completeMu.RUnlock()
	min, max = trimPendingValidatedRange(min, max, pending)
	return min, max, true
}

func (s *Service) contiguousValidatedRangeLocked() (first, last uint32, ok bool) {
	s.historyComponent.mu.RLock()
	defer s.historyComponent.mu.RUnlock()
	if s.validatedLedger == nil {
		return 0, 0, false
	}

	last = s.validatedLedger.Sequence()
	tip, found := s.ledgerHistory[last]
	if !found || tip.Hash() != s.validatedLedger.Hash() {
		return 0, 0, false
	}

	first = last
	current := tip
	for first > 0 {
		previous, found := s.ledgerHistory[first-1]
		if !found || current.ParentHash() != previous.Hash() {
			break
		}
		first--
		current = previous
	}
	return first, last, true
}

func earliestFetch(closed, depth uint32) uint32 {
	if depth == 0 || closed <= depth {
		return 0
	}
	return closed - depth
}

// EarliestFetch returns the configured lower sequence bound for peer serving.
func (s *Service) EarliestFetch() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closedLedger == nil {
		return 0
	}
	return earliestFetch(s.closedLedger.Sequence(), s.config.FetchDepth)
}

// AdvertisableLedgerRange returns the parent-consistent retained range ending
// at the validated ledger, clamped to the configured and online-delete floors.
func (s *Service) AdvertisableLedgerRange() (first, last uint32, ok bool) {
	s.mu.RLock()
	first, last, ok = s.contiguousValidatedRangeLocked()
	var closed uint32
	if s.closedLedger != nil {
		closed = s.closedLedger.Sequence()
	}
	depth := s.config.FetchDepth
	minimumOnlineFunc := s.minimumOnlineFunc
	s.mu.RUnlock()
	if !ok {
		return 0, 0, false
	}

	floor := earliestFetch(closed, depth)
	if minimumOnlineFunc != nil {
		if minimumOnline := minimumOnlineFunc(); minimumOnline > floor {
			floor = minimumOnline
		}
	}
	if floor > first {
		first = floor
	}
	if first > last {
		return 0, 0, false
	}
	return first, last, true
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

// SetServerStateFunc sets a function that provides the server state string.
func (s *Service) SetServerStateFunc(fn func() string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serverStateFunc = fn
}

// SetMinimumOnlineFunc registers the online-delete retention floor used to clamp
// complete_ledgers. Pass nil when online_delete is off.
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

// TxQMetrics returns the current TxQ metrics, or the zero value when
// the queue isn't initialised.
func (s *Service) TxQMetrics() txq.Metrics {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.txQueue == nil {
		return txq.Metrics{}
	}
	var txInLedger uint32
	if current := s.currentOpenLedgerLocked(); current != nil {
		txInLedger = current.TxCount()
	}
	return s.txQueue.Metrics(txInLedger)
}

// OpenLedgerIngressMetrics returns cumulative detached ingress work. The
// snapshot is safe to read while submissions are active and contains no
// transaction or ledger pointers.
func (s *Service) OpenLedgerIngressMetrics() IngressMetricsSnapshot {
	if s == nil {
		return IngressMetricsSnapshot{}
	}
	return s.ingressMetrics.snapshot()
}

// QueueAccountTxs returns the TxQ candidates queued for one account, sorted by
// SeqProxy. Backs account_info's queue_data. Empty when no TxQ is wired.
func (s *Service) QueueAccountTxs(account [20]byte) []*txq.CandidateDetails {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.txQueue == nil {
		return nil
	}
	return s.txQueue.AccountTxs(account)
}

// QueueAllTxs returns every TxQ candidate, ordered by fee level. Backs the
// ledger method's queue_data dump. Empty when no TxQ is wired.
func (s *Service) QueueAllTxs() []*txq.CandidateDetails {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.txQueue == nil {
		return nil
	}
	return s.txQueue.AllTxs()
}

// GetServerInfo returns basic server information
func (s *Service) GetServerInfo() ServerInfo {
	s.mu.RLock()
	info := ServerInfo{
		Standalone:         s.config.Standalone,
		ServerState:        "full",
		NeedsNetworkLedger: s.networkLedgerState == networkLedgerNeeded,
		NetworkID:          s.config.NetworkID,
		HavePublished:      s.havePublished,
		PublishedLedgerSeq: s.publishedLedgerSeq,
	}

	if current := s.currentOpenLedgerLocked(); current != nil {
		info.OpenLedgerSeq = current.Sequence()
	}

	if s.closedLedger != nil {
		info.ClosedLedgerSeq = s.closedLedger.Sequence()
		info.ClosedLedgerHash = s.closedLedger.Hash()
		info.ClosedLedgerCloseTime = protocol.RippleSeconds(s.closedLedger.CloseTime())
	}

	if s.validatedLedger != nil {
		info.HaveValidated = true
		info.ValidatedLedgerSeq = s.validatedLedger.Sequence()
		info.ValidatedLedgerHash = s.validatedLedger.Hash()
		info.ValidatedLedgerCloseTime = protocol.RippleSeconds(s.validatedLedger.CloseTime())
	}

	serverStateFunc := s.serverStateFunc
	minimumOnlineFunc := s.minimumOnlineFunc
	s.mu.RUnlock()

	if serverStateFunc != nil {
		info.ServerState = serverStateFunc()
	}
	if minimumOnlineFunc != nil {
		s.clampCompleteLedgers(minimumOnlineFunc())
	}
	info.CompleteLedgers = s.completeLedgersString()
	if min, max, ok := s.validatedLedgerRange(); ok {
		info.HaveValidatedRange = true
		info.ValidatedRangeMin = min
		info.ValidatedRangeMax = max
	}

	return info
}

// ServerInfo contains basic server status information
type ServerInfo struct {
	Standalone               bool
	ServerState              string // "disconnected", "connected", "syncing", "tracking", "full"
	NeedsNetworkLedger       bool
	OpenLedgerSeq            uint32
	ClosedLedgerSeq          uint32
	ClosedLedgerHash         [32]byte
	ClosedLedgerCloseTime    int64 // Ripple-epoch seconds
	HaveValidated            bool  // mirrors rippled LedgerMaster::haveValidated()
	ValidatedLedgerSeq       uint32
	ValidatedLedgerHash      [32]byte
	ValidatedLedgerCloseTime int64 // Ripple-epoch seconds
	CompleteLedgers          string
	HavePublished            bool
	PublishedLedgerSeq       uint32
	HaveValidatedRange       bool
	ValidatedRangeMin        uint32
	ValidatedRangeMax        uint32
	NetworkID                uint32
}

// LedgerInfo contains information about a ledger
type LedgerInfo struct {
	Sequence  uint32
	Hash      [32]byte
	CloseTime time.Time
	Validated bool
}

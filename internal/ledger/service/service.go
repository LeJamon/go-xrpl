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

// Aliases to the svcerr sentinels for in-package callers; external callers
// MUST compare against svcerr.* directly.
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
	Standalone bool

	// NetworkID is the network identifier for this node.
	// Legacy networks (ID <= 1024) reject transactions that include NetworkID.
	// New networks (ID > 1024) require NetworkID in transactions.
	NetworkID uint32

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
	// mu guards the Service's mutable ledger state. Lock ordering: a path
	// needing both mu and the TxQ mutex MUST take mu first. TxQ never reaches
	// back into the Service, so this one rule keeps submit/close deadlock-free.
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

	// Pending transactions accumulated during the open ledger phase;
	// re-applied in canonical order at AcceptLedger time.
	pendingTxs []pendingTx

	// eventCallback fires when a ledger becomes validated — at quorum-gate time
	// from SetValidatedLedger, not close time, so subscribers stay in lockstep
	// with server_info.validated_ledger.
	eventCallback EventCallback

	// pendingValidation stashes accepted events by hash at close time so
	// eventCallback can fire at quorum. Bounded — see pendingValidationMaxLen.
	pendingValidation map[[32]byte]*LedgerAcceptedEvent

	// pendingValidationOrder tracks insertion order for LRU eviction.
	pendingValidationOrder [][32]byte

	// pendingLedgerValidations stashes trusted-validation notifications by
	// *sequence* when SetValidatedLedger arrives before that seq is adopted;
	// drained and promoted (on hash match within TTL) when the seq lands. The
	// opposite race to pendingValidation (which is hash-keyed accepted events).
	pendingLedgerValidations map[uint32]pendingValidationEntry

	// pendingLedgerValidationsOrder tracks insertion order for LRU
	// eviction of pendingLedgerValidations.
	pendingLedgerValidationsOrder []uint32

	// Invoked off-thread when SetValidatedLedger stashes a validation for a seq
	// beyond closed (arms the inbound-ledger acquisition).
	onPendingValidationStashed func(seq uint32, hash [32]byte)

	// heldAdoptions stashes out-of-order replay-delta adoptions (child seq
	// before parent), keyed by the awaited parent seq so an adopt at N pops the
	// child at N+1 and cascade-adopts it. Multi-level chains cascade via bounded
	// recursion. Unlike the two pending* maps, this holds the ledger payload.
	heldAdoptions map[uint32]*pendingAdopt

	// hooks provides event callbacks for external subscribers
	hooks *EventHooks

	// needsInitialSync is true when the node is in consensus mode
	// and hasn't yet adopted a ledger from peers.
	needsInitialSync bool

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
	// and SubmitOpenLedgerTx(local=true) push each non-permanent result in;
	// Accept replays the pool onto every rebuilt open view until each entry
	// applies or ages out, with stale entries swept on the validated path.
	localTxs *localtxs.LocalTxs

	// txRelay re-broadcasts a recovered tx blob to peers, threaded into
	// OpenLedger.Accept's relay callback so post-LCL replayed txs re-propagate.
	// Nil when overlay broadcast is unwired (tests).
	txRelay func(blob []byte)

	// submittedTxCallback fires from SubmitTransaction only when the tx applied
	// to the open ledger, feeding the transactions_proposed/accounts_proposed
	// WebSocket streams.
	submittedTxCallback SubmittedTxCallback

	// feeTrack is the local LoadFeeTrack mirror, always non-nil. Drivers:
	//   - Raise/LowerLocalFee: per ledger close via tickLoadFeeLocked.
	//   - SetRemoteFee: from OnLedgerFullyValidated, median of trusted LoadFees.
	//   - SetClusterFee: from the Overlay's TMCluster ingress.
	feeTrack *feetrack.LoadFeeTrack

	// lastConsensusRoundTime is the most recent consensus round duration, fed to
	// the TxQ's timeLeap flag by processClosedLedgerLocked. Zero in standalone.
	lastConsensusRoundTime time.Duration

	// stallPing fires once per ledger close so the stall watchdog sees progress.
	// Atomic pointer so it can be installed without s.mu; nil disables it.
	stallPing atomic.Pointer[func()]

	// configCacheMu guards the memoised open-ledger ApplyConfig below. The config
	// is a pure function of closedLedger, rebuilt only when it advances, keeping
	// per-tx ingress off an O(amendments) parse + Rules allocation per submit.
	// A dedicated mutex lets RLock-only SubmitOpenLedgerTx callers populate it.
	// Lock order is always s.mu → configCacheMu (the caller holds s.mu, keeping
	// closedLedger stable as the cache key).
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
	// TxQ defaults; standalone raises MinimumTxnInLedger so fee escalation stays
	// out of integration tests. The [transaction_queue] stanza overrides both.
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
// table (enabled set + majority projection + block detection). Gated to
// flag-ledger windows by NeedValidatedLedger; no-op when no table is configured.
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

	genesisResult, err := genesis.Create(s.config.GenesisConfig)
	if err != nil {
		return fmt.Errorf("failed to create genesis ledger: %w", err)
	}

	// Fees are read dynamically from the FeeSettings SLE by readFeesFromLedger.
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
		// Standalone: create ledger 2 locally and start from there.
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

		openLedger, err := ledger.NewOpen(nextLedger, time.Now())
		if err != nil {
			return fmt.Errorf("failed to create open ledger: %w", err)
		}
		s.openLedger = openLedger
	} else {
		// Consensus mode: stay at genesis (seq 1) and wait to adopt a peer's ledger.
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

	s.pendingTxs = nil

	// Initialise the persistent open-ledger view, anchored on closedLedger.
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

// rebuildOpenLedgerViewLocked rebuilds s.openLedgerView from s.closedLedger
// (clears it when nil). Caller must hold s.mu (write). Used by Start and
// adopt-from-peer paths; the normal close path uses OpenLedger.Accept instead.
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

// closedLedgerCtx implements txq.ClosedLedgerContext over a closed ledger.
// baseFee converts per-tx fees into fee levels for the FeeMetrics update.
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

// processClosedLedgerLocked updates the TxQ's fee metrics from the just-closed
// ledger. timeLeap clamps the metrics window when consensus exceeded the
// slow-consensus threshold instead of advancing it. Caller must hold s.mu.
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

// slowConsensusThreshold: past this round time the TxQ treats consensus as slow
// and freezes the fee-escalation window instead of opening it.
const slowConsensusThreshold = 5 * time.Second

// SetLastConsensusRoundTime records how long the last consensus round took
// (read by processClosedLedgerLocked for timeLeap). Never called in standalone.
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

// acceptOpenLedgerViewLocked invokes OpenLedger.Accept on the LCL transition to
// s.closedLedger. No-op pre-Start. buildRetries are the build pass's retry-state
// txs, replayed first; anyDisputes is the retriesFirst flag. closedSeq is for log
// context only. Caller must hold s.mu.
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
	// Modifier promotes queued candidates into the new open view after replay.
	modifier := func(view *ledger.Ledger) {
		if s.txQueue == nil || view == nil {
			return
		}
		viewCfg := cfg
		viewCfg.LedgerSequence = view.Sequence()
		adapter := openledger.NewTxqAdapter(view, viewCfg)
		_ = s.txQueue.Accept(adapter)
	}
	// Pass the held local pool so entries replay onto the new open view.
	// Sweeping happens on the validated path, not every close (which may fork).
	var locals []openledger.PendingTx
	if s.localTxs != nil {
		locals = s.localTxs.GetTxSet()
	}
	// Seed retries with the build-pass leftovers; Accept drains then re-fills it.
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

// applyConfigLocked returns the ApplyConfig for the current closed ledger,
// memoised per closed ledger so per-tx ingress avoids re-parsing the Amendments
// SLE and re-allocating Rules. The returned value is a copy; callers may mutate
// per-submission fields without affecting the cache. Caller must hold s.mu (read).
func (s *Service) applyConfigLocked() (openledger.ApplyConfig, error) {
	closed := s.closedLedger
	if closed == nil {
		return openledger.ApplyConfig{}, ErrNoClosedLedger
	}

	s.configCacheMu.Lock()
	defer s.configCacheMu.Unlock()
	// Pointer identity is a sufficient key: each close installs a fresh immutable
	// ledger, and the cache pins it so its address can't be reused while keyed.
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

// SubmitOpenLedgerTx routes a tx blob through the persistent OpenLedger view and
// returns the per-tx classification (ResultFailure before Start).
//
// local=true (RPC-originated) pushes any non-Failure result into the LocalTxs
// held pool so it survives LCL transitions until the sender's sequence advances
// or it ages out. local=false (peer relay) doesn't pin the blob — the peer
// manages its own resends.
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

// GetLedgerBySequence returns a ledger by sequence, falling back to the open
// ledger when its sequence matches.
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

// GetAdoptedLedgerBySequence returns a closed ledger from adopted history only,
// never the mutable open ledger — the consensus catch-up walk needs immutable,
// parent-hash-chained ledgers.
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

// ledgerHistoryRangeLocked returns the inclusive [min, max] span of in-memory
// history, or ok=false when empty. Caller holds s.mu. NB: the span assumes
// contiguity — purges/backward-fills can leave gaps, so callers reporting durable
// availability must layer their own floor (see GetServerInfo's clamp).
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

// AvailableLedgerRange returns the inclusive [min, max] range of locally held
// ledgers, or ok=false when none. Used to bound a ledger-integrity cleaning run.
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

// GetQueueAccountTxs returns the TxQ candidates queued for one account, sorted by
// SeqProxy. Backs account_info's queue_data. Empty when no TxQ is wired.
func (s *Service) GetQueueAccountTxs(account [20]byte) []*txq.CandidateDetails {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.txQueue == nil {
		return nil
	}
	return s.txQueue.GetAccountTxs(account)
}

// GetQueueAllTxs returns every TxQ candidate, ordered by fee level. Backs the
// ledger method's queue_data dump. Empty when no TxQ is wired.
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

	if minSeq, maxSeq, ok := s.ledgerHistoryRangeLocked(); ok {
		// Clamp the lower bound up to the online-delete floor: the in-memory
		// window can outlast the node store after a rotation. complete_ledgers
		// must report durable availability.
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

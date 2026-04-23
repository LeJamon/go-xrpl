package service

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/internal/tx"
	xrpllog "github.com/LeJamon/goXRPLd/log"
	"github.com/LeJamon/goXRPLd/shamap"
	"github.com/LeJamon/goXRPLd/storage/nodestore"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// Common errors
var (
	ErrNotStandalone  = errors.New("operation only valid in standalone mode")
	ErrNoOpenLedger   = errors.New("no open ledger")
	ErrNoClosedLedger = errors.New("no closed ledger")
	ErrLedgerNotFound = errors.New("ledger not found")
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
	mu sync.RWMutex

	config Config
	logger xrpllog.Logger

	// NodeStore for persistent storage (nil if in-memory only)
	nodeStore nodestore.Database

	// RelationalDB for transaction indexing (nil if not configured)
	relationalDB relationaldb.RepositoryManager

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

	// hooks provides event callbacks for external subscribers
	hooks *EventHooks

	// needsInitialSync is true when the node is in consensus mode
	// and hasn't yet adopted a ledger from peers.
	needsInitialSync bool

	// serverStateFunc optionally provides the operating mode string for server_info.
	// Set by the consensus adaptor after startup.
	serverStateFunc func() string
}

// New creates a new LedgerService
func New(cfg Config) (*Service, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = xrpllog.Discard()
	}
	s := &Service{
		config:            cfg,
		logger:            logger.Named(xrpllog.PartitionLedger),
		nodeStore:         cfg.NodeStore,
		relationalDB:      cfg.RelationalDB,
		ledgerHistory:     make(map[uint32]*ledger.Ledger),
		txIndex:           make(map[[32]byte]uint32),
		txPositionIndex:   make(map[[32]byte]uint32),
		pendingValidation: make(map[[32]byte]*LedgerAcceptedEvent),
	}

	return s, nil
}

// SetEventCallback sets the callback function for ledger events
func (s *Service) SetEventCallback(callback EventCallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventCallback = callback
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
		return errors.New("failed to create genesis ledger: " + err.Error())
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
	s.ledgerHistory[genesisLedger.Sequence()] = genesisLedger

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
			return errors.New("failed to create next ledger: " + err.Error())
		}
		if err := nextLedger.Close(time.Now(), 0); err != nil {
			return errors.New("failed to close initial ledger: " + err.Error())
		}
		if err := nextLedger.SetValidated(); err != nil {
			return errors.New("failed to validate initial ledger: " + err.Error())
		}
		s.closedLedger = nextLedger
		s.validatedLedger = nextLedger
		s.ledgerHistory[nextLedger.Sequence()] = nextLedger

		// Create the open ledger (ledger 3)
		openLedger, err := ledger.NewOpen(nextLedger, time.Now())
		if err != nil {
			return errors.New("failed to create open ledger: " + err.Error())
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
			return errors.New("failed to create open ledger: " + err.Error())
		}
		s.openLedger = openLedger
	}

	// Reset pending transactions
	s.pendingTxs = nil

	s.logger.Info("Ledger service started",
		"standalone", s.config.Standalone,
		"openLedger", s.openLedger.Sequence(),
		"needsInitialSync", s.needsInitialSync,
	)

	return nil
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

// GetLedgerBySequence returns a ledger by its sequence number
func (s *Service) GetLedgerBySequence(seq uint32) (*ledger.Ledger, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	l, ok := s.ledgerHistory[seq]
	if !ok {
		return nil, ErrLedgerNotFound
	}
	return l, nil
}

// GetLedgerByHash returns a ledger by its hash
func (s *Service) GetLedgerByHash(hash [32]byte) (*ledger.Ledger, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, l := range s.ledgerHistory {
		if l.Hash() == hash {
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
func (s *Service) AcceptLedger() (uint32, error) {
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

	closeTime := time.Now()

	// If there are pending transactions, re-apply them in canonical order
	// on a fresh ledger built from the LCL. This matches rippled's behavior
	// where open ledger transactions are re-ordered via CanonicalTXSet.
	if len(s.pendingTxs) > 0 {
		// Sort pending transactions in canonical order
		canonicalSort(s.pendingTxs)

		// Create a fresh open ledger from the LCL
		freshLedger, err := ledger.NewOpen(s.closedLedger, closeTime)
		if err != nil {
			return 0, errors.New("failed to create fresh ledger for canonical reapply: " + err.Error())
		}

		// Read fees from the LCL for the engine config
		baseFee, reserveBase, reserveIncrement := readFeesFromLedger(s.closedLedger)

		engineConfig := tx.EngineConfig{
			BaseFee:                   baseFee,
			ReserveBase:               reserveBase,
			ReserveIncrement:          reserveIncrement,
			LedgerSequence:            freshLedger.Sequence(),
			SkipSignatureVerification: s.config.Standalone,
			NetworkID:                 s.config.NetworkID,
			Logger:                    s.config.Logger,
		}

		// Multi-pass application matching rippled's BuildLedger.
		//
		// Rippled uses tapRETRY so that tec* results are NOT applied (no fee,
		// no sequence consumed). This lets the same tx be retried on the next pass.
		// Our engine doesn't support tapRETRY, so we rebuild the ledger each pass:
		//
		// Pass 0: Apply all txs. Record which got tesSUCCESS vs tec*/ter*.
		// Pass 1+: Rebuild from LCL. Re-apply only tesSUCCESS txs first (restoring
		//          state), then retry the tec*/ter* ones (which may now succeed).
		//
		// Reference: rippled BuildLedger.cpp, LEDGER_TOTAL_PASSES=3, LEDGER_RETRY_PASSES=1
		const (
			totalPasses = 3
			retryPasses = 1
		)

		type txStatus int
		const (
			txPending   txStatus = iota
			txSucceeded          // tesSUCCESS — will be re-applied on rebuilds
			txRetry              // tec*/ter* during certainRetry — try again
			txFailed             // permanently failed — skip
		)
		statuses := make(map[[32]byte]txStatus, len(s.pendingTxs))

		certainRetry := true
		for pass := 0; pass < totalPasses; pass++ {
			// Rebuild fresh from LCL each pass
			freshLedger, err = ledger.NewOpen(s.closedLedger, closeTime)
			if err != nil {
				return 0, errors.New("failed to create fresh ledger: " + err.Error())
			}
			engineConfig.LedgerSequence = freshLedger.Sequence()
			engine := tx.NewEngine(freshLedger, engineConfig)
			blockProcessor := tx.NewBlockProcessor(engine)

			changes := 0
			hasRetry := false

			for _, ptx := range s.pendingTxs {
				st := statuses[ptx.hash]

				// Skip permanently failed or tec*/ter* txs in rebuild phase.
				// On pass > 0, we ONLY apply txs that previously succeeded (to
				// rebuild state) plus txs that are being retried.
				if st == txFailed {
					continue
				}
				if pass > 0 && st == txRetry {
					// Don't apply yet — we'll retry after all succeeded txs
					continue
				}

				transaction, parseErr := tx.ParseFromBinary(ptx.txBlob)
				if parseErr != nil {
					statuses[ptx.hash] = txFailed
					continue
				}
				transaction.SetRawBytes(ptx.txBlob)

				result, applyErr := blockProcessor.ApplyTransaction(transaction, ptx.txBlob)
				if applyErr != nil {
					statuses[ptx.hash] = txFailed
					continue
				}

				engineResult := result.ApplyResult.Result
				switch {
				case engineResult.IsSuccess():
					freshLedger.AddTransactionWithMeta(result.Hash, result.TxWithMetaBlob)
					s.txIndex[result.Hash] = freshLedger.Sequence()
					if st != txSucceeded {
						changes++
					}
					statuses[ptx.hash] = txSucceeded

				case engineResult.IsTec():
					if certainRetry {
						statuses[ptx.hash] = txRetry
						hasRetry = true
					} else {
						// Final pass: apply tec* normally
						freshLedger.AddTransactionWithMeta(result.Hash, result.TxWithMetaBlob)
						s.txIndex[result.Hash] = freshLedger.Sequence()
						statuses[ptx.hash] = txSucceeded
					}

				case engineResult.ShouldRetry():
					statuses[ptx.hash] = txRetry
					hasRetry = true

				default:
					statuses[ptx.hash] = txFailed
				}
			}

			// Now retry the tec*/ter* transactions (state from succeeded txs is in place)
			if pass > 0 {
				for _, ptx := range s.pendingTxs {
					if statuses[ptx.hash] != txRetry {
						continue
					}

					transaction, parseErr := tx.ParseFromBinary(ptx.txBlob)
					if parseErr != nil {
						statuses[ptx.hash] = txFailed
						continue
					}
					transaction.SetRawBytes(ptx.txBlob)

					result, applyErr := blockProcessor.ApplyTransaction(transaction, ptx.txBlob)
					if applyErr != nil {
						statuses[ptx.hash] = txFailed
						continue
					}

					engineResult := result.ApplyResult.Result
					switch {
					case engineResult.IsSuccess():
						freshLedger.AddTransactionWithMeta(result.Hash, result.TxWithMetaBlob)
						s.txIndex[result.Hash] = freshLedger.Sequence()
						changes++
						statuses[ptx.hash] = txSucceeded

					case engineResult.IsTec():
						if certainRetry {
							hasRetry = true
						} else {
							freshLedger.AddTransactionWithMeta(result.Hash, result.TxWithMetaBlob)
							s.txIndex[result.Hash] = freshLedger.Sequence()
							statuses[ptx.hash] = txSucceeded
						}

					case engineResult.ShouldRetry():
						hasRetry = true

					default:
						statuses[ptx.hash] = txFailed
					}
				}
			}

			if !hasRetry {
				break
			}
			if changes == 0 && !certainRetry {
				break
			}
			if changes == 0 || pass >= retryPasses {
				certainRetry = false
			}
		}

		// Replace the open ledger with the canonically-built one
		s.openLedger = freshLedger
	}

	// Reset pending transactions
	s.pendingTxs = nil

	// Close the current open ledger
	if err := s.openLedger.Close(closeTime, 0); err != nil {
		return 0, errors.New("failed to close ledger: " + err.Error())
	}

	// In standalone mode, immediately validate
	if err := s.openLedger.SetValidated(); err != nil {
		return 0, errors.New("failed to validate ledger: " + err.Error())
	}

	// Persist the closed ledger to storage backends (nodestore and/or relational DB).
	// persistLedger has internal nil guards for each backend.
	if err := s.persistLedger(s.openLedger); err != nil {
		return 0, errors.New("failed to persist ledger: " + err.Error())
	}

	// Store the closed ledger in memory cache
	closedSeq := s.openLedger.Sequence()
	closedLedgerHash := s.openLedger.Hash()
	s.closedLedger = s.openLedger
	s.validatedLedger = s.openLedger
	s.ledgerHistory[closedSeq] = s.openLedger

	// Collect transaction results for event callbacks/hooks
	var txResults []TransactionResultEvent
	if s.eventCallback != nil || (s.hooks != nil && (s.hooks.OnLedgerClosed != nil || s.hooks.OnTransaction != nil)) {
		txResults = s.collectTransactionResults(s.closedLedger, closedSeq, closedLedgerHash)
	}

	// Create new open ledger
	newOpen, err := ledger.NewOpen(s.closedLedger, time.Now())
	if err != nil {
		return 0, errors.New("failed to create new open ledger: " + err.Error())
	}
	s.openLedger = newOpen

	// Build ledger info for callbacks
	ledgerInfo := &LedgerInfo{
		Sequence:   closedSeq,
		Hash:       closedLedgerHash,
		ParentHash: s.closedLedger.ParentHash(),
		CloseTime:  s.closedLedger.CloseTime(),
		TotalDrops: s.closedLedger.TotalDrops(),
		Validated:  s.closedLedger.IsValidated(),
		Closed:     s.closedLedger.IsClosed(),
	}

	// Calculate validated ledgers range string
	validatedLedgers := s.getValidatedLedgersRange()

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
	if len(s.ledgerHistory) == 0 {
		return "empty"
	}

	minSeq := uint32(0xFFFFFFFF)
	maxSeq := uint32(0)
	for seq := range s.ledgerHistory {
		if seq < minSeq {
			minSeq = seq
		}
		if seq > maxSeq {
			maxSeq = seq
		}
	}

	if minSeq == maxSeq {
		return strconv.FormatUint(uint64(minSeq), 10)
	}
	return strconv.FormatUint(uint64(minSeq), 10) + "-" + strconv.FormatUint(uint64(maxSeq), 10)
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

// IsStandalone returns true if running in standalone mode
func (s *Service) IsStandalone() bool {
	return s.config.Standalone
}

// GetGenesisAccount returns the genesis account address
func (s *Service) GetGenesisAccount() (string, error) {
	_, address, err := genesis.GenerateGenesisAccountID()
	return address, err
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
	}

	if s.validatedLedger != nil {
		info.ValidatedLedgerSeq = s.validatedLedger.Sequence()
		info.ValidatedLedgerHash = s.validatedLedger.Hash()
	}

	// Calculate complete ledgers range
	if len(s.ledgerHistory) > 0 {
		minSeq := uint32(0xFFFFFFFF)
		maxSeq := uint32(0)
		for seq := range s.ledgerHistory {
			if seq < minSeq {
				minSeq = seq
			}
			if seq > maxSeq {
				maxSeq = seq
			}
		}
		if minSeq == maxSeq {
			info.CompleteLedgers = strconv.FormatUint(uint64(minSeq), 10)
		} else {
			info.CompleteLedgers = formatRange(minSeq, maxSeq)
		}
	}

	return info
}

// ServerInfo contains basic server status information
type ServerInfo struct {
	Standalone          bool
	ServerState         string // "disconnected", "connected", "syncing", "tracking", "full"
	OpenLedgerSeq       uint32
	ClosedLedgerSeq     uint32
	ClosedLedgerHash    [32]byte
	ValidatedLedgerSeq  uint32
	ValidatedLedgerHash [32]byte
	CompleteLedgers     string
	NetworkID           uint32
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
func (s *Service) AcceptConsensusResult(parent *ledger.Ledger, txBlobs [][]byte, closeTime time.Time) (uint32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closedLedger == nil {
		return 0, ErrNoClosedLedger
	}

	// If the parent differs from our closed ledger (chain switch via wrong
	// ledger detection), reset internal state to build on the correct chain.
	if parent != nil && parent.Sequence() != s.closedLedger.Sequence() {
		s.closedLedger = parent
		s.ledgerHistory[parent.Sequence()] = parent
		newOpen, err := ledger.NewOpen(parent, closeTime)
		if err != nil {
			return 0, fmt.Errorf("failed to create open ledger from parent: %w", err)
		}
		s.openLedger = newOpen
	}

	if s.openLedger == nil {
		return 0, ErrNoOpenLedger
	}

	if len(txBlobs) > 0 {
		// Convert raw blobs to pendingTx structs for canonical sorting
		pending := make([]pendingTx, 0, len(txBlobs))
		for _, blob := range txBlobs {
			ptx, err := parsePendingTx(blob)
			if err != nil {
				continue // skip unparseable transactions
			}
			pending = append(pending, ptx)
		}

		// Sort in canonical order
		canonicalSort(pending)

		// Multi-pass application (same as AcceptLedger)
		freshLedger, err := ledger.NewOpen(s.closedLedger, closeTime)
		if err != nil {
			return 0, errors.New("failed to create fresh ledger for consensus: " + err.Error())
		}

		baseFee, reserveBase, reserveIncrement := readFeesFromLedger(s.closedLedger)
		engineConfig := tx.EngineConfig{
			BaseFee:                   baseFee,
			ReserveBase:               reserveBase,
			ReserveIncrement:          reserveIncrement,
			LedgerSequence:            freshLedger.Sequence(),
			SkipSignatureVerification: false,
			NetworkID:                 s.config.NetworkID,
			Logger:                    s.config.Logger,
		}

		const (
			totalPasses = 3
			retryPasses = 1
		)

		type txStatus int
		const (
			txPending txStatus = iota
			txSucceeded
			txRetry
			txFailed
		)
		statuses := make(map[[32]byte]txStatus, len(pending))

		certainRetry := true
		for pass := 0; pass < totalPasses; pass++ {
			freshLedger, err = ledger.NewOpen(s.closedLedger, closeTime)
			if err != nil {
				return 0, errors.New("failed to create fresh ledger: " + err.Error())
			}
			engineConfig.LedgerSequence = freshLedger.Sequence()
			engine := tx.NewEngine(freshLedger, engineConfig)
			blockProcessor := tx.NewBlockProcessor(engine)

			changes := 0
			hasRetry := false

			for _, ptx := range pending {
				st := statuses[ptx.hash]
				if st == txFailed {
					continue
				}
				if pass > 0 && st == txRetry {
					continue
				}

				transaction, parseErr := tx.ParseFromBinary(ptx.txBlob)
				if parseErr != nil {
					statuses[ptx.hash] = txFailed
					continue
				}
				transaction.SetRawBytes(ptx.txBlob)

				result, applyErr := blockProcessor.ApplyTransaction(transaction, ptx.txBlob)
				if applyErr != nil {
					statuses[ptx.hash] = txFailed
					continue
				}

				engineResult := result.ApplyResult.Result
				switch {
				case engineResult.IsSuccess():
					freshLedger.AddTransactionWithMeta(result.Hash, result.TxWithMetaBlob)
					s.txIndex[result.Hash] = freshLedger.Sequence()
					if st != txSucceeded {
						changes++
					}
					statuses[ptx.hash] = txSucceeded
				case engineResult.IsTec():
					if certainRetry {
						statuses[ptx.hash] = txRetry
						hasRetry = true
					} else {
						freshLedger.AddTransactionWithMeta(result.Hash, result.TxWithMetaBlob)
						s.txIndex[result.Hash] = freshLedger.Sequence()
						statuses[ptx.hash] = txSucceeded
					}
				case engineResult.ShouldRetry():
					statuses[ptx.hash] = txRetry
					hasRetry = true
				default:
					statuses[ptx.hash] = txFailed
				}
			}

			// Retry tec*/ter* transactions
			if pass > 0 {
				for _, ptx := range pending {
					if statuses[ptx.hash] != txRetry {
						continue
					}
					transaction, parseErr := tx.ParseFromBinary(ptx.txBlob)
					if parseErr != nil {
						statuses[ptx.hash] = txFailed
						continue
					}
					transaction.SetRawBytes(ptx.txBlob)

					result, applyErr := blockProcessor.ApplyTransaction(transaction, ptx.txBlob)
					if applyErr != nil {
						statuses[ptx.hash] = txFailed
						continue
					}

					engineResult := result.ApplyResult.Result
					switch {
					case engineResult.IsSuccess():
						freshLedger.AddTransactionWithMeta(result.Hash, result.TxWithMetaBlob)
						s.txIndex[result.Hash] = freshLedger.Sequence()
						changes++
						statuses[ptx.hash] = txSucceeded
					case engineResult.IsTec():
						if certainRetry {
							hasRetry = true
						} else {
							freshLedger.AddTransactionWithMeta(result.Hash, result.TxWithMetaBlob)
							s.txIndex[result.Hash] = freshLedger.Sequence()
							statuses[ptx.hash] = txSucceeded
						}
					case engineResult.ShouldRetry():
						hasRetry = true
					default:
						statuses[ptx.hash] = txFailed
					}
				}
			}

			if !hasRetry {
				break
			}
			if changes == 0 && !certainRetry {
				break
			}
			if changes == 0 || pass >= retryPasses {
				certainRetry = false
			}
		}

		s.openLedger = freshLedger
	}

	// Reset pending transactions
	s.pendingTxs = nil

	// Close the ledger with the consensus-agreed close time
	if err := s.openLedger.Close(closeTime, 0); err != nil {
		return 0, errors.New("failed to close ledger: " + err.Error())
	}

	// Do NOT auto-validate — validation comes from the consensus validation tracker.

	// Persist
	if err := s.persistLedger(s.openLedger); err != nil {
		return 0, errors.New("failed to persist ledger: " + err.Error())
	}

	closedSeq := s.openLedger.Sequence()
	closedLedgerHash := s.openLedger.Hash()
	s.closedLedger = s.openLedger
	s.ledgerHistory[closedSeq] = s.openLedger

	// Collect transaction results for event callbacks/hooks
	var txResults []TransactionResultEvent
	if s.eventCallback != nil || (s.hooks != nil && (s.hooks.OnLedgerClosed != nil || s.hooks.OnTransaction != nil)) {
		txResults = s.collectTransactionResults(s.closedLedger, closedSeq, closedLedgerHash)
	}

	// Create new open ledger
	newOpen, err := ledger.NewOpen(s.closedLedger, time.Now())
	if err != nil {
		return 0, errors.New("failed to create new open ledger: " + err.Error())
	}
	s.openLedger = newOpen

	// Fire event hooks
	ledgerInfo := &LedgerInfo{
		Sequence:   closedSeq,
		Hash:       closedLedgerHash,
		ParentHash: s.closedLedger.ParentHash(),
		CloseTime:  s.closedLedger.CloseTime(),
		TotalDrops: s.closedLedger.TotalDrops(),
		Validated:  s.closedLedger.IsValidated(),
		Closed:     s.closedLedger.IsClosed(),
	}
	validatedLedgers := s.getValidatedLedgersRange()

	if s.hooks != nil && s.hooks.OnLedgerClosed != nil {
		txCount := len(txResults)
		hooks := s.hooks
		info := ledgerInfo
		vl := validatedLedgers
		go hooks.OnLedgerClosed(info, txCount, vl)
	}

	if s.hooks != nil && s.hooks.OnTransaction != nil {
		hooks := s.hooks
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
				TxIndex:  0,
			}
			go hooks.OnTransaction(txInfo, result, closedSeq, closedLedgerHash, closeTimeVal)
		}
	}

	// In the consensus path we do NOT fire eventCallback at close time —
	// the ledger isn't yet validated. Stash the event keyed by hash so
	// SetValidatedLedger can fire it once trusted-validation quorum is
	// reached, keeping WebSocket ledgerClosed events in lockstep with
	// server_info.validated_ledger. Rippled publishes both from the
	// same quorum-gated point (pubLedger / checkAccept).
	if s.eventCallback != nil {
		event := &LedgerAcceptedEvent{
			LedgerInfo:         ledgerInfo,
			TransactionResults: txResults,
		}
		s.stashPendingValidationLocked(closedLedgerHash, event)
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
	if !ok {
		s.mu.Unlock()
		return
	}
	if l.Hash() != expectedHash {
		s.mu.Unlock()
		return
	}
	_ = l.SetValidated()
	s.validatedLedger = l

	// Drain any stashed ledger-accepted event for this hash.
	// Fire on a goroutine (after releasing the lock) so subscriber
	// callbacks can't deadlock the service mutex.
	event := s.drainPendingValidationLocked(expectedHash)
	callback := s.eventCallback
	s.mu.Unlock()

	if event != nil && callback != nil {
		go callback(event)
	}
}

// pendingValidationMaxLen caps the pending-validation stash so a node
// that never reaches quorum (misconfigured UNL, network partition) can't
// leak memory. 16 ledgers ≈ 48s at 3s rounds — larger than any realistic
// quorum-wait window but small enough to be bounded.
const pendingValidationMaxLen = 16

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
	// ValidationTracker fires OnLedgerFullyValidated.
	s.closedLedger = adopted
	s.ledgerHistory[h.LedgerIndex] = adopted

	// Create new open ledger on top
	openLedger, err := ledger.NewOpen(adopted, time.Now())
	if err != nil {
		return fmt.Errorf("failed to create open ledger: %w", err)
	}
	s.openLedger = openLedger
	s.needsInitialSync = false

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
	s.closedLedger = adopted
	s.ledgerHistory[h.LedgerIndex] = adopted

	// Create new open ledger on top
	openLedger, err := ledger.NewOpen(adopted, time.Now())
	if err != nil {
		return fmt.Errorf("failed to create open ledger: %w", err)
	}
	s.openLedger = openLedger
	s.pendingTxs = nil

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
func (s *Service) AdoptLedgerWithState(h *header.LedgerHeader, stateMap *shamap.SHAMap, txMap *shamap.SHAMap) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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

	// Same reasoning as ReAdoptLedgerHeader: peer-adopted ledgers advance
	// closedLedger but not validatedLedger. The quorum gate owns that.
	s.closedLedger = adopted
	s.ledgerHistory[h.LedgerIndex] = adopted
	s.needsInitialSync = false

	// Persist the adopted ledger exactly as the local close path does so
	// tx/account_tx/tx_history/transaction_entry RPCs can answer queries
	// against it. Matches LedgerMaster::setFullLedger -> pendSaveValidated.
	if err := s.persistLedger(adopted); err != nil {
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

	// Create new open ledger on top
	openLedger, err := ledger.NewOpen(adopted, time.Now())
	if err != nil {
		return fmt.Errorf("failed to create open ledger: %w", err)
	}
	s.openLedger = openLedger

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
	if s.eventCallback != nil {
		event := &LedgerAcceptedEvent{
			LedgerInfo:         ledgerInfo,
			TransactionResults: txResults,
		}
		s.stashPendingValidationLocked(h.Hash, event)
	}

	s.logger.Info("Adopted ledger with full state from peer",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
		"account_hash", fmt.Sprintf("%x", h.AccountHash[:8]),
	)

	return nil
}

// GetPendingTxBlobs returns the raw transaction blobs for all pending transactions.
func (s *Service) GetPendingTxBlobs() [][]byte {
	s.mu.RLock()
	defer s.mu.RUnlock()

	blobs := make([][]byte, len(s.pendingTxs))
	for i, ptx := range s.pendingTxs {
		blobs[i] = ptx.txBlob
	}
	return blobs
}

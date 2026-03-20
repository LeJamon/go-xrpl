package service

import (
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

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

	// Pending transactions accumulated during the open ledger phase.
	// Re-applied in canonical order at AcceptLedger time.
	// Reference: rippled CanonicalTXSet / retriableTxs
	pendingTxs []pendingTx

	// EventCallback is called when a ledger is accepted (optional)
	eventCallback EventCallback

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
		config:        cfg,
		logger:        logger.Named(xrpllog.PartitionLedger),
		nodeStore:     cfg.NodeStore,
		relationalDB:  cfg.RelationalDB,
		ledgerHistory: make(map[uint32]*ledger.Ledger),
		txIndex:       make(map[[32]byte]uint32),
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

	// Fire event hooks after state is updated
	if s.hooks != nil && s.hooks.OnLedgerClosed != nil {
		txCount := len(txResults)
		hooks := s.hooks
		info := ledgerInfo
		vl := validatedLedgers
		go hooks.OnLedgerClosed(info, txCount, vl)
	}

	// Fire transaction hooks for each transaction
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
				TxIndex:  0, // TODO: Track actual tx index
			}
			go hooks.OnTransaction(txInfo, result, closedSeq, closedLedgerHash, closeTimeVal)
		}
	}

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
func (s *Service) collectTransactionResults(l *ledger.Ledger, ledgerSeq uint32, ledgerHash [32]byte) []TransactionResultEvent {
	var results []TransactionResultEvent

	// Iterate through all transactions in the ledger
	l.ForEachTransaction(func(txHash [32]byte, txData []byte) bool {
		result := TransactionResultEvent{
			TxHash:      txHash,
			TxData:      txData,
			Validated:   l.IsValidated(),
			LedgerIndex: ledgerSeq,
			LedgerHash:  ledgerHash,
		}

		// Try to extract affected accounts from transaction data
		// This is a simplified extraction - a full implementation would
		// properly parse the transaction to find all affected accounts
		result.AffectedAccounts = extractAffectedAccounts(txData)

		results = append(results, result)
		return true // continue iteration
	})

	return results
}

// extractAffectedAccounts extracts account addresses affected by a transaction
// This is a simplified implementation that extracts the Account field
func extractAffectedAccounts(txData []byte) []string {
	var accounts []string

	//TODO IMPLEMENT FUNCTION
	// In a full implementation, this would:
	// 1. Parse the transaction blob
	// 2. Extract Account (sender)
	// 3. Extract Destination (for payments)
	// 4. Extract accounts from metadata (AffectedNodes)
	//
	// For now, we return an empty list - the caller can enhance this
	// based on their needs

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

	if s.eventCallback != nil {
		event := &LedgerAcceptedEvent{
			LedgerInfo:         ledgerInfo,
			TransactionResults: txResults,
		}
		callback := s.eventCallback
		go callback(event)
	}

	s.logger.Info("Consensus ledger accepted",
		"sequence", closedSeq,
		"hash", fmt.Sprintf("%x", closedLedgerHash[:8]),
		"txs", len(txResults),
	)

	return closedSeq, nil
}

// SetValidatedLedger marks a ledger as validated by consensus.
// Called by the consensus adaptor when the validation tracker confirms
// a ledger has received sufficient validations.
func (s *Service) SetValidatedLedger(seq uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()

	l, ok := s.ledgerHistory[seq]
	if !ok {
		return
	}
	_ = l.SetValidated()
	s.validatedLedger = l
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
	// NewFromHeader creates in StateValidated — no need to call SetValidated().
	adopted := ledger.NewFromHeader(*h, stateMap, txMap, drops.Fees{})

	// Update service state
	s.closedLedger = adopted
	s.validatedLedger = adopted
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

	s.closedLedger = adopted
	s.validatedLedger = adopted
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
func (s *Service) AdoptLedgerWithState(h *header.LedgerHeader, stateMap *shamap.SHAMap) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.genesisLedger == nil {
		return errors.New("no genesis ledger available")
	}

	// Create empty tx map
	txMap, err := s.genesisLedger.TxMapSnapshot()
	if err != nil {
		return fmt.Errorf("failed to snapshot tx map: %w", err)
	}

	adopted := ledger.NewFromHeader(*h, stateMap, txMap, drops.Fees{})

	s.closedLedger = adopted
	s.validatedLedger = adopted
	s.ledgerHistory[h.LedgerIndex] = adopted
	s.needsInitialSync = false

	// Create new open ledger on top
	openLedger, err := ledger.NewOpen(adopted, time.Now())
	if err != nil {
		return fmt.Errorf("failed to create open ledger: %w", err)
	}
	s.openLedger = openLedger

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

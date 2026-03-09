package service

import (
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
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

	// GenesisConfig is the configuration for creating the genesis ledger
	GenesisConfig genesis.Config

	// NodeStore is the persistent storage for ledger nodes (optional, nil for in-memory only)
	NodeStore nodestore.Database

	// RelationalDB is the repository manager for transaction indexing (optional)
	RelationalDB relationaldb.RepositoryManager
}

// DefaultConfig returns the default service configuration
func DefaultConfig() Config {
	return Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     nil,
		RelationalDB:  nil,
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

	// Current fee settings
	fees drops.Fees

	// EventCallback is called when a ledger is accepted (optional)
	eventCallback EventCallback

	// hooks provides event callbacks for external subscribers
	hooks *EventHooks
}

// New creates a new LedgerService
func New(cfg Config) (*Service, error) {
	s := &Service{
		config:        cfg,
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

	// Convert genesis to Ledger
	fees := drops.Fees{} // TODO: properly parse from genesis
	genesisLedger := ledger.FromGenesis(
		genesisResult.Header,
		genesisResult.StateMap,
		genesisResult.TxMap,
		fees,
	)

	s.genesisLedger = genesisLedger
	s.closedLedger = genesisLedger
	s.validatedLedger = genesisLedger
	s.ledgerHistory[genesisLedger.Sequence()] = genesisLedger

	// Create the first open ledger (ledger 2)
	openLedger, err := ledger.NewOpen(genesisLedger, time.Now())
	if err != nil {
		return errors.New("failed to create open ledger: " + err.Error())
	}
	s.openLedger = openLedger

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
func (s *Service) AcceptLedger() (uint32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.config.Standalone {
		return 0, ErrNotStandalone
	}

	if s.openLedger == nil {
		return 0, ErrNoOpenLedger
	}

	// Close the current open ledger
	closeTime := time.Now()
	if err := s.openLedger.Close(closeTime, 0); err != nil {
		return 0, errors.New("failed to close ledger: " + err.Error())
	}

	// In standalone mode, immediately validate
	if err := s.openLedger.SetValidated(); err != nil {
		return 0, errors.New("failed to validate ledger: " + err.Error())
	}

	// Persist the closed ledger to nodestore if available
	if s.nodeStore != nil {
		if err := s.persistLedger(s.openLedger); err != nil {
			return 0, errors.New("failed to persist ledger: " + err.Error())
		}
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

	info := ServerInfo{
		Standalone:      s.config.Standalone,
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
			info.CompleteLedgers = string(rune(minSeq))
		} else {
			info.CompleteLedgers = formatRange(minSeq, maxSeq)
		}
	}

	return info
}

// ServerInfo contains basic server status information
type ServerInfo struct {
	Standalone          bool
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

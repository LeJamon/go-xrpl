package service

import (
	"context"
	"errors"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// SubmitResult contains the result of submitting a transaction
type SubmitResult struct {
	// Result is the engine result code
	Result tx.Result

	// Applied indicates if the transaction was applied to the ledger
	Applied bool

	// Fee is the fee charged (in drops)
	Fee uint64

	// Metadata contains the changes made by the transaction
	Metadata *tx.Metadata

	// Message is a human-readable result message
	Message string

	// CurrentLedger is the current open ledger sequence
	CurrentLedger uint32

	// ValidatedLedger is the highest validated ledger sequence
	ValidatedLedger uint32
}

// SubmitTransaction submits a transaction to the open ledger
func (s *Service) SubmitTransaction(transaction tx.Transaction) (*SubmitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.openLedger == nil {
		return nil, ErrNoOpenLedger
	}

	// Create engine config from current state
	engineConfig := tx.EngineConfig{
		BaseFee:                   10,         // Default base fee in drops
		ReserveBase:               10_000_000, // 10 XRP reserve
		ReserveIncrement:          2_000_000,  // 2 XRP per object
		LedgerSequence:            s.openLedger.Sequence(),
		SkipSignatureVerification: s.config.Standalone, // Skip signatures in standalone mode
	}

	// Create engine with the open ledger as the view
	engine := tx.NewEngine(s.openLedger, engineConfig)

	// Apply the transaction
	applyResult := engine.Apply(transaction)

	result := &SubmitResult{
		Result:          applyResult.Result,
		Applied:         applyResult.Applied,
		Fee:             applyResult.Fee,
		Metadata:        applyResult.Metadata,
		Message:         applyResult.Message,
		CurrentLedger:   s.openLedger.Sequence(),
		ValidatedLedger: 0,
	}

	if s.validatedLedger != nil {
		result.ValidatedLedger = s.validatedLedger.Sequence()
	}

	return result, nil
}

// GetCurrentFees returns the current fee settings
func (s *Service) GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return default fees for now
	// In a full implementation, these would come from the FeeSettings ledger entry
	return 10, 10_000_000, 2_000_000
}

// TransactionResult contains a transaction and its metadata
type TransactionResult struct {
	TxData      []byte
	LedgerIndex uint32
	LedgerHash  [32]byte
	Validated   bool
	TxIndex     uint32
}

// GetTransaction retrieves a transaction by its hash
func (s *Service) GetTransaction(txHash [32]byte) (*TransactionResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Look up which ledger contains this transaction
	ledgerSeq, found := s.txIndex[txHash]
	if !found {
		return nil, errors.New("transaction not found")
	}

	// Get the ledger
	l, ok := s.ledgerHistory[ledgerSeq]
	if !ok {
		return nil, errors.New("ledger not found")
	}

	// Get the transaction data
	txData, found, err := l.GetTransaction(txHash)
	if err != nil {
		return nil, errors.New("failed to get transaction: " + err.Error())
	}
	if !found {
		return nil, errors.New("transaction not found in ledger")
	}

	return &TransactionResult{
		TxData:      txData,
		LedgerIndex: ledgerSeq,
		LedgerHash:  l.Hash(),
		Validated:   l.IsValidated(),
		TxIndex:     0, // TODO: Track transaction index within ledger
	}, nil
}

// StoreTransaction stores a transaction in the current open ledger
func (s *Service) StoreTransaction(txHash [32]byte, txData []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.openLedger == nil {
		return ErrNoOpenLedger
	}

	// Add to the open ledger's transaction map
	if err := s.openLedger.AddTransaction(txHash, txData); err != nil {
		return err
	}

	// Index the transaction to the current open ledger sequence
	s.txIndex[txHash] = s.openLedger.Sequence()

	return nil
}

// SimulateTransaction runs a transaction against a snapshot of the open ledger
// without committing changes. Returns the result and metadata.
func (s *Service) SimulateTransaction(transaction tx.Transaction) (*SubmitResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.openLedger == nil {
		return nil, ErrNoOpenLedger
	}

	// Create a snapshot of the open ledger's state map for isolation
	snapshot, err := s.openLedger.StateMapSnapshot()
	if err != nil {
		return nil, errors.New("failed to create ledger snapshot: " + err.Error())
	}

	// Create a temporary ledger view backed by the snapshot
	simView := newSnapshotView(snapshot, s.openLedger)

	// Create engine config from current state
	engineConfig := tx.EngineConfig{
		BaseFee:                   10,
		ReserveBase:               10_000_000,
		ReserveIncrement:          2_000_000,
		LedgerSequence:            s.openLedger.Sequence(),
		SkipSignatureVerification: true, // Skip signatures for simulation
	}

	// Create engine with the snapshot view
	engine := tx.NewEngine(simView, engineConfig)

	// Apply the transaction (changes go to the snapshot, not the real ledger)
	applyResult := engine.Apply(transaction)

	result := &SubmitResult{
		Result:          applyResult.Result,
		Applied:         applyResult.Applied,
		Fee:             applyResult.Fee,
		Metadata:        applyResult.Metadata,
		Message:         applyResult.Message,
		CurrentLedger:   s.openLedger.Sequence(),
		ValidatedLedger: 0,
	}

	if s.validatedLedger != nil {
		result.ValidatedLedger = s.validatedLedger.Sequence()
	}

	return result, nil
}

// AccountTxResult contains the result of account_tx query
type AccountTxResult struct {
	Account      string                        `json:"account"`
	LedgerMin    uint32                        `json:"ledger_index_min"`
	LedgerMax    uint32                        `json:"ledger_index_max"`
	Limit        uint32                        `json:"limit"`
	Marker       *relationaldb.AccountTxMarker `json:"marker,omitempty"`
	Transactions []AccountTransaction          `json:"transactions"`
	Validated    bool                          `json:"validated"`
}

// AccountTransaction contains transaction data for account_tx
type AccountTransaction struct {
	Hash        [32]byte `json:"hash"`
	LedgerIndex uint32   `json:"ledger_index"`
	TxBlob      []byte   `json:"tx_blob,omitempty"`
	Meta        []byte   `json:"meta,omitempty"`
}

// GetAccountTransactions retrieves transaction history for an account
func (s *Service) GetAccountTransactions(account string, ledgerMin, ledgerMax int64, limit uint32, marker *relationaldb.AccountTxMarker, forward bool) (*AccountTxResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// If no RelationalDB, return error
	if s.relationalDB == nil {
		return nil, errors.New("transaction history not available (no database configured)")
	}

	// Decode account address
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil {
		return nil, errors.New("invalid account address: " + err.Error())
	}
	var accountID relationaldb.AccountID
	copy(accountID[:], accountIDBytes)

	// Set defaults
	if limit == 0 || limit > 400 {
		limit = 200
	}

	// Determine ledger range
	minLedger := relationaldb.LedgerIndex(1)
	maxLedger := relationaldb.LedgerIndex(0xFFFFFFFF)
	if ledgerMin >= 0 {
		minLedger = relationaldb.LedgerIndex(ledgerMin)
	}
	if ledgerMax >= 0 {
		maxLedger = relationaldb.LedgerIndex(ledgerMax)
	}
	if s.validatedLedger != nil && maxLedger > relationaldb.LedgerIndex(s.validatedLedger.Sequence()) {
		maxLedger = relationaldb.LedgerIndex(s.validatedLedger.Sequence())
	}

	ctx := context.Background()
	options := relationaldb.AccountTxPageOptions{
		Account:   accountID,
		MinLedger: minLedger,
		MaxLedger: maxLedger,
		Marker:    marker,
		Limit:     limit,
	}

	var txResult *relationaldb.AccountTxResult
	if forward {
		txResult, err = s.relationalDB.AccountTransaction().GetOldestAccountTxsPage(ctx, options)
	} else {
		txResult, err = s.relationalDB.AccountTransaction().GetNewestAccountTxsPage(ctx, options)
	}
	if err != nil {
		return nil, err
	}

	// Convert to result
	result := &AccountTxResult{
		Account:      account,
		LedgerMin:    uint32(txResult.LedgerRange.Min),
		LedgerMax:    uint32(txResult.LedgerRange.Max),
		Limit:        txResult.Limit,
		Marker:       txResult.Marker,
		Transactions: make([]AccountTransaction, 0, len(txResult.Transactions)),
		Validated:    true,
	}

	for _, txInfo := range txResult.Transactions {
		result.Transactions = append(result.Transactions, AccountTransaction{
			Hash:        [32]byte(txInfo.Hash),
			LedgerIndex: uint32(txInfo.LedgerSeq),
			TxBlob:      txInfo.RawTxn,
			Meta:        txInfo.TxnMeta,
		})
	}

	return result, nil
}

// TxHistoryResult contains the result of tx_history query
type TxHistoryResult struct {
	Index        uint32               `json:"index"`
	Transactions []AccountTransaction `json:"txs"`
}

// GetTransactionHistory retrieves recent transactions
func (s *Service) GetTransactionHistory(startIndex uint32) (*TxHistoryResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.relationalDB == nil {
		return nil, errors.New("transaction history not available (no database configured)")
	}

	ctx := context.Background()
	txInfos, err := s.relationalDB.Transaction().GetTxHistory(ctx, relationaldb.LedgerIndex(startIndex), 20)
	if err != nil {
		return nil, err
	}

	result := &TxHistoryResult{
		Index:        startIndex,
		Transactions: make([]AccountTransaction, 0, len(txInfos)),
	}

	for _, txInfo := range txInfos {
		result.Transactions = append(result.Transactions, AccountTransaction{
			Hash:        [32]byte(txInfo.Hash),
			LedgerIndex: uint32(txInfo.LedgerSeq),
			TxBlob:      txInfo.RawTxn,
			Meta:        txInfo.TxnMeta,
		})
	}

	return result, nil
}

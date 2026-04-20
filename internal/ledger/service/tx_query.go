package service

import (
	"context"
	"errors"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
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

// SubmitTransaction submits a transaction to the open ledger.
// The rawBlob parameter is the original binary transaction blob; it is stored
// so that AcceptLedger can re-apply transactions in canonical order.
func (s *Service) SubmitTransaction(transaction tx.Transaction, rawBlob []byte) (*SubmitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.openLedger == nil {
		return nil, ErrNoOpenLedger
	}

	// Read fee settings from the FeeSettings SLE in the open ledger
	baseFee, reserveBase, reserveIncrement := readFeesFromLedger(s.openLedger)

	// Create engine config from current state
	engineConfig := tx.EngineConfig{
		BaseFee:                   baseFee,
		ReserveBase:               reserveBase,
		ReserveIncrement:          reserveIncrement,
		LedgerSequence:            s.openLedger.Sequence(),
		SkipSignatureVerification: s.config.Standalone, // Skip signatures in standalone mode
		OpenLedger:                true,                // Live submission: check fee adequacy
		NetworkID:                 s.config.NetworkID,
		Logger:                    s.config.Logger,
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

	// Track successfully applied transactions for canonical re-ordering at AcceptLedger.
	// Reference: rippled accumulates applied txs for CanonicalTXSet reapply.
	if applyResult.Applied && rawBlob != nil {
		common := transaction.GetCommon()

		// Decode account address to raw 20-byte AccountID
		var accountID [20]byte
		_, accountBytes, err := addresscodec.DecodeClassicAddressToAccountID(common.Account)
		if err == nil && len(accountBytes) == 20 {
			copy(accountID[:], accountBytes)
		}

		// Compute transaction hash from the original signed blob.
		// We set raw bytes so ComputeTransactionHash uses the exact signed bytes
		// rather than re-serializing (which can produce a different blob/hash).
		transaction.SetRawBytes(rawBlob)
		txHash, hashErr := tx.ComputeTransactionHash(transaction)
		if hashErr == nil {
			s.pendingTxs = append(s.pendingTxs, pendingTx{
				txBlob:   rawBlob,
				hash:     txHash,
				account:  accountID,
				sequence: common.SeqProxy(),
			})
		}
	}

	return result, nil
}

// readFeesFromLedger reads fee settings from the FeeSettings SLE in the given
// ledger. It supports both the modern XRPFees format (BaseFeeDrops /
// ReserveBaseDrops / ReserveIncrementDrops) and the legacy format (BaseFee /
// ReserveBase / ReserveIncrement). Falls back to hardcoded defaults if the SLE
// cannot be found or parsed.
func readFeesFromLedger(l *ledger.Ledger) (baseFee, reserveBase, reserveIncrement uint64) {
	// Hardcoded defaults (same as rippled)
	const (
		defaultBaseFee          = 10
		defaultReserveBase      = 10_000_000
		defaultReserveIncrement = 2_000_000
	)

	if l == nil {
		return defaultBaseFee, defaultReserveBase, defaultReserveIncrement
	}

	data, err := l.Read(keylet.Fees())
	if err != nil || data == nil {
		return defaultBaseFee, defaultReserveBase, defaultReserveIncrement
	}

	feeSettings, err := state.ParseFeeSettings(data)
	if err != nil {
		return defaultBaseFee, defaultReserveBase, defaultReserveIncrement
	}

	return feeSettings.GetBaseFee(), feeSettings.GetReserveBase(), feeSettings.GetReserveIncrement()
}

// GetCurrentFees returns the current fee settings read from the FeeSettings
// ledger entry in the open ledger. Falls back to hardcoded defaults if the
// open ledger is not available or the FeeSettings SLE cannot be read.
func (s *Service) GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return readFeesFromLedger(s.openLedger)
}

// EngineConfigForReplay returns the shared (non-per-ledger) engine
// configuration for replaying a closed ledger anchored on `parent`.
// Fees come from the parent's FeeSettings SLE — replay must use the
// fees that were active when the original txs ran. NetworkID and
// Logger come from the service config.
//
// The caller is expected to override the per-ledger fields
// (LedgerSequence, ParentCloseTime, ParentHash, Rules, ApplyFlags,
// OpenLedger) from the target header before passing this config to the
// engine. ReplayDelta.Apply() does this automatically.
//
// Reference: rippled BuildLedger.cpp uses the parent's view to source
// fees; per-ledger values are stamped from the closed-ledger info.
func (s *Service) EngineConfigForReplay(parent *ledger.Ledger) tx.EngineConfig {
	baseFee, reserveBase, reserveIncrement := readFeesFromLedger(parent)
	return tx.EngineConfig{
		BaseFee:                   baseFee,
		ReserveBase:               reserveBase,
		ReserveIncrement:          reserveIncrement,
		NetworkID:                 s.config.NetworkID,
		SkipSignatureVerification: false, // replay re-checks signatures
		Logger:                    s.config.Logger,
	}
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

	// Read fee settings from the FeeSettings SLE in the open ledger
	simBaseFee, simReserveBase, simReserveIncrement := readFeesFromLedger(s.openLedger)

	// Create engine config from current state
	engineConfig := tx.EngineConfig{
		BaseFee:                   simBaseFee,
		ReserveBase:               simReserveBase,
		ReserveIncrement:          simReserveIncrement,
		LedgerSequence:            s.openLedger.Sequence(),
		SkipSignatureVerification: true, // Skip signatures for simulation
		OpenLedger:                true, // Check fee adequacy for simulation
		NetworkID:                 s.config.NetworkID,
		Logger:                    s.config.Logger,
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
	TxnSeq      uint32   `json:"txn_seq"`
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

	// Determine ledger range.
	// When ledgerMin <= 0, use 1 (earliest possible ledger).
	// When ledgerMax <= 0, use the validated ledger sequence.
	minLedger := relationaldb.LedgerIndex(1)
	if ledgerMin > 0 {
		minLedger = relationaldb.LedgerIndex(ledgerMin)
	}

	var maxLedger relationaldb.LedgerIndex
	if ledgerMax > 0 {
		maxLedger = relationaldb.LedgerIndex(ledgerMax)
	} else if s.validatedLedger != nil {
		maxLedger = relationaldb.LedgerIndex(s.validatedLedger.Sequence())
	} else {
		maxLedger = relationaldb.LedgerIndex(0xFFFFFFFF)
	}

	// Clamp max to validated ledger
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
			TxnSeq:      txInfo.TxnSeq,
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

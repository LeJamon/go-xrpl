package service

import (
	"encoding/hex"
	"time"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
)

// LedgerAcceptedEvent contains information about an accepted ledger and its transactions
type LedgerAcceptedEvent struct {
	LedgerInfo         *LedgerInfo
	TransactionResults []TransactionResultEvent
}

// TransactionResultEvent contains transaction details for event broadcasting
type TransactionResultEvent struct {
	TxHash [32]byte
	TxData []byte

	// MetaData is the transaction metadata (nil if not available)
	MetaData []byte

	// Validated indicates if the transaction is in a validated ledger
	Validated bool

	LedgerIndex      uint32
	LedgerHash       [32]byte
	AffectedAccounts []string
}

type EventCallback func(event *LedgerAcceptedEvent)

// SubmittedTxEvent carries the inputs the WebSocket transactions_proposed
// publisher needs from a SubmitTransaction call.
type SubmittedTxEvent struct {
	RawBlob []byte
	TxHash  [32]byte
	// AffectedAccounts is the full mentioned-accounts set so accounts_proposed
	// fans out to every party referenced by the tx (source, destination,
	// regular key, signers, ...).
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

func (s *Service) SetEventCallback(callback EventCallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventCallback = callback
}

// SetSubmittedTxCallback registers a sink fired from SubmitTransaction after
// every apply attempt. Pass nil to unwire.
func (s *Service) SetSubmittedTxCallback(fn SubmittedTxCallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.submittedTxCallback = fn
}

// SetTxRelay registers the per-tx broadcast handler invoked by
// OpenLedger.Accept's relay callback. Pass nil to unwire.
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

// SetEventHooks registers structured event hooks (richer than SetEventCallback).
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

// fireLedgerClosedHooksLocked fires hooks.OnLedgerClosed and OnTransaction for a
// closed ledger. Each hook runs on its own goroutine so subscriber callbacks
// can't deadlock against s.mu; safe with nil hooks. Caller must hold s.mu.
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

// collectTransactionResults gathers per-tx results from the closed ledger and
// populates s.txIndex/s.txPositionIndex (hash -> seq, position). Idempotent with
// the Apply-time write; the sole index site for the Apply-less peer-adopt path.
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

// extractAffectedAccounts parses the tx blob and returns the account-typed
// fields it mentions (Account, Destination, ...).
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

package service

import (
	"encoding/hex"
	"time"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
)

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

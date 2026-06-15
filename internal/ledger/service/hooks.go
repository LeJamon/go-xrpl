package service

import (
	"time"
)

// EventHooks allows external systems to subscribe to ledger events.
// This provides a decoupled way for the RPC layer to receive notifications
// about ledger state changes without the ledger service depending on RPC types.
type EventHooks struct {
	// OnLedgerClosed is called when a ledger is closed and validated.
	// Parameters:
	//   - info: LedgerInfo containing details about the closed ledger
	//   - txCount: Number of transactions in the ledger
	//   - validatedLedgers: String representation of validated ledger range (e.g., "1-100")
	OnLedgerClosed func(info *LedgerInfo, txCount int, validatedLedgers string)

	// OnTransaction is called for each transaction when a ledger closes.
	// Parameters:
	//   - tx: The transaction details
	//   - result: The transaction result (success/failure code and metadata)
	//   - ledgerSeq: The ledger sequence number containing this transaction
	//   - ledgerHash: The hash of the ledger containing this transaction
	//   - ledgerCloseTime: The close time of the ledger
	OnTransaction func(tx TransactionInfo, result TxResult, ledgerSeq uint32, ledgerHash [32]byte, ledgerCloseTime time.Time)

	// OnConsensusPhase is called when the consensus phase changes.
	// Parameters:
	//   - phase: The new consensus phase ("open", "establish", "accepted")
	OnConsensusPhase func(phase string)
}

// TransactionInfo contains information about a transaction for event hooks.
type TransactionInfo struct {
	// Hash is the transaction hash
	Hash [32]byte

	// TxBlob is the raw transaction bytes
	TxBlob []byte

	// AffectedAccounts is a list of accounts affected by this transaction
	AffectedAccounts []string
}

// TxResult contains the result of applying a transaction.
type TxResult struct {
	// Applied indicates if the transaction was successfully applied
	Applied bool

	// Metadata is the transaction metadata (serialized)
	Metadata []byte

	// TxIndex is the transaction's index within the ledger
	TxIndex uint32
}

// DefaultEventHooks returns an EventHooks with no-op handlers.
func DefaultEventHooks() *EventHooks {
	return &EventHooks{
		OnLedgerClosed: func(info *LedgerInfo, txCount int, validatedLedgers string) {},
		OnTransaction: func(tx TransactionInfo, result TxResult, ledgerSeq uint32, ledgerHash [32]byte, ledgerCloseTime time.Time) {
		},
		OnConsensusPhase: func(phase string) {},
	}
}

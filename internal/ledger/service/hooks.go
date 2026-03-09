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

	// OnValidation is called when a validation is received (for future use in non-standalone mode).
	// Parameters:
	//   - validation: The validation information
	OnValidation func(validation ValidationInfo)

	// OnServerStatusChange is called when server status changes (load factors, etc.).
	// Parameters:
	//   - status: The new server status
	OnServerStatusChange func(status ServerStatus)
}

// TransactionInfo contains information about a transaction for event hooks
type TransactionInfo struct {
	// Hash is the transaction hash
	Hash [32]byte

	// TxBlob is the raw transaction bytes
	TxBlob []byte

	// AffectedAccounts is a list of accounts affected by this transaction
	AffectedAccounts []string

	// TransactionType is the type of transaction (e.g., "Payment", "TrustSet")
	TransactionType string

	// Sequence is the transaction sequence number
	Sequence uint32

	// Fee is the transaction fee in drops
	Fee uint64

	// SourceAccount is the account that initiated the transaction
	SourceAccount string

	// DestinationAccount is the destination account (for payments)
	DestinationAccount string
}

// TxResult contains the result of applying a transaction
type TxResult struct {
	// ResultCode is the engine result code (e.g., "tesSUCCESS")
	ResultCode string

	// ResultCodeNum is the numeric result code
	ResultCodeNum int

	// Message is a human-readable result message
	Message string

	// Applied indicates if the transaction was successfully applied
	Applied bool

	// Metadata is the transaction metadata (serialized)
	Metadata []byte

	// TxIndex is the transaction's index within the ledger
	TxIndex uint32
}

// ValidationInfo contains information about a validation (for future use)
type ValidationInfo struct {
	// LedgerHash is the hash of the ledger being validated
	LedgerHash [32]byte

	// LedgerIndex is the sequence number of the ledger being validated
	LedgerIndex uint32

	// ValidatorPublicKey is the public key of the validator
	ValidatorPublicKey string

	// Signature is the validation signature
	Signature []byte

	// SigningTime is when the validation was signed
	SigningTime time.Time

	// Full indicates if this is a full validation
	Full bool

	// Flags are the validation flags
	Flags uint32
}

// ServerStatus contains server status information for events
type ServerStatus struct {
	// LoadBase is the base load (256 = normal)
	LoadBase int

	// LoadFactor is the current load factor
	LoadFactor int

	// LoadFactorLocal is the local load factor
	LoadFactorLocal int

	// LoadFactorNet is the network load factor
	LoadFactorNet int

	// LoadFactorCluster is the cluster load factor
	LoadFactorCluster int

	// ServerState is the current server state
	ServerState string
}

// DefaultEventHooks returns an EventHooks with no-op handlers
func DefaultEventHooks() *EventHooks {
	return &EventHooks{
		OnLedgerClosed: func(info *LedgerInfo, txCount int, validatedLedgers string) {},
		OnTransaction: func(tx TransactionInfo, result TxResult, ledgerSeq uint32, ledgerHash [32]byte, ledgerCloseTime time.Time) {
		},
		OnConsensusPhase:     func(phase string) {},
		OnValidation:         func(validation ValidationInfo) {},
		OnServerStatusChange: func(status ServerStatus) {},
	}
}

// NewEventHooks creates a new EventHooks with all handlers set to the provided functions.
// Any nil handlers will be replaced with no-op functions.
func NewEventHooks(
	onLedgerClosed func(*LedgerInfo, int, string),
	onTransaction func(TransactionInfo, TxResult, uint32, [32]byte, time.Time),
) *EventHooks {
	hooks := DefaultEventHooks()

	if onLedgerClosed != nil {
		hooks.OnLedgerClosed = onLedgerClosed
	}
	if onTransaction != nil {
		hooks.OnTransaction = onTransaction
	}

	return hooks
}

// SetOnLedgerClosed sets the ledger closed handler
func (h *EventHooks) SetOnLedgerClosed(handler func(*LedgerInfo, int, string)) {
	if handler != nil {
		h.OnLedgerClosed = handler
	}
}

// SetOnTransaction sets the transaction handler
func (h *EventHooks) SetOnTransaction(handler func(TransactionInfo, TxResult, uint32, [32]byte, time.Time)) {
	if handler != nil {
		h.OnTransaction = handler
	}
}

// SetOnConsensusPhase sets the consensus phase handler
func (h *EventHooks) SetOnConsensusPhase(handler func(string)) {
	if handler != nil {
		h.OnConsensusPhase = handler
	}
}

// SetOnValidation sets the validation handler
func (h *EventHooks) SetOnValidation(handler func(ValidationInfo)) {
	if handler != nil {
		h.OnValidation = handler
	}
}

// SetOnServerStatusChange sets the server status change handler
func (h *EventHooks) SetOnServerStatusChange(handler func(ServerStatus)) {
	if handler != nil {
		h.OnServerStatusChange = handler
	}
}

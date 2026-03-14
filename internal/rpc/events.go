package rpc

import (
	"encoding/json"
	"time"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// RippleEpoch is January 1, 2000 00:00:00 UTC - used for XRPL time calculations
var RippleEpochRPC = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// ToRippleTime converts a time.Time to seconds since Ripple epoch
func ToRippleTime(t time.Time) uint32 {
	return uint32(t.Unix() - RippleEpochRPC.Unix())
}

// LedgerCloseEvent represents a ledger close notification sent to subscribers
// This matches the rippled ledgerClosed stream message format
type LedgerCloseEvent struct {
	Type             string `json:"type"`              // Always "ledgerClosed"
	FeeBase          uint64 `json:"fee_base"`          // Transaction cost in fee units
	FeeRef           uint64 `json:"fee_ref"`           // Transaction cost in fee units for reference tx
	LedgerHash       string `json:"ledger_hash"`       // Hash of the ledger that closed
	LedgerIndex      uint32 `json:"ledger_index"`      // Sequence number of the ledger
	LedgerTime       uint32 `json:"ledger_time"`       // Close time in seconds since Ripple epoch
	ReserveBase      uint64 `json:"reserve_base"`      // Minimum reserve requirement in drops
	ReserveInc       uint64 `json:"reserve_inc"`       // Owner reserve increment in drops
	TxnCount         int    `json:"txn_count"`         // Number of transactions in the ledger
	ValidatedLedgers string `json:"validated_ledgers"` // Range of validated ledgers (e.g., "1-100")
	// Optional fields for API v2+
	ValidatedHash string `json:"validated_hash,omitempty"` // Hash of the validated ledger (API v2)
	Validated     bool   `json:"validated,omitempty"`      // Whether this ledger is validated
}

// NewLedgerCloseEvent creates a new LedgerCloseEvent with required fields
func NewLedgerCloseEvent(
	ledgerHash string,
	ledgerIndex uint32,
	closeTime time.Time,
	feeBase, feeRef, reserveBase, reserveInc uint64,
	txnCount int,
	validatedLedgers string,
) *LedgerCloseEvent {
	return &LedgerCloseEvent{
		Type:             "ledgerClosed",
		FeeBase:          feeBase,
		FeeRef:           feeRef,
		LedgerHash:       ledgerHash,
		LedgerIndex:      ledgerIndex,
		LedgerTime:       ToRippleTime(closeTime),
		ReserveBase:      reserveBase,
		ReserveInc:       reserveInc,
		TxnCount:         txnCount,
		ValidatedLedgers: validatedLedgers,
		Validated:        true,
	}
}

// TransactionEvent represents a transaction notification sent to subscribers
// This matches the rippled transaction stream message format
type TransactionEvent struct {
	Type                string          `json:"type"`                           // Always "transaction"
	EngineResult        string          `json:"engine_result"`                  // Transaction engine result code (e.g., "tesSUCCESS")
	EngineResultCode    int             `json:"engine_result_code"`             // Numeric result code
	EngineResultMessage string          `json:"engine_result_message"`          // Human-readable result message
	LedgerCurrentIndex  uint32          `json:"ledger_current_index,omitempty"` // Current ledger index (for proposed)
	LedgerHash          string          `json:"ledger_hash,omitempty"`          // Hash of the ledger containing the tx
	LedgerIndex         uint32          `json:"ledger_index,omitempty"`         // Sequence of the ledger containing the tx
	Meta                json.RawMessage `json:"meta,omitempty"`                 // Transaction metadata (for validated)
	Transaction         json.RawMessage `json:"transaction"`                    // The transaction object
	TxJson              json.RawMessage `json:"tx_json,omitempty"`              // Transaction JSON (alternative format)
	Hash                string          `json:"hash,omitempty"`                 // Transaction hash
	Validated           bool            `json:"validated"`                      // Whether tx is in a validated ledger
	Status              string          `json:"status,omitempty"`               // Status for proposed transactions
	// Account subscription specific fields
	Account string `json:"account,omitempty"` // Account that was affected (for account subscriptions)
}

// NewTransactionEvent creates a new TransactionEvent
func NewTransactionEvent(
	txJSON json.RawMessage,
	meta json.RawMessage,
	hash string,
	ledgerIndex uint32,
	ledgerHash string,
	engineResult string,
	engineResultCode int,
	engineResultMessage string,
	validated bool,
) *TransactionEvent {
	return &TransactionEvent{
		Type:                "transaction",
		Transaction:         txJSON,
		Meta:                meta,
		Hash:                hash,
		LedgerIndex:         ledgerIndex,
		LedgerHash:          ledgerHash,
		EngineResult:        engineResult,
		EngineResultCode:    engineResultCode,
		EngineResultMessage: engineResultMessage,
		Validated:           validated,
	}
}

// ValidationEvent represents a validation message from a validator
// This matches the rippled validationReceived stream message format
type ValidationEvent struct {
	Type                string   `json:"type"`                     // Always "validationReceived"
	Amendments          []string `json:"amendments,omitempty"`     // Amendments this validator is voting for
	BaseFee             uint64   `json:"base_fee,omitempty"`       // Unscaled transaction cost
	Cookie              string   `json:"cookie,omitempty"`         // Unique cookie value (if any)
	Data                string   `json:"data,omitempty"`           // Additional data
	Flags               uint32   `json:"flags"`                    // Validation flags
	Full                bool     `json:"full"`                     // Whether this is a full validation
	LedgerHash          string   `json:"ledger_hash"`              // Hash of proposed ledger
	LedgerIndex         string   `json:"ledger_index"`             // Index of proposed ledger (as string)
	LoadFee             uint32   `json:"load_fee,omitempty"`       // Local load-scaled transaction cost
	MasterKey           string   `json:"master_key,omitempty"`     // Master public key (if different from signing)
	ReserveBase         uint64   `json:"reserve_base,omitempty"`   // Minimum reserve
	ReserveInc          uint64   `json:"reserve_inc,omitempty"`    // Owner reserve increment
	ServerVersion       string   `json:"server_version,omitempty"` // Version of rippled
	Signature           string   `json:"signature"`                // Signature of the validation
	SigningTime         uint32   `json:"signing_time"`             // When validation was signed
	ValidatedHash       string   `json:"validated_hash,omitempty"` // Hash of highest validated ledger
	ValidationPublicKey string   `json:"validation_public_key"`    // Public key used to sign validation
}

// NewValidationEvent creates a new ValidationEvent
func NewValidationEvent(
	ledgerHash string,
	ledgerIndex string,
	validationPublicKey string,
	signature string,
	signingTime uint32,
	flags uint32,
	full bool,
) *ValidationEvent {
	return &ValidationEvent{
		Type:                "validationReceived",
		LedgerHash:          ledgerHash,
		LedgerIndex:         ledgerIndex,
		ValidationPublicKey: validationPublicKey,
		Signature:           signature,
		SigningTime:         signingTime,
		Flags:               flags,
		Full:                full,
	}
}

// ServerStatusEvent represents server status changes
// This is sent to subscribers of the "server" stream
type ServerStatusEvent struct {
	Type                    string `json:"type"`                                 // Always "serverStatus"
	BaseFee                 uint64 `json:"base_fee,omitempty"`                   // Base fee
	LoadBase                int    `json:"load_base"`                            // Load base (256 = normal)
	LoadFactor              int    `json:"load_factor"`                          // Current load factor
	LoadFactorLocal         int    `json:"load_factor_local,omitempty"`          // Local load factor
	LoadFactorNet           int    `json:"load_factor_net,omitempty"`            // Network load factor
	LoadFactorCluster       int    `json:"load_factor_cluster,omitempty"`        // Cluster load factor
	LoadFactorFeeEscalation int    `json:"load_factor_fee_escalation,omitempty"` // Fee escalation load factor
	LoadFactorFeeQueue      int    `json:"load_factor_fee_queue,omitempty"`      // Fee queue load factor
	LoadFactorServer        int    `json:"load_factor_server,omitempty"`         // Server load factor
	ServerStatus            string `json:"server_status,omitempty"`              // Current server status
}

// NewServerStatusEvent creates a new ServerStatusEvent
func NewServerStatusEvent(loadBase, loadFactor int) *ServerStatusEvent {
	return &ServerStatusEvent{
		Type:       "serverStatus",
		LoadBase:   loadBase,
		LoadFactor: loadFactor,
	}
}

// ConsensusEvent represents consensus phase changes
// This is sent to subscribers of the "consensus" stream
type ConsensusEvent struct {
	Type      string `json:"type"`      // Always "consensusPhase"
	Consensus string `json:"consensus"` // Current consensus phase (open, establish, accepted)
}

// NewConsensusEvent creates a new ConsensusEvent
func NewConsensusEvent(phase string) *ConsensusEvent {
	return &ConsensusEvent{
		Type:      "consensusPhase",
		Consensus: phase,
	}
}

// Consensus phases
const (
	ConsensusPhaseOpen      = "open"      // Accepting transactions
	ConsensusPhaseEstablish = "establish" // Building consensus on transaction set
	ConsensusPhaseAccepted  = "accepted"  // Ledger has been accepted
)

// ManifestEvent represents a validator manifest update
// This is sent to subscribers of the "manifests" stream
type ManifestEvent struct {
	Type       string `json:"type"`        // Always "manifestReceived"
	MasterKey  string `json:"master_key"`  // Master public key
	Sequence   uint32 `json:"seq"`         // Manifest sequence number
	Signature  string `json:"signature"`   // Manifest signature
	SigningKey string `json:"signing_key"` // Ephemeral signing key
}

// NewManifestEvent creates a new ManifestEvent
func NewManifestEvent(masterKey, signingKey, signature string, sequence uint32) *ManifestEvent {
	return &ManifestEvent{
		Type:       "manifestReceived",
		MasterKey:  masterKey,
		Sequence:   sequence,
		Signature:  signature,
		SigningKey: signingKey,
	}
}

// PeerStatusEvent represents peer connection status changes
// This is sent to subscribers of the "peer_status" stream
type PeerStatusEvent struct {
	Type           string `json:"type"`                       // Always "peerStatusChange"
	Action         string `json:"action"`                     // Action type (see constants below)
	Date           uint32 `json:"date"`                       // Time of status change (Ripple epoch)
	LedgerHash     string `json:"ledger_hash,omitempty"`      // Ledger hash (if relevant)
	LedgerIndex    uint32 `json:"ledger_index,omitempty"`     // Ledger index (if relevant)
	LedgerIndexMax uint32 `json:"ledger_index_max,omitempty"` // Max ledger index peer has
	LedgerIndexMin uint32 `json:"ledger_index_min,omitempty"` // Min ledger index peer has
}

// Peer status actions
const (
	PeerActionClosingLedger  = "CLOSING_LEDGER"
	PeerActionAcceptedLedger = "ACCEPTED_LEDGER"
	PeerActionSwitchedLedger = "SWITCHED_LEDGER"
	PeerActionLostSync       = "LOST_SYNC"
)

// NewPeerStatusEvent creates a new PeerStatusEvent
func NewPeerStatusEvent(action string, date uint32) *PeerStatusEvent {
	return &PeerStatusEvent{
		Type:   "peerStatusChange",
		Action: action,
		Date:   date,
	}
}

// OrderBookChangeEvent represents changes to an order book
// This is sent to subscribers of specific order books
type OrderBookChangeEvent struct {
	Type        string          `json:"type"`   // Always "orderBookChange" or "transaction"
	Status      string          `json:"status"` // "closed" for processed changes
	LedgerIndex uint32          `json:"ledger_index,omitempty"`
	LedgerHash  string          `json:"ledger_hash,omitempty"`
	LedgerTime  uint32          `json:"ledger_time,omitempty"`
	TakerGets   json.RawMessage `json:"taker_gets,omitempty"` // What the offer provides
	TakerPays   json.RawMessage `json:"taker_pays,omitempty"` // What the offer requests
	// The transaction that caused the change
	Transaction json.RawMessage `json:"transaction,omitempty"`
	Meta        json.RawMessage `json:"meta,omitempty"`
	Validated   bool            `json:"validated,omitempty"`
}

// PathFindEvent represents path finding results
// This is sent in response to path_find create requests
type PathFindEvent struct {
	Type               string            `json:"type"`                // "path_find"
	ID                 interface{}       `json:"id,omitempty"`        // Request ID
	SourceAccount      string            `json:"source_account"`      // Source account
	DestinationAccount string            `json:"destination_account"` // Destination account
	DestinationAmount  json.RawMessage   `json:"destination_amount"`  // Amount to deliver
	FullReply          bool              `json:"full_reply"`          // Whether this is a full reply
	Alternatives       []PathAlternative `json:"alternatives"`        // Alternative paths found
}

// PathAlternative represents a single path alternative
type PathAlternative struct {
	PathsCanonical [][]types.PathStep `json:"paths_canonical,omitempty"` // Canonical path representation
	PathsComputed  [][]types.PathStep `json:"paths_computed,omitempty"`  // Computed paths
	SourceAmount   json.RawMessage    `json:"source_amount"`             // Amount to send
}

// PathStep represents a step in a payment path
type PathStepEvent struct {
	Account  string `json:"account,omitempty"`
	Currency string `json:"currency,omitempty"`
	Issuer   string `json:"issuer,omitempty"`
	Type     int    `json:"type,omitempty"`
	TypeHex  string `json:"type_hex,omitempty"`
}

// ProposedTransactionEvent represents a proposed (unvalidated) transaction
// This is sent to accounts_proposed subscribers
type ProposedTransactionEvent struct {
	Type                string          `json:"type"`                  // Always "transaction"
	EngineResult        string          `json:"engine_result"`         // Preliminary result
	EngineResultCode    int             `json:"engine_result_code"`    // Numeric code
	EngineResultMessage string          `json:"engine_result_message"` // Human message
	LedgerCurrentIndex  uint32          `json:"ledger_current_index"`  // Current open ledger
	Transaction         json.RawMessage `json:"transaction"`           // Transaction object
	Validated           bool            `json:"validated"`             // Always false for proposed
	Status              string          `json:"status,omitempty"`      // "proposed"
	Account             string          `json:"account,omitempty"`     // Affected account
}

// NewProposedTransactionEvent creates a new proposed transaction event
func NewProposedTransactionEvent(
	txJSON json.RawMessage,
	engineResult string,
	engineResultCode int,
	engineResultMessage string,
	ledgerCurrentIndex uint32,
	account string,
) *ProposedTransactionEvent {
	return &ProposedTransactionEvent{
		Type:                "transaction",
		Transaction:         txJSON,
		EngineResult:        engineResult,
		EngineResultCode:    engineResultCode,
		EngineResultMessage: engineResultMessage,
		LedgerCurrentIndex:  ledgerCurrentIndex,
		Validated:           false,
		Status:              "proposed",
		Account:             account,
	}
}

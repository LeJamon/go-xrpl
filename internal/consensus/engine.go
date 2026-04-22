package consensus

import (
	"context"
	"time"
)

// Engine is the main interface for consensus algorithms.
// Different consensus implementations (RCL, experimental algorithms)
// can implement this interface to be plugged into the node.
type Engine interface {
	// Start begins the consensus engine.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the consensus engine.
	Stop() error

	// StartRound begins a new consensus round.
	// The proposing parameter indicates if this node should propose.
	StartRound(round RoundID, proposing bool) error

	// OnProposal handles an incoming proposal. originPeer is the overlay
	// peer ID that delivered the message, or 0 for self-originated
	// proposals (unused on production ingress but convenient for tests).
	// The engine passes originPeer through to the adaptor's relay path
	// so gossip forwards can exclude the originator — mirrors rippled's
	// PeerImp::onMessage(TMProposeSet) behavior.
	OnProposal(proposal *Proposal, originPeer uint64) error

	// OnValidation handles an incoming validation. Same originPeer
	// semantics as OnProposal — mirrors rippled's
	// PeerImp::onMessage(TMValidation) which feeds updateSlotAndSquelch
	// and the gossip-forward path with the originating peer excluded.
	OnValidation(validation *Validation, originPeer uint64) error

	// OnTxSet handles receiving a transaction set we requested.
	OnTxSet(id TxSetID, txs [][]byte) error

	// OnLedger handles receiving a ledger we were missing.
	OnLedger(id LedgerID, ledger []byte) error

	// State returns the current consensus state.
	State() *RoundState

	// Mode returns the current operating mode.
	Mode() Mode

	// Phase returns the current consensus phase.
	Phase() Phase

	// IsProposing returns true if we're actively proposing.
	IsProposing() bool

	// Timing returns the consensus timing parameters.
	Timing() Timing

	// GetLastCloseInfo returns the proposer count and convergence time from the last consensus round.
	GetLastCloseInfo() (proposers int, convergeTime time.Duration)
}

// Adaptor provides the interface between the consensus engine and
// the rest of the node (network, ledger, transaction queue).
// This follows rippled's adaptor pattern for clean separation.
type Adaptor interface {
	// Network operations

	// BroadcastProposal sends OUR OWN proposal to all peers. Not
	// subject to per-peer squelch filtering — we always deliver our
	// self-originated traffic. Mirrors rippled's OverlayImpl which
	// skips the squelch filter for self-originated broadcasts.
	BroadcastProposal(proposal *Proposal) error

	// BroadcastValidation sends OUR OWN validation to all peers. Same
	// no-filter semantics as BroadcastProposal.
	BroadcastValidation(validation *Validation) error

	// RelayProposal forwards a peer's proposal to other peers, honoring
	// the per-peer squelch filter and excluding the originating peer
	// (exceptPeer). Pass 0 for exceptPeer to send to all peers (e.g.
	// for tests that synthesize a relay without an origin).
	RelayProposal(proposal *Proposal, exceptPeer uint64) error

	// RelayValidation forwards a peer's validation to other peers,
	// honoring the per-peer squelch filter and excluding the
	// originating peer (exceptPeer). Same semantics as RelayProposal.
	// Mirrors rippled's gossip-forward path for TMValidation in
	// OverlayImpl::relay.
	RelayValidation(validation *Validation, exceptPeer uint64) error

	// UpdateRelaySlot feeds the reduce-relay state machine with an
	// inbound validator message from peerID. Mirrors rippled's
	// PeerImp::onMessage(TMProposeSet/TMValidation) calling
	// updateSlotAndSquelch — this is what drives the reduce-relay
	// selection logic to emit mtSQUELCH once peer activity crosses
	// the configured thresholds. Router calls this on every trusted
	// inbound proposal/validation.
	UpdateRelaySlot(validatorKey []byte, peerID uint64)

	// RequestTxSet requests a transaction set from peers.
	RequestTxSet(id TxSetID) error

	// RequestLedger requests a ledger from peers.
	RequestLedger(id LedgerID) error

	// Ledger operations

	// GetLedger returns the ledger with the given ID.
	GetLedger(id LedgerID) (Ledger, error)

	// GetLastClosedLedger returns the most recently closed ledger.
	GetLastClosedLedger() (Ledger, error)

	// GetValidatedLedgerHash returns the hash of the most recent ledger
	// this node considers FULLY VALIDATED (trusted-validation quorum
	// reached). Zero LedgerID when no ledger has crossed quorum yet —
	// callers must treat the zero value as "not available" and skip
	// emission (e.g., sfValidatedHash in STValidation). Separate from
	// GetLastClosedLedger which returns the consensus-closed view, not
	// the network-agreement view.
	GetValidatedLedgerHash() LedgerID

	// BuildLedger constructs a new ledger from a transaction set.
	BuildLedger(parent Ledger, txSet TxSet, closeTime time.Time) (Ledger, error)

	// ValidateLedger checks if a ledger is valid.
	ValidateLedger(ledger Ledger) error

	// StoreLedger persists a ledger.
	StoreLedger(ledger Ledger) error

	// Transaction operations

	// GetPendingTxs returns transactions waiting to be included.
	GetPendingTxs() [][]byte

	// GetTxSet returns a transaction set by ID.
	GetTxSet(id TxSetID) (TxSet, error)

	// BuildTxSet creates a transaction set from given transactions.
	BuildTxSet(txs [][]byte) (TxSet, error)

	// HasTx checks if we have a transaction.
	HasTx(id TxID) bool

	// GetTx returns a transaction by ID.
	GetTx(id TxID) ([]byte, error)

	// Validator operations

	// IsValidator returns true if this node is configured as a validator.
	IsValidator() bool

	// GetValidatorKey returns the node's validator public key (if validator).
	GetValidatorKey() (NodeID, error)

	// SignProposal signs a proposal with the validator key.
	SignProposal(proposal *Proposal) error

	// SignValidation signs a validation with the validator key.
	SignValidation(validation *Validation) error

	// VerifyProposal verifies a proposal's signature.
	VerifyProposal(proposal *Proposal) error

	// VerifyValidation verifies a validation's signature.
	VerifyValidation(validation *Validation) error

	// Trust operations

	// IsTrusted returns true if the node is in our UNL.
	IsTrusted(node NodeID) bool

	// GetTrustedValidators returns the current UNL.
	GetTrustedValidators() []NodeID

	// GetQuorum returns the number of validators needed for consensus.
	GetQuorum() int

	// PeerReportedLedgers returns the last-closed ledger hashes that
	// overlay peers have advertised via statusChange messages. Used
	// by getNetworkLedger as a fallback signal when peer proposals
	// haven't yet reached us for the current round — a peer that
	// just advanced its LCL but hasn't gossipped its proposal to us
	// still shows up as a vote for where the network is.
	//
	// Returns an empty slice when no peer statuses have been seen.
	// Peer-status votes are subject to the same quorum-validated
	// gate as proposal votes in checkLedger: they influence the
	// vote count, but switching to a peer's preferred LCL still
	// requires that LCL to have trusted-validation quorum.
	PeerReportedLedgers() []LedgerID

	// Time operations

	// Now returns the current network-adjusted time.
	Now() time.Time

	// CloseTimeResolution returns the close time granularity.
	CloseTimeResolution() time.Duration

	// AdjustCloseTime adjusts the clock offset toward the network average.
	AdjustCloseTime(rawCloseTimes CloseTimes)

	// Status operations

	// GetOperatingMode returns the node's overall operating mode.
	GetOperatingMode() OperatingMode

	// SetOperatingMode updates the node's operating mode.
	SetOperatingMode(mode OperatingMode)

	// OnConsensusReached is called when a round completes successfully.
	// Fires at local-accept time (consensus round resolution). This is
	// when the closed ledger is stored and pending-tx bookkeeping runs.
	// It does NOT mean the network has agreed — see OnLedgerFullyValidated.
	OnConsensusReached(ledger Ledger, validations []*Validation)

	// OnLedgerFullyValidated is called once per ledger the first time
	// trusted validations for that ledger cross the quorum threshold.
	// This is the network-agreement gate: server_info.validated_ledger
	// advances here, not at OnConsensusReached. Mirrors rippled's
	// LedgerMaster::checkAccept() which only calls setValidLedger()
	// after verifying trusted-validation count >= quorum.
	//
	// Safe to call even when the ledger is not yet in local history —
	// implementations should no-op or defer rather than fail.
	OnLedgerFullyValidated(ledgerID LedgerID, seq uint32)

	// OnModeChange is called when consensus mode changes.
	OnModeChange(oldMode, newMode Mode)

	// OnPhaseChange is called when consensus phase changes.
	OnPhaseChange(oldPhase, newPhase Phase)
}

// Ledger represents a ledger in the consensus process.
type Ledger interface {
	// ID returns the ledger hash.
	ID() LedgerID

	// Seq returns the ledger sequence number.
	Seq() uint32

	// ParentID returns the parent ledger hash.
	ParentID() LedgerID

	// CloseTime returns when the ledger was closed.
	CloseTime() time.Time

	// TxSetID returns the hash of the transaction set.
	TxSetID() TxSetID

	// Bytes returns the serialized ledger.
	Bytes() []byte
}

// TxSet represents a set of transactions for a ledger.
type TxSet interface {
	// ID returns the transaction set hash.
	ID() TxSetID

	// Txs returns the transactions in the set.
	Txs() [][]byte

	// Contains checks if a transaction is in the set.
	Contains(id TxID) bool

	// Add adds a transaction to the set.
	Add(tx []byte) error

	// Remove removes a transaction from the set.
	Remove(id TxID) error

	// Size returns the number of transactions.
	Size() int

	// Bytes returns the serialized transaction set.
	Bytes() []byte
}

// OperatingMode represents the node's overall operating state.
type OperatingMode int

const (
	// OpModeDisconnected means no peer connections.
	OpModeDisconnected OperatingMode = iota

	// OpModeConnected means connected to peers but not synced.
	OpModeConnected

	// OpModeSyncing means actively syncing with the network.
	OpModeSyncing

	// OpModeTracking means following the network passively.
	OpModeTracking

	// OpModeFull means fully synchronized and participating.
	OpModeFull
)

// String returns the string representation.
func (m OperatingMode) String() string {
	switch m {
	case OpModeDisconnected:
		return "disconnected"
	case OpModeConnected:
		return "connected"
	case OpModeSyncing:
		return "syncing"
	case OpModeTracking:
		return "tracking"
	case OpModeFull:
		return "full"
	default:
		return "unknown"
	}
}

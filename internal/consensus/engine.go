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
	// Proposal.SuppressionHash is used by the overlay to record each
	// recipient in its reverse index for a later duplicate-arrival
	// lookup (B3) — callers must ensure the field is populated for
	// peer-forwarded proposals.
	RelayProposal(proposal *Proposal, exceptPeer uint64) error

	// RelayValidation forwards a peer's validation to other peers,
	// honoring the per-peer squelch filter and excluding the
	// originating peer (exceptPeer). Same semantics as RelayProposal;
	// uses Validation.SuppressionHash for the reverse-index record.
	RelayValidation(validation *Validation, exceptPeer uint64) error

	// UpdateRelaySlot feeds the reduce-relay state machine with an
	// inbound validator message from originPeer AND every peer in
	// seenPeers (known-havers from the overlay's reverse index).
	// Mirrors rippled's PeerImp::onMessage(TMProposeSet/TMValidation)
	// calling updateSlotAndSquelch with the full haveMessage set
	// (PeerImp.cpp:3013-3017, 3049-3054) — feeding multi-path
	// delivery evidence per duplicate is what lets selection converge
	// at the same rate rippled does. Router calls this on every
	// trusted inbound proposal/validation duplicate.
	UpdateRelaySlot(validatorKey []byte, originPeer uint64, seenPeers []uint64)

	// PeersThatHave returns peer IDs known to have the message with
	// suppressionHash. Populated at outbound relay and queried on
	// inbound duplicates so UpdateRelaySlot can be fed the whole
	// known-haver set (B3). Returns nil when unknown or aged out.
	PeersThatHave(suppressionHash [32]byte) []uint64

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
	// closeTimeCorrect is true when consensus agreed on the close time;
	// when false, the resulting header carries sLCF_NoConsensusTime so
	// the hash matches what rippled produces in the same case.
	BuildLedger(parent Ledger, txSet TxSet, closeTime time.Time, closeTimeCorrect bool) (Ledger, error)

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

	// GetNegativeUNL returns the set of validator NodeIDs currently on
	// the negative-UNL — the XRPL mechanism for temporarily disabling
	// unreliable validators without removing them from the UNL. Rippled
	// keeps this state in the ltNEGATIVE_UNL SLE on every ledger; the
	// adaptor reads it from the currently-validated ledger.
	//
	// Validators on the negative-UNL are still TRUSTED for message
	// acceptance but are EXCLUDED from quorum counts in
	// ValidationTracker.checkFullValidation. Returning the set here
	// lets the engine refresh the tracker on every acceptLedger so a
	// validator added to (or removed from) the negUNL across a ledger
	// boundary is correctly excluded (or re-included).
	//
	// Returns nil or empty when no negUNL is in effect — the tracker
	// treats nil as "all trusted validators contribute to quorum".
	GetNegativeUNL() []NodeID

	// IsFeatureEnabled reports whether the named amendment is enabled
	// on the rules of the currently-validated (or otherwise most
	// authoritative) ledger. Used by the engine to gate optional
	// STValidation fields that rippled only emits under specific
	// amendments — e.g., sfValidatedHash requires featureHardenedValidations
	// (RCLConsensus.cpp:853). Adaptors that can't read rules (no ledger
	// yet, adaptor-only test harness) SHOULD return true to preserve
	// the mainnet-default behavior of emitting the optional fields.
	IsFeatureEnabled(name string) bool

	// GetCookie returns the validator's per-boot sfCookie value —
	// generated once at adaptor construction from a CSPRNG. Emitted on
	// every outgoing validation so peers can detect a validator
	// restart. Mirrors rippled RCLConsensus.cpp:813-818.
	GetCookie() uint64

	// GetServerVersion returns the sfServerVersion value the validator
	// advertises — an implementation + version identifier. Different
	// implementations (rippled vs goXRPL) use different high-byte tags
	// so network-wide version accounting can distinguish them. Zero
	// means "not included" and the serializer will skip the field.
	GetServerVersion() uint64

	// GetLoadFee returns the local load_fee the validator advertises
	// on every outbound validation (sfLoadFee). Rippled emits this
	// under HardenedValidations from the local LoadFeeTrack —
	// RCLConsensus.cpp:851. Adaptors without a load-feedback loop
	// should return 0; the serializer treats 0 as omit.
	GetLoadFee() uint32

	// GetFeeVote returns this validator's fee-vote stance for emission
	// on every validation. postXRPFees reflects the parent ledger's
	// rules: when true the engine populates the AMOUNT triple
	// (sfBaseFeeDrops/sfReserveBaseDrops/sfReserveIncrementDrops);
	// when false it populates the legacy UINT triple
	// (sfBaseFee/sfReserveBase/sfReserveIncrement). Matches rippled's
	// FeeVoteImpl.cpp:120-192 mutual-exclusive gate. Zero values from
	// the adaptor mean "no vote" and the serializer omits the fields.
	GetFeeVote() (baseFee, reserveBase, reserveIncrement uint64, postXRPFees bool)

	// GetAmendmentVote returns the list of amendment IDs this
	// validator wishes to vote FOR on the next flag ledger. The
	// engine calls this on flag-ledger validations only (see
	// isVotingLedger). Returns nil on non-validators or when no
	// amendments require a vote. Matches rippled's
	// AmendmentTable::doValidation (RCLConsensus.cpp:888-893).
	//
	// Implementations should filter amendments already enabled on
	// the current ledger so we don't re-vote for active ones.
	GetAmendmentVote() [][32]byte

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

	// TxIDs returns the hashes of every transaction in the set, in
	// the same order as Txs() so callers can zip the two slices
	// together. Used by consensus dispute tracking to diff two tx
	// sets without re-deriving tx IDs from blobs.
	TxIDs() []TxID

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

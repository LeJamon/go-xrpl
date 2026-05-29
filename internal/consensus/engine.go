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

	// GetJSON returns the current consensus-round state as a JSON-ready
	// map, mirroring rippled's RCLConsensus::getJson. Backs the
	// consensus_info RPC (full=true requests the detailed view).
	GetJSON(full bool) map[string]any

	// Subscribe registers a sink for the engine's typed event bus
	// (RoundStarted / PhaseChanged / ValidationReceived / etc.). The
	// engine fires events on its own goroutine; OnEvent must not block.
	// Mirrors rippled's NetworkOPs::subxxx feed registration without
	// the per-stream filtering — the subscriber is expected to fan
	// events out by type itself.
	Subscribe(sub EventSubscriber)
}

// ValidationHistorian exposes the validation subsystem to an adaptor:
// per-ledger trusted-validation lookups (NegativeUNL score tables,
// remote-fee median) and trie-based preferred-LCL selection (the
// operating-mode promotion gate). Implemented by rcl.ValidationTracker;
// an adaptor receives one via the WireableAdaptor extension.
//
//   - GetTrustedValidations mirrors rippled's
//     `validations.getTrustedForLedger(hash, seq)` at
//     NegativeUNLVote.cpp:208 — goXRPL's tracker keys validations by
//     LedgerID, so the (hash, seq) tuple collapses to a single lookup.
//   - GetPreferred / PreferredFromValidations expose the validation
//     ancestry trie's preferred-ledger pick (and its no-trie fallback),
//     the goXRPL realization of rippled's
//     `validations.getPreferredLCL` (Validations.h:936).
type ValidationHistorian interface {
	GetTrustedValidations(ledgerID LedgerID) []*Validation
	GetPreferred(largestIssued uint32) (LedgerID, uint32, bool)
	PreferredFromValidations(minSeq uint32) (LedgerID, uint32, bool)
}

// WireableAdaptor is an optional extension that engine wires up after
// constructing its ValidationTracker. Adaptors that implement it can
// emit NegativeUNL pseudo-txs; those that don't (e.g. test mocks)
// continue to work and simply skip NegativeUNL voting.
type WireableAdaptor interface {
	SetValidationHistorian(h ValidationHistorian)
}

// The Adaptor interface is composed of smaller per-subsystem interfaces.
// New code should depend on the narrowest interface that satisfies its
// needs (e.g. TimeSource instead of Adaptor for code that only reads
// the clock).

// NetworkBroadcaster handles self-originated outbound traffic and the
// per-peer squelch / reverse-index bookkeeping that goes with it.
type NetworkBroadcaster interface {
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
	UpdateRelaySlot(validatorKey []byte, originPeer uint64, seenPeers []uint64)

	// PeersThatHave returns peer IDs known to have the message with
	// suppressionHash. Returns nil when unknown or aged out.
	PeersThatHave(suppressionHash [32]byte) []uint64

	// RequestTxSet requests a transaction set from peers.
	RequestTxSet(id TxSetID) error

	// RequestLedger requests a ledger from peers.
	RequestLedger(id LedgerID) error
}

// LedgerProvider exposes the node's persistent ledger view to the
// engine: lookup, validated-state, and the build/store/validate
// pipeline.
type LedgerProvider interface {
	// GetLedger returns the ledger with the given ID.
	GetLedger(id LedgerID) (Ledger, error)

	// GetLastClosedLedger returns the most recently closed ledger.
	GetLastClosedLedger() (Ledger, error)

	// GetValidatedLedgerHash returns the hash of the most recent ledger
	// this node considers FULLY VALIDATED (trusted-validation quorum
	// reached). Zero LedgerID when no ledger has crossed quorum yet.
	GetValidatedLedgerHash() LedgerID

	// BuildLedger constructs a new ledger from a transaction set.
	BuildLedger(parent Ledger, txSet TxSet, closeTime time.Time, closeTimeCorrect bool) (Ledger, error)

	// ValidateLedger checks if a ledger is valid.
	ValidateLedger(ledger Ledger) error

	// StoreLedger persists a ledger.
	StoreLedger(ledger Ledger) error
}

// TxPool exposes the open-ledger transaction view to the engine.
type TxPool interface {
	// GetPendingTxs returns the tx blobs currently in the open pool.
	GetPendingTxs() [][]byte

	// GetProposableTxs returns the tx set the node will propose this
	// round. Mirrors rippled's `OpenLedger.current()->txs` read at
	// proposal time.
	GetProposableTxs(parent Ledger) [][]byte

	// GenerateFlagLedgerPseudoTxs returns the fee-vote and
	// amendment-vote pseudo-transaction blobs to inject into the
	// proposal initial set when prevLedger is a flag ledger.
	GenerateFlagLedgerPseudoTxs(prevLedger Ledger, parentValidations []*Validation) [][]byte

	// GenerateNegativeUNLPseudoTx returns the NegativeUNL pseudo-tx
	// blobs to inject when prevLedger is a voting ledger AND the
	// featureNegativeUNL amendment is enabled.
	GenerateNegativeUNLPseudoTx(prevLedger Ledger) [][]byte

	// OnUNLChange registers validators newly added to the operator-trusted
	// set with the NegativeUNL voter's grace-period table. Called by the
	// engine at the head of every consensus round; `upcomingSeq` is
	// `prevLedger.Seq() + 1` (the upcoming round's ledger sequence,
	// derived directly from the parent ledger to keep the grace-period
	// bookkeeping consistent with the voting-path purge — see
	// GenerateNegativeUNLPseudoTx which keys off `prevSeq + 1`). `nowTrusted`
	// is the delta — validators added since the previous round, NOT the
	// full UNL. Mirrors rippled's preStartRound at RCLConsensus.cpp:1041-1043.
	// The engine owns the feature-gate (featureNegativeUNL enabled on
	// prevLedger) and the delta computation.
	OnUNLChange(upcomingSeq uint32, nowTrusted []NodeID)

	// GetTxSet returns a transaction set by ID.
	GetTxSet(id TxSetID) (TxSet, error)

	// BuildTxSet creates a transaction set from given transactions.
	BuildTxSet(txs [][]byte) (TxSet, error)

	// HasTx checks if we have a transaction.
	HasTx(id TxID) bool

	// GetTx returns a transaction by ID.
	GetTx(id TxID) ([]byte, error)
}

// ValidatorIdentity carries the local node's validator credentials and
// the sign/verify pair for proposals and validations.
type ValidatorIdentity interface {
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
}

// TrustOracle exposes the UNL / negative-UNL / quorum state and the
// amendment / standalone gates used during proposal and validation.
type TrustOracle interface {
	// IsTrusted returns true if the node is in our UNL.
	IsTrusted(node NodeID) bool

	// GetTrustedValidators returns the current UNL.
	GetTrustedValidators() []NodeID

	// GetQuorum returns the number of validators needed for consensus.
	GetQuorum() int

	// GetNegativeUNL returns the set of validator NodeIDs currently on
	// the negative-UNL. Validators on negUNL are still trusted for
	// message acceptance but excluded from quorum counts.
	GetNegativeUNL() []NodeID

	// IsFeatureEnabled reports whether the named amendment is enabled
	// on the rules of the currently-validated ledger. Used to gate
	// optional STValidation fields. Adaptors that can't read rules
	// SHOULD return true to preserve mainnet-default behavior.
	IsFeatureEnabled(name string) bool

	// IsFeatureEnabledOnLedger reports whether the named amendment is
	// enabled in the rules of the given ledger. The strict gate variant
	// used during ledger building.
	IsFeatureEnabledOnLedger(ledger Ledger, name string) bool

	// IsStandalone reports whether the node is configured for
	// standalone (single-node) operation.
	IsStandalone() bool

	// GetCookie returns the validator's per-boot sfCookie value.
	GetCookie() uint64

	// GetServerVersion returns the sfServerVersion value the validator
	// advertises. Zero means "not included" and the serializer skips
	// the field.
	GetServerVersion() uint64

	// GetLoadFee returns the local load_fee the validator advertises
	// on every outbound validation (sfLoadFee). Zero is treated as omit.
	GetLoadFee() uint32

	// GetFeeVote returns this validator's fee-vote stance for emission
	// on every validation.
	GetFeeVote() (baseFee, reserveBase, reserveIncrement uint64, postXRPFees bool)

	// GetAmendmentVote returns the list of amendment IDs this
	// validator wishes to vote FOR on the next flag ledger.
	GetAmendmentVote() [][32]byte

	// PeerReportedLedgers returns the last-closed ledger hashes that
	// overlay peers have advertised via statusChange messages.
	PeerReportedLedgers() []LedgerID
}

// TimeSource exposes the network-adjusted clock and close-time machinery.
type TimeSource interface {
	// Now returns the current network-adjusted time.
	Now() time.Time

	// CloseTimeResolution returns the close time granularity.
	CloseTimeResolution() time.Duration

	// AdjustCloseTime adjusts the clock offset toward the network average.
	AdjustCloseTime(rawCloseTimes CloseTimes)
}

// StatusEvents carries the engine's coarse-grained state callbacks —
// operating-mode, consensus-reached, full-validation, and the per-round
// mode/phase transitions used for instrumentation.
type StatusEvents interface {
	// GetOperatingMode returns the node's overall operating mode.
	GetOperatingMode() OperatingMode

	// SetOperatingMode updates the node's overall operating mode.
	SetOperatingMode(mode OperatingMode)

	// OnConsensusReached is called when a round completes successfully.
	// Fires at local-accept time. It does NOT mean the network has
	// agreed — see OnLedgerFullyValidated. roundTime is the wall-clock
	// duration the just-finished consensus round took, used to drive
	// the TxQ slow-consensus timeLeap flag (rippled
	// RCLConsensus.cpp:803-805).
	OnConsensusReached(ledger Ledger, validations []*Validation, roundTime time.Duration)

	// OnLedgerFullyValidated is called once per ledger the first time
	// trusted validations for that ledger cross the quorum threshold.
	OnLedgerFullyValidated(ledgerID LedgerID, seq uint32)

	// OnModeChange is called when consensus mode changes.
	OnModeChange(oldMode, newMode Mode)

	// OnPhaseChange is called when consensus phase changes.
	OnPhaseChange(oldPhase, newPhase Phase)
}

// Adaptor provides the interface between the consensus engine and the
// rest of the node. Follows rippled's adaptor pattern for clean
// separation. New code should prefer one of the narrower seams above.
type Adaptor interface {
	NetworkBroadcaster
	LedgerProvider
	TxPool
	ValidatorIdentity
	TrustOracle
	TimeSource
	StatusEvents
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

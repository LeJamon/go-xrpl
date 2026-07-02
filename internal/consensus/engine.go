package consensus

import (
	"context"
	"time"
)

// Engine is the consensus algorithm interface that node implementations plug into.
type Engine interface {
	Start(ctx context.Context) error

	Stop() error

	// StartRound begins a round; proposing enables this node's proposal.
	StartRound(round RoundID, proposing bool) error

	// OnProposal handles an incoming proposal. originPeer is the overlay peer
	// ID that delivered it (0 for self-originated); passed to the relay path
	// so gossip forwards exclude the originator.
	OnProposal(proposal *Proposal, originPeer uint64) error

	// OnValidation handles an incoming validation. Same originPeer semantics
	// as OnProposal.
	OnValidation(validation *Validation, originPeer uint64) error

	OnTxSet(id TxSetID, txs [][]byte) error

	OnLedger(id LedgerID, ledger []byte) error

	// OnLedgerAcquireFailed reports a clean inbound-acquire failure after the
	// retry budget. Lets a node pinned in wrongLedger un-pin and drop to
	// degraded resync rather than starving the stall watchdog into os.Exit.
	OnLedgerAcquireFailed(id LedgerID)

	State() *RoundState

	Mode() Mode

	Phase() Phase

	IsProposing() bool

	Timing() Timing

	GetLastCloseInfo() (proposers int, convergeTime time.Duration)

	// GetJSON returns the consensus-round state as a JSON map backing the
	// consensus_info RPC; full requests the detailed view.
	GetJSON(full bool) map[string]any

	// Subscribe registers a sink for the engine's typed event bus. The engine
	// fires events on its own goroutine, so OnEvent must not block.
	Subscribe(sub EventSubscriber)
}

// ValidationHistorian exposes the validation subsystem to an adaptor:
// per-ledger trusted-validation lookups and trie-based preferred-LCL
// selection. Implemented by rcl.ValidationTracker, wired via WireableAdaptor.
type ValidationHistorian interface {
	GetTrustedValidations(ledgerID LedgerID) []*Validation
	GetPreferred(largestIssued uint32) (LedgerID, uint32, bool)
	PreferredFromValidations(minSeq uint32) (LedgerID, uint32, bool)
}

// WireableAdaptor is an optional extension engine wires after constructing its
// ValidationTracker. Implementers emit NegativeUNL pseudo-txs; others (e.g.
// test mocks) simply skip NegativeUNL voting.
type WireableAdaptor interface {
	SetValidationHistorian(h ValidationHistorian)
}

// Adaptor is composed of the narrower per-subsystem interfaces below; depend
// on the narrowest one that satisfies your needs.

// NetworkBroadcaster handles self-originated outbound traffic and the per-peer
// squelch / reverse-index bookkeeping that goes with it.
type NetworkBroadcaster interface {
	// BroadcastProposal sends our own proposal to all peers, bypassing
	// per-peer squelch.
	BroadcastProposal(proposal *Proposal) error

	// BroadcastValidation sends our own validation to all peers (no squelch
	// filter).
	BroadcastValidation(validation *Validation) error

	// RelayProposal forwards a peer's proposal to others, honoring per-peer
	// squelch and excluding exceptPeer (0 = all). SuppressionHash must be set:
	// the overlay records each recipient in its reverse index for duplicate-
	// arrival lookups.
	RelayProposal(proposal *Proposal, exceptPeer uint64) error

	// RelayValidation forwards a peer's validation to others; same semantics
	// as RelayProposal, using Validation.SuppressionHash.
	RelayValidation(validation *Validation, exceptPeer uint64) error

	// UpdateRelaySlot feeds the reduce-relay state machine with an inbound
	// validator message from originPeer and every known-haver in seenPeers.
	UpdateRelaySlot(validatorKey []byte, originPeer uint64, seenPeers []uint64)

	// PeersThatHave returns peer IDs known to hold suppressionHash, or nil if
	// unknown or aged out.
	PeersThatHave(suppressionHash [32]byte) []uint64

	RequestTxSet(id TxSetID) error

	RequestLedger(id LedgerID) error
}

// LedgerProvider exposes the node's persistent ledger view: lookup, validated
// state, and the build/store/validate pipeline.
type LedgerProvider interface {
	GetLedger(id LedgerID) (Ledger, error)

	// GetLedgerBySeq returns the locally-held CLOSED ledger at seq from
	// persisted history (never the mutable open ledger), or an error if
	// absent. The catch-up walk uses it to advance prevLedger by the furthest
	// parent-hash-chained ledger in one step.
	GetLedgerBySeq(seq uint32) (Ledger, error)

	GetLastClosedLedger() (Ledger, error)

	// GetValidatedLedgerHash returns the hash of the most recent fully
	// validated ledger (trusted-validation quorum reached), or zero if none.
	GetValidatedLedgerHash() LedgerID

	BuildLedger(parent Ledger, txSet TxSet, closeTime time.Time, closeTimeCorrect bool) (Ledger, error)

	ValidateLedger(ledger Ledger) error

	StoreLedger(ledger Ledger) error
}

// TxPool exposes the open-ledger transaction view to the engine.
type TxPool interface {
	GetPendingTxs() [][]byte

	// GetProposableTxs returns the tx set the node will propose this round.
	GetProposableTxs(parent Ledger) [][]byte

	// GenerateFlagLedgerPseudoTxs returns the fee-vote and amendment-vote
	// pseudo-tx blobs to inject when prevLedger is a flag ledger.
	GenerateFlagLedgerPseudoTxs(prevLedger Ledger, parentValidations []*Validation) [][]byte

	// GenerateNegativeUNLPseudoTx returns the NegativeUNL pseudo-tx blobs to
	// inject when prevLedger is a voting ledger and featureNegativeUNL is enabled.
	GenerateNegativeUNLPseudoTx(prevLedger Ledger) [][]byte

	// OnUNLChange registers newly-trusted validators with the NegativeUNL
	// voter's grace-period table. upcomingSeq is prevLedger.Seq()+1; nowTrusted
	// is the delta of validators added since the previous round, not the full UNL.
	OnUNLChange(upcomingSeq uint32, nowTrusted []NodeID)

	GetTxSet(id TxSetID) (TxSet, error)

	BuildTxSet(txs [][]byte) (TxSet, error)

	HasTx(id TxID) bool

	GetTx(id TxID) ([]byte, error)
}

// ValidatorIdentity carries the local node's validator credentials and the
// sign/verify pair for proposals and validations.
type ValidatorIdentity interface {
	IsValidator() bool

	GetValidatorKey() (NodeID, error)

	SignProposal(proposal *Proposal) error

	SignValidation(validation *Validation) error

	VerifyProposal(proposal *Proposal) error

	VerifyValidation(validation *Validation) error
}

// FeeVoteResult is a validator's fee-vote stance emitted on every validation.
// PostXRPFees selects the AMOUNT triple over the legacy UINT triple; zero
// values mean "no vote" and are omitted.
type FeeVoteResult struct {
	BaseFee          uint64
	ReserveBase      uint64
	ReserveIncrement uint64
	PostXRPFees      bool
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

	// GetNegativeUNL returns validators on the negative-UNL: still trusted for
	// message acceptance but excluded from quorum counts.
	GetNegativeUNL() []NodeID

	// IsFeatureEnabled reports whether the named amendment is enabled on the
	// currently-validated ledger's rules, gating optional STValidation fields.
	// Adaptors that can't read rules should return true (mainnet default).
	IsFeatureEnabled(name string) bool

	// IsFeatureEnabledOnLedger reports whether the amendment is enabled in the
	// given ledger's rules; the strict variant used during ledger building.
	IsFeatureEnabledOnLedger(ledger Ledger, name string) bool

	// IsStandalone reports whether the node runs in standalone (single-node) mode.
	IsStandalone() bool

	// IsUNLBlocked reports the validator-list lock-down: a configured
	// publisher list expired or the trusted union went empty. Adaptors
	// without publisher lists return false.
	IsUNLBlocked() bool

	// GetCookie returns the validator's per-boot sfCookie value.
	GetCookie() uint64

	// GetServerVersion returns the advertised sfServerVersion; zero omits the
	// field.
	GetServerVersion() uint64

	// GetLoadFee returns the advertised sfLoadFee; zero omits the field.
	GetLoadFee() uint32

	GetFeeVote() FeeVoteResult

	// GetAmendmentVote returns the amendment IDs to vote for on the next flag ledger.
	GetAmendmentVote() [][32]byte

	// PeerReportedLedgers returns last-closed ledger hashes peers advertised
	// via statusChange messages.
	PeerReportedLedgers() []LedgerID
}

// TimeSource exposes the network-adjusted clock and close-time machinery.
type TimeSource interface {
	// Now returns the current network-adjusted time.
	Now() time.Time

	CloseTimeResolution() time.Duration

	// PrevCloseTimeResolution returns the last closed ledger's own stored
	// close-time resolution. The empty-ledger idle interval keys off this raw
	// value, not the next-ledger rounding basis CloseTimeResolution returns —
	// the two differ by one ladder rung at resolution boundaries.
	PrevCloseTimeResolution() time.Duration

	// AdjustCloseTime adjusts the clock offset toward the network average.
	AdjustCloseTime(rawCloseTimes CloseTimes)
}

// StatusEvents carries the engine's coarse-grained state callbacks:
// operating-mode, consensus-reached, full-validation, and the per-round
// mode/phase transitions used for instrumentation.
type StatusEvents interface {
	GetOperatingMode() OperatingMode

	SetOperatingMode(mode OperatingMode)

	// OnConsensusReached fires when a round completes locally — NOT network
	// agreement (see OnLedgerFullyValidated). roundTime is the round's
	// wall-clock duration, driving the TxQ slow-consensus timeLeap flag.
	OnConsensusReached(ledger Ledger, validations []*Validation, roundTime time.Duration)

	// OnLedgerFullyValidated fires once per ledger, when trusted validations
	// first cross quorum.
	OnLedgerFullyValidated(ledgerID LedgerID, seq uint32)

	OnModeChange(oldMode, newMode Mode)

	OnPhaseChange(oldPhase, newPhase Phase)
}

// Adaptor is the full seam between the consensus engine and the node; new code
// should prefer one of the narrower interfaces above.
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
	ID() LedgerID

	Seq() uint32

	ParentID() LedgerID

	CloseTime() time.Time

	TxSetID() TxSetID

	Bytes() []byte
}

// TxSet represents a set of transactions for a ledger.
type TxSet interface {
	ID() TxSetID

	Txs() [][]byte

	// TxIDs returns the hash of every tx, in the same order as Txs() so the
	// two slices can be zipped.
	TxIDs() []TxID

	Contains(id TxID) bool

	Add(tx []byte) error

	Remove(id TxID) error

	Size() int

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

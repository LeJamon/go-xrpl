package consensus

import (
	"context"
	"time"
)

type engineLifecycle interface {
	Start(ctx context.Context) error

	Stop() error
}

type EngineTerminal interface {
	Done() <-chan error
}

type engineRoundDriver interface {
	// StartRound begins a round; proposing enables this node's proposal.
	StartRound(round RoundID, proposing bool) error
}

type engineInbound interface {
	// OnProposal handles an incoming proposal. originPeer is the overlay peer
	// ID that delivered it (0 for self-originated); passed to the relay path
	// so gossip forwards exclude the originator.
	OnProposal(proposal *Proposal, originPeer uint64) error

	// OnValidation handles an incoming validation. Same originPeer semantics
	// as OnProposal.
	OnValidation(validation *Validation, originPeer uint64) error

	OnTxSet(id TxSetID, txs [][]byte) error
}

type engineLedgerSwitch interface {
	// CanAcceptLedger applies the validated-ledger freshness and sequence checks
	// without changing the consensus working ledger.
	CanAcceptLedger(id LedgerID) (bool, error)

	// TrySwitchToLedger synchronously attempts to make a locally-held ledger
	// the consensus parent. The candidate must be the exact wrong-ledger
	// recovery target, the validated tip, or the current network preference.
	TrySwitchToLedger(id LedgerID) (LedgerSwitchResult, error)

	// OnLedgerAcquireFailed reports that an in-flight acquisition was invalidated
	// by a topology change, allowing wrong-ledger recovery to re-resolve its target.
	OnLedgerAcquireFailed(id LedgerID)
}

type engineObservability interface {
	Mode() Mode

	Phase() Phase

	IsProposing() bool

	GetLastCloseInfo() (proposers int, convergeTime time.Duration)

	// GetJSON returns the consensus-round state as a JSON map backing the
	// consensus_info RPC; full requests the detailed view.
	GetJSON(full bool) map[string]any
}

type engineEvents interface {
	// Subscribe registers a sink for the engine's typed event bus. The engine
	// fires events on its own goroutine, so OnEvent must not block.
	Subscribe(sub EventSubscriber)
}

type RouterEngine interface {
	engineInbound
	engineLedgerSwitch
}

type Engine interface {
	engineLifecycle
	engineRoundDriver
	RouterEngine
	engineObservability
	engineEvents
}

// VerifiedValidationProcessor is implemented by engines that separate
// signature verification from stateful validation processing. The router uses
// this seam to verify on bounded worker queues, then serializes processing on
// its own goroutine.
type VerifiedValidationProcessor interface {
	ProcessVerifiedValidation(validation *Validation, origin ValidationOrigin) (ValidationDisposition, error)
}

type ValidationOrigin struct {
	PeerID  uint64
	Cluster bool
}

// ValidationStatus describes how the validation tracker classified a verified
// validation. Untracked means the signer was neither trusted nor listed.
type ValidationStatus uint8

const (
	ValidationUntracked ValidationStatus = iota
	ValidationCurrent
	ValidationStale
	ValidationBadSeq
	ValidationMultiple
	ValidationConflicting
)

func (s ValidationStatus) String() string {
	switch s {
	case ValidationUntracked:
		return "untracked"
	case ValidationCurrent:
		return "current"
	case ValidationStale:
		return "stale"
	case ValidationBadSeq:
		return "badSeq"
	case ValidationMultiple:
		return "multiple"
	case ValidationConflicting:
		return "conflicting"
	default:
		return "unknown"
	}
}

type ValidationDisposition struct {
	Status  ValidationStatus
	Tracked bool
	Trusted bool
	Relay   bool
}

// AcquireEligible reports whether the validation can drive ledger catch-up.
// Only trusted validations accepted as current participate.
func (d ValidationDisposition) AcquireEligible() bool {
	return d.Tracked && d.Trusted && d.Status == ValidationCurrent
}

// LedgerSwitchResult describes the outcome of a synchronous ledger switch.
type LedgerSwitchResult uint8

const (
	LedgerSwitchIrrelevant LedgerSwitchResult = iota
	LedgerSwitchAccepted
	LedgerSwitchBusy
	LedgerSwitchRejected
)

// ValidationHistorian exposes the validation subsystem to an adaptor:
// per-ledger trusted-validation lookups and trie-based preferred-LCL
// selection. Implemented by rcl.ValidationTracker, wired via WireableAdaptor.
type ValidationHistorian interface {
	// GetTrustedFullValidations returns trusted full validations for the exact
	// ledger hash and sequence. The sequence check is part of the lookup so
	// protocol voting cannot accidentally consume mixed-sequence evidence that
	// happens to share a ledger hash.
	GetTrustedFullValidations(ledgerID LedgerID, ledgerSeq uint32) []*Validation

	GetPreferred(largestIssued uint32) (LedgerID, uint32, bool)
	PreferredFromValidations(minSeq uint32) (LedgerID, uint32, bool)

	// SetSeqToKeep pins the validation range [low, high) against expiry so
	// the negative-UNL vote's flag-ledger scan window survives a
	// fast-advancing retention floor.
	SetSeqToKeep(low, high uint32)

	// GetJSONTrie returns a JSON-serializable snapshot of the ancestry trie's
	// support state for debugging preferred-ledger divergence, or nil when the
	// trie is disabled.
	GetJSONTrie() map[string]any
}

// ValidationQuorumRechecker is an optional historian capability used when a
// ledger arrives after its validation quorum notification. It snapshots the
// filtered validations and quorum together and atomically rearms a rejected
// ledger so a later validation can notify again.
type ValidationQuorumRechecker interface {
	RecheckFullyValidated(ledgerID LedgerID, seq uint32) ([]*Validation, int, bool)
}

// WireableAdaptor is an optional extension engine wires after constructing its
// ValidationTracker. Implementers emit NegativeUNL pseudo-txs; others (e.g.
// test mocks) simply skip NegativeUNL voting.
type WireableAdaptor interface {
	SetValidationHistorian(h ValidationHistorian)
}

// ListedOracle is an optional trust-oracle extension reporting validator-list
// membership: a listed validator is published by at least one configured list
// publisher but not (necessarily) in the UNL. The engine stores validations
// from listed signers so a later trust change promotes the ones already seen
// instead of waiting for a fresh validation. Absent → nothing is listed.
type ListedOracle interface {
	IsListed(node NodeID) bool
}

// ValidationRelayPolicy is an optional Adaptor extension exposing the
// operator's [relay_validations] stance. Absent → only trusted validations
// are relayed.
type ValidationRelayPolicy interface {
	// RelayUntrustedValidations reports whether verified, current validations
	// signed by validators outside the UNL are forwarded to peers, so nodes
	// with a different UNL that do trust the signer still receive them.
	RelayUntrustedValidations() bool
}

// TrustChangeNotifier is an optional Adaptor extension: registration publishes
// the current snapshot, then implementers invoke the callback after every
// runtime UNL mutation so the engine can promote stored validations from
// newly-trusted validators immediately rather than at the next accepted ledger.
type TrustChangeNotifier interface {
	OnTrustChanged(fn func(trusted []NodeID, quorum int))
}

// TrustChangeSettledNotifier is an optional Adaptor extension. It registers a
// callback invoked after a trust snapshot callback has returned and the
// adaptor's transition gate has reopened. Consumers use it to recheck state
// that deliberately stayed closed while the matching snapshot was installed.
type TrustChangeSettledNotifier interface {
	OnTrustSettled(fn func())
}

// LedgerAcceptDeferrer is an optional Adaptor extension for environments that
// must schedule ledger application on their own serialized driver. Returning
// true transfers completion to the adaptor, which must invoke complete exactly
// once and never inline, including when the scheduled work is canceled or the
// environment shuts down. Returning false leaves acceptance synchronous and
// must not retain complete.
type LedgerAcceptDeferrer interface {
	DeferLedgerAccept(complete func()) bool
}

// LedgerAcceptDeferrerLifecycle is implemented by deferrers that own a
// background acceptance worker. The engine calls StopLedgerAccept before
// shutting down its event bus or any resources used by a completion callback.
type LedgerAcceptDeferrerLifecycle interface {
	LedgerAcceptDeferrer
	StopLedgerAccept() error
}

// Adaptor is composed of the narrower per-subsystem interfaces below; depend
// on the narrowest one that satisfies your needs.

// networkBroadcaster handles self-originated outbound traffic and the per-peer
// squelch / reverse-index bookkeeping that goes with it.
type networkBroadcaster interface {
	// BroadcastProposal sends our own proposal to all peers, bypassing
	// per-peer squelch.
	BroadcastProposal(proposal *Proposal) error

	// BroadcastValidation sends our own validation to all peers (no squelch
	// filter).
	BroadcastValidation(validation *Validation) error

	// RelayProposal forwards a peer's proposal to others, honoring per-peer
	// squelch and excluding exceptPeer (0 = all). SuppressionHash must be set:
	// the overlay uses it to exclude known inbound sources and record relay time.
	RelayProposal(proposal *Proposal, exceptPeer uint64) error

	// RelayValidation forwards a peer's validation to others; same semantics
	// as RelayProposal, using Validation.SuppressionHash.
	RelayValidation(validation *Validation, exceptPeer uint64) error

	// UpdateRelaySlot feeds the reduce-relay state machine with an inbound
	// validator message from originPeer and every known-haver in seenPeers.
	UpdateRelaySlot(validatorKey []byte, originPeer uint64, seenPeers []uint64)

	RequestTxSet(id TxSetID) error

	// RequestLedger may be called repeatedly while a ledger remains unavailable;
	// implementations must suppress duplicate work within their retry window.
	RequestLedger(id LedgerID) error
}

// ledgerProvider exposes the node's persistent ledger view: lookup, validated
// state, and the build/store/validate pipeline.
type ledgerProvider interface {
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

	// GetMaxDisallowedLedgerSeq returns the highest ledger sequence persisted
	// before this process started, or 0 when none. A restarted validator may
	// already have signed validations up to that tip, so it must never
	// propose or validate at or below this floor (anti-self-equivocation
	// across restarts). Immutable after startup.
	GetMaxDisallowedLedgerSeq() uint32

	// BuildLedger closes a ledger from the agreed tx set on parent.
	// disputedTxs are the raw blobs of the round's disputed txs we voted NO
	// on; they are replayed into the next open ledger ahead of the queue.
	BuildLedger(parent Ledger, txSet TxSet, closeTime time.Time, closeTimeCorrect bool, disputedTxs [][]byte) (Ledger, error)

	ValidateLedger(ledger Ledger) error

	StoreLedger(ledger Ledger) error
}

// txPool exposes the open-ledger transaction view to the engine.
type txPool interface {
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

	HasTx(id TxID) (bool, error)

	GetTx(id TxID) ([]byte, error)
}

// validatorIdentity carries the local node's validator credentials and the
// sign/verify pair for proposals and validations.
type validatorIdentity interface {
	IsValidator() bool

	// IsAmendmentBlocked reports whether an unsupported amendment has
	// activated. A blocked node can no longer build correct ledgers and
	// must not propose or validate.
	IsAmendmentBlocked() bool

	GetValidatorKey() (NodeID, error)

	SignProposal(proposal *Proposal) error

	SignValidation(validation *Validation) error

	VerifyProposal(proposal *Proposal) error

	VerifyValidation(validation *Validation) error
}

// FeeVoteResult is a validator's fee-vote stance emitted on flag-ledger
// validations. The Set fields distinguish explicit zero from omission.
type FeeVoteResult struct {
	BaseFee             uint64
	ReserveBase         uint64
	ReserveIncrement    uint64
	BaseFeeSet          bool
	ReserveBaseSet      bool
	ReserveIncrementSet bool
	PostXRPFees         bool
}

// HasBaseFee reports whether the base-fee vote is present.
func (f FeeVoteResult) HasBaseFee() bool {
	return f.BaseFeeSet || f.BaseFee != 0
}

// HasReserveBase reports whether the reserve-base vote is present.
func (f FeeVoteResult) HasReserveBase() bool {
	return f.ReserveBaseSet || f.ReserveBase != 0
}

// HasReserveIncrement reports whether the reserve-increment vote is present.
func (f FeeVoteResult) HasReserveIncrement() bool {
	return f.ReserveIncrementSet || f.ReserveIncrement != 0
}

// trustOracle exposes the UNL / negative-UNL / quorum state and the
// amendment / standalone gates used during proposal and validation.
type trustOracle interface {
	// IsTrusted returns true if the node is in our UNL.
	IsTrusted(node NodeID) bool

	// GetTrustedValidators returns the current UNL.
	GetTrustedValidators() []NodeID

	// GetQuorum returns the number of validators needed for consensus.
	GetQuorum() int

	// GetTrustedValidatorsAndQuorum returns one consistent trust snapshot.
	GetTrustedValidatorsAndQuorum() ([]NodeID, int)

	// IsQuorumUnavailable reports whether publisher availability or an
	// in-flight trust transition makes finality unsafe.
	IsQuorumUnavailable() bool

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

	// RefreshUNLState re-evaluates the validator-list trust view against the
	// live clock so IsUNLBlocked reflects an expired list this round. Called
	// once per round-start; no-op for adaptors without publisher lists.
	RefreshUNLState()

	// GetCookie returns the validator's per-boot sfCookie value.
	GetCookie() uint64

	// GetServerVersion returns the advertised sfServerVersion; zero omits the
	// field.
	GetServerVersion() uint64

	// GetLoadFee returns the advertised sfLoadFee; zero omits the field.
	GetLoadFee() uint32

	GetFeeVote(Ledger) FeeVoteResult

	// GetAmendmentVote returns the amendment IDs to vote for on the next flag ledger.
	GetAmendmentVote() [][32]byte

	// PeerReportedLedgers returns last-closed ledger hashes peers advertised
	// via statusChange messages.
	PeerReportedLedgers() []LedgerID
}

// timeSource exposes the network-adjusted clock and close-time machinery.
type timeSource interface {
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

// statusEvents carries the engine's coarse-grained state callbacks:
// operating-mode, consensus-reached, full-validation, and the per-round
// mode/phase transitions used for instrumentation.
type statusEvents interface {
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

	// OnLedgerSwitched fires when the engine abandons its previous LCL and
	// adopts ledger (wrong-ledger recovery), so peers can be told the jump
	// via a SWITCHED_LEDGER status change.
	OnLedgerSwitched(ledger Ledger) error
}

// Adaptor is the full seam between the consensus engine and the node; new code
// should prefer one of the narrower interfaces above.
type Adaptor interface {
	networkBroadcaster
	ledgerProvider
	txPool
	validatorIdentity
	trustOracle
	timeSource
	statusEvents
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

	Size() int
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

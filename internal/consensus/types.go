// Package consensus defines the interface and types for XRPL consensus algorithms.
// It provides a pluggable architecture allowing different consensus implementations
// to be used interchangeably.
package consensus

import (
	"time"
)

// Mode represents the current consensus operating mode.
// A node can transition between modes during consensus rounds.
type Mode int

const (
	// ModeProposing means the node is actively participating in consensus,
	// proposing transactions and voting on proposals. Only validators in sync.
	ModeProposing Mode = iota

	// ModeObserving means the node is watching consensus but not proposing.
	// Non-validators always operate in this mode.
	ModeObserving

	// ModeWrongLedger means the node detected it's on a different ledger
	// than the network and is acquiring the correct one.
	ModeWrongLedger

	// ModeSwitchedLedger means the node recovered from wrong ledger
	// and is now observing until fully synced.
	ModeSwitchedLedger
)

// String returns the string representation of the mode.
func (m Mode) String() string {
	switch m {
	case ModeProposing:
		return "proposing"
	case ModeObserving:
		return "observing"
	case ModeWrongLedger:
		return "wrongLedger"
	case ModeSwitchedLedger:
		return "switchedLedger"
	default:
		return "unknown"
	}
}

// Phase represents the current phase within a consensus round.
type Phase int

const (
	// PhaseOpen is the initial phase where transactions are being accumulated.
	// The ledger is "open" for new transactions.
	PhaseOpen Phase = iota

	// PhaseEstablish is the negotiation phase where validators exchange
	// proposals and work toward agreement on the transaction set.
	PhaseEstablish

	// PhaseAccepted means consensus has been reached and the new ledger
	// is accepted. Waiting for the next round to begin.
	PhaseAccepted
)

// String returns the string representation of the phase.
func (p Phase) String() string {
	switch p {
	case PhaseOpen:
		return "open"
	case PhaseEstablish:
		return "establish"
	case PhaseAccepted:
		return "accepted"
	default:
		return "unknown"
	}
}

// Result represents the outcome of a consensus round.
type Result int

const (
	// ResultSuccess means consensus was reached normally.
	ResultSuccess Result = iota

	// ResultTimeout means the round timed out without consensus.
	ResultTimeout

	// ResultMovedOn means we moved on without full consensus
	// (e.g., supermajority agreed).
	ResultMovedOn

	// ResultFail means consensus failed for this round.
	ResultFail

	// ResultAbandoned means the round was hard-abandoned because its
	// duration exceeded the ledgerABANDON_CONSENSUS clamp (15s..120s).
	// Matches rippled's ConsensusState::Expired (ConsensusTypes.h:191),
	// which triggers leaveConsensus() to bow out before the accept step.
	ResultAbandoned
)

// String returns the string representation of the result.
func (r Result) String() string {
	switch r {
	case ResultSuccess:
		return "success"
	case ResultTimeout:
		return "timeout"
	case ResultMovedOn:
		return "movedOn"
	case ResultFail:
		return "fail"
	case ResultAbandoned:
		return "abandoned"
	default:
		return "unknown"
	}
}

// RoundID uniquely identifies a consensus round.
type RoundID struct {
	// Seq is the ledger sequence number being built.
	Seq uint32

	// ParentHash is the hash of the parent ledger.
	ParentHash [32]byte
}

// NodeID uniquely identifies a node in the network.
type NodeID [33]byte // Compressed public key

// TxID uniquely identifies a transaction.
type TxID [32]byte

// TxSetID uniquely identifies a transaction set.
type TxSetID [32]byte

// LedgerID uniquely identifies a ledger.
type LedgerID [32]byte

// Proposal represents a consensus proposal from a validator.
type Proposal struct {
	// Round identifies which consensus round this proposal is for.
	Round RoundID

	// NodeID is the proposing validator's public key.
	NodeID NodeID

	// Position is the sequence number of this proposal (0, 1, 2...).
	// Validators can update their position during establish phase.
	Position uint32

	// TxSet is the hash of the proposed transaction set.
	TxSet TxSetID

	// CloseTime is the proposed ledger close time.
	CloseTime time.Time

	// Signature is the validator's signature on this proposal.
	Signature []byte

	// PreviousLedger is the hash of the ledger this builds on.
	PreviousLedger LedgerID

	// Timestamp is when this proposal was created.
	Timestamp time.Time

	// SuppressionHash is the router-level dedup key for this proposal,
	// computed via the canonical proposalUniqueId scheme
	// (RCLCxPeerPos.cpp:66-83). Mirrors rippled's
	// RCLCxPeerPos::suppressionID() carried on the peer-position
	// instance so later relay + slot-feeding code doesn't have to
	// recompute it. Populated by the consensus router on inbound
	// messages; zero on self-originated proposals (Broadcast skips the
	// reverse index anyway).
	SuppressionHash [32]byte
}

// Validation represents a validation message from a validator.
type Validation struct {
	// LedgerID is the hash of the validated ledger.
	LedgerID LedgerID

	// LedgerSeq is the sequence number of the validated ledger.
	LedgerSeq uint32

	// NodeID is the validating node's public key.
	NodeID NodeID

	// SignTime is when the validation was signed.
	SignTime time.Time

	// SeenTime is when we received this validation.
	SeenTime time.Time

	// CloseTime is sfCloseTime from the ledger header the validator
	// signed. Optional per rippled STValidation.cpp:63 (soeOPTIONAL)
	// and populated only when the parser sees it. Not used by the
	// engine today — surfaced for RPC consumers that need per-
	// validation close times. Zero time.Time means "not present".
	CloseTime time.Time

	// Signature is the validator's signature.
	Signature []byte

	// Full indicates if this is a full validation (vs partial).
	Full bool

	// Cookie is a unique identifier for this validation session.
	Cookie uint64

	// LoadFee is the validator's current load-based fee.
	LoadFee uint32

	// ConsensusHash is sfConsensusHash — the hash of the agreed-upon
	// transaction set that produced the validated ledger. Rippled
	// includes this in validations so peers can tie-break between
	// multiple ledgers at the same seq with different tx sets.
	// Zero-hash means "not included".
	ConsensusHash [32]byte

	// ServerVersion is sfServerVersion — the validator's build
	// version, encoded as rippled's 64-bit packed version number.
	// Rippled attaches this to the first validation per peer session.
	// Zero means "not included".
	ServerVersion uint64

	// ValidatedHash is sfValidatedHash — the hash of the most
	// recent ledger THIS validator considers fully validated at the
	// time of signing (rippled RCLConsensus.cpp:858-859). Emitted when
	// the featureHardenedValidations amendment is enabled on the
	// parent. Peers use this as an additional fork-detection signal.
	// Zero-hash means "not included".
	ValidatedHash [32]byte

	// Amendments is sfAmendments — the list of amendment IDs this
	// validator wishes to vote FOR on the current flag ledger. Only
	// populated on flag ledgers (seq % 256 == 0) when the validator
	// has amendments to propose (rippled RCLConsensus.cpp:886-894).
	// Empty means either not a flag ledger or no amendments to vote
	// on.
	Amendments [][32]byte

	// Fee-voting fields. Rippled emits these on flag ledgers via
	// FeeVote::doValidation (RCLConsensus.cpp:882-883). Pre-XRPFees
	// amendment nodes emit the UINT32/UINT64 legacy forms; post-
	// XRPFees they emit the AMOUNT "Drops" variants. We model both
	// sets; the adaptor populates whichever is appropriate for the
	// parent ledger's amendment set. Zero values mean "not emitted".

	// BaseFee is sfBaseFee (UINT64 field 5, legacy drops).
	BaseFee uint64
	// ReserveBase is sfReserveBase (UINT32 field 31, legacy drops).
	ReserveBase uint32
	// ReserveIncrement is sfReserveIncrement (UINT32 field 32, legacy
	// drops).
	ReserveIncrement uint32

	// BaseFeeDrops is sfBaseFeeDrops (AMOUNT field 22, post-XRPFees).
	// XRP-denominated drops amount encoded as an Amount.
	BaseFeeDrops uint64
	// ReserveBaseDrops is sfReserveBaseDrops (AMOUNT field 23).
	ReserveBaseDrops uint64
	// ReserveIncrementDrops is sfReserveIncrementDrops (AMOUNT field 24).
	ReserveIncrementDrops uint64

	// SigningData holds the canonical serialized fields (excluding
	// sfSignature, but INCLUDING sfSigningPubKey) for signature
	// verification. Populated by parseSTValidation for inbound
	// validations. For outbound self-built validations, it is left
	// nil — SignValidation synthesizes its own preimage from the
	// struct fields at sign time.
	SigningData []byte

	// SuppressionHash is the router-level dedup key for this validation.
	// Computed as sha512Half(innerSTValidationBlob) — matches rippled
	// PeerImp.cpp:2374 (`sha512Half(makeSlice(m->validation()))`).
	// Populated by the consensus router on inbound validations so later
	// relay + slot-feeding code doesn't have to recompute it. Zero on
	// self-originated validations (Broadcast skips the reverse index).
	SuppressionHash [32]byte
}

// AvalancheState tracks per-dispute threshold escalation during
// establish phase. Matches rippled's ConsensusParms::AvalancheState
// enum (ConsensusParms.h:134).
type AvalancheState int

const (
	// AvalancheInit requires 50% agreement. This is the starting state
	// for every new dispute.
	AvalancheInit AvalancheState = iota
	// AvalancheMid requires 65%. Triggered once the round has run
	// past 50% of the previous round's duration.
	AvalancheMid
	// AvalancheLate requires 70%. Triggered at 85%.
	AvalancheLate
	// AvalancheStuck requires 95%. Triggered at 200% (i.e., the round
	// is taking twice as long as the previous one).
	AvalancheStuck
)

// DisputedTx represents a transaction that validators disagree on.
//
// During consensus, a DisputedTx exists for every tx found in the
// symmetric difference between our proposed tx set and any peer's
// proposed tx set. The struct tracks per-peer votes so we can
// correctly re-vote (one peer changing its vote does not double-count)
// and strip a bowed-out peer's contribution via unVote.
//
// Matches rippled's DisputedTx template class
// (rippled/src/xrpld/consensus/DisputedTx.h).
type DisputedTx struct {
	// TxID is the transaction hash.
	TxID TxID

	// Tx is the raw transaction bytes.
	Tx []byte

	// OurVote is whether we think this tx should be included.
	OurVote bool

	// Yays is the count of validators (not including us) who voted to
	// include. Cached from Votes; SetVote/UnVote keep it in sync.
	Yays int

	// Nays is the count of validators (not including us) who voted to
	// exclude. Cached from Votes; SetVote/UnVote keep it in sync.
	Nays int

	// Votes tracks per-peer yes/no votes on this transaction. A peer
	// without an entry has not yet reported a position that lets us
	// count it. Maintained by DisputeTracker.SetVote / UnVote.
	Votes map[NodeID]bool

	// AvalancheState is the current threshold bracket for this
	// dispute's re-vote logic. It advances monotonically through
	// init/mid/late/stuck as consensus duration progresses.
	AvalancheState AvalancheState

	// AvalancheCounter counts phaseEstablish ticks spent in the
	// current AvalancheState. Rippled's getNeededWeight uses this to
	// enforce avMIN_ROUNDS before advancing.
	AvalancheCounter int

	// CurrentVoteCounter counts phaseEstablish ticks since we last
	// changed OurVote. Rippled's stalled() check uses this together
	// with peerUnchangedCounter to detect dead-locked disputes.
	CurrentVoteCounter int
}

// CloseTimes tracks proposed close times from validators.
type CloseTimes struct {
	// Peers maps close time to count of validators proposing it.
	Peers map[time.Time]int

	// Self is our proposed close time.
	Self time.Time
}

// RoundState represents the current state of a consensus round.
type RoundState struct {
	// Round identifies this consensus round.
	Round RoundID

	// Mode is the current operating mode.
	Mode Mode

	// Phase is the current consensus phase.
	Phase Phase

	// Proposals is the set of proposals received this round.
	Proposals map[NodeID]*Proposal

	// Disputed tracks transactions with disagreement.
	Disputed map[TxID]*DisputedTx

	// CloseTimes tracks proposed close times.
	CloseTimes CloseTimes

	// OurPosition is our current proposal (if proposing).
	OurPosition *Proposal

	// StartTime is when this round started.
	StartTime time.Time

	// PhaseStart is when the current phase started.
	PhaseStart time.Time

	// Converged indicates if proposals have converged.
	Converged bool

	// HaveCorrectLCL indicates if we have the correct last closed ledger.
	HaveCorrectLCL bool
}

// Timing holds consensus timing parameters.
type Timing struct {
	// LedgerMinClose is minimum time a ledger stays open.
	LedgerMinClose time.Duration

	// LedgerMaxClose is a legacy alias for LedgerMaxConsensus kept for
	// source compatibility. New code should read LedgerMaxConsensus.
	// Prior to E3 this was a goXRPL-only 10s hard timeout that did not
	// correspond to any rippled constant — DefaultTiming now pins it to
	// LedgerMaxConsensus (15s) so call-sites retain matching semantics.
	LedgerMaxClose time.Duration

	// LedgerIdleInterval is time between ledgers when idle.
	LedgerIdleInterval time.Duration

	// LedgerMinConsensus is the minimum time to remain in the establish phase
	// before accepting consensus. Matches rippled's ledgerMIN_CONSENSUS (1950ms).
	LedgerMinConsensus time.Duration

	// LedgerMaxConsensus is the soft deadline for the establish phase.
	// After this duration the engine forces acceptance (ResultTimeout)
	// rather than waiting further. Matches rippled's ledgerMAX_CONSENSUS
	// (ConsensusParms.h:95 = 15s).
	LedgerMaxConsensus time.Duration

	// LedgerAbandonConsensus is the absolute hard ceiling for a
	// consensus round. If the round exceeds this duration it is
	// abandoned — we bow out and emit ResultAbandoned. Matches
	// rippled's ledgerABANDON_CONSENSUS (ConsensusParms.h:113 = 120s).
	LedgerAbandonConsensus time.Duration

	// LedgerAbandonConsensusFactor scales the previous round's duration
	// to produce the actual abandon clamp. The effective hard deadline
	// is clamp(prevRoundTime * factor, LedgerMaxConsensus, LedgerAbandonConsensus).
	// Matches rippled's ledgerABANDON_CONSENSUS_FACTOR (ConsensusParms.h:105 = 10).
	LedgerAbandonConsensusFactor int

	// LedgerGranularity is the close time resolution.
	LedgerGranularity time.Duration

	// ProposeFreshness is how long a proposal is considered fresh.
	ProposeFreshness time.Duration

	// ValidationFreshness is how long a validation is considered fresh.
	ValidationFreshness time.Duration
}

// DefaultTiming returns the default consensus timing parameters.
func DefaultTiming() Timing {
	return Timing{
		LedgerMinClose:               2 * time.Second,
		LedgerMaxConsensus:           15 * time.Second,
		LedgerMaxClose:               15 * time.Second, // legacy alias, kept in sync with LedgerMaxConsensus
		LedgerAbandonConsensus:       120 * time.Second,
		LedgerAbandonConsensusFactor: 10,
		LedgerMinConsensus:           1950 * time.Millisecond,
		LedgerIdleInterval:           15 * time.Second,
		LedgerGranularity:            10 * time.Second,
		ProposeFreshness:             20 * time.Second,
		ValidationFreshness:          20 * time.Second,
	}
}

// Thresholds holds consensus threshold parameters.
//
// Note on terminology: rippled defines a single consensus percentage,
// minCONSENSUS_PCT = 80 (see rippled/src/xrpld/consensus/ConsensusParms.h:79),
// which is the threshold above which consensus may be declared. goXRPL
// layers an additional lower gate (EarlyConvergencePct) used to mark a
// round as "converged" earlier than the accept threshold — this is a
// goXRPL-local construct and has no direct rippled counterpart. The
// accept threshold itself (MinConsensusPct below) is arithmetically
// identical to rippled's minCONSENSUS_PCT.
type Thresholds struct {
	// EarlyConvergencePct is the percentage of trusted proposals that must
	// agree on a tx set for a round to be marked "converged" (but not yet
	// accepted). This is a goXRPL-local early-convergence gate and has no
	// direct equivalent in rippled.
	EarlyConvergencePct int

	// IncreaseConsensusPct is the percentage increase per round.
	IncreaseConsensusPct int

	// MinConsensusPct is the minimum percentage of trusted proposals that
	// must agree on a tx set before consensus may be declared. This
	// corresponds directly to rippled's minCONSENSUS_PCT = 80 (see
	// rippled/src/xrpld/consensus/ConsensusParms.h:79).
	MinConsensusPct int
}

// DefaultThresholds returns the default consensus thresholds.
//
// MinConsensusPct = 80 matches rippled's minCONSENSUS_PCT
// (rippled/src/xrpld/consensus/ConsensusParms.h:79). EarlyConvergencePct
// is a goXRPL-local earlier gate used to flag convergence before accept.
func DefaultThresholds() Thresholds {
	return Thresholds{
		EarlyConvergencePct:  50,
		IncreaseConsensusPct: 5,
		MinConsensusPct:      80,
	}
}

// AvalancheCutoff is one row in the avalanche cutoff table. Matches
// rippled's ConsensusParms::AvalancheCutoff (ConsensusParms.h:135-140).
type AvalancheCutoff struct {
	// ConsensusTime is the convergePercent threshold at which this
	// state activates (e.g., 50 means "once we're 50% of the way
	// through a normal round, advance to this state").
	ConsensusTime int

	// ConsensusPct is the agreement percentage required while in this
	// state for a dispute to flip our vote.
	ConsensusPct int

	// Next is the state this one advances to once ConsensusTime of the
	// successor has been reached. Stuck loops back to itself.
	Next AvalancheState
}

// ConsensusParms holds the avalanche-threshold cutoffs and the
// min/stalled round counts that drive per-tx dispute re-voting.
// Matches rippled's ConsensusParms (ConsensusParms.h:38-170) for the
// subset used by DisputedTx::updateVote.
type ConsensusParms struct {
	// AvalancheCutoffs maps each state to its activation time,
	// required agreement percentage, and next state. Rippled uses
	// {init:(0,50,mid), mid:(50,65,late), late:(85,70,stuck),
	//  stuck:(200,95,stuck)}.
	AvalancheCutoffs map[AvalancheState]AvalancheCutoff

	// MinRounds is the minimum number of phaseEstablish ticks that
	// must be spent in a given avalanche state before advancing.
	// Matches rippled's avMIN_ROUNDS = 2.
	MinRounds int

	// StalledRounds is the number of rounds without any vote change
	// after which a dispute is considered stalled.
	// Matches rippled's avSTALLED_ROUNDS = 4.
	StalledRounds int

	// MinConsensusPct is the stall threshold: a dispute with more
	// than MinConsensusPct agreement one way or the other is
	// considered stuck. Matches rippled's minCONSENSUS_PCT = 80.
	MinConsensusPct int
}

// DefaultConsensusParms returns the avalanche parameters matching
// rippled's defaults (ConsensusParms.h:146-157,165,169).
func DefaultConsensusParms() ConsensusParms {
	return ConsensusParms{
		AvalancheCutoffs: map[AvalancheState]AvalancheCutoff{
			AvalancheInit:  {ConsensusTime: 0, ConsensusPct: 50, Next: AvalancheMid},
			AvalancheMid:   {ConsensusTime: 50, ConsensusPct: 65, Next: AvalancheLate},
			AvalancheLate:  {ConsensusTime: 85, ConsensusPct: 70, Next: AvalancheStuck},
			AvalancheStuck: {ConsensusTime: 200, ConsensusPct: 95, Next: AvalancheStuck},
		},
		MinRounds:       2,
		StalledRounds:   4,
		MinConsensusPct: 80,
	}
}

// NeededWeight computes the agreement percentage required for a
// dispute at the current avalanche state, and optionally the next
// state to advance into.
//
// Matches rippled's getNeededWeight free function
// (ConsensusParms.h:172-199): we may advance to the next state iff
// the current state is not terminal, at least minimumRounds have
// passed in this state, and enough round-percent time has elapsed to
// cross the next cutoff.
func (p ConsensusParms) NeededWeight(
	state AvalancheState,
	percentTime int,
	currentRounds int,
	minimumRounds int,
) (int, *AvalancheState) {
	current := p.AvalancheCutoffs[state]
	if current.Next != state && currentRounds >= minimumRounds {
		next := p.AvalancheCutoffs[current.Next]
		if percentTime >= next.ConsensusTime {
			advanced := current.Next
			return next.ConsensusPct, &advanced
		}
	}
	return current.ConsensusPct, nil
}

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

	// Signature is the validator's signature.
	Signature []byte

	// Full indicates if this is a full validation (vs partial).
	Full bool

	// Cookie is a unique identifier for this validation session.
	Cookie uint64

	// LoadFee is the validator's current load-based fee.
	LoadFee uint32
}

// DisputedTx represents a transaction that validators disagree on.
type DisputedTx struct {
	// TxID is the transaction hash.
	TxID TxID

	// Tx is the raw transaction bytes.
	Tx []byte

	// OurVote is whether we think this tx should be included.
	OurVote bool

	// Yays is the count of validators who voted to include.
	Yays int

	// Nays is the count of validators who voted to exclude.
	Nays int
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

	// LedgerMaxClose is maximum time before forcing close.
	LedgerMaxClose time.Duration

	// LedgerIdleInterval is time between ledgers when idle.
	LedgerIdleInterval time.Duration

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
		LedgerMinClose:      2 * time.Second,
		LedgerMaxClose:      10 * time.Second,
		LedgerIdleInterval:  15 * time.Second,
		LedgerGranularity:   10 * time.Second,
		ProposeFreshness:    20 * time.Second,
		ValidationFreshness: 20 * time.Second,
	}
}

// Thresholds holds consensus threshold parameters.
type Thresholds struct {
	// MinConsensusPct is the minimum percentage for initial consensus.
	MinConsensusPct int

	// IncreaseConsensusPct is the percentage increase per round.
	IncreaseConsensusPct int

	// MaxConsensusPct is the maximum consensus percentage required.
	MaxConsensusPct int
}

// DefaultThresholds returns the default consensus thresholds.
func DefaultThresholds() Thresholds {
	return Thresholds{
		MinConsensusPct:      50,
		IncreaseConsensusPct: 5,
		MaxConsensusPct:      80,
	}
}

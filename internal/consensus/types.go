// Package consensus defines the interface and types for XRPL consensus algorithms.
// It provides a pluggable architecture allowing different consensus implementations
// to be used interchangeably.
package consensus

import (
	"time"

	"github.com/LeJamon/go-xrpl/drops"
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

// NodeID uniquely identifies a validator in the network. It is the
// 20-byte RIPEMD-160(SHA-256(masterPubKey)) hash matching rippled's
// calcNodeID (rippled/src/libxrpl/protocol/PublicKey.cpp:319-327 and
// rippled/include/xrpl/protocol/UintTypes.h:59 — base_uint<160>).
//
// NodeID is distinct from the 33-byte compressed signing public key
// that travels on the wire as sfSigningPubKey: the wire field carries
// the rotatable ephemeral key, while NodeID identifies the long-term
// master key behind it. Use CalcNodeID to derive a NodeID from a
// master pubkey; the consensus router resolves an inbound message's
// signing key to its master via the manifest cache before populating
// NodeID, so all in-memory maps key on the master shape consistently.
type NodeID [20]byte

// SigningPubKey is a 33-byte compressed XRPL public key (secp256k1
// 0x02/0x03 prefix or ed25519 0xED prefix). It carries the ephemeral
// signing key on Proposal/Validation — the bytes that go on the wire
// as sfSigningPubKey and that the signature verifier consumes.
type SigningPubKey [33]byte

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

	// NodeID is the proposing validator's master-derived 20-byte
	// identifier. Populated from SigningPubKey via CalcNodeID, with
	// the consensus router substituting the master key from the
	// manifest cache before quorum lookups.
	NodeID NodeID

	// SigningPubKey is the 33-byte compressed ephemeral signing key
	// the proposal was signed with — the bytes carried on the wire as
	// TMProposeSet.nodepubkey and consumed by VerifyProposal.
	SigningPubKey SigningPubKey

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

	// NodeID is the validating node's master-derived 20-byte
	// identifier. Populated from SigningPubKey via CalcNodeID, with
	// the consensus router substituting the master key from the
	// manifest cache before trust / quorum lookups.
	NodeID NodeID

	// SigningPubKey is the 33-byte compressed ephemeral signing key
	// the validation was signed with — the bytes carried on the wire
	// as sfSigningPubKey and consumed by VerifyValidation.
	SigningPubKey SigningPubKey

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
	// Derived from (Flags & vfFullValidation) != 0; carried alongside
	// Flags so existing call sites that branch on Full keep working.
	Full bool

	// Flags is the original sfFlags wire word as signed by the
	// validator. parseSTValidation captures it verbatim; outbound
	// self-built validations set vfFullValidation | vfFullyCanonicalSig.
	// Consumers reading the field should mask known bits — rippled may
	// set additional vendor flags we don't yet recognize.
	Flags uint32

	// Cookie is a unique identifier for this validation session.
	Cookie uint64

	// LoadFee is the validator's current load-based fee.
	LoadFee uint32
	// loadFeePresent distinguishes an explicit sfLoadFee value of zero from an
	// omitted field. Non-zero legacy struct literals are treated as present too.
	loadFeePresent bool

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

	// Fee-voting fields. Pre-XRPFees validations use the unsigned legacy
	// forms; post-XRPFees validations use signed native Amounts.

	// BaseFee is sfBaseFee (UINT64 field 5, legacy drops).
	BaseFee uint64
	// ReserveBase is sfReserveBase (UINT32 field 31, legacy drops).
	ReserveBase uint32
	// ReserveIncrement is sfReserveIncrement (UINT32 field 32, legacy
	// drops).
	ReserveIncrement uint32

	// BaseFeeDrops is sfBaseFeeDrops (AMOUNT field 22, post-XRPFees).
	BaseFeeDrops drops.XRPAmount
	// ReserveBaseDrops is sfReserveBaseDrops (AMOUNT field 23).
	ReserveBaseDrops drops.XRPAmount
	// ReserveIncrementDrops is sfReserveIncrementDrops (AMOUNT field 24).
	ReserveIncrementDrops drops.XRPAmount

	feeVotePresent uint8
	feeVoteNative  uint8

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

	// Raw is the original wire bytes of the serialized STValidation.
	// Populated by parseSTValidation for inbound validations. Nil for
	// self-built validations until SerializeSTValidation is called.
	// Used by the validation archive to persist the canonical blob
	// without a parse → re-serialize round-trip.
	Raw []byte
}

// SetLoadFee records sfLoadFee as present, including when fee is zero.
func (v *Validation) SetLoadFee(fee uint32) {
	v.LoadFee = fee
	v.loadFeePresent = true
}

// HasLoadFee reports whether sfLoadFee was present on the wire or populated by
// a legacy non-zero struct literal.
func (v *Validation) HasLoadFee() bool {
	return v != nil && (v.loadFeePresent || v.LoadFee != 0)
}

const (
	feeVoteBase uint8 = 1 << iota
	feeVoteReserveBase
	feeVoteReserveIncrement
	feeVoteBaseDrops
	feeVoteReserveBaseDrops
	feeVoteReserveIncrementDrops
)

// SetBaseFee records sfBaseFee as present, including when it is zero.
func (v *Validation) SetBaseFee(value uint64) {
	v.BaseFee = value
	v.feeVotePresent |= feeVoteBase
}

// HasBaseFee reports whether sfBaseFee is present.
func (v *Validation) HasBaseFee() bool {
	return v != nil && (v.feeVotePresent&feeVoteBase != 0 || v.BaseFee != 0)
}

// SetReserveBase records sfReserveBase as present, including when it is zero.
func (v *Validation) SetReserveBase(value uint32) {
	v.ReserveBase = value
	v.feeVotePresent |= feeVoteReserveBase
}

// HasReserveBase reports whether sfReserveBase is present.
func (v *Validation) HasReserveBase() bool {
	return v != nil && (v.feeVotePresent&feeVoteReserveBase != 0 || v.ReserveBase != 0)
}

// SetReserveIncrement records sfReserveIncrement as present, including when it is zero.
func (v *Validation) SetReserveIncrement(value uint32) {
	v.ReserveIncrement = value
	v.feeVotePresent |= feeVoteReserveIncrement
}

// HasReserveIncrement reports whether sfReserveIncrement is present.
func (v *Validation) HasReserveIncrement() bool {
	return v != nil && (v.feeVotePresent&feeVoteReserveIncrement != 0 || v.ReserveIncrement != 0)
}

// SetBaseFeeDrops records a native sfBaseFeeDrops value as present.
func (v *Validation) SetBaseFeeDrops(value drops.XRPAmount) {
	v.BaseFeeDrops = value
	v.feeVotePresent |= feeVoteBaseDrops
	v.feeVoteNative |= feeVoteBaseDrops
}

// SetBaseFeeDropsNonNative records a present non-native sfBaseFeeDrops value.
func (v *Validation) SetBaseFeeDropsNonNative() {
	v.BaseFeeDrops = 0
	v.feeVotePresent |= feeVoteBaseDrops
	v.feeVoteNative &^= feeVoteBaseDrops
}

// BaseFeeDropsVote returns the native vote and whether the field is present and native.
func (v *Validation) BaseFeeDropsVote() (drops.XRPAmount, bool) {
	return v.nativeFeeVote(feeVoteBaseDrops, v.BaseFeeDrops)
}

// HasBaseFeeDrops reports whether sfBaseFeeDrops is present.
func (v *Validation) HasBaseFeeDrops() bool {
	return v != nil && (v.feeVotePresent&feeVoteBaseDrops != 0 || v.BaseFeeDrops != 0)
}

// SetReserveBaseDrops records a native sfReserveBaseDrops value as present.
func (v *Validation) SetReserveBaseDrops(value drops.XRPAmount) {
	v.ReserveBaseDrops = value
	v.feeVotePresent |= feeVoteReserveBaseDrops
	v.feeVoteNative |= feeVoteReserveBaseDrops
}

// SetReserveBaseDropsNonNative records a present non-native sfReserveBaseDrops value.
func (v *Validation) SetReserveBaseDropsNonNative() {
	v.ReserveBaseDrops = 0
	v.feeVotePresent |= feeVoteReserveBaseDrops
	v.feeVoteNative &^= feeVoteReserveBaseDrops
}

// ReserveBaseDropsVote returns the native vote and whether the field is present and native.
func (v *Validation) ReserveBaseDropsVote() (drops.XRPAmount, bool) {
	return v.nativeFeeVote(feeVoteReserveBaseDrops, v.ReserveBaseDrops)
}

// HasReserveBaseDrops reports whether sfReserveBaseDrops is present.
func (v *Validation) HasReserveBaseDrops() bool {
	return v != nil && (v.feeVotePresent&feeVoteReserveBaseDrops != 0 || v.ReserveBaseDrops != 0)
}

// SetReserveIncrementDrops records a native sfReserveIncrementDrops value as present.
func (v *Validation) SetReserveIncrementDrops(value drops.XRPAmount) {
	v.ReserveIncrementDrops = value
	v.feeVotePresent |= feeVoteReserveIncrementDrops
	v.feeVoteNative |= feeVoteReserveIncrementDrops
}

// SetReserveIncrementDropsNonNative records a present non-native sfReserveIncrementDrops value.
func (v *Validation) SetReserveIncrementDropsNonNative() {
	v.ReserveIncrementDrops = 0
	v.feeVotePresent |= feeVoteReserveIncrementDrops
	v.feeVoteNative &^= feeVoteReserveIncrementDrops
}

// ReserveIncrementDropsVote returns the native vote and whether the field is present and native.
func (v *Validation) ReserveIncrementDropsVote() (drops.XRPAmount, bool) {
	return v.nativeFeeVote(feeVoteReserveIncrementDrops, v.ReserveIncrementDrops)
}

// HasReserveIncrementDrops reports whether sfReserveIncrementDrops is present.
func (v *Validation) HasReserveIncrementDrops() bool {
	return v != nil && (v.feeVotePresent&feeVoteReserveIncrementDrops != 0 || v.ReserveIncrementDrops != 0)
}

func (v *Validation) nativeFeeVote(field uint8, value drops.XRPAmount) (drops.XRPAmount, bool) {
	if v == nil || !v.hasNativeFeeVote(field, value) {
		return 0, false
	}
	return value, true
}

func (v *Validation) hasNativeFeeVote(field uint8, value drops.XRPAmount) bool {
	if v.feeVotePresent&field == 0 {
		return value != 0
	}
	return v.feeVoteNative&field != 0
}

// ByzantineValidationError reports a validation that conflicts with one
// already tracked for the same node and sequence — a double-sign that is
// either misconfiguration or a Byzantine validator. The engine returns it
// from OnValidation to signal that the validation was kept out of the
// quorum/trie but still relayed; the router logs it and does NOT charge
// the delivering peer, mirroring rippled's log-and-forward handling of
// ValStatus conflicting/multiple (Validations.h:637-681,
// RCLValidations.cpp:214-247).
type ByzantineValidationError struct {
	NodeID NodeID
	// Reason is "conflicting" (different ledger, or same ledger with a
	// different sign time) or "multiple" (same ledger and sign time but a
	// different cookie — probably accidental misconfiguration).
	Reason string
	// Trusted reports whether the double-signing validator is in our UNL;
	// the router logs trusted offenders at error and listed-but-untrusted
	// ones at info, mirroring rippled's journal-level split.
	Trusted bool
}

func (e *ByzantineValidationError) Error() string {
	return "byzantine validation (" + e.Reason + ")"
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

// Timing holds consensus timing parameters.
type Timing struct {
	// LedgerMinClose is minimum time a ledger stays open.
	LedgerMinClose time.Duration

	// LedgerIdleInterval is time between ledgers when idle.
	LedgerIdleInterval time.Duration

	// LedgerMinConsensus is the minimum time to remain in the establish phase
	// before accepting consensus. Matches rippled's ledgerMIN_CONSENSUS (1950ms).
	LedgerMinConsensus time.Duration

	// LedgerMaxConsensus is the soft deadline for the establish phase.
	// After this duration the engine moves on and forces acceptance
	// (emitting ResultMovedOn) rather than waiting further. Matches
	// rippled's ledgerMAX_CONSENSUS (ConsensusParms.h:95 = 15s).
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

	// LedgerGranularity is how often the engine checks state or
	// changes positions during a consensus round — the heartbeat
	// interval. Matches rippled's ledgerGRANULARITY
	// (ConsensusParms.h:102 = 1s). Dispute re-vote cadence and the
	// peerUnchangedCounter advance once per granularity tick, so a
	// value larger than rippled's 1s slows stall detection
	// proportionally. (Close-time resolution is a distinct concept
	// derived from getNextLedgerTimeResolution, not from this field.)
	LedgerGranularity time.Duration

	// ProposeFreshness is how long a proposal is considered fresh.
	// Matches rippled's proposeFRESHNESS (ConsensusParms.h:69 = 20s).
	ProposeFreshness time.Duration

	// ProposeInterval is how often we force generating a new proposal
	// to keep ours fresh. Matches rippled's proposeINTERVAL
	// (ConsensusParms.h:72 = 12s). updatePosition re-proposes our
	// unchanged position once it goes this stale, so peers don't prune it
	// at ProposeFreshness during a long round (rippled Consensus.h:1636).
	ProposeInterval time.Duration

	// ValidationFreshness is how long a validation is considered fresh
	// for laggard accounting. Matches rippled's validationFRESHNESS
	// (Validations.h:89 = 20s).
	ValidationFreshness time.Duration
}

// DefaultTiming returns the default consensus timing parameters.
func DefaultTiming() Timing {
	return Timing{
		LedgerMinClose:               2 * time.Second,
		LedgerMaxConsensus:           15 * time.Second,
		LedgerAbandonConsensus:       120 * time.Second,
		LedgerAbandonConsensusFactor: 10,
		LedgerMinConsensus:           1950 * time.Millisecond,
		LedgerIdleInterval:           15 * time.Second,
		LedgerGranularity:            1 * time.Second,
		ProposeFreshness:             20 * time.Second,
		ProposeInterval:              12 * time.Second,
		ValidationFreshness:          20 * time.Second,
	}
}

// Thresholds holds consensus threshold parameters.
type Thresholds struct {
	// MinConsensusPct is the minimum percentage of trusted proposals that
	// must agree on a tx set before consensus may be declared. This
	// corresponds directly to rippled's minCONSENSUS_PCT = 80 (see
	// rippled/src/xrpld/consensus/ConsensusParms.h:79).
	MinConsensusPct int
}

// DefaultThresholds returns the default consensus thresholds.
//
// MinConsensusPct = 80 matches rippled's minCONSENSUS_PCT
// (rippled/src/xrpld/consensus/ConsensusParms.h:79).
func DefaultThresholds() Thresholds {
	return Thresholds{
		MinConsensusPct: 80,
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
		MinRounds:       2,
		StalledRounds:   4,
		MinConsensusPct: 80,
	}
}

// AvalancheCutoff returns the fixed protocol cutoff for state.
func (ConsensusParms) AvalancheCutoff(state AvalancheState) AvalancheCutoff {
	switch state {
	case AvalancheInit:
		return AvalancheCutoff{ConsensusTime: 0, ConsensusPct: 50, Next: AvalancheMid}
	case AvalancheMid:
		return AvalancheCutoff{ConsensusTime: 50, ConsensusPct: 65, Next: AvalancheLate}
	case AvalancheLate:
		return AvalancheCutoff{ConsensusTime: 85, ConsensusPct: 70, Next: AvalancheStuck}
	case AvalancheStuck:
		return AvalancheCutoff{ConsensusTime: 200, ConsensusPct: 95, Next: AvalancheStuck}
	default:
		panic("invalid avalanche state")
	}
}

// AvalancheCutoffCount returns the number of fixed avalanche states.
func (ConsensusParms) AvalancheCutoffCount() int {
	return int(AvalancheStuck-AvalancheInit) + 1
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
	current := p.AvalancheCutoff(state)
	if current.Next != state && currentRounds >= minimumRounds {
		next := p.AvalancheCutoff(current.Next)
		if next.ConsensusTime < current.ConsensusTime {
			panic("avalanche cutoff time decreased")
		}
		if percentTime >= next.ConsensusTime {
			advanced := current.Next
			return next.ConsensusPct, &advanced
		}
	}
	return current.ConsensusPct, nil
}

// Package feevote ports rippled's FeeVoteImpl
// (src/xrpld/app/misc/FeeVoteImpl.cpp) — the producer side that
// decides whether to inject a SetFee pseudo-tx into the consensus
// tx set on a flag-ledger boundary, based on trusted validators'
// fee votes from the prior voting ledger.
//
// The algorithm:
//
//  1. Initialize three VotableValues (baseFee, reserveBase,
//     reserveIncrement) seeded with the parent ledger's current
//     fees and the local validator's preferred target. Each
//     constructor pre-increments voteMap[target], which represents
//     the local validator's stance.
//
//  2. For each trusted validation, extract the fee fields and
//     addVote() the value (or noVote() if the field is missing or
//     out of legal range — counts as a vote for current).
//
//  3. getVotes() picks the most-voted value WITHIN
//     [min(current, target), max(current, target)] — votes outside
//     that window are clamped out. The chosen value flips
//     `changed` if it differs from current.
//
//  4. If any of the three changed, build a SetFee pseudo-tx with
//     all three values (chosen-or-current) and the upcoming
//     ledger sequence. Pre-XRPFees uses sfBaseFee/sfReserveBase/
//     sfReserveIncrement/sfReferenceFeeUnits; post-XRPFees uses
//     sfBaseFeeDrops/sfReserveBaseDrops/sfReserveIncrementDrops.
package feevote

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/pseudo"
)

// MaxLegalDrops is the upper bound on a legal XRPAmount, equal to
// INITIAL_XRP (1e17 drops = 100 billion XRP). Mirrors rippled's
// isLegalAmountSigned check at SystemParameters.h:55-59 — values
// exceeding this are silently treated as "no vote" rather than
// errors. rippled also rejects amounts below -INITIAL_XRP, but Vote
// fields are *uint64 so the negative branch is structurally
// unreachable here. Any extractor that builds Vote from an
// STValidation MUST clamp negative XRPAmounts to noVote before
// reaching this layer.
const MaxLegalDrops uint64 = 100_000_000_000 * 1_000_000

// ReferenceFeeUnitsDeprecated is the legacy sfReferenceFeeUnits
// value rippled stamps on every pre-XRPFees SetFee pseudo-tx
// (FeeVoteImpl.cpp:317 → Config::FEE_UNITS_DEPRECATED == 10).
const ReferenceFeeUnitsDeprecated uint32 = 10

// Stance is the three vote-able fee parameters (in drops) — both
// the parent ledger's "current" and the validator's preferred
// "target" use this shape. ReserveBase / ReserveIncrement are
// uint32 in the pre-XRPFees wire format; values above UINT32_MAX
// fall back to current at emission time, mirroring rippled's
// dropsAs<uint32>(current) at FeeVoteImpl.cpp:312-316.
type Stance struct {
	BaseFee          uint64
	ReserveBase      uint64
	ReserveIncrement uint64
}

// Vote is one trusted validator's per-field fee preference,
// extracted from sfBaseFee / sfReserveBase / sfReserveIncrement
// (pre-XRPFees) or the *Drops variants (post-XRPFees) on their
// STValidation. Each field is independent: a validator may emit
// any subset, and a missing or out-of-range value is treated as
// "noVote" — counted as a vote for current at tally time.
//
// Preconditions on extractor (the future STValidation→Vote
// adapter, deferred per #369): the extractor MUST set the field
// to nil ("noVote") when:
//
//   - post-XRPFees: the STAmount is non-native (IOU). Mirrors
//     FeeVoteImpl.cpp:222 (`field->native()` guard).
//   - pre-XRPFees: the STUInt64 exceeds INT64_MAX. Mirrors
//     FeeVoteImpl.cpp:254 (`vote <= numeric_limits<int64>::max()`
//     guard, which precedes isLegalAmountSigned).
//   - either mode: the XRPAmount is negative. Mirrors
//     isLegalAmountSigned's lower bound at SystemParameters.h:58.
//
// applyVote here only enforces the upper bound (MaxLegalDrops);
// the other three conditions are structurally unrepresentable in
// *uint64 and so MUST be filtered upstream.
type Vote struct {
	BaseFee          *uint64
	ReserveBase      *uint64
	ReserveIncrement *uint64
}

// votableValue is the per-field tallying state. Mirrors rippled's
// detail::VotableValue at FeeVoteImpl.cpp:31-86.
type votableValue struct {
	current uint64
	target  uint64
	votes   map[uint64]int
}

func newVotableValue(current, target uint64) *votableValue {
	v := &votableValue{
		current: current,
		target:  target,
		votes:   map[uint64]int{},
	}
	v.votes[target]++
	return v
}

func (v *votableValue) addVote(value uint64) {
	v.votes[value]++
}

func (v *votableValue) noVote() {
	v.votes[v.current]++
}

// getVotes returns (chosen, changed). chosen is the most-voted
// value within [min(current, target), max(current, target)] —
// votes outside that range are clamped out. On ties, the lowest
// in-window key wins, matching rippled's std::map ascending-key
// iteration with strict `val > weight` at FeeVoteImpl.cpp:69-86.
// changed indicates whether chosen differs from current.
func (v *votableValue) getVotes() (uint64, bool) {
	lo, hi := v.current, v.target
	if lo > hi {
		lo, hi = hi, lo
	}

	keys := make([]uint64, 0, len(v.votes))
	for k := range v.votes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	chosen := v.current
	weight := 0
	for _, value := range keys {
		if value < lo || value > hi {
			continue
		}
		if v.votes[value] > weight {
			chosen = value
			weight = v.votes[value]
		}
	}
	return chosen, chosen != v.current
}

// DoVoting tallies trusted validators' fee votes from the
// validations of the prior voting ledger and returns a serialized
// SetFee pseudo-tx blob if any of the three settings would
// change, or nil otherwise.
//
// upcomingSeq is the sequence the SetFee tx will carry (parent +
// 1). current is the parent ledger's fee setup; target is the
// local validator's preferred stance. votes are the trusted
// validations' fee votes (the local validator's stance is already
// represented by target — getVotes seeds +1 for target in the
// constructor, so the local stance is implicit).
//
// xrpFeesEnabled selects the pre-XRPFees vs post-XRPFees wire
// format. Both use the same algorithm; only the SetFee field set
// differs.
func DoVoting(
	upcomingSeq uint32,
	current, target Stance,
	votes []Vote,
	xrpFeesEnabled bool,
) ([]byte, error) {
	baseFee := newVotableValue(current.BaseFee, target.BaseFee)
	reserveBase := newVotableValue(current.ReserveBase, target.ReserveBase)
	reserveIncrement := newVotableValue(current.ReserveIncrement, target.ReserveIncrement)

	for _, v := range votes {
		applyVote(baseFee, v.BaseFee)
		applyVote(reserveBase, v.ReserveBase)
		applyVote(reserveIncrement, v.ReserveIncrement)
	}

	chosenBase, baseChanged := baseFee.getVotes()
	chosenReserveBase, reserveBaseChanged := reserveBase.getVotes()
	chosenReserveIncrement, reserveIncrementChanged := reserveIncrement.getVotes()

	if !baseChanged && !reserveBaseChanged && !reserveIncrementChanged {
		return nil, nil
	}

	chosen := Stance{
		BaseFee:          chosenBase,
		ReserveBase:      chosenReserveBase,
		ReserveIncrement: chosenReserveIncrement,
	}
	return buildSetFeeTx(upcomingSeq, current, chosen, xrpFeesEnabled)
}

// applyVote routes a per-validator field vote into the tally.
// Missing fields and overflow values both count as a vote for
// current (noVote), so a single bad field does not poison the
// rest of a validator's votes — matching rippled's
// isLegalAmountSigned guard at FeeVoteImpl.cpp:222-262.
func applyVote(v *votableValue, field *uint64) {
	if field == nil || *field > MaxLegalDrops {
		v.noVote()
		return
	}
	v.addVote(*field)
}

// buildSetFeeTx serializes a SetFee pseudo-tx. Pre-XRPFees uses
// sfBaseFee (uint64) / sfReserveBase (uint32) / sfReserveIncrement
// (uint32) / sfReferenceFeeUnits; post-XRPFees uses sfBaseFeeDrops
// / sfReserveBaseDrops / sfReserveIncrementDrops (XRPAmount-as-
// string). Mirrors FeeVoteImpl.cpp:297-319.
func buildSetFeeTx(seq uint32, current, chosen Stance, xrpFeesEnabled bool) ([]byte, error) {
	stx := &pseudo.SetFee{
		BaseTx:         *tx.NewBaseTx(tx.TypeFee, pseudo.ZeroAccount),
		LedgerSequence: &seq,
	}

	if xrpFeesEnabled {
		stx.BaseFeeDrops = strconv.FormatUint(chosen.BaseFee, 10)
		stx.ReserveBaseDrops = strconv.FormatUint(chosen.ReserveBase, 10)
		stx.ReserveIncrementDrops = strconv.FormatUint(chosen.ReserveIncrement, 10)
	} else {
		stx.BaseFee = fmt.Sprintf("%X", chosen.BaseFee)
		rb := narrowToUint32(chosen.ReserveBase, current.ReserveBase)
		stx.ReserveBase = &rb
		ri := narrowToUint32(chosen.ReserveIncrement, current.ReserveIncrement)
		stx.ReserveIncrement = &ri
		ref := ReferenceFeeUnitsDeprecated
		stx.ReferenceFeeUnits = &ref
	}

	return pseudo.EncodePseudoTx(stx)
}

// narrowToUint32 returns chosen as uint32, or fallback if chosen
// exceeds UINT32_MAX. Mirrors rippled's
// dropsAs<std::uint32_t>(current) at FeeVoteImpl.cpp:312-316: if
// the chosen XRPAmount cannot fit in uint32, fall back to the
// current ledger setting rather than silently truncating.
//
// If fallback itself exceeds UINT32_MAX the low 32 bits are
// returned. This is unreachable on any real ledger — pre-XRPFees
// reserves are sourced from on-chain FeeSettings whose uint32
// fields cannot exceed UINT32_MAX by construction. Documented
// here so the divergence from rippled's `T(drops_)` cast (which
// is implementation-defined for an out-of-range XRPAmount) is
// explicit.
func narrowToUint32(chosen, fallback uint64) uint32 {
	if chosen > math.MaxUint32 {
		return uint32(fallback)
	}
	return uint32(chosen)
}

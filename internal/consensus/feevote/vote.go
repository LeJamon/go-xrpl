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
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/pseudo"
)

// MaxLegalDrops is the upper bound on a single XRPAmount field
// value. Mirrors rippled's isLegalAmountSigned check at
// FeeVoteImpl.cpp:225-228 — values exceeding this are silently
// treated as "no vote" rather than errors. 100 billion XRP * 10⁶
// drops/XRP. Matches codec/binarycodec/types.MaxDrops.
const MaxLegalDrops uint64 = 100_000_000_000 * 1_000_000

// ReferenceFeeUnitsDeprecated is the legacy sfReferenceFeeUnits
// value rippled stamps on every pre-XRPFees SetFee pseudo-tx
// (FeeVoteImpl.cpp:317 → Config::FEE_UNITS_DEPRECATED == 10).
const ReferenceFeeUnitsDeprecated uint32 = 10

// Stance is the three vote-able fee parameters (in drops) — both
// the parent ledger's "current" and the validator's preferred
// "target" use this shape. ReserveBase / ReserveIncrement are
// uint32 in the pre-XRPFees wire format but always representable
// as uint64 drops in memory.
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
// value within [min(current, target), max(current, target)];
// changed indicates whether chosen differs from current. Mirrors
// VotableValue::getVotes at FeeVoteImpl.cpp:69-86.
func (v *votableValue) getVotes() (uint64, bool) {
	lo, hi := v.current, v.target
	if lo > hi {
		lo, hi = hi, lo
	}

	chosen := v.current
	weight := 0
	for value, count := range v.votes {
		if value < lo || value > hi {
			continue
		}
		if count > weight {
			chosen = value
			weight = count
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
	return buildSetFeeTx(upcomingSeq, chosen, xrpFeesEnabled)
}

// applyVote calls addVote with field's value when set and within
// [0, MaxLegalDrops], or noVote otherwise. Out-of-range values are
// clamped out at the per-field level so a single bad field doesn't
// poison the rest of a validator's votes — matching rippled's
// isLegalAmountSigned check at FeeVoteImpl.cpp:225-262.
func applyVote(v *votableValue, field *uint64) {
	if field == nil || *field > MaxLegalDrops {
		v.noVote()
		return
	}
	v.addVote(*field)
}

// zeroAccount is the base58-encoded all-zero AccountID used as the
// source on every pseudo-transaction (rippled AccountID()). The
// wire form serializes to a 20-byte zero blob.
const zeroAccount = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"

// buildSetFeeTx serializes a SetFee pseudo-tx. Pre-XRPFees uses
// sfBaseFee (uint64) / sfReserveBase (uint32) / sfReserveIncrement
// (uint32) / sfReferenceFeeUnits; post-XRPFees uses sfBaseFeeDrops
// / sfReserveBaseDrops / sfReserveIncrementDrops (XRPAmount-as-
// string). Mirrors FeeVoteImpl.cpp:297-319.
func buildSetFeeTx(seq uint32, chosen Stance, xrpFeesEnabled bool) ([]byte, error) {
	zeroSeq := uint32(0)
	stx := &pseudo.SetFee{
		BaseTx:         *tx.NewBaseTx(tx.TypeFee, zeroAccount),
		LedgerSequence: &seq,
	}
	stx.Common.Fee = "0"
	stx.Common.SigningPubKey = ""
	stx.Common.Sequence = &zeroSeq

	if xrpFeesEnabled {
		stx.BaseFeeDrops = strconv.FormatUint(chosen.BaseFee, 10)
		stx.ReserveBaseDrops = strconv.FormatUint(chosen.ReserveBase, 10)
		stx.ReserveIncrementDrops = strconv.FormatUint(chosen.ReserveIncrement, 10)
	} else {
		// Pre-XRPFees: sfBaseFee is uint64 hex, sfReserveBase /
		// sfReserveIncrement are uint32. ReferenceFeeUnits is
		// required and always 10 (FEE_UNITS_DEPRECATED).
		baseFeeHex := fmt.Sprintf("%X", chosen.BaseFee)
		stx.BaseFee = baseFeeHex
		rb := uint32(chosen.ReserveBase)
		stx.ReserveBase = &rb
		ri := uint32(chosen.ReserveIncrement)
		stx.ReserveIncrement = &ri
		ref := ReferenceFeeUnitsDeprecated
		stx.ReferenceFeeUnits = &ref
	}

	flat, err := stx.Flatten()
	if err != nil {
		return nil, fmt.Errorf("flatten SetFee: %w", err)
	}
	hexStr, err := binarycodec.Encode(flat)
	if err != nil {
		return nil, fmt.Errorf("encode SetFee: %w", err)
	}
	return hex.DecodeString(hexStr)
}

// Package feevote decides whether to inject a SetFee pseudo-tx into the
// consensus tx set at a flag-ledger boundary, tallying trusted validators'
// fee votes from the prior voting ledger. Mirrors rippled FeeVoteImpl.cpp.
package feevote

import (
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/LeJamon/go-xrpl/internal/consensus/common"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
)

// MaxLegalDrops is the upper bound on a legal XRPAmount (INITIAL_XRP, 1e17
// drops); values above it count as "no vote". The negative bound is
// unreachable here (Vote fields are *uint64) but extractors MUST clamp
// negative XRPAmounts to noVote upstream.
const MaxLegalDrops uint64 = 100_000_000_000 * 1_000_000

// ReferenceFeeUnitsDeprecated is the legacy sfReferenceFeeUnits stamped on
// every pre-XRPFees SetFee (== 10).
const ReferenceFeeUnitsDeprecated uint32 = 10

// Stance is the three vote-able fee parameters (in drops), shared by
// "current" and "target". Pre-XRPFees ReserveBase/ReserveIncrement are
// uint32; values above UINT32_MAX fall back to current at emission.
type Stance struct {
	BaseFee          uint64
	ReserveBase      uint64
	ReserveIncrement uint64
}

// Vote is one trusted validator's per-field fee preference. Each field is
// independent; a missing or out-of-range value is "noVote" (a vote for
// current). applyVote enforces only the MaxLegalDrops upper bound — the
// extractor MUST set a field to nil for the cases unrepresentable in
// *uint64: post-XRPFees non-native (IOU) amounts, pre-XRPFees values above
// INT64_MAX, and negative XRPAmounts (either mode).
type Vote struct {
	BaseFee          *uint64
	ReserveBase      *uint64
	ReserveIncrement *uint64
}

// votableValue is the per-field tallying state.
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

// getVotes returns the most-voted value within [min,max](current,target)
// (out-of-window votes clamped; ties pick the lowest key) and whether it
// differs from current.
func (v *votableValue) getVotes() (uint64, bool) {
	lo, hi := v.current, v.target
	if lo > hi {
		lo, hi = hi, lo
	}

	keys := make([]uint64, 0, len(v.votes))
	for k := range v.votes {
		keys = append(keys, k)
	}
	slices.Sort(keys)

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

// DoVoting tallies trusted validators' fee votes and returns a SetFee
// pseudo-tx blob if any of the three settings would change, else nil.
// Stateless: a pure function of its per-round inputs. upcomingSeq is the
// tx sequence (parent + 1); the local validator's stance is implicit in
// target (getVotes seeds +1 for it). xrpFeesEnabled selects the wire
// format (same algorithm, different SetFee fields).
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

// applyVote routes a field vote into the tally; missing or overflow values
// count as a vote for current (noVote) so one bad field doesn't poison the rest.
func applyVote(v *votableValue, field *uint64) {
	if field == nil || *field > MaxLegalDrops {
		v.noVote()
		return
	}
	v.addVote(*field)
}

// buildSetFeeTx serializes a SetFee pseudo-tx; the field set differs
// between pre- and post-XRPFees wire formats.
func buildSetFeeTx(seq uint32, current, chosen Stance, xrpFeesEnabled bool) ([]byte, error) {
	return common.BuildPseudoTx(tx.TypeFee, func(base tx.BaseTx) tx.Transaction {
		stx := &pseudo.SetFee{
			BaseTx:         base,
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

		return stx
	})
}

// narrowToUint32 returns chosen as uint32, or fallback if it exceeds
// UINT32_MAX (fall back to current rather than truncate). A fallback above
// UINT32_MAX is unreachable — on-chain FeeSettings uint32 fields can't exceed it.
func narrowToUint32(chosen, fallback uint64) uint32 {
	if chosen > math.MaxUint32 {
		return uint32(fallback)
	}
	return uint32(chosen)
}

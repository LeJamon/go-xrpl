// Package feevote decides whether to inject a SetFee pseudo-tx into the
// consensus tx set at a flag-ledger boundary, tallying trusted validators'
// fee votes from the prior voting ledger.
package feevote

import (
	"fmt"
	"math"
	"strconv"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/consensus/common"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
)

// referenceFeeUnitsDeprecated is the legacy sfReferenceFeeUnits stamped on
// every pre-XRPFees SetFee (== 10).
const referenceFeeUnitsDeprecated uint32 = 10

// Stance is the three vote-able fee parameters (in drops), shared by
// "current" and "target". Pre-XRPFees ReserveBase/ReserveIncrement are
// uint32; values above UINT32_MAX fall back to current at emission.
type Stance struct {
	BaseFee          uint64
	ReserveBase      uint64
	ReserveIncrement uint64
}

// Vote is one trusted validator's per-field fee preference. A nil or illegal
// amount is a vote for the current value.
type Vote struct {
	BaseFee          *drops.XRPAmount
	ReserveBase      *drops.XRPAmount
	ReserveIncrement *drops.XRPAmount
}

// votableValue is the per-field tallying state.
type votableValue struct {
	current uint64
	target  uint64
	votes   map[drops.XRPAmount]int
	noVotes int
}

func newVotableValue(current, target uint64) *votableValue {
	return &votableValue{
		current: current,
		target:  target,
		votes:   map[drops.XRPAmount]int{},
	}
}

func (v *votableValue) addVote(value drops.XRPAmount) {
	v.votes[value]++
}

func (v *votableValue) noVote() {
	v.noVotes++
}

// getVotes returns the most-voted value within [min,max](current,target)
// (out-of-window votes are ignored; ties pick the lowest value) and whether
// it differs from current.
func (v *votableValue) getVotes() (uint64, bool) {
	lo, hi := v.current, v.target
	if lo > hi {
		lo, hi = hi, lo
	}

	chosen := v.target
	weight := 1
	if target, ok := signedAmount(v.target); ok {
		weight += v.votes[target]
	}

	currentWeight := v.noVotes
	if current, ok := signedAmount(v.current); ok {
		currentWeight += v.votes[current]
	}
	if currentWeight > weight || (currentWeight == weight && v.current < chosen) {
		chosen = v.current
		weight = currentWeight
	}

	for value, count := range v.votes {
		if value < 0 {
			continue
		}
		candidate := uint64(value)
		if candidate == v.current || candidate == v.target || candidate < lo || candidate > hi {
			continue
		}
		if count > weight || (count == weight && candidate < chosen) {
			chosen = candidate
			weight = count
		}
	}
	return chosen, chosen != v.current
}

func signedAmount(value uint64) (drops.XRPAmount, bool) {
	if value > math.MaxInt64 {
		return 0, false
	}
	return drops.XRPAmount(value), true
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
func applyVote(v *votableValue, field *drops.XRPAmount) {
	if field == nil || *field < -drops.MaxDrops || *field > drops.MaxDrops {
		v.noVote()
		return
	}
	v.addVote(*field)
}

// buildSetFeeTx serializes a SetFee pseudo-tx; the field set differs
// between pre- and post-XRPFees wire formats.
func buildSetFeeTx(seq uint32, current, chosen Stance, xrpFeesEnabled bool) ([]byte, error) {
	var reserveBase, reserveIncrement uint32
	if !xrpFeesEnabled {
		var err error
		reserveBase, err = narrowToUint32(chosen.ReserveBase, current.ReserveBase)
		if err != nil {
			return nil, fmt.Errorf("reserve base: %w", err)
		}
		reserveIncrement, err = narrowToUint32(chosen.ReserveIncrement, current.ReserveIncrement)
		if err != nil {
			return nil, fmt.Errorf("reserve increment: %w", err)
		}
	}

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
			stx.ReserveBase = &reserveBase
			stx.ReserveIncrement = &reserveIncrement
			ref := referenceFeeUnitsDeprecated
			stx.ReferenceFeeUnits = &ref
		}

		return stx
	})
}

// narrowToUint32 returns chosen as uint32, or fallback if chosen does not fit.
func narrowToUint32(chosen, fallback uint64) (uint32, error) {
	if chosen <= math.MaxUint32 {
		return uint32(chosen), nil
	}
	if fallback > math.MaxUint32 {
		return 0, fmt.Errorf("current value %d exceeds uint32", fallback)
	}
	return uint32(fallback), nil
}

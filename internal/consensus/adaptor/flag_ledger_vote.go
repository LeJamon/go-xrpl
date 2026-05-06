package adaptor

import (
	"time"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/amendmentvote"
	"github.com/LeJamon/goXRPLd/internal/consensus/feevote"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx/pseudo"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/protocol"
)

// amendmentMajorityTimeout is how long an amendment must hold
// majority on the ledger before it's enabled. Mainnet config:
// 14 days. Mirrors rippled's default at AmendmentTable.cpp via
// SET_AMENDMENT_MAJORITY_TIME (Application.cpp:1216-1220).
//
// Constant for now — operators don't currently have a knob to
// override it; if that changes the value moves into Config.
const amendmentMajorityTimeout = 14 * 24 * time.Hour

// readAmendmentsSLE pulls the parent ledger's Amendments SLE
// (enabled set + majorities array) once at the producer boundary.
// Both runners consume the result, and the enabled set doubles as
// the feature-flag oracle (rules.Enabled(...) replacement) since
// *ledger.Ledger doesn't carry an amendment.Rules struct.
//
// On read or parse failure, returns empty maps and logs at warn —
// "no amendments enabled" is a safe degradation: it just means
// the legacy / pre-fix code paths are taken.
func (a *Adaptor) readAmendmentsSLE(prev *ledger.Ledger) (
	enabled map[[32]byte]bool,
	majorities map[[32]byte]time.Time,
) {
	enabled = map[[32]byte]bool{}
	majorities = map[[32]byte]time.Time{}

	data, err := prev.Read(keylet.Amendments())
	if err != nil {
		a.logger.Warn("flag-ledger producer: failed to read Amendments SLE",
			"err", err, "seq", prev.Sequence())
		return enabled, majorities
	}
	if len(data) == 0 {
		return enabled, majorities
	}
	sle, err := pseudo.ParseAmendmentsSLE(data)
	if err != nil {
		a.logger.Warn("flag-ledger producer: failed to parse Amendments SLE",
			"err", err, "seq", prev.Sequence())
		return enabled, majorities
	}
	for _, h := range sle.Amendments {
		enabled[h] = true
	}
	for _, m := range sle.Majorities {
		// MajorityEntry.CloseTime is XRPL-epoch seconds — convert
		// to time.Time so the algorithm's
		// majoritySince + MajorityTimeout <= closeTime arithmetic
		// runs over a uniform clock.
		majorities[m.Amendment] = time.Unix(protocol.RippleEpochUnix+int64(m.CloseTime), 0).UTC()
	}
	return enabled, majorities
}

// runFeeVote runs the FeeVote producer against the parent
// ledger's current FeeSettings SLE and the trusted validations
// that referenced it. Returns the serialized SetFee blob (one or
// none) or nil on read/parse failure (logged at warn, treated as
// "abstain" so a single bad SLE doesn't block amendment voting).
func (a *Adaptor) runFeeVote(
	prev *ledger.Ledger,
	upcomingSeq uint32,
	parentValidations []*consensus.Validation,
	enabled map[[32]byte]bool,
) [][]byte {
	feeData, err := prev.Read(keylet.Fees())
	if err != nil {
		a.logger.Warn("flag-ledger fee vote: failed to read FeeSettings SLE",
			"err", err, "seq", prev.Sequence())
		return nil
	}
	if len(feeData) == 0 {
		// No FeeSettings SLE installed yet — pre-genesis-bootstrap
		// or a corrupted state. Either way, no current to vote
		// against; bail.
		return nil
	}
	fees, err := state.ParseFeeSettings(feeData)
	if err != nil {
		a.logger.Warn("flag-ledger fee vote: failed to parse FeeSettings SLE",
			"err", err, "seq", prev.Sequence())
		return nil
	}

	xrpFeesEnabled := enabled[amendment.FeatureXRPFees]

	var current feevote.Stance
	if xrpFeesEnabled {
		current = feevote.Stance{
			BaseFee:          fees.BaseFeeDrops,
			ReserveBase:      fees.ReserveBaseDrops,
			ReserveIncrement: fees.ReserveIncrementDrops,
		}
	} else {
		current = feevote.Stance{
			BaseFee:          fees.BaseFee,
			ReserveBase:      uint64(fees.ReserveBase),
			ReserveIncrement: uint64(fees.ReserveIncrement),
		}
	}

	// Local target stance from the operator config. Zero values
	// mean "no preference" — fall back to current so the algorithm
	// sees a no-change signal for that field.
	target := feevote.Stance{
		BaseFee:          a.feeVote.BaseFee,
		ReserveBase:      uint64(a.feeVote.ReserveBase),
		ReserveIncrement: uint64(a.feeVote.ReserveIncrement),
	}
	if target.BaseFee == 0 {
		target.BaseFee = current.BaseFee
	}
	if target.ReserveBase == 0 {
		target.ReserveBase = current.ReserveBase
	}
	if target.ReserveIncrement == 0 {
		target.ReserveIncrement = current.ReserveIncrement
	}

	votes := make([]feevote.Vote, 0, len(parentValidations))
	for _, v := range parentValidations {
		votes = append(votes, extractFeeVote(v, xrpFeesEnabled))
	}

	blob, err := feevote.DoVoting(upcomingSeq, current, target, votes, xrpFeesEnabled)
	if err != nil {
		a.logger.Warn("flag-ledger fee vote: producer error",
			"err", err, "seq", prev.Sequence())
		return nil
	}
	if blob == nil {
		return nil
	}
	return [][]byte{blob}
}

// extractFeeVote pulls the relevant fee fields off a validation
// into a feevote.Vote. The field set depends on whether the
// XRPFees amendment is enabled on the parent ledger — pre-XRPFees
// uses sfBaseFee / sfReserveBase / sfReserveIncrement; post-XRPFees
// uses the *Drops variants. A zero value on the wire means "field
// not present" (rippled's STValidation never carries an explicit
// zero for these fields), which extractFeeVote translates into a
// nil pointer — feevote.applyVote then routes that to noVote.
func extractFeeVote(v *consensus.Validation, xrpFeesEnabled bool) feevote.Vote {
	var out feevote.Vote
	if xrpFeesEnabled {
		if v.BaseFeeDrops != 0 {
			x := v.BaseFeeDrops
			out.BaseFee = &x
		}
		if v.ReserveBaseDrops != 0 {
			x := v.ReserveBaseDrops
			out.ReserveBase = &x
		}
		if v.ReserveIncrementDrops != 0 {
			x := v.ReserveIncrementDrops
			out.ReserveIncrement = &x
		}
		return out
	}
	if v.BaseFee != 0 {
		x := v.BaseFee
		out.BaseFee = &x
	}
	if v.ReserveBase != 0 {
		x := uint64(v.ReserveBase)
		out.ReserveBase = &x
	}
	if v.ReserveIncrement != 0 {
		x := uint64(v.ReserveIncrement)
		out.ReserveIncrement = &x
	}
	return out
}

// runAmendmentVote runs the AmendmentTable producer against the
// parent ledger's enabled amendments + majorities (already parsed
// at the boundary in readAmendmentsSLE) and the trusted
// validations' sfAmendments. Returns the serialized
// EnableAmendment blobs or nil.
func (a *Adaptor) runAmendmentVote(
	prev *ledger.Ledger,
	upcomingSeq uint32,
	parentValidations []*consensus.Validation,
	enabled map[[32]byte]bool,
	majority map[[32]byte]time.Time,
) [][]byte {
	votes := make(map[amendmentvote.Amendment]int)
	for _, v := range parentValidations {
		for _, h := range v.Amendments {
			votes[h]++
		}
	}

	stances := make(map[amendmentvote.Amendment]amendmentvote.Stance, len(a.amendmentVoteIDs))
	for _, id := range a.amendmentVoteIDs {
		stances[id] = amendmentvote.VoteUp
	}

	in := amendmentvote.Inputs{
		UpcomingSeq:        upcomingSeq,
		CloseTime:          prev.Header().CloseTime,
		MajorityTimeout:    amendmentMajorityTimeout,
		TrustedValidations: len(parentValidations),
		Votes:              votes,
		Enabled:            enabled,
		Majority:           majority,
		Stances:            stances,
		StrictMajority:     enabled[amendment.FeatureFixAmendmentMajorityCalc],
	}
	blobs, err := amendmentvote.DoVoting(in)
	if err != nil {
		a.logger.Warn("flag-ledger amendment vote: producer error",
			"err", err, "seq", prev.Sequence())
		return nil
	}
	return blobs
}

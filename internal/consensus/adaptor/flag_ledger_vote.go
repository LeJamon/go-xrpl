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
const amendmentMajorityTimeout = 14 * 24 * time.Hour

// readAmendmentsSLE pulls the parent ledger's Amendments SLE
// (enabled set + majorities array) once at the producer boundary.
// Both runners consume the result, and the enabled set doubles as
// the feature-flag oracle (rules.Enabled(...) replacement) since
// *ledger.Ledger doesn't carry an amendment.Rules struct.
//
// On read or parse failure returns ok=false; the producer falls
// through to nil. Fail-closed mirrors rippled's
// RCLConsensus.cpp::onClose — there is no try/catch around
// amendmentTable.doVoting, so a malformed Amendments SLE
// propagates the exception and suppresses the round. Treating a
// corrupted SLE as "no amendments enabled" would let GotMajority /
// Enable fire spuriously on every tracked amendment.
//
// A genuinely empty SLE (len(data)==0 — pre-bootstrap genesis
// state) is a successful read of empty state, not corruption, and
// returns ok=true with empty maps.
func (a *Adaptor) readAmendmentsSLE(prev *ledger.Ledger) (
	enabled map[[32]byte]bool,
	majorities map[[32]byte]time.Time,
	ok bool,
) {
	data, err := prev.Read(keylet.Amendments())
	if err != nil {
		a.logger.Warn("flag-ledger producer: failed to read Amendments SLE; suppressing this round",
			"err", err, "seq", prev.Sequence())
		return nil, nil, false
	}
	enabled, majorities, ok = parseAmendmentsSLEBytes(data)
	if !ok {
		a.logger.Warn("flag-ledger producer: failed to parse Amendments SLE; suppressing this round",
			"seq", prev.Sequence())
	}
	return enabled, majorities, ok
}

// parseAmendmentsSLEBytes returns ok=false ONLY on parse failure
// of non-empty data. An empty input (len(data)==0, the
// pre-bootstrap genesis state) is a successful read of empty state
// and returns ok=true with empty maps — matching the rippled
// ledger walk that finds no SLE at the keylet.Amendments() index.
func parseAmendmentsSLEBytes(data []byte) (
	enabled map[[32]byte]bool,
	majorities map[[32]byte]time.Time,
	ok bool,
) {
	enabled = map[[32]byte]bool{}
	majorities = map[[32]byte]time.Time{}
	if len(data) == 0 {
		return enabled, majorities, true
	}
	sle, err := pseudo.ParseAmendmentsSLE(data)
	if err != nil {
		return nil, nil, false
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
	return enabled, majorities, true
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

	// Local target stance from the operator config. Each field is
	// guaranteed non-zero here — adaptor.New() substituted the
	// rippled FeeSetup defaults (Config.h:65-78) for any field the
	// operator left unset. We deliberately do NOT fall back to
	// `current` for zero fields: rippled's FeeVoteImpl.cpp:114-117
	// constructor takes the supplied FeeSetup verbatim and never
	// re-defaults at doVoting time, so an operator who somehow
	// supplied a zero (e.g. via a bug elsewhere) should produce a
	// zero vote, not silently inherit the parent ledger's setting.
	target := feevote.Stance{
		BaseFee:          a.feeVote.BaseFee,
		ReserveBase:      uint64(a.feeVote.ReserveBase),
		ReserveIncrement: uint64(a.feeVote.ReserveIncrement),
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
//
// Vote tallies are routed through a.trustedVotes — a 24h
// per-validator cache mirroring rippled's TrustedVotes at
// AmendmentTable.cpp:75-286 — so a validator that drops briefly
// near a flag ledger doesn't cause an amendment to flap between
// GotMajority and LostMajority across consecutive rounds. Both
// TrustedValidations (the threshold denominator) and Votes flow
// from the cache; the raw parentValidations slice is fed into
// the cache via RecordVotes and not used afterwards.
func (a *Adaptor) runAmendmentVote(
	prev *ledger.Ledger,
	upcomingSeq uint32,
	parentValidations []*consensus.Validation,
	enabled map[[32]byte]bool,
	majority map[[32]byte]time.Time,
) [][]byte {
	// Use prev's parent close time, not prev's own close time:
	// rippled passes lastClosedLedger->parentCloseTime() into
	// AmendmentTable::doVoting (AmendmentTable.h:157), which is the
	// close time of the ledger whose validations we're tallying
	// (parentValidations come from prev's parent). Pairing the
	// validations with prev's close time would drift the 24h
	// trusted-vote cache expiry and the majority-window enable
	// check by one round.
	closeTime := prev.Header().ParentCloseTime
	a.trustedVotes.RecordVotes(closeTime, parentValidations)
	available, rawVotes := a.trustedVotes.GetVotes()

	votes := make(map[amendmentvote.Amendment]int, len(rawVotes))
	for k, v := range rawVotes {
		votes[k] = v
	}

	stances := make(map[amendmentvote.Amendment]amendmentvote.Stance, len(a.amendmentStances))
	for id, stance := range a.amendmentStances {
		stances[id] = stance
	}

	in := amendmentvote.Inputs{
		UpcomingSeq:        upcomingSeq,
		CloseTime:          closeTime,
		MajorityTimeout:    amendmentMajorityTimeout,
		TrustedValidations: available,
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

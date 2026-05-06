// Package amendmentvote ports rippled's AmendmentTableImpl::doVoting
// (src/xrpld/app/misc/detail/AmendmentTable.cpp:847-941) — the
// producer side that decides whether to inject EnableAmendment
// pseudo-txs into the consensus tx set on a flag-ledger boundary.
//
// The algorithm:
//
//  1. Compute the validator threshold from trustedValidations.
//     Pre-fixAmendmentMajorityCalc: 204/256 ≈ 79.7%; post-fix:
//     80/100 = 80%. threshold = max(1, trustedValidations × frac).
//
//  2. For every amendment the local server knows about that isn't
//     already enabled, classify against three signals:
//
//     - hasValMajority — did votes ≥ threshold (lax) or > threshold
//       (strict, post-fixAmendmentMajorityCalc)? With exactly one
//       trusted validator, both modes degrade to ≥.
//     - hasLedgerMajority — is the amendment recorded in the
//       parent ledger's majority list (the sfMajorities SLE)?
//     - vote — is this server voting yes locally (Stance == Up)?
//
//  3. Emit one of three actions, mirroring AmendmentTable.cpp:902-924:
//
//     - tfGotMajority — validators say yes, ledger doesn't yet
//       record majority, AND we're voting yes locally.
//     - tfLostMajority — ledger records majority but validators
//       fell off (regardless of local stance).
//     - 0 (enable) — ledger records majority, the timer has
//       expired (majorityTime + majorityTimeout ≤ closeTime), AND
//       we're voting yes locally.
//
//     All other classifications produce no action.
//
//  4. Each entry is serialized as an EnableAmendment pseudo-tx
//     with sfAmendment, sfLedgerSequence, and sfFlags (omitted
//     when 0; rippled writes only when non-zero per
//     AmendmentTable.h:172-174).
package amendmentvote

import (
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/pseudo"
)

const (
	// PreFixThresholdNum / PreFixThresholdDen express the
	// pre-fixAmendmentMajorityCalc threshold fraction (204/256 ≈
	// 79.69%). Mirrors rippled SystemParameters.h:81.
	PreFixThresholdNum = 204
	PreFixThresholdDen = 256

	// PostFixThresholdNum / PostFixThresholdDen express the
	// post-fixAmendmentMajorityCalc threshold fraction (80/100 =
	// 80%). Mirrors rippled SystemParameters.h:83.
	PostFixThresholdNum = 80
	PostFixThresholdDen = 100

	// TfGotMajority / TfLostMajority are the EnableAmendment
	// sfFlags values that signal a state-change pseudo-tx (as
	// opposed to an enable, which carries no flags). Mirrors
	// rippled TxFlags.h:128-129.
	TfGotMajority  uint32 = 0x00010000
	TfLostMajority uint32 = 0x00020000
)

// Amendment is the 32-byte hash uniquely identifying an amendment.
type Amendment = [32]byte

// Stance is the local server's voting stance toward an amendment.
// Mirrors rippled's AmendmentVote enum.
type Stance int

const (
	// VoteAbstain is the default — the server has no opinion. Used
	// for amendments rippled is aware of but the operator hasn't
	// taken a position on. Mirrors AmendmentVote::down — abstaining
	// counts as "no" for tally purposes.
	VoteAbstain Stance = iota
	// VoteUp — the server actively votes yes. Required for the
	// gotMajority and enable actions; the lostMajority action does
	// not require it (it fires regardless of local stance, because
	// the ledger needs to record the majority loss either way).
	VoteUp
	// VoteObsolete — vetoed locally; never propose enable, never
	// propose gotMajority. Mirrors AmendmentVote::obsolete.
	VoteObsolete
)

// Inputs aggregates everything DoVoting needs to decide. The
// caller resolves rules / per-ledger state into these primitive
// shapes — the algorithm itself is pure and testable in isolation.
type Inputs struct {
	// UpcomingSeq is the sequence the EnableAmendment tx will
	// carry (parent + 1).
	UpcomingSeq uint32

	// CloseTime is the parent ledger's close time. Used as the
	// "now" reference for the majority-held-long-enough check.
	// Mirrors AmendmentTable.cpp:849.
	CloseTime time.Time

	// MajorityTimeout is the duration an amendment must hold
	// majority on the ledger before it can be enabled. Mainnet:
	// 14 days. Mirrors majorityTime_ at AmendmentTable.cpp:423.
	MajorityTimeout time.Duration

	// TrustedValidations is the count of trusted validators whose
	// votes are tallied this round. Used to compute the threshold.
	TrustedValidations int

	// Votes counts trusted upVotes per amendment. Mirrors the
	// AmendmentSet::votes_ map at AmendmentTable.cpp:318.
	Votes map[Amendment]int

	// Enabled is the set of amendments already enabled on the
	// parent ledger (from the sfAmendments SLE). Already-enabled
	// amendments are skipped at AmendmentTable.cpp:877-882.
	Enabled map[Amendment]bool

	// Majority maps amendment → time it gained majority on the
	// ledger (from the sfMajorities SLE). Empty time.Time means
	// "no entry"; this matches AmendmentTable.cpp:886-893's
	// std::optional handling.
	Majority map[Amendment]time.Time

	// Stances is this server's per-amendment voting stance, keyed
	// by amendment hash. Amendments not in the map default to
	// VoteAbstain. Mirrors AmendmentState::vote at
	// AmendmentTable.cpp:286.
	Stances map[Amendment]Stance

	// StrictMajority is true once fixAmendmentMajorityCalc is
	// enabled. Selects the post-fix threshold fraction (80/100 vs
	// 204/256) AND switches the passes-comparison from ≥ to >.
	// Mirrors AmendmentTable.cpp:328-340 + 372-376.
	StrictMajority bool
}

// Decision is one entry of the doVoting return map at
// AmendmentTable.cpp:872. Flags is 0 (enable), TfGotMajority, or
// TfLostMajority.
type Decision struct {
	Amendment Amendment
	Flags     uint32
}

// Threshold returns the vote count an amendment needs to pass.
// Pre-fix: (trusted * 204) / 256. Post-fix: (trusted * 80) / 100.
// Both clamp to a minimum of 1 to keep the gate reachable on tiny
// validator sets. Mirrors AmendmentTable.cpp:325-341.
func Threshold(trustedValidations int, strict bool) int {
	num, den := PreFixThresholdNum, PreFixThresholdDen
	if strict {
		num, den = PostFixThresholdNum, PostFixThresholdDen
	}
	t := (trustedValidations * num) / den
	if t < 1 {
		return 1
	}
	return t
}

// passes is the per-amendment quorum check. With exactly one
// trusted validator, both pre-fix and post-fix degrade to ≥
// (otherwise the gate would be unreachable). Otherwise post-fix
// uses strict >. Mirrors AmendmentTable.cpp:359-377.
func passes(votes, threshold, trustedValidations int, strict bool) bool {
	if !strict || trustedValidations == 1 {
		return votes >= threshold
	}
	return votes > threshold
}

// Decide is the pure-algorithm step. Returns a deterministic,
// hash-sorted slice of Decisions — at most one per amendment —
// classifying each tracked amendment as gotMajority / lostMajority
// / enable, or omitting it entirely. Result order is stable
// across runs to keep the tx-set hash deterministic; rippled's
// std::map iterates in hash-key order, so we match by sorting on
// amendment bytes.
func Decide(in Inputs) []Decision {
	threshold := Threshold(in.TrustedValidations, in.StrictMajority)

	// Walk every amendment the server is aware of (Stances ∪
	// Votes ∪ Majority). An amendment with no Stance entry is
	// treated as VoteAbstain.
	seen := make(map[Amendment]struct{}, len(in.Stances)+len(in.Votes)+len(in.Majority))
	for k := range in.Stances {
		seen[k] = struct{}{}
	}
	for k := range in.Votes {
		seen[k] = struct{}{}
	}
	for k := range in.Majority {
		seen[k] = struct{}{}
	}

	var out []Decision
	for amendment := range seen {
		if in.Enabled[amendment] {
			// Already enabled — never produces a pseudo-tx.
			// Mirrors AmendmentTable.cpp:877-882.
			continue
		}

		stance := in.Stances[amendment]
		votes := in.Votes[amendment]
		hasValMajority := passes(votes, threshold, in.TrustedValidations, in.StrictMajority)
		majoritySince, hasLedgerMajority := in.Majority[amendment]

		switch {
		case hasValMajority && !hasLedgerMajority && stance == VoteUp:
			// Validators say yes; ledger doesn't record majority
			// yet; we vote yes locally. Inject GotMajority so the
			// ledger starts the majority-held timer.
			// AmendmentTable.cpp:902-909.
			out = append(out, Decision{Amendment: amendment, Flags: TfGotMajority})

		case !hasValMajority && hasLedgerMajority:
			// Ledger records majority, validators fell off.
			// Inject LostMajority regardless of local stance —
			// the ledger needs to clear the timer either way.
			// AmendmentTable.cpp:910-915.
			out = append(out, Decision{Amendment: amendment, Flags: TfLostMajority})

		case hasLedgerMajority &&
			!majoritySince.Add(in.MajorityTimeout).After(in.CloseTime) &&
			stance == VoteUp:
			// Majority has held on the ledger for at least
			// MajorityTimeout; we still vote yes locally; emit the
			// enable pseudo-tx. AmendmentTable.cpp:916-924.
			out = append(out, Decision{Amendment: amendment, Flags: 0})

		default:
			// Logging-only branches in rippled — no pseudo-tx.
			// AmendmentTable.cpp:926-935.
		}
	}

	// Stable hash-key order so the tx-set hash is deterministic.
	sort.Slice(out, func(i, j int) bool {
		return lessAmendment(out[i].Amendment, out[j].Amendment)
	})
	return out
}

func lessAmendment(a, b Amendment) bool {
	for i := 0; i < 32; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// DoVoting runs Decide and serializes each Decision as an
// EnableAmendment pseudo-tx wire blob. Returns nil when no
// pseudo-txs apply.
func DoVoting(in Inputs) ([][]byte, error) {
	decisions := Decide(in)
	if len(decisions) == 0 {
		return nil, nil
	}
	out := make([][]byte, 0, len(decisions))
	for _, d := range decisions {
		blob, err := buildEnableAmendmentTx(in.UpcomingSeq, d.Amendment, d.Flags)
		if err != nil {
			return nil, fmt.Errorf("amendmentvote: serialize %s: %w",
				hex.EncodeToString(d.Amendment[:8]), err)
		}
		out = append(out, blob)
	}
	return out, nil
}

// buildEnableAmendmentTx serializes an EnableAmendment pseudo-tx.
// Wire format mirrors AmendmentTable.h:165-187: zero account, zero
// fee, empty signing key, sequence 0; sfAmendment carries the
// 32-byte hash; sfLedgerSequence carries the upcoming seq; sfFlags
// is set only when non-zero (got/lost majority).
func buildEnableAmendmentTx(seq uint32, amendment Amendment, flags uint32) ([]byte, error) {
	etx := &pseudo.EnableAmendment{
		BaseTx:         *tx.NewBaseTx(tx.TypeAmendment, pseudo.ZeroAccount),
		Amendment:      hex.EncodeToString(amendment[:]),
		LedgerSequence: &seq,
	}
	if flags != 0 {
		f := flags
		etx.Common.Flags = &f
	}
	return pseudo.EncodePseudoTx(etx)
}

// Package amendmentvote decides whether to inject EnableAmendment
// pseudo-txs into the consensus tx set at a flag-ledger boundary.
// Mirrors rippled AmendmentTableImpl::doVoting (AmendmentTable.cpp:847-941).
package amendmentvote

import (
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus/common"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
)

const (
	// pre-fixAmendmentMajorityCalc threshold fraction (204/256 ≈ 79.69%)
	PreFixThresholdNum = 204
	PreFixThresholdDen = 256

	// post-fixAmendmentMajorityCalc threshold fraction (80/100)
	PostFixThresholdNum = 80
	PostFixThresholdDen = 100

	// EnableAmendment sfFlags for a state-change pseudo-tx (enable carries none)
	TfGotMajority  uint32 = 0x00010000
	TfLostMajority uint32 = 0x00020000
)

// Amendment is the 32-byte hash uniquely identifying an amendment.
type Amendment = [32]byte

// Stance is the local server's vote toward an amendment.
type Stance int

const (
	// default: no opinion, counts as "no" for tally
	VoteAbstain Stance = iota
	// actively votes yes; required for gotMajority and enable (not lostMajority)
	VoteUp
	// vetoed locally; never propose enable or gotMajority
	VoteObsolete
)

// Inputs aggregates everything DoVoting needs; the algorithm is pure.
type Inputs struct {
	// sequence the EnableAmendment tx will carry (parent + 1)
	UpcomingSeq uint32

	// parent ledger close time; the "now" for the majority-held check
	CloseTime time.Time

	// how long majority must hold before enable (mainnet 14 days)
	MajorityTimeout time.Duration

	// count of trusted validators tallied this round; sets the threshold
	TrustedValidations int

	// trusted up-votes per amendment
	Votes map[Amendment]int

	// amendments already enabled on the parent ledger (skipped)
	Enabled map[Amendment]bool

	// amendment → time it gained ledger majority; zero time means no entry
	Majority map[Amendment]time.Time

	// this server's per-amendment stance; absent defaults to VoteAbstain
	Stances map[Amendment]Stance

	// amendments this server supports — the walk domain for Decide.
	// Includes DefaultYes/DefaultNo/Obsolete but not unsupported, so a
	// supported-but-down amendment can still emit LostMajority. Nil walks
	// the union of Stances/Votes/Majority (test-only; inputs assumed known).
	Known map[Amendment]bool

	// fixAmendmentMajorityCalc: post-fix fraction (80/100), switches ≥ to >
	StrictMajority bool
}

// Decision is one voting outcome; Flags is 0 (enable), TfGotMajority, or TfLostMajority.
type Decision struct {
	Amendment Amendment
	Flags     uint32
}

// Threshold is the vote count an amendment needs to pass, clamped to a
// minimum of 1 so the gate stays reachable on tiny validator sets.
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

// passes is the per-amendment quorum check. A single trusted validator
// degrades to ≥ (else unreachable); otherwise post-fix uses strict >.
func passes(votes, threshold, trustedValidations int, strict bool) bool {
	if !strict || trustedValidations == 1 {
		return votes >= threshold
	}
	return votes > threshold
}

// Decide classifies each tracked amendment as gotMajority/lostMajority/
// enable (or omits it). Hash-sorted so the tx-set hash is deterministic.
func Decide(in Inputs) []Decision {
	threshold := Threshold(in.TrustedValidations, in.StrictMajority)

	// Walk domain: Known when supplied, else the union of the input maps.
	var seen map[Amendment]struct{}
	if in.Known != nil {
		seen = make(map[Amendment]struct{}, len(in.Known))
		for k := range in.Known {
			seen[k] = struct{}{}
		}
	} else {
		seen = make(map[Amendment]struct{}, len(in.Stances)+len(in.Votes)+len(in.Majority))
		for k := range in.Stances {
			seen[k] = struct{}{}
		}
		for k := range in.Votes {
			seen[k] = struct{}{}
		}
		for k := range in.Majority {
			seen[k] = struct{}{}
		}
	}

	var out []Decision
	for amendment := range seen {
		if in.Enabled[amendment] {
			// already enabled — never produces a pseudo-tx
			continue
		}

		stance := in.Stances[amendment]
		votes := in.Votes[amendment]
		hasValMajority := passes(votes, threshold, in.TrustedValidations, in.StrictMajority)
		majoritySince, hasLedgerMajority := in.Majority[amendment]

		switch {
		case hasValMajority && !hasLedgerMajority && stance == VoteUp:
			// validators yes, ledger not yet recording, local yes → start the majority timer
			out = append(out, Decision{Amendment: amendment, Flags: TfGotMajority})

		case !hasValMajority && hasLedgerMajority:
			// ledger records majority but validators fell off — clear the timer (any stance)
			out = append(out, Decision{Amendment: amendment, Flags: TfLostMajority})

		case hasLedgerMajority &&
			!majoritySince.Add(in.MajorityTimeout).After(in.CloseTime) &&
			stance == VoteUp:
			// majority held ≥ MajorityTimeout and local yes → emit enable
			out = append(out, Decision{Amendment: amendment, Flags: 0})

		default:
			// logging-only in rippled — no pseudo-tx
		}
	}

	// Stable hash-key order so the tx-set hash is deterministic.
	sort.Slice(out, func(i, j int) bool {
		return lessAmendment(out[i].Amendment, out[j].Amendment)
	})
	return out
}

func lessAmendment(a, b Amendment) bool {
	for i := range 32 {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// DoVoting runs Decide and serializes each Decision as an EnableAmendment
// pseudo-tx blob. Returns nil when no pseudo-txs apply. Stateless: a pure
// function of the per-round Inputs.
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

// buildEnableAmendmentTx serializes an EnableAmendment pseudo-tx: zero
// account/fee/key, sequence 0; sfFlags set only when non-zero.
func buildEnableAmendmentTx(seq uint32, amendment Amendment, flags uint32) ([]byte, error) {
	return common.BuildPseudoTx(tx.TypeAmendment, func(base tx.BaseTx) tx.Transaction {
		etx := &pseudo.EnableAmendment{
			BaseTx:         base,
			Amendment:      hex.EncodeToString(amendment[:]),
			LedgerSequence: &seq,
		}
		if flags != 0 {
			f := flags
			etx.Common.Flags = &f
		}
		return etx
	})
}

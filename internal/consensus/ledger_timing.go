package consensus

// This file ports rippled's close-time-resolution binning from
// src/xrpld/consensus/LedgerTiming.h (in particular
// getNextLedgerTimeResolution at lines 80-122).
//
// Algorithm recap (rippled LedgerTiming.h:55-122):
//
//   - Resolutions are drawn from a fixed ordered bin set
//     (ledgerPossibleTimeResolutions): 10s, 20s, 30s, 60s, 90s, 120s.
//     Index 0 is the FINEST (most precise) bin; index len-1 is the
//     COARSEST (widest bin).
//   - Each new ledger starts from the parent's resolution. Depending
//     on whether the prior round agreed on close time, and the new
//     ledger's sequence number, we optionally step one slot coarser
//     (towards index len-1, wider bins — "decreasing resolution" in
//     the common-english sense of the header comments) or one slot
//     finer (towards index 0, narrower bins — "increasing
//     resolution").
//   - If !previousAgree && seq % decreaseLedgerTimeResolutionEvery (1)
//     == 0: try to step coarser (++iter in rippled). This widens the
//     rounding bin so that peers with slightly different clocks are
//     more likely to round to the same value and agree.
//   - If previousAgree && seq % increaseLedgerTimeResolutionEvery (8)
//     == 0: try to step finer (--iter in rippled). Peers were
//     agreeing; see if we can tighten the bin.
//   - At the extremes (iter == begin / end), the step is refused and
//     previousResolution is returned unchanged.
//
// The terminology is confusing: rippled comments use "increase
// resolution" in the human sense (finer → smaller seconds), but the
// array is sorted by seconds-per-bin ascending, so "finer" means
// moving to a smaller array index. We preserve rippled's constant
// names (increaseLedgerTimeResolutionEvery / decreaseLedger...)
// exactly so cross-references to LedgerTiming.h are unambiguous.
//
// Rippled parity test (LedgerTiming_test.cpp):
//   - Starting at idx 2 (30s), previousAgree=false for 10 rounds:
//     expect 3 steps coarser (→ 60s, 90s, 120s), then 7 rounds
//     equal (saturates at max).
//
// Reference: rippled/src/xrpld/consensus/LedgerTiming.h:30-122.

// ledgerPossibleTimeResolutions lists the valid close-time-resolution
// bin widths, in seconds, finest (smallest) to coarsest (largest).
// Matches rippled LedgerTiming.h:35-41.
var ledgerPossibleTimeResolutions = []uint32{10, 20, 30, 60, 90, 120}

// LedgerDefaultTimeResolution is the starting resolution for an
// unconstrained ledger (rippled's ledgerPossibleTimeResolutions[2]).
const LedgerDefaultTimeResolution uint32 = 30

// LedgerGenesisTimeResolution is the resolution used for the genesis
// ledger (rippled's ledgerPossibleTimeResolutions[0]).
const LedgerGenesisTimeResolution uint32 = 10

// increaseLedgerTimeResolutionEvery: every N ledgers, if the prior
// round agreed, try to step to a FINER bin (smaller seconds).
// Matches rippled LedgerTiming.h:50.
const increaseLedgerTimeResolutionEvery uint32 = 8

// decreaseLedgerTimeResolutionEvery: every N ledgers, if the prior
// round did NOT agree, try to step to a COARSER bin (larger
// seconds). Matches rippled LedgerTiming.h:53.
const decreaseLedgerTimeResolutionEvery uint32 = 1

// FlagLedgerInterval is the period (in ledgers) on which validators
// vote on amendments and fee/reserve changes. Matches rippled's
// FLAG_LEDGER_INTERVAL constant (Ledger.cpp).
const FlagLedgerInterval uint32 = 256

// IsFlagLedger reports whether the ledger at the given sequence is a
// flag ledger — the ledger on which fee and amendment vote results
// take effect. Matches rippled Ledger.cpp `isFlagLedger`: true when
// seq is non-zero and divisible by FlagLedgerInterval.
//
// Pseudo-txs from a flag ledger's voting cycle are injected into the
// ledger AFTER the flag ledger; rippled gates this on
// `prevLedger.isFlagLedger()` in RCLConsensus.cpp:354.
func IsFlagLedger(seq uint32) bool {
	return seq != 0 && seq%FlagLedgerInterval == 0
}

// IsVotingLedger reports whether the validation for the ledger at the
// given sequence carries fee-vote and amendment-vote fields — i.e.
// the ledger BEFORE a flag ledger. Matches rippled Ledger.cpp:
// (seq + 1) % FlagLedgerInterval == 0.
//
// In rippled this also gates NegativeUNL pseudo-tx injection at
// RCLConsensus.cpp:368-380 (when prevLedger is a voting ledger the
// upcoming ledger is a flag ledger and NegUNL changes apply).
func IsVotingLedger(seq uint32) bool {
	return (seq+1)%FlagLedgerInterval == 0
}

// GetNextLedgerTimeResolution returns the close-time resolution, in
// seconds, for the ledger at sequence newLedgerSeq, given the parent
// ledger's resolution and whether the prior consensus round agreed
// on close time.
//
// Contract:
//   - parentResolution MUST be one of the valid bin widths in
//     ledgerPossibleTimeResolutions. If it isn't (bug or corruption),
//     parentResolution is returned unchanged — matching rippled's
//     "precaution" branch at LedgerTiming.h:100-101.
//   - newLedgerSeq MUST be non-zero. Rippled asserts this at
//     LedgerTiming.h:85-87; in Go we silently tolerate it (returning
//     the parent resolution), since the helper is pure and the
//     constraint is enforced by callers (new ledgers always have
//     seq ≥ 2 — genesis is seq 1 and never passes through here).
//
// Parity: rippled LedgerTiming.h:78-122.
func GetNextLedgerTimeResolution(parentResolution uint32, previousAgree bool, newLedgerSeq uint32) uint32 {
	if newLedgerSeq == 0 {
		return parentResolution
	}

	// Locate parentResolution in the bin array. If not found, bail out
	// with parentResolution (rippled LedgerTiming.h:100-101).
	idx := -1
	for i, r := range ledgerPossibleTimeResolutions {
		if r == parentResolution {
			idx = i
			break
		}
	}
	if idx < 0 {
		return parentResolution
	}

	// Previous round did NOT agree: try to step coarser (larger bin).
	// rippled LedgerTiming.h:105-110.
	if !previousAgree && newLedgerSeq%decreaseLedgerTimeResolutionEvery == 0 {
		if idx+1 < len(ledgerPossibleTimeResolutions) {
			return ledgerPossibleTimeResolutions[idx+1]
		}
	}

	// Previous round DID agree: try to step finer (smaller bin).
	// rippled LedgerTiming.h:113-119. Note: if idx == 0 (already
	// finest), rippled's post-decrement "iter-- != begin" check
	// fails and we fall through, preserving the parent resolution.
	if previousAgree && newLedgerSeq%increaseLedgerTimeResolutionEvery == 0 {
		if idx > 0 {
			return ledgerPossibleTimeResolutions[idx-1]
		}
	}

	return parentResolution
}

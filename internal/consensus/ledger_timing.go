package consensus

// ledgerPossibleTimeResolutions lists the valid close-time-resolution
// bin widths, in seconds, finest (smallest) to coarsest (largest).
var ledgerPossibleTimeResolutions = []uint32{10, 20, 30, 60, 90, 120}

// LedgerDefaultTimeResolution is the starting resolution for an
// unconstrained ledger: 30s, the middle bin.
const LedgerDefaultTimeResolution uint32 = 30

// increaseLedgerTimeResolutionEvery: every N ledgers, if the prior
// round agreed, try to step to a FINER bin (smaller seconds).
const increaseLedgerTimeResolutionEvery uint32 = 8

// decreaseLedgerTimeResolutionEvery: every N ledgers, if the prior
// round did NOT agree, try to step to a COARSER bin (larger seconds).
const decreaseLedgerTimeResolutionEvery uint32 = 1

// FlagLedgerInterval is the period (in ledgers) on which validators
// vote on amendments and fee/reserve changes. Matches rippled's
// FLAG_LEDGER_INTERVAL constant at Ledger.h:426.
const FlagLedgerInterval uint32 = 256

// IsFlagLedger reports whether the ledger at the given sequence is a
// flag ledger — the ledger on which fee and amendment vote results
// take effect. Matches rippled's free `isFlagLedger(LedgerIndex seq)`
// at Ledger.cpp:957-959 and the member `Ledger::isFlagLedger()` at
// Ledger.cpp:946-948 exactly: `seq % FLAG_LEDGER_INTERVAL == 0`,
// with no special-casing of seq=0.
//
// Pseudo-txs from a flag ledger's voting cycle are injected into the
// ledger AFTER the flag ledger; rippled gates this on
// `prevLedger.isFlagLedger()` at RCLConsensus.cpp:354.
func IsFlagLedger(seq uint32) bool {
	return seq%FlagLedgerInterval == 0
}

// IsVotingLedger reports whether the validation for the ledger at the
// given sequence carries fee-vote and amendment-vote fields — i.e.
// the ledger BEFORE a flag ledger. Matches rippled's
// `Ledger::isVotingLedger()` at Ledger.cpp:950-953:
// `(seq + 1) % FLAG_LEDGER_INTERVAL == 0`.
//
// In rippled this also gates NegativeUNL pseudo-tx injection at
// RCLConsensus.cpp:368-380 (when prevLedger is a voting ledger the
// upcoming ledger is a flag ledger and NegUNL changes apply).
func IsVotingLedger(seq uint32) bool {
	return (seq+1)%FlagLedgerInterval == 0
}

// GetNextLedgerTimeResolution returns the close-time resolution, in
// seconds, for the ledger at sequence newLedgerSeq, given the parent
// ledger's resolution and whether the prior consensus round agreed on
// close time.
//
// Widening the bin (when the prior round disagreed) makes peers with
// slightly different clocks more likely to round to the same close
// time and agree; narrowing (when they agreed) tightens precision.
// Stepping is refused at the finest and coarsest bins, leaving the
// resolution unchanged.
//
// Contract:
//   - parentResolution MUST be one of the valid bin widths in
//     ledgerPossibleTimeResolutions. If it isn't, parentResolution is
//     returned unchanged.
//   - newLedgerSeq MUST be non-zero; callers guarantee this (new
//     ledgers always have seq >= 2, genesis being seq 1). A zero seq
//     returns the parent resolution unchanged.
func GetNextLedgerTimeResolution(parentResolution uint32, previousAgree bool, newLedgerSeq uint32) uint32 {
	if newLedgerSeq == 0 {
		return parentResolution
	}

	// Locate parentResolution in the bin array; if absent, return it
	// unchanged.
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

	// Disagreed: try to step to a coarser (wider) bin.
	if !previousAgree && newLedgerSeq%decreaseLedgerTimeResolutionEvery == 0 {
		if idx+1 < len(ledgerPossibleTimeResolutions) {
			return ledgerPossibleTimeResolutions[idx+1]
		}
	}

	// Agreed: try to step to a finer (narrower) bin. At the finest bin
	// (idx 0) the step is refused and the parent resolution stands.
	if previousAgree && newLedgerSeq%increaseLedgerTimeResolutionEvery == 0 {
		if idx > 0 {
			return ledgerPossibleTimeResolutions[idx-1]
		}
	}

	return parentResolution
}

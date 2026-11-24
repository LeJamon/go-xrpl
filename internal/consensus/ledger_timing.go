package consensus

import (
	"time"

	"github.com/LeJamon/go-xrpl/protocol"
)

// ledgerPossibleTimeResolutions lists valid close-time-resolution bin widths,
// in seconds, finest to coarsest.
var ledgerPossibleTimeResolutions = []uint32{10, 20, 30, 60, 90, 120}

// LedgerDefaultTimeResolution is the starting close-time resolution: 30s, the
// middle bin.
const LedgerDefaultTimeResolution uint32 = 30

// increaseLedgerTimeResolutionEvery: every N agreeing rounds, step to a finer bin.
const increaseLedgerTimeResolutionEvery uint32 = 8

// decreaseLedgerTimeResolutionEvery: every N disagreeing rounds, step to a coarser bin.
const decreaseLedgerTimeResolutionEvery uint32 = 1

// GetNextLedgerTimeResolution returns the close-time resolution (seconds) for
// newLedgerSeq, given the parent's resolution and whether the prior round
// agreed. Widening (on disagreement) lets clock-skewed peers round to the same
// close time; narrowing (on agreement) tightens precision. Stepping is refused
// at the finest and coarsest bins. A parentResolution not in the bin set, or a
// zero newLedgerSeq, returns parentResolution unchanged.
func GetNextLedgerTimeResolution(parentResolution uint32, previousAgree bool, newLedgerSeq uint32) uint32 {
	if newLedgerSeq == 0 {
		return parentResolution
	}

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

	// Disagreed: step to a coarser (wider) bin.
	if !previousAgree && newLedgerSeq%decreaseLedgerTimeResolutionEvery == 0 {
		if idx+1 < len(ledgerPossibleTimeResolutions) {
			return ledgerPossibleTimeResolutions[idx+1]
		}
	}

	// Agreed: step to a finer (narrower) bin; refused at the finest (idx 0).
	if previousAgree && newLedgerSeq%increaseLedgerTimeResolutionEvery == 0 {
		if idx > 0 {
			return ledgerPossibleTimeResolutions[idx-1]
		}
	}

	return parentResolution
}

// RoundCloseTime rounds to the nearest multiple of resolution (up at the
// midpoint). Truncates sub-second precision first so nanosecond-skewed
// validators round identically; does the modulo in XRPL-epoch space to
// match rippled byte-for-byte.
func RoundCloseTime(closeTime time.Time, resolution time.Duration) time.Time {
	if closeTime.IsZero() {
		return closeTime
	}
	resSec := int64(resolution.Seconds())
	if resSec <= 0 {
		return closeTime
	}
	xrplSec := closeTime.Unix() - protocol.RippleEpochUnix
	xrplSec += resSec / 2
	xrplSec -= xrplSec % resSec
	return time.Unix(xrplSec+protocol.RippleEpochUnix, 0).UTC()
}

// EffCloseTime rounds to resolution, then floors at priorCloseTime + 1s.
func EffCloseTime(closeTime time.Time, resolution time.Duration, priorCloseTime time.Time) time.Time {
	if closeTime.IsZero() {
		return closeTime
	}
	rounded := RoundCloseTime(closeTime, resolution)
	minTime := priorCloseTime.Add(time.Second)
	if rounded.Before(minTime) {
		return minTime
	}
	return rounded
}

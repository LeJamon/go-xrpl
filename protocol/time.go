package protocol

import (
	"time"
)

const closeTimeISOLayout = "2006-01-02T15:04:05Z"
const closeTimeHumanLayout = "2006-Jan-02 15:04:05 UTC"

// RippleSeconds converts a wall-clock time to seconds since the XRPL epoch.
// Go's zero time encodes as zero.
func RippleSeconds(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix() - RippleEpochUnix
}

// ToRippleTime converts a wall-clock time to the uint32 XRPL network-time
// representation. Values above its range retain uint32 truncation semantics.
func ToRippleTime(t time.Time) uint32 {
	return uint32(RippleSeconds(t))
}

// FromRippleTime converts XRPL network time to UTC. A value of zero is the
// XRPL epoch, 2000-01-01T00:00:00Z.
func FromRippleTime(seconds uint32) time.Time {
	return time.Unix(RippleEpochUnix+int64(seconds), 0).UTC()
}

// FormatCloseTimeISO formats a ledger close time as UTC with whole-second
// precision.
func FormatCloseTimeISO(t time.Time) string {
	if t.IsZero() {
		t = FromRippleTime(0)
	}
	return t.UTC().Format(closeTimeISOLayout)
}

// FormatCloseTimeHuman formats a ledger close time in the human-readable
// whole-second form used by XRPL ledger responses.
func FormatCloseTimeHuman(t time.Time) string {
	if t.IsZero() {
		t = FromRippleTime(0)
	}
	return t.UTC().Format(closeTimeHumanLayout)
}

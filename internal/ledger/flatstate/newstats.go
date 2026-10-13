package flatstate

import "time"

// Stats is a point-in-time, in-memory snapshot of flat-state operational
// counters. The values are maintained by Store as transitions complete; taking
// a snapshot never reads Pebble.
type Stats struct {
	MemTableTargetBytes int64
	OutboxCount         int
	OutboxBytes         int64
	OutboxAge           time.Duration
	Advancing           bool
	Failures            uint64
}

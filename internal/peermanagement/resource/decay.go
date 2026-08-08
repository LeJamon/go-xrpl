package resource

import "time"

// decayingSample is an exponentially-decaying sample over a fixed
// window. Mirrors basics::DecayingSample<Window, Clock> at
// rippled/include/xrpl/basics/DecayingSample.h.
type decayingSample struct {
	windowSeconds int64
	value         int64
	when          time.Time
}

func newDecayingSample(now time.Time, windowSeconds int) decayingSample {
	return decayingSample{windowSeconds: int64(windowSeconds), when: now}
}

func (d *decayingSample) add(v int64, now time.Time) int64 {
	d.decay(now)
	if v > 0 && d.value > int64(^uint64(0)>>1)-v {
		d.value = int64(^uint64(0) >> 1)
	} else {
		d.value += v
	}
	return d.value / d.windowSeconds
}

func (d *decayingSample) valueAt(now time.Time) int64 {
	d.decay(now)
	return d.value / d.windowSeconds
}

// decay reduces value toward zero based on elapsed seconds since the
// last update. Matches rippled's per-second multiplicative shrink:
//
//	value -= (value + window - 1) / window
//
// for each elapsed whole second. Elapsed > 4*window collapses to zero
// directly, since the residual is statistically insignificant — same
// short-circuit rippled uses.
//
// rippled drives DecayingSample with a whole-second clock, so its
// anchor only ever moves in one-second ticks. Go's wall clock is
// nanosecond-precision: anchoring on the raw timestamp would let a
// sub-second call swallow the fractional progress (elapsed truncates
// to 0) while still advancing the anchor, so a peer charged more than
// once per second would never decay. Truncating `now` to the second
// before anchoring restores rippled's behaviour — repeated calls
// within the same second are no-ops, and the anchor only advances on a
// genuine second boundary.
func (d *decayingSample) decay(now time.Time) {
	elapsed := int64(now.Sub(d.when) / time.Second)
	if elapsed <= 0 {
		return
	}
	if d.value != 0 {
		if elapsed > 4*d.windowSeconds {
			d.value = 0
		} else {
			for range elapsed {
				d.value -= (d.value + d.windowSeconds - 1) / d.windowSeconds
			}
		}
	}
	d.when = d.when.Add(time.Duration(elapsed) * time.Second)
}

package resource

import "time"

// decayingSample is an exponentially-decaying sample over a fixed
// window. Mirrors basics::DecayingSample<Window, Clock> at
// rippled/include/xrpl/basics/DecayingSample.h.
type decayingSample struct {
	windowSeconds int
	value         int
	when          time.Time
}

func newDecayingSample(now time.Time, windowSeconds int) decayingSample {
	return decayingSample{windowSeconds: windowSeconds, when: now}
}

func (d *decayingSample) add(v int, now time.Time) int {
	d.decay(now)
	d.value += v
	return d.value / d.windowSeconds
}

func (d *decayingSample) valueAt(now time.Time) int {
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
// short-circuit rippled uses. The anchor is advanced to `now` on every
// call where `now != d.when` (mirroring DecayingSample.h:96): aging is
// tied to whole-second ticks, and sub-second progress between calls
// is intentionally discarded so the algorithm is invariant under
// call-rate.
func (d *decayingSample) decay(now time.Time) {
	if now.Equal(d.when) {
		return
	}
	if !now.After(d.when) {
		// Clock went backwards. Don't reverse-age; just resync.
		d.when = now
		return
	}
	if d.value != 0 {
		elapsed := int(now.Sub(d.when) / time.Second)
		if elapsed > 4*d.windowSeconds {
			d.value = 0
		} else {
			for range elapsed {
				d.value -= (d.value + d.windowSeconds - 1) / d.windowSeconds
			}
		}
	}
	d.when = now
}

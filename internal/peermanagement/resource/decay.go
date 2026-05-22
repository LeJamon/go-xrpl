package resource

import "time"

// decayingSample is a sampling function using exponential decay over a
// fixed window of windowSeconds. Mirrors rippled's
// basics::DecayingSample<Window, Clock> at
// rippled/include/xrpl/basics/DecayingSample.h. add() applies aging
// against `now` before adding the new sample, returning the
// window-normalized value; value() applies aging without adding.
type decayingSample struct {
	windowSeconds int
	value         int
	when          time.Time
}

func newDecayingSample(now time.Time, windowSeconds int) decayingSample {
	return decayingSample{windowSeconds: windowSeconds, when: now}
}

// add ages the running value against now, accumulates v, and returns
// the window-normalized result.
func (d *decayingSample) add(v int, now time.Time) int {
	d.decay(now)
	d.value += v
	return d.value / d.windowSeconds
}

// valueAt ages the running value against now and returns the
// window-normalized result. No sample is added.
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
// short-circuit rippled uses.
func (d *decayingSample) decay(now time.Time) {
	if !now.After(d.when) {
		if now.Equal(d.when) {
			return
		}
		// Clock went backwards. Don't reverse-age; just resync the
		// timestamp so future adds use the new origin.
		d.when = now
		return
	}
	if d.value == 0 {
		d.when = now
		return
	}
	elapsed := int(now.Sub(d.when) / time.Second)
	if elapsed == 0 {
		// Sub-second progress: don't slide the anchor forward, or we
		// lose the cumulative effect of repeated sub-second adds and
		// the window collapses to whatever decays-in-1s.
		return
	}
	if elapsed > 4*d.windowSeconds {
		d.value = 0
	} else {
		for i := 0; i < elapsed; i++ {
			d.value -= (d.value + d.windowSeconds - 1) / d.windowSeconds
		}
	}
	d.when = now
}

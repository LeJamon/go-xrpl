package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/stretchr/testify/assert"
)

// TestRoundCloseTime_RippledParity pins the XRPL-epoch integer math
// rippled uses (LedgerTiming.h:131-143). Issue #363 E1.
//
// rippled's roundCloseTime over NetClock::time_point operates on
// integer seconds since the XRPL epoch (2000-01-01). The algorithm:
//
//	closeTime += closeResolution / 2
//	return closeTime - (closeTime.time_since_epoch() % closeResolution)
//
// goXRPL must produce identical results when given the same wall-clock
// inputs at standard close resolutions.
func TestRoundCloseTime_RippledParity(t *testing.T) {
	xrplBase := time.Unix(protocol.RippleEpochUnix, 0).UTC()

	cases := []struct {
		name       string
		input      time.Time
		resolution time.Duration
		want       time.Time
	}{
		{
			name:       "zero stays zero",
			input:      time.Time{},
			resolution: 10 * time.Second,
			want:       time.Time{},
		},
		{
			name:       "exact multiple of resolution",
			input:      xrplBase.Add(30 * time.Second),
			resolution: 10 * time.Second,
			want:       xrplBase.Add(30 * time.Second),
		},
		{
			name:       "below midpoint rounds down",
			input:      xrplBase.Add(34 * time.Second),
			resolution: 10 * time.Second,
			want:       xrplBase.Add(30 * time.Second),
		},
		{
			name:       "at midpoint rounds up",
			input:      xrplBase.Add(35 * time.Second),
			resolution: 10 * time.Second,
			want:       xrplBase.Add(40 * time.Second),
		},
		{
			name:       "above midpoint rounds up",
			input:      xrplBase.Add(36 * time.Second),
			resolution: 10 * time.Second,
			want:       xrplBase.Add(40 * time.Second),
		},
		{
			name:       "sub-second component truncated then rounded",
			input:      xrplBase.Add(34*time.Second + 999*time.Millisecond),
			resolution: 10 * time.Second,
			want:       xrplBase.Add(30 * time.Second),
		},
		{
			name:       "30s resolution at exact half",
			input:      xrplBase.Add(15 * time.Second),
			resolution: 30 * time.Second,
			want:       xrplBase.Add(30 * time.Second),
		},
		{
			name:       "60s resolution",
			input:      xrplBase.Add(89 * time.Second),
			resolution: 60 * time.Second,
			want:       xrplBase.Add(60 * time.Second),
		},
		{
			name:       "well past XRPL epoch (2025-era)",
			input:      time.Unix(protocol.RippleEpochUnix+780_000_000, 0).UTC(),
			resolution: 10 * time.Second,
			want:       time.Unix(protocol.RippleEpochUnix+780_000_000, 0).UTC(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := roundCloseTime(tc.input, tc.resolution)
			assert.True(t, got.Equal(tc.want),
				"roundCloseTime(%v, %v) = %v, want %v",
				tc.input, tc.resolution, got, tc.want)
		})
	}
}

// TestRoundCloseTime_StableAcrossSubSecondInputs verifies that two
// inputs differing only in their sub-second component produce the same
// rounded output, because rippled's NetClock has integer-second
// precision and goXRPL must match that semantics.
func TestRoundCloseTime_StableAcrossSubSecondInputs(t *testing.T) {
	xrplBase := time.Unix(protocol.RippleEpochUnix+1_000_000, 0).UTC()
	resolution := 10 * time.Second

	// Same integer second, different sub-second components.
	a := xrplBase.Add(34 * time.Second)
	b := xrplBase.Add(34*time.Second + 500*time.Millisecond)
	c := xrplBase.Add(34*time.Second + 999_999_999*time.Nanosecond)

	roundedA := roundCloseTime(a, resolution)
	roundedB := roundCloseTime(b, resolution)
	roundedC := roundCloseTime(c, resolution)

	assert.True(t, roundedA.Equal(roundedB),
		"sub-second variance must not change rounded output: %v vs %v", roundedA, roundedB)
	assert.True(t, roundedA.Equal(roundedC),
		"nanosecond variance must not change rounded output: %v vs %v", roundedA, roundedC)
}

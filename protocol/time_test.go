package protocol

import (
	"math"
	"testing"
	"time"
)

func TestRippleTimeConversions(t *testing.T) {
	epoch := time.Unix(RippleEpochUnix, 0).UTC()
	instant := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	const seconds = 631152000

	if got := FromRippleTime(0); !got.Equal(epoch) {
		t.Fatalf("FromRippleTime(0) = %v, want %v", got, epoch)
	}
	if got := RippleSeconds(instant); got != seconds {
		t.Fatalf("RippleSeconds = %d, want %d", got, seconds)
	}
	if got := ToRippleTime(instant); got != seconds {
		t.Fatalf("ToRippleTime = %d, want %d", got, seconds)
	}
	if got := FromRippleTime(seconds); !got.Equal(instant) {
		t.Fatalf("FromRippleTime = %v, want %v", got, instant)
	}
}

func TestToRippleTimeBounds(t *testing.T) {
	epoch := time.Unix(RippleEpochUnix, 0).UTC()
	if got := ToRippleTime(time.Time{}); got != 0 {
		t.Errorf("ToRippleTime(zero) = %d, want 0", got)
	}
	if got := RippleSeconds(time.Time{}); got != 0 {
		t.Errorf("RippleSeconds(zero) = %d, want 0", got)
	}
	preEpoch := epoch.Add(-time.Second)
	if got := ToRippleTime(preEpoch); got != math.MaxUint32 {
		t.Errorf("ToRippleTime(pre-epoch) = %d, want uint32 wrap to %d", got, uint32(math.MaxUint32))
	}
	if got := RippleSeconds(preEpoch); got != -1 {
		t.Errorf("RippleSeconds(pre-epoch) = %d, want -1", got)
	}
	overflow := epoch.Add(time.Duration(math.MaxUint32)*time.Second + time.Second)
	if got := ToRippleTime(overflow); got != 0 {
		t.Errorf("ToRippleTime(overflow) = %d, want uint32 truncation to 0", got)
	}
}

func TestFormatCloseTimeISO(t *testing.T) {
	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{name: "epoch", in: FromRippleTime(0), want: "2000-01-01T00:00:00Z"},
		{
			name: "known time",
			in:   time.Date(2024, time.July, 6, 15, 4, 5, 987654321, time.FixedZone("offset", 2*60*60)),
			want: "2024-07-06T13:04:05Z",
		},
		{name: "zero", in: time.Time{}, want: "2000-01-01T00:00:00Z"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := FormatCloseTimeISO(test.in); got != test.want {
				t.Fatalf("FormatCloseTimeISO(%v) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

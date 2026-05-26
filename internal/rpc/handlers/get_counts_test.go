package handlers

import (
	"testing"
	"time"
)

// TestUptimeText pins uptimeText to rippled's GetCounts.cpp textTime output:
// largest non-zero units in descending order, comma-separated, pluralized,
// zero units skipped (including intermediate ones), empty below one second.
func TestUptimeText(t *testing.T) {
	const day = 24 * time.Hour
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, ""},
		{500 * time.Millisecond, ""},
		{1 * time.Second, "1 second"},
		{45 * time.Second, "45 seconds"},
		{1 * time.Minute, "1 minute"},
		{90 * time.Second, "1 minute, 30 seconds"},
		{1 * time.Hour, "1 hour"},
		{25 * time.Hour, "1 day, 1 hour"},
		{day, "1 day"},
		{day + 5*time.Second, "1 day, 5 seconds"}, // intermediate zero units skipped
		{2 * day, "2 days"},
		{365*day + 2*day + 3*time.Hour, "1 year, 2 days, 3 hours"},
	}
	for _, c := range cases {
		if got := uptimeText(c.in); got != c.want {
			t.Errorf("uptimeText(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

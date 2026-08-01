package openledger

import "testing"

func TestNextRetryStateMatchesRippledPassTransitions(t *testing.T) {
	tests := []struct {
		name         string
		certainRetry bool
		changes      int
		pass         int
		want         bool
	}{
		{name: "productive first pass remains retry", certainRetry: true, changes: 1, pass: 0, want: true},
		{name: "unproductive first pass becomes final", certainRetry: true, changes: 0, pass: 0, want: false},
		{name: "retry pass limit becomes final", certainRetry: true, changes: 1, pass: retryPasses, want: false},
		{name: "final state never reenters retry", certainRetry: false, changes: 1, pass: 0, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := nextRetryState(test.certainRetry, test.changes, test.pass); got != test.want {
				t.Fatalf("nextRetryState(%t, %d, %d) = %t, want %t",
					test.certainRetry, test.changes, test.pass, got, test.want)
			}
		})
	}
}

package rcl

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// TestCloseTimeTracker_ResetAndState verifies the tracker starts (and resets)
// at the initial avalanche level with no close-time consensus.
func TestCloseTimeTracker_ResetAndState(t *testing.T) {
	c := newCloseTimeTracker()
	if c.haveConsensus {
		t.Error("new tracker should not have close-time consensus")
	}
	if c.stateName() != "init" {
		t.Errorf("new tracker stateName = %q, want init", c.stateName())
	}

	c.haveConsensus = true
	c.avalancheState = avalancheStuck
	c.reset()
	if c.haveConsensus {
		t.Error("reset did not clear haveConsensus")
	}
	if c.stateName() != "init" {
		t.Errorf("reset stateName = %q, want init", c.stateName())
	}
}

// TestCloseTimeTracker_NeededWeightEscalation walks the close-time avalanche
// threshold up through its levels as the converge percent rises, asserting
// both the required weight and the resulting state name at each step.
// Mirrors the cutoffs in DefaultConsensusParms (50→65→70→95).
func TestCloseTimeTracker_NeededWeightEscalation(t *testing.T) {
	parms := consensus.DefaultConsensusParms()
	c := newCloseTimeTracker()

	// Below the first cutoff: stays at init / 50%.
	if pct := c.neededWeight(40, parms); pct != 50 {
		t.Errorf("neededWeight(40) = %d, want 50", pct)
	}
	if c.stateName() != "init" {
		t.Errorf("stateName after 40%% = %q, want init", c.stateName())
	}

	// Crossing each cutoff advances exactly one level.
	steps := []struct {
		converge int
		wantPct  int
		wantName string
	}{
		{50, 65, "mid"},
		{85, 70, "late"},
		{200, 95, "stuck"},
		{300, 95, "stuck"}, // terminal — no further advance
	}
	for _, s := range steps {
		if pct := c.neededWeight(s.converge, parms); pct != s.wantPct {
			t.Errorf("neededWeight(%d) = %d, want %d", s.converge, pct, s.wantPct)
		}
		if c.stateName() != s.wantName {
			t.Errorf("stateName after %d%% = %q, want %q", s.converge, c.stateName(), s.wantName)
		}
	}
}

// TestParticipantsNeeded checks the percentage-to-count rounding, including
// the floor of 1 when the rounded result would be zero.
func TestParticipantsNeeded(t *testing.T) {
	cases := []struct {
		participants, percent, want int
	}{
		{0, 80, 1},  // (0+40)/100 = 0 -> floor of 1
		{1, 80, 1},  // (80+40)/100 = 1
		{4, 75, 3},  // (300+37)/100 = 3
		{10, 75, 7}, // (750+37)/100 = 7
		{10, 50, 5}, // (500+25)/100 = 5
		{100, 80, 80},
	}
	for _, c := range cases {
		if got := participantsNeeded(c.participants, c.percent); got != c.want {
			t.Errorf("participantsNeeded(%d, %d) = %d, want %d",
				c.participants, c.percent, got, c.want)
		}
	}
}

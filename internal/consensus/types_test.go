package consensus

import "testing"

func TestConsensusParms_InvalidAvalancheStatePanics(t *testing.T) {
	parms := DefaultConsensusParms()
	tests := []struct {
		name  string
		state AvalancheState
	}{
		{name: "negative", state: AvalancheState(-1)},
		{name: "above stuck", state: AvalancheStuck + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NeededWeight did not panic")
				}
			}()
			parms.NeededWeight(tt.state, 0, 0, parms.MinRounds)
		})
	}
}

func TestConsensusParms_AvalancheCutoffsAreImmutable(t *testing.T) {
	parms := DefaultConsensusParms()
	cutoff := parms.AvalancheCutoff(AvalancheMid)
	cutoff.ConsensusPct = 1

	if got := parms.AvalancheCutoff(AvalancheMid).ConsensusPct; got != 65 {
		t.Fatalf("AvalancheMid ConsensusPct = %d, want 65", got)
	}
	if got := parms.AvalancheCutoffCount(); got != 4 {
		t.Fatalf("AvalancheCutoffCount = %d, want 4", got)
	}
}

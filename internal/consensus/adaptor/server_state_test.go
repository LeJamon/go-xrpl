package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

func TestConsensusServerState(t *testing.T) {
	for _, tc := range []struct {
		name       string
		opMode     consensus.OperatingMode
		mode       consensus.Mode
		validating bool
		want       string
	}{
		{"proposing outranks validating", consensus.OpModeFull, consensus.ModeProposing, true, "proposing"},
		{"proposing without validating", consensus.OpModeFull, consensus.ModeProposing, false, "proposing"},
		{"validating while observing", consensus.OpModeFull, consensus.ModeObserving, true, "validating"},
		{"validating after switched ledger", consensus.OpModeFull, consensus.ModeSwitchedLedger, true, "validating"},
		{"full non-validating node", consensus.OpModeFull, consensus.ModeObserving, false, "full"},
		{"wrong ledger suppresses role", consensus.OpModeFull, consensus.ModeWrongLedger, true, "full"},
		{"not synced ignores role", consensus.OpModeTracking, consensus.ModeProposing, true, "tracking"},
		{"connected", consensus.OpModeConnected, consensus.ModeObserving, false, "connected"},
		{"disconnected", consensus.OpModeDisconnected, consensus.ModeObserving, false, "disconnected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := consensusServerState(tc.opMode, tc.mode, tc.validating); got != tc.want {
				t.Errorf("consensusServerState(%v, %v, %v) = %q, want %q",
					tc.opMode, tc.mode, tc.validating, got, tc.want)
			}
		})
	}
}

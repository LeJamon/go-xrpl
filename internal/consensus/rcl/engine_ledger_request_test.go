package rcl

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/stretchr/testify/require"
)

type ledgerRequestCallbackAdaptor struct {
	*mockAdaptor
	request func(consensus.LedgerID) error
}

func (a *ledgerRequestCallbackAdaptor) RequestLedger(id consensus.LedgerID) error {
	return a.request(id)
}

func TestEngine_LedgerRequestCanCompleteSynchronously(t *testing.T) {
	for _, retry := range []bool{false, true} {
		name := "first request"
		if retry {
			name = "retry"
		}
		t.Run(name, func(t *testing.T) {
			base := newMockAdaptor()
			base.opMode = consensus.OpModeTracking
			a := &ledgerRequestCallbackAdaptor{mockAdaptor: base}
			e := NewEngine(a, DefaultConfig())
			initial := base.lastLCL
			e.prevLedger = initial
			require.NoError(t, e.StartRound(consensus.RoundID{
				Seq: initial.Seq() + 1, ParentHash: initial.ID(),
			}, false))

			target := chainLedger(initial.Seq()+1, 0xA1, initial.ID()[0])
			target.closeTime = base.Now()
			base.peerLCLs = []consensus.LedgerID{target.ID(), target.ID()}
			if retry {
				e.mode = consensus.ModeWrongLedger
				e.wrongLedgerID = target.ID()
				e.state.HaveCorrectLCL = false
			}

			requests := 0
			a.request = func(id consensus.LedgerID) error {
				requests++
				require.Equal(t, target.ID(), id)
				if !e.mu.TryLock() {
					t.Error("ledger request callback holds the consensus mutex")
					return nil
				}
				mode, pinned, correct := e.mode, e.wrongLedgerID, e.state.HaveCorrectLCL
				e.mu.Unlock()
				require.Equal(t, consensus.ModeWrongLedger, mode)
				require.Equal(t, id, pinned)
				require.False(t, correct)

				require.NoError(t, base.StoreLedger(target))
				result, err := e.TrySwitchToLedger(id)
				require.NoError(t, err)
				require.Equal(t, consensus.LedgerSwitchAccepted, result)
				return nil
			}

			e.timerEntry()
			require.Equal(t, 1, requests)
			require.Equal(t, target.ID(), e.prevLedger.ID())
			require.Equal(t, consensus.ModeSwitchedLedger, e.mode)
			require.Empty(t, e.wrongLedgerID)
			require.True(t, e.state.HaveCorrectLCL)
			require.Equal(t, target.Seq()+1, e.state.Round.Seq)
		})
	}
}

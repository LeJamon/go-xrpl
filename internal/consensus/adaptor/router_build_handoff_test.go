package adaptor

import (
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/stretchr/testify/require"
)

func TestRouter_DemotedEngineDoesNotBlockSuccessorAcquisition(t *testing.T) {
	for _, supportsReplay := range []bool{false, true} {
		t.Run(fmt.Sprintf("replay_peer=%t", supportsReplay), func(t *testing.T) {
			svc := newTestLedgerService(t)
			a, sender := newRecordingAdaptor(t, svc)
			sender.peerSupportsReplay = supportsReplay
			a.SetOperatingMode(consensus.OpModeFull)
			a.closeOffsetNs.Store(int64(2 * time.Minute))
			now := a.Now()
			cfg := rcl.DefaultConfig()
			cfg.ManualTick = true
			cfg.Clock = func() time.Time { return now }
			engine := rcl.NewEngine(a, cfg)
			require.NoError(t, engine.Start(t.Context()))
			t.Cleanup(func() { require.NoError(t, engine.Stop()) })
			r := newTestRouter(engine, a, nil)
			base := svc.GetClosedLedger()
			require.NoError(t, engine.StartRound(consensus.RoundID{
				Seq: base.Sequence() + 1, ParentHash: consensus.LedgerID(base.Hash()),
			}, true))
			now = now.Add(2 * time.Minute)
			engine.TimerEntry()
			require.Equal(t, consensus.PhaseEstablish, engine.Phase())

			// Freeze the round as the production demotion does, while a trusted
			// successor remains the immediate recovery target. Exercise the real
			// engine's acquisition guard with both classic and replay peers.
			a.networkValidatedSeq.Store(base.Sequence() + 2)
			a.SetOperatingMode(consensus.OpModeConnected)
			engine.TimerEntry()
			require.Equal(t, consensus.ModeObserving, engine.Mode())
			link := buildAlternativeReplaySuccessor(t, base, time.Second)
			r.recordSeqHash(link.seq, link.hash, base.Hash(), true)
			trackCatchupPeer(r, 7, link.seq, link.hash)
			r.armValidatedLedgerAcquisition(link.seq, link.hash)
			if supportsReplay {
				require.True(t, r.replayer.Has(link.hash), "parked establish must not reserve the replay successor")
				require.Len(t, sender.replayCalls(), 1)
			} else {
				acquisition := r.fetchTracker.Find(link.hash)
				require.NotNil(t, acquisition, "classic peers must also start recovery immediately")
				require.True(t, acquisition.TransactionOnly(), "reuse the local parent instead of acquiring full state")
				require.Len(t, sender.legacyCalls(), 1)
			}
		})
	}
}

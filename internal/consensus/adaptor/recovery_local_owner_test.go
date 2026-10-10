package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

func TestRecoveryLocalPreparationPreventsGapEscalation(t *testing.T) {
	for _, delta := range []bool{false, true} {
		name := "transactions"
		if delta {
			name = "delta"
		}
		t.Run(name, func(t *testing.T) {
			r, _, sender, svc := makeRouter(t)
			c := r.catchupReplay
			parent := svc.GetClosedLedger()
			links := buildStandardReplayTestChain(t, r, parent, maxForwardDeltaGap+2)
			next, target := links[0], links[len(links)-1]
			if delta {
				rd, err := c.replayer.Acquire(next.hash, 7, parent)
				require.NoError(t, err)
				require.NoError(t, rd.GotResponse(next.response))
				c.legacyDeltaTask = &legacyDeltaTask{delta: rd}
				t.Cleanup(func() { c.replayer.Abandon(next.hash) })
			} else {
				il := inbound.New(next.hash, next.seq, 7, serveTestLogger(), inbound.WithTransactionOnly())
				require.NoError(t, il.GotBase([]message.LedgerNode{{NodeData: next.response.LedgerHeader}, {NodeData: []byte{1}}}))
				c.completionTasks[1] = &acquisitionCompletionTask{ledger: il}
			}
			require.True(t, c.localForwardReplayOwnsRecovery(target.seq, target.hash))
			require.True(t, c.beginFrozenPivotRecovery(target.seq, target.hash, 7))
			require.False(t, c.standardReplay.active, "local verification must not create a competing pivot")
			require.Empty(t, sender.legacyCalls())
			// A directly conflicting target must not be protected by old work.
			require.False(t, c.localForwardReplayOwnsRecovery(next.seq, [32]byte{99}))
			c.legacyDeltaTask = nil
			c.completionTasks[1] = nil
			require.False(t, c.localForwardReplayOwnsRecovery(target.seq, target.hash), "failure/completion releases the lease")
		})
	}
}

func TestRecoveryCommittedAnchorSurvivesMovingTargetPastInitialWindow(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	c := r.catchupReplay
	parent := svc.GetClosedLedger()
	links := buildStandardReplayTestChain(t, r, parent, maxForwardDeltaGap+3)
	target := links[len(links)-1]
	// The just-committed anchor can equal the canonical frontier. A changing
	// target must not erase it or restore the initial 128-ledger cutoff.
	c.consensusRecovery = consensusRecovery{anchorSeq: parent.Sequence(), anchorHash: parent.Hash(), targetHash: links[len(links)-2].hash}
	seq, hash, _, forward, _ := c.recoveryForwardStep(svc, target.seq, target.hash, c.consensusRecovery)
	require.True(t, forward)
	require.Equal(t, links[0].seq, seq)
	require.Equal(t, links[0].hash, hash)
	trackCatchupPeer(r, 7, target.seq, target.hash)
	c.recordCatchupTarget(target.seq, target.hash, 7)
	c.armCatchupTowardTarget()
	require.False(t, c.standardReplay.active, "moving head abandoned the committed replay anchor")
	require.NotEmpty(t, sender.replayCalls())
	require.Equal(t, links[0].hash, sender.replayCalls()[0].hash)
	require.Empty(t, sender.legacyCalls())
}

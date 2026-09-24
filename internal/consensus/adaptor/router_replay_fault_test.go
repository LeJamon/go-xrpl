package adaptor

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func TestReplayFaultAuthenticationRequiresVerifiedAncestry(t *testing.T) {
	r, a, _, svc := makeRouter(t)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 2)
	first, last := links[0], links[1]
	historian := &recordingFeeHistorian{stubHistorian: &stubHistorian{byLedger: map[consensus.LedgerID][]*consensus.Validation{
		consensus.LedgerID(last.hash): {{Full: true, LedgerSeq: last.seq, SignTime: time.Now()}},
	}}}
	a.SetValidationHistorian(historian)
	r.acquisitionMu.Lock()
	r.standardReplay.targetSeq = last.seq
	r.standardReplay.targetHash = last.hash
	r.acquisitionMu.Unlock()
	r.seqHashMu.Lock()
	delete(r.seqHash, last.seq)
	r.seqHashMu.Unlock()
	r.recordPeerSeqHash(last.seq, last.hash, first.hash, true)
	require.False(t, r.replayTargetAuthenticated(first.ledger.Header()))
	r.recordAcquiredSeqHash(last.seq, last.hash, first.hash)
	require.True(t, r.replayTargetAuthenticated(first.ledger.Header()))
	require.True(t, r.replayTargetAuthenticated(last.ledger.Header()))
	fork := first.ledger.Header()
	fork.Hash[0] ^= 1
	require.False(t, r.replayTargetAuthenticated(fork))
	historian.byLedger = nil
	require.False(t, r.replayTargetAuthenticated(first.ledger.Header()))
}

func TestReplayFaultRepairCannotBeRearmedByValidationNotifications(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	_, target, _, _ := buildSuccessorAgainstParent(t, parent)
	trackCatchupPeer(r, 7, target.Sequence(), target.Hash())
	svc.RecordReplayPreparationFailure(context.Background(), target.Header(), shamap.New(shamap.TypeTransaction), nil, true, shamap.ErrNodeNotInStore)
	require.True(t, svc.ReplayBlocked())
	repair := r.fetchTracker.Find(parent.Hash())
	require.NotNil(t, repair)
	before := len(sender.legacyCalls())
	r.failInboundAcquisition(repair)
	for range 5 {
		r.onLedgerFullyValidated(parent.Sequence(), parent.Hash())
		r.armConsensusCatchup()
		_, started := r.startGenericAcquisition(parent.Hash(), parent.Sequence())
		require.False(t, started)
		r.acquisitionMu.Lock()
		r.startLedgerAcquisitionLegacyLocked(parent.Sequence(), parent.Hash(), 7)
		r.acquisitionMu.Unlock()
	}
	require.Nil(t, r.fetchTracker.Find(parent.Hash()))
	require.Equal(t, before, len(sender.legacyCalls()))
	require.Equal(t, 1, svc.ReplayFaultStatus().Recovery.AcquisitionAttempts)
}

package adaptor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
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

func newReplayFaultRepair(t *testing.T) (*Router, *inbound.Ledger) {
	t.Helper()
	r, _, _, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	_, target, _, _ := buildSuccessorAgainstParent(t, parent)
	trackCatchupPeer(r, 7, target.Sequence(), target.Hash())
	svc.RecordReplayPreparationFailure(
		context.Background(),
		target.Header(),
		shamap.New(shamap.TypeTransaction),
		nil,
		true,
		shamap.ErrNodeNotInStore,
	)
	repair := r.fetchTracker.Find(parent.Hash())
	require.NotNil(t, repair)
	return r, repair
}

func TestReplayFaultRepairPersistsTerminalAcquisitionCauses(t *testing.T) {
	tests := []struct {
		name   string
		result func(*inbound.Ledger) acquisitionWorkResult
		want   string
	}{
		{
			name: "timer",
			result: func(il *inbound.Ledger) acquisitionWorkResult {
				return acquisitionWorkResult{
					ledger:       il,
					remove:       true,
					timerFailure: true,
					snapshot:     il.Snapshot(),
					haveSnapshot: true,
				}
			},
			want: "inbound ledger acquisition timer expired",
		},
		{
			name: "policy rejection",
			result: func(il *inbound.Ledger) acquisitionWorkResult {
				return acquisitionWorkResult{
					ledger:        il,
					remove:        true,
					policyFailure: true,
					err:           errors.New("header rejected by history policy"),
					snapshot:      il.Snapshot(),
					haveSnapshot:  true,
				}
			},
			want: "header rejected by history policy",
		},
		{
			name: "discard",
			result: func(il *inbound.Ledger) acquisitionWorkResult {
				return acquisitionWorkResult{
					ledger:       il,
					remove:       true,
					err:          errors.New("state node failed validation"),
					snapshot:     il.Snapshot(),
					haveSnapshot: true,
				}
			},
			want: "state node failed validation",
		},
		{
			name: "verified-node persistence",
			result: func(il *inbound.Ledger) acquisitionWorkResult {
				return acquisitionWorkResult{
					ledger:         il,
					complete:       true,
					persistenceErr: errors.New("verified-node persistence failed"),
				}
			},
			want: "verified-node persistence failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r, repair := newReplayFaultRepair(t)
			r.handleAcquisitionWorkResult(test.result(repair))
			status := r.adaptor.LedgerService().ReplayFaultStatus()
			require.Contains(t, status.Recovery.AcquisitionError, test.want)
			require.Nil(t, r.fetchTracker.Find(repair.Hash()))
		})
	}
}

func TestReplayFaultRepairIgnoresStaleAndUnrelatedAcquisitionResults(t *testing.T) {
	t.Run("stale replacement", func(t *testing.T) {
		r, repair := newReplayFaultRepair(t)
		svc := r.adaptor.LedgerService()
		svc.RecordReplayAcquisitionFailure(repair.Hash(), errors.New("first terminal failure"))
		require.True(t, r.fetchTracker.RemoveExpectedWithSnapshot(repair, repair.Snapshot(), false))
		replacement, created := r.fetchTracker.GetOrCreate(repair.Hash(), func() *inbound.Ledger {
			return inbound.New(repair.Hash(), repair.Seq(), 8, serveTestLogger())
		})
		require.True(t, created)
		require.NotSame(t, repair, replacement)

		r.handleAcquisitionWorkResult(acquisitionWorkResult{
			ledger:       repair,
			remove:       true,
			timerFailure: true,
			snapshot:     repair.Snapshot(),
			haveSnapshot: true,
		})
		require.Equal(t, "first terminal failure", svc.ReplayFaultStatus().Recovery.AcquisitionError)
	})

	t.Run("unrelated acquisition", func(t *testing.T) {
		r, repair := newReplayFaultRepair(t)
		svc := r.adaptor.LedgerService()
		other := inbound.New([32]byte{0xee}, repair.Seq()+1, 9, serveTestLogger())
		r.fetchTracker.Track(other)

		r.handleAcquisitionWorkResult(acquisitionWorkResult{
			ledger:       other,
			remove:       true,
			timerFailure: true,
			snapshot:     other.Snapshot(),
			haveSnapshot: true,
		})
		require.Empty(t, svc.ReplayFaultStatus().Recovery.AcquisitionError)
	})
}

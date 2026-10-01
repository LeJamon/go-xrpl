package adaptor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFrozenPivotRecoveryDefersPersistedReplayFault(t *testing.T) {
	a := newReplayFaultBlockedAdaptor(t)
	svc := a.LedgerService()
	t.Cleanup(svc.Stop)
	r := newTestRouter(nil, a, nil)
	var logs bytes.Buffer
	r.logger = slog.New(slog.NewTextHandler(&logs, nil))
	r.catchupReplay.logger = r.logger

	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xa1}
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	trusted, err := a.GetValidatorKey()
	require.NoError(t, err)
	tracker := rcl.NewValidationTracker(1)
	tracker.SetTrustedAndQuorum([]consensus.NodeID{trusted}, 1)
	tracker.SetFullyValidatedCallback(func(id consensus.LedgerID, seq uint32) {
		a.OnLedgerFullyValidated(id, seq)
	})
	a.SetValidationHistorian(tracker)
	now := time.Now()
	require.True(t, tracker.Add(&consensus.Validation{
		NodeID:    trusted,
		LedgerSeq: pivotSeq,
		LedgerID:  consensus.LedgerID(pivotHash),
		SignTime:  now,
		SeenTime:  now,
		Full:      true,
	}))

	for range 5 {
		a.OnLedgerFullyValidated(consensus.LedgerID(pivotHash), pivotSeq)
	}

	assert.True(t, r.catchupReplay.replayFaultBlocked())
	assert.False(t, a.IsValidator())
	assert.False(t, r.catchupReplay.standardReplay.active)
	assert.Zero(t, r.catchupReplay.standardReplay.generation)
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(pivotHash))
	assert.Equal(t, 1, strings.Count(logs.String(), "frozen recovery pivot admission deferred by replay fault"))
	assert.Contains(t, logs.String(), "fault-1")
	assert.Contains(t, logs.String(), "invoke admin replay_recover with fault_id")
}

func TestReplayFaultAuthenticationRequiresVerifiedAncestry(t *testing.T) {
	r, a, _, svc := makeRouter(t)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 2)
	first, last := links[0], links[1]
	historian := &recordingFeeHistorian{stubHistorian: &stubHistorian{byLedger: map[consensus.LedgerID][]*consensus.Validation{
		consensus.LedgerID(last.hash): {{Full: true, LedgerSeq: last.seq, SignTime: time.Now()}},
	}}}
	a.SetValidationHistorian(historian)
	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.standardReplay.targetSeq = last.seq
	r.catchupReplay.standardReplay.targetHash = last.hash
	r.catchupReplay.acquisitionMu.Unlock()
	r.catchupReplay.seqHashMu.Lock()
	delete(r.catchupReplay.seqHash, last.seq)
	r.catchupReplay.seqHashMu.Unlock()
	r.catchupReplay.recordPeerSeqHash(last.seq, last.hash, first.hash, true)
	require.False(t, r.catchupReplay.replayTargetAuthenticated(first.ledger.Header()))
	r.catchupReplay.recordAcquiredSeqHash(last.seq, last.hash, first.hash)
	require.True(t, r.catchupReplay.replayTargetAuthenticated(first.ledger.Header()))
	require.True(t, r.catchupReplay.replayTargetAuthenticated(last.ledger.Header()))
	fork := first.ledger.Header()
	fork.Hash[0] ^= 1
	require.False(t, r.catchupReplay.replayTargetAuthenticated(fork))
	historian.byLedger = nil
	require.False(t, r.catchupReplay.replayTargetAuthenticated(first.ledger.Header()))
}

func TestReplayFaultRepairCannotBeRearmedByValidationNotifications(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	_, target, _, _ := buildSuccessorAgainstParent(t, parent)
	trackCatchupPeer(r, 7, target.Sequence(), target.Hash())
	svc.RecordReplayPreparationFailure(context.Background(), target.Header(), shamap.New(shamap.TypeTransaction), nil, true, shamap.ErrNodeNotInStore)
	require.True(t, svc.ReplayBlocked())
	repair := r.catchupReplay.fetchTracker.Find(parent.Hash())
	require.NotNil(t, repair)
	before := len(sender.legacyCalls())
	r.catchupReplay.failInboundAcquisition(repair)
	for range 5 {
		r.catchupReplay.onLedgerFullyValidated(parent.Sequence(), parent.Hash())
		r.catchupReplay.armConsensusCatchup()
		_, started := r.catchupReplay.startGenericAcquisition(parent.Hash(), parent.Sequence())
		require.False(t, started)
		r.catchupReplay.acquisitionMu.Lock()
		r.catchupReplay.startLedgerAcquisitionLegacyLocked(parent.Sequence(), parent.Hash(), 7)
		r.catchupReplay.acquisitionMu.Unlock()
	}
	require.Nil(t, r.catchupReplay.fetchTracker.Find(parent.Hash()))
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
	repair := r.catchupReplay.fetchTracker.Find(parent.Hash())
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
			r.catchupReplay.handleAcquisitionWorkResult(test.result(repair))
			status := r.adaptor.LedgerService().ReplayFaultStatus()
			require.Contains(t, status.Recovery.AcquisitionError, test.want)
			require.Nil(t, r.catchupReplay.fetchTracker.Find(repair.Hash()))
		})
	}
}

func TestReplayFaultRepairIgnoresStaleAndUnrelatedAcquisitionResults(t *testing.T) {
	t.Run("stale replacement", func(t *testing.T) {
		r, repair := newReplayFaultRepair(t)
		svc := r.adaptor.LedgerService()
		svc.RecordReplayAcquisitionFailure(repair.Hash(), errors.New("first terminal failure"))
		require.True(t, r.catchupReplay.fetchTracker.RemoveExpectedWithSnapshot(repair, repair.Snapshot(), false))
		replacement, created := r.catchupReplay.fetchTracker.GetOrCreate(repair.Hash(), func() *inbound.Ledger {
			return inbound.New(repair.Hash(), repair.Seq(), 8, serveTestLogger())
		})
		require.True(t, created)
		require.NotSame(t, repair, replacement)

		r.catchupReplay.handleAcquisitionWorkResult(acquisitionWorkResult{
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
		r.catchupReplay.fetchTracker.Track(other)

		r.catchupReplay.handleAcquisitionWorkResult(acquisitionWorkResult{
			ledger:       other,
			remove:       true,
			timerFailure: true,
			snapshot:     other.Snapshot(),
			haveSnapshot: true,
		})
		require.Empty(t, svc.ReplayFaultStatus().Recovery.AcquisitionError)
	})
}

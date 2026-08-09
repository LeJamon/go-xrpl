package adaptor

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/stretchr/testify/require"
)

type countingValidationTracker struct {
	*rcl.ValidationTracker
	rechecks map[consensus.LedgerID]int
}

func (t *countingValidationTracker) RecheckFullyValidated(
	id consensus.LedgerID,
	seq uint32,
) ([]*consensus.Validation, int, bool) {
	t.rechecks[id]++
	return t.ValidationTracker.RecheckFullyValidated(id, seq)
}

func TestFastSyncFinalityMoreThan256TargetsUseLiveEvidence(t *testing.T) {
	a := newTestAdaptor(t)
	engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
	r := newTestRouter(engine, a, nil)
	engine.switchHook = func(id consensus.LedgerID) {
		selected, err := a.GetLedger(id)
		require.NoError(t, err)
		require.NoError(t, a.OnLedgerSwitched(selected))
	}
	tracker := &countingValidationTracker{
		ValidationTracker: rcl.NewValidationTracker(2),
		rechecks:          make(map[consensus.LedgerID]int),
	}
	nodes := []consensus.NodeID{{1}, {2}}
	tracker.SetTrustedAndQuorum(nodes, 2)
	a.SetValidationHistorian(tracker)
	tracker.SetFullyValidatedCallback(func(id consensus.LedgerID, seq uint32) {
		r.onLedgerFullyValidated(seq, [32]byte(id))
	})

	base := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	now := base
	tracker.SetNow(func() time.Time { return now })

	const (
		targets  = 300
		finalSeq = uint32(10_000 + targets - 1)
	)
	final := completedCatchUpAcquisitionWithHeader(t, header.LedgerHeader{
		LedgerIndex: finalSeq,
		ParentHash:  [32]byte{0xEE},
		CloseTime:   base,
	})
	hashes := make([][32]byte, targets)
	for i := range targets {
		now = base.Add(time.Duration(i) * time.Second)
		seq := uint32(10_000 + i)
		if seq == finalSeq {
			hashes[i] = final.Hash()
		} else {
			binary.BigEndian.PutUint32(hashes[i][:4], seq)
		}
		for _, node := range nodes {
			require.True(t, tracker.Add(&consensus.Validation{
				LedgerID:  consensus.LedgerID(hashes[i]),
				LedgerSeq: seq,
				NodeID:    node,
				SignTime:  now,
				SeenTime:  now,
				Full:      true,
			}))
		}
	}

	seq, hash, _ := r.bestCatchupTarget()
	require.Equal(t, finalSeq, seq)
	require.Equal(t, hashes[targets-1], hash)
	require.Equal(t, uint64(targets-1), r.FastSyncMetrics().TargetSuperseded)

	r.completeStoredConsensusRecovery(10_000, hashes[0], [32]byte{}, false)
	require.Equal(t, hash, func() [32]byte {
		_, current, _ := r.bestCatchupTarget()
		return current
	}())
	require.Equal(t, uint64(1), r.FastSyncMetrics().ObsoleteAcquisitionCompleted)
	require.Equal(t, 1, tracker.rechecks[consensus.LedgerID(hashes[0])])

	r.fetchTracker.Track(final)
	r.completeInboundLedger(final)
	require.Equal(t, final.Hash(), a.ledgerService.GetClosedLedger().Hash())
	require.Equal(t, final.Hash(), a.ledgerService.GetValidatedLedger().Hash())
	require.Equal(t, uint64(2), r.FastSyncMetrics().CompletionRecheckAccepted)
	require.Equal(t, 2, tracker.rechecks[consensus.LedgerID(final.Hash())])
}

func TestFastSyncCompletionRecheckUsesCurrentTrustAndExpiry(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*rcl.ValidationTracker, *time.Time, []consensus.NodeID, uint32)
		wantResult validationRecheckResult
	}{
		{
			name: "trust removed",
			mutate: func(tracker *rcl.ValidationTracker, _ *time.Time, nodes []consensus.NodeID, _ uint32) {
				tracker.SetTrustedAndQuorum(nodes[:1], 2)
			},
			wantResult: validationRecheckBelowQuorum,
		},
		{
			name: "evidence expired",
			mutate: func(tracker *rcl.ValidationTracker, now *time.Time, _ []consensus.NodeID, seq uint32) {
				*now = now.Add(11 * time.Minute)
				tracker.ExpireOld(seq + 1)
			},
			wantResult: validationRecheckNoEvidence,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a := newTestAdaptor(t)
			engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
			r := newTestRouter(engine, a, nil)
			engine.switchHook = func(id consensus.LedgerID) {
				selected, err := a.GetLedger(id)
				require.NoError(t, err)
				require.NoError(t, a.OnLedgerSwitched(selected))
			}
			tracker := rcl.NewValidationTracker(2)
			nodes := []consensus.NodeID{{1}, {2}}
			tracker.SetTrustedAndQuorum(nodes, 2)
			historian := &countingValidationTracker{
				ValidationTracker: tracker,
				rechecks:          make(map[consensus.LedgerID]int),
			}
			a.SetValidationHistorian(historian)
			tracker.SetFullyValidatedCallback(func(id consensus.LedgerID, seq uint32) {
				r.onLedgerFullyValidated(seq, [32]byte(id))
			})

			now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
			tracker.SetNow(func() time.Time { return now })
			const seq uint32 = 20_000
			completed := completedCatchUpAcquisitionWithHeader(t, header.LedgerHeader{
				LedgerIndex: seq,
				ParentHash:  [32]byte{0xAB},
				CloseTime:   now,
			})
			hash := completed.Hash()
			previousValidated := a.ledgerService.GetValidatedLedger()
			for _, node := range nodes {
				require.True(t, tracker.Add(&consensus.Validation{
					LedgerID:  consensus.LedgerID(hash),
					LedgerSeq: seq,
					NodeID:    node,
					SignTime:  now,
					SeenTime:  now,
					Full:      true,
				}))
			}

			test.mutate(tracker, &now, nodes, seq)
			r.fetchTracker.Track(completed)
			r.completeInboundLedger(completed)
			metrics := r.FastSyncMetrics()
			switch test.wantResult {
			case validationRecheckBelowQuorum:
				require.Equal(t, uint64(1), metrics.CompletionRecheckRejectedBelowQuorum)
			case validationRecheckNoEvidence:
				require.Equal(t, uint64(1), metrics.CompletionRecheckRejectedNoEvidence)
			}
			require.Zero(t, metrics.CompletionRecheckAccepted)
			require.Equal(t, hash, a.ledgerService.GetClosedLedger().Hash())
			require.Equal(t, previousValidated.Hash(), a.ledgerService.GetValidatedLedger().Hash())
			require.Equal(t, 2, historian.rechecks[consensus.LedgerID(hash)])
		})
	}
}

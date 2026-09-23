package adaptor

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/stretchr/testify/require"
)

func TestHistoryWindowMinimum(t *testing.T) {
	for _, tc := range []struct{ tip, depth, want uint32 }{
		{1000, 256, 745}, {1001, 256, 746}, {255, 256, 1}, {256, 256, 1},
		{1, 256, 1}, {0, 256, 1}, {1000, 1, 1000}, {1000, ^uint32(0), 1},
		{^uint32(0), 256, ^uint32(0) - 255}, {^uint32(0), ^uint32(0), 1},
	} {
		require.Equal(t, tc.want, historyWindowMinimum(tc.tip, tc.depth))
	}
}

func TestHistoryBackfillDisabledDoesNotDisableConsensusAcquisition(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	defer svc.Stop()
	r.configureHistoryBackfill(false, 256)
	tip := svc.GetValidatedLedgerIndex()
	historyHash := [32]byte{0xC1}
	trackCatchupPeer(r, 7, tip+10)
	r.startHistoryBackfill(tip-1, historyHash, 7, 0)
	r.onLedgerSwitched(tip+10, [32]byte{0xC2}, historyHash, tip)
	r.armHistoryBackfill()
	require.Zero(t, r.history.seq)
	require.Nil(t, r.prepareHistoryAcquisition(tip-1, historyHash, 7))
	require.Empty(t, sender.legacyCalls())
	require.True(t, r.startLedgerAcquisition(tip+1, [32]byte{0xC3}, 7))
	require.NotZero(t, len(sender.replayCalls())+len(sender.legacyCalls()))
}

func TestHistoryBackfillWindowCancelsOnlyExpiredHistory(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	defer svc.Stop()
	r.configureHistoryBackfill(true, 256)
	old := inbound.NewHistory([32]byte{1}, 745, 7, r.logger)
	keep := inbound.NewHistory([32]byte{2}, 746, 7, r.logger)
	consensus := inbound.New([32]byte{3}, 700, 7, r.logger)
	generic := inbound.NewGeneric([32]byte{4}, 700, 7, r.logger)
	for _, il := range []*inbound.Ledger{old, keep, consensus, generic} {
		r.fetchTracker.Track(il)
	}
	r.startHistoryBackfill(old.Seq(), old.Hash(), 7, 0)
	r.pruneHistoryBackfill(1000)
	require.Same(t, old, r.fetchTracker.Find(old.Hash()))
	r.pruneHistoryBackfill(1001)
	require.Nil(t, r.fetchTracker.Find(old.Hash()))
	require.Zero(t, r.history.seq)
	for _, il := range []*inbound.Ledger{keep, consensus, generic} {
		require.Same(t, il, r.fetchTracker.Find(il.Hash()))
	}
	// Local cancellation is not a peer failure / retry-suppression tombstone.
	require.NotContains(t, r.FetchInfo(), "745")
}

func TestHistoryBackfillWindowCancelsWorkerIO(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	defer svc.Stop()
	lane := newAcquisitionWorkLane(1)
	entered := make(chan struct{})
	lane.process = func(ctx context.Context, il *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		close(entered)
		<-ctx.Done()
		return acquisitionWorkResult{ledger: il, err: ctx.Err()}
	}
	lane.start(t.Context())
	defer lane.stop()
	r.acquisitionWork = lane
	il := inbound.NewHistory([32]byte{5}, 745, 7, r.logger)
	r.fetchTracker.Track(il)
	require.True(t, lane.submit(il, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("history worker did not start")
	}
	r.pruneHistoryBackfill(1001)
	select {
	case result := <-lane.results():
		require.ErrorIs(t, result.err, context.Canceled)
		close(result.ack)
	case <-time.After(time.Second):
		t.Fatal("expired history kept its disk traversal alive")
	}
	require.Nil(t, r.fetchTracker.Find(il.Hash()))
}

func TestHistoryBackfillRespectsWindowAndOnlineFloor(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	defer svc.Stop()
	for svc.GetValidatedLedgerIndex() < 6 {
		_, err := svc.AcceptLedger(context.Background())
		require.NoError(t, err)
	}
	r.configureHistoryBackfill(true, 3) // [4,6], history [4,5]
	require.False(t, r.historySequenceAllowed(3))
	require.True(t, r.historySequenceAllowed(4))
	require.True(t, r.historySequenceAllowed(5))
	require.False(t, r.historySequenceAllowed(6))
	r.SetMinimumOnlineFloor(stubFloor(5))
	require.False(t, r.historySequenceAllowed(4))
	require.True(t, r.historySequenceAllowed(5))
	for _, depth := range []uint32{0, 1} {
		r.configureHistoryBackfill(true, depth)
		require.False(t, r.historySequenceAllowed(5))
	}
}

func TestHistoryBackfillRestartSkipsCompleteLedgers(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	defer svc.Stop()
	for svc.GetValidatedLedgerIndex() < 8 {
		_, err := svc.AcceptLedger(context.Background())
		require.NoError(t, err)
	}
	r.configureHistoryBackfill(true, 3)
	trackCatchupPeer(r, 7, 8)
	r.armHistoryBackfill()
	require.True(t, r.historySeeded)
	require.Zero(t, r.history.seq, "all retained history is already complete")
	require.Empty(t, sender.legacyCalls())
}

func TestHistoryBackfillLateResultCannotReviveDisabledWork(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	defer svc.Stop()
	r.configureHistoryBackfill(false, 256)
	il := inbound.NewHistory([32]byte{9}, 1, 7, r.logger)
	r.fetchTracker.Track(il)
	r.handleAcquisitionWorkResult(acquisitionWorkResult{ledger: il, yielded: true})
	require.Nil(t, r.fetchTracker.Find(il.Hash()))
	r.completeInboundLedgerReady(il) // stale completion must not reach Result/ingest
	require.Nil(t, r.fetchTracker.Find(il.Hash()))
}

func TestHistoryBackfillNewestMissingFirst(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	defer svc.Stop()
	for svc.GetValidatedLedgerIndex() < 6 {
		_, err := svc.AcceptLedger(context.Background())
		require.NoError(t, err)
	}
	r.configureHistoryBackfill(true, 3)
	trackCatchupPeer(r, 7, 6)
	old := inbound.NewHistory([32]byte{0xA1}, 4, 7, r.logger)
	r.fetchTracker.Track(old)
	newestHash := [32]byte{0xA2}
	r.startHistoryBackfill(5, newestHash, 7, 0)
	r.armHistoryBackfill()
	require.Nil(t, r.fetchTracker.Find(old.Hash()), "newer gap must not wait for an older walk")
	require.NotNil(t, r.fetchTracker.Find(newestHash))
	require.Len(t, sender.legacyCalls(), 1)
	require.EqualValues(t, 5, sender.legacyCalls()[0].seq)

	// Completion advances backward, while a late completion for the replaced
	// acquisition cannot change the current cursor.
	r.completeHistoryBackfill(4, old.Hash(), [32]byte{0xA3}, 7)
	require.EqualValues(t, 5, r.history.seq)
	r.completeHistoryBackfill(5, newestHash, old.Hash(), 7)
	require.EqualValues(t, 4, r.history.seq)
	require.Equal(t, old.Hash(), r.history.hash)
}

func TestHistoryBackfillLocalSkipWorkIsBounded(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	defer svc.Stop()
	for svc.GetValidatedLedgerIndex() < 40 {
		_, err := svc.AcceptLedger(context.Background())
		require.NoError(t, err)
	}
	r.configureHistoryBackfill(true, 256)
	trackCatchupPeer(r, 7, 40)
	r.armHistoryBackfill()
	require.EqualValues(t, 39-historySkipBudget, r.history.seq)
	require.Empty(t, sender.legacyCalls())
	r.armHistoryBackfill()
	require.EqualValues(t, 39-2*historySkipBudget, r.history.seq)
	r.armHistoryBackfill()
	require.Zero(t, r.history.seq)
}

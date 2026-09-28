package adaptor

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func timeoutInboundAcquisition(t *testing.T, il *inbound.Ledger) {
	t.Helper()
	now := time.Now()
	for range 2 {
		now = now.Add(4 * time.Second)
		require.Equal(t, inbound.TimerEscalate, il.OnTimer(now))
	}
}

func TestIssue1863AutomaticPivotSurvivesNoProgressAndDistanceEviction(t *testing.T) {
	tests := []struct {
		name       string
		targetStep uint32
		pivotStep  bool
	}{
		{name: "no progress", targetStep: 2, pivotStep: true},
		{name: "forward distance", targetStep: maxForwardDeltaGap + 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _, _, svc := makeRouter(t)
			pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
			pivotHash := [32]byte{0x18}
			trackCatchupPeer(r, 7, pivotSeq, pivotHash)
			require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
			pivot := r.catchupReplay.fetchTracker.Find(pivotHash)
			require.NotNil(t, pivot)

			otherSeq := pivotSeq + 1
			otherHash := [32]byte{0x19}
			other := inbound.New(otherHash, otherSeq, 7, serveTestLogger())
			r.catchupReplay.fetchTracker.Track(other)
			if tt.pivotStep {
				timeoutInboundAcquisition(t, r.catchupReplay.fetchTracker.Find(pivotHash))
				timeoutInboundAcquisition(t, other)
			}

			targetSeq := pivotSeq + tt.targetStep
			r.catchupReplay.acquisitionMu.Lock()
			victim := r.catchupReplay.obsoleteCatchupVictimLocked(targetSeq)
			r.catchupReplay.acquisitionMu.Unlock()

			require.Same(t, other, victim)
			assert.Same(t, pivot, r.catchupReplay.fetchTracker.Find(pivotHash))
			assert.True(t, r.catchupReplay.standardReplay.active)
			assert.False(t, r.catchupReplay.standardReplay.pivotReady)
			assert.Equal(t, pivotHash, r.catchupReplay.standardReplay.pivotHash)
		})
	}
}

func TestIssue1863OrphanedAutomaticPivotRearmsSameGeneration(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0x1a}
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	pivot := r.catchupReplay.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivot)
	generation := r.catchupReplay.standardReplay.generation

	require.True(t, r.catchupReplay.fetchTracker.DiscardExpected(pivot))
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(pivotHash))
	require.True(t, r.catchupReplay.rebootstrapFrozenPivotIfStalled(time.Now().Add(24*time.Hour)))

	rearmed := r.catchupReplay.fetchTracker.Find(pivotHash)
	require.NotNil(t, rearmed)
	assert.NotSame(t, pivot, rearmed)
	assert.Equal(t, generation, r.catchupReplay.standardReplay.generation)
	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.False(t, r.catchupReplay.standardReplay.pivotReady)
	assert.Equal(t, pivotHash, r.catchupReplay.standardReplay.pivotHash)
	assert.Len(t, sender.legacyCalls(), 2)
}

func TestIssue1863PivotHandoffCountsCapacityAndRejectsDuplicate(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0x1b}
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	pivot := r.catchupReplay.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivot)

	r.catchupReplay.replayCommitMu.Lock()
	r.catchupReplay.acquisitionMu.Lock()
	handoff, claimed := r.catchupReplay.claimStandardReplayPivotHandoffLocked(pivot)
	require.True(t, claimed)
	require.True(t, r.catchupReplay.fetchTracker.RemoveExpectedWithSnapshot(pivot, pivot.Snapshot(), true))
	r.catchupReplay.acquisitionMu.Unlock()
	r.catchupReplay.replayCommitMu.Unlock()

	assert.True(t, r.catchupReplay.isAcquiring(pivotHash))
	assert.Equal(t, 1, r.catchupReplay.protectedCatchupInFlight())
	duplicateStarted := r.catchupReplay.startLedgerAcquisition(pivotSeq, pivotHash, 7)
	assert.True(t, duplicateStarted)
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(pivotHash))

	otherHash := [32]byte{0x1c}
	other := inbound.New(otherHash, pivotSeq+1, 7, serveTestLogger())
	r.catchupReplay.fetchTracker.Track(other)
	assert.Equal(t, maxConcurrentSpeculativeCatchup, r.catchupReplay.protectedCatchupInFlight())
	thirdHash := [32]byte{0x1d}
	assert.False(t, r.catchupReplay.startLedgerAcquisition(pivotSeq+2, thirdHash, 7))
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(thirdHash))

	r.catchupReplay.acquisitionMu.Lock()
	assert.True(t, r.catchupReplay.standardReplayPivotHandoffMatchesLocked(handoff))
	r.catchupReplay.acquisitionMu.Unlock()
}

func TestIssue1863StalePivotCompletionCannotInstallReplacementGeneration(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0x1e}
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	pivot := r.catchupReplay.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivot)

	r.catchupReplay.replayCommitMu.Lock()
	r.catchupReplay.acquisitionMu.Lock()
	handoff, claimed := r.catchupReplay.claimStandardReplayPivotHandoffLocked(pivot)
	require.True(t, claimed)
	require.True(t, r.catchupReplay.fetchTracker.RemoveExpectedWithSnapshot(pivot, pivot.Snapshot(), true))
	r.catchupReplay.acquisitionMu.Unlock()
	r.catchupReplay.replayCommitMu.Unlock()

	r.catchupReplay.replayCommitMu.Lock()
	r.catchupReplay.acquisitionMu.Lock()
	retirement := r.catchupReplay.cancelStandardReplayPipelineLocked("test_cancellation")
	r.catchupReplay.acquisitionMu.Unlock()
	r.catchupReplay.replayCommitMu.Unlock()
	r.catchupReplay.retireStandardReplay(retirement)

	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	replacementGeneration := r.catchupReplay.standardReplay.generation
	pivotHeader := header.LedgerHeader{LedgerIndex: pivotSeq, Hash: pivotHash}
	require.False(t, r.catchupReplay.completeFrozenPivotAcquisitionOwned(&pivotHeader, false, handoff))
	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.False(t, r.catchupReplay.standardReplay.pivotReady)
	assert.Equal(t, replacementGeneration, r.catchupReplay.standardReplay.generation)
	assert.Equal(t, pivotHash, r.catchupReplay.standardReplay.pivotHash)
}

func TestIssue1863MalformedPivotReplyRetiresSession(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0x1f}
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	pivot := r.catchupReplay.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivot)

	consumed := r.handleInboundLedgerData(pivot, &message.LedgerData{
		InfoType: message.LedgerInfoBase,
		Nodes:    []message.LedgerNode{{NodeData: []byte{0x01}}},
	}, 7)

	assert.True(t, consumed)
	assert.False(t, r.catchupReplay.standardReplay.active)
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(pivotHash))
}

type pivotHandoffLogBarrier struct {
	slog.Handler
	entered chan struct{}
	release <-chan struct{}
}

func (h *pivotHandoffLogBarrier) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "acquired ledger with full state from peer" {
		close(h.entered)
		<-h.release
	}
	return h.Handler.Handle(ctx, record)
}

func TestIssue1863MaintenancePreservesCompletionHandoff(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivot := completedCatchUpAcquisition(t, svc.GetClosedLedgerIndex()+10)
	r.catchupReplay.fetchTracker.Track(pivot)
	trackCatchupPeer(r, 7, pivot.Seq(), pivot.Hash())
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivot.Seq(), pivot.Hash(), 7))
	generation := r.catchupReplay.standardReplay.generation
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	r.logger = slog.New(&pivotHandoffLogBarrier{Handler: r.logger.Handler(), entered: entered, release: release})
	r.catchupReplay.logger = r.logger
	done := make(chan struct{})
	go func() {
		r.catchupReplay.completeInboundLedger(pivot)
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("completion did not reach the handoff")
	}
	require.Nil(t, r.catchupReplay.fetchTracker.Find(pivot.Hash()))
	stored, err := svc.GetLedgerByHash(pivot.Hash())
	require.NoError(t, err)
	require.Equal(t, pivot.Hash(), stored.Hash())
	r.maintenanceTick()
	require.False(t, r.catchupReplay.rebootstrapFrozenPivotIfStalled(time.Now().Add(24*time.Hour)))
	r.catchupReplay.acquisitionMu.Lock()
	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.False(t, r.catchupReplay.standardReplay.pivotReady)
	assert.Equal(t, generation, r.catchupReplay.standardReplay.generation)
	assert.NotNil(t, r.catchupReplay.standardReplay.pivotHandoff)
	r.catchupReplay.acquisitionMu.Unlock()
	unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("completion did not finish")
	}
	assert.False(t, r.catchupReplay.standardReplay.active)
	assert.True(t, r.catchupReplay.standardReplay.pivotReady)
	assert.Nil(t, r.catchupReplay.standardReplay.pivotHandoff)
}

func TestIssue1863OrphanRearmHonorsCapacityAndGeneration(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	seq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	hash := [32]byte{0x71}
	trackCatchupPeer(r, 7, seq, hash)
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(seq, hash, 7))
	generation := r.catchupReplay.standardReplay.generation
	require.True(t, r.catchupReplay.fetchTracker.DiscardExpected(r.catchupReplay.fetchTracker.Find(hash)))
	for i := range maxConcurrentSpeculativeCatchup {
		r.catchupReplay.fetchTracker.Track(inbound.New([32]byte{byte(0x72 + i)}, seq+1, 7, serveTestLogger()))
	}
	require.False(t, r.catchupReplay.rearmFrozenPivotAcquisition(generation, seq, hash, time.Now()))
	require.Nil(t, r.catchupReplay.fetchTracker.Find(hash))
	r.catchupReplay.discardFailedInboundAcquisition(r.catchupReplay.fetchTracker.Find([32]byte{0x72}), nil)
	require.True(t, r.catchupReplay.rearmFrozenPivotAcquisition(generation, seq, hash, time.Now()))
	assert.Equal(t, maxConcurrentSpeculativeCatchup, r.catchupReplay.protectedCatchupInFlight())

	r.ClearFetchInfo()
	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(seq, hash, 7))
	replacementGeneration := r.catchupReplay.standardReplay.generation
	require.Greater(t, replacementGeneration, generation)
	require.True(t, r.catchupReplay.fetchTracker.DiscardExpected(r.catchupReplay.fetchTracker.Find(hash)))
	require.False(t, r.catchupReplay.rearmFrozenPivotAcquisition(generation, seq, hash, time.Now()))
	require.Nil(t, r.catchupReplay.fetchTracker.Find(hash))
	require.True(t, r.catchupReplay.rearmFrozenPivotAcquisition(replacementGeneration, seq, hash, time.Now()))
	assert.Equal(t, replacementGeneration, r.catchupReplay.standardReplay.generation)
}

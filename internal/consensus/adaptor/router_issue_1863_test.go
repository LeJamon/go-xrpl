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
			require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
			pivot := r.fetchTracker.Find(pivotHash)
			require.NotNil(t, pivot)

			otherSeq := pivotSeq + 1
			otherHash := [32]byte{0x19}
			other := inbound.New(otherHash, otherSeq, 7, serveTestLogger())
			r.fetchTracker.Track(other)
			if tt.pivotStep {
				timeoutInboundAcquisition(t, r.fetchTracker.Find(pivotHash))
				timeoutInboundAcquisition(t, other)
			}

			targetSeq := pivotSeq + tt.targetStep
			r.acquisitionMu.Lock()
			victim := r.obsoleteCatchupVictimLocked(targetSeq)
			r.acquisitionMu.Unlock()

			require.Same(t, other, victim)
			assert.Same(t, pivot, r.fetchTracker.Find(pivotHash))
			assert.True(t, r.standardReplay.active)
			assert.False(t, r.standardReplay.pivotReady)
			assert.Equal(t, pivotHash, r.standardReplay.pivotHash)
		})
	}
}

func TestIssue1863OrphanedAutomaticPivotRearmsSameGeneration(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0x1a}
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	pivot := r.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivot)
	generation := r.standardReplay.generation

	require.True(t, r.fetchTracker.DiscardExpected(pivot))
	assert.Nil(t, r.fetchTracker.Find(pivotHash))
	require.True(t, r.rebootstrapFrozenPivotIfStalled(time.Now().Add(24*time.Hour)))

	rearmed := r.fetchTracker.Find(pivotHash)
	require.NotNil(t, rearmed)
	assert.NotSame(t, pivot, rearmed)
	assert.Equal(t, generation, r.standardReplay.generation)
	assert.True(t, r.standardReplay.active)
	assert.False(t, r.standardReplay.pivotReady)
	assert.Equal(t, pivotHash, r.standardReplay.pivotHash)
	assert.Len(t, sender.legacyCalls(), 2)
}

func TestIssue1863PivotHandoffCountsCapacityAndRejectsDuplicate(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0x1b}
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	pivot := r.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivot)

	r.replayCommitMu.Lock()
	r.acquisitionMu.Lock()
	handoff, claimed := r.claimStandardReplayPivotHandoffLocked(pivot)
	require.True(t, claimed)
	require.True(t, r.fetchTracker.RemoveExpectedWithSnapshot(pivot, pivot.Snapshot(), true))
	r.acquisitionMu.Unlock()
	r.replayCommitMu.Unlock()

	assert.True(t, r.isAcquiring(pivotHash))
	assert.Equal(t, 1, r.protectedCatchupInFlight())
	duplicateStarted := r.startLedgerAcquisition(pivotSeq, pivotHash, 7)
	assert.True(t, duplicateStarted)
	assert.Nil(t, r.fetchTracker.Find(pivotHash))

	otherHash := [32]byte{0x1c}
	other := inbound.New(otherHash, pivotSeq+1, 7, serveTestLogger())
	r.fetchTracker.Track(other)
	assert.Equal(t, maxConcurrentSpeculativeCatchup, r.protectedCatchupInFlight())
	thirdHash := [32]byte{0x1d}
	assert.False(t, r.startLedgerAcquisition(pivotSeq+2, thirdHash, 7))
	assert.Nil(t, r.fetchTracker.Find(thirdHash))

	r.acquisitionMu.Lock()
	assert.True(t, r.standardReplayPivotHandoffMatchesLocked(handoff))
	r.acquisitionMu.Unlock()
}

func TestIssue1863StalePivotCompletionCannotInstallReplacementGeneration(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0x1e}
	trackCatchupPeer(r, 7, pivotSeq, pivotHash)
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	pivot := r.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivot)

	r.replayCommitMu.Lock()
	r.acquisitionMu.Lock()
	handoff, claimed := r.claimStandardReplayPivotHandoffLocked(pivot)
	require.True(t, claimed)
	require.True(t, r.fetchTracker.RemoveExpectedWithSnapshot(pivot, pivot.Snapshot(), true))
	r.acquisitionMu.Unlock()
	r.replayCommitMu.Unlock()

	r.replayCommitMu.Lock()
	r.acquisitionMu.Lock()
	retirement := r.cancelStandardReplayPipelineLocked("test_cancellation")
	r.acquisitionMu.Unlock()
	r.replayCommitMu.Unlock()
	r.retireStandardReplay(retirement)

	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	replacementGeneration := r.standardReplay.generation
	pivotHeader := header.LedgerHeader{LedgerIndex: pivotSeq, Hash: pivotHash}
	require.False(t, r.completeFrozenPivotAcquisitionOwned(&pivotHeader, false, handoff))
	assert.True(t, r.standardReplay.active)
	assert.False(t, r.standardReplay.pivotReady)
	assert.Equal(t, replacementGeneration, r.standardReplay.generation)
	assert.Equal(t, pivotHash, r.standardReplay.pivotHash)
}

func TestIssue1863MalformedPivotReplyRetiresSession(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0x1f}
	require.True(t, r.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	pivot := r.fetchTracker.Find(pivotHash)
	require.NotNil(t, pivot)

	consumed := r.handleInboundLedgerData(pivot, &message.LedgerData{
		InfoType: message.LedgerInfoBase,
		Nodes:    []message.LedgerNode{{NodeData: []byte{0x01}}},
	}, 7)

	assert.True(t, consumed)
	assert.False(t, r.standardReplay.active)
	assert.Nil(t, r.fetchTracker.Find(pivotHash))
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
	r.fetchTracker.Track(pivot)
	trackCatchupPeer(r, 7, pivot.Seq(), pivot.Hash())
	require.True(t, r.beginFrozenPivotRecovery(pivot.Seq(), pivot.Hash(), 7))
	generation := r.standardReplay.generation
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	r.logger = slog.New(&pivotHandoffLogBarrier{Handler: r.logger.Handler(), entered: entered, release: release})
	done := make(chan struct{})
	go func() {
		r.completeInboundLedger(pivot)
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("completion did not reach the handoff")
	}
	require.Nil(t, r.fetchTracker.Find(pivot.Hash()))
	stored, err := svc.GetLedgerByHash(pivot.Hash())
	require.NoError(t, err)
	require.Equal(t, pivot.Hash(), stored.Hash())
	r.maintenanceTick()
	require.False(t, r.rebootstrapFrozenPivotIfStalled(time.Now().Add(24*time.Hour)))
	r.acquisitionMu.Lock()
	assert.True(t, r.standardReplay.active)
	assert.False(t, r.standardReplay.pivotReady)
	assert.Equal(t, generation, r.standardReplay.generation)
	assert.NotNil(t, r.standardReplay.pivotHandoff)
	r.acquisitionMu.Unlock()
	unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("completion did not finish")
	}
	assert.False(t, r.standardReplay.active)
	assert.True(t, r.standardReplay.pivotReady)
	assert.Nil(t, r.standardReplay.pivotHandoff)
}

func TestIssue1863OrphanRearmHonorsCapacityAndGeneration(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	seq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	hash := [32]byte{0x71}
	trackCatchupPeer(r, 7, seq, hash)
	require.True(t, r.beginFrozenPivotRecovery(seq, hash, 7))
	generation := r.standardReplay.generation
	require.True(t, r.fetchTracker.DiscardExpected(r.fetchTracker.Find(hash)))
	for i := range maxConcurrentSpeculativeCatchup {
		r.fetchTracker.Track(inbound.New([32]byte{byte(0x72 + i)}, seq+1, 7, serveTestLogger()))
	}
	require.False(t, r.rearmFrozenPivotAcquisition(generation, seq, hash, time.Now()))
	require.Nil(t, r.fetchTracker.Find(hash))
	r.discardFailedInboundAcquisition(r.fetchTracker.Find([32]byte{0x72}))
	require.True(t, r.rearmFrozenPivotAcquisition(generation, seq, hash, time.Now()))
	assert.Equal(t, maxConcurrentSpeculativeCatchup, r.protectedCatchupInFlight())

	r.ClearFetchInfo()
	require.True(t, r.beginFrozenPivotRecovery(seq, hash, 7))
	replacementGeneration := r.standardReplay.generation
	require.Greater(t, replacementGeneration, generation)
	require.True(t, r.fetchTracker.DiscardExpected(r.fetchTracker.Find(hash)))
	require.False(t, r.rearmFrozenPivotAcquisition(generation, seq, hash, time.Now()))
	require.Nil(t, r.fetchTracker.Find(hash))
	require.True(t, r.rearmFrozenPivotAcquisition(replacementGeneration, seq, hash, time.Now()))
	assert.Equal(t, replacementGeneration, r.standardReplay.generation)
}

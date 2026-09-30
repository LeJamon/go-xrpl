package adaptor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFrozenPivotRecoveryDefersAtCapacityAndRetriesOnePendingIntent(t *testing.T) {
	r, sender, svc := makeProvisionalWarmRouter(t)
	closed := svc.GetClosedLedgerIndex()
	survivorSeq := closed + maxForwardDeltaGap + 1
	survivorHash := [32]byte{0xe1}
	targetSeq := survivorSeq + 2
	targetHash := [32]byte{0xe2}

	r.catchupReplay.startLedgerAcquisition(survivorSeq, survivorHash, 7)
	survivor := r.catchupReplay.fetchTracker.Find(survivorHash)
	require.NotNil(t, survivor)
	generation := r.catchupReplay.standardReplay.generation

	assert.False(t, r.catchupReplay.beginFrozenPivotRecovery(targetSeq, targetHash, 8))
	assert.Equal(t, generation, r.catchupReplay.standardReplay.generation)
	assert.False(t, r.catchupReplay.standardReplay.active)
	assert.Same(t, survivor, r.catchupReplay.fetchTracker.Find(survivorHash))
	r.catchupReplay.acquisitionMu.Lock()
	assert.Equal(t, frozenPivotPendingIntent{seq: targetSeq, hash: targetHash, peerID: 8}, r.catchupReplay.pendingFrozenPivot)
	r.catchupReplay.acquisitionMu.Unlock()
	assert.Len(t, sender.legacyCalls(), 1)

	// A terminal acquisition wakeup retries the same intent after the sole
	// provisional full-state slot is released; it does not create generations
	// while the survivor still owns that slot.
	r.catchupReplay.failInboundAcquisition(survivor)
	require.Eventually(t, func() bool {
		r.catchupReplay.acquisitionMu.Lock()
		defer r.catchupReplay.acquisitionMu.Unlock()
		return r.catchupReplay.standardReplay.active &&
			r.catchupReplay.standardReplay.pivotHash == targetHash
	}, time.Second, time.Millisecond)
	assert.Equal(t, generation+1, r.catchupReplay.standardReplay.generation)
	assert.NotNil(t, r.catchupReplay.fetchTracker.Find(targetHash))
	assert.Len(t, sender.legacyCalls(), 2)
}

func TestFrozenPivotRecoveryBlocksRegularFullStateDuringActiveSession(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xe3}
	otherHash := [32]byte{0xe4}
	targetHash := [32]byte{0xe5}

	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	generation := r.catchupReplay.standardReplay.generation

	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.standardReplay.pivotReady = true
	r.catchupReplay.standardReplay.targetSeq = pivotSeq + 1
	r.catchupReplay.standardReplay.targetHash = targetHash
	r.catchupReplay.standardReplay.entries[pivotSeq+1] = &standardReplayEntry{
		generation: r.catchupReplay.standardReplay.generation,
		seq:        pivotSeq + 1,
		hash:       targetHash,
	}
	// A prepared target is still owned by the replay pipeline; a regular
	// validation/consensus caller cannot start a second full-state walk for it.
	r.catchupReplay.startLedgerAcquisitionLegacyLocked(pivotSeq+1, targetHash, 8)
	r.catchupReplay.startLedgerAcquisitionLegacyLocked(pivotSeq+1, otherHash, 8)
	r.catchupReplay.acquisitionMu.Unlock()

	assert.Nil(t, r.catchupReplay.fetchTracker.Find(targetHash))
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(otherHash))
	assert.Equal(t, generation, r.catchupReplay.standardReplay.generation)
	assert.Len(t, sender.legacyCalls(), 1)
}

func TestFrozenPivotReplacementAdmissionExplicitlyBypassesReplayOwner(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	pivotSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	pivotHash := [32]byte{0xe5}
	replacementHash := [32]byte{0xe6}

	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(pivotSeq, pivotHash, 7))
	r.catchupReplay.acquisitionMu.Lock()
	admission := r.catchupReplay.startFrozenPivotReplacementLocked(pivotSeq+1, replacementHash, 8)
	r.catchupReplay.acquisitionMu.Unlock()

	assert.Equal(t, fullStateAdmissionStarted, admission.outcome)
	assert.NotNil(t, admission.acquisition)
	assert.NotNil(t, r.catchupReplay.fetchTracker.Find(replacementHash))
	assert.Len(t, sender.legacyCalls(), 2)
}

func TestKnownFrozenPivotAdoptionRequiresVerifiedTrustedAncestry(t *testing.T) {
	t.Run("acquired chain to trusted target", func(t *testing.T) {
		r, _, _, svc := makeRouter(t)
		pivotSeq := svc.GetClosedLedgerIndex() + 10
		pivotHash := [32]byte{0xe7}
		targetSeq := pivotSeq + 1
		targetHash := [32]byte{0xe9}

		r.catchupReplay.recordAcquiredSeqHash(targetSeq, targetHash, pivotHash)
		r.catchupReplay.recordValidationCatchupTarget(targetSeq, targetHash, 7, catchupSourceQuorum)

		assert.True(t, r.catchupReplay.canAdoptKnownFrozenPivot(pivotSeq, pivotHash, targetSeq, targetHash))
	})

	t.Run("peer target or missing link is rejected", func(t *testing.T) {
		r, _, _, svc := makeRouter(t)
		pivotSeq := svc.GetClosedLedgerIndex() + 10
		pivotHash := [32]byte{0xea}
		targetSeq := pivotSeq + 2
		targetHash := [32]byte{0xeb}

		// The sequence is nearby, but the intermediate parent link is absent.
		r.catchupReplay.recordAcquiredSeqHash(targetSeq, targetHash, [32]byte{0xec})
		r.catchupReplay.recordCatchupTarget(targetSeq, targetHash, 7)
		assert.False(t, r.catchupReplay.canAdoptKnownFrozenPivot(pivotSeq, pivotHash, targetSeq, targetHash))

		// Even a complete link is not adoptable when the current target is only
		// peer gossip. This uses a fresh coordinator so the target can be reset.
		r2, _, _, svc2 := makeRouter(t)
		pivotSeq2 := svc2.GetClosedLedgerIndex() + 10
		pivotHash2 := [32]byte{0xed}
		targetSeq2 := pivotSeq2 + 1
		targetHash2 := [32]byte{0xee}
		r2.catchupReplay.recordAcquiredSeqHash(targetSeq2, targetHash2, pivotHash2)
		r2.catchupReplay.recordCatchupTarget(targetSeq2, targetHash2, 7)
		assert.False(t, r2.catchupReplay.canAdoptKnownFrozenPivot(pivotSeq2, pivotHash2, targetSeq2, targetHash2))
	})
}

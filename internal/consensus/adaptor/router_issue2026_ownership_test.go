package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
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

func TestFrozenPivotRecoveryAdoptsVerifiedKnownSequenceSurvivor(t *testing.T) {
	r, sender, svc := makeProvisionalWarmRouter(t)
	sender.mu.Lock()
	sender.peerSupportsReplay = false
	sender.mu.Unlock()
	closed := svc.GetClosedLedgerIndex()
	pivotSeq := closed + 10
	pivotHash := [32]byte{0xf1}
	successorHash := [32]byte{0xf2}
	targetSeq := pivotSeq + 2
	targetHash := [32]byte{0xf3}
	trackCatchupPeer(r, 8, targetSeq, targetHash)

	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.startLedgerAcquisitionLegacyLocked(pivotSeq, pivotHash, 7)
	r.catchupReplay.acquisitionMu.Unlock()
	survivor := r.catchupReplay.fetchTracker.Find(pivotHash)
	require.NotNil(t, survivor)
	require.False(t, survivor.TransactionOnly())

	r.catchupReplay.recordAcquiredSeqHash(pivotSeq+1, successorHash, pivotHash)
	r.catchupReplay.recordAcquiredSeqHash(targetSeq, targetHash, successorHash)
	r.catchupReplay.recordValidationCatchupTarget(targetSeq, targetHash, 8, catchupSourceQuorum)

	require.True(t, r.catchupReplay.beginFrozenPivotRecovery(targetSeq, targetHash, 8))
	r.catchupReplay.acquisitionMu.Lock()
	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.Equal(t, pivotSeq, r.catchupReplay.standardReplay.pivotSeq)
	assert.Equal(t, pivotHash, r.catchupReplay.standardReplay.pivotHash)
	assert.Equal(t, targetSeq, r.catchupReplay.standardReplay.targetSeq)
	assert.Equal(t, targetHash, r.catchupReplay.standardReplay.targetHash)
	r.catchupReplay.acquisitionMu.Unlock()
	assert.Same(t, survivor, r.catchupReplay.fetchTracker.Find(pivotHash))
	successor := r.catchupReplay.fetchTracker.Find(successorHash)
	require.NotNil(t, successor)
	assert.True(t, successor.TransactionOnly())
	calls := sender.legacyCalls()
	require.Len(t, calls, 3)
	assert.Equal(t, pivotSeq, calls[0].seq)
	assert.Equal(t, pivotHash, calls[0].hash)
	assert.Equal(t, pivotSeq+1, calls[1].seq)
	assert.Equal(t, targetSeq, calls[2].seq)
}

func TestFrozenPivotRecoveryStartsHeaderDiscoveryForUnknownSurvivorAncestry(t *testing.T) {
	r, sender, svc := makeProvisionalWarmRouter(t)
	closed := svc.GetClosedLedgerIndex()
	pivotSeq := closed + 10
	pivotHash := [32]byte{0xf4}
	targetSeq := pivotSeq + 2
	targetHash := [32]byte{0xf5}

	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.startLedgerAcquisitionLegacyLocked(pivotSeq, pivotHash, 7)
	r.catchupReplay.acquisitionMu.Unlock()
	require.NotNil(t, r.catchupReplay.fetchTracker.Find(pivotHash))
	r.catchupReplay.recordValidationCatchupTarget(targetSeq, targetHash, 8, catchupSourceQuorum)

	assert.False(t, r.catchupReplay.beginFrozenPivotRecovery(targetSeq, targetHash, 8))
	r.catchupReplay.headerDiscoveryMu.Lock()
	discovery := r.catchupReplay.headerDiscovery
	r.catchupReplay.headerDiscoveryMu.Unlock()
	require.NotNil(t, discovery)
	assert.Equal(t, targetSeq, discovery.targetSeq)
	assert.Equal(t, targetHash, discovery.targetHash)
	requests := sender.headerRequests()
	require.Len(t, requests, 1)
	assert.Equal(t, targetSeq, requests[0].seq)
	assert.Equal(t, targetHash, requests[0].hash)
	assert.Len(t, sender.legacyCalls(), 1)
}

func TestFullStateAdmissionDefersUnrelatedGenericAcquisition(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	targetSeq := svc.GetClosedLedgerIndex() + maxForwardDeltaGap + 1
	targetHash := [32]byte{0xf6}
	trackCatchupPeer(r, 7, targetSeq, targetHash)
	_, started := r.catchupReplay.startGenericAcquisition(targetHash, targetSeq)
	require.True(t, started)
	generic := r.catchupReplay.fetchTracker.Find(targetHash)
	require.NotNil(t, generic)
	require.Equal(t, inbound.ReasonGeneric, generic.Reason())

	r.catchupReplay.acquisitionMu.Lock()
	admission := r.catchupReplay.admitFullStateLocked(
		targetSeq, targetHash, 7, fullStateAdmissionCatchup,
	)
	r.catchupReplay.acquisitionMu.Unlock()

	assert.Equal(t, fullStateAdmissionDeferred, admission.outcome)
	assert.Same(t, generic, r.catchupReplay.fetchTracker.Find(targetHash))
	assert.Len(t, sender.legacyCalls(), 1)
}

func TestPendingFrozenPivotUsesLatestTrustedTarget(t *testing.T) {
	r, sender, svc := makeProvisionalWarmRouter(t)
	closed := svc.GetClosedLedgerIndex()
	oldSeq := closed + 10
	oldHash := [32]byte{0xf7}
	newHash := [32]byte{0xf8}

	r.catchupReplay.acquisitionMu.Lock()
	r.catchupReplay.rememberFrozenPivotPendingLocked(oldSeq, oldHash, 7)
	r.catchupReplay.acquisitionMu.Unlock()
	r.catchupReplay.recordValidationCatchupTarget(oldSeq, newHash, 8, catchupSourceQuorum)

	require.True(t, r.catchupReplay.retryPendingFrozenPivot())
	r.catchupReplay.acquisitionMu.Lock()
	assert.True(t, r.catchupReplay.standardReplay.active)
	assert.Equal(t, oldSeq, r.catchupReplay.standardReplay.targetSeq)
	assert.Equal(t, newHash, r.catchupReplay.standardReplay.targetHash)
	r.catchupReplay.acquisitionMu.Unlock()
	assert.Nil(t, r.catchupReplay.fetchTracker.Find(oldHash))
	assert.NotNil(t, r.catchupReplay.fetchTracker.Find(newHash))
	assert.Len(t, sender.legacyCalls(), 1)
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

func TestFrozenPivotAncestryDiscoveryDoesNotTrustPeerTarget(t *testing.T) {
	r, _, svc := makeProvisionalWarmRouter(t)
	c := r.catchupReplay
	seq := svc.GetClosedLedgerIndex() + 10
	pivot, target := [32]byte{0x91}, [32]byte{0x92}
	c.startLedgerAcquisitionLegacy(seq, pivot, 7)
	c.recordCatchupTarget(seq+2, target, 7)
	candidate, missingAncestry := c.knownFrozenPivotCandidate(seq+2, target)
	assert.Nil(t, candidate)
	assert.False(t, missingAncestry)
}

func TestFullStateAdmissionRejectsConflictingKnownSequence(t *testing.T) {
	r, _, svc := makeProvisionalWarmRouter(t)
	c := r.catchupReplay
	seq, hash := svc.GetClosedLedgerIndex()+10, [32]byte{0x93}
	c.startLedgerAcquisitionLegacy(seq, hash, 7)
	original := c.fetchTracker.Find(hash)
	require.NotNil(t, original)
	c.acquisitionMu.Lock()
	admission := c.admitFullStateLocked(seq+1, hash, 7, fullStateAdmissionCatchup)
	c.acquisitionMu.Unlock()
	assert.Equal(t, fullStateAdmissionRejected, admission.outcome)
	assert.Equal(t, seq, original.Seq())
	assert.False(t, c.standardReplay.active)
}

func TestPendingFrozenPivotPrefersTrustedTargetOverHigherPendingSequence(t *testing.T) {
	r, _, svc := makeProvisionalWarmRouter(t)
	c := r.catchupReplay
	seq := svc.GetClosedLedgerIndex() + 10
	pendingHash, trustedHash := [32]byte{0x94}, [32]byte{0x95}
	c.acquisitionMu.Lock()
	c.rememberFrozenPivotPendingLocked(seq+1, pendingHash, 7)
	c.acquisitionMu.Unlock()
	c.recordValidationCatchupTarget(seq, trustedHash, 8, catchupSourceQuorum)
	require.True(t, c.retryPendingFrozenPivot())
	assert.Equal(t, trustedHash, c.standardReplay.targetHash)
	assert.Nil(t, c.fetchTracker.Find(pendingHash))
}

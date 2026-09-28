package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplayFallbackHistoryBoundsActiveGenericAcquisitions(t *testing.T) {
	r := newTestRouter(nil, newTestAdaptor(t), make(chan *peermanagement.InboundMessage, 1))
	c := r.catchupReplay
	c.acquisitionMu.Lock()
	c.consensusRecovery.targetHash = [32]byte{0xF1, 0}
	c.consensusRecovery.stepHash = [32]byte{0xF1, 1}
	active := make([]*inbound.Ledger, 0, replayFallbackHistoryLimit+8)
	for i := range replayFallbackHistoryLimit + 8 {
		hash := [32]byte{0xF1, byte(i)}
		seq := uint32(i + 100)
		il := inbound.NewGeneric(hash, seq, 1, serveTestLogger())
		c.fetchTracker.Track(il)
		active = append(active, il)
		c.requireReplayFullStateLocked(seq, hash)
		require.LessOrEqual(t, len(c.replayFallbackRequired), replayFallbackHistoryLimit)
	}

	assert.Contains(t, c.replayFallbackRequired, c.consensusRecovery.targetHash)
	assert.Contains(t, c.replayFallbackRequired, c.consensusRecovery.stepHash)
	for _, il := range active {
		require.True(t, c.replayNeedsFullStateLocked(il.Hash()), "active fallback must remain protected")
	}

	evicted := active[2]
	require.NotContains(t, c.replayFallbackRequired, evicted.Hash())
	require.True(t, evicted.FullStateRequired())
	joined, created := c.startLedgerReplayAcquisitionLegacyLocked(evicted.Seq(), evicted.Hash(), 1)
	require.False(t, created)
	require.Same(t, evicted, joined)
	assert.False(t, joined.TransactionOnly())
	require.ErrorContains(t, c.startReplayDeltaAcquisition(evicted.Seq(), evicted.Hash(), 1, nil), "requires full-state acquisition")
	assert.False(t, c.replayer.Has(evicted.Hash()))
	c.acquisitionMu.Unlock()

	_, _, removed := c.removeInboundAcquisitionWithSession(evicted, evicted.Snapshot(), false)
	require.True(t, removed)
	c.acquisitionMu.Lock()
	require.True(t, c.replayNeedsFullStateLocked(evicted.Hash()), "failed acquisition must retain fallback across retry")
	assert.LessOrEqual(t, len(c.replayFallbackRequired), replayFallbackHistoryLimit)
	retried, created := c.startLedgerReplayAcquisitionLegacyLocked(evicted.Seq(), evicted.Hash(), 1)
	require.False(t, created)
	require.NotNil(t, retried)
	require.NotSame(t, evicted, retried)
	assert.False(t, retried.TransactionOnly())
	c.acquisitionMu.Unlock()

	canceled := active[3]
	require.True(t, canceled.FullStateRequired())
	c.acquisitionMu.Lock()
	require.True(t, c.discardInboundAcquisitionLocked(canceled))
	assert.True(t, c.replayNeedsFullStateLocked(canceled.Hash()), "cancellation must retain fallback across retry")
	assert.True(t, c.replayNeedsFullStateLocked(retried.Hash()), "restoring history must preserve other active retries")
	assert.LessOrEqual(t, len(c.replayFallbackRequired), replayFallbackHistoryLimit)
	c.acquisitionMu.Unlock()
	c.retireLegacyAcquisitions([]*inbound.Ledger{canceled})
}

func TestReplayFallbackRetirementDoesNotRestoreCompletedReplacement(t *testing.T) {
	r := newTestRouter(nil, newTestAdaptor(t), make(chan *peermanagement.InboundMessage, 1))
	c := r.catchupReplay
	hash := [32]byte{0xF2}
	old := inbound.NewGeneric(hash, 100, 1, serveTestLogger())
	old.RequireFullState()
	c.fetchTracker.Track(old)

	c.acquisitionMu.Lock()
	require.True(t, c.discardInboundAcquisitionLocked(old))
	require.True(t, c.replayNeedsFullStateLocked(hash))
	replacement := inbound.NewGeneric(hash, 100, 1, serveTestLogger())
	c.fetchTracker.Track(replacement)
	require.True(t, c.fetchTracker.RemoveExpectedWithSnapshot(replacement, replacement.Snapshot(), true))
	// The replacement completes before the old acquisition's storage work retires.
	delete(c.replayFallbackRequired, hash)
	c.acquisitionMu.Unlock()

	c.retireLegacyAcquisitions([]*inbound.Ledger{old})
	c.acquisitionMu.Lock()
	assert.False(t, c.replayNeedsFullStateLocked(hash))
	c.acquisitionMu.Unlock()
}

func TestReplayFallbackClearRetainsActiveRequirement(t *testing.T) {
	r := newTestRouter(nil, newTestAdaptor(t), make(chan *peermanagement.InboundMessage, 1))
	c := r.catchupReplay
	hash := [32]byte{0xF3}
	il := inbound.NewGeneric(hash, 100, 1, serveTestLogger())
	il.RequireFullState()
	c.fetchTracker.Track(il)

	c.ClearFetchInfo()

	c.acquisitionMu.Lock()
	assert.Nil(t, c.fetchTracker.Find(hash))
	assert.True(t, c.replayNeedsFullStateLocked(hash))
	c.acquisitionMu.Unlock()
}

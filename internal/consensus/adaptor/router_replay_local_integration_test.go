package adaptor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalReplayReplacementResumesWithoutPeer(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	c := r.catchupReplay
	completeStandardReplayTestLink(t, r, links[0])
	completeStandardReplayTestLink(t, r, links[2])
	storeRecoveryLedger(t, svc, links[1].ledger)
	c.peersMu.Lock()
	for id := range c.peerStates {
		delete(c.peerStates, id)
	}
	c.peersMu.Unlock()
	_, available := c.resolveAcquisitionPeer(links[1].seq, 0)
	require.False(t, available)
	requests := len(sender.legacyCalls())
	generation := c.standardReplay.generation
	require.True(t, c.reserveStandardReplayReplacement(generation, links[1].seq, links[1].hash, 0, time.Now()))
	drainStandardReplayTestPipeline(t, r)
	assert.Nil(t, c.standardReplay.replacement)
	assert.False(t, c.standardReplay.active)
	assert.Equal(t, generation, c.standardReplay.generation)
	assert.Len(t, sender.legacyCalls(), requests)
	assert.Equal(t, uint64(2), c.replayPipelineApplied.Load())
	held, err := svc.GetLedgerByHash(links[2].hash)
	require.NoError(t, err)
	require.NotNil(t, held)
}

func TestLocalReplayReplacementRequiresCurrentIdentity(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 2)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	c := r.catchupReplay
	generation, anchor := c.standardReplay.generation, c.standardReplay.anchorHash
	c.peersMu.Lock()
	for id := range c.peerStates {
		delete(c.peerStates, id)
	}
	c.peersMu.Unlock()
	require.True(t, c.reserveStandardReplayReplacement(generation, links[0].seq, links[0].hash, 0, time.Now()))
	replacement := c.standardReplay.replacement
	require.NotNil(t, replacement)
	require.Nil(t, replacement.acquisition)
	storeRecoveryLedger(t, svc, links[0].ledger)
	h, state, txs, err := c.localReplayReplacementCandidate(links[0].seq, links[0].hash)
	require.NoError(t, err)
	stale := *replacement
	assert.False(t, c.completeLocalStandardReplayReplacement(generation, &stale, h, state, txs))
	assert.False(t, c.completeLocalStandardReplayReplacement(generation+1, replacement, h, state, txs))
	assert.Equal(t, anchor, c.standardReplay.anchorHash)
	assert.True(t, c.completeLocalStandardReplayReplacement(generation, replacement, h, state, txs))
	assert.Equal(t, links[0].hash, c.standardReplay.anchorHash)
	assert.False(t, c.completeLocalStandardReplayReplacement(generation, replacement, h, state, txs))
}

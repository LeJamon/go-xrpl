package adaptor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type replayReplacementRetirementFamily struct {
	retired atomic.Uint32
}

func (f *replayReplacementRetirementFamily) Fetch(context.Context, [32]byte) ([]byte, error) {
	return nil, nil
}

func (f *replayReplacementRetirementFamily) StoreBatch(context.Context, []shamap.FlushEntry) error {
	return nil
}

func (f *replayReplacementRetirementFamily) Retire(context.Context) error {
	f.retired.Add(1)
	return nil
}

func newReplayReplacementRetirementAcquisition(
	t *testing.T,
	c *catchupReplayCoordinator,
	seq uint32,
	hash [32]byte,
) (*inbound.Ledger, *replayReplacementRetirementFamily) {
	t.Helper()
	family := &replayReplacementRetirementFamily{}
	acquisition := inbound.New(hash, seq, 7, c.logger, inbound.WithFamily(family))
	c.fetchTracker.Track(acquisition)
	return acquisition, family
}

func TestStandardReplayBaseRetiresObsoleteReplacementAfterStoredAdvance(t *testing.T) {
	for _, tc := range []struct {
		name             string
		replacementIndex int
		matchingStep     bool
	}{
		{name: "at replacement", replacementIndex: 2, matchingStep: true},
		{name: "past replacement", replacementIndex: 1, matchingStep: true},
		{name: "unrelated recovery step", replacementIndex: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, sender, svc := makeRouter(t)
			_, err := svc.AcceptLedger(t.Context())
			require.NoError(t, err)
			base := svc.GetClosedLedger()
			links := buildStandardReplayTestChain(t, r, base, 4)
			for _, link := range links[:3] {
				storeRecoveryLedger(t, svc, link.ledger)
			}

			c := r.catchupReplay
			const generation = 41
			buffered := &standardReplayEntry{
				generation: generation,
				seq:        links[3].seq,
				hash:       links[3].hash,
				parentHash: links[2].hash,
			}
			replacementAcquisition, retirementFamily := newReplayReplacementRetirementAcquisition(
				t, c, links[tc.replacementIndex].seq, links[tc.replacementIndex].hash,
			)
			replacement := &standardReplayReplacement{
				generation:  generation,
				seq:         links[tc.replacementIndex].seq,
				hash:        links[tc.replacementIndex].hash,
				peerID:      7,
				acquisition: replacementAcquisition,
			}
			c.standardReplay = standardReplayPipeline{
				generation:  generation,
				active:      true,
				pivotReady:  true,
				pivotSeq:    base.Sequence(),
				pivotHash:   base.Hash(),
				anchorSeq:   base.Sequence(),
				anchorHash:  base.Hash(),
				collectSeq:  base.Sequence(),
				collectHash: base.Hash(),
				targetSeq:   links[3].seq,
				targetHash:  links[3].hash,
				entries:     map[uint32]*standardReplayEntry{buffered.seq: buffered},
				replacement: replacement,
			}
			stepHash := [32]byte{0xee}
			if tc.matchingStep {
				stepHash = replacement.hash
			}
			c.consensusRecovery = consensusRecovery{targetHash: links[3].hash, stepHash: stepHash}
			c.acquisitionMu.Lock()
			identity := c.standardReplayIdentityLocked()
			c.acquisitionMu.Unlock()

			// No peer is available and no suffix is ready; the only progress comes
			// from the complete locally stored prefix.
			r.setPeerSessionView(&testPeerSessions{})
			_, connected := c.resolveAcquisitionPeer(replacement.seq, replacement.peerID)
			require.False(t, connected)
			_, updated, current := c.standardReplayBase(svc, base, links[3].seq, links[3].hash)
			require.True(t, current)
			require.Equal(t, identity.generation, updated.generation)
			require.Equal(t, links[2].seq, updated.anchorSeq)
			require.Equal(t, links[2].hash, updated.anchorHash)
			require.Same(t, buffered, c.standardReplay.entries[buffered.seq])
			require.Nil(t, c.standardReplay.replacement)
			require.Nil(t, c.fetchTracker.Find(replacement.hash))
			require.Equal(t, uint32(1), retirementFamily.retired.Load())
			if tc.matchingStep {
				assert.Zero(t, c.consensusRecovery.stepHash)
			} else {
				assert.Equal(t, stepHash, c.consensusRecovery.stepHash)
			}
			assert.Zero(t, c.FastSyncMetrics().ReplayPipelineReadyDepth)
			assert.Empty(t, sender.legacyCalls())
		})
	}
}

func TestRetryStandardReplayReplacementRetiresObsoleteBeforeRetryGates(t *testing.T) {
	cases := []struct {
		name    string
		retryAt bool
		tracked bool
	}{
		{name: "cooldown", retryAt: true, tracked: true},
		{name: "no peer", tracked: false},
		{name: "tracked live acquisition", tracked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _, sender, svc := makeRouter(t)
			base := svc.GetClosedLedger()
			seq := base.Sequence() + 1
			hash := [32]byte{0xa2}
			c := r.catchupReplay
			var acquisition *inbound.Ledger
			var retirementFamily *replayReplacementRetirementFamily
			if tc.tracked {
				acquisition, retirementFamily = newReplayReplacementRetirementAcquisition(t, c, seq, hash)
			}
			now := time.Unix(100, 0)
			replacement := &standardReplayReplacement{
				generation:  7,
				seq:         seq,
				hash:        hash,
				peerID:      7,
				acquisition: acquisition,
			}
			if tc.retryAt {
				replacement.retryAt = now.Add(time.Hour)
			}
			c.standardReplay = standardReplayPipeline{
				generation:  7,
				active:      true,
				anchorSeq:   seq,
				anchorHash:  hash,
				targetSeq:   seq,
				targetHash:  hash,
				replacement: replacement,
			}
			c.consensusRecovery.stepHash = hash
			r.setPeerSessionView(&testPeerSessions{})
			_, connected := c.resolveAcquisitionPeer(replacement.seq, replacement.peerID)
			require.False(t, connected)

			c.retryStandardReplayReplacement(now)

			assert.Nil(t, c.standardReplay.replacement)
			assert.Zero(t, c.consensusRecovery.stepHash)
			if tc.tracked {
				assert.Nil(t, c.fetchTracker.Find(hash))
				assert.Equal(t, uint32(1), retirementFamily.retired.Load())
			}
			assert.Empty(t, sender.legacyCalls())
		})
	}
}

func TestRetryStandardReplayReplacementWaitsForReplayCommitBeforeRetirement(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	seq := base.Sequence() + 1
	hash := [32]byte{0xa3}
	c := r.catchupReplay
	acquisition, retirementFamily := newReplayReplacementRetirementAcquisition(t, c, seq, hash)
	replacement := &standardReplayReplacement{
		generation:  9,
		seq:         seq,
		hash:        hash,
		acquisition: acquisition,
	}
	c.standardReplay = standardReplayPipeline{
		generation:  9,
		active:      true,
		anchorSeq:   seq,
		anchorHash:  hash,
		replacement: replacement,
	}
	c.consensusRecovery.stepHash = hash

	done := make(chan struct{})
	func() {
		c.replayCommitMu.Lock()
		defer c.replayCommitMu.Unlock()
		go func() {
			c.retryStandardReplayReplacement(time.Now())
			close(done)
		}()
		select {
		case <-done:
			t.Fatal("replacement retry returned while replay commit was held")
		case <-time.After(50 * time.Millisecond):
		}
		c.acquisitionMu.Lock()
		assert.Same(t, replacement, c.standardReplay.replacement)
		c.acquisitionMu.Unlock()
		assert.Zero(t, retirementFamily.retired.Load())
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("replacement retry did not finish after replay commit release")
	}
	assert.Nil(t, c.standardReplay.replacement)
	assert.Equal(t, uint32(1), retirementFamily.retired.Load())
}

func TestReplayReplacementLateCallbacksCannotRetireFreshSameHashAcquisition(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	seq := base.Sequence() + 1
	hash := [32]byte{0xa4}
	c := r.catchupReplay

	oldAcquisition, oldFamily := newReplayReplacementRetirementAcquisition(t, c, seq, hash)
	oldReplacement := &standardReplayReplacement{
		generation:  1,
		seq:         seq,
		hash:        hash,
		acquisition: oldAcquisition,
	}
	c.standardReplay = standardReplayPipeline{
		generation:  1,
		active:      true,
		pivotReady:  true,
		anchorSeq:   seq,
		anchorHash:  hash,
		targetSeq:   seq,
		targetHash:  hash,
		replacement: oldReplacement,
	}
	oldIdentity := c.standardReplayIdentityLocked()
	c.retryStandardReplayReplacement(time.Now())
	require.Nil(t, c.standardReplay.replacement)
	c.retryStandardReplayReplacement(time.Now())
	require.Equal(t, uint32(1), oldFamily.retired.Load())

	newAcquisition, newFamily := newReplayReplacementRetirementAcquisition(t, c, seq, hash)
	newReplacement := &standardReplayReplacement{
		generation:  2,
		seq:         seq,
		hash:        hash,
		acquisition: newAcquisition,
	}
	c.standardReplay.generation = 2
	c.standardReplay.anchorSeq, c.standardReplay.anchorHash = base.Sequence(), base.Hash()
	c.standardReplay.replacement = newReplacement
	c.consensusRecovery = consensusRecovery{targetHash: hash, stepHash: hash}
	currentIdentity := c.standardReplayIdentityLocked()

	_, current := c.cancelStandardReplayPipelineIdentity(oldIdentity, "late_stale_cancellation")
	require.False(t, current)
	require.False(t, c.failStandardReplayReplacement(oldAcquisition, errors.New("late failure")))
	require.False(t, c.completeStandardReplayReplacement(oldAcquisition, nil, nil, nil))
	c.failInboundAcquisition(oldAcquisition)

	assert.Equal(t, currentIdentity, c.standardReplayIdentityLocked())
	assert.Same(t, newAcquisition, c.fetchTracker.Find(hash))
	assert.Same(t, newReplacement, c.standardReplay.replacement)
	assert.Equal(t, hash, c.consensusRecovery.stepHash)
	assert.Equal(t, uint32(1), oldFamily.retired.Load())
	assert.Zero(t, newFamily.retired.Load())
}

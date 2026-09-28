package adaptor

import "github.com/LeJamon/go-xrpl/internal/ledger/inbound"

const replayFallbackHistoryLimit = 64

// acquisitionMu protects these markers across pipeline cancellation and fetch
// retries. A child hash commits to its parent, so retries cannot repair the same
// replay by selecting a different parent.
func (c *catchupReplayCoordinator) replayNeedsFullStateLocked(hash [32]byte) bool {
	if _, required := c.replayFallbackRequired[hash]; required {
		return true
	}
	il := c.fetchTracker.Find(hash)
	return il != nil && il.FullStateRequired()
}

func (c *catchupReplayCoordinator) requireReplayFullStateLocked(seq uint32, hash [32]byte) {
	if c.replayFallbackRequired == nil {
		c.replayFallbackRequired = make(map[[32]byte]uint32)
	}
	if _, required := c.replayFallbackRequired[hash]; required {
		return
	}
	if len(c.replayFallbackRequired) >= replayFallbackHistoryLimit {
		var oldest [32]byte
		oldestSeq := ^uint32(0)
		found := false
		for candidate, candidateSeq := range c.replayFallbackRequired {
			if candidate == c.consensusRecovery.targetHash || candidate == c.consensusRecovery.stepHash ||
				c.replayer.Has(candidate) ||
				(c.standardReplay.pivotHandoff != nil && c.standardReplay.pivotHandoff.acquisition.Hash() == candidate) {
				continue
			}
			if !found || candidateSeq < oldestSeq {
				oldest, oldestSeq = candidate, candidateSeq
				found = true
			}
		}
		// Active acquisitions retain the requirement after its history entry expires.
		if il := c.fetchTracker.Find(oldest); il != nil {
			il.RequireFullState()
		}
		delete(c.replayFallbackRequired, oldest)
	}
	c.replayFallbackRequired[hash] = seq
}

func (c *catchupReplayCoordinator) restoreReplayFallback(il *inbound.Ledger) {
	if !il.FullStateRequired() || c.stoppedForShutdown() {
		return
	}
	c.acquisitionMu.Lock()
	c.requireReplayFullStateLocked(il.Seq(), il.Hash())
	c.acquisitionMu.Unlock()
}

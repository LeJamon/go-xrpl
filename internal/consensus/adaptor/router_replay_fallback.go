package adaptor

const replayFallbackHistoryLimit = 64

// acquisitionMu protects these markers across pipeline cancellation and fetch
// retries. A child hash commits to its parent, so retries cannot repair the same
// replay by selecting a different parent.
func (c *catchupReplayCoordinator) replayNeedsFullStateLocked(hash [32]byte) bool {
	_, required := c.replayFallbackRequired[hash]
	return required
}

func (c *catchupReplayCoordinator) requireReplayFullStateLocked(seq uint32, hash [32]byte) {
	if c.replayFallbackRequired == nil {
		c.replayFallbackRequired = make(map[[32]byte]uint32)
	}
	if c.replayNeedsFullStateLocked(hash) {
		return
	}
	if len(c.replayFallbackRequired) >= replayFallbackHistoryLimit {
		var oldest [32]byte
		oldestSeq := ^uint32(0)
		for candidate, candidateSeq := range c.replayFallbackRequired {
			if candidate == c.consensusRecovery.targetHash || candidate == c.consensusRecovery.stepHash ||
				c.isAcquiringLocked(candidate) {
				continue
			}
			if oldest == ([32]byte{}) || candidateSeq < oldestSeq {
				oldest, oldestSeq = candidate, candidateSeq
			}
		}
		delete(c.replayFallbackRequired, oldest)
	}
	c.replayFallbackRequired[hash] = seq
}

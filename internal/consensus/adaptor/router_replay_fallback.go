package adaptor

const replayFallbackHistoryLimit = 64

// acquisitionMu protects these markers across pipeline cancellation and fetch
// retries. A child hash commits to its parent, so retries cannot repair the same
// replay by selecting a different parent.
func (r *Router) replayNeedsFullStateLocked(hash [32]byte) bool {
	_, required := r.replayFallbackRequired[hash]
	return required
}

func (r *Router) requireReplayFullStateLocked(seq uint32, hash [32]byte) {
	if r.replayFallbackRequired == nil {
		r.replayFallbackRequired = make(map[[32]byte]uint32)
	}
	if r.replayNeedsFullStateLocked(hash) {
		return
	}
	if len(r.replayFallbackRequired) >= replayFallbackHistoryLimit {
		var oldest [32]byte
		oldestSeq := ^uint32(0)
		for candidate, candidateSeq := range r.replayFallbackRequired {
			if candidate == r.consensusRecovery.targetHash || candidate == r.consensusRecovery.stepHash ||
				r.isAcquiringLocked(candidate) {
				continue
			}
			if oldest == ([32]byte{}) || candidateSeq < oldestSeq {
				oldest, oldestSeq = candidate, candidateSeq
			}
		}
		delete(r.replayFallbackRequired, oldest)
	}
	r.replayFallbackRequired[hash] = seq
}

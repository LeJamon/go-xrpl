package adaptor

import "github.com/LeJamon/go-xrpl/internal/ledger/header"

// Only completed network work with an active local preparation owns this
// lease. Unresponsive peers retain their ordinary acquisition timeouts.
func (c *catchupReplayCoordinator) localForwardReplayOwnsRecovery(targetSeq uint32, targetHash [32]byte) bool {
	if c.adaptor == nil || c.adaptor.LedgerService() == nil || c.adaptor.LedgerService().ReplayBlocked() {
		return false
	}
	parent := c.catchupReplayBase(c.adaptor.LedgerService())
	if parent == nil || parent.Sequence() == ^uint32(0) || targetSeq <= parent.Sequence() {
		return false
	}
	c.completionMu.Lock()
	deltaTask, txTask := c.legacyDeltaTask, c.completionTasks[1]
	c.completionMu.Unlock()
	var candidates []header.LedgerHeader
	if deltaTask != nil && c.replayer.Find(deltaTask.delta.Hash()) == deltaTask.delta {
		candidates = append(candidates, deltaTask.delta.TargetHeader())
	}
	if txTask != nil && txTask.ledger.TransactionOnly() {
		if h, _, _, err := txTask.ledger.Result(); err == nil && h != nil {
			candidates = append(candidates, *h)
		}
	}
	for _, h := range candidates {
		if h.LedgerIndex != parent.Sequence()+1 || h.ParentHash != parent.Hash() {
			continue
		}
		if entry, known := c.lookupSeqHash(h.LedgerIndex); known && entry.hash != h.Hash {
			continue
		}
		// Known contradictory ancestry releases the lease. Missing ancestry
		// does not discard a locally owned successor while verification runs.
		seq, hash := targetSeq, targetHash
		for steps := 0; seq > h.LedgerIndex && steps < seqHashRetain; steps++ {
			entry, known := c.lookupSeqHash(seq)
			if !known || entry.hash != hash || !entry.haveParent {
				return true
			}
			hash, seq = entry.parentHash, seq-1
		}
		if seq > h.LedgerIndex || hash == h.Hash {
			return true
		}
	}
	return false
}

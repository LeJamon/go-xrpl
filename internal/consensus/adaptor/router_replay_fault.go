package adaptor

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
)

func (c *catchupReplayCoordinator) replayFaultBlocked() bool {
	return c.adaptor != nil && c.adaptor.LedgerService() != nil && c.adaptor.LedgerService().ReplayBlocked()
}

func (c *catchupReplayCoordinator) replayTargetAuthenticated(h header.LedgerHeader) bool {
	if _, result := c.adaptor.recheckFullyValidated(h.LedgerIndex, h.Hash); result == validationRecheckAccepted {
		return true
	}
	c.acquisitionMu.Lock()
	targetSeq, targetHash := c.standardReplay.targetSeq, c.standardReplay.targetHash
	c.acquisitionMu.Unlock()
	if targetSeq <= h.LedgerIndex {
		targetSeq, targetHash, _ = c.bestCatchupTarget()
	}
	if targetSeq <= h.LedgerIndex {
		return false
	}
	if _, result := c.adaptor.recheckFullyValidated(targetSeq, targetHash); result != validationRecheckAccepted {
		return false
	}
	hash := h.Hash
	for seq := h.LedgerIndex + 1; seq != 0 && seq <= targetSeq; seq++ {
		entry, ok := c.lookupSeqHash(seq)
		if !ok || !entry.haveParent || entry.parentHash != hash || entry.parentFrom == seqHashSourcePeer {
			return false
		}
		hash = entry.hash
	}
	return hash == targetHash
}

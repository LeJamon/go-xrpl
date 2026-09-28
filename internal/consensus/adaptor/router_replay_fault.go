package adaptor

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
)

func (r *Router) replayFaultBlocked() bool {
	return r.adaptor != nil && r.adaptor.LedgerService() != nil && r.adaptor.LedgerService().ReplayBlocked()
}

func (r *Router) replayTargetAuthenticated(h header.LedgerHeader) bool {
	if _, result := r.adaptor.recheckFullyValidated(h.LedgerIndex, h.Hash); result == validationRecheckAccepted {
		return true
	}
	r.acquisitionMu.Lock()
	targetSeq, targetHash := r.standardReplay.targetSeq, r.standardReplay.targetHash
	r.acquisitionMu.Unlock()
	if targetSeq <= h.LedgerIndex {
		targetSeq, targetHash, _ = r.bestCatchupTarget()
	}
	if targetSeq <= h.LedgerIndex {
		return false
	}
	if _, result := r.adaptor.recheckFullyValidated(targetSeq, targetHash); result != validationRecheckAccepted {
		return false
	}
	hash := h.Hash
	for seq := h.LedgerIndex + 1; seq != 0 && seq <= targetSeq; seq++ {
		entry, ok := r.lookupSeqHash(seq)
		if !ok || !entry.haveParent || entry.parentHash != hash || entry.parentFrom == seqHashSourcePeer {
			return false
		}
		hash = entry.hash
	}
	return hash == targetHash
}

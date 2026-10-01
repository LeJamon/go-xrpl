package adaptor

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
)

func (c *catchupReplayCoordinator) replayFaultBlocked() bool {
	return c.adaptor != nil && c.adaptor.LedgerService() != nil && c.adaptor.LedgerService().ReplayBlocked()
}

func (c *catchupReplayCoordinator) replayFaultStatus() (replayfault.Status, bool) {
	if c.adaptor == nil {
		return replayfault.Status{}, false
	}
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return replayfault.Status{}, false
	}
	status := svc.ReplayFaultStatus()
	return status, status.Blocked || svc.ReplayBlocked()
}

func (c *catchupReplayCoordinator) deferFrozenPivotForReplayFault(seq uint32, hash [32]byte) bool {
	if !c.replayFaultBlocked() {
		c.replayFaultBlockWarningMu.Lock()
		c.replayFaultBlockWarningID = ""
		c.replayFaultBlockWarningMu.Unlock()
		return false
	}
	status, blocked := c.replayFaultStatus()
	if !blocked {
		c.replayFaultBlockWarningMu.Lock()
		c.replayFaultBlockWarningID = ""
		c.replayFaultBlockWarningMu.Unlock()
		return false
	}

	faultID := ""
	warningID := "blocked"
	if status.Fault != nil {
		faultID = status.Fault.ID
		warningID = faultID
	}
	if status.PersistenceError != nil && faultID == "" {
		warningID = status.PersistenceError.Error()
	}
	c.replayFaultBlockWarningMu.Lock()
	shouldLog := c.replayFaultBlockWarningID != warningID
	if shouldLog {
		c.replayFaultBlockWarningID = warningID
	}
	c.replayFaultBlockWarningMu.Unlock()
	if shouldLog {
		action := "invoke admin replay_recover with fault_id"
		if faultID == "" {
			action = "restore readable replay fault journal before invoking admin replay_recover"
		}
		args := []any{
			"pivot_seq", seq,
			"pivot_hash", fmt.Sprintf("%x", hash[:8]),
			"fault_id", faultID,
			"action", action,
		}
		if status.Fault != nil {
			args = append(args,
				"fault_class", status.Fault.Class,
				"fault_sequence", status.Fault.Sequence,
				"acquisition_attempts", status.Fault.AcquisitionAttempts,
			)
		}
		if status.PersistenceError != nil {
			args = append(args, "persistence_error", status.PersistenceError)
		}
		c.logger.Warn("frozen recovery pivot admission deferred by replay fault", args...)
	}
	return true
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

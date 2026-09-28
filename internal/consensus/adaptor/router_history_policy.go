package adaptor

import "github.com/LeJamon/go-xrpl/internal/ledger/inbound"

// Bound local history inspection per maintenance tick. State acquisition itself
// uses the existing resumable 2,048-visit budget and one history acquisition.
const historySkipBudget = 16

// configureHistoryBackfill must be called before Run. Disabling history never
// disables consensus/replay acquisition, persistence, or serving retained data.
func (c *catchupReplayCoordinator) configureHistoryBackfill(enabled bool, depth uint32) {
	c.historyBackfill = enabled
	c.historyDepth = depth
	c.logger.Info("Historical ledger backfill configured", "backfill", enabled, "ledger_history", depth)
}

func historyWindowMinimum(tip, depth uint32) uint32 {
	if depth == 0 || tip <= depth {
		return 1
	}
	return tip - depth
}

func (c *catchupReplayCoordinator) historySequenceAllowed(seq uint32) bool {
	if !c.historyBackfill || c.historyDepth == 0 || seq == 0 || c.adaptor == nil {
		return false
	}
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return false
	}
	tip := svc.GetValidatedLedgerIndex()
	return seq < tip && seq >= historyWindowMinimum(tip, c.historyDepth) && !c.belowFloor(seq)
}

// pruneHistoryBackfill is called as the validated tip advances and on maintenance
// even when catch-up prevents new history work. Retirement cancels worker I/O;
// dropping only the cursor would leave a yielding full-state walk running.
func (c *catchupReplayCoordinator) pruneHistoryBackfill(tip uint32) {
	minimum := historyWindowMinimum(tip, c.historyDepth)
	disabled := !c.historyBackfill || c.historyDepth == 0
	c.historyMu.Lock()
	if disabled || (c.history.seq != 0 && (c.history.seq < minimum || c.belowFloor(c.history.seq))) {
		c.history = catchupTarget{}
		c.historyFloor = 0
	}
	c.historyMu.Unlock()
	var retired []*inbound.Ledger
	c.acquisitionMu.Lock()
	for _, candidate := range c.fetchTracker.Active() {
		if candidate.Reason() != inbound.ReasonHistory {
			continue
		}
		if disabled || candidate.Seq() < minimum || c.belowFloor(candidate.Seq()) {
			if c.discardInboundAcquisitionLocked(candidate) {
				retired = append(retired, candidate)
			}
		}
	}
	c.acquisitionMu.Unlock()
	c.retireLegacyAcquisitions(retired)
	if len(retired) != 0 {
		c.logger.Info("Canceled historical acquisitions outside retention window",
			"validated_seq", tip, "minimum_seq", minimum, "canceled", len(retired))
	}
}

func (c *catchupReplayCoordinator) discardHistoryAcquisition(il *inbound.Ledger, reason string) {
	c.acquisitionMu.Lock()
	removed := c.discardInboundAcquisitionLocked(il)
	c.acquisitionMu.Unlock()
	if removed {
		c.retireLegacyAcquisitions([]*inbound.Ledger{il})
		c.logger.Info("Canceled historical acquisition", "seq", il.Seq(), "reason", reason)
	}
}

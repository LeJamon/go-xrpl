package adaptor

import "github.com/LeJamon/go-xrpl/internal/ledger/inbound"

// Bound local history inspection per maintenance tick. State acquisition itself
// uses the existing resumable 2,048-visit budget and one history acquisition.
const historySkipBudget = 16

// configureHistoryBackfill must be called before Run. Disabling history never
// disables consensus/replay acquisition, persistence, or serving retained data.
func (r *Router) configureHistoryBackfill(enabled bool, depth uint32) {
	r.historyBackfill = enabled
	r.historyDepth = depth
	r.logger.Info("Historical ledger backfill configured", "backfill", enabled, "ledger_history", depth)
}

func historyWindowMinimum(tip, depth uint32) uint32 {
	if depth == 0 || tip < depth {
		return 1
	}
	return tip - depth + 1
}

func (r *Router) historySequenceAllowed(seq uint32) bool {
	if !r.historyBackfill || r.historyDepth <= 1 || seq == 0 || r.adaptor == nil {
		return false
	}
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return false
	}
	tip := svc.GetValidatedLedgerIndex()
	return seq < tip && seq >= historyWindowMinimum(tip, r.historyDepth) && !r.belowFloor(seq)
}

// pruneHistoryBackfill is called as the validated tip advances and on maintenance
// even when catch-up prevents new history work. Retirement cancels worker I/O;
// dropping only the cursor would leave a yielding full-state walk running.
func (r *Router) pruneHistoryBackfill(tip uint32) {
	minimum := historyWindowMinimum(tip, r.historyDepth)
	disabled := !r.historyBackfill || r.historyDepth <= 1
	r.historyMu.Lock()
	if disabled || (r.history.seq != 0 && (r.history.seq < minimum || r.belowFloor(r.history.seq))) {
		r.history = catchupTarget{}
		r.historyFloor = 0
	}
	var retired []*inbound.Ledger
	for _, candidate := range r.fetchTracker.Active() {
		if candidate.Reason() != inbound.ReasonHistory {
			continue
		}
		if disabled || candidate.Seq() < minimum || r.belowFloor(candidate.Seq()) {
			if r.fetchTracker.DiscardExpected(candidate) {
				retired = append(retired, candidate)
			}
		}
	}
	r.historyMu.Unlock()
	r.retireLegacyAcquisitions(retired)
	if len(retired) != 0 {
		r.logger.Info("Canceled historical acquisitions outside retention window",
			"validated_seq", tip, "minimum_seq", minimum, "canceled", len(retired))
	}
}

func (r *Router) discardHistoryAcquisition(il *inbound.Ledger, reason string) {
	if r.fetchTracker.DiscardExpected(il) {
		r.retireLegacyAcquisitions([]*inbound.Ledger{il})
		r.logger.Info("Canceled historical acquisition", "seq", il.Seq(), "reason", reason)
	}
}

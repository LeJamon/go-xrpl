package shamap

type issue2043Metrics struct {
	limit           uint64
	used            uint64
	peak            uint64
	cursorUsed      uint64
	cursorPeak      uint64
	cursorBytes     uint64
	cursorPeakBytes uint64
	durableReads    uint64
	blockedCapacity uint64
	activeCapacity  uint64
	proofCapacity   uint64
	enforceBudget   bool
}

func issue2043NewBudget(limit uint64) *AcquisitionBudget {
	return NewAcquisitionBudget(limit)
}

func issue2043AttachBudget(sm *SHAMap, budget *AcquisitionBudget) {
	sm.SetAcquisitionBudget(budget)
}

func issue2043SnapshotMetrics(sm *SHAMap, budget *AcquisitionBudget) issue2043Metrics {
	walk := sm.BackedWalkStats()
	acquisition := budget.Snapshot()
	return issue2043Metrics{
		limit:           acquisition.Limit,
		used:            acquisition.Used,
		peak:            acquisition.Peak,
		cursorUsed:      acquisition.CursorUsed,
		cursorPeak:      acquisition.CursorPeak,
		cursorBytes:     walk.CursorBytes,
		cursorPeakBytes: walk.CursorPeakBytes,
		durableReads:    walk.DurableReads,
		blockedCapacity: walk.BlockedCapacity,
		activeCapacity:  walk.ActiveCapacity,
		proofCapacity:   walk.ProofCapacity,
		enforceBudget:   true,
	}
}

package inbound

import "time"

// RecordTraversalProgress credits newly discovered data and completed phases.
// Repeated reads or cache hits on unresolved paths do not establish progress.
func (l *Ledger) RecordTraversalProgress(previous Snapshot) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	current := l.snapshotLocked()
	progressed := current.StateWalk.TraversalDiscoveries > previous.StateWalk.TraversalDiscoveries ||
		current.TxWalk.TraversalDiscoveries > previous.TxWalk.TraversalDiscoveries ||
		current.StateUseful > previous.StateUseful || current.TxUseful > previous.TxUseful ||
		current.HaveHeader && !previous.HaveHeader ||
		current.HaveState && !previous.HaveState ||
		current.HaveTransactions && !previous.HaveTransactions
	if progressed {
		l.markProgressLocked()
	}
	return progressed
}

func (l *Ledger) ProgressSnapshot(now time.Time) (Snapshot, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.lastProgressReport.IsZero() && now.Sub(l.lastProgressReport) < 30*time.Second {
		return Snapshot{}, false
	}
	l.lastProgressReport = now
	snapshot := l.snapshotLocked()
	l.snapshot.Store(snapshotCopy(snapshot))
	return snapshot, true
}

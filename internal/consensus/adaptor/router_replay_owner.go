package adaptor

// A drain owns one generation for one bounded batch. Keep execution ownership
// outside standardReplay: cancellation must reset session state without
// allowing another applier to overlap an old worker still unwinding.
type standardReplayDrainOwner struct {
	generation uint64
	// Written only by this drain, read by its deferred release. Preserve the
	// router-loop yield even if the next acquisition is not ready yet.
	yielded bool
}

func (r *Router) beginStandardReplayDrain() *standardReplayDrainOwner {
	r.acquisitionMu.Lock()
	defer r.acquisitionMu.Unlock()
	if r.standardReplayDrainOwner != nil {
		// Its deferred release will re-arm any ready replacement head. This
		// also handles a wake consumed during a concurrent/reentrant drain.
		return nil
	}
	if !r.standardReplay.active || !r.standardReplay.pivotReady {
		r.standardReplay.applying = false
		return nil
	}
	owner := &standardReplayDrainOwner{generation: r.standardReplay.generation}
	r.standardReplayDrainOwner = owner
	r.standardReplay.applying = true
	return owner
}

func (r *Router) finishStandardReplayDrain(owner *standardReplayDrainOwner) {
	r.acquisitionMu.Lock()
	defer r.acquisitionMu.Unlock()
	if r.standardReplayDrainOwner != owner {
		return // An old completion must not release a newer actual owner.
	}
	r.standardReplayDrainOwner = nil
	// No drain can begin while acquisitionMu is held. Reconcile the current
	// generation's reservation instead of leaving the old flag behind or
	// dropping a replacement wake that arrived during the old handoff.
	r.standardReplay.applying = false
	if owner.yielded && r.standardReplay.active && r.standardReplay.pivotReady &&
		r.standardReplay.generation == owner.generation {
		r.standardReplay.applying = true
		r.scheduleStandardReplayDrain()
		return
	}
	r.scheduleReadyStandardReplayDrainLocked()
}

// Caller holds acquisitionMu. A pending wake is not an executing applier.
// Re-sending an edge-triggered wake is safe and repairs an orphaned reservation.
func (r *Router) scheduleReadyStandardReplayDrainLocked() bool {
	if r.standardReplayDrainOwner != nil || !r.standardReplay.active || !r.standardReplay.pivotReady {
		return false
	}
	head := r.standardReplay.entries[r.standardReplay.anchorSeq+1]
	if head == nil || (head.readyAt.IsZero() && !head.failed) {
		return false
	}
	r.standardReplay.applying = true
	r.scheduleStandardReplayDrain()
	return true
}

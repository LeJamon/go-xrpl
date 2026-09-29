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

func (c *catchupReplayCoordinator) beginStandardReplayDrain() *standardReplayDrainOwner {
	if c.stoppedForShutdown() {
		return nil
	}
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()
	if c.standardReplayDrainOwner != nil {
		// Its deferred release will re-arm any ready replacement head. This
		// also handles a wake consumed during a concurrent/reentrant drain.
		return nil
	}
	if !c.standardReplay.active || !c.standardReplay.pivotReady {
		c.standardReplay.applying = false
		return nil
	}
	owner := &standardReplayDrainOwner{generation: c.standardReplay.generation}
	c.standardReplayDrainOwner = owner
	c.standardReplay.applying = true
	return owner
}

func (c *catchupReplayCoordinator) finishStandardReplayDrain(owner *standardReplayDrainOwner) {
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()
	if c.standardReplayDrainOwner != owner {
		return // An old completion must not release a newer actual owner.
	}
	c.standardReplayDrainOwner = nil
	// No drain can begin while acquisitionMu is held. Reconcile the current
	// generation's reservation instead of leaving the old flag behind or
	// dropping a replacement wake that arrived during the old handoff.
	c.standardReplay.applying = false
	if c.stoppedForShutdown() {
		return
	}
	if owner.yielded && c.standardReplay.active && c.standardReplay.pivotReady &&
		c.standardReplay.generation == owner.generation {
		c.standardReplay.applying = true
		c.scheduleStandardReplayDrain()
		return
	}
	c.scheduleReadyStandardReplayDrainLocked()
}

// Caller holds acquisitionMu. A pending wake is not an executing applier.
// Re-sending an edge-triggered wake is safe and repairs an orphaned reservation.
func (c *catchupReplayCoordinator) scheduleReadyStandardReplayDrainLocked() bool {
	if c.stoppedForShutdown() || c.standardReplayDrainOwner != nil || !c.standardReplay.active || !c.standardReplay.pivotReady {
		return false
	}
	head := c.standardReplay.entries[c.standardReplay.anchorSeq+1]
	if head == nil || (head.readyAt.IsZero() && !head.failed) {
		return false
	}
	c.standardReplay.applying = true
	c.scheduleStandardReplayDrain()
	return true
}

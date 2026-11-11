package adaptor

// standardReplayPhase is the lifecycle phase of standardReplayPipeline.
type standardReplayPhase uint8

const (
	// standardReplayIdle: no pipeline is armed.
	standardReplayIdle standardReplayPhase = iota
	// standardReplayAcquiringPivot: a frozen full-state pivot is being
	// acquired; collected successors cannot drain yet.
	standardReplayAcquiringPivot
	// standardReplayPivotReady: the anchor is verified and no drain is reserved.
	standardReplayPivotReady
	// standardReplayDraining: a drain is reserved or running on the anchor.
	standardReplayDraining
	// standardReplayComplete: the target was installed. The pivot and anchor
	// are retained until the next arm or cancellation.
	standardReplayComplete
	// standardReplayCompleteDraining: a verified frozen pivot equal to the
	// target completed the pipeline while a drain reservation was still held.
	standardReplayCompleteDraining
	standardReplayPhaseCount
)

func (p standardReplayPhase) String() string {
	switch p {
	case standardReplayIdle:
		return "idle"
	case standardReplayAcquiringPivot:
		return "acquiring_pivot"
	case standardReplayPivotReady:
		return "pivot_ready"
	case standardReplayDraining:
		return "draining"
	case standardReplayComplete:
		return "complete"
	case standardReplayCompleteDraining:
		return "complete_draining"
	default:
		return "unknown"
	}
}

// deactivated is the phase after the pipeline stops owning recovery. A
// verified pivot stays recorded.
func (p standardReplayPhase) deactivated() standardReplayPhase {
	switch p {
	case standardReplayAcquiringPivot:
		return standardReplayIdle
	case standardReplayPivotReady:
		return standardReplayComplete
	case standardReplayDraining:
		return standardReplayCompleteDraining
	default:
		return p
	}
}

// withoutDrain is the phase after a drain reservation is released.
func (p standardReplayPhase) withoutDrain() standardReplayPhase {
	switch p {
	case standardReplayDraining:
		return standardReplayPivotReady
	case standardReplayCompleteDraining:
		return standardReplayComplete
	default:
		return p
	}
}

// standardReplayTransitions lists the legal successors of each phase.
// Self-transitions are always legal.
var standardReplayTransitions = [standardReplayPhaseCount][]standardReplayPhase{
	standardReplayIdle:           {standardReplayAcquiringPivot, standardReplayPivotReady},
	standardReplayAcquiringPivot: {standardReplayPivotReady, standardReplayIdle},
	standardReplayPivotReady:     {standardReplayDraining, standardReplayComplete, standardReplayIdle},
	standardReplayDraining: {
		standardReplayPivotReady, standardReplayComplete, standardReplayCompleteDraining, standardReplayIdle,
	},
	standardReplayComplete: {standardReplayAcquiringPivot, standardReplayPivotReady, standardReplayIdle},
	standardReplayCompleteDraining: {
		standardReplayComplete, standardReplayAcquiringPivot, standardReplayPivotReady, standardReplayIdle,
	},
}

func standardReplayTransitionAllowed(from, to standardReplayPhase) bool {
	if from >= standardReplayPhaseCount || to >= standardReplayPhaseCount {
		return false
	}
	if from == to {
		return true
	}
	for _, next := range standardReplayTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// isActive reports whether the pipeline owns recovery toward its target.
func (p *standardReplayPipeline) isActive() bool {
	switch p.phase {
	case standardReplayAcquiringPivot, standardReplayPivotReady, standardReplayDraining:
		return true
	default:
		return false
	}
}

// isPivotReady reports whether the pivot is verified and anchors the chain.
func (p *standardReplayPipeline) isPivotReady() bool {
	switch p.phase {
	case standardReplayPivotReady, standardReplayDraining, standardReplayComplete, standardReplayCompleteDraining:
		return true
	default:
		return false
	}
}

// isApplying reports whether a drain is reserved or running.
func (p *standardReplayPipeline) isApplying() bool {
	return p.phase == standardReplayDraining || p.phase == standardReplayCompleteDraining
}

// standardReplayPhaseHook observes every phase write. Tests install it before
// any coordinator exists; it is nil in production.
var standardReplayPhaseHook func(c *catchupReplayCoordinator, from, to standardReplayPhase, allowed bool)

// setStandardReplayPhaseLocked always performs the transition; an edge outside
// standardReplayTransitions is logged. Caller holds acquisitionMu.
func (c *catchupReplayCoordinator) setStandardReplayPhaseLocked(next standardReplayPhase) {
	from := c.standardReplay.phase
	allowed := standardReplayTransitionAllowed(from, next)
	if hook := standardReplayPhaseHook; hook != nil {
		hook(c, from, next, allowed)
	}
	if !allowed && c.logger != nil {
		c.logger.Error("illegal standard replay phase transition",
			"from", from.String(),
			"to", next.String(),
			"generation", c.standardReplay.generation,
		)
	}
	c.standardReplay.phase = next
}

// Caller holds acquisitionMu.
func (c *catchupReplayCoordinator) releaseStandardReplayDrainLocked() {
	c.setStandardReplayPhaseLocked(c.standardReplay.phase.withoutDrain())
}

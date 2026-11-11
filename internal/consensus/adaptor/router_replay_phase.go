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
	// standardReplayPhaseIllegal is never stored; phaseOf returns it for a
	// flag combination outside the enumerated set.
	standardReplayPhaseIllegal
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
		return "illegal"
	}
}

// standardReplayPhaseOf maps the legacy (active, pivotReady, applying) flags
// to a phase.
func standardReplayPhaseOf(active, pivotReady, applying bool) standardReplayPhase {
	switch {
	case !active && !pivotReady && !applying:
		return standardReplayIdle
	case active && !pivotReady && !applying:
		return standardReplayAcquiringPivot
	case active && pivotReady && !applying:
		return standardReplayPivotReady
	case active && pivotReady && applying:
		return standardReplayDraining
	case !active && pivotReady && !applying:
		return standardReplayComplete
	case !active && pivotReady && applying:
		return standardReplayCompleteDraining
	default:
		return standardReplayPhaseIllegal
	}
}

// standardReplayTransitions lists the legal successors of each phase.
// Self-transitions are always legal.
var standardReplayTransitions = [standardReplayPhaseIllegal][]standardReplayPhase{
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
	if from >= standardReplayPhaseIllegal || to >= standardReplayPhaseIllegal {
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

// standardReplayPhaseHook observes every phase write. Tests install it before
// any coordinator exists; it is nil in production.
var standardReplayPhaseHook func(p *standardReplayPipeline, phase standardReplayPhase)

// Caller holds acquisitionMu.
func (c *catchupReplayCoordinator) checkStandardReplayPhaseLocked() {
	if hook := standardReplayPhaseHook; hook != nil {
		p := &c.standardReplay
		hook(p, standardReplayPhaseOf(p.active, p.pivotReady, p.applying))
	}
}

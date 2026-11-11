package adaptor

import (
	"fmt"
	"os"
	"runtime/debug"
	"slices"
	"sync"
	"testing"
	"weak"

	"github.com/stretchr/testify/assert"
)

// standardReplayPhaseRecorder checks every phase write made while the package
// tests run. Any illegal phase fails the package from TestMain.
type standardReplayPhaseRecorder struct {
	mu          sync.Mutex
	last        map[weak.Pointer[standardReplayPipeline]]standardReplayPhase
	transitions map[[2]standardReplayPhase]int
	violations  []string
}

var standardReplayPhases = &standardReplayPhaseRecorder{
	last:        make(map[weak.Pointer[standardReplayPipeline]]standardReplayPhase),
	transitions: make(map[[2]standardReplayPhase]int),
}

func (r *standardReplayPhaseRecorder) observe(p *standardReplayPipeline, phase standardReplayPhase) {
	key := weak.Make(p)
	r.mu.Lock()
	defer r.mu.Unlock()
	if phase == standardReplayPhaseIllegal {
		r.violations = append(r.violations, fmt.Sprintf("illegal phase flags active=%t pivotReady=%t applying=%t\n%s",
			p.active, p.pivotReady, p.applying, debug.Stack()))
		return
	}
	from, known := r.last[key]
	r.last[key] = phase
	if !known {
		return
	}
	r.transitions[[2]standardReplayPhase{from, phase}]++
	if !standardReplayTransitionAllowed(from, phase) {
		r.violations = append(r.violations, fmt.Sprintf("illegal phase transition %s -> %s\n%s",
			from, phase, debug.Stack()))
	}
}

func (r *standardReplayPhaseRecorder) report() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if path := os.Getenv("STANDARD_REPLAY_PHASE_TRANSITIONS"); path != "" {
		lines := make([]string, 0, len(r.transitions))
		for edge, count := range r.transitions {
			lines = append(lines, fmt.Sprintf("%s -> %s %d", edge[0], edge[1], count))
		}
		slices.Sort(lines)
		out := ""
		for _, line := range lines {
			out += line + "\n"
		}
		_ = os.WriteFile(path, []byte(out), 0o600)
	}
	for _, violation := range r.violations {
		fmt.Fprintln(os.Stderr, "standard replay phase violation:", violation)
	}
	if len(r.violations) != 0 {
		return 1
	}
	return 0
}

func TestMain(m *testing.M) {
	standardReplayPhaseHook = standardReplayPhases.observe
	code := m.Run()
	if code == 0 {
		code = standardReplayPhases.report()
	}
	os.Exit(code)
}

func TestStandardReplayPhaseOfEnumeratesLegacyFlags(t *testing.T) {
	cases := []struct {
		active, pivotReady, applying bool
		want                         standardReplayPhase
	}{
		{false, false, false, standardReplayIdle},
		{true, false, false, standardReplayAcquiringPivot},
		{true, true, false, standardReplayPivotReady},
		{true, true, true, standardReplayDraining},
		{false, true, false, standardReplayComplete},
		{false, true, true, standardReplayCompleteDraining},
		{true, false, true, standardReplayPhaseIllegal},
		{false, false, true, standardReplayPhaseIllegal},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, standardReplayPhaseOf(tc.active, tc.pivotReady, tc.applying),
			"active=%t pivotReady=%t applying=%t", tc.active, tc.pivotReady, tc.applying)
	}
}

func TestStandardReplayTransitionTable(t *testing.T) {
	for from := standardReplayIdle; from < standardReplayPhaseIllegal; from++ {
		assert.True(t, standardReplayTransitionAllowed(from, from), "%s self", from)
		assert.True(t, standardReplayTransitionAllowed(from, standardReplayIdle), "%s cancel", from)
		assert.False(t, standardReplayTransitionAllowed(from, standardReplayPhaseIllegal), "%s illegal", from)
		seen := make(map[standardReplayPhase]bool)
		for _, to := range standardReplayTransitions[from] {
			assert.NotEqual(t, from, to, "%s lists itself", from)
			assert.Less(t, to, standardReplayPhaseIllegal, "%s lists illegal", from)
			assert.False(t, seen[to], "%s lists %s twice", from, to)
			seen[to] = true
		}
	}
	assert.False(t, standardReplayTransitionAllowed(standardReplayIdle, standardReplayDraining))
	assert.False(t, standardReplayTransitionAllowed(standardReplayAcquiringPivot, standardReplayDraining))
	assert.False(t, standardReplayTransitionAllowed(standardReplayPivotReady, standardReplayCompleteDraining))
	assert.False(t, standardReplayTransitionAllowed(standardReplayComplete, standardReplayDraining))
	assert.False(t, standardReplayTransitionAllowed(standardReplayPhaseIllegal, standardReplayIdle))
}

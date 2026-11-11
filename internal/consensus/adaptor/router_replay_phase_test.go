package adaptor

import (
	"fmt"
	"os"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// standardReplayPhaseRecorder checks every phase transition made while the
// package tests run. Any illegal transition fails the package from TestMain.
type standardReplayPhaseRecorder struct {
	mu          sync.Mutex
	transitions map[[2]standardReplayPhase]int
	violations  []string
	// expected holds coordinators that deliberately exercise illegal edges.
	expected map[*catchupReplayCoordinator][][2]standardReplayPhase
}

var standardReplayPhases = &standardReplayPhaseRecorder{
	transitions: make(map[[2]standardReplayPhase]int),
	expected:    make(map[*catchupReplayCoordinator][][2]standardReplayPhase),
}

func (r *standardReplayPhaseRecorder) observe(c *catchupReplayCoordinator, from, to standardReplayPhase, allowed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if illegal, ok := r.expected[c]; ok {
		if !allowed {
			r.expected[c] = append(illegal, [2]standardReplayPhase{from, to})
		}
		return
	}
	r.transitions[[2]standardReplayPhase{from, to}]++
	if !allowed {
		r.violations = append(r.violations, fmt.Sprintf("illegal phase transition %s -> %s\n%s", from, to, debug.Stack()))
	}
}

func (r *standardReplayPhaseRecorder) expectIllegal(t *testing.T, c *catchupReplayCoordinator) func() [][2]standardReplayPhase {
	r.mu.Lock()
	r.expected[c] = nil
	r.mu.Unlock()
	t.Cleanup(func() {
		r.mu.Lock()
		delete(r.expected, c)
		r.mu.Unlock()
	})
	return func() [][2]standardReplayPhase {
		r.mu.Lock()
		defer r.mu.Unlock()
		return slices.Clone(r.expected[c])
	}
}

// report writes the observed transitions to STANDARD_REPLAY_PHASE_TRANSITIONS
// when set and returns a non-zero exit code if any transition was illegal.
func (r *standardReplayPhaseRecorder) report() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if path := os.Getenv("STANDARD_REPLAY_PHASE_TRANSITIONS"); path != "" {
		lines := make([]string, 0, len(r.transitions))
		for edge, count := range r.transitions {
			lines = append(lines, fmt.Sprintf("%s -> %s %d\n", edge[0], edge[1], count))
		}
		slices.Sort(lines)
		_ = os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600)
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

// Each phase corresponds to exactly one combination of the former
// active/pivotReady/applying flags; the remaining two combinations
// (applying without a verified pivot) are not representable.
func TestStandardReplayPhasePredicates(t *testing.T) {
	cases := []struct {
		phase                        standardReplayPhase
		active, pivotReady, applying bool
	}{
		{standardReplayIdle, false, false, false},
		{standardReplayAcquiringPivot, true, false, false},
		{standardReplayPivotReady, true, true, false},
		{standardReplayDraining, true, true, true},
		{standardReplayComplete, false, true, false},
		{standardReplayCompleteDraining, false, true, true},
	}
	is := assert.New(t)
	is.Len(cases, int(standardReplayPhaseCount))
	for _, tc := range cases {
		p := standardReplayPipeline{phase: tc.phase}
		is.Equal(tc.active, p.isActive(), "%s active", tc.phase)
		is.Equal(tc.pivotReady, p.isPivotReady(), "%s pivotReady", tc.phase)
		is.Equal(tc.applying, p.isApplying(), "%s applying", tc.phase)

		deactivated := standardReplayPipeline{phase: tc.phase.deactivated()}
		is.False(deactivated.isActive(), "%s deactivated", tc.phase)
		is.Equal(tc.pivotReady, deactivated.isPivotReady(), "%s deactivated pivotReady", tc.phase)
		is.Equal(tc.applying, deactivated.isApplying(), "%s deactivated applying", tc.phase)

		released := standardReplayPipeline{phase: tc.phase.withoutDrain()}
		is.Equal(tc.active, released.isActive(), "%s released active", tc.phase)
		is.Equal(tc.pivotReady, released.isPivotReady(), "%s released pivotReady", tc.phase)
		is.False(released.isApplying(), "%s released", tc.phase)
	}
}

func TestStandardReplayTransitionTable(t *testing.T) {
	for from := standardReplayIdle; from < standardReplayPhaseCount; from++ {
		assert.True(t, standardReplayTransitionAllowed(from, from), "%s self", from)
		assert.True(t, standardReplayTransitionAllowed(from, standardReplayIdle), "%s cancel", from)
		assert.True(t, standardReplayTransitionAllowed(from, from.withoutDrain()), "%s release drain", from)
		assert.False(t, standardReplayTransitionAllowed(from, standardReplayPhaseCount), "%s unknown", from)
		seen := make(map[standardReplayPhase]bool)
		for _, to := range standardReplayTransitions[from] {
			assert.NotEqual(t, from, to, "%s lists itself", from)
			assert.Less(t, to, standardReplayPhaseCount, "%s lists unknown", from)
			assert.False(t, seen[to], "%s lists %s twice", from, to)
			seen[to] = true
		}
	}
	assert.True(t, standardReplayTransitionAllowed(standardReplayPivotReady, standardReplayPivotReady.deactivated()))
	assert.True(t, standardReplayTransitionAllowed(standardReplayDraining, standardReplayDraining.deactivated()))
	assert.True(t, standardReplayTransitionAllowed(standardReplayAcquiringPivot, standardReplayAcquiringPivot.deactivated()))
	assert.False(t, standardReplayTransitionAllowed(standardReplayIdle, standardReplayDraining))
	assert.False(t, standardReplayTransitionAllowed(standardReplayAcquiringPivot, standardReplayDraining))
	assert.False(t, standardReplayTransitionAllowed(standardReplayPivotReady, standardReplayCompleteDraining))
	assert.False(t, standardReplayTransitionAllowed(standardReplayComplete, standardReplayDraining))
	assert.False(t, standardReplayTransitionAllowed(standardReplayPhaseCount, standardReplayIdle))
}

func TestIllegalStandardReplayTransitionStillApplies(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	c := r.catchupReplay
	illegal := standardReplayPhases.expectIllegal(t, c)

	c.acquisitionMu.Lock()
	c.setStandardReplayPhaseLocked(standardReplayDraining)
	phase := c.standardReplay.phase
	c.acquisitionMu.Unlock()

	assert.Equal(t, standardReplayDraining, phase)
	assert.Equal(t, [][2]standardReplayPhase{{standardReplayIdle, standardReplayDraining}}, illegal())
}

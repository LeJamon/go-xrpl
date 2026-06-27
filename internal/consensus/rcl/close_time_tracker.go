package rcl

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/protocol"
)

// avalancheState is the close-time vote threshold escalation level.
type avalancheState int

const (
	avalancheInit  avalancheState = iota // 50% threshold
	avalancheMid                         // 65% threshold
	avalancheLate                        // 70% threshold
	avalancheStuck                       // 95% threshold
)

// closeTimeTracker owns a round's close-time consensus state (avalanche
// threshold level, whether consensus is reached). Not independently
// synchronized: every method runs under the Engine's e.mu.
type closeTimeTracker struct {
	// close-time consensus reached this round
	haveConsensus bool

	// threshold level, escalated by neededWeight as converge percent rises
	avalancheState avalancheState
}

func newCloseTimeTracker() *closeTimeTracker {
	return &closeTimeTracker{}
}

func (c *closeTimeTracker) reset() {
	c.haveConsensus = false
	c.avalancheState = avalancheInit
}

// neededWeight returns the vote percentage required for close-time consensus,
// advancing the avalanche level when convergePercent crosses the next cutoff.
// Round-dwell args are 0: close-time advancement is purely percent-based.
func (c *closeTimeTracker) neededWeight(convergePercent int, parms consensus.ConsensusParms) int {
	pct, newState := parms.NeededWeight(
		consensus.AvalancheState(c.avalancheState),
		convergePercent,
		0,
		0,
	)
	if newState != nil {
		c.avalancheState = avalancheState(*newState)
	}
	return pct
}

func (c *closeTimeTracker) stateName() string {
	switch c.avalancheState {
	case avalancheInit:
		return "init"
	case avalancheMid:
		return "mid"
	case avalancheLate:
		return "late"
	case avalancheStuck:
		return "stuck"
	}
	return "unknown"
}

// summarizeCloseTimeVotes renders the vote distribution as "ct=count" pairs
// (XRPL-epoch seconds), capped at 8 entries.
func summarizeCloseTimeVotes(votes map[time.Time]int) string {
	if len(votes) == 0 {
		return "(empty)"
	}
	type kv struct {
		ct    int64
		count int
	}
	all := make([]kv, 0, len(votes))
	for t, c := range votes {
		all = append(all, kv{ct: t.Unix() - protocol.RippleEpochUnix, count: c})
	}
	limit := min(len(all), 8)
	var b strings.Builder
	for i := range limit {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%d=%d", all[i].ct, all[i].count)
	}
	if len(all) > limit {
		fmt.Fprintf(&b, " (+%d more)", len(all)-limit)
	}
	return b.String()
}

// participantsNeeded rounds to the nearest participant count and never returns 0.
func participantsNeeded(participants, percent int) int {
	result := (participants*percent + percent/2) / 100
	if result == 0 {
		return 1
	}
	return result
}

// mostVotedAscending returns the most-voted close time among those with count
// >= minCount, breaking ties toward the LARGEST time. Ascending iteration makes
// the result independent of Go's randomized map order — two nodes tallying the
// same votes must agree or they fork. The bool reports whether any time met
// minCount; callers must use it, since the zero time is a legitimate winner.
func mostVotedAscending(votes map[time.Time]int, minCount int) (time.Time, int, bool) {
	sorted := make([]time.Time, 0, len(votes))
	for t := range votes {
		sorted = append(sorted, t)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Before(sorted[j])
	})

	var best time.Time
	bar := minCount
	found := false
	for _, t := range sorted {
		if count := votes[t]; count >= bar {
			best = t
			bar = count
			found = true
		}
	}
	return best, bar, found
}

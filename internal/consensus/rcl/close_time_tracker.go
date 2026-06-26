package rcl

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/protocol"
)

// avalancheState tracks the close time voting threshold escalation.
// Matches rippled's avalanche cutoffs in ConsensusParms.h.
type avalancheState int

const (
	avalancheInit  avalancheState = iota // 50% threshold
	avalancheMid                         // 65% threshold
	avalancheLate                        // 70% threshold
	avalancheStuck                       // 95% threshold
)

// closeTimeTracker owns the close-time consensus state of a round: the
// avalanche threshold level the close-time vote has escalated to, and
// whether a close-time consensus has been reached. It hosts the close-time
// vote tallying helpers used by the Engine's updateCloseTimePosition /
// determineCloseTime.
//
// Like ProposalTracker it is NOT independently synchronized: every method
// is called with the Engine's e.mu held, the same lock that protected the
// two state fields when they were inline on the Engine.
type closeTimeTracker struct {
	// haveConsensus reports whether a close-time consensus has been reached
	// this round (rippled's haveCloseTimeConsensus_).
	haveConsensus bool

	// avalancheState is the close-time vote threshold level, escalated by
	// neededWeight as the round's converge percent rises.
	avalancheState avalancheState
}

// newCloseTimeTracker creates a close-time tracker in its round-start
// state (no consensus, threshold at the initial avalanche level).
func newCloseTimeTracker() *closeTimeTracker {
	return &closeTimeTracker{}
}

// reset returns the tracker to its round-start state.
func (c *closeTimeTracker) reset() {
	c.haveConsensus = false
	c.avalancheState = avalancheInit
}

// neededWeight returns the minimum vote percentage required for close-time
// consensus at the current avalanche level, advancing that level when the
// converge percent crosses the next cutoff. Mirrors rippled's call at
// Consensus.h:1578-1581:
//
//	auto const [neededWeight, newState] = getNeededWeight(
//	    parms, closeTimeAvalancheState_, convergePercent_, 0, 0);
//	if (newState)
//	    closeTimeAvalancheState_ = *newState;
//
// Close-time avalanche advancement is purely percent-based — rippled
// passes currentRounds=0 and minimumRounds=0 so the round-dwell check is
// trivially satisfied — matching that here keeps a single canonical
// NeededWeight implementation across per-tx disputes and close-time
// threshold escalation.
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

// stateName renders the current avalanche level for logging.
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

// summarizeCloseTimeVotes renders the vote distribution as "ct=count"
// pairs (XRPL-epoch seconds), capped at 8 entries.
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

// participantsNeeded computes the minimum number of participants required
// to meet a given percentage threshold. Matches rippled's participantsNeeded().
func participantsNeeded(participants, percent int) int {
	result := (participants*percent + percent/2) / 100
	if result == 0 {
		return 1
	}
	return result
}

// mostVotedAscending returns the close time with the most votes, considering
// only times whose count is >= minCount, and breaks ties toward the LARGEST
// time. It iterates ascending so the result never depends on Go's randomized
// map iteration: two nodes tallying the same votes must agree or they finalize
// different ledger hashes (a fork). Mirrors rippled's std::map<NetClock,int>
// "raise the bar" loop (Consensus.h:1605-1621). The bool reports whether any
// time met minCount; callers must use it rather than best.IsZero(), since a
// legitimate winner may be the zero time (unset close times round to zero).
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

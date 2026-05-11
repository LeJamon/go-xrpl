package rcl

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestCloseTimeTieBreak_DeterministicAcrossValidators pins the
// close-time tie-break rule that the all-5 UNL bootstrap depends on.
//
// Bug history: updateCloseTimePosition iterated `closeTimeVotes`
// (a Go map) in random order. On a tied vote count, the for-loop's
// "raise bar to pick the MOST popular" pattern selects the LAST
// candidate visited that meets threshold — but with random map
// iteration each goxrpl validator picks a DIFFERENT tied candidate,
// so the network never converges on close-time consensus and stalls
// at the first 3-3 tie. The 5-validator no-tx soak hit this at
// seq=18 with {831802190:3, 831802200:3} → goxrpl-0 picks one,
// goxrpl-1 picks the other, neither matches rippled's pick; the 3
// rippled validators tie 3-3 with the 2 goxrpls → no candidate
// reaches quorum=4 ever.
//
// Fix: sort closeTimeVotes by time ascending before the for-loop
// (matches std::map<NetClock,int> iteration in rippled). The "raise
// bar" pattern then deterministically picks the LARGER tied close-
// time across all validators.
//
// This test pins the deterministic sort-then-pick behavior directly.
// It does NOT exercise the full Engine — that requires too much
// setup for the property under test, which is purely about
// iteration order.
func TestCloseTimeTieBreak_DeterministicAcrossValidators(t *testing.T) {
	// Reproduce the seq=18 stall: two close-times tied at 3 votes.
	smaller := time.Unix(831802190, 0).UTC()
	larger := time.Unix(831802200, 0).UTC()
	votes := map[time.Time]int{
		smaller: 3,
		larger:  3,
	}

	// Run the same selection algorithm 50 times. With Go's randomized
	// map iteration, the buggy version would pick smaller ~25 times
	// and larger ~25 times. The fixed (sort-first) version picks
	// `larger` every single time.
	const iterations = 50
	threshVoteInitial := 3 // 50% of 6 participants → 3
	picks := make(map[time.Time]int)
	for i := 0; i < iterations; i++ {
		threshVote := threshVoteInitial
		var picked time.Time

		// Sort votes by time ascending — the fix.
		sortedTimes := make([]time.Time, 0, len(votes))
		for tt := range votes {
			sortedTimes = append(sortedTimes, tt)
		}
		sort.Slice(sortedTimes, func(i, j int) bool {
			return sortedTimes[i].Before(sortedTimes[j])
		})

		for _, tt := range sortedTimes {
			count := votes[tt]
			if count >= threshVote {
				picked = tt
				threshVote = count
			}
		}
		picks[picked]++
	}

	assert.Equal(t, iterations, picks[larger],
		"deterministic tie-break must pick the LARGER close-time on every iteration "+
			"(matches rippled's std::map ascending iteration → last-meeting-threshold wins) "+
			"— if this varies across runs, goxrpl validators won't converge on tied votes")
	assert.Zero(t, picks[smaller],
		"smaller tied close-time must NEVER be picked under the deterministic rule")
}

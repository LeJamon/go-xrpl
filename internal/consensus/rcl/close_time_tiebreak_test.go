package rcl

import (
	"sort"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
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
	for range iterations {
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

// TestDetermineCloseTime_ObserverTieBreakDeterministic pins the
// observer-path close-time tie-break (issue #678). determineCloseTime is
// the fallback used when this node has no OurPosition this round — during
// catch-up or as a non-proposing trusted validator. On a tied close-time
// vote it must pick the SAME time as every other node, or honest
// observers embed different close times in the ledger header → different
// ledger_hash → a fork.
//
// Bug: the observer loop iterated roundedVotes (a Go map) with a strict
// `count > bestCount`, so on a tie two observers picked different times
// depending on randomized map order. Fix mirrors updateCloseTimePosition:
// sort ascending and keep the LARGEST time on a tie.
func TestDetermineCloseTime_ObserverTieBreakDeterministic(t *testing.T) {
	adaptor := newMockAdaptor() // CloseTimeResolution() == 1s

	// Two close-times tied at 3 votes, both already on a 1s boundary so
	// roundCloseTime maps each to itself.
	smaller := time.Unix(831802190, 0).UTC()
	larger := time.Unix(831802200, 0).UTC()

	eng := &Engine{
		adaptor: adaptor,
		state: &consensus.RoundState{
			OurPosition: nil, // observer: forces the fallback path
			CloseTimes: consensus.CloseTimes{
				Peers: map[time.Time]int{smaller: 3, larger: 3},
			},
		},
	}

	// Go randomizes map iteration on every range, so the buggy strict-'>'
	// loop would return `smaller` on roughly half of these calls.
	const iterations = 100
	for i := range iterations {
		got := eng.determineCloseTime()
		assert.True(t, got.Equal(larger),
			"observer must deterministically pick the LARGER tied close-time "+
				"(matches updateCloseTimePosition / rippled's ascending std::map); "+
				"got %v on iteration %d", got, i)
	}
}

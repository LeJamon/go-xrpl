package rcl

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/stretchr/testify/assert"
)

// TestMostPopularTxSet_DeterministicTieBreak pins the deterministic
// tie-break used when selecting the most-supported transaction set
// (issue #612).
//
// Bug history: both the acceptLedger fallback selector and
// ProposalTracker.GetWinningTxSet iterated a Go map with a strict `>`
// comparison. On equal counts the winner depended on Go's randomized
// map-iteration order, so two nodes (or the same node replaying) could
// pick different tx sets from an identical proposal distribution and
// seed a fork or replay mismatch.
//
// Fix: on equal counts keep the lexicographically smallest TxSetID, so
// every node converges on the same pick.
func TestMostPopularTxSet_DeterministicTieBreak(t *testing.T) {
	// Three tx sets tied at 2 votes each. The lexicographically smallest
	// id ({0x01...}) must win every iteration despite map randomization.
	a := consensus.TxSetID{0x01}
	b := consensus.TxSetID{0x02}
	c := consensus.TxSetID{0x03}
	counts := map[consensus.TxSetID]int{a: 2, b: 2, c: 2}

	const iterations = 100
	for i := 0; i < iterations; i++ {
		id, count := mostPopularTxSet(counts)
		assert.Equal(t, a, id,
			"tie must always resolve to the lexicographically smallest TxSetID")
		assert.Equal(t, 2, count)
	}
}

// TestMostPopularTxSet_ClearWinner confirms a strict winner is selected
// regardless of tie-break, and that an empty map yields the zero id / 0.
func TestMostPopularTxSet_ClearWinner(t *testing.T) {
	a := consensus.TxSetID{0x01}
	b := consensus.TxSetID{0x02}

	id, count := mostPopularTxSet(map[consensus.TxSetID]int{a: 1, b: 3})
	assert.Equal(t, b, id)
	assert.Equal(t, 3, count)

	id, count = mostPopularTxSet(map[consensus.TxSetID]int{})
	assert.Equal(t, consensus.TxSetID{}, id)
	assert.Equal(t, 0, count)
}

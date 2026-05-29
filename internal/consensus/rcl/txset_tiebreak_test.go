package rcl

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/stretchr/testify/assert"
)

// TestMostPopularTxSet_DeterministicTieBreak pins the issue #612 tie-break:
// on equal counts the lexicographically smallest TxSetID must win, never
// Go's randomized map-iteration order.
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

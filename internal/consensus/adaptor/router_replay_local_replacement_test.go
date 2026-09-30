package adaptor

import (
	"context"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type localReplacementFamily struct {
	mu      sync.Mutex
	nodes   map[[32]byte][]byte
	fetches map[[32]byte]int
}

func (f *localReplacementFamily) Fetch(_ context.Context, hash [32]byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches[hash]++
	return append([]byte(nil), f.nodes[hash]...), nil
}

func (f *localReplacementFamily) StoreBatch(_ context.Context, entries []shamap.FlushEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, entry := range entries {
		f.nodes[entry.Hash] = append([]byte(nil), entry.Data...)
	}
	return nil
}

func TestReplayReplacementRejectsIncompleteOrInvalidLocalCandidate(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	_, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 1)
	stateMap, err := links[0].ledger.StateMapSnapshot()
	require.NoError(t, err)
	for i := byte(0); i < 32; i++ {
		var key [32]byte
		key[0] = 0xf0
		key[31] = i + 1
		value := make([]byte, 12)
		value[0] = i
		require.NoError(t, stateMap.Put(key, value))
	}
	wireNodes, err := stateMap.WalkWireNodes()
	require.NoError(t, err)
	require.Greater(t, len(wireNodes), 1)
	family := &localReplacementFamily{
		nodes:   make(map[[32]byte][]byte, len(wireNodes)),
		fetches: make(map[[32]byte]int, len(wireNodes)),
	}
	hashes := make([][32]byte, 0, len(wireNodes))
	for _, node := range wireNodes {
		entry, convertErr := shamap.FlushEntryFromWire(node.Data, links[0].seq, shamap.TypeState)
		require.NoError(t, convertErr)
		family.nodes[entry.Hash] = append([]byte(nil), entry.Data...)
		hashes = append(hashes, entry.Hash)
	}
	rootHash, err := stateMap.Hash()
	require.NoError(t, err)
	incompleteState, err := shamap.NewFromRootHash(shamap.TypeState, rootHash, family)
	require.NoError(t, err)
	txMap, err := links[0].ledger.TxMapSnapshot()
	require.NoError(t, err)
	h := links[0].ledger.Header()
	h.AccountHash = rootHash
	h.Hash = header.CalculateHash(h)
	require.NoError(t, svc.StoreLedgerWithState(t.Context(), &h, incompleteState, txMap))
	family.mu.Lock()
	var missingHash [32]byte
	for _, hash := range hashes {
		if hash != rootHash && family.fetches[hash] == 0 {
			missingHash = hash
			break
		}
	}
	delete(family.nodes, missingHash)
	family.mu.Unlock()
	require.NotEqual(t, [32]byte{}, missingHash)
	held, err := svc.GetLedgerByHash(h.Hash)
	require.NoError(t, err)
	require.NotNil(t, held)
	require.False(t, svc.HasCompleteLedgerHash(h.LedgerIndex, h.Hash))

	c := r.catchupReplay
	_, _, _, ok := c.localReplayReplacementCandidate(h.LedgerIndex, h.Hash)
	assert.False(t, ok)
	assert.False(t, c.locallySatisfiesLedger(h.LedgerIndex, h.Hash))
	_, _, _, ok = c.localReplayReplacementCandidate(h.LedgerIndex+1, h.Hash)
	assert.False(t, ok)
	wrongHash := h.Hash
	wrongHash[0]++
	_, _, _, ok = c.localReplayReplacementCandidate(h.LedgerIndex, wrongHash)
	assert.False(t, ok)
}

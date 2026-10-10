package shamap

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetNodeFatDoesNotRetainReadNodesInSharedSnapshots(t *testing.T) {
	// Keep a resident root shared by the owner and its serving snapshot. Cold
	// children must remain hashes after serving, even after repeated deep reads.
	family := newMemoryFamily()
	source := buildRandomState(t, 1024)
	root, err := source.Hash()
	require.NoError(t, err)
	require.NoError(t, flushToFamily(source, family))
	owner, err := NewFromRootHash(TypeState, root, family)
	require.NoError(t, err)
	snapshot, err := owner.SnapshotMutable()
	require.NoError(t, err)
	require.Same(t, owner.tree.root, snapshot.tree.root)
	want, err := source.GetNodeFatByPath([32]byte{}, 0, 4, true)
	require.NoError(t, err)
	require.Greater(t, len(want), 100)
	for range 3 {
		got, err := snapshot.GetNodeFatByPath([32]byte{}, 0, 4, true)
		require.NoError(t, err)
		require.Equal(t, want, got)
		for branch := range BranchFactor {
			child, _, _ := owner.tree.root.LoadChild(branch)
			require.Nil(t, child, "peer read attached branch %d to the owner", branch)
		}
	}
	// Requests for arbitrary descendants must not retain the path used to
	// locate them either; testing only root requests misses that path.
	for branch := range BranchFactor {
		path := [32]byte{byte(branch << 4)}
		want, err := source.GetNodeFatByPath(path, 1, 3, true)
		require.NoError(t, err)
		got, err := snapshot.GetNodeFatByPath(path, 1, 3, true)
		require.NoError(t, err)
		require.Equal(t, want, got)
		child, _, _ := owner.tree.root.LoadChild(branch)
		require.Nil(t, child, "subtree lookup attached branch %d to the owner", branch)
	}
	gotRoot, err := owner.Hash()
	require.NoError(t, err)
	require.Equal(t, root, gotRoot)
	// Non-retaining reads must still reject corrupt backing data.
	for branch := range BranchFactor {
		_, hash, present := owner.tree.root.LoadChild(branch)
		if present {
			family.mu.Lock()
			family.store[hash] = []byte("corrupt node")
			family.mu.Unlock()
			break
		}
	}
	_, err = snapshot.GetNodeFatByPath([32]byte{}, 0, 4, true)
	require.Error(t, err)
}

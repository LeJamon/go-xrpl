package jtx

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestOwnerDirectoryContainsFollowsLinksAndFailsClosed(t *testing.T) {
	t.Run("sparse continuation", func(t *testing.T) {
		env := NewTestEnv(t)
		owner := NewAccount("owner")
		rootKey := keylet.OwnerDir(owner.ID)
		target := [32]byte{0x42}

		root := &state.DirectoryNode{Owner: owner.ID}
		root.SetIndexNext(4096)
		root.SetIndexPrevious(4096)
		putDirectoryNode(t, env, rootKey, root)
		putDirectoryNode(t, env, keylet.OwnerDirPage(owner.ID, 4096), &state.DirectoryNode{
			RootIndex: rootKey.Key,
			Indexes:   [][32]byte{target},
		})

		found, err := OwnerDirectoryContains(env, owner, target)
		require.NoError(t, err)
		require.True(t, found)
	})

	t.Run("missing continuation", func(t *testing.T) {
		env := NewTestEnv(t)
		owner := NewAccount("owner")
		root := &state.DirectoryNode{Owner: owner.ID}
		root.SetIndexNext(9)
		root.SetIndexPrevious(9)
		putDirectoryNode(t, env, keylet.OwnerDir(owner.ID), root)

		_, err := OwnerDirectoryContains(env, owner, [32]byte{0x42})
		require.ErrorContains(t, err, "continuation page 9 is missing")
	})

	t.Run("cycle", func(t *testing.T) {
		env := NewTestEnv(t)
		owner := NewAccount("owner")
		rootKey := keylet.OwnerDir(owner.ID)
		target := [32]byte{0x42}
		root := &state.DirectoryNode{Owner: owner.ID}
		root.SetIndexNext(9)
		root.SetIndexPrevious(9)
		putDirectoryNode(t, env, rootKey, root)
		page := &state.DirectoryNode{RootIndex: rootKey.Key, Indexes: [][32]byte{target}}
		page.SetIndexNext(9)
		putDirectoryNode(t, env, keylet.OwnerDirPage(owner.ID, 9), page)

		_, err := OwnerDirectoryContains(env, owner, target)
		require.ErrorContains(t, err, "cycle at page 9")
	})
}

func putDirectoryNode(t *testing.T, env *TestEnv, key keylet.Keylet, node *state.DirectoryNode) {
	t.Helper()
	data, err := state.SerializeDirectoryNode(node, false)
	require.NoError(t, err)
	require.NoError(t, env.ledger.Insert(key, data))
}

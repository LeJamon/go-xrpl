package applystate

import (
	"testing"

	ledgercore "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/stretchr/testify/require"
)

func TestSuccessorTracksMutationAndAtomicCommit(t *testing.T) {
	base := newMockBaseView()
	base.data[key(2)] = []byte("base")
	table := newTestApplyStateTable(t, base, [32]byte{}, 0, nil)
	_, err := table.Read(kl(2))
	require.NoError(t, err)
	require.NoError(t, table.Update(kl(2), []byte("modified")))
	require.NoError(t, table.Update(kl(3), []byte("implicit insert")))
	require.NoError(t, table.Insert(kl(1), []byte("insert")))
	check := func(after, want byte, data string) {
		k, v, ok, err := table.Succ(key(after))
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, key(want), k)
		require.Equal(t, data, string(v))
	}
	check(0, 1, "insert")
	check(1, 2, "modified")
	require.NoError(t, table.Erase(kl(1)))
	require.NoError(t, table.Erase(kl(2)))
	check(0, 3, "implicit insert")
	require.NoError(t, table.Insert(kl(2), []byte("reinsert")))
	check(0, 2, "reinsert")
	require.NoError(t, table.ApplyAtomically(func(v ledgercore.Writer) error { return v.Insert(kl(1), []byte("atomic")) }))
	check(0, 1, "atomic")
	clone := table.clone()
	defer clone.Close()
	require.NoError(t, clone.Erase(kl(1)))
	check(0, 1, "atomic")
}

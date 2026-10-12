package applystate

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"testing"

	ledgercore "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/stretchr/testify/require"
)

func TestSuccessorIndexTracksMutationAndAtomicCommit(t *testing.T) {
	base := newMockBaseView()
	base.data[key(2)] = []byte("base")
	table := NewApplyStateTable(base, [32]byte{}, 0, nil)
	_, err := table.Read(kl(2))
	require.NoError(t, err)
	require.Empty(t, table.insertedKeys, "cached base keys need no local index")
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
	require.NoError(t, clone.Erase(kl(1)))
	check(0, 1, "atomic")
}

// Isolate local read-cache overhead from the base SHAMap's ordered traversal.
func BenchmarkSuccessorWithCachedReads(b *testing.B) {
	for _, count := range []int{100, 10000, 100000} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			table := NewApplyStateTable(newMockBaseView(), [32]byte{}, 0, nil)
			for i := range count {
				var k [32]byte
				binary.BigEndian.PutUint64(k[:8], uint64(i+1))
				table.items[k] = &TrackedEntry{Action: ActionCache, Current: []byte{1}}
			}
			query := [32]byte{0xff}
			b.Run("indexed", func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					_, _, _, _ = table.Succ(query)
				}
			})
			b.Run("previous_map_scan", func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					var best [32]byte
					for k, e := range table.items {
						if e.Action != ActionErase && bytes.Compare(k[:], query[:]) > 0 && (best == ([32]byte{}) || bytes.Compare(k[:], best[:]) < 0) {
							best = k
						}
					}
					if best != ([32]byte{}) {
						b.Fatal("unexpected successor")
					}
				}
			})
		})
	}
}

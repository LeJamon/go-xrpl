package shamap

import (
	"bytes"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/stretchr/testify/require"
)

func TestCleanResidencyPeerScansPreserveForeground(t *testing.T) {
	for _, peerFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "foreground-first", true: "peer-first"}[peerFirst], func(t *testing.T) {
			pool := membudget.New(128 << 10)
			account, err := pool.NewAccount("cache", 64<<10, 96<<10)
			require.NoError(t, err)
			pressure, err := pool.NewAccount("pressure", 16<<10, 0)
			require.NoError(t, err)
			cache := NewCleanResidency(96<<10, account)
			require.Equal(t, uint64(84<<10), cache.Stats().Foreground.Limit)
			require.Equal(t, uint64(12<<10), cache.Stats().Peer.Limit)
			t.Cleanup(cache.Close)
			body := prefixNodeBody{kind: storedInner, payload: make([]byte, BranchFactor*32)}
			scan := func() {
				for peer := 1; peer <= 4; peer++ {
					for i := 0; i < 128; i++ {
						hash := [32]byte{byte(peer), byte(i)}
						cache.insertForPolicy(cache.beginRead(), hash, body, ReadPeer)
					}
				}
			}
			if peerFirst {
				scan()
			}
			for i := 0; i < 32; i++ {
				cache.insert([32]byte{0, byte(i)}, body)
			}
			before := cache.Stats().Foreground
			require.Equal(t, uint64(32), before.Nodes)
			require.True(t, pressure.TryReserve(64<<10))
			for pass := 0; pass < 3; pass++ {
				scan()
			}
			after := cache.Stats()
			require.Equal(t, before.UsedBytes, after.Foreground.UsedBytes)
			require.Equal(t, before.Evictions, after.Foreground.Evictions)
			require.Positive(t, after.Peer.Evictions)
			require.LessOrEqual(t, after.Peer.UsedBytes, after.Peer.Limit)
			for i := 0; i < 32; i++ {
				_, ok := cache.lookup([32]byte{0, byte(i)})
				require.True(t, ok)
			}
			require.Equal(t, after.UsedBytes, account.Snapshot().Used)
			require.LessOrEqual(t, pool.Snapshot().Used, pool.Snapshot().Limit)
			pressure.Release(64 << 10)
			cache.Close()
			require.Zero(t, pool.Snapshot().Used)
			require.Zero(t, pool.Snapshot().InvalidReleases)
		})
	}
}

func TestCleanResidencyPeerReuseAndForegroundPromotion(t *testing.T) {
	cache := NewCleanResidency(64<<10, nil)
	defer cache.Close()
	body := prefixNodeBody{kind: storedInner, payload: make([]byte, BranchFactor*32)}
	hash := [32]byte{1}
	cache.insertForPolicy(cache.beginRead(), hash, body, ReadPeer)
	_, ok := cache.lookupForPolicy(hash, ReadPeer)
	require.True(t, ok)
	require.Equal(t, uint64(1), cache.Stats().Peer.Hits)
	_, ok = cache.lookup(hash)
	require.True(t, ok)
	stats := cache.Stats()
	require.Equal(t, uint64(1), stats.Foreground.Nodes)
	require.Zero(t, stats.Peer.Nodes)
	require.Zero(t, stats.Peer.UsedBytes)
	require.Equal(t, uint64(1), stats.Foreground.Admissions)
	_, ok = cache.lookupForPolicy(hash, ReadPeer)
	require.True(t, ok)
	require.Equal(t, uint64(2), cache.Stats().Peer.Hits)
	require.Equal(t, uint64(1), cache.Stats().Nodes)
}

func TestCleanResidencyPeerHitsDoNotPromoteForeground(t *testing.T) {
	body := prefixNodeBody{kind: storedInner, payload: make([]byte, BranchFactor*32)}
	charge := cleanResidencyFixedBytes + uint64(len(body.payload))
	cache := NewCleanResidency(2*charge*8/7, nil)
	defer cache.Close()
	cache.insert([32]byte{1}, body)
	cache.insert([32]byte{2}, body)
	_, ok := cache.lookupForPolicy([32]byte{1}, ReadPeer)
	require.True(t, ok)
	cache.insert([32]byte{3}, body)
	_, ok = cache.lookup([32]byte{1})
	require.False(t, ok, "peer use must not reorder the foreground LRU")
	_, ok = cache.lookup([32]byte{2})
	require.True(t, ok)
}

func TestCleanResidencyBothClassesRejectRetiredGeneration(t *testing.T) {
	cache := NewCleanResidency(64<<10, nil)
	defer cache.Close()
	body := prefixNodeBody{kind: storedInner, payload: make([]byte, BranchFactor*32)}
	generation := cache.beginRead()
	for _, policy := range []ReadPolicy{ReadForeground, ReadPeer} {
		cache.insertForPolicy(generation, [32]byte{byte(policy)}, body, policy)
	}
	require.Equal(t, uint64(2), cache.Stats().Nodes)
	finish := cache.BeginMutation()
	finish()
	for _, policy := range []ReadPolicy{ReadForeground, ReadPeer} {
		hash := [32]byte{byte(policy)}
		cache.insertForPolicy(generation, hash, body, policy)
		_, ok := cache.lookupForPolicy(hash, policy)
		require.False(t, ok)
	}
	require.Zero(t, cache.Stats().UsedBytes)
	require.Equal(t, uint64(2), cache.Stats().AdmissionDenials)
}

func TestCleanResidencyConcurrentClassesAndMutation(t *testing.T) {
	pool := membudget.New(64 << 10)
	account, err := pool.NewAccount("cache", 0, 0)
	require.NoError(t, err)
	cache := NewCleanResidency(64<<10, account)
	body := prefixNodeBody{kind: storedInner, payload: make([]byte, BranchFactor*32)}
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			policy := ReadPolicy(worker % 2)
			for i := 0; i < 128; i++ {
				hash := [32]byte{byte(worker), byte(i)}
				cache.insertForPolicy(cache.beginRead(), hash, body, policy)
				cache.lookupForPolicy(hash, policy)
				if i%16 == 0 {
					finish := cache.BeginMutation()
					finish()
				}
			}
		}()
	}
	wg.Wait()
	cache.Close()
	require.Zero(t, pool.Snapshot().Used)
	require.Zero(t, pool.Snapshot().InvalidReleases)
}

func TestCleanResidencyPromotionPressureKeepsForeground(t *testing.T) {
	body := prefixNodeBody{kind: storedInner, payload: make([]byte, BranchFactor*32)}
	charge := cleanResidencyFixedBytes + uint64(len(body.payload))
	pool := membudget.New(2 * charge)
	account, err := pool.NewAccount("cache", 0, 0)
	require.NoError(t, err)
	cache := NewCleanResidency(16*charge, account)
	defer cache.Close()
	cache.insert([32]byte{1}, body)
	cache.insertForPolicy(cache.beginRead(), [32]byte{2}, body, ReadPeer)
	_, ok := cache.lookup([32]byte{2})
	require.True(t, ok)
	_, ok = cache.lookup([32]byte{1})
	require.True(t, ok, "failed promotion must preserve the existing foreground working set")
	stats := cache.Stats()
	require.Zero(t, stats.Foreground.Evictions)
	require.Equal(t, uint64(1), stats.Peer.Nodes)
	require.Equal(t, stats.UsedBytes, account.Snapshot().Used)
}

func TestPeerReplyPathsUseBoundedResidency(t *testing.T) {
	for _, mode := range []string{"fat-node", "fetch-pack"} {
		t.Run(mode, func(t *testing.T) {
			family := &cleanResidencyFamily{memoryFamily: newMemoryFamily(), cache: NewCleanResidency(1<<20, nil)}
			t.Cleanup(family.cache.Close)
			source := New(TypeState)
			for i := 0; i < 32; i++ {
				require.NoError(t, source.Put([32]byte{byte(i * 8), 1}, bytes.Repeat([]byte{byte(i)}, 32)))
			}
			require.NoError(t, flushToFamily(source, family.memoryFamily))
			root, err := source.Hash()
			require.NoError(t, err)
			backed, err := NewFromRootHash(TypeState, root, family)
			require.NoError(t, err)
			serve := func() {
				if mode == "fat-node" {
					nodes, err := backed.GetNodeFatByPath([32]byte{}, 0, 2, true)
					require.NoError(t, err)
					require.NotEmpty(t, nodes)
				} else {
					nodes, err := backed.WalkFetchPackNodes(64)
					require.NoError(t, err)
					require.NotEmpty(t, nodes)
				}
			}
			serve()
			first := family.fetches.Load()
			require.Positive(t, family.cache.Stats().Peer.Nodes)
			require.Zero(t, family.cache.Stats().Foreground.Nodes)
			serve()
			require.Equal(t, first, family.fetches.Load(), "repeated peer requests should reuse their bounded segment")
			require.Zero(t, loadedChildCount(backed.tree.root))
		})
	}
}

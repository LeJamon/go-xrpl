package service

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type batchVerificationDatabase struct {
	nodestore.Database

	mu           sync.Mutex
	batchCalls   int
	uncachedRead int
	requests     [][]nodestore.Hash256
	rewrite      func(nodestore.Hash256, []byte) ([]byte, error)
	batchStarted chan struct{}
	batchOnce    sync.Once
}

func (d *batchVerificationDatabase) FetchDataUncached(
	ctx context.Context,
	hash nodestore.Hash256,
) ([]byte, error) {
	return d.fetchUncached(ctx, hash)
}

func (d *batchVerificationDatabase) FetchBatchUncached(
	ctx context.Context,
	hashes []nodestore.Hash256,
	maxNodes int,
	maxBytes int,
) ([]*nodestore.Node, error) {
	if maxNodes <= 0 || maxBytes <= 0 {
		return nil, errors.New("invalid batch limits")
	}
	d.mu.Lock()
	d.batchCalls++
	d.requests = append(d.requests, append([]nodestore.Hash256(nil), hashes...))
	d.mu.Unlock()
	if d.batchStarted != nil {
		d.batchOnce.Do(func() { close(d.batchStarted) })
		<-ctx.Done()
		return nil, ctx.Err()
	}

	count := min(len(hashes), maxNodes)
	nodes := make([]*nodestore.Node, 0, count)
	bufferedBytes := 0
	for _, hash := range hashes[:count] {
		data, err := d.fetchUncached(ctx, hash)
		if err != nil {
			return nil, err
		}
		if data == nil {
			nodes = append(nodes, nil)
			continue
		}
		if len(nodes) > 0 && bufferedBytes+len(data) > maxBytes {
			break
		}
		bufferedBytes += len(data)
		nodes = append(nodes, &nodestore.Node{Hash: hash, Data: data})
	}
	return nodes, nil
}

func (d *batchVerificationDatabase) fetchUncached(
	ctx context.Context,
	hash nodestore.Hash256,
) ([]byte, error) {
	d.mu.Lock()
	d.uncachedRead++
	d.mu.Unlock()
	uncached, ok := d.Database.(interface {
		FetchDataUncached(context.Context, nodestore.Hash256) ([]byte, error)
	})
	if !ok {
		return nil, errors.New("uncached reads are unavailable")
	}
	data, err := uncached.FetchDataUncached(ctx, hash)
	if err != nil || data == nil || d.rewrite == nil {
		return data, err
	}
	return d.rewrite(hash, data)
}

func (d *batchVerificationDatabase) counts() (batch, uncached int, requests [][]nodestore.Hash256) {
	d.mu.Lock()
	defer d.mu.Unlock()
	requests = make([][]nodestore.Hash256, len(d.requests))
	for i := range d.requests {
		requests[i] = append([]nodestore.Hash256(nil), d.requests[i]...)
	}
	return d.batchCalls, d.uncachedRead, requests
}

func TestService_VerifyStoredSHAMapUsesOptionalUncachedBatchFetch(t *testing.T) {
	svc, db, root, _ := newParallelStoredVerificationFixture(t)
	tracked := &batchVerificationDatabase{Database: db}
	svc.nodeStore = tracked
	svc.config.FastLoadWorkers = 4
	var expectedNodes uint64
	require.NoError(t, svc.walkStoredSHAMap(t.Context(), root, shamap.TypeState,
		func([32]byte, *nodestore.Node) error {
			expectedNodes++
			return nil
		},
	))
	before := db.Stats()

	require.NoError(t, svc.verifyStoredSHAMap(t.Context(), root, shamap.TypeState))

	batchCalls, uncachedReads, requests := tracked.counts()
	require.Positive(t, batchCalls)
	require.EqualValues(t, expectedNodes, uncachedReads)
	for _, hashes := range requests {
		require.NotEmpty(t, hashes)
		require.LessOrEqual(t, len(hashes), storedSHAMapVerificationBatchNodes)
		require.True(t, sort.SliceIsSorted(hashes, func(i, j int) bool {
			return bytes.Compare(hashes[i][:], hashes[j][:]) < 0
		}))
	}
	after := db.Stats()
	require.Equal(t, before.CacheSize, after.CacheSize)
	require.Equal(t, before.CacheHits, after.CacheHits)
	require.Equal(t, before.CacheMisses, after.CacheMisses)
	require.Equal(t, before.Reads+expectedNodes, after.Reads)
}

func TestService_VerifyStoredSHAMapBatchPreservesValidation(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		mutate func(nodestore.Hash256, []byte) ([]byte, error)
	}{
		{
			name: "malformed",
			want: "unknown hash prefix",
			mutate: func(_ nodestore.Hash256, _ []byte) ([]byte, error) {
				return []byte("malformed"), nil
			},
		},
		{
			name: "hash mismatch",
			want: "invalid content hash",
			mutate: func(_ nodestore.Hash256, data []byte) ([]byte, error) {
				return append([]byte(nil), data...), nil
			},
		},
		{
			name: "missing",
			want: "is missing",
			mutate: func(_ nodestore.Hash256, _ []byte) ([]byte, error) {
				return nil, nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, db, root, _ := newBatchValidationFixture(t)
			rootNode, _, err := svc.loadStoredSHAMapNode(
				t.Context(), storedSHAMapNode{hash: root}, shamap.TypeState,
			)
			require.NoError(t, err)
			inner, ok := rootNode.(shamap.InnerNodeReader)
			require.True(t, ok)
			child, err := inner.ChildHash(0)
			require.NoError(t, err)
			childHash := nodestore.Hash256(child)
			raw, ok := db.(interface {
				FetchDataUncached(context.Context, nodestore.Hash256) ([]byte, error)
			})
			require.True(t, ok)
			childData, err := raw.FetchDataUncached(t.Context(), childHash)
			require.NoError(t, err)
			tracked := &batchVerificationDatabase{Database: db}
			tracked.rewrite = func(hash nodestore.Hash256, data []byte) ([]byte, error) {
				if hash == childHash {
					if test.name == "hash mismatch" {
						rootData, fetchErr := raw.FetchDataUncached(t.Context(), nodestore.Hash256(root))
						return rootData, fetchErr
					}
					return test.mutate(hash, childData)
				}
				return data, nil
			}
			svc.nodeStore = tracked
			svc.config.FastLoadWorkers = 1
			provider, ok := svc.shamapFamily.(interface {
				FullBelowCache() *shamap.FullBelowCache
			})
			require.True(t, ok)
			provider.FullBelowCache().Bump()

			err = svc.verifyStoredSHAMap(t.Context(), root, shamap.TypeState)
			require.ErrorContains(t, err, test.want)
			require.Zero(t, provider.FullBelowCache().Size())
			batchCalls, _, _ := tracked.counts()
			require.Positive(t, batchCalls)
		})
	}
}

func TestService_VerifyStoredSHAMapBatchRejectsWrongLeafType(t *testing.T) {
	db := newTestNodeStore(t, 128)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	txData := append(protocol.HashPrefixTransactionID().Bytes(), []byte("transaction!")...)
	txHash := storePrefixedVerificationNode(t, db, txData)
	children := make([][32]byte, shamap.BranchFactor)
	for branch := range shamap.BranchFactor {
		if branch == 0 {
			children[branch] = txHash
			continue
		}
		leafData := make([]byte, 4+12+32)
		copy(leafData, protocol.HashPrefixLeafNode().Bytes())
		leafData[4] = 1
		leafData[len(leafData)-1] = byte(branch)
		children[branch] = storePrefixedVerificationNode(t, db, leafData)
	}
	rootData := make([]byte, 4+shamap.BranchFactor*32)
	copy(rootData, protocol.HashPrefixInnerNode().Bytes())
	for branch, child := range children {
		copy(rootData[4+branch*32:], child[:])
	}
	root := storePrefixedVerificationNode(t, db, rootData)
	svc, err := New(Config{
		NodeStore:       db,
		SHAMapFamily:    backend.New(db),
		FastLoadWorkers: 1,
	})
	require.NoError(t, err)
	tracked := &batchVerificationDatabase{Database: db}
	svc.nodeStore = tracked

	err = svc.verifyStoredSHAMap(t.Context(), root, shamap.TypeState)
	require.ErrorContains(t, err, "state tree contains")
	require.ErrorContains(t, err, "transaction")
	batchCalls, _, _ := tracked.counts()
	require.Positive(t, batchCalls)
	provider, ok := svc.shamapFamily.(interface {
		FullBelowCache() *shamap.FullBelowCache
	})
	require.True(t, ok)
	require.Zero(t, provider.FullBelowCache().Size())
}

func TestService_VerifyStoredSHAMapBatchRejectsExcessiveDepth(t *testing.T) {
	db := newTestNodeStore(t, 512)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	children := make([][32]byte, 4)
	for branch := range children {
		leafData := make([]byte, 4+12+32)
		copy(leafData, protocol.HashPrefixLeafNode().Bytes())
		leafData[4] = 1
		leafData[len(leafData)-1] = byte(branch + 1)
		child := storePrefixedVerificationNode(t, db, leafData)
		for range 65 {
			child = storePrefixedVerificationNode(t, db, prefixedInnerVerificationNode(0, child))
		}
		children[branch] = child
	}
	rootData := make([]byte, 4+shamap.BranchFactor*32)
	copy(rootData, protocol.HashPrefixInnerNode().Bytes())
	for branch, child := range children {
		copy(rootData[4+branch*32:], child[:])
	}
	root := storePrefixedVerificationNode(t, db, rootData)
	svc, err := New(Config{
		NodeStore:       db,
		SHAMapFamily:    backend.New(db),
		FastLoadWorkers: 1,
	})
	require.NoError(t, err)
	tracked := &batchVerificationDatabase{Database: db}
	svc.nodeStore = tracked

	err = svc.verifyStoredSHAMap(t.Context(), root, shamap.TypeState)
	require.ErrorContains(t, err, "exceeds maximum depth")
	batchCalls, _, _ := tracked.counts()
	require.Positive(t, batchCalls)
	provider, ok := svc.shamapFamily.(interface {
		FullBelowCache() *shamap.FullBelowCache
	})
	require.True(t, ok)
	require.Zero(t, provider.FullBelowCache().Size())
}

func TestService_VerifyStoredSHAMapBatchCancellationDoesNotPublishProofs(t *testing.T) {
	svc, db, root, _, _ := newStoredVerificationFixture(t, shamap.BranchFactor)
	provider, ok := svc.shamapFamily.(interface {
		FullBelowCache() *shamap.FullBelowCache
	})
	require.True(t, ok)
	cache := provider.FullBelowCache()
	cache.Bump()
	generation := cache.Generation()
	tracked := &batchVerificationDatabase{
		Database:     db,
		batchStarted: make(chan struct{}),
	}
	svc.nodeStore = tracked
	svc.config.FastLoadWorkers = 1
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- svc.verifyStoredSHAMap(ctx, root, shamap.TypeState) }()
	select {
	case <-tracked.batchStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for batch fetch")
	}
	cancel()
	err := <-done
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, cache.Size())
	require.False(t, cache.Has(generation, root))
}

func TestService_WalkStoredSHAMapBatchReadHandlesPartialPrefix(t *testing.T) {
	sm := shamap.New(shamap.TypeState)
	for i := range 8 {
		var key [32]byte
		key[0] = byte(i << 4)
		key[31] = byte(i + 1)
		data := make([]byte, 12)
		data[11] = byte(i + 1)
		require.NoError(t, sm.Put(key, data))
	}
	var entries []shamap.FlushEntry
	require.NoError(t, sm.StoreDirty(func(dirty []shamap.FlushEntry) error {
		entries = append(entries, dirty...)
		return nil
	}))
	byHash := make(map[nodestore.Hash256]*nodestore.Node)
	var stack []storedSHAMapNode
	for _, entry := range entries {
		node, err := shamap.DeserializeFromPrefix(entry.Data)
		require.NoError(t, err)
		if _, inner := node.(shamap.InnerNodeReader); inner {
			continue
		}
		hash := nodestore.Hash256(entry.Hash)
		byHash[hash] = &nodestore.Node{Hash: hash, Data: entry.Data}
		stack = append(stack, storedSHAMapNode{hash: entry.Hash, depth: 1})
	}
	require.Len(t, stack, 8)

	var requests [][]nodestore.Hash256
	var visited []nodestore.Hash256
	want := append([]storedSHAMapNode(nil), stack...)
	err := (&Service{}).walkStoredSHAMapNodesWithBatchFetch(
		t.Context(),
		stack,
		shamap.TypeState,
		func(
			_ context.Context,
			hashes []nodestore.Hash256,
			maxBytes int,
		) ([]*nodestore.Node, kvstore.PromotionStats, error) {
			require.LessOrEqual(t, len(hashes), 2)
			require.Positive(t, maxBytes)
			requests = append(requests, append([]nodestore.Hash256(nil), hashes...))
			return []*nodestore.Node{byHash[hashes[0]]}, kvstore.PromotionStats{}, nil
		},
		2,
		1,
		func(node storedSHAMapNode, _ *nodestore.Node) error {
			visited = append(visited, nodestore.Hash256(node.hash))
			return nil
		},
	)
	require.NoError(t, err)
	require.ElementsMatch(t, want, func() []storedSHAMapNode {
		out := make([]storedSHAMapNode, len(visited))
		for i, hash := range visited {
			out[i] = storedSHAMapNode{hash: [32]byte(hash), depth: 1}
		}
		return out
	}())
	require.Greater(t, len(requests), 1)
	for _, hashes := range requests {
		require.True(t, sort.SliceIsSorted(hashes, func(i, j int) bool {
			return bytes.Compare(hashes[i][:], hashes[j][:]) < 0
		}))
	}
}

func newBatchValidationFixture(t *testing.T) (*Service, nodestore.Database, [32]byte, nodestore.Hash256) {
	t.Helper()
	db := newTestNodeStore(t, 128)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	children := make([][32]byte, shamap.BranchFactor)
	for branch := range children {
		leafData := make([]byte, 4+12+32)
		copy(leafData, protocol.HashPrefixLeafNode().Bytes())
		leafData[4] = 1
		leafData[len(leafData)-1] = byte(branch + 1)
		children[branch] = storePrefixedVerificationNode(t, db, leafData)
	}
	rootData := make([]byte, 4+shamap.BranchFactor*32)
	copy(rootData, protocol.HashPrefixInnerNode().Bytes())
	for branch, child := range children {
		copy(rootData[4+branch*32:], child[:])
	}
	root := storePrefixedVerificationNode(t, db, rootData)
	svc, err := New(Config{
		NodeStore:       db,
		SHAMapFamily:    backend.New(db),
		FastLoadWorkers: 1,
	})
	require.NoError(t, err)
	return svc, db, root, nodestore.Hash256(children[0])
}

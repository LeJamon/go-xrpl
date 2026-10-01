package service

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"

	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/shamap"
	shamapbackend "github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

func newRetentionTestService(t *testing.T) (*Service, *nodestore.RotatingKVDatabase, *shamapbackend.NodeStore) {
	t.Helper()
	store, err := kvpebble.NewRotating(filepath.Join(t.TempDir(), "nodes"), kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200})
	require.NoError(t, err)
	db, err := nodestore.NewRotatingKVDatabase(store, nodestore.DatabaseConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	family := shamapbackend.New(db)
	return &Service{nodeStore: db, shamapFamily: family, logger: xrpllog.Discard()}, db, family
}

func retentionTestLedger(t *testing.T, tree *shamap.SHAMap, seq uint32) *ledger.Ledger {
	t.Helper()
	root, err := tree.Hash()
	require.NoError(t, err)
	h := header.LedgerHeader{LedgerIndex: seq, AccountHash: root}
	h.Hash = header.CalculateHash(h)
	l, err := ledger.NewFromHeader(h, tree, shamap.New(shamap.TypeTransaction), drops.Fees{})
	require.NoError(t, err)
	return l
}

func TestStateRetentionRejectsStalePlan(t *testing.T) {
	for _, change := range []string{"new_root", "generation", "canceled"} {
		t.Run(change, func(t *testing.T) {
			svc, db, family := newRetentionTestService(t)
			tree, err := shamap.NewBacked(shamap.TypeState, family)
			require.NoError(t, err)
			require.NoError(t, tree.Put([32]byte{0x30}, []byte("original-state")))
			svc.validatedLedger = retentionTestLedger(t, tree, 20)
			seq, guard, err := svc.PrepareStateRetention(t.Context(), 20, nil)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch change {
			case "new_root":
				require.NoError(t, tree.Put([32]byte{0x30}, []byte("replacement-state")))
				svc.closedLedger = retentionTestLedger(t, tree, 21)
			case "generation":
				committed, err := db.RotateGeneration(t.Context(), 10, 1)
				require.NoError(t, err)
				require.True(t, committed)
			case "canceled":
				cancel()
			}
			before, err := db.DurableFingerprint(t.Context())
			require.NoError(t, err)
			pruned := false
			committed, err := db.RotateGenerationWithRetention(ctx, seq, 11, guard, func() func() {
				pruned = true
				return family.BeginPrune()
			})
			require.False(t, committed)
			if change == "canceled" {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, errStateRetentionChanged)
			}
			require.False(t, pruned)
			after, err := db.DurableFingerprint(t.Context())
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.True(t, svc.openLedgerMu.TryLock(), "failed admission must release the service guard")
			svc.openLedgerMu.Unlock()
			mutationCtx, mutationCancel := context.WithTimeout(t.Context(), time.Second)
			defer mutationCancel()
			committed, err = db.RotateGeneration(mutationCtx, 40, 21)
			require.NoError(t, err)
			require.True(t, committed, "failed admission must release the generation pin")
		})
	}
}

func TestStateRetentionReconcilesOpenIngressAtAdmission(t *testing.T) {
	for _, size := range []int{1, 8192} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			svc, db, family := newRetentionTestService(t)
			tree, err := shamap.NewBacked(shamap.TypeState, family)
			require.NoError(t, err)
			require.NoError(t, tree.Put([32]byte{0x30}, []byte("original-state")))
			svc.validatedLedger = retentionTestLedger(t, tree, 20)
			svc.openLedger, err = ledger.NewOpenWithHeader(header.LedgerHeader{LedgerIndex: 21}, tree, shamap.New(shamap.TypeTransaction), drops.Fees{})
			require.NoError(t, err)
			seq, guard, err := svc.PrepareStateRetention(t.Context(), 20, nil)
			require.NoError(t, err)
			for i := range size {
				var key [32]byte
				binary.BigEndian.PutUint32(key[:], uint32(i)<<18)
				key[31] = 1
				require.NoError(t, tree.Put(key, []byte("new-open-state")))
			}
			svc.openLedger, err = ledger.NewOpenWithHeader(header.LedgerHeader{LedgerIndex: 21}, tree, shamap.New(shamap.TypeTransaction), drops.Fees{})
			require.NoError(t, err)
			committed, err := db.RotateGenerationWithRetention(t.Context(), seq, 11, guard, family.BeginPrune)
			if size > stateRetentionCatchupNodes {
				require.ErrorIs(t, err, errStateRetentionChanged)
				require.False(t, committed)
				return
			}
			require.NoError(t, err)
			require.True(t, committed)
			root, err := svc.openLedger.StateMapHash()
			require.NoError(t, err)
			require.NoError(t, svc.verifyStoredSHAMap(t.Context(), root, shamap.TypeState))
		})
	}
}

type retentionCountingDatabase struct {
	*nodestore.RotatingKVDatabase
	promotions  atomic.Int64
	failSync    bool
	promoteOnce sync.Once
	onPromote   func()
}

func (d *retentionCountingDatabase) FetchBatchForPromotion(ctx context.Context, hashes []nodestore.Hash256, maxBytes int) ([]*nodestore.Node, kvstore.PromotionStats, error) {
	d.promoteOnce.Do(func() {
		if d.onPromote != nil {
			d.onPromote()
		}
	})
	return d.RotatingKVDatabase.FetchBatchForPromotion(ctx, hashes, maxBytes)
}

func (d *retentionCountingDatabase) FetchForPromotion(ctx context.Context, hash nodestore.Hash256) (*nodestore.Node, error) {
	d.promotions.Add(1)
	d.promoteOnce.Do(func() {
		if d.onPromote != nil {
			d.onPromote()
		}
	})
	return d.RotatingKVDatabase.FetchForPromotion(ctx, hash)
}

func (d *retentionCountingDatabase) Sync(ctx context.Context) error {
	if d.failSync {
		return errors.New("injected sync failure")
	}
	return d.RotatingKVDatabase.Sync(ctx)
}

func TestStateRetentionVisitsOnlyChangedSubtrees(t *testing.T) {
	svc, db, family := newRetentionTestService(t)
	counting := &retentionCountingDatabase{RotatingKVDatabase: db}
	svc.nodeStore = counting
	tree, err := shamap.NewBacked(shamap.TypeState, family)
	require.NoError(t, err)
	for i := range 4096 {
		var key [32]byte
		binary.BigEndian.PutUint32(key[:], uint32(i)<<20)
		key[31] = 1
		require.NoError(t, tree.Put(key, []byte("old-state-value")))
	}
	svc.validatedLedger = retentionTestLedger(t, tree, 20)
	old, err := tree.Hash()
	require.NoError(t, err)
	require.NoError(t, svc.preserveStateDifference(t.Context(), retainedStateRoot{old, shamap.TypeState}, [32]byte{}, 20, nil, 0))
	key := [32]byte{0x30}
	key[31] = 1
	require.NoError(t, tree.Put(key, []byte("changed-state")))
	updated := retentionTestLedger(t, tree, 21)
	root, err := updated.StateMapHash()
	require.NoError(t, err)
	counting.promotions.Store(0)
	require.NoError(t, svc.preserveStateDifference(t.Context(), retainedStateRoot{root, shamap.TypeState}, old, 21, nil, 0))
	require.Positive(t, counting.promotions.Load())
	require.Less(t, counting.promotions.Load(), int64(16), "one changed leaf must not walk 4096 shared leaves")
}

func TestStateRetentionDoesNotPublishPlanAfterSyncFailure(t *testing.T) {
	svc, db, family := newRetentionTestService(t)
	tree, err := shamap.NewBacked(shamap.TypeState, family)
	require.NoError(t, err)
	require.NoError(t, tree.Put([32]byte{0x30}, []byte("example-state")))
	svc.validatedLedger = retentionTestLedger(t, tree, 20)
	svc.nodeStore = &retentionCountingDatabase{RotatingKVDatabase: db, failSync: true}
	_, guard, err := svc.PrepareStateRetention(t.Context(), 20, nil)
	require.ErrorContains(t, err, "injected sync failure")
	require.Nil(t, guard)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	committed, err := db.RotateGeneration(ctx, 20, 1)
	require.NoError(t, err)
	require.True(t, committed, "failed preparation must release the generation pin")
}

func TestStateRetentionRestampsLegacyStateAndTransactions(t *testing.T) {
	ctx := t.Context()
	db, err := nodestore.NewKVDatabase(memorydb.New(), nodestore.DatabaseConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	family := shamapbackend.New(db)
	svc := &Service{nodeStore: db, shamapFamily: family, logger: xrpllog.Discard()}
	state, err := shamap.NewBacked(shamap.TypeState, family)
	require.NoError(t, err)
	require.NoError(t, state.Put([32]byte{0x30}, []byte("old-state-value")))
	txs, err := shamap.NewBacked(shamap.TypeTransaction, family)
	require.NoError(t, err)
	require.NoError(t, txs.PutWithNodeType([32]byte{0x90}, []byte("transaction-data"), shamap.NodeTypeTransactionWithMeta))
	for _, tree := range []*shamap.SHAMap{state, txs} {
		tree.SetLedgerSeq(1)
		require.NoError(t, tree.StoreDirty(func(entries []shamap.FlushEntry) error { return family.StoreBatch(ctx, entries) }))
	}
	stateRoot, err := state.Hash()
	require.NoError(t, err)
	txRoot, err := txs.Hash()
	require.NoError(t, err)
	h := header.LedgerHeader{LedgerIndex: 20, AccountHash: stateRoot, TxHash: txRoot}
	h.Hash = header.CalculateHash(h)
	svc.validatedLedger, err = ledger.NewFromHeader(h, state, txs, drops.Fees{})
	require.NoError(t, err)
	orphan := nodestore.Hash256{0xfe}
	require.NoError(t, db.Store(ctx, &nodestore.Node{Type: nodestore.NodeLedger, Hash: orphan, Data: []byte("obsolete header"), LedgerSeq: 1}))
	_, guard, err := svc.PrepareStateRetention(ctx, 20, nil)
	require.NoError(t, err)
	_, err = db.DeleteBeforeWithRetention(ctx, 11, 16, guard, family.BeginPrune)
	require.NoError(t, err)
	require.NoError(t, svc.verifyStoredSHAMap(ctx, stateRoot, shamap.TypeState))
	require.NoError(t, svc.verifyStoredSHAMap(ctx, txRoot, shamap.TypeTransaction))
	node, err := db.Fetch(ctx, orphan)
	require.NoError(t, err)
	require.Nil(t, node)
}

func TestStateRetentionPreservesRevivedArchiveSubtree(t *testing.T) {
	for _, name := range []string{"unread_subtree", "cached_subtree", "linear_successor", "validation_candidate", "pending_persistence", "adopted_during_refresh"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			svc, db, family := newRetentionTestService(t)
			persist := func(tree *shamap.SHAMap) {
				require.NoError(t, tree.StoreDirty(func(entries []shamap.FlushEntry) error { return family.StoreBatch(ctx, entries) }))
				require.NoError(t, db.Sync(ctx))
			}
			old, err := shamap.NewBacked(shamap.TypeState, family)
			require.NoError(t, err)
			old.SetLedgerSeq(10)
			for _, key := range [][32]byte{{0x30}, {0x31}, {0x90}} {
				require.NoError(t, old.Put(key, []byte("old-state-value")))
			}
			persist(old)
			oldRoot, err := old.Hash()
			require.NoError(t, err)
			selected, err := old.SnapshotMutableWithLedgerSeq(20)
			require.NoError(t, err)
			require.NoError(t, selected.Put([32]byte{0x30}, []byte("selected-state")))
			persist(selected)
			selectedRoot, err := selected.Hash()
			require.NoError(t, err)
			committed, err := db.RotateGeneration(ctx, 10, 1)
			require.NoError(t, err)
			require.True(t, committed)

			require.NoError(t, svc.refreshGenerationState(ctx, selectedRoot, 20, db, nil))
			reuseRoot := oldRoot
			if name == "linear_successor" {
				reuseRoot = selectedRoot
			}
			reused, err := shamap.NewFromRootHash(shamap.TypeState, reuseRoot, family)
			require.NoError(t, err)
			reused.SetLedgerSeq(21)
			if name == "cached_subtree" {
				_, found, err := reused.Get([32]byte{0x32})
				require.NoError(t, err)
				require.False(t, found)
			}
			require.NoError(t, reused.Put([32]byte{0x90}, []byte("new-state-value")))
			persist(reused)
			liveRoot, err := reused.Hash()
			require.NoError(t, err)
			require.NoError(t, svc.verifyStoredSHAMap(ctx, liveRoot, shamap.TypeState))
			missing, err := reused.GetMissingNodesContext(ctx, 64, nil)
			require.NoError(t, err)
			require.Empty(t, missing)
			svc.validatedLedger = retentionTestLedger(t, selected, 20)
			live := retentionTestLedger(t, reused, 21)
			switch name {
			case "adopted_during_refresh":
				svc.nodeStore = &retentionCountingDatabase{RotatingKVDatabase: db, onPromote: func() {
					svc.mu.Lock()
					svc.closedLedger = live
					svc.mu.Unlock()
				}}
			case "validation_candidate":
				svc.validationCandidates = map[uint32]*ledger.Ledger{21: live}
			case "pending_persistence":
				// A dequeued job remains an owner until persistence finishes.
				svc.persistActive = &persistJob{l: live}
			default:
				svc.closedLedger = live
			}
			seq, guard, err := svc.PrepareStateRetention(ctx, 20, nil)
			require.NoError(t, err)
			require.Equal(t, uint32(20), seq)
			committed, err = db.RotateGenerationWithRetention(ctx, seq, 11, guard, family.BeginPrune)
			require.NoError(t, err)
			require.True(t, committed)
			require.NoError(t, svc.verifyStoredSHAMap(ctx, selectedRoot, shamap.TypeState))
			cold, err := shamap.NewFromRootHash(shamap.TypeState, liveRoot, family)
			require.NoError(t, err)
			_, _, lookupErr := cold.Get([32]byte{0x32})
			require.NoError(t, lookupErr)
			require.NoError(t, svc.verifyStoredSHAMap(ctx, liveRoot, shamap.TypeState), "rotation discarded a clean reused subtree of a persisted newer root")
		})
	}
}

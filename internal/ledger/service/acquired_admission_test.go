package service

import (
	"context"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

type acquiredAdmissionFamily struct {
	*replayAdmissionFamily
	pinMu       sync.Mutex
	activePins  int
	corruptHash [32]byte
}

func (f *acquiredAdmissionFamily) AcquireDurableSnapshot(ctx context.Context) ([32]byte, func(), error) {
	fingerprint, release, err := f.replayAdmissionFamily.AcquireDurableSnapshot(ctx)
	if err != nil {
		return fingerprint, nil, err
	}
	f.pinMu.Lock()
	f.activePins++
	f.pinMu.Unlock()
	return fingerprint, sync.OnceFunc(func() {
		release()
		f.pinMu.Lock()
		f.activePins--
		f.pinMu.Unlock()
	}), nil
}

func (f *acquiredAdmissionFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	if hash == f.corruptHash {
		return []byte("corrupt acquired node"), nil
	}
	return f.replayAdmissionFamily.FetchDurable(ctx, hash)
}

func (f *acquiredAdmissionFamily) pins() int {
	f.pinMu.Lock()
	defer f.pinMu.Unlock()
	return f.activePins
}

func TestAcquiredAdmissionCertifiedBaseSafety(t *testing.T) {
	for _, scenario := range []string{
		"unchanged", "changed", "no proof", "candidate only", "invalidated", "generation changed",
		"missing base", "missing changed node", "corrupt changed node", "missing transaction", "canceled",
	} {
		t.Run(scenario, func(t *testing.T) {
			family := &acquiredAdmissionFamily{replayAdmissionFamily: newReplayAdmissionFamily(t)}
			svc, err := New(Config{SHAMapFamily: family})
			require.NoError(t, err)
			base := persistedReplayAdmissionState(t, family.replayAdmissionFamily, 4096)
			root, err := base.Hash()
			require.NoError(t, err)
			baseHeader := header.LedgerHeader{LedgerIndex: 100, AccountHash: root}
			baseHeader.Hash = header.CalculateHash(baseHeader)
			// Establish the same complete-tree certificate that strict startup
			// or checkpoint acceptance supplies. A candidate alone is not proof.
			require.NoError(t, svc.VerifyDetachedMaps(t.Context(), base, nil))
			fingerprint, err := family.DurableFingerprint(t.Context())
			require.NoError(t, err)
			if scenario == "candidate only" {
				svc.rememberValidatedStateBaseCandidate(baseHeader)
			} else if scenario != "no proof" {
				svc.rememberValidatedStateBase(baseHeader, fingerprint)
			}
			state, err := base.SnapshotMutableContext(t.Context())
			require.NoError(t, err)
			key := [32]byte{0xf0, 1}
			if scenario != "unchanged" {
				require.NoError(t, state.PutWithNodeType(key, []byte("acquired-changed-value"), shamap.NodeTypeAccountState))
			}
			state, err = state.SnapshotImmutableContext(t.Context())
			require.NoError(t, err)
			targetRoot, err := state.Hash()
			require.NoError(t, err)
			h := header.LedgerHeader{LedgerIndex: 1300, ParentHash: [32]byte{0xaa}, AccountHash: targetRoot}
			var txMap *shamap.SHAMap
			expectFailure := false
			ctx := t.Context()
			switch scenario {
			case "invalidated":
				svc.InvalidateFastLoadCheckpointEligibility()
			case "generation changed":
				// A missing unchanged subtree MUST be detected after the base's
				// generation expires, even though warm aliases remain usable.
				family.hide(replayAdmissionFirstChildHash(t, family.replayAdmissionFamily, root))
				expectFailure = true
			case "missing base":
				family.hideWithoutGeneration(root)
			case "missing changed node", "corrupt changed node":
				missing := replayAdmissionPathDescendantHash(t, family.replayAdmissionFamily, targetRoot, key, 1)
				if scenario == "missing changed node" {
					family.hideWithoutGeneration(missing)
				} else {
					family.corruptHash = missing
				}
				expectFailure = true
			case "missing transaction":
				transactions := shamap.New(shamap.TypeTransaction)
				require.NoError(t, transactions.PutWithNodeType([32]byte{0x11}, []byte("acquired-transaction"), shamap.NodeTypeTransactionWithMeta))
				require.NoError(t, transactions.StoreDirty(func(entries []shamap.FlushEntry) error {
					return family.StoreBatch(ctx, entries)
				}))
				h.TxHash, err = transactions.Hash()
				require.NoError(t, err)
				txMap, err = shamap.NewFromRootHashContext(ctx, shamap.TypeTransaction, h.TxHash, family)
				require.NoError(t, err)
				family.hideWithoutGeneration(replayAdmissionFirstChildHash(t, family.replayAdmissionFamily, h.TxHash))
				expectFailure = true
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				expectFailure = true
			}
			h.Hash = header.CalculateHash(h)
			family.FullBelowCache().Bump()
			family.resetReads()
			prepared, err := svc.PrepareAcquiredLedger(ctx, &h, state, txMap)
			if expectFailure {
				require.Error(t, err)
				require.Nil(t, prepared)
				require.Zero(t, family.pins(), "failed admission leaked a generation pin")
				require.Zero(t, svc.replayPreparedSlotCount)
				return
			}
			require.NoError(t, err)
			defer prepared.Release()
			stats := prepared.Stats()
			sparse := scenario == "unchanged" || scenario == "changed"
			require.Equal(t, sparse, stats.State.UsedVerifiedBase)
			require.False(t, stats.ParentProofUsed, "a checkpoint must not masquerade as a replay parent")
			if sparse {
				require.Less(t, family.readCount(), 128)
				require.Positive(t, stats.State.EqualSubtreesSkipped)
			} else {
				require.Greater(t, family.readCount(), 4000, "unavailable proof must fall back to cold verification")
			}
			require.Positive(t, family.pins(), "retain the generation through publication")
			prepared.Release()
			require.Zero(t, family.pins())
		})
	}
}

func TestAcquiredAdmissionReusesLoadedCheckpointForNonconsecutivePivot(t *testing.T) {
	ctx := t.Context()
	db := newTestNodeStore(t, 0)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	tracked := &checkpointTrackingDatabase{Database: db, uncached: db}
	repositories := newTestRepositories(t, ctx)
	writer := newFastLoadCheckpointService(t, tracked, repositories, true)
	require.NoError(t, writer.Start())
	for i := range 4096 {
		key := [32]byte{byte(i), byte(i >> 8), 1}
		require.NoError(t, writer.openLedger.Insert(keylet.Keylet{Key: key}, []byte("checkpoint-state-value")))
	}
	closeStoredLedgerFixture(t, writer)
	writer.FlushPersists()
	writer.Stop()
	ready, err := writer.PrepareFastLoadCheckpoint(ctx)
	require.NoError(t, err)
	require.True(t, ready)

	reader := newFastLoadCheckpointService(t, tracked, repositories, false)
	require.NoError(t, reader.Start())
	t.Cleanup(reader.Stop)
	require.True(t, reader.IsFastLoadProvisional())
	proof, ok := reader.currentValidatedStateBaseProof()
	require.True(t, ok, "checkpoint acceptance must create the service-owned certificate")
	state, err := reader.GetValidatedLedger().StateMapSnapshotContext(ctx)
	require.NoError(t, err)
	changedKey := [32]byte{0xf0, 1}
	require.NoError(t, state.PutWithNodeType(changedKey, []byte("new-pivot-value"), shamap.NodeTypeAccountState))
	state, err = state.SnapshotImmutableContext(ctx)
	require.NoError(t, err)
	root, err := state.Hash()
	require.NoError(t, err)
	h := header.LedgerHeader{LedgerIndex: proof.sequence + 1200, ParentHash: [32]byte{0xab}, AccountHash: root}
	h.Hash = header.CalculateHash(h)
	require.NotEqual(t, proof.ledgerHash, h.ParentHash, "the checkpoint is a comparison base, not a replay parent")
	// Evict shallow cached proofs to require the root-to-root Merkle diff.
	reader.shamapFamily.(interface{ FullBelowCache() *shamap.FullBelowCache }).FullBelowCache().Bump()
	tracked.resetUncachedReads()
	prepared, err := reader.PrepareAcquiredLedger(ctx, &h, state, nil)
	require.NoError(t, err)
	defer prepared.Release()
	stats := prepared.Stats()
	require.True(t, stats.GenerationPinned)
	require.False(t, stats.ParentProofUsed)
	require.True(t, stats.State.UsedVerifiedBase)
	require.Positive(t, stats.State.EqualSubtreesSkipped)
	require.Positive(t, stats.State.DurableReads, "changed paths still require durable verification")
	require.Less(t, tracked.uncachedReads(), 128, "admission must not re-walk the entire checkpoint state")
	t.Logf("checkpoint-relative admission: reads=%d loaded=%d skipped=%d", tracked.uncachedReads(), stats.State.NodesLoaded, stats.State.EqualSubtreesSkipped)
}

func TestAcquiredAdmissionPublishesWithoutRepeatingColdWalk(t *testing.T) {
	for _, generic := range []bool{false, true} {
		name := "consensus"
		if generic {
			name = "generic"
		}
		t.Run(name, func(t *testing.T) {
			family := newReplayAdmissionFamily(t)
			cfg := DefaultConfig()
			cfg.SHAMapFamily = family
			svc, err := New(cfg)
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			t.Cleanup(svc.Stop)
			state := persistedReplayAdmissionState(t, family, 4096)
			root, err := state.Hash()
			require.NoError(t, err)
			h := header.LedgerHeader{LedgerIndex: 100, ParentHash: svc.GetClosedLedger().Hash(), AccountHash: root}
			h.Hash = header.CalculateHash(h)
			prepared, err := svc.PrepareAcquiredLedger(t.Context(), &h, state, nil)
			require.NoError(t, err)
			defer prepared.Release()
			require.Greater(t, prepared.Stats().State.NodesLoaded, uint64(4000))
			family.resetReads()
			if generic {
				require.NoError(t, svc.StorePreparedLedger(t.Context(), prepared))
			} else {
				_, err = svc.BootstrapPreparedLedger(t.Context(), prepared)
				require.NoError(t, err)
			}
			require.Less(t, family.readCount(), 20, "publication repeated full-state verification")
			stored, err := svc.GetLedgerByHash(h.Hash)
			require.NoError(t, err)
			family.resetReads()
			require.NoError(t, svc.VerifyDetachedLedger(t.Context(), stored))
			require.Less(t, family.readCount(), 20, "activation repeated full-state verification")
			proof, err := svc.PrepareReplayParent(t.Context(), stored)
			require.NoError(t, err)
			defer proof.Release()
			require.True(t, proof.Stats().ProofCacheHit, "first replay should reuse the acquired parent's proof")
			require.Error(t, svc.StorePreparedLedger(t.Context(), prepared), "token must remain single-use")
		})
	}
}

func TestAcquiredAdmissionRejectsMissingStateAndChangedGeneration(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	state := persistedReplayAdmissionState(t, family, 128)
	root, err := state.Hash()
	require.NoError(t, err)
	h := header.LedgerHeader{LedgerIndex: 100, AccountHash: root}
	h.Hash = header.CalculateHash(h)
	prepared, err := svc.PrepareAcquiredLedger(t.Context(), &h, state, shamap.New(shamap.TypeTransaction))
	require.NoError(t, err)
	defer prepared.Release()
	family.hide(replayAdmissionFirstChildHash(t, family, root))
	_, err = svc.BootstrapPreparedLedger(t.Context(), prepared)
	require.ErrorContains(t, err, "generation changed")
	missing, err := svc.PrepareAcquiredLedger(t.Context(), &h, state, nil)
	require.Error(t, err)
	require.Nil(t, missing)
	h.Hash[0] ^= 1
	_, err = svc.PrepareAcquiredLedger(t.Context(), &h, state, nil)
	require.ErrorContains(t, err, "header hash mismatch")
}

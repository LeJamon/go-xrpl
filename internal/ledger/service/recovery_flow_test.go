package service

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/skiplist"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

// Exercise the entire certificate lifecycle, not just an isolated cache hit:
// clean checkpoint -> nonconsecutive pivot -> queued persistence -> normal
// validation -> one-ledger recovery -> another clean restart checkpoint.
func TestRecoveryFlowCheckpointPivotLiveAndReplay(t *testing.T) {
	ctx := t.Context()
	db := newTestNodeStore(t, 0)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	tracked := &checkpointTrackingDatabase{Database: db, uncached: db}
	repositories := newTestRepositories(t, ctx)
	writer := newFastLoadCheckpointService(t, tracked, repositories, true)
	require.NoError(t, writer.Start())
	for i := range 4096 {
		key := [32]byte{byte(i), byte(i >> 8), 1}
		require.NoError(t, writer.openLedger.Insert(keylet.Keylet{Key: key}, []byte("recovery-flow-state")))
	}
	closeStoredLedgerFixture(t, writer)
	writer.Stop()
	ready, err := writer.PrepareFastLoadCheckpoint(ctx)
	require.NoError(t, err)
	require.True(t, ready)

	svc := newFastLoadCheckpointService(t, tracked, repositories, false)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	svc.StopStateBaseRecertification() // No background full walk may hide a lost handoff.
	base := svc.GetValidatedLedger()
	state, err := base.StateMapSnapshotContext(ctx)
	require.NoError(t, err)
	require.NoError(t, state.PutWithNodeType([32]byte{0xf0, 1}, []byte("pivot delta state"), shamap.NodeTypeAccountState))
	// Give the synthetic distant pivot internally consistent ancestry SLEs.
	pivotSeq := base.Sequence() + 1200
	require.NoError(t, skiplist.Write(state, keylet.LedgerHashes().Key, nil, make([][32]byte, 256), pivotSeq-1))
	lastHistorical := (pivotSeq - 1) &^ uint32(255)
	require.NoError(t, skiplist.Write(state, keylet.LedgerHashesForSeq(pivotSeq).Key, nil, make([][32]byte, lastHistorical/256), lastHistorical))
	root, err := state.Hash()
	require.NoError(t, err)
	h := base.Header()
	h.LedgerIndex += 1200
	h.ParentHash = [32]byte{0xab}
	h.AccountHash, h.TxHash = root, [32]byte{}
	// Replay commonly constructs the transaction tree in memory. Its proof
	// becomes durable through the persistence job, not a pre-existing cache.
	txs := shamap.New(shamap.TypeTransaction)
	rawTx, _ := validRelationalTestTransaction(t, 1)
	txBlob, txHash := makeTxMetaBlobForTest(t, rawTx, 0)
	require.NoError(t, txs.PutWithNodeType(txHash, txBlob, shamap.NodeTypeTransactionWithMeta))
	h.TxHash, err = txs.Hash()
	require.NoError(t, err)
	h.Hash = header.CalculateHash(h)
	cache := svc.shamapFamily.(interface{ FullBelowCache() *shamap.FullBelowCache }).FullBelowCache()
	cache.Bump()
	prepared, err := svc.PrepareAcquiredLedger(ctx, &h, state, txs)
	require.NoError(t, err)
	require.True(t, prepared.Stats().State.UsedVerifiedBase)
	_, err = svc.BootstrapPreparedLedger(ctx, prepared)
	require.NoError(t, err)
	pivot, err := svc.GetLedgerByHash(h.Hash)
	require.NoError(t, err)
	require.NoError(t, svc.SwitchToPreferredLedger(pivot))

	// Hold the canonical publication while its job takes ownership of the
	// certificate. Evict every transient proof before that job can finish.
	svc.canonicalPersistMu.Lock()
	svc.SetValidatedLedgerAt(h.LedgerIndex, h.Hash, time.Time{})
	svc.persistMu.Lock()
	job := svc.validatedPersistJobs[h.LedgerIndex]
	var captured bool
	if job != nil {
		captured = job.proof != nil
	}
	svc.persistMu.Unlock()
	prepared.Release()
	svc.replayProofMu.Lock()
	svc.replayProofs = nil
	svc.replayProofMu.Unlock()
	cache.Bump()
	svc.canonicalPersistMu.Unlock()
	require.True(t, captured, "persistence must own the certificate beyond prepared-token release")
	svc.FlushPersists()
	proof, found := svc.currentValidatedStateBaseProof()
	require.True(t, found)
	require.Equal(t, h.Hash, proof.ledgerHash)

	for range 300 {
		closeStoredLedgerFixture(t, svc)
	}
	svc.FlushPersists()
	live := svc.GetValidatedLedger()
	proof, found = svc.currentValidatedStateBaseProof()
	require.True(t, found)
	require.Equal(t, live.Hash(), proof.ledgerHash)
	cache.Bump()
	tracked.resetUncachedReads()
	parent, err := svc.PrepareReplayParent(ctx, live)
	require.NoError(t, err)
	require.True(t, parent.Stats().ProofCacheHit)
	require.Zero(t, parent.Stats().State.NodesLoaded)
	parent.Release()
	require.Less(t, tracked.uncachedReads(), 8, "warm recovery must not traverse 4,096 leaves")
	svc.Stop()
	ready, err = svc.PrepareFastLoadCheckpoint(ctx)
	require.NoError(t, err)
	require.True(t, ready)
	restarted := newFastLoadCheckpointService(t, tracked, repositories, false)
	require.NoError(t, restarted.Start())
	t.Cleanup(restarted.Stop)
	require.Equal(t, live.Hash(), restarted.GetValidatedLedger().Hash())
	require.True(t, restarted.fastLoadBaseVerified)
}

func TestRecoveryPersistenceProofRejectsInvalidIdentityAndGeneration(t *testing.T) {
	for _, change := range []string{"valid", "generation", "state", "transactions", "header", "state-only"} {
		t.Run(change, func(t *testing.T) {
			f := newStateBaseRecertificationFixture(t)
			f.svc.StopStateBaseRecertification()
			h := f.validated.Header()
			fp, err := f.db.DurableFingerprint(t.Context())
			require.NoError(t, err)
			f.svc.rememberReplayProof(h, fp, true)
			proof := f.svc.replayPersistenceProof(h)
			require.NotNil(t, proof)
			f.svc.replayProofMu.Lock()
			f.svc.replayProofs = nil
			f.svc.replayProofMu.Unlock()
			switch change {
			case "generation":
				proof.nodeStoreFingerprint[0]++
			case "state":
				proof.stateRoot[0]++
			case "transactions":
				proof.txRoot[0]++
			case "header":
				proof.ledgerHash[0]++
			case "state-only":
				proof.transactionComplete = false
			}
			f.svc.validatedStateBaseMu.Lock()
			f.svc.validatedStateBaseProof = nil
			f.svc.validatedStateBaseCandidate = nil
			f.svc.validatedStateBaseMu.Unlock()
			require.NoError(t, f.svc.advanceValidatedStateBaseProofWithReplay(t.Context(), f.validated, proof))
			_, found := f.svc.currentValidatedStateBaseProof()
			require.Equal(t, change == "valid", found)
		})
	}
}

func TestRecoveryParentDiffRequiresCertifiedCurrentGeneration(t *testing.T) {
	for _, mode := range []string{"certified", "candidate", "expired"} {
		t.Run(mode, func(t *testing.T) {
			family := newReplayAdmissionFamily(t)
			svc, err := New(Config{SHAMapFamily: family})
			require.NoError(t, err)
			state := persistedReplayAdmissionState(t, family, 4096)
			base := replayAdmissionLedger(t, 500, [32]byte{50}, [32]byte{49}, state, shamap.New(shamap.TypeTransaction))
			if mode == "candidate" {
				svc.rememberValidatedStateBaseCandidate(base.Header())
			} else {
				svc.rememberValidatedStateBase(base.Header(), family.fingerprint)
			}
			changed, err := base.StateMapSnapshot()
			require.NoError(t, err)
			require.NoError(t, changed.PutWithNodeType([32]byte{0xf0, 1}, []byte("changed parent state"), shamap.NodeTypeAccountState))
			parent := replayAdmissionLedger(t, 800, [32]byte{80}, [32]byte{79}, changed, shamap.New(shamap.TypeTransaction))
			if mode == "expired" {
				family.bumpFingerprint()
			}
			family.FullBelowCache().Bump()
			family.resetReads()
			proof, err := svc.PrepareReplayParent(t.Context(), parent)
			require.NoError(t, err)
			defer proof.Release()
			if mode == "certified" {
				require.True(t, proof.Stats().State.UsedVerifiedBase)
				require.Positive(t, proof.Stats().State.EqualSubtreesSkipped)
				require.Less(t, family.readCount(), 128)
			} else {
				require.False(t, proof.Stats().State.UsedVerifiedBase)
				require.Greater(t, family.readCount(), 4096)
			}
		})
	}
}

func TestRecoveryStateWalkLeavesTransactionCapacityAndBackgroundYields(t *testing.T) {
	capacity := uint64(2 * ReplayVerificationWorkspaceBytes)
	pool := membudget.New(capacity)
	account, err := pool.NewAccount("verification", capacity, capacity)
	require.NoError(t, err)
	svc, err := New(Config{ReplayVerificationMemory: account, FastLoadWorkers: 8})
	require.NoError(t, err)
	releaseGate, err := svc.acquireForegroundStateVerification(t.Context())
	require.NoError(t, err)
	defer releaseGate()
	workers, _, releaseState, err := svc.acquireReplayWorkspace(t.Context(), shamap.TypeState)
	require.NoError(t, err)
	defer releaseState()
	require.Equal(t, 1, workers)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	workers, _, releaseTx, err := svc.acquireReplayWorkspace(ctx, shamap.TypeTransaction)
	require.NoError(t, err, "transaction verification starved behind state scan")
	require.Equal(t, 1, workers)
	releaseTx()
	canceled, stop := context.WithCancel(t.Context())
	stop()
	_, err = svc.acquireForegroundStateVerification(canceled)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, svc.backgroundStateVerificationPolicy().pause(canceled, 0), context.Canceled)
	done := make(chan error, 1)
	go func() { done <- svc.backgroundStateVerificationPolicy().pause(ctx, 0) }()
	select {
	case <-done:
		t.Fatal("background scan did not yield to foreground recovery")
	case <-time.After(20 * time.Millisecond):
	}
	releaseState()
	releaseGate()
	require.NoError(t, <-done)
	require.Zero(t, pool.Snapshot().Used)
}

func TestRecoveryRetentionTransfersVerifiedRootsAcrossRotation(t *testing.T) {
	_, db, family := newRetentionTestService(t)
	svc, err := New(Config{Standalone: true, GenesisConfig: genesis.DefaultConfig(), NodeStore: db, SHAMapFamily: family, FastLoad: true})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	svc.StopStateBaseRecertification()
	closeStoredLedgerFixture(t, svc)
	svc.FlushPersists()
	require.NoError(t, svc.recertifyValidatedStateBase(t.Context()))
	for range 2 {
		closeStoredLedgerFixture(t, svc)
		svc.FlushPersists()
		l := svc.GetValidatedLedger()
		before, err := db.DurableFingerprint(t.Context())
		require.NoError(t, err)
		seq, guard, err := svc.PrepareStateRetention(t.Context(), l.Sequence(), nil)
		require.NoError(t, err)
		committed, err := db.RotateGenerationWithRetention(t.Context(), seq, 1, guard, func() func() {
			svc.InvalidateFastLoadCheckpointEligibility()
			return family.BeginPrune()
		})
		require.NoError(t, err)
		require.True(t, committed)
		after, err := db.DurableFingerprint(t.Context())
		require.NoError(t, err)
		require.NotEqual(t, before, after)
		record := svc.replayPersistenceProof(l.Header())
		require.NotNil(t, record)
		require.True(t, record.matchesExactHeader(l.Header(), after, true))
		require.False(t, record.matchesExactHeader(l.Header(), before, true))
		reads := db.Stats().Reads
		require.NoError(t, svc.recertifyValidatedStateBase(t.Context()))
		require.Less(t, db.Stats().Reads-reads, uint64(20), "retention already verified the trees")
		proof, found := svc.currentValidatedStateBaseProof()
		require.True(t, found)
		require.Equal(t, after, proof.nodeStoreFingerprint)
		require.Equal(t, uint32(fastLoadCheckpointEligible), svc.fastLoadCheckpointState.Load())
	}
}

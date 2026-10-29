package service

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestRememberLocalChildReplayProofIsStateOnlyAndGenerationBound(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)

	parentState := persistedReplayAdmissionState(t, family, 64)
	parentHash := [32]byte{0x31}
	parent := replayAdmissionLedger(t, 100, parentHash, [32]byte{0x30}, parentState, shamap.New(shamap.TypeTransaction))
	parentProof, err := svc.PrepareReplayParent(t.Context(), parent)
	require.NoError(t, err)
	defer parentProof.Release()

	childState, err := parent.StateMapSnapshotContext(t.Context())
	require.NoError(t, err)
	require.NoError(t, childState.PutWithNodeType([32]byte{0xf1}, []byte("local-child-state"), shamap.NodeTypeAccountState))
	childStateRoot, err := childState.Hash()
	require.NoError(t, err)
	childHeader := header.LedgerHeader{
		LedgerIndex: parent.Sequence() + 1,
		ParentHash:  parent.Hash(),
		AccountHash: childStateRoot,
	}
	childHeader.Hash = header.CalculateHash(childHeader)
	child := replayAdmissionLedger(t, childHeader.LedgerIndex, childHeader.Hash, childHeader.ParentHash, childState, shamap.New(shamap.TypeTransaction))

	recorded, err := svc.rememberLocalChildReplayProof(t.Context(), parent, child)
	require.NoError(t, err)
	require.True(t, recorded)
	fingerprint := family.fingerprint
	require.True(t, svc.lookupReplayProof(child.Header(), fingerprint, false))
	require.False(t, svc.lookupReplayProof(child.Header(), fingerprint, true), "local transaction tree is not durable yet")

	family.bumpFingerprint()
	require.False(t, svc.lookupReplayProof(child.Header(), family.fingerprint, false), "a rotated generation must invalidate local provenance")
}

func TestRememberLocalChildReplayProofRequiresParentProof(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)

	parentState := persistedReplayAdmissionState(t, family, 8)
	parentHash := [32]byte{0x41}
	parent := replayAdmissionLedger(t, 200, parentHash, [32]byte{0x40}, parentState, shamap.New(shamap.TypeTransaction))
	childState, err := parent.StateMapSnapshotContext(t.Context())
	require.NoError(t, err)
	require.NoError(t, childState.PutWithNodeType([32]byte{0xf2}, []byte("unproven-child-state"), shamap.NodeTypeAccountState))
	childStateRoot, err := childState.Hash()
	require.NoError(t, err)
	childHeader := header.LedgerHeader{LedgerIndex: 201, ParentHash: parentHash, AccountHash: childStateRoot}
	childHeader.Hash = header.CalculateHash(childHeader)
	child := replayAdmissionLedger(t, childHeader.LedgerIndex, childHeader.Hash, childHeader.ParentHash, childState, shamap.New(shamap.TypeTransaction))

	recorded, err := svc.rememberLocalChildReplayProof(t.Context(), parent, child)
	require.NoError(t, err)
	require.False(t, recorded)
	require.False(t, svc.lookupReplayProof(child.Header(), family.fingerprint, false))
}

func TestAcceptConsensusResultRetainsLocalChildReplayProofAcrossRollback(t *testing.T) {
	ctx := t.Context()
	db := newTestNodeStore(t, 0)
	family := &replayAdmissionFamily{
		NodeStore:   backend.New(db),
		fingerprint: [32]byte{1},
		hidden:      make(map[[32]byte]bool),
		readHashes:  make(map[[32]byte]int),
	}
	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  family,
		FastLoad:      true,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(func() {
		svc.Stop()
		require.NoError(t, db.Close())
	})
	svc.StopStateBaseRecertification()

	closeStoredLedgerFixture(t, svc)
	svc.FlushPersists()
	parent := svc.GetValidatedLedger()
	require.NotNil(t, parent)

	parentProof, err := svc.PrepareReplayParent(ctx, parent)
	require.NoError(t, err)
	parentProof.Release()
	require.True(t, svc.lookupReplayProof(parent.Header(), family.fingerprint, false))

	// Keep the child's durable sync and successor relay independent. The child
	// is observable at the canonical frontier before either delayed operation
	// completes.
	dbGate := &checkpointTrackingDatabase{
		Database:        db,
		uncached:        db,
		blockOnSyncCall: 1,
		syncStarted:     make(chan struct{}, 1),
		syncRelease:     make(chan struct{}),
	}
	svc.nodeStore = dbGate
	blob, _ := startupPaymentBlob(t, "local-child-provenance", 1)
	outcome, err := svc.SubmitOpenLedgerTxDetailed(blob, true)
	require.NoError(t, err)
	require.True(t, outcome.Applied)

	relayStarted := make(chan struct{})
	relayRelease := make(chan struct{})
	var relayBlock sync.Once
	var releaseOnce sync.Once
	var databaseReleaseOnce sync.Once
	svc.SetTxRelay(func([]byte) {
		relayBlock.Do(func() {
			close(relayStarted)
			<-relayRelease
		})
	})
	defer releaseOnce.Do(func() { close(relayRelease) })
	defer databaseReleaseOnce.Do(func() { close(dbGate.syncRelease) })

	type acceptanceResult struct {
		sequence uint32
		err      error
	}
	accepted := make(chan acceptanceResult, 1)
	go func() {
		sequence, acceptErr := svc.AcceptConsensusResult(
			ctx, parent, nil, nil, parent.CloseTime().Add(time.Second), true,
		)
		accepted <- acceptanceResult{sequence: sequence, err: acceptErr}
	}()
	select {
	case <-dbGate.syncStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("local child persistence did not reach the delayed sync")
	}
	select {
	case <-relayStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("local child publication did not reach the delayed relay")
	}

	child := svc.GetClosedLedger()
	require.NotNil(t, child)
	require.Equal(t, parent.Sequence()+1, child.Sequence())
	childHeader := child.Header()
	require.True(t, svc.lookupReplayProof(childHeader, family.fingerprint, false))
	require.False(t, svc.lookupReplayProof(childHeader, family.fingerprint, true), "delayed persistence must not certify the child transaction tree")

	releaseOnce.Do(func() { close(relayRelease) })
	acceptedResult := <-accepted
	require.NoError(t, acceptedResult.err)
	require.Equal(t, child.Sequence(), acceptedResult.sequence)

	require.NoError(t, svc.SwitchToPreferredLedger(parent))
	require.Same(t, parent, svc.GetClosedLedger())
	family.FullBelowCache().Bump()
	family.resetReads()
	require.NoError(t, svc.SwitchToPreferredLedger(child))
	require.Equal(t, child.Hash(), svc.GetClosedLedger().Hash())
	returnReads := family.readCount()
	require.LessOrEqual(t, returnReads, 2, "returning a locally proven child must not cold-walk its state tree")

	databaseReleaseOnce.Do(func() { close(dbGate.syncRelease) })
	svc.FlushPersists()

	conflict, err := ledger.NewOpen(parent, parent.CloseTime().Add(2*time.Second))
	require.NoError(t, err)
	require.NoError(t, conflict.Close(parent.CloseTime().Add(2*time.Second), 0))
	require.Equal(t, child.Sequence(), conflict.Sequence())
	require.NotEqual(t, child.Hash(), conflict.Hash())
	require.False(t, svc.lookupReplayProof(conflict.Header(), family.fingerprint, false), "a same-sequence sibling must not inherit the child proof")
	family.FullBelowCache().Bump()
	family.resetReads()
	require.NoError(t, svc.VerifyDetachedLedger(ctx, conflict))
	conflictReads := family.readCount()
	require.Greater(t, conflictReads, returnReads, "the conflicting sibling must take the durable verification path")
	t.Logf("local child return durable reads=%d, conflicting sibling reads=%d", returnReads, conflictReads)

	family.bumpFingerprint()
	require.False(t, svc.lookupReplayProof(childHeader, family.fingerprint, false), "generation changes invalidate local child provenance")
	family.FullBelowCache().Bump()
	family.resetReads()
	require.NoError(t, svc.VerifyDetachedLedger(ctx, child))
	require.Positive(t, family.readCount(), "generation invalidation must prevent a proof bypass")
}

func TestStateRetentionTransfersOwnedClosedReplayProofsOnly(t *testing.T) {
	svc, db, family := newRetentionTestService(t)
	ctx := t.Context()

	makeTree := func(kind shamap.Type, key byte, value string, seq uint32) *shamap.SHAMap {
		tree, err := shamap.NewBacked(kind, family)
		require.NoError(t, err)
		tree.SetLedgerSeq(seq)
		require.NoError(t, tree.PutWithNodeType([32]byte{key}, []byte(value+"-payload"), func() shamap.NodeType {
			if kind == shamap.TypeState {
				return shamap.NodeTypeAccountState
			}
			return shamap.NodeTypeTransactionWithMeta
		}()))
		require.NoError(t, tree.StoreDirty(func(entries []shamap.FlushEntry) error { return family.StoreBatch(ctx, entries) }))
		return tree
	}
	makeLedger := func(seq uint32, stateKey, txKey byte, validated bool) *ledger.Ledger {
		state := makeTree(shamap.TypeState, stateKey, "state", seq)
		txs := makeTree(shamap.TypeTransaction, txKey, "transaction", seq)
		stateRoot, err := state.Hash()
		require.NoError(t, err)
		txRoot, err := txs.Hash()
		require.NoError(t, err)
		h := header.LedgerHeader{LedgerIndex: seq, AccountHash: stateRoot, TxHash: txRoot, Validated: validated}
		h.Hash = header.CalculateHash(h)
		result, err := ledger.NewFromHeader(h, state, txs, drops.Fees{})
		require.NoError(t, err)
		return result
	}

	validated := makeLedger(20, 0x20, 0x80, true)
	closed := makeLedger(21, 0x21, 0x81, false)
	candidate := makeLedger(22, 0x22, 0x82, false)
	cachedOnly := makeLedger(23, 0x23, 0x83, false)
	history := make(map[uint32]*ledger.Ledger)
	for seq := uint32(10); seq < 18; seq++ {
		history[seq] = makeLedger(seq, byte(seq), byte(seq+0x60), false)
	}
	svc.validatedLedger = validated
	svc.closedLedger = closed
	svc.validationCandidates = map[uint32]*ledger.Ledger{candidate.Sequence(): candidate}
	svc.ledgerHistory = history
	svc.persistedLedgers = map[[32]byte]*ledger.Ledger{cachedOnly.Hash(): cachedOnly}

	seq, guard, err := svc.PrepareStateRetention(ctx, validated.Sequence(), nil)
	require.NoError(t, err)
	require.Equal(t, validated.Sequence(), seq)
	committed, err := db.RotateGenerationWithRetention(ctx, seq, 11, guard, family.BeginPrune)
	require.NoError(t, err)
	require.True(t, committed)
	next, err := db.DurableFingerprint(ctx)
	require.NoError(t, err)
	for _, l := range []*ledger.Ledger{validated, closed, candidate} {
		require.Truef(t, svc.lookupReplayProof(l.Header(), next, true), "missing retained proof for owned ledger %d", l.Sequence())
	}
	require.False(t, svc.lookupReplayProof(history[10].Header(), next, true), "old history must yield to recent canonical owners in the bounded proof cache")
	require.False(t, svc.lookupReplayProof(cachedOnly.Header(), next, true), "cache-only ledger must not be certified by retention")
}

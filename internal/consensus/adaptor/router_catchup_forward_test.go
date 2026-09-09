package adaptor

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeProvisionalWarmRouter(t *testing.T) (*Router, *recordingSender, *service.Service) {
	t.Helper()
	ctx := context.Background()
	db, err := nodestore.NewKVDatabase(memorydb.New(), nodestore.DatabaseConfig{
		PositiveCache: nodestore.CacheConfig{
			Enabled:    true,
			MaxEntries: 10_000,
			TTL:        time.Hour,
		},
	})
	require.NoError(t, err)
	rm, err := sqlitedb.NewRepositoryManager(ctx, t.TempDir(), sqlitedb.Settings{})
	require.NoError(t, err)

	writer, err := service.New(service.Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, writer.Start())
	_, err = writer.AcceptLedger(ctx)
	require.NoError(t, err)
	writer.FlushPersists()
	writer.Stop()

	svc, err := service.New(service.Config{
		Standalone:    false,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
		FastLoad:      true,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(func() {
		svc.Stop()
		require.NoError(t, rm.Close())
		require.NoError(t, db.Close())
	})
	require.False(t, svc.NeedsInitialSync())
	require.True(t, svc.IsFastLoadProvisional())
	a, sender := newRecordingAdaptor(t, svc)
	return newTestRouter(nil, a, make(chan *peermanagement.InboundMessage, 1)), sender, svc
}

func statusChangeWithParent(
	t *testing.T,
	peerID peermanagement.PeerID,
	seq uint32,
	hash, parentHash [32]byte,
) *peermanagement.InboundMessage {
	t.Helper()
	encoded, err := message.Encode(&message.StatusChange{
		NewEvent:           message.NodeEventClosingLedger,
		LedgerSeq:          seq,
		LedgerHash:         hash[:],
		LedgerHashPrevious: parentHash[:],
	})
	require.NoError(t, err)
	return &peermanagement.InboundMessage{
		PeerID:  peerID,
		Type:    message.TypeStatusChange,
		Payload: encoded,
	}
}

// Issue #1161 keep-up: catch-up WALKS FORWARD one ledger at a time via
// replay-delta against the held parent when behind on the SAME branch, rather
// than jump-adopting the far tip's full state on every hop.

// Same branch, parent known: closed+1 descends from our closed ledger, tip a
// few ahead. The router must issue a replay-delta for closed+1 (parent local),
// NOT a legacy jump-adopt for the far tip.
func TestRouter_ForwardDeltaStep_SameBranch(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	closedHash := svc.GetClosedLedger().Hash()

	var nextHash [32]byte
	nextHash[0] = 0xB1
	r.recordSeqHash(closed+1, nextHash, closedHash, true)

	// A far tip is the recorded catch-up target (the jump-adopt fallback).
	var tipHash [32]byte
	tipHash[0] = 0xF0
	trackCatchupPeer(r, 7, closed+5, tipHash)
	r.recordCatchupTarget(closed+5, tipHash, 7)

	r.armCatchupTowardTarget()

	replays := rs.replayCalls()
	require.Len(t, replays, 1, "same-branch catch-up must issue one forward replay-delta for closed+1")
	assert.Equal(t, nextHash, replays[0].hash)
	assert.Empty(t, rs.legacyCalls(), "must not jump-adopt the far tip when a clean forward step exists")
	assert.True(t, r.replayer.Has(nextHash), "replay-delta acquisition must be in flight for closed+1")
}

func TestRouter_CaughtUpLeavesNextLedgerToConsensus(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	target := [32]byte{0xC1}
	trackCatchupPeer(r, 7, closed+1, target)
	r.recordCatchupTarget(closed+1, target, 7)

	r.armCatchupTowardTarget()

	assert.Empty(t, rs.replayCalls())
	assert.Empty(t, rs.legacyCalls())
	assert.Zero(t, r.catchupInFlight())
}

// Same branch, parent unknown (validation-only): closed+1 has a hash but no
// recorded parent; the same-branch check falls back to our own closed seq's
// recorded hash. When it matches our closed hash the forward step is still taken.
func TestRouter_ForwardDeltaStep_SameBranchViaClosedSeqProxy(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	closedHash := svc.GetClosedLedger().Hash()

	// A trusted validation for our own closed seq confirms we agree with the
	// network there — equivalent to knowing closed+1's parent.
	r.recordSeqHash(closed, closedHash, [32]byte{}, false)
	var nextHash [32]byte
	nextHash[0] = 0xB1
	r.recordSeqHash(closed+1, nextHash, [32]byte{}, false)

	var tipHash [32]byte
	tipHash[0] = 0xF0
	trackCatchupPeer(r, 7, closed+3, tipHash)
	r.recordCatchupTarget(closed+3, tipHash, 7)

	r.armCatchupTowardTarget()

	replays := rs.replayCalls()
	require.Len(t, replays, 1, "closed-seq linkage must still enable the forward step")
	assert.Equal(t, nextHash, replays[0].hash)
	assert.Empty(t, rs.legacyCalls())
}

// Fork: closed+1's recorded parent differs from our closed hash → divergent
// branch → jump-adopt the far validated tip (legacy full-state), not a forward
// delta.
func TestRouter_ForwardDeltaStep_ForkFallsBackToJumpAdopt(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()

	var nextHash, wrongParent [32]byte
	nextHash[0] = 0xB1
	wrongParent[0] = 0xDE // NOT our closed hash
	r.recordSeqHash(closed+1, nextHash, wrongParent, true)

	var tipHash [32]byte
	tipHash[0] = 0xF0
	trackCatchupPeer(r, 7, closed+5, tipHash)
	r.recordCatchupTarget(closed+5, tipHash, 7)

	r.armCatchupTowardTarget()

	assert.Empty(t, rs.replayCalls(), "a forked forward step must not replay-delta")
	legacy := rs.legacyCalls()
	require.Len(t, legacy, 1, "fork must jump-adopt toward the far tip")
	assert.Equal(t, tipHash, legacy[0].hash)
	assert.Equal(t, closed+5, legacy[0].seq)
}

// Cold: closed+1's hash is unknown (no validation/status for it yet) → the
// router can't prove a clean forward step → jump-adopt the far tip.
func TestRouter_ForwardDeltaStep_UnknownNextFallsBackToJumpAdopt(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()

	var tipHash [32]byte
	tipHash[0] = 0xF0
	trackCatchupPeer(r, 7, closed+5, tipHash)
	r.recordCatchupTarget(closed+5, tipHash, 7)
	// No seqHash entry for closed+1.

	r.armCatchupTowardTarget()

	assert.Empty(t, rs.replayCalls())
	legacy := rs.legacyCalls()
	require.Len(t, legacy, 1, "unknown closed+1 must jump-adopt the far tip")
	assert.Equal(t, tipHash, legacy[0].hash)
	assert.Equal(t, closed+5, legacy[0].seq)
}

// Far/cold gap: closed+1 is a known clean child, but the tip is beyond
// maxForwardDeltaGap → a single jump-adopt is preferred over a long serial walk.
func TestRouter_ForwardDeltaStep_FarGapJumpAdopts(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	closedHash := svc.GetClosedLedger().Hash()

	var nextHash [32]byte
	nextHash[0] = 0xB1
	r.recordSeqHash(closed+1, nextHash, closedHash, true)

	var tipHash [32]byte
	tipHash[0] = 0xF0
	tipSeq := closed + maxForwardDeltaGap + 10
	trackCatchupPeer(r, 7, tipSeq, tipHash)
	r.recordCatchupTarget(tipSeq, tipHash, 7)

	r.armCatchupTowardTarget()

	assert.Empty(t, rs.replayCalls(), "beyond the forward bound the router must jump, not walk")
	legacy := rs.legacyCalls()
	require.Len(t, legacy, 1)
	assert.Equal(t, tipSeq, legacy[0].seq)
}

func TestRouter_ProvisionalWarmStartRecentBranchReplaysForward(t *testing.T) {
	r, sender, svc := makeProvisionalWarmRouter(t)
	require.True(t, svc.IsFastLoadProvisional())
	require.Equal(t, consensus.OpModeDisconnected, r.adaptor.GetOperatingMode())
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	var nextHash [32]byte
	nextHash[0] = 0xB1
	r.recordSeqHash(closed.Sequence()+1, nextHash, closed.Hash(), true)
	tipSeq := closed.Sequence() + 2
	trackCatchupPeer(r, 7, tipSeq, [32]byte{0xF0})
	r.recordCatchupTarget(tipSeq, [32]byte{0xF0}, 7)

	r.armCatchupTowardTarget()

	replay := sender.replayCalls()
	require.Len(t, replay, 1)
	assert.Equal(t, nextHash, replay[0].hash)
	assert.Empty(t, sender.legacyCalls())
	assert.Equal(t, closed.Hash(), svc.GetClosedLedger().Hash())
	assert.True(t, svc.IsFastLoadProvisional())
}

func TestRouter_ProvisionalWarmStartFarGapJumpAdopts(t *testing.T) {
	r, sender, svc := makeProvisionalWarmRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	r.recordSeqHash(closed.Sequence()+1, [32]byte{0xB1}, closed.Hash(), true)
	tipSeq := closed.Sequence() + maxForwardDeltaGap + 1
	tipHash := [32]byte{0xF0}
	trackCatchupPeer(r, 7, tipSeq, tipHash)
	r.recordCatchupTarget(tipSeq, tipHash, 7)

	r.armCatchupTowardTarget()

	assert.Empty(t, sender.replayCalls())
	legacy := sender.legacyCalls()
	require.Len(t, legacy, 1)
	assert.Equal(t, tipSeq, legacy[0].seq)
	assert.Equal(t, tipHash, legacy[0].hash)
	assert.Equal(t, closed.Hash(), svc.GetClosedLedger().Hash())
	assert.True(t, svc.IsFastLoadProvisional())
}

func TestRouter_ProvisionalWarmStartAllowsOnlyOneFullStateAcquisition(t *testing.T) {
	r, sender, svc := makeProvisionalWarmRouter(t)
	closed := svc.GetClosedLedgerIndex()
	targetSeq := closed + maxForwardDeltaGap + 1
	trackCatchupPeer(r, 7, targetSeq+1)

	firstHash := [32]byte{0xF1}
	secondHash := [32]byte{0xF2}
	require.True(t, r.startLedgerAcquisition(targetSeq, firstHash, 7))
	assert.False(t, r.startLedgerAcquisition(targetSeq+1, secondHash, 7))

	assert.NotNil(t, r.fetchTracker.Find(firstHash))
	assert.Nil(t, r.fetchTracker.Find(secondHash))
	require.Len(t, sender.legacyCalls(), 1)
	assert.Equal(t, firstHash, sender.legacyCalls()[0].hash)
}

func TestRouter_ProvisionalWarmStartStillAllowsTransactionOnlyReplay(t *testing.T) {
	r, sender, svc := makeProvisionalWarmRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	trackCatchupPeer(r, 7, closed.Sequence()+maxForwardDeltaGap+1)

	fullHash := [32]byte{0xF1}
	require.True(t, r.startLedgerAcquisition(closed.Sequence()+maxForwardDeltaGap+1, fullHash, 7))

	nextHash := [32]byte{0xB1}
	require.True(t, r.startLedgerAcquisition(closed.Sequence()+1, nextHash, 7))

	assert.NotNil(t, r.fetchTracker.Find(fullHash))
	assert.True(t, r.isAcquiring(nextHash))
	require.Len(t, sender.legacyCalls(), 1)
	require.Len(t, sender.replayCalls(), 1)
	assert.Equal(t, nextHash, sender.replayCalls()[0].hash)
}

func TestRouter_ProvisionalWarmStartRecentForkJumpAdopts(t *testing.T) {
	r, sender, svc := makeProvisionalWarmRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	r.recordSeqHash(closed.Sequence()+1, [32]byte{0xB1}, [32]byte{0xD1}, true)
	tipSeq := closed.Sequence() + 3
	tipHash := [32]byte{0xF0}
	trackCatchupPeer(r, 7, tipSeq, tipHash)
	r.recordCatchupTarget(tipSeq, tipHash, 7)

	r.armCatchupTowardTarget()

	assert.Empty(t, sender.replayCalls())
	legacy := sender.legacyCalls()
	require.Len(t, legacy, 1)
	assert.Equal(t, tipSeq, legacy[0].seq)
	assert.Equal(t, tipHash, legacy[0].hash)
	assert.True(t, svc.IsFastLoadProvisional())
}

func TestRouter_ProvisionalWarmStartEventOrderPreservesForwardReplay(t *testing.T) {
	for _, validationFirst := range []bool{true, false} {
		name := "status_before_validation"
		if validationFirst {
			name = "validation_before_status"
		}
		t.Run(name, func(t *testing.T) {
			r, sender, svc := makeProvisionalWarmRouter(t)
			closed := svc.GetClosedLedger()
			require.NotNil(t, closed)
			trusted, err := r.adaptor.GetValidatorKey()
			require.NoError(t, err)

			peerID := peermanagement.PeerID(7)
			nextHash := [32]byte{0xB1}
			tipHash := consensus.LedgerID{0xF0}
			tipSeq := closed.Sequence() + 2
			trackCatchupPeer(r, peerID, tipSeq)

			tipValidation := &consensus.Validation{
				NodeID:    trusted,
				LedgerSeq: tipSeq,
				LedgerID:  tipHash,
			}
			status := statusChangeWithParent(
				t,
				peerID,
				tipSeq,
				[32]byte(tipHash),
				nextHash,
			)

			if validationFirst {
				r.maybeAcquireFromValidation(tipValidation, uint64(peerID))
				assert.Empty(t, sender.replayCalls())
				assert.Empty(t, sender.legacyCalls())
				r.handleMessage(status)
			} else {
				r.handleMessage(status)
				assert.Empty(t, sender.legacyCalls())
				r.maybeAcquireFromValidation(tipValidation, uint64(peerID))
			}

			replay := sender.replayCalls()
			require.Len(t, replay, 1)
			assert.Equal(t, nextHash, replay[0].hash)
			assert.Empty(t, sender.legacyCalls())
			assert.True(t, svc.IsFastLoadProvisional())
		})
	}
}

func TestRouter_TrustedHashReconcilesConflictingPeerParent(t *testing.T) {
	r, _, svc := makeProvisionalWarmRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	seq := closed.Sequence() + 1
	hash := [32]byte{}
	for i := range hash {
		hash[i] = 0xff
	}
	wrongParent := [32]byte{0x01}
	setPeer := func(peerID peermanagement.PeerID, parent [32]byte) {
		r.peersMu.Lock()
		r.peerStates[peerID] = &peerLedgerState{
			LedgerSeq: seq, LedgerHash: hash, parentHash: parent, haveParent: true,
		}
		r.peersMu.Unlock()
		r.adaptor.UpdatePeerLCL(uint64(peerID), consensus.LedgerID(hash))
		r.reconcilePeerSeqHash(seq)
	}

	setPeer(7, wrongParent)
	entry, ok := r.lookupSeqHash(seq)
	require.True(t, ok)
	require.Equal(t, wrongParent, entry.parentHash)
	r.recordValidationSeqHash(seq, hash)
	setPeer(8, closed.Hash())

	entry, ok = r.lookupSeqHash(seq)
	require.True(t, ok)
	assert.Equal(t, seqHashSourceValidation, entry.source)
	assert.True(t, entry.haveParent)
	assert.Equal(t, closed.Hash(), entry.parentHash)
	nextSeq, nextHash, parentLedger, replay, _ := r.recoveryForwardStep(svc, seq, hash, consensusRecovery{})
	assert.True(t, replay)
	require.NotNil(t, parentLedger)
	assert.Equal(t, closed.Hash(), parentLedger.Hash())
	assert.Equal(t, seq, nextSeq)
	assert.Equal(t, hash, nextHash)
}

func TestRouter_ConflictingPeerParentsClearSeededPredecessor(t *testing.T) {
	r, _, svc := makeProvisionalWarmRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	seq := closed.Sequence() + 2
	hash := [32]byte{0xff}
	setPeer := func(peerID peermanagement.PeerID, parent [32]byte) {
		r.peersMu.Lock()
		r.peerStates[peerID] = &peerLedgerState{
			LedgerSeq: seq, LedgerHash: hash, parentHash: parent, haveParent: true,
		}
		r.peersMu.Unlock()
		r.adaptor.UpdatePeerLCL(uint64(peerID), consensus.LedgerID(hash))
		r.reconcilePeerSeqHash(seq)
	}

	setPeer(7, [32]byte{0x01})
	_, seeded := r.lookupSeqHash(seq - 1)
	require.True(t, seeded)
	setPeer(8, [32]byte{0x02})

	entry, ok := r.lookupSeqHash(seq)
	require.True(t, ok)
	assert.False(t, entry.haveParent)
	_, seeded = r.lookupSeqHash(seq - 1)
	assert.False(t, seeded)
}

func TestRouter_ProvisionalWarmStartEventOrderFallsBackWhenLinkageIsIncomplete(t *testing.T) {
	for _, validationFirst := range []bool{true, false} {
		name := "status_before_validation"
		if validationFirst {
			name = "validation_before_status"
		}
		t.Run(name, func(t *testing.T) {
			r, sender, svc := makeProvisionalWarmRouter(t)
			closed := svc.GetClosedLedger()
			require.NotNil(t, closed)
			trusted, err := r.adaptor.GetValidatorKey()
			require.NoError(t, err)

			peerID := peermanagement.PeerID(7)
			tipHash := consensus.LedgerID{0xF0}
			tipSeq := closed.Sequence() + 3
			trackCatchupPeer(r, peerID, tipSeq)
			validation := &consensus.Validation{
				NodeID:    trusted,
				LedgerSeq: tipSeq,
				LedgerID:  tipHash,
			}
			status := statusChangeWithParent(
				t,
				peerID,
				tipSeq,
				[32]byte(tipHash),
				[32]byte{0xB2},
			)

			if validationFirst {
				r.maybeAcquireFromValidation(validation, uint64(peerID))
				assert.Empty(t, sender.replayCalls())
				assert.Empty(t, sender.legacyCalls())
				r.handleMessage(status)
			} else {
				r.handleMessage(status)
				r.maybeAcquireFromValidation(validation, uint64(peerID))
			}

			assert.Empty(t, sender.replayCalls())
			legacy := sender.legacyCalls()
			require.Len(t, legacy, 1)
			assert.Equal(t, tipSeq, legacy[0].seq)
			assert.Equal(t, [32]byte(tipHash), legacy[0].hash)
			assert.True(t, svc.IsFastLoadProvisional())
		})
	}
}

func TestRouter_ProvisionalWarmStartMaintenanceFallsBackAfterGrace(t *testing.T) {
	r, sender, svc := makeProvisionalWarmRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	trusted, err := r.adaptor.GetValidatorKey()
	require.NoError(t, err)

	peerID := peermanagement.PeerID(7)
	tipHash := consensus.LedgerID{0xF0}
	tipSeq := closed.Sequence() + 2
	trackCatchupPeer(r, peerID, tipSeq)
	r.maybeAcquireFromValidation(&consensus.Validation{
		NodeID:    trusted,
		LedgerSeq: tipSeq,
		LedgerID:  tipHash,
	}, uint64(peerID))

	assert.Empty(t, sender.replayCalls())
	assert.Empty(t, sender.legacyCalls())
	r.catchupMu.Lock()
	firstWait := r.linkageWait.since
	r.catchupMu.Unlock()

	latestHash := consensus.LedgerID{0xF1}
	latestSeq := tipSeq + 1
	r.maybeAcquireFromValidation(&consensus.Validation{
		NodeID:    trusted,
		LedgerSeq: latestSeq,
		LedgerID:  latestHash,
	}, uint64(peerID))

	r.catchupMu.Lock()
	assert.Equal(t, firstWait, r.linkageWait.since)
	r.linkageWait.since = time.Now().Add(-catchupLinkageGracePeriod)
	r.catchupMu.Unlock()

	r.maintenanceTick()

	assert.Empty(t, sender.replayCalls())
	legacy := sender.legacyCalls()
	require.Len(t, legacy, 1)
	assert.Equal(t, latestSeq, legacy[0].seq)
	assert.Equal(t, [32]byte(latestHash), legacy[0].hash)
}

func TestAdaptor_FastLoadedLedgerIsReplacedBySameHeightQuorum(t *testing.T) {
	r, _, svc := makeProvisionalWarmRouter(t)
	_, started := r.startLifecycle(t.Context())
	require.True(t, started)
	t.Cleanup(r.stopLifecycle)
	loaded := svc.GetValidatedLedger()
	require.NotNil(t, loaded)

	stateMap, err := loaded.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := loaded.TxMapSnapshot()
	require.NoError(t, err)
	replacementHeader := loaded.Header()
	replacementHeader.Validated = false
	replacementHeader.CloseTime = replacementHeader.CloseTime.Add(time.Second)
	replacementHeader.Hash = header.CalculateHash(replacementHeader)
	replacementHash := replacementHeader.Hash
	initialCandidate, err := svc.BootstrapLedgerWithState(
		context.Background(),
		&replacementHeader,
		stateMap,
		txMap,
	)
	require.NoError(t, err)
	require.True(t, initialCandidate)

	switchDone := make(chan error, 1)
	engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
	engine.switchHook = func(id consensus.LedgerID) {
		selected, err := r.adaptor.GetLedger(id)
		if err == nil {
			err = r.adaptor.OnLedgerSwitched(selected)
		}
		switchDone <- err
	}
	r.engine = engine
	tracker := rcl.NewValidationTracker(1)
	node := consensus.NodeID{1}
	tracker.SetTrustedAndQuorum([]consensus.NodeID{node}, 1)
	now := time.Now()
	require.True(t, tracker.Add(&consensus.Validation{
		LedgerID:  consensus.LedgerID(replacementHash),
		LedgerSeq: replacementHeader.LedgerIndex,
		NodeID:    node,
		SignTime:  now,
		SeenTime:  now,
		Full:      true,
	}))
	r.adaptor.SetValidationHistorian(tracker)
	r.adaptor.OnLedgerFullyValidated(
		consensus.LedgerID(replacementHash),
		replacementHeader.LedgerIndex,
	)

	select {
	case err = <-switchDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("quorum-backed provisional replacement was not handed to consensus")
	}
	require.Equal(t, replacementHash, svc.GetClosedLedger().Hash())
	require.Eventually(t, func() bool {
		validated := svc.GetValidatedLedger()
		return validated != nil && validated.Hash() == replacementHash &&
			!svc.IsFastLoadProvisional() && !svc.NeedsInitialSync()
	}, time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		return r.adaptor.GetOperatingMode() == consensus.OpModeTracking
	}, time.Second, 10*time.Millisecond)
	require.Contains(t, engine.getLedgers(), consensus.LedgerID(replacementHash))
}

func TestRouter_FastLoadedSameHeightQuorumAcquiresUnknownReplacement(t *testing.T) {
	r, sender, svc := makeProvisionalWarmRouter(t)
	loaded := svc.GetValidatedLedger()
	require.NotNil(t, loaded)
	peerID := peermanagement.PeerID(7)
	replacementHash := loaded.Hash()
	replacementHash[0] ^= 0xFF
	trusted, err := r.adaptor.GetValidatorKey()
	require.NoError(t, err)

	r.maybeAcquireFromValidation(&consensus.Validation{
		NodeID:    trusted,
		LedgerSeq: loaded.Sequence(),
		LedgerID:  consensus.LedgerID(replacementHash),
	}, uint64(peerID))

	r.adaptor.OnLedgerFullyValidated(
		consensus.LedgerID(replacementHash),
		loaded.Sequence(),
	)

	require.Eventually(t, func() bool {
		return len(sender.legacyCalls()) == 1
	}, time.Second, 10*time.Millisecond)
	assert.Empty(t, sender.replayCalls())
	legacy := sender.legacyCalls()
	require.Len(t, legacy, 1)
	assert.Equal(t, loaded.Sequence(), legacy[0].seq)
	assert.Equal(t, replacementHash, legacy[0].hash)
	assert.Equal(t, uint64(peerID), legacy[0].peerID)
	assert.Empty(t, r.peerStates)
	assert.True(t, svc.IsFastLoadProvisional())
	assert.Equal(t, loaded.Hash(), svc.GetClosedLedger().Hash())
}

// A completed forward fetch must advance recovery independently of consensus:
// the closed ledger can still be older than the verified replay parent.
func TestRouter_ForwardWalkStoresNextForConsensus(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	c := parent.Sequence()

	// A real forward child at c+1 anchored on our closed ledger, completable on
	// GotBase + state nodes alone.
	rootHash, rootData, wire := buildSelfHealSourceState(t)
	hdr := header.LedgerHeader{
		LedgerIndex: c + 1,
		ParentHash:  parent.Hash(),
		AccountHash: rootHash,
	}
	data := header.AddRaw(hdr, false)
	childHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), data)

	il := inbound.New(childHash, c+1, 7, serveTestLogger())
	require.NoError(t, il.GotBase([]message.LedgerNode{{NodeData: data}, {NodeData: rootData}}))
	require.NoError(t, il.GotStateNodes(wire))
	il.CollectMissingRequest(false)
	require.True(t, il.IsComplete())
	r.fetchTracker.Track(il)

	// Record the next forward child (c+2) anchored on the child we're about to
	// adopt, plus a far tip so the walk still has somewhere to go.
	var next2 [32]byte
	next2[0] = 0xB2
	r.recordSeqHash(c+2, next2, childHash, true)
	var tipHash [32]byte
	tipHash[0] = 0xF0
	trackCatchupPeer(r, 7, c+10, tipHash)
	r.recordCatchupTarget(c+10, tipHash, 7)

	r.completeInboundLedger(il)

	require.Equal(t, c, svc.GetClosedLedgerIndex())
	stored, err := svc.GetLedgerByHash(childHash)
	require.NoError(t, err)
	require.Equal(t, c+1, stored.Sequence())
	replays := rs.replayCalls()
	require.Len(t, replays, 1, "continue past the stored child instead of deduplicating it forever")
	assert.Equal(t, next2, replays[0].hash)
	assert.Empty(t, rs.legacyCalls())
	r.armCatchupTowardTarget()
	assert.Len(t, rs.replayCalls(), 1, "the next step stays deduplicated")
}

// maxConcurrentCatchup is preserved: while a forward step is in flight, a second
// arm is suppressed (serial forward walk).
func TestRouter_ForwardWalk_SerialUnderCap(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	closedHash := svc.GetClosedLedger().Hash()

	var nextHash [32]byte
	nextHash[0] = 0xB1
	r.recordSeqHash(closed+1, nextHash, closedHash, true)
	trackCatchupPeer(r, 7, closed+5, [32]byte{0xF0})
	r.recordCatchupTarget(closed+5, [32]byte{0xF0}, 7)

	r.armCatchupTowardTarget()
	require.Equal(t, 1, r.catchupInFlight(), "one forward step in flight")

	// A second arm while the first is in flight must not add another.
	r.armCatchupTowardTarget()
	assert.Equal(t, 1, r.catchupInFlight(),
		"the same forward target remains deduplicated")
	assert.Len(t, rs.replayCalls(), 1, "no second acquisition while one is in flight")
}

func TestRouter_ForwardReplayUsesValidatedBaseAfterObserverFork(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	validated := svc.GetClosedLedger()
	svc.SetValidatedLedgerAt(validated.Sequence(), validated.Hash(), time.Now())
	require.Equal(t, validated.Hash(), svc.GetValidatedLedger().Hash())
	_, err := svc.AcceptConsensusResult(context.Background(), validated, nil, nil, time.Now(), true)
	require.NoError(t, err)
	local := svc.GetClosedLedger()
	require.Greater(t, local.Sequence(), validated.Sequence())

	nextHash := [32]byte{0xB1}
	tipHash := [32]byte{0xF0}
	r.recordSeqHash(validated.Sequence()+1, nextHash, validated.Hash(), true)
	trackCatchupPeer(r, 7, validated.Sequence()+5, tipHash)
	r.recordCatchupTarget(validated.Sequence()+5, tipHash, 7)
	r.armCatchupTowardTarget()

	replays := rs.replayCalls()
	require.Len(t, replays, 1)
	require.Equal(t, nextHash, replays[0].hash)
	require.Empty(t, rs.legacyCalls(), "a speculative close must not force full-state acquisition")
	require.Equal(t, local.Hash(), svc.GetClosedLedger().Hash(), "acquisition must not mutate consensus")
}

func TestRouter_ForwardDeltaFailureCooldownUsesChildHash(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedgerIndex()
	closedHash := svc.GetClosedLedger().Hash()
	nextHash := [32]byte{0xB1}
	tipHash := [32]byte{0xF0}
	r.recordSeqHash(closed+1, nextHash, closedHash, true)
	trackCatchupPeer(r, 7, closed+5, tipHash)
	r.recordCatchupTarget(closed+5, tipHash, 7)

	il := inbound.New(nextHash, closed+1, 7, serveTestLogger())
	r.fetchTracker.Track(il)
	r.failInboundAcquisition(il)
	r.armCatchupTowardTarget()

	assert.Empty(t, rs.replayCalls())
	assert.Empty(t, rs.legacyCalls())
	assert.True(t, r.catchupRetryBlocked(nextHash, time.Now()))
	assert.False(t, r.catchupRetryBlocked(tipHash, time.Now()))
}

// The seqHash table is bounded: once it exceeds the trailing seqHashRetain
// window, entries older than (max-seqHashRetain) are pruned on insert so a
// long-running node never grows it unbounded.
func TestRouter_SeqHashTableBounded(t *testing.T) {
	r, _, _, _ := makeRouter(t)

	var h [32]byte
	h[0] = 0x01
	top := uint32(seqHashRetain + 10)
	for seq := uint32(1); seq <= top; seq++ {
		r.recordSeqHash(seq, h, [32]byte{}, false)
	}

	_, lowKept := r.lookupSeqHash(1)
	assert.False(t, lowKept, "an entry older than the retention window must be pruned")
	_, highKept := r.lookupSeqHash(top)
	assert.True(t, highKept, "the newest entry must be retained")

	r.seqHashMu.Lock()
	size := len(r.seqHash)
	r.seqHashMu.Unlock()
	assert.LessOrEqual(t, size, seqHashRetain+1, "the table stays within the retention window")
}

func TestRouter_OutlierPeerStatusCannotPoisonSeqHashRetention(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	r.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	r.recordSeqHash(closed.Sequence(), closed.Hash(), [32]byte{}, false)

	normalHash := [32]byte{0xff}
	r.handleStatusChange(statusChangeMessageWithParent(
		t,
		7,
		closed.Sequence()+1,
		normalHash,
		closed.Hash(),
		true,
	))

	outlierHash := [32]byte{0x01}
	const outlierBase = uint32(106_000_000)
	for offset := uint32(0); offset < seqHashRetain+10; offset++ {
		r.handleStatusChange(statusChangeMessageWithParent(
			t,
			9,
			outlierBase+offset,
			outlierHash,
			[32]byte{0x02},
			true,
		))
	}

	link, ok := r.lookupSeqHash(closed.Sequence() + 1)
	require.True(t, ok)
	assert.Equal(t, normalHash, link.hash)
	assert.Equal(t, closed.Hash(), link.parentHash)
	assert.True(t, link.haveParent)
	_, outlierRecorded := r.lookupSeqHash(outlierBase + seqHashRetain + 9)
	assert.False(t, outlierRecorded)

	r.seqHashMu.Lock()
	anchor := r.seqHashAnchor
	size := len(r.seqHash)
	r.seqHashMu.Unlock()
	assert.Equal(t, closed.Sequence(), anchor)
	assert.LessOrEqual(t, size, seqHashRetain+maxForwardDeltaGap+1)

	r.peersMu.RLock()
	_, outlierAdmitted := r.peerStates[9]
	candidate, outlierStaged := r.peerStatusCandidates[9]
	r.peersMu.RUnlock()
	assert.False(t, outlierAdmitted)
	require.True(t, outlierStaged)
	assert.Equal(t, outlierBase+seqHashRetain+9, candidate.LedgerSeq)
	assert.Equal(t, outlierHash, candidate.LedgerHash)
}

func TestRouter_PeerSeqHashCannotOverwriteTrustedEvidence(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	seq := svc.GetClosedLedgerIndex() + 1
	trustedHash := [32]byte{0xa1}
	peerHash := [32]byte{0xb2}
	parentHash := svc.GetClosedLedger().Hash()

	r.recordValidationSeqHash(seq, trustedHash)
	r.recordPeerSeqHash(seq, peerHash, [32]byte{0xc3}, true)
	r.recordPeerSeqHash(seq, trustedHash, parentHash, true)

	entry, ok := r.lookupSeqHash(seq)
	require.True(t, ok)
	assert.Equal(t, trustedHash, entry.hash)
	assert.Equal(t, parentHash, entry.parentHash)
	assert.True(t, entry.haveParent)
	assert.Equal(t, seqHashSourceValidation, entry.source)
	assert.Equal(t, seqHashSourcePeer, entry.parentFrom)
}

func TestRouter_PeerStatusAdmissionUsesDivergenceWindow(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	const anchor = uint32(10_000)
	r.recordValidationSeqHash(anchor, [32]byte{0xa4})

	assert.True(t, r.peerStatusWithinAnchor(anchor-maxForwardDeltaGap))
	assert.True(t, r.peerStatusWithinAnchor(anchor+maxForwardDeltaGap))
	assert.False(t, r.peerStatusWithinAnchor(anchor-maxForwardDeltaGap-1))
	assert.False(t, r.peerStatusWithinAnchor(anchor+maxForwardDeltaGap+1))
}

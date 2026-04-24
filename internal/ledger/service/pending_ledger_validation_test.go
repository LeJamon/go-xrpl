package service

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetValidatedLedger_StashesWhenSeqMissing_FiresOnAdopt pins F4:
// when SetValidatedLedger fires for a seq whose ledger has not yet been
// inserted into ledgerHistory (validation tracker leads the peer-adopt
// loop), the (seq, expectedHash) pair must be stashed and later promoted
// when the adopt path installs that ledger with a matching hash.
//
// Without this, a trusted validation for seq N that races ahead of the
// peer-adoption of N is silently dropped and validatedLedger never
// advances — the observable symptom is server_info.validated_ledger
// lagging behind closed_ledger indefinitely despite quorum being met.
func TestSetValidatedLedger_StashesWhenSeqMissing_FiresOnAdopt(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	// Build a header+state that we intend to adopt shortly.
	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	var adoptedHash [32]byte
	adoptedHash[0] = 0xA1
	adoptedSeq := svc.GetClosedLedgerIndex() + 1
	hdr := &header.LedgerHeader{
		LedgerIndex: adoptedSeq,
		Hash:        adoptedHash,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}

	// Validation races ahead of adopt: call SetValidatedLedger *before*
	// the seq exists in ledgerHistory. Must NOT promote yet — just stash.
	startValidated := svc.GetValidatedLedgerIndex()
	svc.SetValidatedLedger(adoptedSeq, adoptedHash)

	assert.Equal(t, startValidated, svc.GetValidatedLedgerIndex(),
		"SetValidatedLedger must not advance validatedLedger when seq is not yet in history")

	// The stash must hold this seq pending a future adopt.
	svc.mu.RLock()
	pending, stashed := svc.pendingLedgerValidations[adoptedSeq]
	svc.mu.RUnlock()
	require.True(t, stashed,
		"SetValidatedLedger must stash (seq, expectedHash) when seq is not yet in history")
	assert.Equal(t, adoptedHash, pending.expectedHash)

	// When the adopt path installs this ledger, the stashed validation
	// must be drained and validatedLedger promoted to the adopted ledger.
	require.NoError(t, svc.AdoptLedgerWithState(hdr, stateMap, txMap))

	assert.Equal(t, adoptedSeq, svc.GetValidatedLedgerIndex(),
		"AdoptLedgerWithState must drain the stashed validation and promote validatedLedger")

	// Drain must remove the entry.
	svc.mu.RLock()
	_, stillStashed := svc.pendingLedgerValidations[adoptedSeq]
	svc.mu.RUnlock()
	assert.False(t, stillStashed,
		"adopt-drain must remove the stashed entry")
}

// TestSetValidatedLedger_StashExpires pins F4: a stashed validation older
// than pendingValidationTTL must be discarded on drain and must NOT
// promote the adopted ledger to validated. An expired stash is a staleness
// signal — by the time we adopt, the quorum gossip that produced the
// original validation is too old to trust without a fresh re-confirmation.
func TestSetValidatedLedger_StashExpires(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	var adoptedHash [32]byte
	adoptedHash[0] = 0xB2
	adoptedSeq := svc.GetClosedLedgerIndex() + 1
	hdr := &header.LedgerHeader{
		LedgerIndex: adoptedSeq,
		Hash:        adoptedHash,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}

	startValidated := svc.GetValidatedLedgerIndex()

	// Stash a validation for this seq, then backdate `at` past the TTL.
	svc.SetValidatedLedger(adoptedSeq, adoptedHash)

	svc.mu.Lock()
	entry, ok := svc.pendingLedgerValidations[adoptedSeq]
	require.True(t, ok, "setup: stash must exist before backdate")
	entry.at = time.Now().Add(-2 * pendingValidationTTL)
	svc.pendingLedgerValidations[adoptedSeq] = entry
	svc.mu.Unlock()

	// Adopt — the drain must see the stale entry and refuse to promote.
	require.NoError(t, svc.AdoptLedgerWithState(hdr, stateMap, txMap))

	assert.Equal(t, startValidated, svc.GetValidatedLedgerIndex(),
		"expired stash must NOT promote validatedLedger on adopt")

	// The stale entry must be cleaned up regardless of promotion outcome.
	svc.mu.RLock()
	_, stillStashed := svc.pendingLedgerValidations[adoptedSeq]
	svc.mu.RUnlock()
	assert.False(t, stillStashed,
		"drain must delete expired entries even when refusing to promote")
}

// TestSetValidatedLedger_StashHashMismatch pins F4: a stashed validation
// for hash A must NOT promote an adopted ledger whose hash is B. This is
// the fork-safety guard — if peers validated a different hash at that seq
// than the one we adopted locally, our adopted ledger is on the wrong
// fork and silently promoting it to validated would be a correctness bug.
// Mirrors the inline-hash-match guard SetValidatedLedger already applies
// when the seq IS present in history.
func TestSetValidatedLedger_StashHashMismatch(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	// Peer-adoption path will install hash B.
	var adoptedHashB [32]byte
	adoptedHashB[0] = 0xC3

	// But trusted validation arrived earlier referencing hash A.
	var validatedHashA [32]byte
	validatedHashA[0] = 0xC4

	adoptedSeq := svc.GetClosedLedgerIndex() + 1
	hdr := &header.LedgerHeader{
		LedgerIndex: adoptedSeq,
		Hash:        adoptedHashB,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}

	startValidated := svc.GetValidatedLedgerIndex()

	// Stash validation for A.
	svc.SetValidatedLedger(adoptedSeq, validatedHashA)

	svc.mu.RLock()
	entry, stashed := svc.pendingLedgerValidations[adoptedSeq]
	svc.mu.RUnlock()
	require.True(t, stashed, "setup: stash must exist before adopt")
	require.Equal(t, validatedHashA, entry.expectedHash)

	// Adopt B. Must NOT promote — fork signal.
	require.NoError(t, svc.AdoptLedgerWithState(hdr, stateMap, txMap))

	assert.Equal(t, startValidated, svc.GetValidatedLedgerIndex(),
		"hash mismatch between stashed validation and adopted ledger must NOT promote")

	// The stale-hash entry must be cleaned up so it can't accidentally
	// match a later adopt at the same seq.
	svc.mu.RLock()
	_, stillStashed := svc.pendingLedgerValidations[adoptedSeq]
	svc.mu.RUnlock()
	assert.False(t, stillStashed,
		"drain must delete hash-mismatched entries on the seq-adopt side")
}

// TestAdoptLedgerWithState_EventCallbackFiresAfterValidationFirstRace pins
// the validation-first race fix: when SetValidatedLedger arrives BEFORE the
// adopt path installs the ledger, the subsequent adopt's F4 drain promotes
// validatedLedger in-line — but nothing will ever call SetValidatedLedger
// again for that hash, so the hash-keyed LedgerAcceptedEvent stash would
// never drain. The legacy eventCallback (wired to the WebSocket
// ledgerClosed + transaction streams) must therefore fire inline when F4
// drain returns true, and the hash-keyed stash must NOT be populated in
// that case (no one will drain it). Skipping the stash also prevents a
// double-fire hazard if a late duplicate SetValidatedLedger arrives.
func TestAdoptLedgerWithState_EventCallbackFiresAfterValidationFirstRace(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	var (
		mu            sync.Mutex
		callbackCount int
		lastEvent     *LedgerAcceptedEvent
	)
	done := make(chan struct{}, 1)

	svc.SetEventCallback(func(event *LedgerAcceptedEvent) {
		mu.Lock()
		callbackCount++
		lastEvent = event
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	})

	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	blob1, id1 := makeTxMetaBlobForTest(t, []byte("race-tx-blob-A-padding-padpad"), 0)
	require.NoError(t, txMap.PutWithNodeType(id1, blob1, shamap.NodeTypeTransactionWithMeta))
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	var adoptedHash [32]byte
	adoptedHash[0] = 0xE1
	adoptedSeq := svc.GetClosedLedgerIndex() + 1
	hdr := &header.LedgerHeader{
		LedgerIndex: adoptedSeq,
		Hash:        adoptedHash,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}

	// Validation-first race: trusted-validation quorum gossip reaches us
	// before the peer-adopt loop installs seq N. This stashes in
	// pendingLedgerValidations keyed by seq.
	svc.SetValidatedLedger(adoptedSeq, adoptedHash)

	// Sanity: the seq-keyed stash must be populated and validatedLedger
	// must NOT have advanced yet (the seq isn't in history).
	svc.mu.RLock()
	_, seqStashed := svc.pendingLedgerValidations[adoptedSeq]
	svc.mu.RUnlock()
	require.True(t, seqStashed,
		"setup: SetValidatedLedger must stash (seq, hash) when seq not in history")

	// Now adopt. F4 drain inside adoptLedgerWithStateLocked sees the
	// matching-hash, non-expired stash and promotes validatedLedger
	// to the adopted ledger. Because no SetValidatedLedger will arrive
	// later for this hash, the legacy eventCallback MUST fire inline.
	require.NoError(t, svc.AdoptLedgerWithState(hdr, stateMap, txMap))

	assert.Equal(t, adoptedSeq, svc.GetValidatedLedgerIndex(),
		"F4 drain must promote validatedLedger on matching adopt")

	// Wait for the inline-dispatched eventCallback.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("eventCallback must fire inline when F4 drain promotes the adopted ledger — " +
			"no later SetValidatedLedger will arrive to drain the hash-keyed stash")
	}

	mu.Lock()
	assert.Equal(t, 1, callbackCount,
		"eventCallback must fire exactly once on the validation-first race path")
	require.NotNil(t, lastEvent)
	require.NotNil(t, lastEvent.LedgerInfo)
	assert.Equal(t, adoptedSeq, lastEvent.LedgerInfo.Sequence,
		"fired event must carry the adopted ledger's seq")
	assert.Equal(t, adoptedHash, lastEvent.LedgerInfo.Hash,
		"fired event must carry the adopted ledger's hash")
	assert.Len(t, lastEvent.TransactionResults, 1,
		"fired event must carry the adopted tx results")
	mu.Unlock()

	// The hash-keyed stash must NOT have been populated — firing inline
	// supersedes stashing. Leaving a stash here would cause a double-fire
	// if a late duplicate SetValidatedLedger arrived for the same hash.
	svc.mu.RLock()
	_, hashStashed := svc.pendingValidation[adoptedHash]
	svc.mu.RUnlock()
	assert.False(t, hashStashed,
		"pendingValidation[hash] must NOT be populated when F4 drain fires inline — "+
			"the event has already been consumed")

	// Defense-in-depth: a second SetValidatedLedger call for the same
	// (seq, hash) must be a no-op (seq is in history and already validated)
	// and MUST NOT fire eventCallback again.
	svc.SetValidatedLedger(adoptedSeq, adoptedHash)

	select {
	case <-done:
		t.Fatal("eventCallback must not fire twice — no late-duplicate stash should remain")
	case <-time.After(100 * time.Millisecond):
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, callbackCount,
		"late-duplicate SetValidatedLedger must not cause a second eventCallback dispatch")
}

// TestAcceptConsensusResult_EventCallbackFiresAfterValidationFirstRace pins
// the same fix for the consensus-close path. When a trusted-validation
// gossip for seq N arrives before the local consensus round closes seq N,
// AcceptConsensusResult's F4 drain promotes validatedLedger in-line — and
// likewise no later SetValidatedLedger will arrive to drain the hash-keyed
// stash. The eventCallback must fire inline.
func TestAcceptConsensusResult_EventCallbackFiresAfterValidationFirstRace(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	var (
		mu            sync.Mutex
		callbackCount int
		lastEvent     *LedgerAcceptedEvent
	)
	done := make(chan struct{}, 1)

	svc.SetEventCallback(func(event *LedgerAcceptedEvent) {
		mu.Lock()
		callbackCount++
		lastEvent = event
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	})

	// Predict the hash that AcceptConsensusResult will compute so we can
	// stash a matching validation. The openLedger already chains off the
	// current closedLedger via Start() — closing it with (closeTime, 0)
	// and no txs is deterministic, so we can snapshot+close a clone to
	// derive the exact hash. Simpler alternative: stash the *seq* with a
	// wrong hash first to verify validation-first no-ops, then do the
	// real-race test below. But the cleanest form is to perform the close
	// twice: once discarded to capture the hash, then re-close for real.
	//
	// Even simpler: do the real close first, observe the produced hash,
	// then drive AcceptConsensusResult on a fresh service where we can
	// pre-stash that hash. That keeps the test free of internal cloning.
	probeSvc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, probeSvc.Start())

	parent := probeSvc.GetClosedLedger()
	require.NotNil(t, parent)
	expectedSeq := parent.Sequence() + 1
	closeTime := time.Unix(1700000000, 0)
	_, err = probeSvc.AcceptConsensusResult(parent, nil, closeTime)
	require.NoError(t, err)

	// Capture the hash that the deterministic close produced.
	probeSvc.mu.RLock()
	expectedHash := probeSvc.closedLedger.Hash()
	probeSvc.mu.RUnlock()
	require.NotEqual(t, [32]byte{}, expectedHash)

	// Back to the real svc: stash the validation BEFORE AcceptConsensusResult.
	parentReal := svc.GetClosedLedger()
	require.NotNil(t, parentReal)
	require.Equal(t, parent.Sequence(), parentReal.Sequence(),
		"probe and real service must start from the same closedLedger seq")

	svc.SetValidatedLedger(expectedSeq, expectedHash)

	svc.mu.RLock()
	_, seqStashed := svc.pendingLedgerValidations[expectedSeq]
	svc.mu.RUnlock()
	require.True(t, seqStashed,
		"setup: SetValidatedLedger must stash (seq, hash) when seq not in history")

	// Close the consensus ledger. F4 drain sees the matching-hash,
	// non-expired stash and promotes validatedLedger. eventCallback MUST
	// fire inline because no later SetValidatedLedger will arrive.
	closedSeq, err := svc.AcceptConsensusResult(parentReal, nil, closeTime)
	require.NoError(t, err)
	require.Equal(t, expectedSeq, closedSeq)

	assert.Equal(t, expectedSeq, svc.GetValidatedLedgerIndex(),
		"F4 drain must promote validatedLedger on matching consensus close")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("eventCallback must fire inline when F4 drain promotes the closed ledger")
	}

	mu.Lock()
	assert.Equal(t, 1, callbackCount,
		"eventCallback must fire exactly once on the consensus-close validation-first race path")
	require.NotNil(t, lastEvent)
	require.NotNil(t, lastEvent.LedgerInfo)
	assert.Equal(t, expectedSeq, lastEvent.LedgerInfo.Sequence)
	assert.Equal(t, expectedHash, lastEvent.LedgerInfo.Hash)
	mu.Unlock()

	svc.mu.RLock()
	_, hashStashed := svc.pendingValidation[expectedHash]
	svc.mu.RUnlock()
	assert.False(t, hashStashed,
		"pendingValidation[hash] must NOT be populated when F4 drain fires inline")
}

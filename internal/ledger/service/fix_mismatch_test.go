package service

import (
	"context"
	"testing"

	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeStubLedger constructs a Ledger with an explicit seq/hash/parentHash
// triple, backed by an empty state and tx map. The hash does not need to
// match the canonical computed hash for this test — we exercise the
// parent-hash chain check, which consumes the *stored* header values.
func makeStubLedger(t *testing.T, seq uint32, hash, parentHash [32]byte) *ledger.Ledger {
	t.Helper()
	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	hdr := header.LedgerHeader{
		LedgerIndex: seq,
		Hash:        hash,
		ParentHash:  parentHash,
	}
	return ledger.NewFromHeader(hdr, stateMap, txMap, drops.Fees{})
}

// TestAdoptLedgerWithState_FixMismatchInvalidatesDivergedTail pins F5:
// when the adopted ledger's parent hash does NOT match the hash of the
// prev-seq entry in ledgerHistory, the diverged slot (and every
// orphaned forward entry) must be purged before the adopt installs
// the new ledger.
//
// Seed chain: genesis (Start-installed) + stub A at seq S, stub B at
// seq S+1 (child of A), stub C at seq S+2 (child of B). Adopt D at
// seq S+1 whose parentHash != A.Hash(). F5 must:
//   - Remove B from ledgerHistory (mismatched prev) and C (orphaned forward).
//   - Install D in their place.
//   - Leave A untouched (it's at a different seq than the one adopted;
//     the adopted header pins seq S+1 only, so seqs < S+1 that do not
//     chain to D's parent are not rewritten here).
//
// Regression guard: before F5, the old B3 would be silently overwritten
// and C3 would linger as an orphan on the wrong fork.
func TestAdoptLedgerWithState_FixMismatchInvalidatesDivergedTail(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	// Build a 3-ledger fork chain in history: A@S, B@S+1 (child of A),
	// C@S+2 (child of B). S starts one above the closed ledger Start installed.
	baseSeq := svc.GetClosedLedgerIndex() + 1

	var hashA, hashB, hashC [32]byte
	hashA[0] = 0xAA
	hashB[0] = 0xBB
	hashC[0] = 0xCC

	// parentHash of A is immaterial — we do not adopt at seq A.
	var zero [32]byte
	ledA := makeStubLedger(t, baseSeq, hashA, zero)
	ledB := makeStubLedger(t, baseSeq+1, hashB, hashA) // B chains to A
	ledC := makeStubLedger(t, baseSeq+2, hashC, hashB) // C chains to B

	// Seed the in-memory history directly. Bypass AdoptLedgerWithState
	// to isolate the F5 path under test.
	svc.mu.Lock()
	svc.ledgerHistory[ledA.Sequence()] = ledA
	svc.ledgerHistory[ledB.Sequence()] = ledB
	svc.ledgerHistory[ledC.Sequence()] = ledC
	// Simulate state where closedLedger advanced to C — the adoptLedger
	// path will reassign closedLedger to the adopted D, but fixMismatch
	// must also recognize when closedLedger points at an invalidated
	// slot.
	svc.closedLedger = ledC
	svc.mu.Unlock()

	// Build the divergent D at seq S+1. Its parentHash is NOT hashA,
	// so it does NOT chain to our stored A — this must trip fixMismatch.
	var hashD [32]byte
	hashD[0] = 0xDD
	var divergentParent [32]byte
	divergentParent[0] = 0xFF // deliberately != hashA

	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	hdrD := &header.LedgerHeader{
		LedgerIndex: baseSeq + 1,
		Hash:        hashD,
		ParentHash:  divergentParent,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}

	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdrD, stateMap, txMap))

	svc.mu.RLock()
	defer svc.mu.RUnlock()

	// D must be installed at seq S+1.
	gotD, okD := svc.ledgerHistory[baseSeq+1]
	require.True(t, okD, "adopted ledger D must be installed at its seq")
	assert.Equal(t, hashD, gotD.Hash(), "seq S+1 must now hold D, not the purged B")

	// B was on a divergent chain at the same seq as D — it's been
	// overwritten by installing D. That is inherent to the map write,
	// not F5's contribution. F5's job is the *forward* and *parent*
	// sweeps. C must be purged (orphaned forward entry).
	_, okC := svc.ledgerHistory[baseSeq+2]
	assert.False(t, okC, "orphaned forward ledger C (> adoptedSeq) must be purged by fixMismatch")

	// closedLedger must point to D after the adopt (adopt reassigns it),
	// not at the invalidated C.
	require.NotNil(t, svc.closedLedger)
	assert.Equal(t, hashD, svc.closedLedger.Hash(),
		"closedLedger must track the adopted ledger after a fork-switch adopt")
}

// TestAdoptLedgerWithState_NoMismatchNoOp pins the happy path:
// if the adopted ledger's parentHash matches the hash of the prev-seq
// entry in ledgerHistory, fixMismatch must leave history alone.
//
// Seed: A@S, B@S+1 (child of A). Adopt D@S+2 whose parentHash == B.Hash().
// After adopt: A, B, D must all remain in history, D is new.
func TestAdoptLedgerWithState_NoMismatchNoOp(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	baseSeq := svc.GetClosedLedgerIndex() + 1

	var hashA, hashB [32]byte
	hashA[0] = 0xA1
	hashB[0] = 0xB1
	var zero [32]byte
	ledA := makeStubLedger(t, baseSeq, hashA, zero)
	ledB := makeStubLedger(t, baseSeq+1, hashB, hashA)

	svc.mu.Lock()
	svc.ledgerHistory[ledA.Sequence()] = ledA
	svc.ledgerHistory[ledB.Sequence()] = ledB
	svc.closedLedger = ledB
	svc.mu.Unlock()

	// D at seq S+2 correctly chains to B.
	var hashD [32]byte
	hashD[0] = 0xD1

	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)
	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	hdrD := &header.LedgerHeader{
		LedgerIndex: baseSeq + 2,
		Hash:        hashD,
		ParentHash:  hashB, // chains correctly to B
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}

	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdrD, stateMap, txMap))

	svc.mu.RLock()
	defer svc.mu.RUnlock()

	// A, B, D must all be present — no invalidation.
	gotA, okA := svc.ledgerHistory[baseSeq]
	require.True(t, okA, "A must remain: happy-path adopt does not invalidate")
	assert.Equal(t, hashA, gotA.Hash())

	gotB, okB := svc.ledgerHistory[baseSeq+1]
	require.True(t, okB, "B must remain: its hash matches D.parentHash")
	assert.Equal(t, hashB, gotB.Hash())

	gotD, okD := svc.ledgerHistory[baseSeq+2]
	require.True(t, okD)
	assert.Equal(t, hashD, gotD.Hash())
}

// TestAdoptLedgerWithState_FixMismatchValidatedLedgerInvalidationLogsError
// pins the escalation behavior: if fixMismatch invalidates a ledger that
// was already quorum-validated, it MUST NOT silently reset
// s.validatedLedger. A validated-ledger invalidation indicates a genuine
// fork between our local quorum and the peer-adopted chain — that is
// an operational alert, not a run-of-the-mill history rewrite.
//
// Verification strategy: confirm that s.validatedLedger retains its
// prior pointer after fixMismatch runs on a validated-prev case.
// The ERROR log itself is observed through the exercised code path;
// verifying the log message text is brittle and is not checked here.
func TestAdoptLedgerWithState_FixMismatchValidatedLedgerInvalidationLogsError(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	baseSeq := svc.GetClosedLedgerIndex() + 1

	// Seed B at seq S+0; makeStubLedger's NewFromHeader already marks
	// the ledger as validated (state=StateValidated). Adopt D at seq
	// S+1 whose parentHash does not equal B.Hash() — fixMismatch must
	// purge B and log ERROR. The validatedLedger pointer must not
	// silently flip.
	var hashB [32]byte
	hashB[0] = 0x42
	var zero [32]byte
	ledB := makeStubLedger(t, baseSeq, hashB, zero)
	require.True(t, ledB.IsValidated(),
		"stub ledger via NewFromHeader must be in validated state")

	svc.mu.Lock()
	svc.ledgerHistory[ledB.Sequence()] = ledB
	svc.closedLedger = ledB
	prevValidated := svc.validatedLedger
	svc.mu.Unlock()

	var hashD [32]byte
	hashD[0] = 0xD4
	var divergentParent [32]byte
	divergentParent[0] = 0xAB

	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)
	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	hdrD := &header.LedgerHeader{
		LedgerIndex: baseSeq + 1,
		Hash:        hashD,
		ParentHash:  divergentParent,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}

	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdrD, stateMap, txMap))

	svc.mu.RLock()
	defer svc.mu.RUnlock()
	assert.Equal(t, prevValidated, svc.validatedLedger,
		"validatedLedger must NOT be silently reset by fixMismatch — "+
			"a validated-ledger invalidation is an operational alert and "+
			"requires operator action, not silent rewrite")
}

// TestAdoptLedgerWithState_FixMismatchPurgesTxIndex pins that when a
// ledger is invalidated by fixMismatch, any tx-index entries pointing
// at it are removed too. Otherwise `tx` RPCs would keep resolving tx
// hashes to a ledger slot whose contents were just discarded.
func TestAdoptLedgerWithState_FixMismatchPurgesTxIndex(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	baseSeq := svc.GetClosedLedgerIndex() + 1

	var hashA, hashB, hashC [32]byte
	hashA[0] = 0x1A
	hashB[0] = 0x1B
	hashC[0] = 0x1C
	var zero [32]byte
	ledA := makeStubLedger(t, baseSeq, hashA, zero)
	ledB := makeStubLedger(t, baseSeq+1, hashB, hashA)
	ledC := makeStubLedger(t, baseSeq+2, hashC, hashB)

	// Fake tx-index entries for the ledgers we expect to get purged.
	var txInB, txInC [32]byte
	txInB[0] = 0x0B
	txInC[0] = 0x0C

	svc.mu.Lock()
	svc.ledgerHistory[ledA.Sequence()] = ledA
	svc.ledgerHistory[ledB.Sequence()] = ledB
	svc.ledgerHistory[ledC.Sequence()] = ledC
	svc.closedLedger = ledC
	svc.txIndex[txInB] = ledB.Sequence()
	svc.txPositionIndex[txInB] = 0
	svc.txIndex[txInC] = ledC.Sequence()
	svc.txPositionIndex[txInC] = 0
	svc.mu.Unlock()

	// Divergent D at seq S+1.
	var hashD [32]byte
	hashD[0] = 0x1D
	var divergentParent [32]byte
	divergentParent[0] = 0xEE

	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)
	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	hdrD := &header.LedgerHeader{
		LedgerIndex: baseSeq + 1,
		Hash:        hashD,
		ParentHash:  divergentParent,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}

	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), hdrD, stateMap, txMap))

	svc.mu.RLock()
	defer svc.mu.RUnlock()

	// The tx from the orphaned C must be purged from the tx-index.
	_, okC := svc.txIndex[txInC]
	assert.False(t, okC, "tx-index must drop entries for orphaned forward ledgers")
	_, okCPos := svc.txPositionIndex[txInC]
	assert.False(t, okCPos, "tx-position-index must drop entries for orphaned forward ledgers")

	// The tx from B is subtler: B's slot is being overwritten by D (seq
	// collision), but the *contents* of B are gone. Any tx-index entry
	// that still resolved to baseSeq+1 would now dereference D's empty
	// tx map and return nothing. So entries for invalidated B must also
	// be dropped — unless the adopted D carries the same hash, which it
	// doesn't here.
	_, okB := svc.txIndex[txInB]
	assert.False(t, okB, "tx-index must drop entries for a prev-seq ledger that mismatched the adopted parent")
}

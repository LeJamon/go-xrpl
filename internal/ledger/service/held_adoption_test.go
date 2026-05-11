package service

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// heldAdoptionFixture bundles the three inputs AdoptLedgerWithState needs
// (header + state map + tx map) so the cascade tests can build a chain of
// ledgers without repeating boilerplate.
type heldAdoptionFixture struct {
	hdr      *header.LedgerHeader
	stateMap *shamap.SHAMap
	txMap    *shamap.SHAMap
}

// buildHeldAdoptionInputs fabricates a header + fresh empty state/tx maps
// with matching TxHash/AccountHash so AdoptLedgerWithState does not trip
// over hash-consistency checks downstream. The caller supplies seq, hash,
// and parentHash — the cascade logic cares only about those.
func buildHeldAdoptionInputs(t *testing.T, seq uint32, hash, parentHash [32]byte) heldAdoptionFixture {
	t.Helper()

	stateMap, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	stateRoot, err := stateMap.Hash()
	require.NoError(t, err)

	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	hdr := &header.LedgerHeader{
		LedgerIndex: seq,
		Hash:        hash,
		ParentHash:  parentHash,
		TxHash:      txRoot,
		AccountHash: stateRoot,
	}
	return heldAdoptionFixture{hdr: hdr, stateMap: stateMap, txMap: txMap}
}

// TestAdoptLedgerWithState_CascadesHeldOrphan pins F6: when a replay-delta
// for seq N+2 arrives before seq N+1 (out-of-order completion), the
// service stashes the N+2 ledger as a held orphan keyed by its awaited
// parent (seq N+1). Once seq N+1 is adopted and its hash equals the
// stashed parent-hash reference, the cascade must promote the held N+2
// in the same AdoptLedgerWithState call — no second external trigger.
//
// Without the cascade, the out-of-order replay-delta stalls until the
// inbound loop happens to re-request N+2, delaying catch-up.
func TestAdoptLedgerWithState_CascadesHeldOrphan(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	baseSeq := svc.GetClosedLedgerIndex() + 1

	// seq 101 (= baseSeq) is the "awaited parent" for seq 102. Build 101
	// first so we know its hash, then build 102 chaining to that hash.
	var hash101, hash102 [32]byte
	hash101[0] = 0xAA
	hash102[0] = 0xBB

	// Parent of 101 can be anything — we're not exercising the fixMismatch
	// chain on 101 itself, just the held-cascade onto 102.
	var parent101 [32]byte
	fx101 := buildHeldAdoptionInputs(t, baseSeq, hash101, parent101)
	fx102 := buildHeldAdoptionInputs(t, baseSeq+1, hash102, hash101) // 102 chains to 101

	// Submit 102 as a held adoption. 101 is not yet in history so it
	// must stash, not adopt immediately.
	_, err = svc.SubmitHeldAdoption(context.TODO(), fx102.hdr, fx102.stateMap, fx102.txMap)
	require.NoError(t, err)

	svc.mu.RLock()
	_, held := svc.heldAdoptions[baseSeq] // keyed by awaited-parent seq
	svc.mu.RUnlock()
	require.True(t, held, "102 must be stashed under key = awaited parent seq (101)")

	_, err = svc.GetLedgerByHash(hash102)
	require.Error(t, err, "102 must not be in history before 101 arrives")

	// Now adopt 101 directly. Its hash matches the stashed parent
	// reference on 102, so the cascade must promote 102 in the same
	// call.
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), fx101.hdr, fx101.stateMap, fx101.txMap))

	// Both 101 and 102 must be installed in history.
	got101, err := svc.GetLedgerByHash(hash101)
	require.NoError(t, err)
	require.NotNil(t, got101)
	assert.Equal(t, hash101, got101.Hash())

	got102, err := svc.GetLedgerByHash(hash102)
	require.NoError(t, err, "cascade must have adopted 102 after 101 landed")
	require.NotNil(t, got102)
	assert.Equal(t, hash102, got102.Hash())

	// closedLedger must have advanced to 102 (the tip of the cascade).
	assert.Equal(t, hash102, svc.GetClosedLedger().Hash(),
		"closedLedger must advance to the cascade tip, not stop at the parent")

	// The stash must be empty after the successful cascade.
	svc.mu.RLock()
	_, stillHeld := svc.heldAdoptions[baseSeq]
	svc.mu.RUnlock()
	assert.False(t, stillHeld, "successful cascade must remove the held entry")
}

// TestAdoptLedgerWithState_OrphanMismatchDropped pins F6 safety: if the
// held orphan's ParentHash field does NOT match the hash of the ledger
// that just adopted at the parent seq, the orphan is from a divergent
// fork. It must be dropped (not silently promoted onto the wrong chain)
// and must not linger in the stash to re-fire later.
func TestAdoptLedgerWithState_OrphanMismatchDropped(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	baseSeq := svc.GetClosedLedgerIndex() + 1

	// 101 will adopt with hash Y.
	var hashY, hashX, hash102 [32]byte
	hashY[0] = 0xA1
	hashX[0] = 0xFF // deliberately != hashY — the "other fork" parent
	hash102[0] = 0x02

	var parent101 [32]byte
	fx101 := buildHeldAdoptionInputs(t, baseSeq, hashY, parent101)

	// 102 says its parent is X (a different fork), not Y.
	fx102 := buildHeldAdoptionInputs(t, baseSeq+1, hash102, hashX)

	_, err = svc.SubmitHeldAdoption(context.TODO(), fx102.hdr, fx102.stateMap, fx102.txMap)
	require.NoError(t, err)

	// Adopt 101 with hash Y. 102's ParentHash is X ≠ Y → drop 102.
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), fx101.hdr, fx101.stateMap, fx101.txMap))

	// 101 is installed.
	_, err = svc.GetLedgerByHash(hashY)
	require.NoError(t, err)

	// 102 is NOT installed — parent-hash mismatch means it was on a
	// different fork from the one we just adopted.
	_, err = svc.GetLedgerByHash(hash102)
	require.Error(t, err, "102 must NOT be adopted when its ParentHash ≠ adopted hash at the parent seq")

	// And the stash must be empty — the mismatched entry was dropped,
	// not left to re-fire if 101 is re-adopted.
	svc.mu.RLock()
	_, stillHeld := svc.heldAdoptions[baseSeq]
	svc.mu.RUnlock()
	assert.False(t, stillHeld, "mismatched held entry must be dropped, not retained")
}

// TestSubmitHeldAdoption_ParentAlreadyPresent verifies the fast path:
// if the awaited parent is already in ledgerHistory AND its hash matches
// the caller-supplied ParentHash, SubmitHeldAdoption adopts immediately
// rather than pointlessly stashing.
func TestSubmitHeldAdoption_ParentAlreadyPresent(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	baseSeq := svc.GetClosedLedgerIndex() + 1

	var hash101, hash102 [32]byte
	hash101[0] = 0xC1
	hash102[0] = 0xC2
	var parent101 [32]byte

	fx101 := buildHeldAdoptionInputs(t, baseSeq, hash101, parent101)
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), fx101.hdr, fx101.stateMap, fx101.txMap))

	fx102 := buildHeldAdoptionInputs(t, baseSeq+1, hash102, hash101)
	res, err := svc.SubmitHeldAdoption(context.TODO(), fx102.hdr, fx102.stateMap, fx102.txMap)
	require.NoError(t, err)
	require.True(t, res.Adopted, "fast path must report Adopted=true when parent is present")
	require.False(t, res.Stashed, "fast path must not report Stashed=true")

	// 102 must be installed immediately (parent already present).
	got102, err := svc.GetLedgerByHash(hash102)
	require.NoError(t, err)
	assert.Equal(t, hash102, got102.Hash())

	// Nothing must be stashed — the fast path took over.
	svc.mu.RLock()
	_, stashed := svc.heldAdoptions[baseSeq]
	svc.mu.RUnlock()
	assert.False(t, stashed, "fast path must not stash when parent is already present")
}

// TestSubmitHeldAdoption_ParentPresentButHashMismatch pins the fork
// check in SubmitHeldAdoption's fast path: if the awaited parent seq is
// in ledgerHistory but its hash differs from ParentHash, the submission
// is on a divergent fork and must be stashed (not applied onto the wrong
// chain). The stashed entry will be dropped later when the actual fork's
// parent seq is adopted at a different hash — or will TTL-out.
//
// Rationale: silently adopting onto a mismatched parent would corrupt
// history. Stashing-then-dropping lets the correct-fork adopt land first
// without noise.
func TestSubmitHeldAdoption_ParentPresentButHashMismatch(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	baseSeq := svc.GetClosedLedgerIndex() + 1

	var hash101Present, hash101Wanted, hash102 [32]byte
	hash101Present[0] = 0xAA
	hash101Wanted[0] = 0xBB
	hash102[0] = 0x02
	var parent101 [32]byte

	// Adopt 101 with hash AA.
	fx101 := buildHeldAdoptionInputs(t, baseSeq, hash101Present, parent101)
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), fx101.hdr, fx101.stateMap, fx101.txMap))

	// Submit 102 claiming parent BB (≠ AA). Must not adopt onto the
	// mismatched chain; it must be refused as a no-op from the ledger-
	// history perspective.
	fx102 := buildHeldAdoptionInputs(t, baseSeq+1, hash102, hash101Wanted)
	_, err = svc.SubmitHeldAdoption(context.TODO(), fx102.hdr, fx102.stateMap, fx102.txMap)
	require.NoError(t, err)

	// 102 must NOT be in history — neither adopted nor cascaded.
	_, err = svc.GetLedgerByHash(hash102)
	require.Error(t, err, "divergent-parent submissions must not be adopted onto the existing chain")
}

// TestSubmitHeldAdoption_DivergentParentResultFlag pins the
// DivergentParent signal that the router needs to bypass its
// "skip if parentSeq <= closedLedger" guard in armParentAcquisition.
//
// Without this flag the all-5 UNL soak observed goxrpl-1 wedged at
// validated_seq=5 / closed_seq=43: a consensus-timeout at seq=43 left
// the local chain on a fork, peers kept supplying the network's seq=44+
// via inbound acquisition, but every SubmitHeldAdoption(seq=44) hit the
// divergent-parent branch and the router's armParentAcquisition early-
// returned because parent_seq=43 ≤ closed_seq=43. No re-acquisition
// fired for the network's correct seq=43 → fixMismatchLocked never
// tripped → chain stuck.
//
// Properties pinned:
//  1. Result.Stashed=true (carries to existing tests).
//  2. Result.DivergentParent=true ONLY when parent IS present locally
//     but on a different fork (not when parent is simply missing).
//  3. ParentSeq / ParentHash echo the candidate's awaited parent so the
//     router can target acquisition at the right (seq, hash).
func TestSubmitHeldAdoption_DivergentParentResultFlag(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	baseSeq := svc.GetClosedLedgerIndex() + 1

	var localParentHash, candidateParentHash, child [32]byte
	localParentHash[0] = 0xAA
	candidateParentHash[0] = 0xBB
	child[0] = 0xCC
	var grandparent [32]byte

	// Adopt local parent at baseSeq with hash AA.
	fxLocal := buildHeldAdoptionInputs(t, baseSeq, localParentHash, grandparent)
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), fxLocal.hdr, fxLocal.stateMap, fxLocal.txMap))

	// Submit child at baseSeq+1 claiming parent BB (≠ AA we have).
	fxChild := buildHeldAdoptionInputs(t, baseSeq+1, child, candidateParentHash)
	res, err := svc.SubmitHeldAdoption(context.TODO(), fxChild.hdr, fxChild.stateMap, fxChild.txMap)
	require.NoError(t, err)

	assert.True(t, res.Stashed, "divergent-parent submission must be stashed")
	assert.True(t, res.DivergentParent,
		"DivergentParent must be true when parent IS present locally but on the wrong fork — "+
			"this is the signal that lets the router override its "+
			"`parentSeq <= closedLedger` guard in armParentAcquisition")
	assert.Equal(t, baseSeq, res.ParentSeq, "ParentSeq must echo the awaited parent seq")
	assert.Equal(t, candidateParentHash, res.ParentHash, "ParentHash must echo the candidate's awaited parent hash")
	assert.False(t, res.Adopted, "must not adopt onto a wrong-fork local parent")
}

// TestSubmitHeldAdoption_MissingParentDoesNotSetDivergent ensures the
// DivergentParent flag is reserved for the genuine fork case. When the
// parent is simply not present yet (out-of-order replay-delta arrival),
// stashing is correct but DivergentParent must stay false — the router
// would otherwise issue redundant acquisitions for parents that are
// merely in flight.
func TestSubmitHeldAdoption_MissingParentDoesNotSetDivergent(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	// Submit a child whose awaited parent seq is not in history.
	farSeq := svc.GetClosedLedgerIndex() + 50
	var childHash, parentHash [32]byte
	childHash[0] = 0xDD
	parentHash[0] = 0xEE
	fx := buildHeldAdoptionInputs(t, farSeq, childHash, parentHash)
	res, err := svc.SubmitHeldAdoption(context.TODO(), fx.hdr, fx.stateMap, fx.txMap)
	require.NoError(t, err)

	assert.True(t, res.Stashed)
	assert.False(t, res.DivergentParent,
		"missing parent must NOT set DivergentParent — the router's redundant-"+
			"acquisition guard relies on this distinction")
}

// TestAdoptLedgerWithState_HeldAdoptionExpires verifies the 60s TTL: a
// held entry older than heldAdoptionTTL must be evicted on the next
// adopt call and must NOT cascade even if the parent hash matches.
// The evicted entry's cascade is prevented to avoid promoting a ledger
// whose peer-source context is stale (the replay-delta may have been
// superseded by a later round).
func TestAdoptLedgerWithState_HeldAdoptionExpires(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	baseSeq := svc.GetClosedLedgerIndex() + 1

	var hash101, hash102 [32]byte
	hash101[0] = 0xE1
	hash102[0] = 0xE2
	var parent101 [32]byte

	fx101 := buildHeldAdoptionInputs(t, baseSeq, hash101, parent101)
	fx102 := buildHeldAdoptionInputs(t, baseSeq+1, hash102, hash101)

	// Manually stash 102 with a stale `at` to simulate age > TTL.
	svc.mu.Lock()
	svc.heldAdoptions[baseSeq] = &pendingAdopt{
		header:   fx102.hdr,
		stateMap: fx102.stateMap,
		txMap:    fx102.txMap,
		at:       time.Now().Add(-2 * heldAdoptionTTL),
	}
	svc.mu.Unlock()

	// Adopt 101. Even though the hash matches 102's ParentHash, 102 is
	// expired and must be evicted without cascading.
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), fx101.hdr, fx101.stateMap, fx101.txMap))

	_, err = svc.GetLedgerByHash(hash102)
	require.Error(t, err, "expired held entry must NOT cascade — 102 must stay out of history")

	svc.mu.RLock()
	_, stillHeld := svc.heldAdoptions[baseSeq]
	svc.mu.RUnlock()
	assert.False(t, stillHeld, "expired entry must be evicted from the stash")
}

// TestAdoptLedgerWithState_MultiLevelCascade pins that a chain of held
// orphans (102 waiting on 101, 103 waiting on 102) promotes in one
// AdoptLedgerWithState(101) call. The cascade walks forward until the
// stash has no match at the next-seq key.
//
// Two-hop cascade is the realistic upper bound for goXRPL's replay-delta
// stream: the 256-level cap exists to guard against pathological inputs,
// but real cascades are short.
func TestAdoptLedgerWithState_MultiLevelCascade(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	baseSeq := svc.GetClosedLedgerIndex() + 1

	var hash101, hash102, hash103 [32]byte
	hash101[0] = 0x01
	hash102[0] = 0x02
	hash103[0] = 0x03
	var parent101 [32]byte

	fx101 := buildHeldAdoptionInputs(t, baseSeq, hash101, parent101)
	fx102 := buildHeldAdoptionInputs(t, baseSeq+1, hash102, hash101)
	fx103 := buildHeldAdoptionInputs(t, baseSeq+2, hash103, hash102)

	// Submit 103 before 102 — both stash.
	_, err = svc.SubmitHeldAdoption(context.TODO(), fx103.hdr, fx103.stateMap, fx103.txMap)
	require.NoError(t, err)
	_, err = svc.SubmitHeldAdoption(context.TODO(), fx102.hdr, fx102.stateMap, fx102.txMap)
	require.NoError(t, err)

	svc.mu.RLock()
	_, has102Parent := svc.heldAdoptions[baseSeq]   // 102 waits on 101
	_, has103Parent := svc.heldAdoptions[baseSeq+1] // 103 waits on 102
	svc.mu.RUnlock()
	require.True(t, has102Parent, "102 must stash under parent-seq 101")
	require.True(t, has103Parent, "103 must stash under parent-seq 102")

	// Adopt 101 — cascade must flush both 102 and 103.
	require.NoError(t, svc.AdoptLedgerWithState(context.TODO(), fx101.hdr, fx101.stateMap, fx101.txMap))

	for _, h := range [][32]byte{hash101, hash102, hash103} {
		got, err := svc.GetLedgerByHash(h)
		require.NoError(t, err, "cascade must adopt 101, 102, and 103")
		require.NotNil(t, got)
	}

	assert.Equal(t, hash103, svc.GetClosedLedger().Hash(),
		"closedLedger must track the cascade tip (103), not stop mid-cascade")

	// Stash must be empty.
	svc.mu.RLock()
	remaining := len(svc.heldAdoptions)
	svc.mu.RUnlock()
	assert.Zero(t, remaining, "multi-level cascade must drain every matching held entry")
}

// TestSubmitHeldAdoption_RejectsNil pins basic input validation: nil
// header or nil state map must not be stashed (they would cause a panic
// later on cascade). txMap may be nil (legacy catchup path).
func TestSubmitHeldAdoption_RejectsNil(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	_, err = svc.SubmitHeldAdoption(context.TODO(), nil, nil, nil)
	assert.Error(t, err, "nil header must be rejected")

	hdr := &header.LedgerHeader{LedgerIndex: 42}
	_, err = svc.SubmitHeldAdoption(context.TODO(), hdr, nil, nil)
	assert.Error(t, err, "nil state map must be rejected")
}

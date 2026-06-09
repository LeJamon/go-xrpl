package adaptor

import (
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// proofPathCall records a RequestProofPath the router issues. Lets
// the test verify the sender saw exactly one proof-path request with
// the expected tip hash and key.
type proofPathCall struct {
	peerID     uint64
	ledgerHash [32]byte
	key        [32]byte
	mapType    message.LedgerMapType
}

// recordingSenderWithProofPath wraps the existing recordingSender
// with proof-path recording — the multi-ledger replay path adds a
// second wire-request type the original recorder didn't track.
type recordingSenderWithProofPath struct {
	recordingSender
	pathMu    sync.Mutex
	pathCalls []proofPathCall
}

func (s *recordingSenderWithProofPath) RequestProofPath(peerID uint64, ledgerHash, key [32]byte, mt message.LedgerMapType) error {
	s.pathMu.Lock()
	defer s.pathMu.Unlock()
	s.pathCalls = append(s.pathCalls, proofPathCall{
		peerID: peerID, ledgerHash: ledgerHash, key: key, mapType: mt,
	})
	return nil
}

func (s *recordingSenderWithProofPath) proofPathCalls() []proofPathCall {
	s.pathMu.Lock()
	defer s.pathMu.Unlock()
	out := make([]proofPathCall, len(s.pathCalls))
	copy(out, s.pathCalls)
	return out
}

// closeEmpty closes the supplied open ledger at closeTime with no
// txs and returns the resulting closed ledger plus its serialized
// header bytes. Mirrors buildEmptyClosedSuccessorResponse's shape
// but caller-driven so we can build a chain by feeding each closed
// ledger back as the parent for the next NewOpen.
func closeEmpty(t *testing.T, parent *ledger.Ledger, closeTime time.Time) (*ledger.Ledger, []byte) {
	t.Helper()
	open, err := ledger.NewOpen(parent, closeTime)
	require.NoError(t, err)
	require.NoError(t, open.Close(closeTime, 0))
	hdr := open.Header()
	hdrBytes := header.AddRaw(hdr, false)
	return open, hdrBytes
}

// buildChainFromAnchor closes `n` ledgers on top of `anchor`,
// returning the resulting chain in order — chain[0] = anchor+1,
// chain[n-1] = the tip. headerBytes[i] is the serialized header of
// chain[i] (matches what a peer would put in TMReplayDeltaResponse).
func buildChainFromAnchor(t *testing.T, anchor *ledger.Ledger, n int) (chain []*ledger.Ledger, headerBytes [][]byte) {
	t.Helper()
	chain = make([]*ledger.Ledger, n)
	headerBytes = make([][]byte, n)
	parent := anchor
	closeBase := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		ct := closeBase.Add(time.Duration(i) * 10 * time.Second)
		closed, hb := closeEmpty(t, parent, ct)
		chain[i] = closed
		headerBytes[i] = hb
		parent = closed
	}
	return
}

// installSkipListIntoTipState mutates `tip`'s state map to install a
// LedgerHashes SLE at keylet::skip() containing the supplied 256-hash
// rolling buffer. Returns the new state-map root hash (which becomes
// the tip's AccountHash for the proof-verification path) plus the
// leaf-to-root proof path of the inserted entry.
//
// We don't actually need the tip's "real" AccountHash for the
// integration test — what matters is that the proof verifies against
// SOME stateHash we declare to be the tip's, and the verifier picks
// up the right Hashes vector. So this helper builds a synthetic
// state map containing just the LedgerHashes SLE.
func installSkipListIntoTipState(t *testing.T, hashes [][32]byte, lastSeq uint32) (stateHash [32]byte, path [][]byte) {
	t.Helper()
	hashHexes := make([]string, len(hashes))
	for i, h := range hashes {
		hashHexes[i] = fmt.Sprintf("%064X", h)
	}
	hx, err := binarycodec.Encode(map[string]any{
		"LedgerEntryType":    "LedgerHashes",
		"Flags":              uint32(0),
		"Hashes":             hashHexes,
		"LastLedgerSequence": lastSeq,
	})
	require.NoError(t, err)
	payload, err := hex.DecodeString(hx)
	require.NoError(t, err)

	sm, err := shamap.New(shamap.TypeState)
	require.NoError(t, err)
	skipKL := keylet.LedgerHashes()
	require.NoError(t, sm.PutWithNodeType(skipKL.Key, payload, shamap.NodeTypeAccountState))
	require.NoError(t, sm.SetImmutable())
	stateHash, err = sm.Hash()
	require.NoError(t, err)
	proof, err := sm.GetProofPath(skipKL.Key)
	require.NoError(t, err)
	require.True(t, proof.Found)
	return stateHash, proof.Path
}

// makeRouterWithProofPath wires a router against a recordingSender
// that supports RequestProofPath. Returns the pieces tests poke +
// inspect.
func makeRouterWithProofPath(t *testing.T) (*Router, *Adaptor, *recordingSenderWithProofPath, *service.Service) {
	t.Helper()
	svc := newTestLedgerService(t)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	rs := &recordingSenderWithProofPath{
		recordingSender: recordingSender{peerSupportsReplay: true},
	}
	a := New(Config{
		LedgerService: svc,
		Sender:        rs,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
	inbox := make(chan *peermanagement.InboundMessage, 8)
	r := NewRouter(nil, a, nil, inbox)
	return r, a, rs, svc
}

// TestRouter_ReplayTask_EndToEnd_3LedgerWalk drives the full Phase 5
// integration: arm a task, deliver a verified proof-path response,
// deliver three replay-delta responses in chain order, and assert
// that each of the three intermediate ledgers adopts via the
// router's adoption path.
//
// Three is the smallest chain that exercises the full machinery
// (anchor → +1 → +2 → +3 tip) — bigger chains would stress the same
// code path identically with more wire round-trips.
func TestRouter_ReplayTask_EndToEnd_3LedgerWalk(t *testing.T) {
	r, _, rs, svc := makeRouterWithProofPath(t)

	anchor := svc.GetClosedLedger()
	require.NotNil(t, anchor)
	anchorSeq := anchor.Sequence()

	// Build a chain of 3 ledgers on top of the anchor. chain[2] is
	// the tip; chain[0..1] are the intermediates we want the task to
	// fetch via replay-delta.
	chain, headerBytesByIdx := buildChainFromAnchor(t, anchor, 3)
	tip := chain[len(chain)-1]
	tipSeq := tip.Sequence()
	tipHash := tip.Hash()
	require.Equal(t, anchorSeq+3, tipSeq)

	// Build the skip-list SLE the proof-path response will carry.
	// For depth=3 we need 2 ancestor hashes (chain[0].Hash and
	// chain[1].Hash) — the tip itself is fetched via a separate
	// replay-delta. Pad earlier entries with deterministic filler.
	skipHashes := make([][32]byte, 256)
	for i := range 256 - 2 {
		var h [32]byte
		h[0] = 0xFE
		h[1] = byte(i)
		skipHashes[i] = h
	}
	skipHashes[254] = chain[0].Hash()
	skipHashes[255] = chain[1].Hash()

	stateHash, proofPath := installSkipListIntoTipState(t, skipHashes, tipSeq-1)

	// Arm the task.
	require.NoError(t, r.StartReplayTask(
		tipHash, stateHash, tipSeq, 3,
		anchor,
		[]uint64{77, 78},
	))
	assert.True(t, r.HasActiveReplayTask())

	// Sender should have seen exactly one proof-path request.
	require.Eventually(t, func() bool {
		return len(rs.proofPathCalls()) == 1
	}, time.Second, 5*time.Millisecond)
	pp := rs.proofPathCalls()[0]
	assert.Equal(t, tipHash, pp.ledgerHash)
	assert.Equal(t, keylet.LedgerHashes().Key, pp.key)
	assert.Equal(t, message.LedgerMapAccountState, pp.mapType)

	// Deliver the proof-path response. The router should fan out
	// 3 replay-delta requests immediately (depth=3 fits under the
	// global cap of 16).
	skipKey := keylet.LedgerHashes().Key
	proofResp := &message.ProofPathResponse{
		LedgerHash: tipHash[:],
		Key:        skipKey[:],
		MapType:    message.LedgerMapAccountState,
		Path:       proofPath,
	}
	proofEnc, err := message.Encode(proofResp)
	require.NoError(t, err)
	r.handleProofPathResponse(&peermanagement.InboundMessage{
		PeerID:  peermanagement.PeerID(77),
		Type:    uint16(message.TypeProofPathResponse),
		Payload: proofEnc,
	})

	require.Eventually(t, func() bool {
		return len(rs.replayCalls()) == 3
	}, time.Second, 5*time.Millisecond)
	dc := rs.replayCalls()
	wantHashes := map[[32]byte]bool{
		chain[0].Hash(): false,
		chain[1].Hash(): false,
		chain[2].Hash(): false,
	}
	for _, c := range dc {
		_, ok := wantHashes[c.hash]
		assert.Truef(t, ok, "delta request for unexpected hash %x", c.hash[:8])
		wantHashes[c.hash] = true
	}
	for h, seen := range wantHashes {
		assert.Truef(t, seen, "no delta request for chain hash %x", h[:8])
	}

	// Deliver delta responses in chain order (oldest first). The
	// router's drain loop applies each as soon as its parent is in
	// the adopted set, so feeding in order lets every adopt fire.
	for i, l := range chain {
		lHash := l.Hash()
		resp := &message.ReplayDeltaResponse{
			LedgerHash:   lHash[:],
			LedgerHeader: headerBytesByIdx[i],
			Transactions: nil,
		}
		enc, err := message.Encode(resp)
		require.NoError(t, err)
		r.handleReplayDeltaResponse(&peermanagement.InboundMessage{
			PeerID:  peermanagement.PeerID(77),
			Type:    uint16(message.TypeReplayDeltaResponse),
			Payload: enc,
		})
	}

	// After all three responses, the task should complete and the
	// service should have adopted up through the tip.
	require.Eventually(t, func() bool {
		return !r.HasActiveReplayTask()
	}, time.Second, 5*time.Millisecond)
	assert.Equal(t, tipSeq, svc.GetClosedLedgerIndex(),
		"every chain ledger should have adopted")
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	assert.Equal(t, tipHash, closed.Hash())
}

// TestRouter_ReplayTask_RejectsBadProof drives the abort path:
// a peer serves a tampered proof, the task transitions to Failed,
// the router clears its registry and charges the peer for
// proof-path-verify bad data.
func TestRouter_ReplayTask_RejectsBadProof(t *testing.T) {
	r, _, rs, svc := makeRouterWithProofPath(t)

	anchor := svc.GetClosedLedger()
	require.NotNil(t, anchor)
	chain, _ := buildChainFromAnchor(t, anchor, 3)
	tip := chain[2]
	tipHash := tip.Hash()

	// Compute a VALID stateHash for some hashes vector, then
	// declare to the router that the SAME stateHash is the tip's —
	// but serve a proof against a DIFFERENT hashes vector. The
	// merkle verify must fail.
	correctHashes := make([][32]byte, 256)
	for i := range correctHashes {
		correctHashes[i] = [32]byte{byte(i), 0xCC}
	}
	stateHash, _ := installSkipListIntoTipState(t, correctHashes, tip.Sequence()-1)

	wrongHashes := make([][32]byte, 256)
	for i := range wrongHashes {
		wrongHashes[i] = [32]byte{byte(i), 0xDD}
	}
	_, wrongPath := installSkipListIntoTipState(t, wrongHashes, tip.Sequence()-1)

	require.NoError(t, r.StartReplayTask(
		tipHash, stateHash, tip.Sequence(), 3,
		anchor,
		[]uint64{99},
	))
	require.Eventually(t, func() bool {
		return len(rs.proofPathCalls()) == 1
	}, time.Second, 5*time.Millisecond)

	skipKey := keylet.LedgerHashes().Key
	badResp := &message.ProofPathResponse{
		LedgerHash: tipHash[:],
		Key:        skipKey[:],
		MapType:    message.LedgerMapAccountState,
		Path:       wrongPath,
	}
	enc, err := message.Encode(badResp)
	require.NoError(t, err)
	r.handleProofPathResponse(&peermanagement.InboundMessage{
		PeerID:  peermanagement.PeerID(99),
		Type:    uint16(message.TypeProofPathResponse),
		Payload: enc,
	})

	assert.False(t, r.HasActiveReplayTask(),
		"task must clear after a proof-path verification failure")
	// No replay-delta requests should have been issued — the task
	// aborted before fan-out.
	assert.Empty(t, rs.replayCalls(),
		"no replay-delta requests should fire after a proof failure")
}

// TestRouter_ReplayTask_DoubleStartRejected guards against
// double-arming. A second StartReplayTask while one is in flight
// must return an error without disturbing the first task.
func TestRouter_ReplayTask_DoubleStartRejected(t *testing.T) {
	r, _, _, svc := makeRouterWithProofPath(t)
	anchor := svc.GetClosedLedger()
	chain, _ := buildChainFromAnchor(t, anchor, 3)
	tip := chain[2]
	skipHashes := make([][32]byte, 256)
	skipHashes[254] = chain[0].Hash()
	skipHashes[255] = chain[1].Hash()
	stateHash, _ := installSkipListIntoTipState(t, skipHashes, tip.Sequence()-1)

	require.NoError(t, r.StartReplayTask(
		tip.Hash(), stateHash, tip.Sequence(), 3,
		anchor,
		[]uint64{1},
	))
	err := r.StartReplayTask(
		tip.Hash(), stateHash, tip.Sequence(), 3,
		anchor,
		[]uint64{1},
	)
	require.Error(t, err)
	assert.True(t, r.HasActiveReplayTask(),
		"first task must remain in flight after double-start rejection")

	r.AbortActiveReplayTask(nil)
	assert.False(t, r.HasActiveReplayTask())
}

// TestRouter_ReplayTask_AnchorSeqMustMatch rejects an anchor whose
// sequence doesn't equal tipSeq - depth. Wiring this check at the
// entry point prevents a class of bugs where the caller miscomputes
// depth and ends up with a chain that links nowhere.
func TestRouter_ReplayTask_AnchorSeqMustMatch(t *testing.T) {
	r, _, _, svc := makeRouterWithProofPath(t)
	anchor := svc.GetClosedLedger()

	// Anchor is at seq=2 (standalone genesis); claim tipSeq=10 with
	// depth=3 — that requires anchor at seq 7, which we don't have.
	err := r.StartReplayTask(
		[32]byte{0xAB}, [32]byte{0xCD}, 10, 3,
		anchor,
		[]uint64{1},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "anchorParent.Sequence")
}

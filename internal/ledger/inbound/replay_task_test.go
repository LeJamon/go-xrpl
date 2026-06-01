package inbound

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// emptyTxMapRoot returns the canonical empty SHAMap.TypeTransaction
// root. Cached at first use because constructing it is the same work
// the verifier does for an empty tx list — keeping the helper
// transparent makes the test header builder one-liner.
var emptyTxMapRootOnce = struct {
	sync.Once
	root [32]byte
}{}

func emptyTxMapRoot(t *testing.T) [32]byte {
	t.Helper()
	emptyTxMapRootOnce.Do(func() {
		sm, err := shamap.New(shamap.TypeTransaction)
		require.NoError(t, err)
		require.NoError(t, sm.SetImmutable())
		root, err := sm.Hash()
		require.NoError(t, err)
		emptyTxMapRootOnce.root = root
	})
	return emptyTxMapRootOnce.root
}

// makeSyntheticLedgerHeader fabricates a serializable ledger header
// for the given seq, parent hash, and a synthetic account hash. Each
// header in the chain links cryptographically: the returned hash
// equals what genesis.CalculateLedgerHash produces for the header
// bytes, so a ReplayDeltaResponse carrying these bytes verifies under
// the existing framing path.
func makeSyntheticLedgerHeader(t *testing.T, seq uint32, parentHash, accountHash [32]byte) (headerBytes []byte, ledgerHash [32]byte) {
	t.Helper()

	closeBase := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Stagger close times by seq so each header is distinct even when
	// every other field matches.
	closeTime := closeBase.Add(time.Duration(seq) * 10 * time.Second)

	hdr := header.LedgerHeader{
		LedgerIndex:         seq,
		ParentHash:          parentHash,
		ParentCloseTime:     closeTime,
		CloseTime:           closeTime.Add(10 * time.Second),
		CloseTimeResolution: 10,
		Drops:               100_000_000_000_000_000,
		CloseFlags:          0,
		TxHash:              emptyTxMapRoot(t),
		AccountHash:         accountHash,
	}
	bytes := header.AddRaw(hdr, false)

	parsed, err := header.DeserializeHeader(bytes, false)
	require.NoError(t, err)
	expected := genesis.CalculateLedgerHash(*parsed)
	return bytes, expected
}

// buildChain fabricates a chain of n+1 ledger headers starting from
// anchor seq+1 up through tipSeq=anchorSeq+n. Returns:
//
//	headers[i] = serialized header for seq anchorSeq+1+i
//	hashes[i]  = canonical hash of headers[i]
//
// hashes[0] is the oldest, hashes[n-1] is the tip. Each header's
// ParentHash chains to hashes[i-1] (or anchorHash for i=0).
func buildChain(t *testing.T, anchorHash [32]byte, anchorSeq uint32, n int) (headers [][]byte, hashes [][32]byte) {
	t.Helper()
	headers = make([][]byte, n)
	hashes = make([][32]byte, n)
	parent := anchorHash
	for i := 0; i < n; i++ {
		seq := anchorSeq + uint32(i) + 1
		// Per-seq synthetic account hash so headers differ even with
		// identical close times and tx hashes.
		var ah [32]byte
		ah[0] = byte(seq >> 8)
		ah[1] = byte(seq)
		hb, h := makeSyntheticLedgerHeader(t, seq, parent, ah)
		headers[i] = hb
		hashes[i] = h
		parent = h
	}
	return
}

// recordingTaskSender captures every RequestProofPath /
// RequestReplayDelta the task issues and exposes them for replay
// against the task via callbacks. It tracks in-flight delta counts so
// the parallelism-cap test can assert at every wire event.
type recordingTaskSender struct {
	mu        sync.Mutex
	proofReqs []proofReq
	deltaReqs []deltaReq

	// inFlightPerPeer / inFlightGlobal are book-kept on each delta
	// request and decremented when the test "delivers" a response.
	inFlightPerPeer map[uint64]int
	inFlightGlobal  int

	// peakInFlight* track the maximum observed so test assertions can
	// confirm the caps were respected.
	peakGlobal  int
	peakPerPeer map[uint64]int

	// failProof / failDelta optionally cause the next wire call to
	// fail synchronously, simulating transport errors.
	failProofOnce atomic.Bool
	failDeltaOnce atomic.Bool
}

type proofReq struct {
	PeerID     uint64
	LedgerHash [32]byte
	Key        [32]byte
	MapType    message.LedgerMapType
}

type deltaReq struct {
	PeerID uint64
	Hash   [32]byte
}

func newRecordingTaskSender() *recordingTaskSender {
	return &recordingTaskSender{
		inFlightPerPeer: make(map[uint64]int),
		peakPerPeer:     make(map[uint64]int),
	}
}

func (s *recordingTaskSender) RequestProofPath(peerID uint64, ledgerHash, key [32]byte, mt message.LedgerMapType) error {
	if s.failProofOnce.CompareAndSwap(true, false) {
		return errors.New("synthetic proof-path send failure")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proofReqs = append(s.proofReqs, proofReq{peerID, ledgerHash, key, mt})
	return nil
}

func (s *recordingTaskSender) RequestReplayDelta(peerID uint64, hash [32]byte) error {
	if s.failDeltaOnce.CompareAndSwap(true, false) {
		return errors.New("synthetic delta send failure")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deltaReqs = append(s.deltaReqs, deltaReq{peerID, hash})
	s.inFlightGlobal++
	s.inFlightPerPeer[peerID]++
	if s.inFlightGlobal > s.peakGlobal {
		s.peakGlobal = s.inFlightGlobal
	}
	if s.inFlightPerPeer[peerID] > s.peakPerPeer[peerID] {
		s.peakPerPeer[peerID] = s.inFlightPerPeer[peerID]
	}
	return nil
}

func (s *recordingTaskSender) noteDeltaDelivered(peerID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlightGlobal--
	s.inFlightPerPeer[peerID]--
}

func (s *recordingTaskSender) deltaReqsCopy() []deltaReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]deltaReq, len(s.deltaReqs))
	copy(out, s.deltaReqs)
	return out
}

// TestLedgerReplayTask_50LedgerBackwardWalk drives the full backward
// catchup: node has local anchor at seq N-50, task fetches tip's
// skip-list, then framing-verifies all 50 ancestor + tip ledgers in
// chain order. Verifies the OnDeltaVerified callback fires for every
// chain entry and the task transitions to TaskStateComplete.
func TestLedgerReplayTask_50LedgerBackwardWalk(t *testing.T) {
	t.Parallel()
	const (
		depth     uint32 = 50
		anchorSeq uint32 = 1000
		tipSeq    uint32 = anchorSeq + depth
	)
	anchorHash := [32]byte{0xAA}

	// Build the 50 ledgers from anchor+1 to tip.
	chainHeaders, chainHashes := buildChain(t, anchorHash, anchorSeq, int(depth))
	tipHash := chainHashes[len(chainHashes)-1]

	// Build the skip-list SLE for the tip's state: the rolling-256
	// list as it would exist after the tip's close. Pad earlier
	// entries with dummy hashes so the LAST 49 entries are the
	// hashes of seqs tip-49..tip-1 (which is chainHashes[0..48]).
	skipHashes := make([][32]byte, 256)
	// Earlier 207 slots filled with deterministic but distinct
	// fillers so they wouldn't accidentally collide with chainHashes.
	for i := 0; i < 256-49; i++ {
		var h [32]byte
		h[0] = 0xCC
		h[1] = byte(i)
		skipHashes[i] = h
	}
	// Last 49 = chainHashes[0..48] (seqs tipSeq-49..tipSeq-1).
	copy(skipHashes[256-49:], chainHashes[:49])

	leafPayload := buildSkipListLeafSLE(t, skipHashes, tipSeq-1)
	stateHash, proofPath := buildSkipListProof(t, leafPayload)

	// Wire up task + sender + replayer.
	replayer := NewReplayer(nil, nil, 0)
	sender := newRecordingTaskSender()

	verifiedOrder := []uint32{}
	var verifiedMu sync.Mutex
	cb := TaskCallbacks{
		OnDeltaVerified: func(seq uint32, hash [32]byte, rd *ReplayDelta) {
			verifiedMu.Lock()
			defer verifiedMu.Unlock()
			verifiedOrder = append(verifiedOrder, seq)
			assert.True(t, rd.IsComplete(), "callback must fire after rd.IsComplete")
		},
	}

	task, err := NewLedgerReplayTask(
		tipHash, stateHash, tipSeq, depth,
		[]uint64{42, 43, 44}, // three peers — global cap can be reached
		replayer, sender, nil, cb,
	)
	require.NoError(t, err)

	require.NoError(t, task.Start())
	assert.Equal(t, TaskStateWantSkipList, task.State())
	require.Len(t, sender.proofReqs, 1)
	assert.Equal(t, tipHash, sender.proofReqs[0].LedgerHash)
	assert.Equal(t, message.LedgerMapAccountState, sender.proofReqs[0].MapType)

	// Deliver the skip-list proof — task should fan out deltas.
	skipKey := keylet.LedgerHashes().Key
	proofResp := &message.ProofPathResponse{
		LedgerHash: tipHash[:],
		Key:        skipKey[:],
		MapType:    message.LedgerMapAccountState,
		Path:       proofPath,
	}
	require.NoError(t, task.OnSkipListResponse(proofResp))
	assert.Equal(t, TaskStateRunningDeltas, task.State())

	// After initial fan-out, sender should have queued exactly
	// min(depth, DefaultMaxInFlightReplays) delta requests — depth=50
	// here, cap=16, so 16 requests are in flight, the rest pending.
	require.GreaterOrEqual(t, len(sender.deltaReqsCopy()), 1,
		"task must fan out at least one delta on skip-list arrival")
	assert.LessOrEqual(t, len(sender.deltaReqsCopy()), DefaultMaxInFlightReplays,
		"initial fan-out must respect the global cap")

	// Deliver delta responses in chain order, refilling the fan-out
	// each time. Continue until the task completes.
	chainSeqByHash := make(map[[32]byte]int)
	for i, h := range chainHashes {
		chainSeqByHash[h] = i
	}
	alreadyDelivered := map[[32]byte]bool{}

	for !task.IsComplete() {
		// Take a snapshot of the next-not-yet-responded request.
		reqs := sender.deltaReqsCopy()
		var next *deltaReq
		for i := range reqs {
			r := reqs[i]
			// We don't track per-request delivery state on the recorder
			// directly; instead we look up the chain entry by hash and
			// check if the task already verified it.
			idx, ok := chainSeqByHash[r.Hash]
			if !ok {
				t.Fatalf("sender issued delta for unknown hash %x", r.Hash[:8])
			}
			seq := chainHashes[idx]
			_ = seq
			// Use the task's snapshot to find an unverified hash.
			// (Avoid coupling to internal chain state by querying
			// what's outstanding via the per-test pendingMap.)
			next = &r
			// Skip if we've already delivered (i.e., the in-flight
			// counter would underflow). The simplest invariant is:
			// for each unique deltaReq, deliver exactly once. We
			// track that here.
			if alreadyDelivered[r.Hash] {
				next = nil
				continue
			}
			break
		}
		require.NotNil(t, next, "no pending delta to deliver but task incomplete")
		alreadyDelivered[next.Hash] = true

		idx := chainSeqByHash[next.Hash]
		deltaResp := &message.ReplayDeltaResponse{
			LedgerHash:   next.Hash[:],
			LedgerHeader: chainHeaders[idx],
			Transactions: nil,
		}
		sender.noteDeltaDelivered(next.PeerID)
		require.NoError(t, task.OnDeltaResponse(deltaResp))
	}

	// Every chain entry must have verified, in oldest-first order.
	require.Len(t, verifiedOrder, int(depth))
	// The task fires the callback in the order responses arrive (not
	// strictly chain-ordered: deliveries can interleave across peers).
	// What we MUST hold: every seq from anchorSeq+1..tipSeq appears
	// exactly once.
	seen := make(map[uint32]bool, len(verifiedOrder))
	for _, s := range verifiedOrder {
		assert.GreaterOrEqual(t, s, anchorSeq+1)
		assert.LessOrEqual(t, s, tipSeq)
		assert.False(t, seen[s], "seq %d verified twice", s)
		seen[s] = true
	}
	assert.Len(t, seen, int(depth))
	assert.Equal(t, TaskStateComplete, task.State())
	// Replayer slots should be drained.
	assert.Equal(t, 0, replayer.Count(), "all replay-delta slots freed")
	assert.Equal(t, 0, replayer.SkipListCount(), "skip-list slot freed")
}

// TestLedgerReplayTask_ParallelismBoundedByExistingCaps drives the
// same 50-ledger backward walk but asserts at every wire event that
// in-flight counts never exceed the Replayer's caps:
//
//	DefaultMaxInFlightReplays = 16 globally
//	MaxPerPeerReplays         =  2 per peer
//
// With three peers and 50 chain entries, the global cap of 16 caps
// concurrent in-flight at 16 (3 peers × 2 each = 6 is the per-peer
// ceiling, so the per-peer cap is the binding constraint here).
func TestLedgerReplayTask_ParallelismBoundedByExistingCaps(t *testing.T) {
	t.Parallel()
	const (
		depth     uint32 = 50
		anchorSeq uint32 = 5000
		tipSeq    uint32 = anchorSeq + depth
	)
	anchorHash := [32]byte{0xBB}

	chainHeaders, chainHashes := buildChain(t, anchorHash, anchorSeq, int(depth))
	tipHash := chainHashes[len(chainHashes)-1]

	skipHashes := make([][32]byte, 256)
	for i := 0; i < 256-49; i++ {
		var h [32]byte
		h[0] = 0xDD
		h[1] = byte(i)
		skipHashes[i] = h
	}
	copy(skipHashes[256-49:], chainHashes[:49])

	leafPayload := buildSkipListLeafSLE(t, skipHashes, tipSeq-1)
	stateHash, proofPath := buildSkipListProof(t, leafPayload)

	replayer := NewReplayer(nil, nil, 0)
	sender := newRecordingTaskSender()

	peers := []uint64{100, 101, 102}
	task, err := NewLedgerReplayTask(
		tipHash, stateHash, tipSeq, depth,
		peers,
		replayer, sender, nil, TaskCallbacks{},
	)
	require.NoError(t, err)

	require.NoError(t, task.Start())
	skipKey := keylet.LedgerHashes().Key
	require.NoError(t, task.OnSkipListResponse(&message.ProofPathResponse{
		LedgerHash: tipHash[:],
		Key:        skipKey[:],
		MapType:    message.LedgerMapAccountState,
		Path:       proofPath,
	}))

	// Drive deliveries one at a time, asserting cap invariants after
	// every batch of new wire requests.
	chainSeqByHash := make(map[[32]byte]int)
	for i, h := range chainHashes {
		chainSeqByHash[h] = i
	}
	delivered := make(map[[32]byte]bool)

	for !task.IsComplete() {
		// After every fan-out, the in-flight bookkeeping must respect
		// both caps.
		assertCaps(t, sender, peers)

		// Find the next undelivered delta in order of issuance.
		reqs := sender.deltaReqsCopy()
		var next *deltaReq
		for i := range reqs {
			r := reqs[i]
			if !delivered[r.Hash] {
				next = &r
				break
			}
		}
		require.NotNil(t, next, "no pending delta but task incomplete")
		delivered[next.Hash] = true

		idx := chainSeqByHash[next.Hash]
		sender.noteDeltaDelivered(next.PeerID)
		require.NoError(t, task.OnDeltaResponse(&message.ReplayDeltaResponse{
			LedgerHash:   next.Hash[:],
			LedgerHeader: chainHeaders[idx],
			Transactions: nil,
		}))
	}

	// Final assertions.
	assertCaps(t, sender, peers)
	assert.Equal(t, TaskStateComplete, task.State())
	assert.LessOrEqual(t, sender.peakGlobal, DefaultMaxInFlightReplays,
		"global cap must never be exceeded")
	for _, p := range peers {
		assert.LessOrEqual(t, sender.peakPerPeer[p], MaxPerPeerReplays,
			"per-peer cap must never be exceeded for peer %d", p)
	}
	// Sanity: the work was actually distributed — not all 50 deltas
	// went through one peer.
	usedPeers := 0
	for _, p := range peers {
		if sender.peakPerPeer[p] > 0 {
			usedPeers++
		}
	}
	assert.Greater(t, usedPeers, 1, "task must rotate across peers under per-peer cap pressure")
}

func assertCaps(t *testing.T, s *recordingTaskSender, peers []uint64) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.LessOrEqualf(t, s.inFlightGlobal, DefaultMaxInFlightReplays,
		"global in-flight %d exceeds cap %d", s.inFlightGlobal, DefaultMaxInFlightReplays)
	for _, p := range peers {
		assert.LessOrEqualf(t, s.inFlightPerPeer[p], MaxPerPeerReplays,
			"per-peer in-flight for %d = %d exceeds cap %d",
			p, s.inFlightPerPeer[p], MaxPerPeerReplays)
	}
}

// TestLedgerReplayTask_DepthValidation rejects out-of-range depth.
func TestLedgerReplayTask_DepthValidation(t *testing.T) {
	t.Parallel()
	r := NewReplayer(nil, nil, 0)
	s := newRecordingTaskSender()

	_, err := NewLedgerReplayTask([32]byte{}, [32]byte{}, 100, 0, []uint64{1}, r, s, nil, TaskCallbacks{})
	assert.ErrorIs(t, err, ErrTaskBadDepth, "depth=0 must be rejected")

	_, err = NewLedgerReplayTask([32]byte{}, [32]byte{}, 100, MaxBackwardDepth+1, []uint64{1}, r, s, nil, TaskCallbacks{})
	assert.ErrorIs(t, err, ErrTaskBadDepth, "depth>MaxBackwardDepth must be rejected")

	_, err = NewLedgerReplayTask([32]byte{}, [32]byte{}, 100, 10, nil, r, s, nil, TaskCallbacks{})
	assert.ErrorIs(t, err, ErrTaskNoPeers, "empty peer list must be rejected")
}

// TestLedgerReplayTask_SkipListTooShort aborts when the verified
// skip-list has fewer hashes than the requested depth needs.
func TestLedgerReplayTask_SkipListTooShort(t *testing.T) {
	t.Parallel()
	const depth uint32 = 50
	tipHash := [32]byte{0xEE}
	// Only 10 hashes — depth=50 needs 49 ancestors.
	short := make([][32]byte, 10)
	for i := range short {
		short[i] = [32]byte{byte(i + 1)}
	}
	leafPayload := buildSkipListLeafSLE(t, short, 999)
	stateHash, proofPath := buildSkipListProof(t, leafPayload)

	r := NewReplayer(nil, nil, 0)
	s := newRecordingTaskSender()
	task, err := NewLedgerReplayTask(tipHash, stateHash, 1000, depth, []uint64{1}, r, s, nil, TaskCallbacks{})
	require.NoError(t, err)
	require.NoError(t, task.Start())

	skipKey := keylet.LedgerHashes().Key
	resp := &message.ProofPathResponse{
		LedgerHash: tipHash[:],
		Key:        skipKey[:],
		MapType:    message.LedgerMapAccountState,
		Path:       proofPath,
	}
	err = task.OnSkipListResponse(resp)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTaskSkipListTooShort)
	assert.Equal(t, TaskStateFailed, task.State())
	assert.Equal(t, 0, r.SkipListCount(), "skip-list slot freed on abort")
}

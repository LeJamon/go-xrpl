package adaptor

import (
	"context"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/internal/ledger/inbound/inboundtest"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/LeJamon/goXRPLd/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingSender captures the calls the router makes against the
// NetworkSender interface. The router is the unit under test, so the
// sender is the natural seam to inspect for "preferred replay-delta vs
// fell back to legacy" assertions.
type recordingSender struct {
	noopSender
	mu               sync.Mutex
	replayDeltaCalls []replayDeltaCall
	legacyBaseCalls  []legacyBaseCall
	// peerSupportsReplay controls the handshake-feature answer. Defaults
	// to true so existing tests continue to exercise the "peer advertises
	// ledger-replay" path without extra setup; tests that want to cover
	// the no-support fallback flip this to false.
	peerSupportsReplay bool

	// availableReplayPeers is the pool returned by
	// ReplayCapablePeersExcluding. Empty by default — tests exercising
	// the R4.8 peer-swap retry loop populate this to drive the
	// rotation path.
	availableReplayPeers []uint64
}

type replayDeltaCall struct {
	peerID uint64
	hash   [32]byte
}

type legacyBaseCall struct {
	peerID uint64
	hash   [32]byte
	seq    uint32
}

func (s *recordingSender) RequestReplayDelta(peerID uint64, hash [32]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replayDeltaCalls = append(s.replayDeltaCalls, replayDeltaCall{peerID: peerID, hash: hash})
	return nil
}

func (s *recordingSender) RequestLedgerBaseFromPeer(peerID uint64, hash [32]byte, seq uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.legacyBaseCalls = append(s.legacyBaseCalls, legacyBaseCall{peerID: peerID, hash: hash, seq: seq})
	return nil
}

func (s *recordingSender) replayCalls() []replayDeltaCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]replayDeltaCall, len(s.replayDeltaCalls))
	copy(out, s.replayDeltaCalls)
	return out
}

func (s *recordingSender) legacyCalls() []legacyBaseCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]legacyBaseCall, len(s.legacyBaseCalls))
	copy(out, s.legacyBaseCalls)
	return out
}

// PeerSupportsReplay returns the configured handshake-feature answer.
// Overrides the noopSender default (false) so tests that set up the
// recordingSender without extra configuration still exercise the
// replay-delta-preferred path.
func (s *recordingSender) PeerSupportsReplay(uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peerSupportsReplay
}

// ReplayCapablePeersExcluding: tests may pre-populate availableReplayPeers
// to exercise the peer-swap retry loop. When empty the router's rotate
// path falls straight through to the legacy fallback, which is what the
// pre-R4.8 tests implicitly assume.
func (s *recordingSender) ReplayCapablePeersExcluding(excluded []uint64, max int) []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.availableReplayPeers) == 0 {
		return nil
	}
	excludedSet := make(map[uint64]struct{}, len(excluded))
	for _, id := range excluded {
		excludedSet[id] = struct{}{}
	}
	out := make([]uint64, 0, max)
	for _, id := range s.availableReplayPeers {
		if _, skip := excludedSet[id]; skip {
			continue
		}
		out = append(out, id)
		if len(out) >= max {
			break
		}
	}
	return out
}

// newRecordingAdaptor wires a fresh adaptor against the supplied service
// with our recordingSender installed. The validator identity is reused
// from the test helper because the router doesn't need a specific key.
func newRecordingAdaptor(t *testing.T, svc *service.Service) (*Adaptor, *recordingSender) {
	t.Helper()
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	rs := &recordingSender{peerSupportsReplay: true}
	a := New(Config{
		LedgerService: svc,
		Sender:        rs,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
	return a, rs
}

// vlEncode mirrors internal/tx EncodeVL — duplicated so the test stays
// self-contained.
func vlEncode(length int) []byte {
	switch {
	case length <= 192:
		return []byte{byte(length)}
	case length <= 12480:
		l := length - 193
		return []byte{byte((l >> 8) + 193), byte(l & 0xFF)}
	default:
		l := length - 12481
		return []byte{byte((l >> 16) + 241), byte((l >> 8) & 0xFF), byte(l & 0xFF)}
	}
}

// metaBlob serializes a tiny metadata STObject so the inbound parser
// can extract sfTransactionIndex.
func metaBlob(t *testing.T, txIndex uint32) []byte {
	t.Helper()
	hexStr, err := binarycodec.Encode(map[string]any{
		"TransactionResult": "tesSUCCESS",
		"TransactionIndex":  txIndex,
	})
	require.NoError(t, err)
	out, err := hex.DecodeString(hexStr)
	require.NoError(t, err)
	return out
}

// txWithMetaBlob assembles VL(tx) + VL(metadata) and computes the
// canonical XRPL tx ID (used as the SHAMap key on insert).
func txWithMetaBlob(t *testing.T, txBytes []byte, txIndex uint32) (blob []byte, txID [32]byte) {
	t.Helper()
	meta := metaBlob(t, txIndex)
	txID = common.Sha512Half(protocol.HashPrefixTransactionID[:], txBytes)
	blob = append(blob, vlEncode(len(txBytes))...)
	blob = append(blob, txBytes...)
	blob = append(blob, vlEncode(len(meta))...)
	blob = append(blob, meta...)
	return blob, txID
}

// buildResponseAgainstParent constructs a valid mtREPLAY_DELTA_RESPONSE
// that descends from `parent`. Uses close times well past the XRPL epoch
// so AddRaw / DeserializeHeader round-trip cleanly.
func buildResponseAgainstParent(t *testing.T, svc *service.Service, txCount int) (*message.ReplayDeltaResponse, [32]byte, uint32) {
	t.Helper()
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	blobs := make([][]byte, 0, txCount)
	ids := make([][32]byte, 0, txCount)
	for i := 0; i < txCount; i++ {
		txBytes := []byte{0x10, 0x20, 0x30, byte(i), 0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x01}
		blob, id := txWithMetaBlob(t, txBytes, uint32(i))
		blobs = append(blobs, blob)
		ids = append(ids, id)
	}

	txMap, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	for i := range blobs {
		require.NoError(t, txMap.PutWithNodeType(ids[i], blobs[i], shamap.NodeTypeTransactionWithMeta))
	}
	require.NoError(t, txMap.SetImmutable())
	txRoot, err := txMap.Hash()
	require.NoError(t, err)

	parentHash := parent.Hash()
	closeTime := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	hdr := header.LedgerHeader{
		LedgerIndex:         parent.Sequence() + 1,
		ParentHash:          parentHash,
		ParentCloseTime:     closeTime,
		CloseTime:           closeTime.Add(10 * time.Second),
		CloseTimeResolution: parent.Header().CloseTimeResolution,
		Drops:               parent.TotalDrops(),
		TxHash:              txRoot,
		AccountHash:         parent.Header().AccountHash,
	}
	bytesHdr, err := header.AddRaw(hdr, false)
	require.NoError(t, err)
	parsed, err := header.DeserializeHeader(bytesHdr, false)
	require.NoError(t, err)
	expected := genesis.CalculateLedgerHash(*parsed)

	resp := &message.ReplayDeltaResponse{
		LedgerHash:   expected[:],
		LedgerHeader: bytesHdr,
		Transactions: blobs,
	}
	return resp, expected, hdr.LedgerIndex
}

// buildEmptyClosedSuccessorResponse constructs a wire response carrying
// a real Close()-generated header for the empty-tx successor of the
// service's current closed ledger. This exercises the same Close() path
// (skip-list update, drops accounting, hash derivation) that Apply
// runs, so the response's AccountHash / TxHash / Hash all match what
// Apply will recompute. Use this rather than a hand-built header when
// you want the apply step to succeed end-to-end.
func buildEmptyClosedSuccessorResponse(t *testing.T, svc *service.Service) (*message.ReplayDeltaResponse, [32]byte, uint32) {
	t.Helper()
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	closeTime := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	open, err := ledger.NewOpen(parent, closeTime)
	require.NoError(t, err)
	require.NoError(t, open.Close(closeTime, 0))
	hdr := open.Header()

	hdrBytes, err := header.AddRaw(hdr, false)
	require.NoError(t, err)

	resp := &message.ReplayDeltaResponse{
		LedgerHash:   hdr.Hash[:],
		LedgerHeader: hdrBytes,
		Transactions: nil,
	}
	return resp, hdr.Hash, hdr.LedgerIndex
}

// makeRouter wires a router against a real adaptor + recording sender,
// returning the pieces tests will poke and inspect.
func makeRouter(t *testing.T) (*Router, *Adaptor, *recordingSender, *service.Service) {
	t.Helper()
	svc := newTestLedgerService(t)
	a, rs := newRecordingAdaptor(t, svc)
	inbox := make(chan *peermanagement.InboundMessage, 8)
	r := NewRouter(nil, a, nil, inbox)
	return r, a, rs, svc
}

// TestRouter_PrefersReplayDelta verifies that when a parent ledger is
// available the router issues mtREPLAY_DELTA_REQUEST instead of the
// legacy mtGET_LEDGER.
func TestRouter_PrefersReplayDelta(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	target := [32]byte{0xAB}
	r.startLedgerAcquisition(parent.Sequence()+1, target, 7)

	calls := rs.replayCalls()
	require.Len(t, calls, 1, "router must prefer replay-delta when parent is local")
	assert.Equal(t, uint64(7), calls[0].peerID)
	assert.Equal(t, target, calls[0].hash)
	assert.Empty(t, rs.legacyCalls(), "legacy path must not run when replay-delta succeeds at issue")
	assert.True(t, r.replayer.Has(target), "coordinator must hold an in-flight acquisition for the target hash")
	assert.Equal(t, 1, r.replayer.Count())
}

// TestRouter_NoParent_FallsBackToLegacy verifies the fallback when the
// parent ledger isn't locally available — startLedgerAcquisition should
// take the legacy mtGET_LEDGER path.
func TestRouter_NoParent_FallsBackToLegacy(t *testing.T) {
	r, _, rs, _ := makeRouter(t)

	// Ask for a ledger far in the future — we have no parent at seq-1.
	target := [32]byte{0xAB}
	r.startLedgerAcquisition(99999, target, 7)

	assert.Empty(t, rs.replayCalls(), "no parent → no replay-delta request")
	calls := rs.legacyCalls()
	require.Len(t, calls, 1, "legacy fallback must run")
	assert.Equal(t, uint32(99999), calls[0].seq)
	assert.Equal(t, target, calls[0].hash)
	assert.Equal(t, uint64(7), calls[0].peerID)
	assert.NotNil(t, r.inboundLedger)
	assert.Equal(t, 0, r.replayer.Count(), "no replay-delta acquisition when no parent is available")
}

// TestRouter_PeerDoesNotSupportReplay_FallsBackToLegacy verifies that
// when the peer did NOT advertise the ledger-replay protocol feature
// during handshake, the router takes the legacy mtGET_LEDGER path even
// if we have a local parent. Mirrors the policy behind
// LedgerDeltaAcquire::trigger skipping peers without
// ProtocolFeature::LedgerReplay.
func TestRouter_PeerDoesNotSupportReplay_FallsBackToLegacy(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	// Peer didn't advertise ledger-replay in its handshake headers.
	rs.mu.Lock()
	rs.peerSupportsReplay = false
	rs.mu.Unlock()

	target := [32]byte{0xCD}
	r.startLedgerAcquisition(parent.Sequence()+1, target, 11)

	assert.Empty(t, rs.replayCalls(), "must not issue replay-delta to peer that doesn't support it")
	calls := rs.legacyCalls()
	require.Len(t, calls, 1, "legacy fallback must run")
	assert.Equal(t, target, calls[0].hash)
	assert.Equal(t, uint64(11), calls[0].peerID)
	assert.Equal(t, 0, r.replayer.Count(), "replay-delta must not be armed")
	assert.NotNil(t, r.inboundLedger, "legacy acquisition must be armed")
}

// TestRouter_ReplayDeltaResponse_Routed verifies that an inbound
// mtREPLAY_DELTA_RESPONSE for the active acquisition reaches the
// InboundReplayDelta verifier, runs the post-state derivation in
// Apply(), and adopts the resulting ledger.
//
// We use an empty tx set so Apply trivially succeeds without needing
// real, parseable tx blobs — the goal here is to verify routing +
// post-state adoption wiring, not engine semantics. The Apply path
// itself is exhaustively covered in
// internal/ledger/inbound/replay_delta_apply_test.go.
func TestRouter_ReplayDeltaResponse_Routed(t *testing.T) {
	r, a, _, svc := makeRouter(t)
	resp, expectedHash, seq := buildEmptyClosedSuccessorResponse(t, svc)

	// Arm an acquisition for the same hash.
	parent := svc.GetClosedLedger()
	require.NoError(t, r.startReplayDeltaAcquisition(seq, expectedHash, 7, parent))

	payload, err := message.Encode(resp)
	require.NoError(t, err)

	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeReplayDeltaResponse),
		Payload: payload,
	})

	assert.Equal(t, 0, r.replayer.Count(), "successful adoption must clear the active acquisition")
	// Service should have advanced its closed ledger to the verified seq.
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	assert.Equal(t, expectedHash, closed.Hash())
	// And the operating mode should be at least Tracking.
	assert.True(t, a.GetOperatingMode() >= consensus.OpModeTracking,
		"adoption should advance to Tracking or higher (was %s)", a.GetOperatingMode())
}

// TestRouter_FallsBackToLegacyOnReplayFailure verifies that a malformed
// response causes the router to abandon the replay-delta acquisition and
// re-issue the request via the legacy path.
func TestRouter_FallsBackToLegacyOnReplayFailure(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	target := [32]byte{0xAB}
	require.NoError(t, r.startReplayDeltaAcquisition(parent.Sequence()+1, target, 7, parent))

	// Cook a response that matches the active hash but carries a
	// peer-signaled error. The verifier rejects it and the router
	// must re-arm via the legacy path.
	bad := &message.ReplayDeltaResponse{
		LedgerHash: target[:],
		Error:      message.ReplyErrorNoLedger,
	}
	payload, err := message.Encode(bad)
	require.NoError(t, err)

	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeReplayDeltaResponse),
		Payload: payload,
	})

	assert.Equal(t, 0, r.replayer.Count(), "failed verification must clear the replay state")
	require.Len(t, rs.legacyCalls(), 1, "router must fall back to the legacy path")
	assert.Equal(t, target, rs.legacyCalls()[0].hash)
	assert.NotNil(t, r.inboundLedger)
}

// TestRouter_MaintenanceTick_TimeoutFallback verifies that a stalled
// replay-delta acquisition gets timed out and re-issued via the legacy
// path by the periodic maintenance tick.
func TestRouter_MaintenanceTick_TimeoutFallback(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	parent := svc.GetClosedLedger()

	// Install a fake clock so we can age the acquisition past its timeout
	// without wall-clock waits. Must be set before startReplayDeltaAcquisition
	// so the new ReplayDelta adopts it as its time source.
	clock := inboundtest.NewFakeClock(time.Now())
	r.SetInboundClock(clock)

	target := [32]byte{0xAB}
	require.NoError(t, r.startReplayDeltaAcquisition(parent.Sequence()+1, target, 7, parent))

	// Advance the fake past replayDeltaTimeout (~30s); IsTimedOut reads the
	// same clock via the injected dependency.
	clock.Advance(time.Hour)

	r.maintenanceTick()
	assert.Equal(t, 0, r.replayer.Count(), "tick must clear the timed-out acquisition")
	require.Len(t, rs.legacyCalls(), 1, "tick must re-issue via the legacy path")
}

// TestRouter_ReplayDeltaApply_AdoptsDerivedLedger verifies that the
// router runs Apply (not just GotResponse) and adopts the
// post-state-derived ledger. Specifically: the adopted ledger's
// StateMapHash must differ from the parent's — the empty-tx successor
// has an updated state map (LedgerHashes skip-list), so a router
// path that cheaply forwarded the parent's state map would fail this
// invariant.
func TestRouter_ReplayDeltaApply_AdoptsDerivedLedger(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	// Build a genesis successor at seq 2 — there's no skip-list update
	// for prevIndex=1 (genesis ledger), so the state map root would be
	// unchanged. Step the service forward one ledger first so we
	// actually exercise the skip-list mutation Close() runs.
	_, err := svc.AcceptLedger(context.TODO())
	require.NoError(t, err)

	resp, expectedHash, seq := buildEmptyClosedSuccessorResponse(t, svc)
	parent = svc.GetClosedLedger()
	parentState, err := parent.StateMapHash()
	require.NoError(t, err)

	require.NoError(t, r.startReplayDeltaAcquisition(seq, expectedHash, 7, parent))

	payload, err := message.Encode(resp)
	require.NoError(t, err)

	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeReplayDeltaResponse),
		Payload: payload,
	})

	require.Equal(t, 0, r.replayer.Count(), "successful adoption must clear the active acquisition")
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	assert.Equal(t, expectedHash, closed.Hash(),
		"adopted ledger must be the verified successor")
	closedState, err := closed.StateMapHash()
	require.NoError(t, err)
	assert.NotEqual(t, parentState, closedState,
		"adopted state map must reflect the post-Close skip-list update — proves Apply ran")
}

// TestRouter_ReplayDeltaApply_StateMismatchFallsBack verifies that
// when the response carries a tx-map root that GotResponse accepts
// but a state-map root that Apply rejects (post-state derivation
// disagrees with the header), the router abandons the replay-delta
// acquisition and re-issues via the legacy mtGET_LEDGER path. This is
// the safety net: a peer that lies about AccountHash, or our own
// engine diverging from rippled, must NOT silently produce a corrupt
// closed ledger.
func TestRouter_ReplayDeltaApply_StateMismatchFallsBack(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	// Build a real Close-derived empty-tx response, then tamper with
	// AccountHash and re-derive the byte-level header hash so
	// GotResponse still passes (header hash + tx-map root remain
	// internally consistent). Apply will then catch the state-map
	// divergence and fall back.
	resp, _, _ := buildEmptyClosedSuccessorResponse(t, svc)
	parsed, err := header.DeserializeHeader(resp.LedgerHeader, false)
	require.NoError(t, err)
	parsed.AccountHash[0] ^= 0xFF
	hdrBytes, err := header.AddRaw(*parsed, false)
	require.NoError(t, err)
	tampered := common.Sha512Half(protocol.HashPrefixLedgerMaster.Bytes(), hdrBytes)
	resp.LedgerHash = tampered[:]
	resp.LedgerHeader = hdrBytes

	require.NoError(t, r.startReplayDeltaAcquisition(parent.Sequence()+1, tampered, 7, parent))

	payload, err := message.Encode(resp)
	require.NoError(t, err)

	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeReplayDeltaResponse),
		Payload: payload,
	})

	assert.Equal(t, 0, r.replayer.Count(),
		"failed Apply must clear the replay state")
	require.Len(t, rs.legacyCalls(), 1,
		"router must fall back to the legacy path on state-map mismatch")
	assert.Equal(t, tampered, rs.legacyCalls()[0].hash)
	assert.NotNil(t, r.inboundLedger, "legacy acquisition must be armed for retry")
}

// TestRouter_ConcurrentAcquisitions_RouteCorrectly verifies that two
// in-flight replay-delta acquisitions for distinct ledger hashes route
// their responses independently. Reversing the response delivery order
// must not cross-pollinate state: the response for hash A advances
// ONLY acquisition A, and the response for hash B advances ONLY
// acquisition B. This is the headline coordinator guarantee from
// Gap 7 — proof that the single-slot field removal behaves correctly
// under concurrency.
func TestRouter_ConcurrentAcquisitions_RouteCorrectly(t *testing.T) {
	r, _, _, svc := makeRouter(t)

	// Two distinct targets against the SAME parent (seq N+1). For the
	// happy-path adoption we use the empty-closed successor helper,
	// which is deterministic; the SECOND acquisition is armed against
	// a synthetic hash so we can watch its state stay at WantBase
	// while the first completes.
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	resp, realHash, seq := buildEmptyClosedSuccessorResponse(t, svc)
	otherHash := [32]byte{0xDE, 0xAD, 0xBE, 0xEF}

	require.NoError(t, r.startReplayDeltaAcquisition(seq, realHash, 7, parent))
	require.NoError(t, r.startReplayDeltaAcquisition(seq, otherHash, 9, parent))
	require.Equal(t, 2, r.replayer.Count(), "both acquisitions must be in flight")

	// Deliver the response for the SECOND-armed hash first. The router
	// must route by hash, not by insertion order.
	payload, err := message.Encode(resp)
	require.NoError(t, err)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeReplayDeltaResponse),
		Payload: payload,
	})

	// After dispatch: realHash is completed+adopted, otherHash remains
	// in-flight (nobody responded for it).
	assert.False(t, r.replayer.Has(realHash), "successful adoption clears the matching slot")
	assert.True(t, r.replayer.Has(otherHash), "unrelated acquisition must not be cleared")
	assert.Equal(t, 1, r.replayer.Count())

	// Closed ledger advanced to the verified successor.
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	assert.Equal(t, realHash, closed.Hash())
}

// TestRouter_IgnoresUnsolicitedReplayDeltaResponse verifies that a
// response with no matching active acquisition is silently dropped.
// Mirrors rippled's behavior of dropping unsolicited replies.
func TestRouter_IgnoresUnsolicitedReplayDeltaResponse(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	resp, _, _ := buildResponseAgainstParent(t, svc, 1)
	payload, err := message.Encode(resp)
	require.NoError(t, err)

	// No active acquisition yet.
	require.Equal(t, 0, r.replayer.Count())

	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeReplayDeltaResponse),
		Payload: payload,
	})

	assert.Equal(t, 0, r.replayer.Count(), "unsolicited response must not arm the verifier")
}

// buildSuccessorAgainstParent is the same close-and-serialize dance as
// buildEmptyClosedSuccessorResponse, but against an arbitrary parent
// Ledger object rather than svc.GetClosedLedger(). This lets the F6
// cascade test construct a two-link chain (N+1, N+2) where N+1 is
// held in-memory rather than installed in svc history — exactly the
// situation the router must handle when a replay-delta for N+2
// arrives ahead of N+1.
func buildSuccessorAgainstParent(t *testing.T, parent *ledger.Ledger) (*message.ReplayDeltaResponse, *ledger.Ledger, [32]byte, uint32) {
	t.Helper()
	// Offset by seq so each ledger in a chain has a distinct close time
	// — the hash derivation consumes close time, so identical values
	// would make chained successors collide.
	closeTime := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC).Add(time.Duration(parent.Sequence()) * time.Second)
	open, err := ledger.NewOpen(parent, closeTime)
	require.NoError(t, err)
	require.NoError(t, open.Close(closeTime, 0))
	hdr := open.Header()

	hdrBytes, err := header.AddRaw(hdr, false)
	require.NoError(t, err)

	resp := &message.ReplayDeltaResponse{
		LedgerHash:   hdr.Hash[:],
		LedgerHeader: hdrBytes,
		Transactions: nil,
	}
	return resp, open, hdr.Hash, hdr.LedgerIndex
}

// TestRouter_ReplayDelta_CascadesOutOfOrderArrival is the F6 integration
// test. It wires the replay-delta path end-to-end (acquisition →
// response → Apply → adopt) and drives an OUT-OF-ORDER arrival: the
// seq N+2 response reaches the router before the seq N+1 response.
// The router is expected to route both through SubmitHeldAdoption,
// which stashes N+2 until N+1 lands and then cascade-promotes it.
//
// Without the F6 wiring this test would fail at two points:
//  1. On delivery of the N+2 response, AdoptLedgerWithState would
//     return an error (parent-hash mismatch against the then-current
//     closedLedger at seq N) and the derived ledger would be lost —
//     svc's closedLedger never advances past N+1 even after N+1
//     arrives.
//  2. svc.GetLedgerByHash(N+2) would stay error-returning because no
//     second acquisition round retries N+2.
func TestRouter_ReplayDelta_CascadesOutOfOrderArrival(t *testing.T) {
	r, a, _, svc := makeRouter(t)

	// Step svc past genesis once so we have a real N-at-seq-2 parent
	// whose hash is consistent with a Close()-derived successor at
	// seq 3 (the genesis ledger's skip-list is a special case — same
	// reasoning as TestRouter_ReplayDeltaApply_AdoptsDerivedLedger).
	_, err := svc.AcceptLedger(context.TODO())
	require.NoError(t, err)

	parentN := svc.GetClosedLedger()
	require.NotNil(t, parentN)
	parentSeq := parentN.Sequence()

	// Build the N+1 response against parentN AND keep the N+1 ledger
	// object in-memory so we can chain N+2 off of it. N+1 is NOT
	// installed in svc history yet — the whole point of this test is
	// to deliver N+2 before N+1 is adopted.
	respN1, ledgerN1, hashN1, seqN1 := buildSuccessorAgainstParent(t, parentN)
	respN2, _, hashN2, seqN2 := buildSuccessorAgainstParent(t, ledgerN1)
	require.Equal(t, parentSeq+1, seqN1)
	require.Equal(t, parentSeq+2, seqN2)
	require.NotEqual(t, hashN1, hashN2, "chained successors must have distinct hashes")

	// --- Out-of-order delivery: N+2 arrives first ---
	//
	// Arm an acquisition for N+2 with ledgerN1 as the parent object.
	// startReplayDeltaAcquisition takes the parent Ledger directly, so
	// we can supply an in-memory N+1 that svc doesn't yet know about.
	// Apply will use it to derive N+2's post-state; the router then
	// hands the derived ledger to SubmitHeldAdoption, which must stash
	// because svc has no N+1 in ledgerHistory.
	require.NoError(t, r.startReplayDeltaAcquisition(seqN2, hashN2, 7, ledgerN1))
	payloadN2, err := message.Encode(respN2)
	require.NoError(t, err)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeReplayDeltaResponse),
		Payload: payloadN2,
	})

	// N+2's acquisition has completed (verified + applied), but the
	// adopt was stashed — not persisted to history.
	require.Equal(t, 0, r.replayer.Count(),
		"N+2 acquisition must be cleared once the response is verified and adopt routed")
	_, err = svc.GetLedgerByHash(hashN2)
	require.Error(t, err,
		"N+2 must NOT be in svc history yet — parent seq N+1 has not arrived, so SubmitHeldAdoption must stash")
	assert.Equal(t, parentSeq, svc.GetClosedLedger().Sequence(),
		"closedLedger must not advance when the replay-delta is stashed")

	// --- In-order arrival: N+1 lands and cascades N+2 ---
	//
	// Arm and deliver N+1. Its ParentHash matches parentN.Hash()
	// (parentN IS in svc history), so SubmitHeldAdoption fast-paths
	// into the adopt, which then runs cascadeHeldAdoptionsLocked and
	// finds the N+2 entry keyed by seqN1. N+2's ParentHash ==
	// hashN1, so the cascade promotes it.
	require.NoError(t, r.startReplayDeltaAcquisition(seqN1, hashN1, 9, parentN))
	payloadN1, err := message.Encode(respN1)
	require.NoError(t, err)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  9,
		Type:    uint16(message.TypeReplayDeltaResponse),
		Payload: payloadN1,
	})

	require.Equal(t, 0, r.replayer.Count(),
		"N+1 adoption should clear the acquisition")

	// Both N+1 AND N+2 must be in svc history. This is the F6
	// contract: a single AdoptLedgerWithState at the parent seq
	// drains every held child whose ParentHash matches.
	gotN1, err := svc.GetLedgerByHash(hashN1)
	require.NoError(t, err, "N+1 must be adopted by its own response")
	assert.Equal(t, hashN1, gotN1.Hash())

	gotN2, err := svc.GetLedgerByHash(hashN2)
	require.NoError(t, err,
		"N+2 must be cascade-adopted when its awaited parent N+1 lands — this fails without F6 wiring")
	assert.Equal(t, hashN2, gotN2.Hash())

	// closedLedger must advance to the cascade tip (N+2), not stop
	// at N+1. This is the observable signal that the cascade ran
	// end-to-end through the router — half-cascades (stop at N+1)
	// leave the node visibly behind until the inbound loop retries.
	assert.Equal(t, hashN2, svc.GetClosedLedger().Hash(),
		"closedLedger must advance to the cascade tip N+2")
	assert.Equal(t, seqN2, svc.GetClosedLedger().Sequence())

	// Cascade-driven adoption still advances operating mode to
	// Tracking. Router's adoptVerifiedLedger only runs the
	// SetOperatingMode branch for the outer ledger (N+1); N+2 is
	// promoted from inside the service and the router never sees it.
	// But the N+1 adoption already did the Tracking transition, so
	// the final state must be >= Tracking regardless.
	assert.True(t, a.GetOperatingMode() >= consensus.OpModeTracking,
		"operating mode must be at least Tracking after cascade (was %s)", a.GetOperatingMode())
}

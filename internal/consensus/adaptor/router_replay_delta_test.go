package adaptor

import (
	"context"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound/inboundtest"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingSender captures the calls the router makes against its acquisition
// network. The router is the unit under test, so the
// sender is the natural seam to inspect for "preferred replay-delta vs
// fell back to legacy" assertions.
type recordingSender struct {
	noopSender
	mu               sync.Mutex
	replayDeltaCalls []replayDeltaCall
	replayDeltaErr   error
	legacyBaseCalls  []legacyBaseCall
	legacyBaseErr    error
	legacyBaseErrs   map[uint64]error
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
	acquisitionPeers     []uint64
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
	return s.replayDeltaErr
}

func (s *recordingSender) RequestLedgerBaseFromPeer(peerID uint64, hash [32]byte, seq uint32, indirect bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.legacyBaseCalls = append(s.legacyBaseCalls, legacyBaseCall{peerID: peerID, hash: hash, seq: seq})
	if err, ok := s.legacyBaseErrs[peerID]; ok {
		return err
	}
	return s.legacyBaseErr
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

func (s *recordingSender) SelectLedgerPeers(_ [32]byte, _ uint32, excluded []uint64, max int) []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	excludedSet := make(map[uint64]struct{}, len(excluded))
	for _, id := range excluded {
		excludedSet[id] = struct{}{}
	}
	out := make([]uint64, 0, max)
	for _, id := range s.acquisitionPeers {
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
	txID = sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), txBytes)
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
	for i := range txCount {
		txBytes := []byte{0x10, 0x20, 0x30, byte(i), 0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x01}
		blob, id := txWithMetaBlob(t, txBytes, uint32(i))
		blobs = append(blobs, blob)
		ids = append(ids, id)
	}

	txMap := shamap.New(shamap.TypeTransaction)
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
	bytesHdr := header.AddRaw(hdr, false)
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

	hdrBytes := header.AddRaw(hdr, false)

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
	r := newTestRouter(nil, a, inbox)
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
	assert.NotNil(t, r.fetchTracker.Find(target))
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
	assert.NotNil(t, r.fetchTracker.Find(target), "legacy acquisition must be armed")
}

// TestRouter_ReplayDeltaResponse_Routed verifies that an inbound
// mtREPLAY_DELTA_RESPONSE for the active acquisition reaches the
// InboundReplayDelta verifier, runs the post-state derivation in
// Apply(), and adopts the resulting ledger.
//
// We use an empty tx set so Apply trivially succeeds without needing
// real, parseable tx blobs — the goal here is to verify routing +
// post-state storage wiring, not engine semantics. The Apply path
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
		Type:    message.TypeReplayDeltaResponse,
		Payload: payload,
	})

	assert.Equal(t, 0, r.replayer.Count(), "successful storage must clear the active acquisition")
	stored, err := svc.GetLedgerByHash(expectedHash)
	require.NoError(t, err)
	assert.Equal(t, seq, stored.Sequence())
	assert.NotEqual(t, expectedHash, svc.GetClosedLedger().Hash())
	assert.Equal(t, consensus.OpModeDisconnected, a.GetOperatingMode())
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
		Type:    message.TypeReplayDeltaResponse,
		Payload: payload,
	})

	assert.Equal(t, 0, r.replayer.Count(), "failed verification must clear the replay state")
	require.Len(t, rs.legacyCalls(), 1, "router must fall back to the legacy path")
	assert.Equal(t, target, rs.legacyCalls()[0].hash)
	assert.NotNil(t, r.fetchTracker.Find(target))
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

// TestRouter_ReplayDeltaApplyStoresDerivedLedger verifies that the
// router runs Apply (not just GotResponse) and stores the
// post-state-derived ledger. Specifically: the stored ledger's
// StateMapHash must differ from the parent's — the empty-tx successor
// has an updated state map (LedgerHashes skip-list), so a router
// path that cheaply forwarded the parent's state map would fail this
// invariant.
func TestRouter_ReplayDeltaApplyStoresDerivedLedger(t *testing.T) {
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
		Type:    message.TypeReplayDeltaResponse,
		Payload: payload,
	})

	require.Equal(t, 0, r.replayer.Count(), "successful storage must clear the active acquisition")
	stored, err := svc.GetLedgerByHash(expectedHash)
	require.NoError(t, err)
	closedState, err := stored.StateMapHash()
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
	hdrBytes := header.AddRaw(*parsed, false)
	tampered := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), hdrBytes)
	resp.LedgerHash = tampered[:]
	resp.LedgerHeader = hdrBytes

	require.NoError(t, r.startReplayDeltaAcquisition(parent.Sequence()+1, tampered, 7, parent))

	payload, err := message.Encode(resp)
	require.NoError(t, err)

	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    message.TypeReplayDeltaResponse,
		Payload: payload,
	})

	assert.Equal(t, 0, r.replayer.Count(),
		"failed Apply must clear the replay state")
	require.Len(t, rs.legacyCalls(), 1,
		"router must fall back to the legacy path on state-map mismatch")
	assert.Equal(t, tampered, rs.legacyCalls()[0].hash)
	assert.NotNil(t, r.fetchTracker.Find(tampered), "legacy acquisition must be armed for retry")
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
		Type:    message.TypeReplayDeltaResponse,
		Payload: payload,
	})

	// After dispatch: realHash is completed+adopted, otherHash remains
	// in-flight (nobody responded for it).
	assert.False(t, r.replayer.Has(realHash), "successful adoption clears the matching slot")
	assert.True(t, r.replayer.Has(otherHash), "unrelated acquisition must not be cleared")
	assert.Equal(t, 1, r.replayer.Count())

	stored, err := svc.GetLedgerByHash(realHash)
	require.NoError(t, err)
	assert.Equal(t, seq, stored.Sequence())
	assert.Equal(t, parent.Hash(), svc.GetClosedLedger().Hash())
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
		Type:    message.TypeReplayDeltaResponse,
		Payload: payload,
	})

	assert.Equal(t, 0, r.replayer.Count(), "unsolicited response must not arm the verifier")
}

// buildSuccessorAgainstParent is the same close-and-serialize dance as
// buildEmptyClosedSuccessorResponse, but against an arbitrary parent
// Ledger object rather than svc.GetClosedLedger(). This lets tests construct a
// two-link chain where N+1 is held in memory while N+2 arrives first.
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

	hdrBytes := header.AddRaw(hdr, false)

	resp := &message.ReplayDeltaResponse{
		LedgerHash:   hdr.Hash[:],
		LedgerHeader: hdrBytes,
		Transactions: nil,
	}
	return resp, open, hdr.Hash, hdr.LedgerIndex
}

func TestRouter_ConsensusRecoveryWalkNotifiesOnlyExactTarget(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
	r.engine = engine
	_, err := svc.AcceptLedger(context.TODO())
	require.NoError(t, err)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	resp1, ledger1, hash1, seq1 := buildSuccessorAgainstParent(t, parent)
	resp2, ledger2, hash2, seq2 := buildSuccessorAgainstParent(t, ledger1)
	r.recordSeqHash(seq1, hash1, parent.Hash(), true)
	r.recordSeqHash(seq2, hash2, hash1, true)
	trackCatchupPeer(r, 7, seq2)

	require.NoError(t, a.RequestLedger(consensus.LedgerID(hash2)))
	require.Equal(t, consensusRecovery{targetHash: hash2, stepHash: hash1}, r.consensusRecovery)
	require.Equal(t, []replayDeltaCall{{peerID: 7, hash: hash1}}, sender.replayCalls())

	payload1, err := message.Encode(resp1)
	require.NoError(t, err)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID: 7, Type: message.TypeReplayDeltaResponse, Payload: payload1,
	})
	require.Empty(t, engine.getLedgers())
	require.Equal(t, consensusRecovery{
		targetHash: hash2,
		stepHash:   hash2,
		anchorHash: hash1,
		anchorSeq:  seq1,
	}, r.consensusRecovery)
	require.Equal(t, []replayDeltaCall{{peerID: 7, hash: hash1}, {peerID: 7, hash: hash2}}, sender.replayCalls())

	payload2, err := message.Encode(resp2)
	require.NoError(t, err)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID: 7, Type: message.TypeReplayDeltaResponse, Payload: payload2,
	})
	require.Equal(t, []consensus.LedgerID{consensus.LedgerID(hash2)}, engine.getLedgers())
	require.Equal(t, consensusRecovery{anchorHash: hash2, anchorSeq: seq2}, r.consensusRecovery)
	require.Equal(t, parent.Sequence(), svc.GetClosedLedgerIndex())

	_, _, hash3, seq3 := buildSuccessorAgainstParent(t, ledger2)
	r.recordSeqHash(seq3, hash3, hash2, true)
	trackCatchupPeer(r, 7, seq3)
	require.NoError(t, a.RequestLedger(consensus.LedgerID(hash3)))
	require.Equal(t, consensusRecovery{
		targetHash: hash3,
		stepHash:   hash3,
		anchorHash: hash2,
		anchorSeq:  seq2,
	}, r.consensusRecovery)
	require.Equal(t, replayDeltaCall{peerID: 7, hash: hash3}, sender.replayCalls()[2])
}

func TestRouter_ConsensusRecoveryTargetChangeKeepsStepStoreOnly(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	engine := &mockEngine{}
	r.engine = engine
	_, err := svc.AcceptLedger(context.TODO())
	require.NoError(t, err)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	resp1, ledger1, hash1, seq1 := buildSuccessorAgainstParent(t, parent)
	_, _, oldTarget, oldTargetSeq := buildSuccessorAgainstParent(t, ledger1)
	r.recordSeqHash(seq1, hash1, parent.Hash(), true)
	r.recordSeqHash(oldTargetSeq, oldTarget, hash1, true)
	trackCatchupPeer(r, 7, oldTargetSeq)
	require.NoError(t, a.RequestLedger(consensus.LedgerID(oldTarget)))

	newTarget := [32]byte{0xE7}
	require.NoError(t, a.RequestLedger(consensus.LedgerID(newTarget)))
	require.Equal(t, consensusRecovery{targetHash: newTarget, stepHash: hash1}, r.consensusRecovery)
	legacy := sender.legacyCalls()
	require.Empty(t, legacy)

	payload, err := message.Encode(resp1)
	require.NoError(t, err)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID: 7, Type: message.TypeReplayDeltaResponse, Payload: payload,
	})
	require.Empty(t, engine.getLedgers())
	require.Equal(t, consensusRecovery{
		targetHash: newTarget,
		stepHash:   newTarget,
	}, r.consensusRecovery)
	legacy = sender.legacyCalls()
	require.Len(t, legacy, 1)
	require.Equal(t, newTarget, legacy[0].hash)
}

func TestRouter_ConsensusRecoveryUsesStoredJumpAsReplayAnchor(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.TODO())
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	parent := closed
	var jump *ledger.Ledger
	var hashes [5][32]byte
	for i := range hashes {
		_, next, hash, seq := buildSuccessorAgainstParent(t, parent)
		hashes[i] = hash
		r.recordSeqHash(seq, hash, parent.Hash(), true)
		parent = next
		if i == 2 {
			jump = next
		}
	}
	require.NotNil(t, jump)
	stateMap, err := jump.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := jump.TxMapSnapshot()
	require.NoError(t, err)
	h := jump.Header()
	require.NoError(t, svc.StoreLedgerWithState(t.Context(), &h, stateMap, txMap))
	r.consensusRecovery.anchorHash = jump.Hash()
	r.consensusRecovery.anchorSeq = jump.Sequence()

	targetHash := hashes[len(hashes)-1]
	targetSeq := closed.Sequence() + uint32(len(hashes))
	trackCatchupPeer(r, 7, targetSeq)
	require.NoError(t, a.RequestLedger(consensus.LedgerID(targetHash)))

	require.Equal(t, consensusRecovery{
		targetHash: targetHash,
		stepHash:   hashes[3],
		anchorHash: jump.Hash(),
		anchorSeq:  jump.Sequence(),
	}, r.consensusRecovery)
	require.Equal(t, []replayDeltaCall{{peerID: 7, hash: hashes[3]}}, sender.replayCalls())
	require.Empty(t, sender.legacyCalls())
}

func TestRouter_ConsensusRecoveryReplaysPastSpeculativeGap(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.TODO())
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	parent := closed
	var anchor *ledger.Ledger
	var firstReplayHash [32]byte
	var targetHash [32]byte
	var targetSeq uint32
	for i := 0; i <= maxForwardDeltaGap+1; i++ {
		_, next, hash, seq := buildSuccessorAgainstParent(t, parent)
		r.recordSeqHash(seq, hash, parent.Hash(), true)
		parent = next
		switch i {
		case 0:
			anchor = next
		case 1:
			firstReplayHash = hash
		}
		targetHash = hash
		targetSeq = seq
	}
	require.NotNil(t, anchor)
	stateMap, err := anchor.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := anchor.TxMapSnapshot()
	require.NoError(t, err)
	h := anchor.Header()
	require.NoError(t, svc.StoreLedgerWithState(t.Context(), &h, stateMap, txMap))
	r.consensusRecovery.anchorHash = anchor.Hash()
	r.consensusRecovery.anchorSeq = anchor.Sequence()

	trackCatchupPeer(r, 7, targetSeq)
	require.NoError(t, a.RequestLedger(consensus.LedgerID(targetHash)))

	require.Equal(t, firstReplayHash, r.consensusRecovery.stepHash)
	require.Equal(t, []replayDeltaCall{{peerID: 7, hash: firstReplayHash}}, sender.replayCalls())
	require.Empty(t, sender.legacyCalls())
}

func TestRouter_ConsensusRecoveryBrokenAnchorLinkFallsBackToExactTarget(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.TODO())
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	_, anchor, anchorHash, anchorSeq := buildSuccessorAgainstParent(t, closed)
	_, _, targetHash, targetSeq := buildSuccessorAgainstParent(t, anchor)
	r.recordSeqHash(anchorSeq, anchorHash, closed.Hash(), true)
	r.recordSeqHash(targetSeq, targetHash, [32]byte{0xFF}, true)
	stateMap, err := anchor.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := anchor.TxMapSnapshot()
	require.NoError(t, err)
	h := anchor.Header()
	require.NoError(t, svc.StoreLedgerWithState(t.Context(), &h, stateMap, txMap))
	r.consensusRecovery.anchorHash = anchorHash
	r.consensusRecovery.anchorSeq = anchorSeq
	trackCatchupPeer(r, 7, targetSeq)

	require.NoError(t, a.RequestLedger(consensus.LedgerID(targetHash)))

	require.Empty(t, sender.replayCalls())
	legacy := sender.legacyCalls()
	require.Len(t, legacy, 1)
	require.Equal(t, targetHash, legacy[0].hash)
	require.Equal(t, consensusRecovery{targetHash: targetHash, stepHash: targetHash}, r.consensusRecovery)
}

// Out-of-order replay deltas are independently retrievable by hash and do not
// mutate the canonical frontier.
func TestRouter_ReplayDeltaStoresOutOfOrderArrivalsByHash(t *testing.T) {
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

	require.NoError(t, r.startReplayDeltaAcquisition(seqN2, hashN2, 7, ledgerN1))
	payloadN2, err := message.Encode(respN2)
	require.NoError(t, err)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    message.TypeReplayDeltaResponse,
		Payload: payloadN2,
	})

	require.Equal(t, 0, r.replayer.Count())
	gotN2, err := svc.GetLedgerByHash(hashN2)
	require.NoError(t, err)
	assert.Equal(t, seqN2, gotN2.Sequence())
	assert.Equal(t, parentSeq, svc.GetClosedLedger().Sequence(),
		"acquisition must not advance the closed ledger")

	require.NoError(t, r.startReplayDeltaAcquisition(seqN1, hashN1, 9, parentN))
	payloadN1, err := message.Encode(respN1)
	require.NoError(t, err)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  9,
		Type:    message.TypeReplayDeltaResponse,
		Payload: payloadN1,
	})

	require.Equal(t, 0, r.replayer.Count(),
		"N+1 storage should clear the acquisition")

	gotN1, err := svc.GetLedgerByHash(hashN1)
	require.NoError(t, err)
	assert.Equal(t, hashN1, gotN1.Hash())

	gotN2, err = svc.GetLedgerByHash(hashN2)
	require.NoError(t, err)
	assert.Equal(t, hashN2, gotN2.Hash())
	assert.Equal(t, parentSeq, svc.GetClosedLedger().Sequence())
	assert.Equal(t, consensus.OpModeDisconnected, a.GetOperatingMode())
}

// A verified replay delta already carries a complete derived ledger, so storing
// it does not require a parent-history chase.
func TestRouter_ReplayDeltaStoresWithoutParentChase(t *testing.T) {
	r, _, rs, svc := makeRouter(t)

	// Step svc past genesis so we have a real parent at seq 2 whose
	// hash chains correctly into a Close()-derived successor at seq 3
	// (genesis's skip-list is a special case).
	_, err := svc.AcceptLedger(context.TODO())
	require.NoError(t, err)

	parentN := svc.GetClosedLedger()
	require.NotNil(t, parentN)
	parentSeq := parentN.Sequence()

	// Build a 2-link chain in-memory: N+1 (the gap) and N+2 (the tip).
	// Only parentN is in svc history; N+1 is NOT — that is the gap.
	_, ledgerN1, hashN1, seqN1 := buildSuccessorAgainstParent(t, parentN)
	respN2, _, hashN2, seqN2 := buildSuccessorAgainstParent(t, ledgerN1)
	require.Equal(t, parentSeq+1, seqN1)
	require.Equal(t, parentSeq+2, seqN2)
	require.NotEqual(t, hashN1, hashN2)

	// Tip-only acquisition: arm N+2. ledgerN1 is supplied as the in-
	// memory parent so verification passes — we want to exercise the
	// post-Apply adopt path, not the verifier.
	require.NoError(t, r.startReplayDeltaAcquisition(seqN2, hashN2, 7, ledgerN1))

	// Reset the recording sender so the assertion below sees only the
	// auto-arm, not the manual N+2 arm above.
	rs.mu.Lock()
	rs.replayDeltaCalls = nil
	rs.legacyBaseCalls = nil
	rs.mu.Unlock()

	payloadN2, err := message.Encode(respN2)
	require.NoError(t, err)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    message.TypeReplayDeltaResponse,
		Payload: payloadN2,
	})

	stored, err := svc.GetLedgerByHash(hashN2)
	require.NoError(t, err)
	require.Equal(t, seqN2, stored.Sequence())
	assert.Equal(t, parentSeq, svc.GetClosedLedger().Sequence(),
		"closedLedger must not advance for a stored acquisition")

	totalAutoArmCalls := len(rs.replayCalls()) + len(rs.legacyCalls())
	require.Zero(t, totalAutoArmCalls)
}

func TestRouter_InitialReplaySwitchSchedulesHistoryBackfill(t *testing.T) {
	svc := adg_newNonStandaloneService(t)
	t.Cleanup(svc.Stop)
	a, _ := newRecordingAdaptor(t, svc)
	a.SetOperatingMode(consensus.OpModeConnected)
	engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
	engine.switchHook = func(id consensus.LedgerID) {
		selected, err := a.GetLedger(id)
		require.NoError(t, err)
		require.NoError(t, a.OnLedgerSwitched(selected))
	}
	r := newTestRouter(engine, a, make(chan *peermanagement.InboundMessage, 1))

	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	_, anchor, anchorHash, anchorSeq := buildSuccessorAgainstParent(t, closed)
	anchorState, err := anchor.StateMapSnapshot()
	require.NoError(t, err)
	anchorTx, err := anchor.TxMapSnapshot()
	require.NoError(t, err)
	anchorHeader := anchor.Header()
	require.NoError(t, svc.StoreLedgerWithState(t.Context(), &anchorHeader, anchorState, anchorTx))

	response, _, targetHash, targetSeq := buildSuccessorAgainstParent(t, anchor)
	require.NoError(t, r.startReplayDeltaAcquisition(targetSeq, targetHash, 7, anchor))
	payload, err := message.Encode(response)
	require.NoError(t, err)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    message.TypeReplayDeltaResponse,
		Payload: payload,
	})

	require.Equal(t, []consensus.LedgerID{consensus.LedgerID(targetHash)}, engine.getLedgers())
	r.historyMu.Lock()
	assert.Equal(t, catchupTarget{seq: anchorSeq, hash: anchorHash}, r.history)
	assert.Equal(t, closed.Sequence(), r.historyFloor)
	r.historyMu.Unlock()
}

func TestRouter_LaterPreferredInitialSwitchSchedulesHistoryBackfill(t *testing.T) {
	svc := adg_newNonStandaloneService(t)
	t.Cleanup(svc.Stop)
	a, _ := newRecordingAdaptor(t, svc)
	engine := &mockEngine{switchResult: consensus.LedgerSwitchIrrelevant}
	r := newTestRouter(engine, a, nil)

	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	_, anchor, anchorHash, anchorSeq := buildSuccessorAgainstParent(t, closed)
	_, selected, selectedHash, selectedSeq := buildSuccessorAgainstParent(t, anchor)
	stateMap, err := selected.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := selected.TxMapSnapshot()
	require.NoError(t, err)
	selectedHeader := selected.Header()
	initialCandidate, err := svc.BootstrapLedgerWithState(t.Context(), &selectedHeader, stateMap, txMap)
	require.NoError(t, err)
	require.True(t, initialCandidate)
	require.False(t, r.completeStoredConsensusRecovery(
		selectedSeq,
		selectedHash,
		anchorHash,
		initialCandidate,
	))

	r.historyMu.Lock()
	assert.Equal(t, catchupTarget{}, r.history)
	r.historyMu.Unlock()

	require.NoError(t, a.OnLedgerSwitched(WrapLedger(selected)))

	r.historyMu.Lock()
	assert.Equal(t, catchupTarget{seq: anchorSeq, hash: anchorHash}, r.history)
	assert.Equal(t, closed.Sequence(), r.historyFloor)
	r.historyMu.Unlock()
}

func TestSwitchedLedgerHistoryFloorFallsBackFromForkedClosedLedger(t *testing.T) {
	svc := newTestLedgerService(t)
	validated := svc.GetClosedLedger()
	require.NotNil(t, validated)
	_, canonicalChild, _, _ := buildSuccessorAgainstParent(t, validated)
	_, canonicalGrandchild, _, _ := buildSuccessorAgainstParent(t, canonicalChild)
	_, selected, _, _ := buildSuccessorAgainstParent(t, canonicalGrandchild)

	forkTime := canonicalChild.CloseTime().Add(time.Hour)
	forkedClosed, err := ledger.NewOpen(validated, forkTime)
	require.NoError(t, err)
	require.NoError(t, forkedClosed.Close(forkTime, 0))
	require.NotEqual(t, canonicalChild.Hash(), forkedClosed.Hash())
	_, forkedGrandchild, _, _ := buildSuccessorAgainstParent(t, forkedClosed)
	_, forkedGreatGrandchild, _, _ := buildSuccessorAgainstParent(t, forkedGrandchild)
	_, laterForkedClosed, _, _ := buildSuccessorAgainstParent(t, forkedGreatGrandchild)

	assert.Equal(t, validated.Sequence(), switchedLedgerHistoryFloor(selected, forkedClosed, validated))
	assert.Equal(t, validated.Sequence(), switchedLedgerHistoryFloor(selected, laterForkedClosed, validated))
	assert.Equal(t, validated.Sequence(), switchedLedgerHistoryFloor(canonicalGrandchild, forkedClosed, validated))
	assert.Equal(t, canonicalGrandchild.Sequence()-1,
		switchedLedgerHistoryFloor(canonicalGrandchild, canonicalChild, validated))
	assert.Equal(t, canonicalChild.Sequence()-1, switchedLedgerHistoryFloor(canonicalChild, forkedClosed, validated))
	assert.Equal(t, canonicalChild.Sequence()-1,
		switchedLedgerHistoryFloor(canonicalChild, canonicalChild, validated))
}

// autoArmTarget returns the hash and seq of the single auto-armed
// acquisition. seq is 0 when the replay-delta path was taken (hash-
// keyed on the wire). Caller asserts totalAutoArmCalls == 1 first.
func autoArmTarget(rs *recordingSender) ([32]byte, uint32) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.replayDeltaCalls) == 1 {
		return rs.replayDeltaCalls[0].hash, 0
	}
	if len(rs.legacyBaseCalls) == 1 {
		return rs.legacyBaseCalls[0].hash, rs.legacyBaseCalls[0].seq
	}
	return [32]byte{}, 0
}

// A quorum target beyond the closed ledger must arm acquisition directly from
// the validation lifecycle, even when the ledger is not yet available locally.
//
// Mirrors rippled's LedgerMaster::checkAccept(hash, seq) (LedgerMaster.cpp:917-919),
// which calls app_.getInboundLedgers().acquire(hash, seq, ...) on the
// same condition.
func TestRouter_ValidatedTargetArmsAcquisition(t *testing.T) {
	r, _, rs, svc := makeRouter(t)

	// recordingSender defaults to peerSupportsReplay=true.
	const peerID peermanagement.PeerID = 7
	r.peersMu.Lock()
	r.peerStates[peerID] = &peerLedgerState{
		LedgerSeq: svc.GetClosedLedgerIndex() + 100,
	}
	r.peersMu.Unlock()

	// Hash is arbitrary — we only verify the router asks for it.
	var validatedHash [32]byte
	for i := range validatedHash {
		validatedHash[i] = byte(0xA0 + i%16)
	}
	validatedSeq := svc.GetClosedLedgerIndex() + 5

	r.onLedgerFullyValidated(validatedSeq, validatedHash)

	totalCalls := len(rs.replayCalls()) + len(rs.legacyCalls())
	require.Equal(t, 1, totalCalls,
		"router must auto-arm exactly one acquisition for the validated target")

	armedHash, armedSeq := autoArmTarget(rs)
	assert.Equal(t, validatedHash, armedHash,
		"auto-armed acquisition must target the validated target's hash")
	if armedSeq != 0 {
		assert.Equal(t, validatedSeq, armedSeq,
			"auto-armed acquisition must carry the stashed validation's seq (legacy path)")
	}
}

// Dispatching to peerID=0 would race against the wire layer's per-peer
// routing; peer-status-change handlers drive acquisition once peers
// reconnect.
func TestRouter_ValidatedTargetDoesNotAcquireWithoutPeers(t *testing.T) {
	r, _, rs, svc := makeRouter(t)

	var validatedHash [32]byte
	for i := range validatedHash {
		validatedHash[i] = byte(0x30 + i%16)
	}
	validatedSeq := svc.GetClosedLedgerIndex() + 5

	r.onLedgerFullyValidated(validatedSeq, validatedHash)

	totalCalls := len(rs.replayCalls()) + len(rs.legacyCalls())
	assert.Equal(t, 0, totalCalls,
		"router must NOT arm an acquisition when no peers are tracked")
}

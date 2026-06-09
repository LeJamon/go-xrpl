package adaptor

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRvl_AppendUint32BE(t *testing.T) {
	cases := []struct {
		v    uint32
		want [4]byte
	}{
		{0, [4]byte{0, 0, 0, 0}},
		{1, [4]byte{0, 0, 0, 1}},
		{0xDEADBEEF, [4]byte{0xDE, 0xAD, 0xBE, 0xEF}},
		{0xFFFFFFFF, [4]byte{0xFF, 0xFF, 0xFF, 0xFF}},
	}
	for _, c := range cases {
		out := appendUint32BE(nil, c.v)
		require.Len(t, out, 4)
		var got [4]byte
		copy(got[:], out)
		assert.Equal(t, c.want, got, "v=%x", c.v)
	}
}

func TestRvl_AppendLengthPrefixed(t *testing.T) {
	data := []byte{0xAA, 0xBB, 0xCC}
	out := appendLengthPrefixed(nil, data)
	require.Len(t, out, 4+len(data))
	length := binary.BigEndian.Uint32(out[:4])
	assert.Equal(t, uint32(len(data)), length)
	assert.Equal(t, data, out[4:])
}

func TestRvl_AppendLengthPrefixed_Empty(t *testing.T) {
	out := appendLengthPrefixed(nil, nil)
	require.Len(t, out, 4)
	assert.Equal(t, []byte{0, 0, 0, 0}, out)
}

func TestRvl_AppendUint32BE_Accumulates(t *testing.T) {
	out := appendUint32BE(nil, 1)
	out = appendUint32BE(out, 2)
	assert.Len(t, out, 8)
	assert.Equal(t, uint32(1), binary.BigEndian.Uint32(out[:4]))
	assert.Equal(t, uint32(2), binary.BigEndian.Uint32(out[4:]))
}

func TestRvl_AppendLengthPrefixed_LargeData(t *testing.T) {
	data := bytes.Repeat([]byte{0xAB}, 1024)
	out := appendLengthPrefixed(nil, data)
	assert.Len(t, out, 4+1024)
	assert.Equal(t, uint32(1024), binary.BigEndian.Uint32(out[:4]))
	assert.Equal(t, data, out[4:])
}

func TestRvl_SemanticHash(t *testing.T) {
	vl := &message.ValidatorList{
		Manifest:  []byte("manifest"),
		Blob:      []byte("blob"),
		Signature: []byte("sig"),
		Version:   3,
	}
	h1 := validatorListSemanticHash(vl)
	h2 := validatorListSemanticHash(vl)
	assert.Equal(t, h1, h2, "deterministic")

	vl2 := *vl
	vl2.Version = 4
	assert.NotEqual(t, h1, validatorListSemanticHash(&vl2))

	vl3 := *vl
	vl3.Blob = []byte("other")
	assert.NotEqual(t, h1, validatorListSemanticHash(&vl3))
}

func TestRvl_SemanticHash_EmptyFields(t *testing.T) {
	vl := &message.ValidatorList{}
	h := validatorListSemanticHash(vl)
	// version(4) + 3 * (length_prefix(4) + 0 bytes) = 16 bytes.
	assert.Len(t, h, 16)
	assert.Equal(t, []byte{0, 0, 0, 0}, h[:4])
}

func TestRvl_CollectionSemanticHash(t *testing.T) {
	coll := &message.ValidatorListCollection{
		Version:  2,
		Manifest: []byte("manifest"),
		Blobs: []message.ValidatorBlobInfo{
			{Manifest: []byte("bm1"), Blob: []byte("b1"), Signature: []byte("s1")},
			{Manifest: []byte("bm2"), Blob: []byte("b2"), Signature: []byte("s2")},
		},
	}
	h1 := validatorListCollectionSemanticHash(coll)
	h2 := validatorListCollectionSemanticHash(coll)
	assert.Equal(t, h1, h2, "deterministic")

	c2 := *coll
	c2.Version = 3
	assert.NotEqual(t, h1, validatorListCollectionSemanticHash(&c2))

	c3 := *coll
	c3.Blobs = coll.Blobs[:1]
	assert.NotEqual(t, h1, validatorListCollectionSemanticHash(&c3))
}

func TestRvl_CollectionSemanticHash_EmptyBlobs(t *testing.T) {
	coll := &message.ValidatorListCollection{Version: 2, Manifest: []byte("m")}
	h := validatorListCollectionSemanticHash(coll)
	// version(4) + len("m")(4) + "m"(1) = 9 bytes.
	assert.Len(t, h, 9)
	assert.Equal(t, []byte{0, 0, 0, 2}, h[:4])
}

func TestRvl_ChargePeer_None(t *testing.T) {
	r, rs := makeRouterWithBadDataRecorder(t)
	chargePeerForDisposition(r, 1, "vl", validatorlist.Accepted)
	assert.Empty(t, rs.getBadDataCalls())
	// Expired also has ChargeNone.
	chargePeerForDisposition(r, 1, "vl", validatorlist.Expired)
	assert.Empty(t, rs.getBadDataCalls())
	// Pending also has ChargeNone.
	chargePeerForDisposition(r, 1, "vl", validatorlist.Pending)
	assert.Empty(t, rs.getBadDataCalls())
}

func TestRvl_ChargePeer_UselessData(t *testing.T) {
	r, rs := makeRouterWithBadDataRecorder(t)
	chargePeerForDisposition(r, 42, "vl", validatorlist.SameSequence)
	calls := rs.getBadDataCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, uint64(42), calls[0].peerID)
	assert.Equal(t, "vl-useless-same_sequence", calls[0].reason)
}

func TestRvl_ChargePeer_KnownSequence(t *testing.T) {
	r, rs := makeRouterWithBadDataRecorder(t)
	chargePeerForDisposition(r, 7, "vl", validatorlist.KnownSequence)
	calls := rs.getBadDataCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "vl-useless-known_sequence", calls[0].reason)
}

func TestRvl_ChargePeer_Untrusted(t *testing.T) {
	r, rs := makeRouterWithBadDataRecorder(t)
	chargePeerForDisposition(r, 9, "pfx", validatorlist.Untrusted)
	calls := rs.getBadDataCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "pfx-useless-untrusted", calls[0].reason)
}

func TestRvl_ChargePeer_InvalidData(t *testing.T) {
	r, rs := makeRouterWithBadDataRecorder(t)
	chargePeerForDisposition(r, 7, "vl-coll", validatorlist.Stale)
	calls := rs.getBadDataCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "vl-coll-baddata-stale", calls[0].reason)
}

func TestRvl_ChargePeer_UnsupportedVersion(t *testing.T) {
	r, rs := makeRouterWithBadDataRecorder(t)
	chargePeerForDisposition(r, 3, "vl", validatorlist.UnsupportedVersion)
	calls := rs.getBadDataCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "vl-baddata-unsupported_version", calls[0].reason)
}

func TestRvl_ChargePeer_InvalidSignature(t *testing.T) {
	r, rs := makeRouterWithBadDataRecorder(t)
	chargePeerForDisposition(r, 3, "vl", validatorlist.Invalid)
	calls := rs.getBadDataCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "vl-badsig-invalid", calls[0].reason)
}

// TestRvl_ChargePeer_MalformedChargeNone verifies Malformed has ChargeNone (poller-only).
func TestRvl_ChargePeer_MalformedChargeNone(t *testing.T) {
	r, rs := makeRouterWithBadDataRecorder(t)
	chargePeerForDisposition(r, 5, "vl", validatorlist.Malformed)
	assert.Empty(t, rs.getBadDataCalls())
}

func TestRvl_PeerSite_NilOverlay(t *testing.T) {
	r, _ := makeRouterWithBadDataRecorder(t)
	assert.Equal(t, "peer:99", r.peerSite(peermanagement.PeerID(99)))
	assert.Equal(t, "peer:0", r.peerSite(peermanagement.PeerID(0)))
	assert.Equal(t, "peer:4294967295", r.peerSite(peermanagement.PeerID(0xFFFFFFFF)))
}

func TestRvl_PeerSupportsVLFeature_NilOverlay(t *testing.T) {
	r, _ := makeRouterWithBadDataRecorder(t)
	assert.True(t, r.peerSupportsValidatorListFeature(1))
}

func TestRvl_PeerSupportsVL2_NilOverlay(t *testing.T) {
	r, _ := makeRouterWithBadDataRecorder(t)
	assert.True(t, r.peerSupportsValidatorList2(1))
}

func rvl_newRouterWithVL(t *testing.T) (*Router, *badDataRecordingSender) {
	t.Helper()
	r, rs := makeRouterWithBadDataRecorder(t)
	agg, err := validatorlist.New(validatorlist.Config{})
	require.NoError(t, err)
	r.SetValidatorListAggregator(agg)
	return r, rs
}

func TestRvl_HandleVL_NilAgg(t *testing.T) {
	r, rs := makeRouterWithBadDataRecorder(t)
	vl := &message.ValidatorList{Version: 1, Manifest: []byte("m"), Blob: []byte("b"), Signature: []byte("s")}
	r.handleValidatorList(&peermanagement.InboundMessage{
		PeerID:  1,
		Type:    uint16(message.TypeValidatorList),
		Payload: encodePayload(t, vl),
	})
	assert.Empty(t, rs.getBadDataCalls())
}

func TestRvl_HandleVL_DecodeError(t *testing.T) {
	r, rs := rvl_newRouterWithVL(t)
	r.handleValidatorList(&peermanagement.InboundMessage{
		PeerID:  5,
		Type:    uint16(message.TypeValidatorList),
		Payload: []byte{0xFF, 0xFE, 0xFD},
	})
	calls := rs.getBadDataCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, uint64(5), calls[0].peerID)
	assert.Equal(t, "vl-decode", calls[0].reason)
}

// TestRvl_HandleVL_Untrusted charges the peer with useless-untrusted when publisher
// is unknown (no publishers configured → Untrusted result).
func TestRvl_HandleVL_Untrusted(t *testing.T) {
	r, rs := rvl_newRouterWithVL(t)
	vl := &message.ValidatorList{Version: 1, Manifest: []byte("m"), Blob: []byte("b"), Signature: []byte("s")}
	r.handleValidatorList(&peermanagement.InboundMessage{
		PeerID:  3,
		Type:    uint16(message.TypeValidatorList),
		Payload: encodePayload(t, vl),
	})
	calls := rs.getBadDataCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, uint64(3), calls[0].peerID)
	assert.Equal(t, "vl-useless-untrusted", calls[0].reason)
}

func TestRvl_HandleVL_Duplicate(t *testing.T) {
	r, rs := rvl_newRouterWithVL(t)
	vl := &message.ValidatorList{Version: 1, Manifest: []byte("m2"), Blob: []byte("b2"), Signature: []byte("s2")}
	payload := encodePayload(t, vl)

	r.handleValidatorList(&peermanagement.InboundMessage{PeerID: 10, Type: uint16(message.TypeValidatorList), Payload: payload})
	calls1 := rs.getBadDataCalls()
	require.Len(t, calls1, 1)
	assert.Equal(t, "vl-useless-untrusted", calls1[0].reason)

	// Second delivery from different peer — same content → dedup fires.
	r.handleValidatorList(&peermanagement.InboundMessage{PeerID: 11, Type: uint16(message.TypeValidatorList), Payload: payload})
	calls2 := rs.getBadDataCalls()
	require.Len(t, calls2, 2)
	assert.Equal(t, uint64(11), calls2[1].peerID)
	assert.Equal(t, "vl-duplicate", calls2[1].reason)
}

func TestRvl_HandleVL_NilMsgSeen(t *testing.T) {
	r, rs := rvl_newRouterWithVL(t)
	r.messageSeen = nil

	vl := &message.ValidatorList{Version: 1, Manifest: []byte("nms"), Blob: []byte("nmsb"), Signature: []byte("nmss")}
	payload := encodePayload(t, vl)

	r.handleValidatorList(&peermanagement.InboundMessage{PeerID: 50, Type: uint16(message.TypeValidatorList), Payload: payload})
	r.handleValidatorList(&peermanagement.InboundMessage{PeerID: 51, Type: uint16(message.TypeValidatorList), Payload: payload})

	// Both processed independently — two Untrusted charges, no duplicate charge.
	calls := rs.getBadDataCalls()
	assert.Len(t, calls, 2)
	for _, c := range calls {
		assert.Equal(t, "vl-useless-untrusted", c.reason)
	}
}

func TestRvl_HandleVLC_NilAgg(t *testing.T) {
	r, rs := makeRouterWithBadDataRecorder(t)
	coll := &message.ValidatorListCollection{Version: 2, Blobs: []message.ValidatorBlobInfo{{Blob: []byte("b")}}}
	r.handleValidatorListCollection(&peermanagement.InboundMessage{
		PeerID:  1,
		Type:    uint16(message.TypeValidatorListCollection),
		Payload: encodePayload(t, coll),
	})
	assert.Empty(t, rs.getBadDataCalls())
}

func TestRvl_HandleVLC_DecodeError(t *testing.T) {
	r, rs := rvl_newRouterWithVL(t)
	r.handleValidatorListCollection(&peermanagement.InboundMessage{
		PeerID:  6,
		Type:    uint16(message.TypeValidatorListCollection),
		Payload: []byte{0xFF, 0xFE, 0xFD},
	})
	calls := rs.getBadDataCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, uint64(6), calls[0].peerID)
	assert.Equal(t, "vl-coll-decode", calls[0].reason)
}

func TestRvl_HandleVLC_WrongVersion(t *testing.T) {
	r, rs := rvl_newRouterWithVL(t)
	coll := &message.ValidatorListCollection{
		Version: 1,
		Blobs:   []message.ValidatorBlobInfo{{Blob: []byte("b")}},
	}
	r.handleValidatorListCollection(&peermanagement.InboundMessage{
		PeerID:  8,
		Type:    uint16(message.TypeValidatorListCollection),
		Payload: encodePayload(t, coll),
	})
	calls := rs.getBadDataCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "vl-coll-wrong-version", calls[0].reason)
}

func TestRvl_HandleVLC_NoBlobs(t *testing.T) {
	r, rs := rvl_newRouterWithVL(t)
	coll := &message.ValidatorListCollection{
		Version: 2,
		Blobs:   nil,
	}
	r.handleValidatorListCollection(&peermanagement.InboundMessage{
		PeerID:  9,
		Type:    uint16(message.TypeValidatorListCollection),
		Payload: encodePayload(t, coll),
	})
	calls := rs.getBadDataCalls()
	require.Len(t, calls, 2)
	reasons := map[string]bool{}
	for _, c := range calls {
		reasons[c.reason] = true
	}
	assert.True(t, reasons["vl-coll-heavy-no-blobs"])
	assert.True(t, reasons["vl-coll-no-blobs"])
}

func TestRvl_HandleVLC_Untrusted(t *testing.T) {
	r, rs := rvl_newRouterWithVL(t)
	coll := &message.ValidatorListCollection{
		Version:  2,
		Manifest: []byte("cut-m"),
		Blobs:    []message.ValidatorBlobInfo{{Blob: []byte("cut-b"), Signature: []byte("cut-s")}},
	}
	r.handleValidatorListCollection(&peermanagement.InboundMessage{
		PeerID:  16,
		Type:    uint16(message.TypeValidatorListCollection),
		Payload: encodePayload(t, coll),
	})
	calls := rs.getBadDataCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, uint64(16), calls[0].peerID)
	assert.Equal(t, "vl-coll-useless-untrusted", calls[0].reason)
}

func TestRvl_HandleVLC_Duplicate(t *testing.T) {
	r, rs := rvl_newRouterWithVL(t)
	coll := &message.ValidatorListCollection{
		Version:  2,
		Manifest: []byte("dup-cm"),
		Blobs:    []message.ValidatorBlobInfo{{Blob: []byte("dup-cb"), Signature: []byte("dup-cs")}},
	}
	payload := encodePayload(t, coll)

	r.handleValidatorListCollection(&peermanagement.InboundMessage{PeerID: 20, Type: uint16(message.TypeValidatorListCollection), Payload: payload})
	calls1 := rs.getBadDataCalls()
	require.Len(t, calls1, 1)

	// Second delivery from different peer → duplicate charge.
	r.handleValidatorListCollection(&peermanagement.InboundMessage{PeerID: 21, Type: uint16(message.TypeValidatorListCollection), Payload: payload})
	calls2 := rs.getBadDataCalls()
	require.Len(t, calls2, 2)
	assert.Equal(t, uint64(21), calls2[1].peerID)
	assert.Equal(t, "vl-coll-duplicate", calls2[1].reason)
}

func TestRvl_HandleVLC_NilMsgSeen(t *testing.T) {
	r, rs := rvl_newRouterWithVL(t)
	r.messageSeen = nil

	coll := &message.ValidatorListCollection{
		Version:  2,
		Manifest: []byte("nms-cm"),
		Blobs:    []message.ValidatorBlobInfo{{Blob: []byte("nms-b"), Signature: []byte("nms-s")}},
	}
	payload := encodePayload(t, coll)

	r.handleValidatorListCollection(&peermanagement.InboundMessage{PeerID: 60, Type: uint16(message.TypeValidatorListCollection), Payload: payload})
	r.handleValidatorListCollection(&peermanagement.InboundMessage{PeerID: 61, Type: uint16(message.TypeValidatorListCollection), Payload: payload})

	calls := rs.getBadDataCalls()
	assert.Len(t, calls, 2)
}

type rvl_trackingSender struct {
	noopSender
	mu    sync.Mutex
	calls []rvl_sendCall
	errOn uint64 // if non-zero, return error for this peerID
}

type rvl_sendCall struct {
	peerID uint64
	frame  []byte
}

func (s *rvl_trackingSender) SendToPeer(peerID uint64, frame []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.errOn != 0 && peerID == s.errOn {
		return fmt.Errorf("send error")
	}
	cp := make([]byte, len(frame))
	copy(cp, frame)
	s.calls = append(s.calls, rvl_sendCall{peerID: peerID, frame: cp})
	return nil
}

func (s *rvl_trackingSender) getCalls() []rvl_sendCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]rvl_sendCall, len(s.calls))
	copy(out, s.calls)
	return out
}

func TestRvl_NewRouterBroadcaster_NilOverlay(t *testing.T) {
	b := NewRouterBroadcaster(nil, nil)
	assert.Nil(t, b.ActivePeers())
	assert.False(t, b.PeerSupportsVL(1))
	assert.False(t, b.PeerSupportsV2(1))
}

func TestRvl_NewRouterBroadcaster_NilReceiver(t *testing.T) {
	var b *RouterBroadcaster
	assert.Nil(t, b.ActivePeers())
	assert.False(t, b.PeerSupportsVL(1))
	assert.False(t, b.PeerSupportsV2(1))
}

func TestRvl_NewValidatorListBroadcaster(t *testing.T) {
	r, _ := makeRouterWithBadDataRecorder(t)
	b := r.NewValidatorListBroadcaster(nil, nil)
	require.NotNil(t, b)
	assert.Equal(t, r.messageSeen, b.suppression)
}

func TestRvl_SendList_NilSender(t *testing.T) {
	b := &RouterBroadcaster{}
	err := b.SendList(1, []byte("m"), []byte("b"), []byte("s"), 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil sender")
}

func TestRvl_SendList_Delivers(t *testing.T) {
	ts := &rvl_trackingSender{}
	b := NewRouterBroadcaster(nil, ts)
	err := b.SendList(42, []byte("manifest"), []byte("blob"), []byte("sig"), 3)
	require.NoError(t, err)
	calls := ts.getCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, uint64(42), calls[0].peerID)
	assert.NotEmpty(t, calls[0].frame)
}

func TestRvl_SendList_SuppressionDedup(t *testing.T) {
	ts := &rvl_trackingSender{}
	r, _ := makeRouterWithBadDataRecorder(t)
	b := r.NewValidatorListBroadcaster(nil, ts)

	manifest := []byte("manifest")
	blob := []byte("blob")
	sig := []byte("sig")
	version := uint32(1)

	require.NoError(t, b.SendList(10, manifest, blob, sig, version))
	require.Len(t, ts.getCalls(), 1)

	require.NoError(t, b.SendList(10, manifest, blob, sig, version))
	assert.Len(t, ts.getCalls(), 1, "second send to already-seen peer must be suppressed")
}

func TestRvl_SendList_DifferentPeers(t *testing.T) {
	ts := &rvl_trackingSender{}
	r, _ := makeRouterWithBadDataRecorder(t)
	b := r.NewValidatorListBroadcaster(nil, ts)

	require.NoError(t, b.SendList(1, []byte("m"), []byte("b"), []byte("s"), 1))
	require.NoError(t, b.SendList(2, []byte("m"), []byte("b"), []byte("s"), 1))
	assert.Len(t, ts.getCalls(), 2)
}

func TestRvl_SendList_SendError(t *testing.T) {
	ts := &rvl_trackingSender{errOn: 99}
	b := NewRouterBroadcaster(nil, ts)
	err := b.SendList(99, []byte("m"), []byte("b"), []byte("s"), 1)
	require.Error(t, err)
}

func TestRvl_SendCollection_NilSender(t *testing.T) {
	b := &RouterBroadcaster{}
	err := b.SendCollection(1, []byte("m"), nil, 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil sender")
}

func TestRvl_SendCollection_Delivers(t *testing.T) {
	ts := &rvl_trackingSender{}
	b := NewRouterBroadcaster(nil, ts)
	blobs := []validatorlist.BroadcastBlob{
		{Manifest: []byte("bm1"), Blob: []byte("b1"), Signature: []byte("s1")},
	}
	err := b.SendCollection(55, []byte("manifest"), blobs, 2)
	require.NoError(t, err)
	calls := ts.getCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, uint64(55), calls[0].peerID)
	assert.NotEmpty(t, calls[0].frame)
}

func TestRvl_SendCollection_EmptyBlobs(t *testing.T) {
	ts := &rvl_trackingSender{}
	b := NewRouterBroadcaster(nil, ts)
	err := b.SendCollection(33, []byte("m"), nil, 2)
	require.NoError(t, err)
	assert.Len(t, ts.getCalls(), 1)
}

func TestRvl_SendCollection_SuppressionDedup(t *testing.T) {
	ts := &rvl_trackingSender{}
	r, _ := makeRouterWithBadDataRecorder(t)
	b := r.NewValidatorListBroadcaster(nil, ts)

	blobs := []validatorlist.BroadcastBlob{{Blob: []byte("b"), Signature: []byte("s")}}
	require.NoError(t, b.SendCollection(77, []byte("m"), blobs, 2))
	require.NoError(t, b.SendCollection(77, []byte("m"), blobs, 2))

	assert.Len(t, ts.getCalls(), 1, "second send to same peer must be suppressed")
}

func TestRvl_SendCollection_SendError(t *testing.T) {
	ts := &rvl_trackingSender{errOn: 66}
	b := NewRouterBroadcaster(nil, ts)
	blobs := []validatorlist.BroadcastBlob{{Blob: []byte("b"), Signature: []byte("s")}}
	err := b.SendCollection(66, []byte("m"), blobs, 2)
	require.Error(t, err)
}

func TestRvl_SendCollection_MultipleBlobs(t *testing.T) {
	ts := &rvl_trackingSender{}
	b := NewRouterBroadcaster(nil, ts)
	blobs := []validatorlist.BroadcastBlob{
		{Manifest: []byte("bm1"), Blob: []byte("b1"), Signature: []byte("s1")},
		{Manifest: []byte("bm2"), Blob: []byte("b2"), Signature: []byte("s2")},
		{Blob: []byte("b3"), Signature: []byte("s3")},
	}
	err := b.SendCollection(88, []byte("pub-manifest"), blobs, 3)
	require.NoError(t, err)
	require.Len(t, ts.getCalls(), 1)
}

func TestRvl_SendList_NoSuppression(t *testing.T) {
	ts := &rvl_trackingSender{}
	b := NewRouterBroadcaster(nil, ts) // no suppression

	// Two identical sends to the same peer — both should deliver when no suppression.
	require.NoError(t, b.SendList(5, []byte("m"), []byte("b"), []byte("s"), 1))
	require.NoError(t, b.SendList(5, []byte("m"), []byte("b"), []byte("s"), 1))
	assert.Len(t, ts.getCalls(), 2)
}

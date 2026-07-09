package peermanagement

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
)

// decodeGetObjectsReply pops the single frame the overlay queued for the
// peer and decodes it back into a TMGetObjectByHash. Fails the test if no
// frame is waiting.
func decodeGetObjectsReply(t *testing.T, peer *Peer) *message.GetObjectByHash {
	t.Helper()
	select {
	case frame := <-peer.send:
		hdr, payload, err := message.ReadMessage(bytes.NewReader(frame))
		require.NoError(t, err)
		require.Equal(t, message.TypeGetObjects, hdr.MessageType)
		decoded, err := message.Decode(message.TypeGetObjects, payload)
		require.NoError(t, err)
		reply, ok := decoded.(*message.GetObjectByHash)
		require.True(t, ok)
		return reply
	default:
		t.Fatal("expected a TMGetObjectByHash reply frame, got none")
		return nil
	}
}

func newServeTestPeer(t *testing.T, id PeerID) *Peer {
	t.Helper()
	identity, err := NewIdentity()
	require.NoError(t, err)
	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	return NewPeer(id, endpoint, true, identity, make(chan Event, 1))
}

// TestServeGetObjects_FetchesAndReplies covers the happy path of the
// generic TMGetObjectByHash serve branch (rippled PeerImp.cpp:2483-2538):
// found hashes are packed into a query=false reply echoing the request's
// type/seq, missing hashes are skipped, malformed (wrong-size) hashes are
// never looked up, and the request's nodeid/ledgerseq are echoed back.
func TestServeGetObjects_FetchesAndReplies(t *testing.T) {
	knownHash := [32]byte{0xAA}
	knownBlob := []byte{0x11, 0x22, 0x33, 0x44}
	missingHash := [32]byte{0xBB}

	lookups := 0
	o := &Overlay{
		peers: make(map[PeerID]*Peer),
		nodeObjectProvider: func(h [32]byte) ([]byte, bool) {
			lookups++
			if h == knownHash {
				return knownBlob, true
			}
			return nil, false
		},
	}
	peer := newServeTestPeer(t, PeerID(401))
	o.peers[peer.ID()] = peer

	nodeID := []byte{0x09, 0x08, 0x07}
	req := &message.GetObjectByHash{
		ObjType: message.ObjectTypeUnknown,
		Query:   true,
		Objects: []message.IndexedObject{
			{Hash: knownHash[:], NodeID: nodeID, LedgerSeq: 42},
			{Hash: missingHash[:]},
			{Hash: []byte{0x01, 0x02}}, // malformed: not 32 bytes
		},
	}
	o.serveGetObjects(peer.ID(), req)

	// Only the two uint256-sized hashes are looked up; the malformed
	// object is skipped before consulting the provider.
	assert.Equal(t, 2, lookups, "provider must be consulted only for valid 32-byte hashes")

	reply := decodeGetObjectsReply(t, peer)
	assert.False(t, reply.Query, "reply must have query=false")
	assert.Equal(t, message.ObjectTypeUnknown, reply.ObjType, "reply must echo the request type")
	require.Len(t, reply.Objects, 1, "only the found object belongs in the reply")
	got := reply.Objects[0]
	assert.Equal(t, knownHash[:], got.Hash)
	assert.Equal(t, knownBlob, got.Data)
	assert.Equal(t, nodeID, got.Index, "request nodeid is echoed into the reply index")
	assert.Equal(t, uint32(42), got.LedgerSeq, "request ledgerseq is echoed back")

	// A generic by-hash request is charged feeModerateBurdenPeer.
	assert.Positive(t, peer.Load(), "serving a by-hash request must charge the peer")
}

// TestServeGetObjects_TruncatesAtReplyCap pins rippled PeerImp.cpp:2551-2562
// (3.2.0 DoS gate): a query for more objects than hardMaxReplyNodes is
// rejected outright before any node-store lookup and charged feeInvalidData,
// rather than being served-then-truncated. Closes the per-object
// NodeStore-fetch DoS at the request boundary (PeerImp.cpp:2500-2506).
func TestServeGetObjects_RejectsOversizedRequest(t *testing.T) {
	lookups := 0
	o := &Overlay{
		peers: make(map[PeerID]*Peer),
		nodeObjectProvider: func([32]byte) ([]byte, bool) {
			lookups++
			return []byte{0x01}, true
		},
	}
	peer := newServeTestPeer(t, PeerID(404))
	o.peers[peer.ID()] = peer

	const requested = hardMaxReplyNodes + 5
	objs := make([]message.IndexedObject, requested)
	for i := range objs {
		var h [32]byte
		binary.BigEndian.PutUint32(h[:], uint32(i+1))
		objs[i] = message.IndexedObject{Hash: h[:]}
	}
	req := &message.GetObjectByHash{Query: true, Objects: objs}
	o.serveGetObjects(peer.ID(), req)

	assert.Zero(t, lookups, "an oversized request is rejected before any node-store lookup")
	select {
	case frame := <-peer.send:
		t.Fatalf("no reply expected for an oversized request, got %d bytes", len(frame))
	default:
	}
	assert.Positive(t, peer.Load(), "an oversized request must charge the peer")
}

// TestGetObjectByHashFee pins the 3.2.0 differential fee schedule: the first
// getObjFreeObjects objects are free, billable objects cost per-hit/per-miss
// (misses billed first), and a one-shot size-band surcharge applies. A single
// all-miss max-size request must exceed the resource drop threshold.
func TestGetObjectByHashFee(t *testing.T) {
	tests := []struct {
		name      string
		requested int
		found     int
		want      int
	}{
		{"empty", 0, 0, 0},
		{"under free tier, all found", 8, 8, 0},
		{"exactly free tier", 16, 16, 0},
		// 20 requested, all found: 4 billable hits * 1 = 4, small band.
		{"just over free tier, all hits", 20, 20, 4},
		// 20 requested, 16 found: 4 misses within billable → 4*8 = 32.
		{"just over free tier, all misses billable", 20, 16, 32},
		// 65 requested (> small band max 64), all found:
		// 49 billable hits * 1 + medium band 100 = 149.
		{"medium band, all hits", 65, 65, 149},
		// 1025 requested (> medium band max 1024), all found:
		// 1009 billable hits * 1 + large band 1000 = 2009.
		{"large band, all hits", 1025, 1025, 2009},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := getObjectByHashFee(tc.requested, tc.found)
			assert.Equal(t, tc.want, got.Cost())
		})
	}

	// A single all-miss max-size request must be expensive enough that one
	// message trips the drop threshold, matching rippled's tuning intent.
	maxMiss := getObjectByHashFee(hardMaxReplyNodes, 0)
	assert.Greater(t, maxMiss.Cost(), resource.DropThreshold,
		"one all-miss max-size request must exceed the drop threshold")
}

// TestServeGetObjects_AlwaysRepliesEvenWhenEmpty verifies the rippled
// contract that the generic branch always sends a reply (PeerImp.cpp:2538),
// even when none of the requested hashes are held — so a requester can
// distinguish "I have none" from a silent peer.
func TestServeGetObjects_AlwaysRepliesEvenWhenEmpty(t *testing.T) {
	o := &Overlay{
		peers: make(map[PeerID]*Peer),
		nodeObjectProvider: func([32]byte) ([]byte, bool) {
			return nil, false
		},
	}
	peer := newServeTestPeer(t, PeerID(402))
	o.peers[peer.ID()] = peer

	hash := [32]byte{0xCC}
	req := &message.GetObjectByHash{
		Query:   true,
		Objects: []message.IndexedObject{{Hash: hash[:]}},
	}
	o.serveGetObjects(peer.ID(), req)

	reply := decodeGetObjectsReply(t, peer)
	assert.False(t, reply.Query)
	assert.Empty(t, reply.Objects, "no held objects → empty reply, but a reply nonetheless")
}

// TestServeGetObjects_NilProviderDropsWithoutCharge verifies that an
// overlay with no node store wired drops the request without replying and
// without charging the peer — honest peers are not punished for a
// capability we don't run.
func TestServeGetObjects_NilProviderDropsWithoutCharge(t *testing.T) {
	o := &Overlay{peers: make(map[PeerID]*Peer)} // nodeObjectProvider stays nil
	peer := newServeTestPeer(t, PeerID(403))
	o.peers[peer.ID()] = peer

	hash := [32]byte{0xDD}
	req := &message.GetObjectByHash{
		Query:   true,
		Objects: []message.IndexedObject{{Hash: hash[:]}},
	}
	o.serveGetObjects(peer.ID(), req)

	select {
	case frame := <-peer.send:
		t.Fatalf("no reply expected when no node store is wired, got %d bytes", len(frame))
	default:
	}
	assert.Zero(t, peer.Load(), "unserved request must not charge the peer")
}

// TestServeGetObjects_BadLedgerHashCharged verifies that a wrong-sized
// optional ledger hash is rejected with a malformed-request charge and no
// reply, mirroring rippled PeerImp.cpp:2492-2501.
func TestServeGetObjects_BadLedgerHashCharged(t *testing.T) {
	lookups := 0
	o := &Overlay{
		peers: make(map[PeerID]*Peer),
		nodeObjectProvider: func([32]byte) ([]byte, bool) {
			lookups++
			return nil, false
		},
	}
	peer := newServeTestPeer(t, PeerID(404))
	o.peers[peer.ID()] = peer

	hash := [32]byte{0xEE}
	req := &message.GetObjectByHash{
		Query:      true,
		LedgerHash: []byte{0x01, 0x02, 0x03}, // not 32 bytes
		Objects:    []message.IndexedObject{{Hash: hash[:]}},
	}
	o.serveGetObjects(peer.ID(), req)

	assert.Zero(t, lookups, "a malformed ledger hash short-circuits before any fetch")
	select {
	case frame := <-peer.send:
		t.Fatalf("no reply expected for a malformed ledger hash, got %d bytes", len(frame))
	default:
	}
	assert.Positive(t, peer.Load(), "a malformed ledger hash must charge the peer")
}

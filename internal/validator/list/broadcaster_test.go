package list_test

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/manifest"
	"github.com/LeJamon/goXRPLd/internal/validator/list"
)

// fakeBroadcaster records each call so tests can assert which wire
// shape (SendList vs SendCollection) BroadcastLatest selected for each
// peer based on its negotiated feature flags.
type fakeBroadcaster struct {
	mu       sync.Mutex
	peers    []uint64
	supports map[uint64]bool
	v2       map[uint64]bool

	listCalls       []sendListCall
	collectionCalls []sendCollectionCall
}

type sendListCall struct {
	peerID  uint64
	blob    []byte
	sig     []byte
	version uint32
}

type sendCollectionCall struct {
	peerID  uint64
	blobs   []list.BroadcastBlob
	version uint32
}

func newFakeBroadcaster(peers []uint64, vlSupport, v2Support map[uint64]bool) *fakeBroadcaster {
	return &fakeBroadcaster{peers: peers, supports: vlSupport, v2: v2Support}
}

func (f *fakeBroadcaster) ActivePeers() []uint64 {
	out := make([]uint64, len(f.peers))
	copy(out, f.peers)
	return out
}

func (f *fakeBroadcaster) PeerSupportsVL(peerID uint64) bool {
	return f.supports[peerID]
}

func (f *fakeBroadcaster) PeerSupportsV2(peerID uint64) bool {
	return f.v2[peerID]
}

func (f *fakeBroadcaster) SendList(peerID uint64, manifest, blob, signature []byte, version uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls = append(f.listCalls, sendListCall{
		peerID:  peerID,
		blob:    append([]byte(nil), blob...),
		sig:     append([]byte(nil), signature...),
		version: version,
	})
	return nil
}

func (f *fakeBroadcaster) SendCollection(peerID uint64, manifest []byte, blobs []list.BroadcastBlob, version uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]list.BroadcastBlob, len(blobs))
	for i, b := range blobs {
		cp[i] = list.BroadcastBlob{
			Manifest:  append([]byte(nil), b.Manifest...),
			Blob:      append([]byte(nil), b.Blob...),
			Signature: append([]byte(nil), b.Signature...),
		}
	}
	f.collectionCalls = append(f.collectionCalls, sendCollectionCall{
		peerID:  peerID,
		blobs:   cp,
		version: version,
	})
	return nil
}

// TestBroadcastLatest_V2PeerGetsCollection_NoRemaining pins M1: a
// v2-capable peer must receive a TMValidatorListCollection (single
// entry — current only) even when the publisher has no Remaining
// blobs, mirroring rippled's sendValidatorList branch on
// peer->supportsFeature(ValidatorList2Propagation) at
// ValidatorList.cpp:752-757.
func TestBroadcastLatest_V2PeerGetsCollection_NoRemaining(t *testing.T) {
	pub := newPublisher(t, 0x51, 0x52)
	v1 := derivedValidatorKey(0x60)

	agg, err := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         fixedClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Peer 100: v1-only. Peer 200: v2-capable.
	fake := newFakeBroadcaster(
		[]uint64{100, 200},
		map[uint64]bool{100: true, 200: true},
		map[uint64]bool{100: false, 200: true},
	)
	agg.SetBroadcaster(fake)

	now := fixedClock()()
	exp := now.Add(24 * time.Hour).Unix()
	blob, sig := pub.signList(t, 5, 0, exp, [][33]byte{v1})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob, sig, 1, "p1://"); d != list.Accepted {
		t.Fatalf("apply: %s", d)
	}

	// No Remaining present (single accepted blob).
	agg.BroadcastLatest(list.PublisherKey(pub.masterPub), 0)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.listCalls) != 1 || fake.listCalls[0].peerID != 100 {
		t.Fatalf("v1-only peer must receive exactly one SendList; got %+v", fake.listCalls)
	}
	if len(fake.collectionCalls) != 1 || fake.collectionCalls[0].peerID != 200 {
		t.Fatalf("v2 peer must receive exactly one SendCollection; got %+v", fake.collectionCalls)
	}
	if len(fake.collectionCalls[0].blobs) != 1 {
		t.Fatalf("v2 collection with no Remaining must carry single entry (current); got %d blobs",
			len(fake.collectionCalls[0].blobs))
	}
	if fake.collectionCalls[0].version < 2 {
		t.Fatalf("collection version must be ≥ 2; got %d", fake.collectionCalls[0].version)
	}
}

// TestBroadcastLatest_V2PeerSkippedWhenAtMaxSeq verifies the
// peer-sequence gate is honored on the v2 path.
func TestBroadcastLatest_V2PeerSkippedWhenAtMaxSeq(t *testing.T) {
	pub := newPublisher(t, 0x53, 0x54)
	v1 := derivedValidatorKey(0x61)

	agg, err := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         fixedClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	fake := newFakeBroadcaster(
		[]uint64{200},
		map[uint64]bool{200: true},
		map[uint64]bool{200: true},
	)
	agg.SetBroadcaster(fake)

	now := fixedClock()()
	exp := now.Add(24 * time.Hour).Unix()
	blob, sig := pub.signList(t, 5, 0, exp, [][33]byte{v1})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob, sig, 1, "p1://"); d != list.Accepted {
		t.Fatalf("apply: %s", d)
	}

	// Pre-record that peer 200 has already received sequence 5.
	agg.RecordPeerSequence(200, list.PublisherKey(pub.masterPub), 5)

	agg.BroadcastLatest(list.PublisherKey(pub.masterPub), 0)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.collectionCalls) != 0 {
		t.Fatalf("peer at maxSeq must not receive SendCollection; got %d call(s)", len(fake.collectionCalls))
	}
	if len(fake.listCalls) != 0 {
		t.Fatalf("peer at maxSeq must not receive SendList either; got %d call(s)", len(fake.listCalls))
	}
}

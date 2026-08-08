package list_test

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/validator/list"
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
	peerID   uint64
	manifest []byte
	blob     []byte
	sig      []byte
	version  uint32
}

type sendCollectionCall struct {
	peerID   uint64
	manifest []byte
	blobs    []list.BroadcastBlob
	version  uint32
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
		peerID:   peerID,
		manifest: cloneBroadcastBytes(manifest),
		blob:     append([]byte(nil), blob...),
		sig:      append([]byte(nil), signature...),
		version:  version,
	})
	return nil
}

func (f *fakeBroadcaster) SendCollection(peerID uint64, manifest []byte, blobs []list.BroadcastBlob, version uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]list.BroadcastBlob, len(blobs))
	for i, b := range blobs {
		cp[i] = list.BroadcastBlob{
			Manifest:  cloneBroadcastBytes(b.Manifest),
			Blob:      append([]byte(nil), b.Blob...),
			Signature: append([]byte(nil), b.Signature...),
		}
	}
	f.collectionCalls = append(f.collectionCalls, sendCollectionCall{
		peerID:   peerID,
		manifest: cloneBroadcastBytes(manifest),
		blobs:    cp,
		version:  version,
	})
	return nil
}

func cloneBroadcastBytes(raw []byte) []byte {
	if raw == nil {
		return nil
	}
	return append([]byte{}, raw...)
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
		PublisherKeys:      []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:          1,
		ValidatorManifests: manifest.NewCache(),
		PublisherManifests: manifest.NewCache(),
		Clock:              fixedClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Peer 100: v1-only. Peer 200: v2-capable.
	fake := newFakeBroadcaster(
		[]uint64{100, 200},
		map[uint64]bool{100: true, 200: false},
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
	if !bytes.Equal(fake.collectionCalls[0].manifest, pub.manifestB64) {
		t.Fatalf("collection manifest: got %q want %q", fake.collectionCalls[0].manifest, pub.manifestB64)
	}
	if fake.collectionCalls[0].blobs[0].Manifest != nil {
		t.Fatalf("blob without local manifest must preserve nil presence, got %q", fake.collectionCalls[0].blobs[0].Manifest)
	}
}

// TestBroadcastLatest_V2PeerSkippedWhenAtMaxSeq verifies the
// peer-sequence gate is honored on the v2 path.
func TestBroadcastLatest_V2PeerSkippedWhenAtMaxSeq(t *testing.T) {
	pub := newPublisher(t, 0x53, 0x54)
	v1 := derivedValidatorKey(0x61)

	agg, err := list.New(list.Config{
		PublisherKeys:      []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:          1,
		ValidatorManifests: manifest.NewCache(),
		PublisherManifests: manifest.NewCache(),
		Clock:              fixedClock(),
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

func TestBroadcastLatest_PreservesLocalManifestAndV1EffectiveManifest(t *testing.T) {
	pub := newPublisher(t, 0x55, 0x56)
	validator := derivedValidatorKey(0x62)
	agg, err := list.New(list.Config{
		PublisherKeys:      []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:          1,
		ValidatorManifests: manifest.NewCache(),
		PublisherManifests: manifest.NewCache(),
		Clock:              fixedClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fake := newFakeBroadcaster(
		[]uint64{100, 200},
		map[uint64]bool{100: true, 200: true},
		map[uint64]bool{100: false, 200: true},
	)
	agg.SetBroadcaster(fake)
	now := fixedClock()()
	blob, sig := pub.signList(t, 6, 0, now.Add(24*time.Hour).Unix(), [][33]byte{validator})
	dispositions, _, _ := agg.ApplyCollection(&message.ValidatorListCollection{
		Version:  2,
		Manifest: pub.manifestB64,
		Blobs: []message.ValidatorBlobInfo{{
			Manifest:  cloneBroadcastBytes(pub.manifestB64),
			Blob:      blob,
			Signature: sig,
		}},
	}, "peer://")
	if len(dispositions) != 1 || dispositions[0] != list.Accepted {
		t.Fatalf("ApplyCollection: got %v", dispositions)
	}

	agg.BroadcastLatest(list.PublisherKey(pub.masterPub), 0)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.listCalls) != 1 || !bytes.Equal(fake.listCalls[0].manifest, pub.manifestB64) {
		t.Fatalf("v1 must receive the local manifest override, got %+v", fake.listCalls)
	}
	if fake.listCalls[0].version != 1 {
		t.Fatalf("v1 frame version: got %d want 1", fake.listCalls[0].version)
	}
	if len(fake.collectionCalls) != 1 {
		t.Fatalf("v2 must receive one collection, got %d", len(fake.collectionCalls))
	}
	call := fake.collectionCalls[0]
	if !bytes.Equal(call.manifest, pub.manifestB64) {
		t.Fatalf("v2 collection manifest: got %q want %q", call.manifest, pub.manifestB64)
	}
	if len(call.blobs) != 1 || call.blobs[0].Manifest == nil ||
		!bytes.Equal(call.blobs[0].Manifest, pub.manifestB64) {
		t.Fatalf("v2 local manifest was not preserved: %+v", call.blobs)
	}
}

func TestBroadcastLatest_V2FiltersEntriesByPeerSequence(t *testing.T) {
	pub := newPublisher(t, 0x57, 0x58)
	validator := derivedValidatorKey(0x63)
	agg, err := list.New(list.Config{
		PublisherKeys:      []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:          1,
		ValidatorManifests: manifest.NewCache(),
		PublisherManifests: manifest.NewCache(),
		Clock:              fixedClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fake := newFakeBroadcaster(
		[]uint64{1, 2, 3},
		map[uint64]bool{1: true, 2: true, 3: true},
		map[uint64]bool{1: true, 2: true, 3: true},
	)
	agg.SetBroadcaster(fake)
	now := fixedClock()()
	exp := now.Add(48 * time.Hour).Unix()
	blob5, sig5 := pub.signList(t, 5, 0, exp, [][33]byte{validator})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob5, sig5, 1, "peer://"); d != list.Accepted {
		t.Fatalf("seq=5 apply: %s", d)
	}
	blob10, sig10 := pub.signList(t, 10, now.Add(time.Hour).Unix(), exp, [][33]byte{validator})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob10, sig10, 1, "peer://"); d != list.Pending {
		t.Fatalf("seq=10 apply: %s", d)
	}
	pubKey := list.PublisherKey(pub.masterPub)
	agg.RecordPeerSequence(1, pubKey, 5)
	agg.RecordPeerSequence(2, pubKey, 7)
	agg.BroadcastLatest(pubKey, 0)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.collectionCalls) != 3 {
		t.Fatalf("expected one collection per peer, got %d", len(fake.collectionCalls))
	}
	for _, call := range fake.collectionCalls {
		switch call.peerID {
		case 1, 2:
			if len(call.blobs) != 1 || !bytes.Equal(call.blobs[0].Blob, blob10) {
				t.Fatalf("peer %d should receive only seq 10, got %+v", call.peerID, call.blobs)
			}
		case 3:
			if len(call.blobs) != 2 || !bytes.Equal(call.blobs[0].Blob, blob5) || !bytes.Equal(call.blobs[1].Blob, blob10) {
				t.Fatalf("peer 3 should receive ordered seq 5,10, got %+v", call.blobs)
			}
		default:
			t.Fatalf("unexpected peer %d", call.peerID)
		}
	}
}

func TestAggregatorTickPromotesAndBroadcastsWithoutReadSideMutation(t *testing.T) {
	pub := newPublisher(t, 0x59, 0x5a)
	validator := derivedValidatorKey(0x64)
	now := fixedClock()()
	clockNow := now
	agg, err := list.New(list.Config{
		PublisherKeys:      []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:          1,
		ValidatorManifests: manifest.NewCache(),
		PublisherManifests: manifest.NewCache(),
		Clock:              func() time.Time { return clockNow },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fake := newFakeBroadcaster(
		[]uint64{1, 2},
		map[uint64]bool{1: true, 2: false},
		map[uint64]bool{1: false, 2: true},
	)
	agg.SetBroadcaster(fake)
	expiration := now.Add(48 * time.Hour).Unix()
	blob5, sig5 := pub.signList(t, 5, 0, expiration, [][33]byte{validator})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob5, sig5, 1, "peer://"); d != list.Accepted {
		t.Fatalf("seq=5 apply: %s", d)
	}
	blob10, sig10 := pub.signList(t, 10, now.Add(time.Hour).Unix(), expiration, [][33]byte{validator})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob10, sig10, 2, "peer://"); d != list.Pending {
		t.Fatalf("seq=10 apply: %s", d)
	}
	blob15, sig15 := pub.signList(t, 15, now.Add(3*time.Hour).Unix(), expiration, [][33]byte{validator})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob15, sig15, 2, "peer://"); d != list.Pending {
		t.Fatalf("seq=15 apply: %s", d)
	}

	clockNow = now.Add(2 * time.Hour)
	if got := agg.PublisherSnapshot()[0].Sequence; got != 5 {
		t.Fatalf("PublisherSnapshot promoted pending list: got sequence %d", got)
	}
	_, _ = agg.TrustedValidators()
	if got := agg.PublisherSnapshot()[0].Sequence; got != 5 {
		t.Fatalf("TrustedValidators promoted pending list: got sequence %d", got)
	}

	pubKey := list.PublisherKey(pub.masterPub)
	agg.RecordPeerSequence(2, pubKey, 10)
	agg.Tick()
	if got := agg.PublisherSnapshot()[0].Sequence; got != 10 {
		t.Fatalf("Tick did not promote pending list: got sequence %d", got)
	}
	if got := agg.PeerSequence(2, pubKey); got != 10 {
		t.Fatalf("promotion recorded retained maximum instead of promoted sequence: got %d", got)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.listCalls) != 1 || fake.listCalls[0].peerID != 1 ||
		fake.listCalls[0].version != 1 || !bytes.Equal(fake.listCalls[0].blob, blob10) {
		t.Fatalf("promoted v1 broadcast: %+v", fake.listCalls)
	}
	if len(fake.collectionCalls) != 0 {
		t.Fatalf("up-to-date v2 peer received promotion: %+v", fake.collectionCalls)
	}
}

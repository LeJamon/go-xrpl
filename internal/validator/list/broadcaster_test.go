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

type fakeBroadcaster struct {
	mu     sync.Mutex
	peers  []uint64
	onSend func()

	collectionCalls []sendCollectionCall
}

type sendCollectionCall struct {
	peerID   uint64
	manifest []byte
	blobs    []list.BroadcastBlob
	version  uint32
}

func newFakeBroadcaster(peers []uint64, _ ...map[uint64]bool) *fakeBroadcaster {
	return &fakeBroadcaster{peers: peers}
}

func (f *fakeBroadcaster) ActivePeers() []uint64 {
	out := make([]uint64, len(f.peers))
	copy(out, f.peers)
	return out
}

func (f *fakeBroadcaster) SendCollection(peerID uint64, manifest []byte, blobs []list.BroadcastBlob, version uint32) error {
	if f.onSend != nil {
		f.onSend()
	}
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

func TestBroadcastLatest_UsesCollectionForEveryPeer(t *testing.T) {
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

	fake := newFakeBroadcaster([]uint64{100, 200})
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
	if len(fake.collectionCalls) != 2 {
		t.Fatalf("every active peer must receive exactly one SendCollection; got %+v", fake.collectionCalls)
	}
	for _, call := range fake.collectionCalls {
		if len(call.blobs) != 1 {
			t.Fatalf("collection with no Remaining must carry single entry (current); got %d blobs", len(call.blobs))
		}
		if call.version < 2 {
			t.Fatalf("collection version must be ≥ 2; got %d", call.version)
		}
		if !bytes.Equal(call.manifest, pub.manifestB64) {
			t.Fatalf("collection manifest: got %q want %q", call.manifest, pub.manifestB64)
		}
		if call.blobs[0].Manifest != nil {
			t.Fatalf("blob without local manifest must preserve nil presence, got %q", call.blobs[0].Manifest)
		}
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

	fake := newFakeBroadcaster([]uint64{200})
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
	fake := newFakeBroadcaster([]uint64{100, 200})
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
	if len(fake.collectionCalls) != 2 {
		t.Fatalf("every peer must receive one collection, got %d", len(fake.collectionCalls))
	}
	for _, call := range fake.collectionCalls {
		if !bytes.Equal(call.manifest, pub.manifestB64) {
			t.Fatalf("collection manifest: got %q want %q", call.manifest, pub.manifestB64)
		}
		if len(call.blobs) != 1 || call.blobs[0].Manifest == nil ||
			!bytes.Equal(call.blobs[0].Manifest, pub.manifestB64) {
			t.Fatalf("local manifest was not preserved: %+v", call.blobs)
		}
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
	fake := newFakeBroadcaster([]uint64{1, 2, 3})
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

func TestSendCachedToPeer_ReplaysCurrentAndFutureOnce(t *testing.T) {
	pub := newPublisher(t, 0x65, 0x66)
	validator := derivedValidatorKey(0x67)
	now := fixedClock()()
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

	blob5, sig5 := pub.signList(t, 5, 0, now.Add(48*time.Hour).Unix(), [][33]byte{validator})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob5, sig5, 1, "site://"); d != list.Accepted {
		t.Fatalf("seq=5 apply: %s", d)
	}
	blob10, sig10 := pub.signList(t, 10, now.Add(time.Hour).Unix(), now.Add(48*time.Hour).Unix(), [][33]byte{validator})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob10, sig10, 1, "site://"); d != list.Pending {
		t.Fatalf("seq=10 apply: %s", d)
	}
	blob15, sig15 := pub.signList(t, 15, now.Add(3*time.Hour).Unix(), now.Add(48*time.Hour).Unix(), [][33]byte{validator})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob15, sig15, 1, "site://"); d != list.Pending {
		t.Fatalf("seq=15 apply: %s", d)
	}

	fake := newFakeBroadcaster([]uint64{7, 8})
	agg.SetBroadcaster(fake)
	agg.SendCachedToPeer(42)

	fake.mu.Lock()
	if len(fake.collectionCalls) != 1 {
		fake.mu.Unlock()
		t.Fatalf("cached publisher list was sent %d times, want once", len(fake.collectionCalls))
	}
	call := fake.collectionCalls[0]
	fake.mu.Unlock()
	if call.peerID != 42 {
		t.Fatalf("cached list sent to peer %d, want 42", call.peerID)
	}
	if !bytes.Equal(call.manifest, pub.manifestB64) {
		t.Fatalf("publisher manifest: got %q want %q", call.manifest, pub.manifestB64)
	}
	if call.version < 2 || len(call.blobs) != 3 {
		t.Fatalf("cached collection version/blobs: version=%d blobs=%d", call.version, len(call.blobs))
	}
	for i, want := range [][]byte{blob5, blob10, blob15} {
		if !bytes.Equal(call.blobs[i].Blob, want) {
			t.Fatalf("blob %d changed or reordered", i)
		}
	}
	for i, want := range [][]byte{sig5, sig10, sig15} {
		if !bytes.Equal(call.blobs[i].Signature, want) {
			t.Fatalf("signature %d changed or reordered", i)
		}
		if call.blobs[i].Manifest != nil {
			t.Fatalf("blob %d unexpectedly gained a local manifest", i)
		}
	}

	agg.SendCachedToPeer(42)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.collectionCalls) != 1 {
		t.Fatalf("peer sequence suppression sent %d collections on repeat, want one", len(fake.collectionCalls))
	}
	if got := agg.PeerSequence(42, list.PublisherKey(pub.masterPub)); got != 15 {
		t.Fatalf("peer sequence: got %d want 15", got)
	}
}

func TestSendCachedToPeerReleasesAggregatorLockBeforeSend(t *testing.T) {
	pub := newPublisher(t, 0x68, 0x69)
	validator := derivedValidatorKey(0x6a)
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
	now := fixedClock()()
	blob, sig := pub.signList(t, 1, 0, now.Add(24*time.Hour).Unix(), [][33]byte{validator})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob, sig, 1, "site://"); d != list.Accepted {
		t.Fatalf("apply: %s", d)
	}

	callback := make(chan struct{})
	fake := newFakeBroadcaster(nil)
	fake.onSend = func() {
		_ = agg.PublisherSnapshot()
		close(callback)
	}
	agg.SetBroadcaster(fake)

	done := make(chan struct{})
	go func() {
		agg.SendCachedToPeer(42)
		close(done)
	}()
	select {
	case <-callback:
	case <-time.After(time.Second):
		t.Fatal("SendCollection could not re-enter aggregator; SendCachedToPeer may hold aggregator lock")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SendCachedToPeer did not complete")
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
	fake := newFakeBroadcaster([]uint64{1, 2})
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
	if len(fake.collectionCalls) != 1 || fake.collectionCalls[0].peerID != 1 ||
		fake.collectionCalls[0].version != 2 || len(fake.collectionCalls[0].blobs) != 2 ||
		!bytes.Equal(fake.collectionCalls[0].blobs[0].Blob, blob10) ||
		!bytes.Equal(fake.collectionCalls[0].blobs[1].Blob, blob15) {
		t.Fatalf("promoted collection broadcast: %+v", fake.collectionCalls)
	}
}

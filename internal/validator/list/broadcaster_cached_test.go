package list_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/validator/list"
)

type cachedCollectionCall struct {
	peerID   uint64
	manifest []byte
	blobs    []list.BroadcastBlob
}

type cachedBroadcaster struct {
	peers []uint64
	calls []cachedCollectionCall
}

func (b *cachedBroadcaster) ActivePeers() []uint64 {
	return append([]uint64(nil), b.peers...)
}

func (b *cachedBroadcaster) SendCollection(peerID uint64, manifest []byte, blobs []list.BroadcastBlob, _ uint32) error {
	b.calls = append(b.calls, cachedCollectionCall{
		peerID:   peerID,
		manifest: append([]byte(nil), manifest...),
		blobs:    append([]list.BroadcastBlob(nil), blobs...),
	})
	return nil
}

func TestSendCachedToPeerOnlySendsAvailablePublishers(t *testing.T) {
	available := newPublisher(t, 0x6b, 0x6c)
	expired := newPublisher(t, 0x6d, 0x6e)
	pending := newPublisher(t, 0x6f, 0x70)
	revoked := newPublisher(t, 0x71, 0x72)
	unavailable := newPublisher(t, 0x73, 0x74)
	publishers := []list.PublisherKey{
		list.PublisherKey(available.masterPub),
		list.PublisherKey(expired.masterPub),
		list.PublisherKey(pending.masterPub),
		list.PublisherKey(revoked.masterPub),
		list.PublisherKey(unavailable.masterPub),
	}
	publisherManifests := manifest.NewCache()
	revocation, err := manifest.Deserialize(buildRevocation(t, revoked.masterPub, revoked.masterPriv))
	if err != nil {
		t.Fatalf("revocation deserialize: %v", err)
	}
	if got := publisherManifests.ApplyManifest(revocation); got != manifest.Accepted {
		t.Fatalf("revocation apply: %s", got)
	}
	clockNow := fixedClock()()
	agg, err := list.New(list.Config{
		PublisherKeys:      publishers,
		Threshold:          1,
		ValidatorManifests: manifest.NewCache(),
		PublisherManifests: publisherManifests,
		Clock:              func() time.Time { return clockNow },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := clockNow
	validator := derivedValidatorKey(0x75)
	apply := func(t *testing.T, pub *publisherFixture, sequence uint32, effective, expiration time.Time) list.Disposition {
		t.Helper()
		var effectiveUnix int64
		if !effective.IsZero() {
			effectiveUnix = effective.Unix()
		}
		blob, signature := pub.signList(t, sequence, effectiveUnix, expiration.Unix(), [][33]byte{validator})
		d, _, _ := agg.ApplyList(pub.manifestB64, blob, signature, 1, "site://")
		return d
	}

	if d := apply(t, available, 5, time.Time{}, now.Add(72*time.Hour)); d != list.Accepted {
		t.Fatalf("available list: got %s", d)
	}
	if d := apply(t, expired, 5, time.Time{}, now.Add(24*time.Hour)); d != list.Accepted {
		t.Fatalf("expiring list: got %s", d)
	}
	if d := apply(t, pending, 5, now.Add(72*time.Hour), now.Add(96*time.Hour)); d != list.Pending {
		t.Fatalf("pending-only list: got %s", d)
	}
	clockNow = now.Add(48 * time.Hour)
	agg.Tick()

	broadcaster := &cachedBroadcaster{peers: []uint64{88, 99}}
	agg.SetBroadcaster(broadcaster)
	agg.SendCachedToPeer(77)

	if len(broadcaster.calls) != 1 {
		t.Fatalf("cached send count: got %d want 1", len(broadcaster.calls))
	}
	call := broadcaster.calls[0]
	if call.peerID != 77 || !bytes.Equal(call.manifest, available.manifestB64) {
		t.Fatalf("cached send: peer=%d manifest=%q", call.peerID, call.manifest)
	}
	if len(call.blobs) != 1 {
		t.Fatalf("available collection blobs: got %d want 1", len(call.blobs))
	}
	for _, item := range agg.PublisherSnapshot() {
		switch {
		case bytes.Equal(item.MasterKey[:], available.masterPub[:]):
			if item.Status != list.StatusAvailable {
				t.Fatalf("available status changed to %s", item.Status)
			}
		case bytes.Equal(item.MasterKey[:], expired.masterPub[:]):
			if item.Status != list.StatusExpired {
				t.Fatalf("expired status changed to %s", item.Status)
			}
		case bytes.Equal(item.MasterKey[:], pending.masterPub[:]):
			if item.Status != list.StatusUnavailable {
				t.Fatalf("pending-only status changed to %s", item.Status)
			}
		case bytes.Equal(item.MasterKey[:], revoked.masterPub[:]):
			if item.Status != list.StatusRevoked {
				t.Fatalf("revoked status changed to %s", item.Status)
			}
		case bytes.Equal(item.MasterKey[:], unavailable.masterPub[:]):
			if item.Status != list.StatusUnavailable {
				t.Fatalf("unavailable status changed to %s", item.Status)
			}
		}
	}
}

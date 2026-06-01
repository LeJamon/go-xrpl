package list_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/validator/list"
)

func TestAggregator_Cache_WriteThenRoundTripLoad(t *testing.T) {
	pub := newPublisher(t, 0x21, 0x22)
	v1 := derivedValidatorKey(0x30)

	dir := t.TempDir()
	pubKey := list.PublisherKey(pub.masterPub)

	// First aggregator: write the cache by applying an accepted list.
	src, err := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{pubKey},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         fixedClock(),
	})
	if err != nil {
		t.Fatalf("New src: %v", err)
	}
	if err := src.SetCacheDir(dir); err != nil {
		t.Fatalf("SetCacheDir: %v", err)
	}

	now := fixedClock()()
	exp := now.Add(24 * time.Hour).Unix()
	blob, sig := pub.signList(t, 7, 0, exp, [][33]byte{v1})
	if d, _, _ := src.ApplyList(pub.manifestB64, blob, sig, 1, "src://"); d != list.Accepted {
		t.Fatalf("apply disposition: %s", d)
	}

	// Cache file must be present at the rippled-conformant path.
	cachePath := filepath.Join(dir, "cache."+hex.EncodeToString(pub.masterPub[:]))
	body, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("read cache file: %v", err)
	}

	// Envelope shape must include rippled-compatible fields.
	var env struct {
		Manifest        string `json:"manifest"`
		PublicKey       string `json:"public_key"`
		Blob            string `json:"blob"`
		Signature       string `json:"signature"`
		Version         uint32 `json:"version"`
		RefreshInterval uint32 `json:"refresh_interval"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode cache JSON: %v", err)
	}
	if env.PublicKey != hex.EncodeToString(pub.masterPub[:]) {
		t.Fatalf("public_key: got %q want %q", env.PublicKey, hex.EncodeToString(pub.masterPub[:]))
	}
	if env.RefreshInterval != 24*60 {
		t.Fatalf("refresh_interval: got %d want %d", env.RefreshInterval, 24*60)
	}
	if env.Version == 0 || env.Manifest == "" || env.Blob == "" || env.Signature == "" {
		t.Fatalf("v1 envelope must populate manifest/blob/signature/version, got %+v", env)
	}

	// Second aggregator: hydrate from the same directory and check
	// the publisher reached StatusAvailable at the expected sequence.
	dst, err := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{pubKey},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         fixedClock(),
	})
	if err != nil {
		t.Fatalf("New dst: %v", err)
	}
	if err := dst.SetCacheDir(dir); err != nil {
		t.Fatalf("SetCacheDir dst: %v", err)
	}
	loaded := dst.LoadCache()
	if loaded != 1 {
		t.Fatalf("LoadCache: got %d want 1", loaded)
	}
	snap := dst.PublisherSnapshot()
	if len(snap) != 1 {
		t.Fatalf("PublisherSnapshot: len=%d", len(snap))
	}
	if snap[0].Status != list.StatusAvailable {
		t.Fatalf("status after hydrate: got %s want Available", snap[0].Status)
	}
	if snap[0].Sequence != 7 {
		t.Fatalf("sequence after hydrate: got %d want 7", snap[0].Sequence)
	}
}

func TestAggregator_Cache_LoadSkipsAlreadyAvailable(t *testing.T) {
	pub := newPublisher(t, 0x31, 0x32)
	v1 := derivedValidatorKey(0x40)
	v2 := derivedValidatorKey(0x41)

	dir := t.TempDir()
	pubKey := list.PublisherKey(pub.masterPub)

	agg, err := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{pubKey},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         fixedClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := agg.SetCacheDir(dir); err != nil {
		t.Fatalf("SetCacheDir: %v", err)
	}

	now := fixedClock()()
	exp := now.Add(24 * time.Hour).Unix()

	// Seq 3 lands first and writes the cache.
	blob3, sig3 := pub.signList(t, 3, 0, exp, [][33]byte{v1})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob3, sig3, 1, "p1://"); d != list.Accepted {
		t.Fatalf("seq=3 disposition: %s", d)
	}

	// Seq 9 supersedes — the aggregator is now Available at seq 9 and
	// the cache file on disk is also at seq 9. Calling LoadCache here
	// must NOT re-apply seq 9 (or seq 3) over the live state.
	blob9, sig9 := pub.signList(t, 9, 0, exp, [][33]byte{v1, v2})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob9, sig9, 1, "p1://"); d != list.Accepted {
		t.Fatalf("seq=9 disposition: %s", d)
	}

	// LoadCache must skip the publisher because it is already
	// StatusAvailable, mirroring rippled's loadLists() skip.
	if loaded := agg.LoadCache(); loaded != 0 {
		t.Fatalf("LoadCache after Available: got %d want 0", loaded)
	}
	snap := agg.PublisherSnapshot()
	if snap[0].Sequence != 9 {
		t.Fatalf("sequence drift: got %d want 9", snap[0].Sequence)
	}
}

func TestAggregator_Cache_DisabledDirNoFile(t *testing.T) {
	pub := newPublisher(t, 0x41, 0x42)
	v1 := derivedValidatorKey(0x50)

	dir := t.TempDir()

	agg, err := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         fixedClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Cache disabled (SetCacheDir never called).
	now := fixedClock()()
	exp := now.Add(24 * time.Hour).Unix()
	blob, sig := pub.signList(t, 1, 0, exp, [][33]byte{v1})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob, sig, 1, "p1://"); d != list.Accepted {
		t.Fatalf("apply: %s", d)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("cache dir written without SetCacheDir: %d entries", len(entries))
	}
}

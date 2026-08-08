package list_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/validator/list"
	"github.com/LeJamon/go-xrpl/protocol"
)

func TestAggregator_Cache_WriteThenRoundTripLoad(t *testing.T) {
	pub := newPublisher(t, 0x21, 0x22)
	v1 := derivedValidatorKey(0x30)

	dir := t.TempDir()
	pubKey := list.PublisherKey(pub.masterPub)

	// First aggregator: write the cache by applying an accepted list.
	src, err := list.New(list.Config{
		PublisherKeys:      []list.PublisherKey{pubKey},
		Threshold:          1,
		ValidatorManifests: manifest.NewCache(),
		PublisherManifests: manifest.NewCache(),
		Clock:              fixedClock(),
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
		PublisherKeys:      []list.PublisherKey{pubKey},
		Threshold:          1,
		ValidatorManifests: manifest.NewCache(),
		PublisherManifests: manifest.NewCache(),
		Clock:              fixedClock(),
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
		PublisherKeys:      []list.PublisherKey{pubKey},
		Threshold:          1,
		ValidatorManifests: manifest.NewCache(),
		PublisherManifests: manifest.NewCache(),
		Clock:              fixedClock(),
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
		PublisherKeys:      []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:          1,
		ValidatorManifests: manifest.NewCache(),
		PublisherManifests: manifest.NewCache(),
		Clock:              fixedClock(),
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

func TestAggregatorLoadCacheRejectsVersionShapeMismatch(t *testing.T) {
	pub := newPublisher(t, 0x35, 0x36)
	validator := derivedValidatorKey(0x37)
	blob, signature := pub.signList(t, 1, 0, fixedClock()().Add(24*time.Hour).Unix(), [][33]byte{validator})
	tests := map[string]map[string]any{
		"v1 with blobs_v2": {
			"manifest": string(pub.manifestB64), "version": 1,
			"blob": string(blob), "signature": string(signature),
			"blobs_v2": []map[string]any{{"blob": string(blob), "signature": string(signature)}},
		},
		"v2 with v1 fields": {
			"manifest": string(pub.manifestB64), "version": 2,
			"blob": string(blob), "signature": string(signature),
			"blobs_v2": []map[string]any{{"blob": string(blob), "signature": string(signature)}},
		},
	}
	for name, env := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
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
			if err := agg.SetCacheDir(dir); err != nil {
				t.Fatalf("SetCacheDir: %v", err)
			}
			body, err := json.Marshal(env)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			path := filepath.Join(dir, "cache."+hex.EncodeToString(pub.masterPub[:]))
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if loaded := agg.LoadCache(); loaded != 0 {
				t.Fatalf("LoadCache accepted mismatched envelope: %d", loaded)
			}
			if got := agg.PublisherSnapshot()[0].Sequence; got != 0 {
				t.Fatalf("mismatched envelope mutated publisher: sequence=%d", got)
			}
		})
	}
}

func TestAggregator_Cache_V2RotationPreservesLocalManifestThroughPromotion(t *testing.T) {
	pub := newPublisher(t, 0x45, 0x46)
	validator := derivedValidatorKey(0x51)
	pubKey := list.PublisherKey(pub.masterPub)
	var now = fixedClock()()
	localM2, eph2Priv := rotationManifest(t, pub, 0x47, 2)

	dir := t.TempDir()
	src, err := list.New(list.Config{
		PublisherKeys:      []list.PublisherKey{pubKey},
		Threshold:          1,
		ValidatorManifests: manifest.NewCache(),
		PublisherManifests: manifest.NewCache(),
		Clock:              func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New src: %v", err)
	}
	if err := src.SetCacheDir(dir); err != nil {
		t.Fatalf("SetCacheDir src: %v", err)
	}
	exp := now.Add(48 * time.Hour).Unix()
	blob1, sig1 := pub.signList(t, 5, 0, exp, [][33]byte{validator})
	blob2, sig2 := signListWithKey(t, eph2Priv, 10, now.Add(time.Hour).Unix(), exp, [][33]byte{validator})
	dispositions, _, maxSequence := src.ApplyCollection(&message.ValidatorListCollection{
		Version:  2,
		Manifest: pub.manifestB64,
		Blobs: []message.ValidatorBlobInfo{
			{Blob: blob1, Signature: sig1},
			{Manifest: localM2, Blob: blob2, Signature: sig2},
		},
	}, "cache://src")
	if len(dispositions) != 2 || dispositions[0] != list.Accepted || dispositions[1] != list.Pending || maxSequence != 10 {
		t.Fatalf("ApplyCollection: got %v", dispositions)
	}
	path := filepath.Join(dir, "cache."+hex.EncodeToString(pub.masterPub[:]))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	var env struct {
		Manifest string `json:"manifest"`
		Version  uint32 `json:"version"`
		Blobs    []struct {
			Manifest *string `json:"manifest"`
		} `json:"blobs_v2"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode cache: %v", err)
	}
	if env.Version != 2 || env.Manifest != string(pub.manifestB64) || len(env.Blobs) != 2 {
		t.Fatalf("unexpected v2 envelope: %+v", env)
	}
	if env.Blobs[0].Manifest != nil {
		t.Fatalf("current blob must omit local manifest: %+v", *env.Blobs[0].Manifest)
	}
	if env.Blobs[1].Manifest == nil || *env.Blobs[1].Manifest != string(localM2) {
		t.Fatalf("pending local manifest lost: %+v", env.Blobs[1].Manifest)
	}

	dst, err := list.New(list.Config{
		PublisherKeys:      []list.PublisherKey{pubKey},
		Threshold:          1,
		ValidatorManifests: manifest.NewCache(),
		PublisherManifests: manifest.NewCache(),
		Clock:              func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New dst: %v", err)
	}
	if err := dst.SetCacheDir(dir); err != nil {
		t.Fatalf("SetCacheDir dst: %v", err)
	}
	if loaded := dst.LoadCache(); loaded != 1 {
		t.Fatalf("LoadCache: got %d want 1", loaded)
	}
	snapshot := dst.PublisherSnapshot()
	if len(snapshot) != 1 || snapshot[0].Sequence != 5 || snapshot[0].RawLocalManifestSet {
		t.Fatalf("current state after hydrate: %+v", snapshot)
	}
	if len(snapshot[0].Remaining) != 1 {
		t.Fatalf("pending state after hydrate: %+v", snapshot[0].Remaining)
	}
	pending := snapshot[0].Remaining[10]
	if pending == nil || !pending.RawLocalManifestSet {
		t.Fatalf("pending local manifest after hydrate: %+v", pending)
	}

	now = now.Add(2 * time.Hour)
	dst.Tick()
	snapshot = dst.PublisherSnapshot()
	if snapshot[0].Sequence != 10 || !snapshot[0].RawLocalManifestSet {
		t.Fatalf("promoted rotation lost local manifest: %+v", snapshot[0])
	}
}

func rotationManifest(t *testing.T, pub *publisherFixture, ephSeed byte, sequence uint32) ([]byte, ed25519.PrivateKey) {
	t.Helper()
	ephPub32, ephPriv := deterministicKey(ephSeed)
	var ephPub [33]byte
	copy(ephPub[:], append([]byte{0xED}, ephPub32...))
	raw := buildManifest(t, pub.masterPub, pub.masterPriv, ephPub, ephPriv, sequence)
	return []byte(base64.StdEncoding.EncodeToString(raw)), ephPriv
}

func signListWithKey(t *testing.T, privateKey ed25519.PrivateKey, sequence uint32, validFromUnix, validUntilUnix int64, validatorMasters [][33]byte) ([]byte, []byte) {
	t.Helper()
	type entry struct {
		ValidationPublicKey string `json:"validation_public_key"`
	}
	type body struct {
		Sequence   uint32  `json:"sequence"`
		Expiration uint32  `json:"expiration"`
		Effective  uint32  `json:"effective,omitempty"`
		Validators []entry `json:"validators"`
	}
	v := body{Sequence: sequence, Expiration: uint32(validUntilUnix - protocol.RippleEpochUnix)}
	if validFromUnix > 0 {
		v.Effective = uint32(validFromUnix - protocol.RippleEpochUnix)
	}
	for _, master := range validatorMasters {
		v.Validators = append(v.Validators, entry{ValidationPublicKey: hex.EncodeToString(master[:])})
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("blob JSON marshal: %v", err)
	}
	return []byte(base64.StdEncoding.EncodeToString(raw)), []byte(hex.EncodeToString(ed25519.Sign(privateKey, raw)))
}

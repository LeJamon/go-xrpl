package list_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/manifest"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/internal/validator/list"
	"github.com/LeJamon/goXRPLd/protocol"
)

// rippleEpochOffset mirrors the constant in blob.go — duplicated here so
// the test doesn't depend on package-private fields.
const rippleEpochOffset int64 = 946684800

// fixedClock yields a deterministic "now" so test expectations don't
// drift across CI runs.
func fixedClock() func() time.Time {
	t := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// publisherFixture is a synthetic publisher used in test scenarios.
type publisherFixture struct {
	masterPub  [33]byte
	masterPriv ed25519.PrivateKey
	ephPub     [33]byte
	ephPriv    ed25519.PrivateKey
	// manifestB64 is the base64-encoded manifest STObject bytes (the
	// wire form TMValidatorList carries as `manifest`).
	manifestB64 []byte
}

func newPublisher(t *testing.T, masterSeed, ephSeed byte) *publisherFixture {
	t.Helper()
	masterPub32, masterPriv := deterministicKey(masterSeed)
	ephPub32, ephPriv := deterministicKey(ephSeed)

	var mp, ep [33]byte
	copy(mp[:], append([]byte{0xED}, masterPub32...))
	copy(ep[:], append([]byte{0xED}, ephPub32...))

	manifestRaw := buildManifest(t, mp, masterPriv, ep, ephPriv, 1)
	b64 := []byte(base64.StdEncoding.EncodeToString(manifestRaw))

	return &publisherFixture{
		masterPub:   mp,
		masterPriv:  masterPriv,
		ephPub:      ep,
		ephPriv:     ephPriv,
		manifestB64: b64,
	}
}

// signList produces the (blob, signature) pair the aggregator expects:
// a base64-encoded JSON envelope and a hex-encoded ed25519 signature
// over the base64-decoded JSON bytes (matching rippled's verify which
// signs the decoded form).
func (p *publisherFixture) signList(t *testing.T, sequence uint32, validFromUnix, validUntilUnix int64, validatorMasters [][33]byte) (blobB64, signatureHex []byte) {
	t.Helper()
	type entry struct {
		ValidationPublicKey string `json:"validation_public_key"`
		Manifest            string `json:"manifest,omitempty"`
	}
	type body struct {
		Sequence   uint32  `json:"sequence"`
		Expiration uint32  `json:"expiration"`
		Effective  uint32  `json:"effective,omitempty"`
		Validators []entry `json:"validators"`
	}
	b := body{
		Sequence:   sequence,
		Expiration: uint32(validUntilUnix - rippleEpochOffset),
	}
	if validFromUnix > 0 {
		b.Effective = uint32(validFromUnix - rippleEpochOffset)
	}
	for _, mk := range validatorMasters {
		b.Validators = append(b.Validators, entry{
			ValidationPublicKey: hex.EncodeToString(mk[:]),
		})
	}
	jsonBytes, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("blob JSON marshal: %v", err)
	}
	blobB64 = []byte(base64.StdEncoding.EncodeToString(jsonBytes))
	sig := ed25519.Sign(p.ephPriv, jsonBytes)
	signatureHex = []byte(hex.EncodeToString(sig))
	return blobB64, signatureHex
}

func TestAggregator_ApplyList_Accepted_SinglePublisher_SingleValidator(t *testing.T) {
	pub := newPublisher(t, 0x01, 0x02)
	val1 := derivedValidatorKey(0x10)

	agg, err := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         fixedClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var changeMu sync.Mutex
	var lastMasters [][33]byte
	var changes int
	agg.OnChange(func(_ []consensus.NodeID, m [][33]byte) {
		changeMu.Lock()
		defer changeMu.Unlock()
		lastMasters = append([][33]byte(nil), m...)
		changes++
	})

	now := fixedClock()()
	blob, sig := pub.signList(t, 1, 0, now.Add(24*time.Hour).Unix(), [][33]byte{val1})
	disp, key, _ := agg.ApplyList(pub.manifestB64, blob, sig, 1, "test://")
	if disp != list.Accepted {
		t.Fatalf("disposition: got %s want Accepted", disp)
	}
	if key != list.PublisherKey(pub.masterPub) {
		t.Fatalf("publisher key mismatch")
	}

	changeMu.Lock()
	if changes != 1 {
		t.Fatalf("OnChange invocations: got %d want 1", changes)
	}
	if len(lastMasters) != 1 || lastMasters[0] != val1 {
		t.Fatalf("trusted set: got %x want [%x]", lastMasters, val1)
	}
	changeMu.Unlock()

	// Reapplying the same list is SameSequence and triggers no OnChange.
	disp, _, _ = agg.ApplyList(pub.manifestB64, blob, sig, 1, "test://")
	if disp != list.SameSequence {
		t.Fatalf("re-apply disposition: got %s want SameSequence", disp)
	}
	changeMu.Lock()
	if changes != 1 {
		t.Fatalf("OnChange invoked on resend; got %d want 1", changes)
	}
	changeMu.Unlock()
}

func TestAggregator_ApplyList_Threshold_TwoOfThree(t *testing.T) {
	pub1 := newPublisher(t, 0x01, 0x02)
	pub2 := newPublisher(t, 0x03, 0x04)
	pub3 := newPublisher(t, 0x05, 0x06)
	v1 := derivedValidatorKey(0x11)
	v2 := derivedValidatorKey(0x12)
	v3 := derivedValidatorKey(0x13)

	agg, err := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{
			list.PublisherKey(pub1.masterPub),
			list.PublisherKey(pub2.masterPub),
			list.PublisherKey(pub3.masterPub),
		},
		Threshold: 2,
		Manifests: manifest.NewCache(),
		Clock:     fixedClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	now := fixedClock()()
	exp := now.Add(24 * time.Hour).Unix()

	// Publisher 1 lists {v1, v2}. Threshold not yet met.
	blob, sig := pub1.signList(t, 1, 0, exp, [][33]byte{v1, v2})
	if d, _, _ := agg.ApplyList(pub1.manifestB64, blob, sig, 1, "p1://"); d != list.Accepted {
		t.Fatalf("pub1 disposition: %s", d)
	}
	if _, m := agg.TrustedValidators(); len(m) != 0 {
		t.Fatalf("trusted after one publisher: want 0, got %d", len(m))
	}

	// Publisher 2 lists {v2, v3}. v2 now has 2 publisher votes ≥ threshold.
	blob, sig = pub2.signList(t, 1, 0, exp, [][33]byte{v2, v3})
	if d, _, _ := agg.ApplyList(pub2.manifestB64, blob, sig, 1, "p2://"); d != list.Accepted {
		t.Fatalf("pub2 disposition: %s", d)
	}
	_, masters := agg.TrustedValidators()
	if len(masters) != 1 || masters[0] != v2 {
		t.Fatalf("expected single trusted validator v2, got %d entries", len(masters))
	}

	// Publisher 3 lists {v1, v3}. Now v1, v2, v3 all have 2+ votes.
	blob, sig = pub3.signList(t, 1, 0, exp, [][33]byte{v1, v3})
	if d, _, _ := agg.ApplyList(pub3.manifestB64, blob, sig, 1, "p3://"); d != list.Accepted {
		t.Fatalf("pub3 disposition: %s", d)
	}
	_, masters = agg.TrustedValidators()
	if len(masters) != 3 {
		t.Fatalf("expected 3 trusted validators, got %d", len(masters))
	}
	sortMasters(masters)
	expected := [][33]byte{v1, v2, v3}
	sortMasters(expected)
	for i := range expected {
		if masters[i] != expected[i] {
			t.Fatalf("trusted[%d] mismatch", i)
		}
	}
}

func TestAggregator_ApplyList_Stale(t *testing.T) {
	pub := newPublisher(t, 0x01, 0x02)
	v1 := derivedValidatorKey(0x10)

	agg, err := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         fixedClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	now := fixedClock()()
	exp := now.Add(24 * time.Hour).Unix()

	// Apply sequence 5.
	blob, sig := pub.signList(t, 5, 0, exp, [][33]byte{v1})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob, sig, 1, "test://"); d != list.Accepted {
		t.Fatalf("seq=5 disposition: %s", d)
	}
	// Then sequence 3 should be Stale.
	blob3, sig3 := pub.signList(t, 3, 0, exp, [][33]byte{v1})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob3, sig3, 1, "test://"); d != list.Stale {
		t.Fatalf("seq=3 disposition: got %s want Stale", d)
	}
}

func TestAggregator_ApplyList_UntrustedPublisher(t *testing.T) {
	pubInTrust := newPublisher(t, 0x01, 0x02)
	pubOther := newPublisher(t, 0x09, 0x0A)
	v1 := derivedValidatorKey(0x10)

	agg, err := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pubInTrust.masterPub)},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         fixedClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	now := fixedClock()()
	blob, sig := pubOther.signList(t, 1, 0, now.Add(24*time.Hour).Unix(), [][33]byte{v1})
	d, _, _ := agg.ApplyList(pubOther.manifestB64, blob, sig, 1, "test://")
	if d != list.Untrusted {
		t.Fatalf("disposition: got %s want Untrusted", d)
	}
	if _, m := agg.TrustedValidators(); len(m) != 0 {
		t.Fatalf("untrusted publisher should not contribute, got %d trusted", len(m))
	}
}

func TestAggregator_ApplyList_BadSignature(t *testing.T) {
	pub := newPublisher(t, 0x01, 0x02)
	v1 := derivedValidatorKey(0x10)

	agg, err := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         fixedClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	now := fixedClock()()
	blob, sig := pub.signList(t, 1, 0, now.Add(24*time.Hour).Unix(), [][33]byte{v1})
	// Corrupt the signature.
	sig[5] ^= 0xff
	d, _, _ := agg.ApplyList(pub.manifestB64, blob, sig, 1, "test://")
	if d != list.Invalid {
		t.Fatalf("disposition: got %s want Invalid", d)
	}
}

func TestAggregator_ApplyList_Expired(t *testing.T) {
	pub := newPublisher(t, 0x01, 0x02)
	v1 := derivedValidatorKey(0x10)

	agg, err := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         fixedClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	now := fixedClock()()
	// Expiration in the past relative to fixedClock.
	exp := now.Add(-1 * time.Hour).Unix()
	blob, sig := pub.signList(t, 1, 0, exp, [][33]byte{v1})
	d, _, _ := agg.ApplyList(pub.manifestB64, blob, sig, 1, "test://")
	if d != list.Expired {
		t.Fatalf("disposition: got %s want Expired", d)
	}
	if _, m := agg.TrustedValidators(); len(m) != 0 {
		t.Fatalf("expired list should not contribute, got %d trusted", len(m))
	}
}

func TestAggregator_ApplyList_UnsupportedVersion(t *testing.T) {
	pub := newPublisher(t, 0x01, 0x02)
	agg, _ := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         fixedClock(),
	})
	d, _, _ := agg.ApplyList(pub.manifestB64, []byte("garbage"), []byte("00"), 99, "test://")
	if d != list.UnsupportedVersion {
		t.Fatalf("disposition: got %s want UnsupportedVersion", d)
	}
}

// TestAggregator_ApplyList_BadManifest pins the rippled-faithful mapping
// for malformed publisher manifests: rippled ValidatorList.cpp:1363-1366
// folds both `!m` (manifest deserialize failure) and unknown-publisher
// into ListDisposition::untrusted (charged at feeUselessData), never at
// the heavier feeInvalidSignature. Without this test a future change
// could regress to charging honest peers feeInvalidSignature for
// forwarding lists from broken publishers.
func TestAggregator_ApplyList_BadManifest(t *testing.T) {
	agg, _ := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey{0xED, 1, 2, 3}},
		Threshold:     1,
		Clock:         fixedClock(),
	})
	d, _, _ := agg.ApplyList([]byte("!@not_base64"), []byte("blob"), []byte("00"), 1, "test://")
	if d != list.Untrusted {
		t.Fatalf("disposition: got %s want Untrusted", d)
	}
}

// TestAggregator_ApplyList_MissingRequiredField pins the rippled-faithful
// blob-validation requirement that `sequence`, `expiration`, and
// `validators` are JSON-present (rippled ValidatorList.cpp:1394-1397
// returns Invalid otherwise). Without this guard, a publisher feed
// omitting `validators` would be silently accepted with an empty
// validator set.
func TestAggregator_ApplyList_MissingRequiredField(t *testing.T) {
	pub := newPublisher(t, 0x01, 0x02)
	agg, _ := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         fixedClock(),
	})
	// Blob with only `sequence` and `expiration`, no `validators` array.
	now := fixedClock()()
	body := map[string]any{
		"sequence":   uint32(1),
		"expiration": uint32(now.Add(24*time.Hour).Unix() - rippleEpochOffset),
	}
	jsonBytes, _ := json.Marshal(body)
	blob := []byte(base64.StdEncoding.EncodeToString(jsonBytes))
	sig := ed25519.Sign(pub.ephPriv, jsonBytes)
	d, _, _ := agg.ApplyList(pub.manifestB64, blob, []byte(hex.EncodeToString(sig)), 1, "test://")
	if d != list.Invalid {
		t.Fatalf("disposition: got %s want Invalid (missing validators field)", d)
	}
}

func TestAggregator_ApplyCollection_AcceptedAndStale(t *testing.T) {
	pub := newPublisher(t, 0x01, 0x02)
	v1 := derivedValidatorKey(0x10)

	agg, _ := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         fixedClock(),
	})

	now := fixedClock()()
	blob1, sig1 := pub.signList(t, 1, 0, now.Add(24*time.Hour).Unix(), [][33]byte{v1})
	blob5, sig5 := pub.signList(t, 5, 0, now.Add(48*time.Hour).Unix(), [][33]byte{v1})
	coll := &message.ValidatorListCollection{
		Version:  2,
		Manifest: pub.manifestB64,
		Blobs: []message.ValidatorBlobInfo{
			{Blob: blob5, Signature: sig5},
			{Blob: blob1, Signature: sig1},
		},
	}
	dispList, key, _ := agg.ApplyCollection(coll, "test://")
	if key != list.PublisherKey(pub.masterPub) {
		t.Fatalf("publisher key mismatch")
	}
	if len(dispList) != 2 {
		t.Fatalf("disposition count: %d", len(dispList))
	}
	// Blob 5 applied first (Accepted), blob 1 then Stale.
	if dispList[0] != list.Accepted {
		t.Fatalf("dispList[0]: %s", dispList[0])
	}
	if dispList[1] != list.Stale {
		t.Fatalf("dispList[1]: %s", dispList[1])
	}
}

func deterministicKey(seed byte) (pub32 []byte, priv ed25519.PrivateKey) {
	s := bytes.Repeat([]byte{seed}, ed25519.SeedSize)
	priv = ed25519.NewKeyFromSeed(s)
	pub32 = priv.Public().(ed25519.PublicKey)
	return
}

func derivedValidatorKey(seed byte) [33]byte {
	pub32, _ := deterministicKey(seed)
	var k [33]byte
	copy(k[:], append([]byte{0xED}, pub32...))
	return k
}

// buildManifest constructs a serialized manifest STObject (NOT base64-
// encoded — the caller is responsible for that wrapping when feeding
// it into a wire message). Both the master and ephemeral signatures
// are computed over the manifest's canonical signing preimage.
func buildManifest(t *testing.T, masterPub [33]byte, masterPriv ed25519.PrivateKey, ephPub [33]byte, ephPriv ed25519.PrivateKey, seq uint32) []byte {
	t.Helper()
	obj := map[string]any{
		"PublicKey":     hex.EncodeToString(masterPub[:]),
		"Sequence":      seq,
		"SigningPubKey": hex.EncodeToString(ephPub[:]),
	}
	preimage := manifestSigningPreimage(t, obj)
	ephSig := ed25519.Sign(ephPriv, preimage)
	obj["Signature"] = hex.EncodeToString(ephSig)
	masterSig := ed25519.Sign(masterPriv, preimage)
	obj["MasterSignature"] = hex.EncodeToString(masterSig)

	encoded, err := binarycodec.Encode(obj)
	if err != nil {
		t.Fatalf("binarycodec.Encode manifest: %v", err)
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode manifest hex: %v", err)
	}
	return raw
}

func manifestSigningPreimage(t *testing.T, src map[string]any) []byte {
	t.Helper()
	filtered := make(map[string]any, len(src))
	for k, v := range src {
		if k == "Signature" || k == "MasterSignature" {
			continue
		}
		filtered[k] = v
	}
	encoded, err := binarycodec.Encode(filtered)
	if err != nil {
		t.Fatalf("encode signing body: %v", err)
	}
	body, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode signing body hex: %v", err)
	}
	prefix := protocol.HashPrefixManifest
	out := make([]byte, 0, len(prefix)+len(body))
	out = append(out, prefix[:]...)
	out = append(out, body...)
	return out
}

func sortMasters(m [][33]byte) {
	sort.Slice(m, func(i, j int) bool {
		return string(m[i][:]) < string(m[j][:])
	})
}

// TestAggregator_ApplyList_PendingThenKnownSequence pins the rippled
// state-machine at ValidatorList.cpp:1414-1432: a future-dated blob
// stores into Remaining and returns Pending; a repeat at the same
// sequence returns KnownSequence (no-op). Without the Remaining queue
// the second arrival would also return Pending and the publisher
// would never promote.
func TestAggregator_ApplyList_PendingThenKnownSequence(t *testing.T) {
	pub := newPublisher(t, 0x01, 0x02)
	v1 := derivedValidatorKey(0x10)

	now := fixedClock()()
	mutableNow := now
	clk := func() time.Time { return mutableNow }

	agg, err := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         clk,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	futureEff := mutableNow.Add(2 * time.Hour).Unix()
	exp := mutableNow.Add(48 * time.Hour).Unix()

	blob, sig := pub.signList(t, 7, futureEff, exp, [][33]byte{v1})
	d, _, _ := agg.ApplyList(pub.manifestB64, blob, sig, 1, "test://")
	if d != list.Pending {
		t.Fatalf("first apply: got %s want Pending", d)
	}
	// No promotion yet → trusted set empty.
	if _, m := agg.TrustedValidators(); len(m) != 0 {
		t.Fatalf("trusted before promotion: want 0, got %d", len(m))
	}

	// Same sequence again — KnownSequence (re-arrival at a queued seq).
	d, _, _ = agg.ApplyList(pub.manifestB64, blob, sig, 1, "test://")
	if d != list.KnownSequence {
		t.Fatalf("re-apply at queued sequence: got %s want KnownSequence", d)
	}

	// Advance the clock past validFrom and Tick — rotation promotes
	// and the validator enters the trusted set.
	mutableNow = now.Add(3 * time.Hour)
	agg.Tick()
	_, masters := agg.TrustedValidators()
	if len(masters) != 1 || masters[0] != v1 {
		t.Fatalf("post-Tick trusted: want [v1] got %d entries", len(masters))
	}
}

// TestAggregator_ApplyList_EffectiveSet_Sentinel pins the rippled
// gating at ValidatorList.cpp:1682 — `effective` is only emitted in
// the RPC when the publisher blob actually carried the field. Without
// EffectiveSet, a missing `effective` flattens through the
// ripple-epoch offset into a synthetic 2000-Jan-01 timestamp.
func TestAggregator_ApplyList_EffectiveSet_Sentinel(t *testing.T) {
	pub := newPublisher(t, 0x01, 0x02)
	v1 := derivedValidatorKey(0x10)

	agg, _ := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         fixedClock(),
	})
	now := fixedClock()()
	// signList passes validFromUnix=0 which means we OMIT `effective`.
	blob, sig := pub.signList(t, 1, 0, now.Add(24*time.Hour).Unix(), [][33]byte{v1})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob, sig, 1, "test://"); d != list.Accepted {
		t.Fatalf("disposition: %s", d)
	}
	snap := agg.PublisherSnapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len: %d", len(snap))
	}
	if snap[0].EffectiveSet {
		t.Fatalf("EffectiveSet must be false when blob omits the field")
	}
}

// TestAggregator_ApplyList_Expired_ClearsValidators pins rippled
// removePublisherList(StatusExpired) at ValidatorList.cpp:1529-1542 —
// expiry clears the publisher's `list` so the RPC does not surface
// stale validator keys under `available=false`.
func TestAggregator_ApplyList_Expired_ClearsValidators(t *testing.T) {
	pub := newPublisher(t, 0x01, 0x02)
	v1 := derivedValidatorKey(0x10)

	agg, _ := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Clock:         fixedClock(),
	})
	now := fixedClock()()
	exp := now.Add(-1 * time.Hour).Unix() // expired
	blob, sig := pub.signList(t, 1, 0, exp, [][33]byte{v1})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob, sig, 1, "test://"); d != list.Expired {
		t.Fatalf("disposition: %s", d)
	}
	snap := agg.PublisherSnapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len: %d", len(snap))
	}
	if len(snap[0].Validators) != 0 {
		t.Fatalf("expired publisher must surface empty validator list; got %d", len(snap[0].Validators))
	}
}

// TestAggregator_ApplyList_Expired_SkipsEmbeddedManifests pins the
// rippled behaviour that embedded validator manifests carried by an
// expired blob are NOT applied to validatorManifests_. In rippled the
// embedded-manifest loop sits in updatePublisherList which runs only
// on the accepted branch of applyList; the expired branch goes through
// removePublisherList(StatusExpired) instead, which never touches the
// manifest cache.
func TestAggregator_ApplyList_Expired_SkipsEmbeddedManifests(t *testing.T) {
	pub := newPublisher(t, 0x01, 0x02)

	// Build a real validator master/ephemeral pair plus a signed
	// manifest so the manifest cache would accept it if asked.
	valMaster32, valMasterPriv := deterministicKey(0x20)
	valEph32, valEphPriv := deterministicKey(0x21)
	var valMaster, valEph [33]byte
	copy(valMaster[:], append([]byte{0xED}, valMaster32...))
	copy(valEph[:], append([]byte{0xED}, valEph32...))
	valManifestRaw := buildManifest(t, valMaster, valMasterPriv, valEph, valEphPriv, 1)
	valManifestB64 := base64.StdEncoding.EncodeToString(valManifestRaw)

	type entry struct {
		ValidationPublicKey string `json:"validation_public_key"`
		Manifest            string `json:"manifest,omitempty"`
	}
	type body struct {
		Sequence   uint32  `json:"sequence"`
		Expiration uint32  `json:"expiration"`
		Effective  uint32  `json:"effective,omitempty"`
		Validators []entry `json:"validators"`
	}
	now := fixedClock()()
	exp := now.Add(-1 * time.Hour).Unix() // expired
	b := body{
		Sequence:   1,
		Expiration: uint32(exp - rippleEpochOffset),
		Validators: []entry{{
			ValidationPublicKey: hex.EncodeToString(valMaster[:]),
			Manifest:            valManifestB64,
		}},
	}
	jsonBytes, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("blob marshal: %v", err)
	}
	blob := []byte(base64.StdEncoding.EncodeToString(jsonBytes))
	sigBytes := ed25519.Sign(pub.ephPriv, jsonBytes)
	sig := []byte(hex.EncodeToString(sigBytes))

	cache := manifest.NewCache()
	agg, _ := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:     1,
		Manifests:     cache,
		Clock:         fixedClock(),
	})
	if d, _, _ := agg.ApplyList(pub.manifestB64, blob, sig, 1, "test://"); d != list.Expired {
		t.Fatalf("disposition: %s", d)
	}
	if _, ok := cache.GetSigningKey(valMaster); ok {
		t.Fatalf("expired ingest must not seed embedded validator manifests into the cache")
	}
}

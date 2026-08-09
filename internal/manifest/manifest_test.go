package manifest_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	rootcrypto "github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

// secp256k1CurveOrderN is the order N of the secp256k1 curve, used to
// flip a low-S signature to its mathematically-equivalent high-S form
// (S' = N - S). Hard-coded here to avoid exporting the secp256k1
// package-private constant.
var secp256k1CurveOrderN, _ = new(big.Int).SetString(
	"FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141", 16,
)

// buildManifest constructs a serialized manifest with real ed25519
// signatures. Used by tests that want a valid starting point and then
// selectively corrupt fields. Returns the serialized manifest bytes and
// the two public keys (master, ephemeral) so callers can check the
// stored state after apply.
func buildManifest(t testing.TB, seq uint32, revoked bool, masterSeed, ephemeralSeed byte) (serialized []byte, masterPub [33]byte, ephemeralPub [33]byte) {
	t.Helper()

	masterPubBytes, masterPriv := deterministicEd25519Keypair(masterSeed)
	copy(masterPub[:], masterPubBytes)

	json := map[string]any{
		"PublicKey": hex.EncodeToString(masterPubBytes),
		"Sequence":  seq,
	}

	if !revoked {
		ephPubBytes, ephPriv := deterministicEd25519Keypair(ephemeralSeed)
		copy(ephemeralPub[:], ephPubBytes)
		json["SigningPubKey"] = hex.EncodeToString(ephPubBytes)
		// Ephemeral signature over the signing preimage.
		preimage := signingPreimageFromJSON(t, json)
		ephSig := ed25519.Sign(ed25519.PrivateKey(ephPriv), preimage)
		json["Signature"] = hex.EncodeToString(ephSig)
	}

	// Master signature over the same preimage. MasterSignature isn't a
	// signing field so including it in the JSON we hand to the codec
	// doesn't affect the preimage — but we compute the preimage from a
	// copy that also excludes MasterSignature for clarity.
	preimage := signingPreimageFromJSON(t, json)
	masterSig := ed25519.Sign(ed25519.PrivateKey(masterPriv), preimage)
	json["MasterSignature"] = hex.EncodeToString(masterSig)

	encoded, err := binarycodec.Encode(json)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	b, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode built hex: %v", err)
	}
	return b, masterPub, ephemeralPub
}

// signingPreimageFromJSON replicates the preimage construction the
// manifest package does internally: HashPrefixManifest || Encode(only
// signing fields). Kept in the test to catch preimage drift — if the
// package changes the preimage, this helper stays the old one and the
// test fails loudly.
func signingPreimageFromJSON(t testing.TB, src map[string]any) []byte {
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
	prefix := protocol.HashPrefixManifest()
	out := make([]byte, 0, len(prefix)+len(body))
	out = append(out, prefix[:]...)
	out = append(out, body...)
	return out
}

func BenchmarkDeserializeManifest(b *testing.B) {
	serialized, _, _ := buildManifest(b, 1, false, 0x01, 0x02)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := manifest.Deserialize(serialized); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVerifyManifest(b *testing.B) {
	serialized, _, _ := buildManifest(b, 1, false, 0x01, 0x02)
	m, err := manifest.Deserialize(serialized)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := m.Verify(); err != nil {
			b.Fatal(err)
		}
	}
}

func TestManifest_RejectsFieldsOutsideManifestFormat(t *testing.T) {
	serialized, _, _ := buildManifest(t, 1, false, 0x05, 0x06)
	for field, value := range map[string]any{
		"Flags":        uint32(0),
		"TxnSignature": "00",
	} {
		t.Run(field, func(t *testing.T) {
			decoded, err := binarycodec.Decode(hex.EncodeToString(serialized))
			require.NoError(t, err)
			decoded[field] = value
			encoded, err := binarycodec.Encode(decoded)
			require.NoError(t, err)
			withExtraField, err := hex.DecodeString(encoded)
			require.NoError(t, err)

			_, err = manifest.Deserialize(withExtraField)
			require.ErrorContains(t, err, "unexpected field "+field)
		})
	}
}

func TestManifest_RejectsOversizedPayloadBeforeDecode(t *testing.T) {
	_, err := manifest.Deserialize(make([]byte, manifest.MaxSerializedSize+1))
	require.ErrorContains(t, err, "payload exceeds 358 bytes")
}

func TestManifest_MaximumSerializedPayloadReachesDecoder(t *testing.T) {
	_, err := manifest.Deserialize(make([]byte, manifest.MaxSerializedSize))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "payload exceeds")
}

func TestManifest_CappedCapacityUpdatesAndPromotion(t *testing.T) {
	firstWire, firstMaster, _ := buildManifest(t, 1, false, 0x41, 0x42)
	first, err := manifest.Deserialize(firstWire)
	require.NoError(t, err)
	secondWire, secondMaster, _ := buildManifest(t, 1, false, 0x43, 0x44)
	second, err := manifest.Deserialize(secondWire)
	require.NoError(t, err)

	cache := manifest.NewCache(1)
	require.Equal(t, manifest.Accepted, cache.ApplyManifest(first, manifest.Capped))
	require.True(t, cache.IsUntrusted(firstMaster))
	require.Equal(t, 1, cache.UntrustedCount())

	// A full capped cache rejects a new master before signature/key checks and
	// retains every accepted entry without eviction.
	badSecondWire := append([]byte(nil), secondWire...)
	badSecondWire[len(badSecondWire)-1] ^= 0x01
	badSecond, err := manifest.Deserialize(badSecondWire)
	require.NoError(t, err)
	require.Equal(t, manifest.UntrustedCapacity, cache.ApplyManifest(badSecond, manifest.Capped))
	require.Equal(t, 1, cache.UntrustedCount())
	_, ok := cache.GetManifest(secondMaster)
	require.False(t, ok)
	_, ok = cache.GetManifest(firstMaster)
	require.True(t, ok)

	// Rotations and revocations of an already-cached master bypass capacity.
	rotationWire, _, _ := buildManifest(t, 2, false, 0x41, 0x45)
	rotation, err := manifest.Deserialize(rotationWire)
	require.NoError(t, err)
	require.Equal(t, manifest.Accepted, cache.ApplyManifest(rotation, manifest.Capped))
	require.Equal(t, 1, cache.UntrustedCount())
	revocationWire, _, _ := buildManifest(t, manifest.RevokedSequence, true, 0x41, 0)
	revocation, err := manifest.Deserialize(revocationWire)
	require.NoError(t, err)
	require.Equal(t, manifest.Accepted, cache.ApplyManifest(revocation, manifest.Capped))
	require.Equal(t, 1, cache.UntrustedCount())

	// Promotion frees the slot but keeps the cached revocation; a subsequent
	// capped insertion may consume that slot.
	cache.PromoteToTrusted(firstMaster)
	cache.PromoteToTrusted(firstMaster)
	require.Equal(t, 0, cache.UntrustedCount())
	require.True(t, cache.Revoked(firstMaster))
	require.Equal(t, manifest.Accepted, cache.ApplyManifest(second, manifest.Capped))
	require.Equal(t, 1, cache.UntrustedCount())

	// An uncapped update of a counted master frees its slot and de-listing does
	// not add it back.
	secondRotationWire, _, _ := buildManifest(t, 2, false, 0x43, 0x46)
	secondRotation, err := manifest.Deserialize(secondRotationWire)
	require.NoError(t, err)
	require.Equal(t, manifest.Accepted, cache.ApplyManifest(secondRotation, manifest.Uncapped))
	require.Equal(t, 0, cache.UntrustedCount())
	require.Equal(t, manifest.Stale, cache.ApplyManifest(secondRotation, manifest.Capped))
	require.Equal(t, 0, cache.UntrustedCount())
}

func TestManifest_UncappedBypassesCapacityAndRolesAreIsolated(t *testing.T) {
	firstWire, firstMaster, _ := buildManifest(t, 1, false, 0x51, 0x52)
	first, err := manifest.Deserialize(firstWire)
	require.NoError(t, err)
	secondWire, secondMaster, _ := buildManifest(t, 1, false, 0x53, 0x54)
	second, err := manifest.Deserialize(secondWire)
	require.NoError(t, err)

	validators := manifest.NewCache(1)
	publishers := manifest.NewCache(1)
	require.Equal(t, manifest.Accepted, validators.ApplyManifest(first, manifest.Capped))
	require.Equal(t, manifest.Accepted, publishers.ApplyManifest(first, manifest.Capped))
	require.Equal(t, manifest.UntrustedCapacity, validators.ApplyManifest(second, manifest.Capped))
	require.Equal(t, manifest.UntrustedCapacity, publishers.ApplyManifest(second, manifest.Capped))

	// Configured/listed/DB paths remain uncapped and are isolated per role.
	require.Equal(t, manifest.Accepted, validators.ApplyManifest(second, manifest.Uncapped))
	require.False(t, validators.IsUntrusted(secondMaster))
	require.Equal(t, 1, validators.UntrustedCount())
	require.True(t, validators.IsUntrusted(firstMaster))
	require.False(t, publishers.IsUntrusted(secondMaster))
	require.Equal(t, 1, publishers.UntrustedCount())
}

func TestManifest_CappedCapacityConcurrentFirstInsert(t *testing.T) {
	cache := manifest.NewCache(1)
	const count = 12
	manifests := make([]*manifest.Manifest, 0, count)
	for i := 0; i < count; i++ {
		wire, _, _ := buildManifest(t, 1, false, byte(0x61+i), byte(0x71+i))
		parsed, err := manifest.Deserialize(wire)
		require.NoError(t, err)
		manifests = append(manifests, parsed)
	}

	results := make(chan manifest.Disposition, count)
	var wg sync.WaitGroup
	for _, parsed := range manifests {
		wg.Add(1)
		go func(m *manifest.Manifest) {
			defer wg.Done()
			results <- cache.ApplyManifest(m, manifest.Capped)
		}(parsed)
	}
	wg.Wait()
	close(results)

	accepted := 0
	capacity := 0
	for disposition := range results {
		switch disposition {
		case manifest.Accepted:
			accepted++
		case manifest.UntrustedCapacity:
			capacity++
		default:
			t.Fatalf("unexpected disposition %s", disposition)
		}
	}
	require.Equal(t, 1, accepted)
	require.Equal(t, count-1, capacity)
	require.Equal(t, 1, cache.UntrustedCount())
}

func TestDispositionString(t *testing.T) {
	tests := []struct {
		disposition manifest.Disposition
		want        string
	}{
		{manifest.Accepted, "accepted"},
		{manifest.Stale, "stale"},
		{manifest.Invalid, "invalid"},
		{manifest.BadMasterKey, "bad_master_key"},
		{manifest.BadEphemeralKey, "bad_ephemeral_key"},
		{manifest.UntrustedCapacity, "untrusted_capacity"},
		{manifest.Disposition(99), "unknown"},
	}
	for _, test := range tests {
		require.Equal(t, test.want, test.disposition.String())
	}
}

func TestManifestSignaturesComeFromImmutableParsedState(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		sequence := uint32(7)
		if revoked {
			sequence = manifest.RevokedSequence
		}
		wire, _, _ := buildManifest(t, sequence, revoked, 0x0a, 0x0b)
		decoded, err := binarycodec.DecodeBytes(wire)
		require.NoError(t, err)
		parsed, err := manifest.Deserialize(wire)
		require.NoError(t, err)

		masterSignature, signature := parsed.Signatures()
		require.Equal(t, decoded["MasterSignature"], masterSignature)
		if revoked {
			require.Empty(t, signature)
		} else {
			require.Equal(t, decoded["Signature"], signature)
		}

		serialized := parsed.Serialized()
		serialized[0] ^= 0xff
		gotMaster, gotSigning := parsed.Signatures()
		require.Equal(t, masterSignature, gotMaster)
		require.Equal(t, signature, gotSigning)
	}
}

// deterministicEd25519Keypair returns a 33-byte xrpl-style public key
// (0xED prefix + 32 bytes) and a 64-byte ed25519 private key seeded
// from `seed`. Tests use a byte seed so they're reproducible and each
// caller gets a distinct key.
func deterministicEd25519Keypair(seed byte) (pub33, priv64 []byte) {
	s := bytes.Repeat([]byte{seed}, ed25519.SeedSize)
	priv := ed25519.NewKeyFromSeed(s)
	pub := priv.Public().(ed25519.PublicKey)
	pub33 = append([]byte{0xED}, pub...)
	priv64 = priv
	return
}

func TestManifest_OnAccepted_FiresOnce(t *testing.T) {
	serialized, _, _ := buildManifest(t, 1, false, 0x10, 0x11)
	m, err := manifest.Deserialize(serialized)
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}

	c := manifest.NewCache()
	var fired []*manifest.Manifest
	unsubscribe := c.SubscribeAccepted(func(got *manifest.Manifest) {
		fired = append(fired, got)
	})
	defer unsubscribe()

	if d := c.ApplyManifest(m); d != manifest.Accepted {
		t.Fatalf("ApplyManifest: got %s want accepted", d)
	}
	if len(fired) != 1 {
		t.Fatalf("OnAccepted fired %d times, want 1", len(fired))
	}
	if fired[0].MasterKey() != m.MasterKey() {
		t.Fatalf("OnAccepted manifest mismatch: got master %x want %x", fired[0].MasterKey(), m.MasterKey())
	}

	// Re-applying the same manifest is Stale; the callback must not fire again.
	if d := c.ApplyManifest(m); d != manifest.Stale {
		t.Fatalf("ApplyManifest (re-apply): got %s want stale", d)
	}
	if len(fired) != 1 {
		t.Fatalf("OnAccepted fired %d times after stale re-apply, want 1", len(fired))
	}
}

func TestManifest_CacheOwnsAcceptedState(t *testing.T) {
	serialized, master, signing := buildManifest(t, 1, false, 0x12, 0x13)
	m, err := manifest.Deserialize(serialized)
	require.NoError(t, err)

	c := manifest.NewCache()
	var callback *manifest.Manifest
	c.SubscribeAccepted(func(got *manifest.Manifest) {
		callback = got
		wire := got.Serialized()
		wire[0] ^= 0xff
	})
	require.Equal(t, manifest.Accepted, c.ApplyManifest(m))
	require.NotNil(t, callback)

	inputWire := m.Serialized()
	inputWire[0] ^= 0xff

	require.Equal(t, signing, mustSigningKey(t, c, master))
	require.Equal(t, uint32(1), mustSequence(t, c, master))
	stored, ok := c.GetManifest(master)
	require.True(t, ok)
	require.Equal(t, serialized, stored)

	stored[0] ^= 0xff
	require.Equal(t, serialized, mustManifest(t, c, master))
	all := c.SerializedAll()
	require.Len(t, all, 1)
	all[0][0] ^= 0xff
	require.Equal(t, serialized, mustManifest(t, c, master))
	mapping := c.MasterToSigning()
	mapping[master] = [33]byte{}
	require.Equal(t, signing, mustSigningKey(t, c, master))
	snapshot := c.Snapshot()
	require.Len(t, snapshot, 1)
	snapshotWire := snapshot[0].Serialized()
	snapshotWire[0] ^= 0xff
	require.Equal(t, serialized, mustManifest(t, c, master))
	require.Equal(t, uint32(1), mustSequence(t, c, master))
}

func TestManifest_SubscribersReceiveIndependentCopies(t *testing.T) {
	serialized, master, _ := buildManifest(t, 1, false, 0x14, 0x15)
	m, err := manifest.Deserialize(serialized)
	require.NoError(t, err)

	c := manifest.NewCache()
	var first *manifest.Manifest
	c.SubscribeAccepted(func(got *manifest.Manifest) {
		first = got
		wire := got.Serialized()
		wire[0] ^= 0xff
	})
	var second *manifest.Manifest
	c.SubscribeAccepted(func(got *manifest.Manifest) {
		second = got
	})
	require.Equal(t, manifest.Accepted, c.ApplyManifest(m))
	require.NotNil(t, first)
	require.NotNil(t, second)
	require.NotSame(t, first, second)
	require.Equal(t, master, second.MasterKey())
	require.Equal(t, serialized, second.Serialized())
}

func TestManifest_CallbackReentryPreservesOrder(t *testing.T) {
	firstWire, _, _ := buildManifest(t, 1, false, 0x16, 0x17)
	secondWire, _, _ := buildManifest(t, 2, false, 0x16, 0x18)
	first, err := manifest.Deserialize(firstWire)
	require.NoError(t, err)
	second, err := manifest.Deserialize(secondWire)
	require.NoError(t, err)

	c := manifest.NewCache()
	var delivered []uint32
	c.SubscribeAccepted(func(got *manifest.Manifest) {
		delivered = append(delivered, got.Sequence())
		if got.Sequence() == 1 {
			require.Equal(t, manifest.Accepted, c.ApplyManifest(second))
		}
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		require.Equal(t, manifest.Accepted, c.ApplyManifest(first))
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ApplyManifest deadlocked during callback re-entry")
	}
	require.Equal(t, []uint32{1, 2}, delivered)
}

func TestManifest_SlowSubscriberDoesNotBlockConcurrentApply(t *testing.T) {
	firstWire, _, _ := buildManifest(t, 1, false, 0x19, 0x1a)
	secondWire, _, _ := buildManifest(t, 2, false, 0x19, 0x1b)
	first, err := manifest.Deserialize(firstWire)
	require.NoError(t, err)
	second, err := manifest.Deserialize(secondWire)
	require.NoError(t, err)

	c := manifest.NewCache()
	entered := make(chan struct{})
	release := make(chan struct{})
	c.SubscribeAccepted(func(got *manifest.Manifest) {
		if got.Sequence() == 1 {
			close(entered)
			<-release
		}
	})
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		require.Equal(t, manifest.Accepted, c.ApplyManifest(first))
	}()
	<-entered
	secondDone := make(chan manifest.Disposition, 1)
	go func() { secondDone <- c.ApplyManifest(second) }()
	select {
	case got := <-secondDone:
		require.Equal(t, manifest.Accepted, got)
	case <-time.After(time.Second):
		t.Fatal("concurrent ApplyManifest blocked on slow subscriber")
	}
	close(release)
	<-firstDone
}

func TestManifest_ConcurrentApply(t *testing.T) {
	const count = 32
	manifests := make([]*manifest.Manifest, 0, count)
	for i := 0; i < count; i++ {
		wire, _, _ := buildManifest(t, 1, false, byte(i+1), byte(i+65))
		parsed, err := manifest.Deserialize(wire)
		require.NoError(t, err)
		manifests = append(manifests, parsed)
	}

	cache := manifest.NewCache()
	accepted := make(chan struct{}, count)
	cache.SubscribeAccepted(func(*manifest.Manifest) { accepted <- struct{}{} })
	results := make(chan manifest.Disposition, count)
	for _, parsed := range manifests {
		go func() { results <- cache.ApplyManifest(parsed) }()
	}
	for range count {
		require.Equal(t, manifest.Accepted, <-results)
	}
	require.Len(t, accepted, count)
	require.Equal(t, uint64(count), cache.Sequence())
	require.Len(t, cache.SerializedAll(), count)
}

func TestManifest_SubscriberPanicAndRemovalAreIsolated(t *testing.T) {
	c := manifest.NewCache()
	c.SubscribeAccepted(func(*manifest.Manifest) { panic("subscriber failure") })
	count := 0
	unsubscribe := c.SubscribeAccepted(func(*manifest.Manifest) { count++ })

	firstWire, _, _ := buildManifest(t, 1, false, 0x1c, 0x1d)
	first, err := manifest.Deserialize(firstWire)
	require.NoError(t, err)
	require.Equal(t, manifest.Accepted, c.ApplyManifest(first))
	require.Equal(t, 1, count)

	unsubscribe()
	unsubscribe()
	secondWire, _, _ := buildManifest(t, 2, false, 0x1c, 0x1e)
	second, err := manifest.Deserialize(secondWire)
	require.NoError(t, err)
	require.Equal(t, manifest.Accepted, c.ApplyManifest(second))
	require.Equal(t, 1, count)
}

func TestManifest_KeyCollisionDispositions(t *testing.T) {
	tests := []struct {
		name     string
		first    [3]byte
		second   [3]byte
		expected manifest.Disposition
	}{
		{name: "same master same ephemeral higher sequence", first: [3]byte{1, 0x21, 0x22}, second: [3]byte{2, 0x21, 0x22}, expected: manifest.BadEphemeralKey},
		{name: "different master same ephemeral", first: [3]byte{1, 0x23, 0x24}, second: [3]byte{1, 0x25, 0x24}, expected: manifest.BadEphemeralKey},
		{name: "master already used as ephemeral", first: [3]byte{1, 0x26, 0x27}, second: [3]byte{1, 0x27, 0x28}, expected: manifest.BadMasterKey},
		{name: "ephemeral already used as master", first: [3]byte{1, 0x29, 0x2a}, second: [3]byte{1, 0x2b, 0x29}, expected: manifest.BadEphemeralKey},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			firstWire, _, _ := buildManifest(t, uint32(test.first[0]), false, test.first[1], test.first[2])
			secondWire, _, _ := buildManifest(t, uint32(test.second[0]), false, test.second[1], test.second[2])
			first, err := manifest.Deserialize(firstWire)
			require.NoError(t, err)
			second, err := manifest.Deserialize(secondWire)
			require.NoError(t, err)
			c := manifest.NewCache()
			require.Equal(t, manifest.Accepted, c.ApplyManifest(first))
			require.Equal(t, test.expected, c.ApplyManifest(second))
		})
	}
}

func TestManifest_KeyCollisionsAreCacheLocal(t *testing.T) {
	wire, _, _ := buildManifest(t, 1, false, 0x2c, 0x2d)
	parsed, err := manifest.Deserialize(wire)
	require.NoError(t, err)
	validators := manifest.NewCache()
	publishers := manifest.NewCache()
	require.Equal(t, manifest.Accepted, validators.ApplyManifest(parsed))
	require.Equal(t, manifest.Accepted, publishers.ApplyManifest(parsed))
}

func TestManifest_PersistencePreservesRevocationAndRotation(t *testing.T) {
	revokedOldWire, revokedMaster, revokedSigning := buildManifest(t, 1, false, 0x2e, 0x2f)
	revokedOld, err := manifest.Deserialize(revokedOldWire)
	require.NoError(t, err)
	revocationWire, _, _ := buildManifest(t, manifest.RevokedSequence, true, 0x2e, 0)
	revocation, err := manifest.Deserialize(revocationWire)
	require.NoError(t, err)

	rotationOldWire, rotationMaster, rotationOldSigning := buildManifest(t, 1, false, 0x30, 0x31)
	rotationOld, err := manifest.Deserialize(rotationOldWire)
	require.NoError(t, err)
	rotationWire, _, rotationSigning := buildManifest(t, 2, false, 0x30, 0x32)
	rotation, err := manifest.Deserialize(rotationWire)
	require.NoError(t, err)

	before := manifest.NewCache()
	require.Equal(t, manifest.Accepted, before.ApplyManifest(revokedOld))
	require.Equal(t, manifest.Accepted, before.ApplyManifest(revocation))
	require.Equal(t, manifest.Accepted, before.ApplyManifest(rotationOld))
	require.Equal(t, manifest.Accepted, before.ApplyManifest(rotation))

	rows := make([][]byte, 0, 2)
	for _, entry := range before.Snapshot() {
		rows = append(rows, entry.Serialized())
	}
	ctx := context.Background()
	dir := t.TempDir()
	store, err := manifest.OpenSQLiteStore(ctx, dir)
	require.NoError(t, err)
	require.NoError(t, store.Replace(ctx, manifest.StoredManifests{Validators: rows}))
	require.NoError(t, store.Close())

	store, err = manifest.OpenSQLiteStore(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	stored, err := store.Load(ctx)
	require.NoError(t, err)
	after := manifest.NewCache()
	for _, raw := range stored.Validators {
		parsed, parseErr := manifest.Deserialize(raw)
		require.NoError(t, parseErr)
		require.Equal(t, manifest.Accepted, after.ApplyManifest(parsed))
	}

	require.True(t, after.Revoked(revokedMaster))
	require.Equal(t, manifest.Stale, after.ApplyManifest(revokedOld))
	require.NotEqual(t, revokedMaster, after.GetMasterKey(revokedSigning))
	require.Equal(t, rotationSigning, mustSigningKey(t, after, rotationMaster))
	require.NotEqual(t, rotationMaster, after.GetMasterKey(rotationOldSigning))
}

func mustManifest(t *testing.T, c *manifest.Cache, master [33]byte) []byte {
	t.Helper()
	got, ok := c.GetManifest(master)
	require.True(t, ok)
	return got
}

func mustSequence(t *testing.T, c *manifest.Cache, master [33]byte) uint32 {
	t.Helper()
	got, ok := c.GetSequence(master)
	require.True(t, ok)
	return got
}

func mustSigningKey(t *testing.T, c *manifest.Cache, master [33]byte) [33]byte {
	t.Helper()
	got, ok := c.GetSigningKey(master)
	require.True(t, ok)
	return got
}

func TestManifest_WireDecode_ValidMasterSig_Accepted(t *testing.T) {
	serialized, master, ephemeral := buildManifest(t, 1, false, 0x01, 0x02)

	m, err := manifest.Deserialize(serialized)
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if m.MasterKey() != master {
		t.Fatalf("MasterKey mismatch: got %x want %x", m.MasterKey(), master)
	}
	if m.SigningKey() != ephemeral {
		t.Fatalf("SigningKey mismatch: got %x want %x", m.SigningKey(), ephemeral)
	}
	if m.Sequence() != 1 {
		t.Fatalf("Sequence: got %d want 1", m.Sequence())
	}
	if m.Revoked() {
		t.Fatal("Revoked: true, want false")
	}

	if err := m.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	c := manifest.NewCache()
	if d := c.ApplyManifest(m); d != manifest.Accepted {
		t.Fatalf("ApplyManifest: got %s want accepted", d)
	}

	gotMaster := c.GetMasterKey(ephemeral)
	if gotMaster != master {
		t.Fatalf("GetMasterKey: got %x want %x", gotMaster, master)
	}
	gotEph, ok := c.GetSigningKey(master)
	if !ok || gotEph != ephemeral {
		t.Fatalf("GetSigningKey: ok=%v got %x want %x", ok, gotEph, ephemeral)
	}
	if stored, ok := c.GetManifest(master); !ok || !bytes.Equal(stored, serialized) {
		t.Fatalf("GetManifest: ok=%v match=%v", ok, bytes.Equal(stored, serialized))
	}
	if seq, ok := c.GetSequence(master); !ok || seq != 1 {
		t.Fatalf("GetSequence: ok=%v seq=%d", ok, seq)
	}
}

func TestManifest_WireDecode_BadMasterSig_Rejected(t *testing.T) {
	serialized, master, _ := buildManifest(t, 1, false, 0x03, 0x04)

	// Re-encode with a bogus MasterSignature: decode → overwrite →
	// re-encode. Changing the raw bytes directly would misalign the VL
	// length prefix.
	decoded, err := binarycodec.Decode(hex.EncodeToString(serialized))
	if err != nil {
		t.Fatalf("round-trip decode: %v", err)
	}
	badSig := strings.Repeat("AA", ed25519.SignatureSize)
	decoded["MasterSignature"] = badSig
	corruptedHex, err := binarycodec.Encode(decoded)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	corrupted, err := hex.DecodeString(corruptedHex)
	if err != nil {
		t.Fatalf("re-decode hex: %v", err)
	}

	m, err := manifest.Deserialize(corrupted)
	if err != nil {
		t.Fatalf("Deserialize should succeed (syntax valid): %v", err)
	}
	if err := m.Verify(); err == nil {
		t.Fatal("Verify: got nil, want error (bad master sig)")
	}

	c := manifest.NewCache()
	if d := c.ApplyManifest(m); d != manifest.Invalid {
		t.Fatalf("ApplyManifest: got %s want invalid", d)
	}
	if _, ok := c.GetSigningKey(master); ok {
		t.Fatal("cache stored an invalid manifest")
	}
}

func TestManifest_HigherSeq_Overrides(t *testing.T) {
	seq1Bytes, master, eph1 := buildManifest(t, 1, false, 0x05, 0x06)
	m1, err := manifest.Deserialize(seq1Bytes)
	if err != nil {
		t.Fatalf("Deserialize seq1: %v", err)
	}

	c := manifest.NewCache()
	if d := c.ApplyManifest(m1); d != manifest.Accepted {
		t.Fatalf("seq1 apply: %s", d)
	}

	// seq 2 rotates to a new ephemeral key (different seed).
	seq2Bytes, master2, eph2 := buildManifest(t, 2, false, 0x05, 0x07)
	if master2 != master {
		t.Fatalf("master keys drifted: test helper bug")
	}
	m2, err := manifest.Deserialize(seq2Bytes)
	if err != nil {
		t.Fatalf("Deserialize seq2: %v", err)
	}
	if d := c.ApplyManifest(m2); d != manifest.Accepted {
		t.Fatalf("seq2 apply: %s", d)
	}

	// Old ephemeral should no longer resolve to the master — it's been
	// rotated out.
	if got := c.GetMasterKey(eph1); got == master {
		t.Fatalf("old ephemeral still resolves to master after rotation: got %x", got)
	}
	if got := c.GetMasterKey(eph2); got != master {
		t.Fatalf("new ephemeral doesn't resolve: got %x want %x", got, master)
	}

	// Re-applying seq1 must be Stale.
	if d := c.ApplyManifest(m1); d != manifest.Stale {
		t.Fatalf("stale re-apply: got %s want stale", d)
	}
}

func TestManifest_RevokedMasterKey_Rejected(t *testing.T) {
	// Establish a seq 1 manifest first so we can see revocation
	// erases the ephemeral lookup.
	initBytes, master, eph := buildManifest(t, 1, false, 0x08, 0x09)
	initM, err := manifest.Deserialize(initBytes)
	if err != nil {
		t.Fatalf("Deserialize init: %v", err)
	}
	c := manifest.NewCache()
	if d := c.ApplyManifest(initM); d != manifest.Accepted {
		t.Fatalf("init apply: %s", d)
	}
	if _, ok := c.GetSigningKey(master); !ok {
		t.Fatal("sanity: signing key should resolve pre-revocation")
	}

	// Revoke. Same master, same master-seed, no ephemeral fields,
	// seq = MaxUint32.
	revBytes, master2, _ := buildManifest(t, manifest.RevokedSequence, true, 0x08, 0x00)
	if master2 != master {
		t.Fatalf("master keys drifted: test helper bug")
	}
	revM, err := manifest.Deserialize(revBytes)
	if err != nil {
		t.Fatalf("Deserialize revoke: %v", err)
	}
	if !revM.Revoked() {
		t.Fatal("Revoked() = false, expected true")
	}

	if d := c.ApplyManifest(revM); d != manifest.Accepted {
		t.Fatalf("revoke apply: got %s want accepted", d)
	}
	if _, ok := c.GetSigningKey(master); ok {
		t.Fatal("GetSigningKey still returns ok after revocation")
	}
	if got := c.GetMasterKey(eph); got == master {
		t.Fatal("ephemeral still resolves to master after revocation")
	}
	if !c.Revoked(master) {
		t.Fatal("Revoked(master) = false after applying revocation")
	}
}

func TestManifest_NonRevoked_MissingEphemeral_Rejected(t *testing.T) {
	serialized, _, _ := buildManifest(t, 1, false, 0x0A, 0x0B)

	decoded, err := binarycodec.Decode(hex.EncodeToString(serialized))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	delete(decoded, "SigningPubKey")
	delete(decoded, "Signature")
	corruptedHex, err := binarycodec.Encode(decoded)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	corrupted, err := hex.DecodeString(corruptedHex)
	if err != nil {
		t.Fatalf("re-decode hex: %v", err)
	}

	if _, err := manifest.Deserialize(corrupted); err == nil {
		t.Fatal("Deserialize: got nil error, want rejection of non-revoked w/o ephemeral")
	}
}

func TestManifest_Revoked_WithEphemeral_Rejected(t *testing.T) {
	// Build a non-revoked manifest, then swap its sequence to the
	// revoked sentinel without removing ephemeral fields. The ephemeral
	// signature will be wrong after this tweak, but we're probing the
	// structural invariant in Deserialize — and that runs before Verify.
	serialized, _, _ := buildManifest(t, 1, false, 0x0C, 0x0D)
	decoded, err := binarycodec.Decode(hex.EncodeToString(serialized))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	decoded["Sequence"] = manifest.RevokedSequence
	corruptedHex, err := binarycodec.Encode(decoded)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	corrupted, err := hex.DecodeString(corruptedHex)
	if err != nil {
		t.Fatalf("re-decode hex: %v", err)
	}

	if _, err := manifest.Deserialize(corrupted); err == nil {
		t.Fatal("Deserialize: got nil, want rejection of revoked + ephemeral")
	}
}

func TestManifest_Revoked_WithEmptyEphemeralField_Rejected(t *testing.T) {
	serialized, _, _ := buildManifest(t, manifest.RevokedSequence, true, 0x0E, 0)
	for _, field := range []string{"SigningPubKey", "Signature"} {
		t.Run(field, func(t *testing.T) {
			decoded, err := binarycodec.DecodeBytes(serialized)
			require.NoError(t, err)
			decoded[field] = ""
			encoded, err := binarycodec.Encode(decoded)
			require.NoError(t, err)
			withEmptyField, err := hex.DecodeString(encoded)
			require.NoError(t, err)

			_, err = manifest.Deserialize(withEmptyField)
			require.ErrorContains(t, err, "revoked manifest must not carry ephemeral fields")
		})
	}
}

// buildManifestSecpMaster builds a serialized manifest with a
// secp256k1 master key and ed25519 ephemeral key, both signing the
// canonical preimage. The returned master signature is forced to its
// fully-canonical (low-S) form so a downstream test can flip it to
// high-S and probe the strict-canonicality gate.
func buildManifestSecpMaster(t *testing.T, seq uint32, masterSeed byte, ephemeralSeed byte) (serialized []byte, masterPub [33]byte, ephemeralPub [33]byte) {
	t.Helper()

	algo := secp256k1.Algorithm{}
	seedBytes := bytes.Repeat([]byte{masterSeed}, 16)
	masterPrivHex, masterPubHex, err := algo.DeriveKeypair(seedBytes, false)
	if err != nil {
		t.Fatalf("derive secp256k1 master keypair: %v", err)
	}
	masterPubBytes, err := hex.DecodeString(masterPubHex)
	if err != nil {
		t.Fatalf("decode master pub hex: %v", err)
	}
	if len(masterPubBytes) != 33 {
		t.Fatalf("master pub: got %d bytes want 33", len(masterPubBytes))
	}
	copy(masterPub[:], masterPubBytes)
	masterPrivKeyHex := strings.TrimPrefix(masterPrivHex, "00")

	ephPubBytes, ephPriv := deterministicEd25519Keypair(ephemeralSeed)
	copy(ephemeralPub[:], ephPubBytes)

	json := map[string]any{
		"PublicKey":     hex.EncodeToString(masterPubBytes),
		"Sequence":      seq,
		"SigningPubKey": hex.EncodeToString(ephPubBytes),
	}
	preimage := signingPreimageFromJSON(t, json)
	ephSig := ed25519.Sign(ed25519.PrivateKey(ephPriv), preimage)
	json["Signature"] = hex.EncodeToString(ephSig)

	masterSigHex, err := algo.Sign(string(preimage), masterPrivKeyHex)
	if err != nil {
		t.Fatalf("sign master: %v", err)
	}
	masterSigBytes, err := hex.DecodeString(masterSigHex)
	if err != nil {
		t.Fatalf("decode master sig hex: %v", err)
	}
	// algo.Sign always emits a low-S (fully canonical) secp256k1 signature, so
	// the master sig is already canonical; the high-S flip below depends on that.
	if rootcrypto.ECDSACanonicality(masterSigBytes) != rootcrypto.CanonicalityFullyCanonical {
		t.Fatalf("expected fully-canonical master signature from Sign")
	}
	json["MasterSignature"] = strings.ToUpper(hex.EncodeToString(masterSigBytes))

	encoded, err := binarycodec.Encode(json)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	b, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode built hex: %v", err)
	}
	return b, masterPub, ephemeralPub
}

// TestManifest_Secp256k1MasterSig_HighS_Rejected exercises the strict
// canonicality gate at internal/manifest/manifest.go:244-249 end-to-end:
// rippled's manifest verify path uses the header-default
// mustBeFullyCanonical=true (PublicKey.h:251-256 + Sign.cpp:60-61), so a
// high-S secp256k1 master signature — though mathematically equivalent —
// must be rejected.
func TestManifest_Secp256k1MasterSig_HighS_Rejected(t *testing.T) {
	serialized, _, _ := buildManifestSecpMaster(t, 1, 0x10, 0x20)

	// Sanity: as-built manifest with low-S master sig must verify.
	mGood, err := manifest.Deserialize(serialized)
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if err := mGood.Verify(); err != nil {
		t.Fatalf("baseline Verify: %v", err)
	}

	// Decode, flip master sig S → N - S (high-S), re-encode.
	decoded, err := binarycodec.Decode(hex.EncodeToString(serialized))
	if err != nil {
		t.Fatalf("decode for flip: %v", err)
	}
	masterSigHex, _ := decoded["MasterSignature"].(string)
	masterSigBytes, err := hex.DecodeString(masterSigHex)
	if err != nil {
		t.Fatalf("decode master sig: %v", err)
	}
	r, s, err := rootcrypto.DERSigToRS(masterSigBytes)
	if err != nil {
		t.Fatalf("parse master sig DER: %v", err)
	}
	highS := new(big.Int).Sub(secp256k1CurveOrderN, new(big.Int).SetBytes(s))
	highSDER := rootcrypto.EncodeDERSignature(new(big.Int).SetBytes(r), highS)
	if got := rootcrypto.ECDSACanonicality(highSDER); got != rootcrypto.CanonicalityCanonical {
		t.Fatalf("flipped sig canonicality: got %v want %v (high-S but otherwise valid)", got, rootcrypto.CanonicalityCanonical)
	}
	decoded["MasterSignature"] = strings.ToUpper(hex.EncodeToString(highSDER))

	corruptedHex, err := binarycodec.Encode(decoded)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	corrupted, err := hex.DecodeString(corruptedHex)
	if err != nil {
		t.Fatalf("re-decode hex: %v", err)
	}

	mBad, err := manifest.Deserialize(corrupted)
	if err != nil {
		t.Fatalf("Deserialize corrupted (syntax should still be valid): %v", err)
	}
	err = mBad.Verify()
	if err == nil {
		t.Fatal("Verify returned nil; expected rejection of high-S secp256k1 master signature")
	}
	if !strings.Contains(err.Error(), "master signature invalid") {
		t.Fatalf("Verify error: got %q want one containing %q", err.Error(), "master signature invalid")
	}
}

// Sequence() tracks rippled 3.2.0's seq_++ semantics (#6059): EVERY
// accepted manifest advances the counter — both a first-insert for a
// newly-seen master key and an update replacing an existing one. Only a
// Stale re-apply leaves it alone.
func TestManifest_Sequence_AdvancesOnEveryAccept(t *testing.T) {
	c := manifest.NewCache()
	if got := c.Sequence(); got != 0 {
		t.Fatalf("fresh cache Sequence: got %d want 0", got)
	}

	// First insert under master A → bumps (rippled 3.2.0 first-insert).
	aSeq1Bytes, _, _ := buildManifest(t, 1, false, 0x21, 0x22)
	aSeq1, err := manifest.Deserialize(aSeq1Bytes)
	if err != nil {
		t.Fatalf("Deserialize aSeq1: %v", err)
	}
	if d := c.ApplyManifest(aSeq1); d != manifest.Accepted {
		t.Fatalf("first insert: %s", d)
	}
	if got := c.Sequence(); got != 1 {
		t.Errorf("Sequence after first insert: got %d want 1", got)
	}

	// First insert under a SECOND master B → bumps again.
	bSeq1Bytes, _, _ := buildManifest(t, 1, false, 0x23, 0x24)
	bSeq1, err := manifest.Deserialize(bSeq1Bytes)
	if err != nil {
		t.Fatalf("Deserialize bSeq1: %v", err)
	}
	if d := c.ApplyManifest(bSeq1); d != manifest.Accepted {
		t.Fatalf("second master first insert: %s", d)
	}
	if got := c.Sequence(); got != 2 {
		t.Errorf("Sequence after second first-insert: got %d want 2", got)
	}

	// Update master A → seq bumps.
	aSeq2Bytes, _, _ := buildManifest(t, 2, false, 0x21, 0x25)
	aSeq2, err := manifest.Deserialize(aSeq2Bytes)
	if err != nil {
		t.Fatalf("Deserialize aSeq2: %v", err)
	}
	if d := c.ApplyManifest(aSeq2); d != manifest.Accepted {
		t.Fatalf("update aSeq2: %s", d)
	}
	if got := c.Sequence(); got != 3 {
		t.Errorf("Sequence after update: got %d want 3", got)
	}

	// Stale re-apply must NOT bump.
	if d := c.ApplyManifest(aSeq1); d != manifest.Stale {
		t.Fatalf("stale re-apply: %s", d)
	}
	if got := c.Sequence(); got != 3 {
		t.Errorf("Sequence after stale: got %d want 3", got)
	}
}

// buildManifestWithDomain builds a valid, signed manifest carrying the given
// Domain (VL-encoded as the hex of its UTF-8 bytes, as the wire format
// requires) so the domain-validation path in Deserialize can be exercised.
func buildManifestWithDomain(t *testing.T, domain string, masterSeed, ephemeralSeed byte) []byte {
	t.Helper()

	masterPubBytes, masterPriv := deterministicEd25519Keypair(masterSeed)
	ephPubBytes, ephPriv := deterministicEd25519Keypair(ephemeralSeed)

	json := map[string]any{
		"PublicKey":     hex.EncodeToString(masterPubBytes),
		"Sequence":      uint32(1),
		"Domain":        hex.EncodeToString([]byte(domain)),
		"SigningPubKey": hex.EncodeToString(ephPubBytes),
	}

	preimage := signingPreimageFromJSON(t, json)
	json["Signature"] = hex.EncodeToString(ed25519.Sign(ed25519.PrivateKey(ephPriv), preimage))
	json["MasterSignature"] = hex.EncodeToString(ed25519.Sign(ed25519.PrivateKey(masterPriv), preimage))

	encoded, err := binarycodec.Encode(json)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	b, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode built hex: %v", err)
	}
	return b
}

// TestManifest_Domain_Validation mirrors rippled's isProperlyFormedTomlDomain
// check (Manifest.cpp:107-115): a malformed domain rejects the whole manifest
// rather than being cached and relayed.
func TestManifest_Domain_Validation(t *testing.T) {
	cases := []struct {
		name   string
		domain string
		accept bool
	}{
		{"simple", "example.com", true},
		{"subdomain", "validators.ripple.com", true},
		{"hyphenated", "my-validator.example.org", true},
		{"too-short", "a.b", false},
		{"no-tld", "example", false},
		{"trailing-dot", "example.com.", false},
		{"leading-hyphen-segment", "-bad.example.com", false},
		{"trailing-hyphen-segment", "bad-.example.com", false},
		{"numeric-tld", "example.123", false},
		{"underscore", "bad_domain.com", false},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serialized := buildManifestWithDomain(t, tc.domain, byte(0x40+i), byte(0x80+i))
			m, err := manifest.Deserialize(serialized)
			if tc.accept {
				if err != nil {
					t.Fatalf("Deserialize(%q): unexpected error: %v", tc.domain, err)
				}
				if m.Domain() != tc.domain {
					t.Errorf("Domain: got %q want %q", m.Domain(), tc.domain)
				}
				return
			}
			if err == nil {
				t.Fatalf("Deserialize(%q): expected rejection, got accepted", tc.domain)
			}
		})
	}
}

func TestManifest_MaximumDomainFitsSerializedLimit(t *testing.T) {
	domain := strings.Repeat("a", 63) + "." + strings.Repeat("b", 61) + ".cc"
	require.Len(t, domain, 128)
	serialized := buildManifestWithDomain(t, domain, 0x72, 0x73)
	require.Less(t, len(serialized), 1024)
	parsed, err := manifest.Deserialize(serialized)
	require.NoError(t, err)
	require.Equal(t, domain, parsed.Domain())
	require.NoError(t, parsed.Verify())
}

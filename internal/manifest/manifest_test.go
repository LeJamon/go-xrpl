package manifest_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/manifest"
	"github.com/LeJamon/goXRPLd/protocol"
)

// buildManifest constructs a serialized manifest with real ed25519
// signatures. Used by tests that want a valid starting point and then
// selectively corrupt fields. Returns the serialized manifest bytes and
// the two public keys (master, ephemeral) so callers can check the
// stored state after apply.
func buildManifest(t *testing.T, seq uint32, revoked bool, masterSeed, ephemeralSeed byte) (serialized []byte, masterPub [33]byte, ephemeralPub [33]byte) {
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
func signingPreimageFromJSON(t *testing.T, src map[string]any) []byte {
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

func TestManifest_WireDecode_ValidMasterSig_Accepted(t *testing.T) {
	serialized, master, ephemeral := buildManifest(t, 1, false, 0x01, 0x02)

	m, err := manifest.Deserialize(serialized)
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if m.MasterKey != master {
		t.Fatalf("MasterKey mismatch: got %x want %x", m.MasterKey, master)
	}
	if m.SigningKey != ephemeral {
		t.Fatalf("SigningKey mismatch: got %x want %x", m.SigningKey, ephemeral)
	}
	if m.Sequence != 1 {
		t.Fatalf("Sequence: got %d want 1", m.Sequence)
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

// Sequence() tracks rippled's seq_++ semantics: only the UPDATE branch
// in ApplyManifest advances the counter (Manifest.cpp:538). First-
// inserts and Stale re-applies leave it alone.
func TestManifest_Sequence_AdvancesOnlyOnUpdate(t *testing.T) {
	c := manifest.NewCache()
	if got := c.Sequence(); got != 0 {
		t.Fatalf("fresh cache Sequence: got %d want 0", got)
	}

	// First insert under master A.
	aSeq1Bytes, _, _ := buildManifest(t, 1, false, 0x21, 0x22)
	aSeq1, err := manifest.Deserialize(aSeq1Bytes)
	if err != nil {
		t.Fatalf("Deserialize aSeq1: %v", err)
	}
	if d := c.ApplyManifest(aSeq1); d != manifest.Accepted {
		t.Fatalf("first insert: %s", d)
	}
	if got := c.Sequence(); got != 0 {
		t.Errorf("Sequence after first insert: got %d want 0 (rippled first-insert quirk)", got)
	}

	// First insert under a SECOND master B — still a first-insert, so
	// no bump.
	bSeq1Bytes, _, _ := buildManifest(t, 1, false, 0x23, 0x24)
	bSeq1, err := manifest.Deserialize(bSeq1Bytes)
	if err != nil {
		t.Fatalf("Deserialize bSeq1: %v", err)
	}
	if d := c.ApplyManifest(bSeq1); d != manifest.Accepted {
		t.Fatalf("second master first insert: %s", d)
	}
	if got := c.Sequence(); got != 0 {
		t.Errorf("Sequence after second first-insert: got %d want 0", got)
	}

	// Update master A → seq must bump.
	aSeq2Bytes, _, _ := buildManifest(t, 2, false, 0x21, 0x25)
	aSeq2, err := manifest.Deserialize(aSeq2Bytes)
	if err != nil {
		t.Fatalf("Deserialize aSeq2: %v", err)
	}
	if d := c.ApplyManifest(aSeq2); d != manifest.Accepted {
		t.Fatalf("update aSeq2: %s", d)
	}
	if got := c.Sequence(); got != 1 {
		t.Errorf("Sequence after update: got %d want 1", got)
	}

	// Stale re-apply must NOT bump.
	if d := c.ApplyManifest(aSeq1); d != manifest.Stale {
		t.Fatalf("stale re-apply: %s", d)
	}
	if got := c.Sequence(); got != 1 {
		t.Errorf("Sequence after stale: got %d want 1", got)
	}
}

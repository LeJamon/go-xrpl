package adaptor

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
	"github.com/LeJamon/goXRPLd/crypto/secp256k1"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/protocol"
)

// tokenFixture bundles the synthetic validator-token blob and its key
// material so individual test cases can verify the parts they care
// about (parser, signing, manifest verification) against the same
// inputs.
type tokenFixture struct {
	tokenBlock string
	masterPub  [33]byte
	signingPub [33]byte
	signingSec [32]byte
	sequence   uint32
}

// newTokenFixture mints a self-consistent validator_token: a freshly
// generated ed25519 master, a freshly generated secp256k1 ephemeral
// keypair, a manifest signed by both, and the base64-wrapped JSON
// envelope rippled's validator-keys-tool emits.
//
// All randomness is seeded by `seed` so tests stay deterministic.
func newTokenFixture(t *testing.T, seed byte, sequence uint32) tokenFixture {
	t.Helper()

	// Master: ed25519. Use deterministic-from-seed key material so the
	// fixture is reproducible across runs.
	masterSeedBytes := bytes.Repeat([]byte{seed}, ed25519.SeedSize)
	masterPriv := ed25519.NewKeyFromSeed(masterSeedBytes)
	masterPubBytes := masterPriv.Public().(ed25519.PublicKey)
	master33 := append([]byte{0xED}, masterPubBytes...)

	// Ephemeral: secp256k1. Build a 32-byte secret from the seed; the
	// secret must be in (0, n) — for any 0x00 < seed < curve order it
	// is, and 0xFF is also fine. Tests use small seeds so this holds.
	var sec [32]byte
	for i := range sec {
		sec[i] = seed ^ byte(i+1)
	}
	algo := secp256k1.SECP256K1()
	signingPubBytes, err := algo.DerivePublicKeyFromSecret(sec[:])
	if err != nil {
		t.Fatalf("derive ephemeral pubkey: %v", err)
	}
	if len(signingPubBytes) != 33 {
		t.Fatalf("ephemeral pubkey wrong length: %d", len(signingPubBytes))
	}

	// Build the manifest STObject and sign it. The codec works in
	// JSON-of-fields form; encode produces hex.
	mfst := map[string]any{
		"PublicKey":     hex.EncodeToString(master33),
		"SigningPubKey": hex.EncodeToString(signingPubBytes),
		"Sequence":      sequence,
	}

	preimage := manifestSigningPreimage(t, mfst)

	// Ephemeral signature: secp256k1 over the preimage. The package's
	// Sign internally SHA-512Halves the message — match that with
	// SignCanonical to land in the always-low-S domain rippled prefers.
	sigHex, err := algo.SignCanonical(string(preimage), hex.EncodeToString(sec[:]))
	if err != nil {
		t.Fatalf("sign ephemeral: %v", err)
	}
	mfst["Signature"] = sigHex

	// Master signature: ed25519 directly over the preimage (ed25519
	// hashes internally; rippled's verifier passes raw bytes).
	masterSig := ed25519.Sign(masterPriv, preimage)
	mfst["MasterSignature"] = hex.EncodeToString(masterSig)

	encodedHex, err := binarycodec.Encode(mfst)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	wire, err := hex.DecodeString(encodedHex)
	if err != nil {
		t.Fatalf("decode manifest hex: %v", err)
	}

	// Wrap in the validator-keys-tool token format: base64 over JSON.
	envelope, err := json.Marshal(map[string]string{
		"manifest":              base64.StdEncoding.EncodeToString(wire),
		"validation_secret_key": hex.EncodeToString(sec[:]),
	})
	if err != nil {
		t.Fatalf("marshal token: %v", err)
	}
	tokenB64 := base64.StdEncoding.EncodeToString(envelope)

	// Pretty-print the token across multiple indented lines, mirroring
	// what an operator would copy out of validator-keys-tool. Ensures
	// LoadValidatorToken's whitespace stripping is exercised end-to-end.
	var tokenBlock strings.Builder
	for i := 0; i < len(tokenB64); i += 64 {
		end := i + 64
		if end > len(tokenB64) {
			end = len(tokenB64)
		}
		tokenBlock.WriteString("    ")
		tokenBlock.WriteString(tokenB64[i:end])
		tokenBlock.WriteString("\n")
	}

	fix := tokenFixture{
		tokenBlock: tokenBlock.String(),
		sequence:   sequence,
	}
	copy(fix.masterPub[:], master33)
	copy(fix.signingPub[:], signingPubBytes)
	fix.signingSec = sec
	return fix
}

// manifestSigningPreimage replicates the package-internal preimage
// construction so the test signs over exactly what Verify checks.
// HashPrefix("MAN\0") || STObject(only signing fields).
func manifestSigningPreimage(t *testing.T, src map[string]any) []byte {
	t.Helper()
	filtered := make(map[string]any, len(src))
	for k, v := range src {
		fi, _ := definitions.Get().GetFieldInstanceByFieldName(k)
		if fi != nil && !fi.IsSigningField {
			continue
		}
		filtered[k] = v
	}
	encodedHex, err := binarycodec.Encode(filtered)
	if err != nil {
		t.Fatalf("encode preimage body: %v", err)
	}
	body, err := hex.DecodeString(encodedHex)
	if err != nil {
		t.Fatalf("decode preimage hex: %v", err)
	}
	prefix := protocol.HashPrefixManifest
	out := make([]byte, 0, len(prefix)+len(body))
	out = append(out, prefix[:]...)
	out = append(out, body...)
	return out
}

func TestNewValidatorIdentityFromToken_HappyPath(t *testing.T) {
	fix := newTokenFixture(t, 0x42, 7)

	id, err := NewValidatorIdentityFromToken(fix.tokenBlock)
	if err != nil {
		t.Fatalf("NewValidatorIdentityFromToken: %v", err)
	}
	if id.MasterKey != fix.masterPub {
		t.Errorf("MasterKey mismatch: got %x want %x", id.MasterKey, fix.masterPub)
	}
	if id.SigningKey != fix.signingPub {
		t.Errorf("SigningKey mismatch: got %x want %x", id.SigningKey, fix.signingPub)
	}
	if id.MasterKey == id.SigningKey {
		t.Errorf("token mode must split master and signing keys")
	}
	if id.Manifest == nil {
		t.Fatal("Manifest must be populated")
	}
	if id.Manifest.Sequence != fix.sequence {
		t.Errorf("manifest sequence: got %d want %d", id.Manifest.Sequence, fix.sequence)
	}
	if len(id.SerializedMfst) == 0 {
		t.Error("SerializedMfst must be populated for #372 emission")
	}
	// NodeID is calcNodeID(MasterKey) — the 20-byte master-derived
	// identifier matching rippled's NodeID. It must NOT match the
	// signing pubkey (token mode rotates the ephemeral key) and it
	// must match the canonical calcNodeID derivation so peers compute
	// the same identifier for our master key.
	wantNodeID := consensus.CalcNodeID(fix.masterPub)
	if id.NodeID != wantNodeID {
		t.Errorf("NodeID = %x, want calcNodeID(MasterKey) = %x", id.NodeID, wantNodeID)
	}
	// Sanity: a calcNodeID over the signing key would diverge from the
	// master-derived value when keys are rotated (token mode).
	if id.NodeID == consensus.CalcNodeID(fix.signingPub) {
		t.Error("NodeID coincides with calcNodeID(SigningKey); token mode must derive from master")
	}
}

func TestNewValidatorIdentityFromToken_SignVerifyValidation(t *testing.T) {
	fix := newTokenFixture(t, 0x55, 3)
	id, err := NewValidatorIdentityFromToken(fix.tokenBlock)
	if err != nil {
		t.Fatalf("NewValidatorIdentityFromToken: %v", err)
	}

	v := &consensus.Validation{
		LedgerID:  consensus.LedgerID{0x01, 0x02},
		LedgerSeq: 42,
		SignTime:  time.Unix(protocol.RippleEpochUnix+1000, 0),
		Full:      true,
	}
	if err := id.SignValidation(v); err != nil {
		t.Fatalf("SignValidation: %v", err)
	}
	if len(v.Signature) == 0 {
		t.Fatal("expected non-empty signature")
	}
	if err := VerifyValidation(v); err != nil {
		t.Fatalf("VerifyValidation: %v", err)
	}
}

func TestNewValidatorIdentityFromToken_KeyMismatch(t *testing.T) {
	fix := newTokenFixture(t, 0x21, 1)
	// Replace the validation_secret_key with an unrelated 32 bytes:
	// derived pubkey will no longer match the manifest's SigningPubKey,
	// catching swapped/corrupt token blobs.
	tokenBytes, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(fix.tokenBlock), ""))
	if err != nil {
		t.Fatalf("decode token: %v", err)
	}
	var envelope map[string]string
	if err := json.Unmarshal(tokenBytes, &envelope); err != nil {
		t.Fatalf("unmarshal token: %v", err)
	}
	envelope["validation_secret_key"] = strings.Repeat("11", 32)
	bad, _ := json.Marshal(envelope)
	corrupted := base64.StdEncoding.EncodeToString(bad)

	if _, err := NewValidatorIdentityFromToken(corrupted); err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
}

func TestNewValidatorIdentityFromConfig_Dispatch(t *testing.T) {
	// Empty → observer.
	id, err := NewValidatorIdentityFromConfig("", "")
	if err != nil || id != nil {
		t.Fatalf("empty config should yield nil identity, got id=%v err=%v", id, err)
	}

	// Both set → mutual-exclusion error.
	if _, err := NewValidatorIdentityFromConfig("snoPBrXtMeMyMHUVTgbuqAfg1SUTb", "anything"); err == nil {
		t.Fatal("expected error when both seed and token configured")
	}

	// Token only → token path.
	fix := newTokenFixture(t, 0x77, 5)
	id, err = NewValidatorIdentityFromConfig("", fix.tokenBlock)
	if err != nil {
		t.Fatalf("token-only config: %v", err)
	}
	if id == nil || id.Manifest == nil {
		t.Fatal("token-only config must produce manifest-bearing identity")
	}

	// Seed only → seed path (manifest stays nil).
	id, err = NewValidatorIdentityFromConfig("snoPBrXtMeMyMHUVTgbuqAfg1SUTb", "")
	if err != nil {
		t.Fatalf("seed-only config: %v", err)
	}
	if id == nil {
		t.Fatal("seed-only config must produce identity")
	}
	if id.Manifest != nil {
		t.Fatal("seed-only config must not carry a manifest")
	}
	if id.MasterKey != id.SigningKey {
		t.Fatal("seed-only config: master should equal signing")
	}
}

package adaptor

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
)

// TestVerify_DispatchesByKeyType pins the key-type-aware dispatch in
// adaptor.Verify. Before this dispatch landed, Verify hardcoded
// secp256k1 — every ed25519-signed validation from a peer silently
// failed verification. With 5-validator UNL and quorum=4, even one
// or two ed25519-signing rippled validators dropped goxrpl below
// quorum and stalled the network at val_seq=5.
//
// Properties pinned:
//  1. ed25519 (0xED prefix) verifies correctly.
//  2. secp256k1 (0x02/0x03 prefix) still verifies correctly (regression
//     guard for the secp256k1 path).
//  3. Wrong-algorithm verification fails (cross-key rejection).
//  4. Garbage prefix rejects without panic.
//  5. Non-33-byte input rejects without panic.
func TestVerify_DispatchesByKeyType(t *testing.T) {
	digest := common.Sha512Half([]byte("test message for both key types"))

	// --- ed25519 path ---
	edPub, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	edSig := ed25519.Sign(edPriv, digest[:])
	edWirePubKey := append([]byte{0xED}, edPub...) // 33-byte: 0xED + 32-byte raw key
	if !Verify(edWirePubKey, digest[:], edSig) {
		t.Errorf("ed25519: legitimate signature rejected (key-type dispatch missing — " +
			"this is the bug that stalled the all-5 UNL bootstrap at val_seq=5)")
	}

	// --- secp256k1 path ---
	algo := secp256k1.SECP256K1()
	secPriv := make([]byte, 32)
	if _, err := rand.Read(secPriv); err != nil {
		t.Fatalf("rand: %v", err)
	}
	secPub, err := algo.DerivePublicKeyFromSecret(secPriv)
	if err != nil {
		t.Fatalf("DerivePublicKey: %v", err)
	}
	if len(secPub) != 33 || (secPub[0] != 0x02 && secPub[0] != 0x03) {
		t.Fatalf("expected 33-byte compressed secp256k1 pubkey with 0x02/0x03 prefix, "+
			"got %d bytes prefix=%x", len(secPub), secPub[0])
	}
	secSig, err := secp256k1.SignDigestBytes(digest[:], secPriv)
	if err != nil {
		t.Fatalf("SignDigest: %v", err)
	}
	if !Verify(secPub, digest[:], secSig) {
		t.Error("secp256k1: legitimate signature rejected (regression on the existing path)")
	}

	// --- cross-key rejection ---
	if Verify(edWirePubKey, digest[:], secSig) {
		t.Error("ed25519 pubkey + secp256k1 signature wrongly accepted (cross-key leak)")
	}
	if Verify(secPub, digest[:], edSig) {
		t.Error("secp256k1 pubkey + ed25519 signature wrongly accepted (cross-key leak)")
	}

	// --- malformed inputs reject cleanly ---
	garbagePrefix := append([]byte{0xFF}, edPub...)
	if Verify(garbagePrefix, digest[:], edSig) {
		t.Error("0xFF-prefix pubkey wrongly accepted")
	}
	if Verify(edWirePubKey[:32], digest[:], edSig) {
		t.Error("32-byte pubkey (wrong length) wrongly accepted")
	}
	if Verify(edWirePubKey, digest[:], edSig[:32]) {
		t.Error("32-byte ed25519 signature (wrong length) wrongly accepted")
	}
}

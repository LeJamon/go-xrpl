package ed25519

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"strings"

	rootcrypto "github.com/LeJamon/goXRPLd/crypto"
	"github.com/LeJamon/goXRPLd/crypto/common"
)

const (
	// ed25519 prefix - value is 237
	ed25519Prefix byte = 0xED
)

// ed25519FamilySeedPrefixBytes is the three-byte family seed prefix for
// ed25519 keys per XRPL's address codec. Callers must not mutate the slice
// returned by FamilySeedPrefix.
var ed25519FamilySeedPrefixBytes = []byte{0x01, 0xE1, 0x4B}

var (
	_ rootcrypto.Algorithm = &ED25519CryptoAlgorithm{}

	// ErrValidatorNotSupported is returned when a validator keypair is used with the ED25519 algorithm.
	ErrValidatorNotSupported = errors.New("validator keypairs can not use Ed25519")
	// ErrInvalidPrivateKey is returned when a private key is invalid
	ErrInvalidPrivateKey = errors.New("invalid private key")
	// ErrInvalidPublicKey is returned when a public key is the wrong length.
	ErrInvalidPublicKey = errors.New("invalid public key")
	// ErrInvalidSignature is returned when an ed25519 signature is not 64 bytes.
	ErrInvalidSignature = errors.New("invalid signature")
)

// ED25519CryptoAlgorithm is the implementation of the ED25519 cryptographic algorithm.
type ED25519CryptoAlgorithm struct {
	prefix           byte
	familySeedPrefix []byte
}

// ED25519 returns the ED25519 cryptographic algorithm.
func ED25519() ED25519CryptoAlgorithm {
	return ED25519CryptoAlgorithm{
		prefix:           ed25519Prefix,
		familySeedPrefix: ed25519FamilySeedPrefixBytes,
	}
}

// Prefix returns the prefix for the ED25519 cryptographic algorithm.
func (c ED25519CryptoAlgorithm) Prefix() byte {
	return c.prefix
}

// FamilySeedPrefix returns the family seed prefix for the ED25519 cryptographic algorithm.
// The returned slice aliases shared package state; callers must not mutate it.
func (c ED25519CryptoAlgorithm) FamilySeedPrefix() []byte {
	return c.familySeedPrefix
}

// DeriveKeypair derives a keypair from a seed.
func (c ED25519CryptoAlgorithm) DeriveKeypair(decodedSeed []byte, validator bool) (string, string, error) {
	if validator {
		return "", "", ErrValidatorNotSupported
	}
	rawPriv := common.Sha512Half(decodedSeed)
	pubKey, privKey, err := ed25519.GenerateKey(bytes.NewBuffer(rawPriv[:]))
	if err != nil {
		return "", "", err
	}
	pubKey = append([]byte{c.prefix}, pubKey...)
	public := strings.ToUpper(hex.EncodeToString(pubKey))
	privKey = append([]byte{c.prefix}, privKey...)
	private := strings.ToUpper(hex.EncodeToString(privKey[:32+len([]byte{c.prefix})]))
	return private, public, nil
}

// SignBytes signs msg with a 32-byte ed25519 seed. privKey must be exactly
// 32 bytes; the leading 0xED prefix accepted by the hex Sign wrapper is
// stripped there, not here. This mirrors secp256k1.SignBytes's 32-only
// contract so callers reasoning about raw key buffers see a symmetric API.
func (c ED25519CryptoAlgorithm) SignBytes(msg, privKey []byte) ([]byte, error) {
	if len(privKey) != ed25519.SeedSize {
		return nil, ErrInvalidPrivateKey
	}
	return ed25519.Sign(ed25519.NewKeyFromSeed(privKey), msg), nil
}

// Sign signs msg with the hex-encoded ed25519 private seed. Accepts either
// the raw 32-byte seed (64 hex chars) or the 33-byte 0xED-prefixed form
// (66 hex chars); the prefix is stripped before delegating to SignBytes.
// Output is uppercase hex.
func (c ED25519CryptoAlgorithm) Sign(msg, privKey string) (string, error) {
	b, err := hex.DecodeString(privKey)
	if err != nil {
		return "", ErrInvalidPrivateKey
	}
	if len(b) == ed25519.SeedSize+1 && b[0] == c.prefix {
		b = b[1:]
	}
	sig, err := c.SignBytes([]byte(msg), b)
	if err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(sig)), nil
}

// ValidateBytes verifies sig over msg with pubKey (33 bytes: 0xED prefix +
// 32 raw bytes). Mirrors rippled's PublicKey.cpp:302-313 by gating verify
// on the explicit ed25519Canonical(sig) (s < L) check before invoking the
// primitive. Go's stdlib already enforces s < L internally per RFC 8032,
// but rippled's contract is to reject non-canonical signatures up front.
func (c ED25519CryptoAlgorithm) ValidateBytes(msg, pubKey, sig []byte) bool {
	if len(pubKey) != 33 {
		return false
	}
	if len(sig) != ed25519.SignatureSize {
		return false
	}
	if !rootcrypto.Ed25519Canonical(sig) {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pubKey[1:]), msg, sig)
}

// Validate verifies a hex-encoded signature for a message with a public key.
func (c ED25519CryptoAlgorithm) Validate(msg, pubkey, sig string) bool {
	bp, err := hex.DecodeString(pubkey)
	if err != nil {
		return false
	}
	bs, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	return c.ValidateBytes([]byte(msg), bp, bs)
}

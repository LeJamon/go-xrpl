package secp256k1

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"

	rootcrypto "github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1/shim"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
)

const (
	// SECP256K1 prefix - value is 0
	secp256K1Prefix byte = 0x00
	// SECP256K1 family seed prefix - value is 33
	secp256K1FamilySeedPrefix byte = 0x21
)

// secp256K1FamilySeedPrefixBytes is the byte-slice form returned by
// FamilySeedPrefix. Callers must not mutate the returned slice.
var secp256K1FamilySeedPrefixBytes = []byte{secp256K1FamilySeedPrefix}

var (
	_ rootcrypto.Algorithm = Algorithm{}

	// ErrInvalidPrivateKey is returned when a private key is invalid
	ErrInvalidPrivateKey = errors.New("invalid private key")
	// ErrInvalidMessage is returned when a message is required but not provided
	ErrInvalidMessage = errors.New("message is required")
	// ErrScalarDerivation is returned when family-seed scalar derivation fails to
	// find a valid scalar within the bounded retries. Reaching this is practically
	// impossible (see deriveScalar).
	ErrScalarDerivation = errors.New("unable to derive scalar from seed")
)

// Algorithm implements crypto.Algorithm for the secp256k1 signature scheme.
// It is stateless: the zero value Algorithm{} is ready to use.
type Algorithm struct{}

// Prefix returns the public-key type prefix for the secp256k1 algorithm.
func (c Algorithm) Prefix() byte {
	return secp256K1Prefix
}

// FamilySeedPrefix returns the family seed prefix for the secp256k1 algorithm.
// The returned slice aliases shared package state; callers must not mutate it.
func (c Algorithm) FamilySeedPrefix() []byte {
	return secp256K1FamilySeedPrefixBytes
}

// deriveScalar derives a scalar from a seed using the rippled "XRP Family
// Generator" construction: SHA512(seed | optional discriminator | i+)
// truncated to 32 bytes, retrying until libsecp256k1 accepts the candidate.
// The loop almost always exits on the first iteration; it returns
// ErrScalarDerivation if no valid scalar is found within 128 retries.
func (c Algorithm) deriveScalar(seed []byte, discriminator *uint32) ([]byte, error) {
	bufLen := len(seed) + 4
	if discriminator != nil {
		bufLen += 4
	}
	buf := make([]byte, bufLen)
	defer rootcrypto.SecureErase(buf)
	copy(buf, seed)

	counterOffset := len(seed)
	if discriminator != nil {
		binary.BigEndian.PutUint32(buf[counterOffset:], *discriminator)
		counterOffset += 4
	}

	for counter := uint32(0); counter < 128; counter++ {
		binary.BigEndian.PutUint32(buf[counterOffset:], counter)
		hash := sha512half.Sum(buf)
		if shim.SecretKeyValid(hash[:]) {
			candidate := append([]byte(nil), hash[:]...)
			rootcrypto.SecureErase(hash[:])
			return candidate, nil
		}
		rootcrypto.SecureErase(hash[:])
	}

	return nil, ErrScalarDerivation
}

// DeriveKeypair derives a keypair from a seed, returning the hex-encoded
// private then public key. For regular (non-validator) keys, the derivation
// uses an additional scalar derived from the root public key. For validator
// keys, only the root generator is used.
func (c Algorithm) DeriveKeypair(seed []byte, validator bool) (privHex, pubHex string, err error) {
	privateKey, publicKey, err := c.DeriveKeypairBytes(seed, validator)
	if err != nil {
		return "", "", err
	}
	defer rootcrypto.SecureErase(privateKey)
	return "00" + strings.ToUpper(hex.EncodeToString(privateKey)), strings.ToUpper(hex.EncodeToString(publicKey)), nil
}

// DeriveKeypairBytes derives a keypair from a seed and returns owned raw key
// buffers. The private key is a 32-byte scalar and the public key is compressed.
// Callers should erase the private-key buffer when it is no longer needed.
func (c Algorithm) DeriveKeypairBytes(seed []byte, validator bool) (privateBytes, publicBytes []byte, err error) {
	// Derive the root private generator from the seed
	privateGen, err := c.deriveScalar(seed, nil)
	if err != nil {
		return nil, nil, err
	}

	if validator {
		// For validator keys, use the root generator directly.
		publicBytes, ok := shim.PublicKeyCreate(privateGen)
		if !ok {
			rootcrypto.SecureErase(privateGen)
			return nil, nil, ErrInvalidPrivateKey
		}
		return privateGen, publicBytes, nil
	}

	defer rootcrypto.SecureErase(privateGen)
	// For regular keys, derive an additional scalar from the root public key.
	rootPublic, ok := shim.PublicKeyCreate(privateGen)
	if !ok {
		return nil, nil, ErrInvalidPrivateKey
	}
	var zero uint32
	derivedScalar, err := c.deriveScalar(rootPublic, &zero)
	if err != nil {
		return nil, nil, err
	}
	defer rootcrypto.SecureErase(derivedScalar)
	privateBytes, ok = shim.SecretKeyTweakAdd(privateGen, derivedScalar)
	if !ok {
		return nil, nil, ErrInvalidPrivateKey
	}
	publicBytes, ok = shim.PublicKeyCreate(privateBytes)
	if !ok {
		rootcrypto.SecureErase(privateBytes)
		return nil, nil, ErrInvalidPrivateKey
	}
	return privateBytes, publicBytes, nil
}

// SignBytes signs msg with a 32-byte raw secp256k1 private key and returns
// the DER-encoded signature in bytes.
func (c Algorithm) SignBytes(msg, privKey []byte) ([]byte, error) {
	if err := validatePrivateKey(privKey); err != nil {
		return nil, err
	}
	if len(msg) == 0 {
		return nil, ErrInvalidMessage
	}
	hash := sha512half.Sum(msg)
	sig, ok := shim.SignDigest(hash[:], privKey)
	if !ok {
		return nil, ErrInvalidPrivateKey
	}
	return sig, nil
}

func validatePrivateKey(privKey []byte) error {
	if len(privKey) != 32 {
		return ErrInvalidPrivateKey
	}

	if !shim.SecretKeyValid(privKey) {
		return ErrInvalidPrivateKey
	}
	return nil
}

// decodePrivKeyHex decodes a secp256k1 private key supplied as either a bare
// 64-hex-char (32-byte) scalar or the 66-char 0x00-prefixed form. It validates
// the length and, for the prefixed form, that the prefix is exactly "00"
// (parity with ed25519.Sign's prefix check), returning the raw 32-byte scalar.
func decodePrivKeyHex(privKeyHex string) ([]byte, error) {
	if len(privKeyHex) != 64 && len(privKeyHex) != 66 {
		return nil, ErrInvalidPrivateKey
	}
	if len(privKeyHex) == 66 {
		if privKeyHex[:2] != "00" {
			return nil, ErrInvalidPrivateKey
		}
		privKeyHex = privKeyHex[2:]
	}
	key, err := hex.DecodeString(privKeyHex)
	if err != nil {
		return nil, ErrInvalidPrivateKey
	}
	return key, nil
}

// Sign signs a message with a private key (hex-encoded, optionally
// 0x00-prefixed). The returned signature is the uppercase hex form of the
// DER-encoded signature.
func (c Algorithm) Sign(msg, privKey string) (string, error) {
	key, err := decodePrivKeyHex(privKey)
	if err != nil {
		return "", err
	}
	defer rootcrypto.SecureErase(key)
	sig, err := c.SignBytes([]byte(msg), key)
	if err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(sig)), nil
}

// SignDigest signs a pre-computed 32-byte digest directly without re-hashing.
// Matches rippled's signDigest() which passes the SHA-512Half hash directly
// to secp256k1 signing. The private key hex is validated exactly like Sign,
// then signing is delegated to the validated [SignDigestBytes] core.
func (c Algorithm) SignDigest(digest [32]byte, privKeyHex string) ([]byte, error) {
	key, err := decodePrivKeyHex(privKeyHex)
	if err != nil {
		return nil, err
	}
	defer rootcrypto.SecureErase(key)
	return SignDigestBytes(digest[:], key)
}

// Validate validates a signature for a message with a public key.
// It checks that the signature is fully canonical (low S) to prevent
// signature malleability attacks.
func (c Algorithm) Validate(msg, pubkey, sig string) bool {
	return c.ValidateWithCanonicality(msg, pubkey, sig, true)
}

// ValidateWithCanonicality validates a signature with optional canonicality checking.
// If mustBeFullyCanonical is true, the signature must have S <= curve_order/2.
func (c Algorithm) ValidateWithCanonicality(msg, pubkey, sig string, mustBeFullyCanonical bool) bool {
	sigBytes, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	pubkeyBytes, err := hex.DecodeString(pubkey)
	if err != nil {
		return false
	}
	return c.validateBytes([]byte(msg), pubkeyBytes, sigBytes, mustBeFullyCanonical, true)
}

// ValidateBytes verifies a fully-canonical DER signature with a SHA-512Half-of-msg digest.
func (c Algorithm) ValidateBytes(msg, pubkey, sig []byte) bool {
	return c.validateBytes(msg, pubkey, sig, true, true)
}

// validateBytes is the byte-level core used by Validate/ValidateBytes/ValidateDigest.
// When hashMsg is true the message is SHA-512Half-hashed before verification;
// otherwise msg is treated as a pre-computed 32-byte digest.
func (c Algorithm) validateBytes(msg, pubkey, sig []byte, mustBeFullyCanonical, hashMsg bool) bool {
	if rootcrypto.PublicKeyType(pubkey) != rootcrypto.KeyTypeSecp256k1 {
		return false
	}
	canonicality := rootcrypto.ECDSACanonicality(sig)
	if canonicality == rootcrypto.CanonicalityNone {
		return false
	}
	if mustBeFullyCanonical && canonicality != rootcrypto.CanonicalityFullyCanonical {
		return false
	}
	var digest [32]byte
	if hashMsg {
		digest = sha512half.Sum(msg)
	} else {
		if len(msg) != 32 {
			return false
		}
		copy(digest[:], msg)
	}
	return verifyDigestRaw(digest[:], pubkey, sig)
}

// ValidateDigest verifies a signature against a pre-computed digest (hash).
// Unlike Validate, this does NOT re-hash the data — it uses the digest directly.
// Matches rippled's verifyDigest() which passes the SHA-512Half hash directly
// to secp256k1_ecdsa_verify.
func (c Algorithm) ValidateDigest(digest [32]byte, pubkeyBytes []byte, sigBytes []byte) bool {
	return c.ValidateDigestWithCanonicality(digest, pubkeyBytes, sigBytes, false)
}

// ValidateDigestWithCanonicality verifies a pre-computed digest and optionally
// requires a fully canonical low-S signature.
func (c Algorithm) ValidateDigestWithCanonicality(digest [32]byte, pubkeyBytes []byte, sigBytes []byte, mustBeFullyCanonical bool) bool {
	return c.validateBytes(digest[:], pubkeyBytes, sigBytes, mustBeFullyCanonical, false)
}

// DerivePublicKeyFromPublicGenerator derives a public key from a public generator.
func (c Algorithm) DerivePublicKeyFromPublicGenerator(pubKey []byte) ([]byte, error) {
	// Parse the input public key to validate it, while retaining the original
	// serialization for the XRPL family-generator hash.
	if _, ok := shim.ParsePublicKey(pubKey, true); !ok {
		return nil, errors.New("invalid public key")
	}
	var zero uint32
	scalar, err := c.deriveScalar(pubKey, &zero)
	if err != nil {
		return nil, err
	}
	defer rootcrypto.SecureErase(scalar)
	publicKey, ok := shim.PublicKeyTweakAdd(pubKey, scalar)
	if !ok {
		return nil, errors.New("invalid public key tweak")
	}
	return publicKey, nil
}

// DerivePublicKeyFromSecret returns the 33-byte compressed secp256k1
// public key for a raw 32-byte secret. Mirrors rippled's
// derivePublicKey(KeyType::secp256k1, SecretKey) used by validator-token
// loading, where the JSON `validation_secret_key` already is the raw
// scalar (no seed expansion).
func (c Algorithm) DerivePublicKeyFromSecret(secret []byte) ([]byte, error) {
	if err := validatePrivateKey(secret); err != nil {
		return nil, err
	}
	publicKey, ok := shim.PublicKeyCreate(secret)
	if !ok {
		return nil, ErrInvalidPrivateKey
	}
	return publicKey, nil
}

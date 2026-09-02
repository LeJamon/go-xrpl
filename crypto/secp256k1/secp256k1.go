package secp256k1

import (
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"

	rootcrypto "github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	ecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
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
// Generator" construction: SHA512(seed | optional discrim | i++) truncated to
// 32 bytes, retrying until the result is in (0, n). The loop almost always
// exits on the first iteration; it returns ErrScalarDerivation if no valid
// scalar is found within 128 retries, mirroring rippled's bounded retry.
func (c Algorithm) deriveScalar(seed []byte, discrim *big.Int) (*big.Int, error) {
	order := btcec.S256().N
	hasher := sha512.New()
	sum := make([]byte, 0, sha512.Size)
	defer func() {
		rootcrypto.SecureErase(sum)
	}()

	var discrimWord uint32
	var hasDiscrim bool
	if discrim != nil {
		discrimWord = uint32(discrim.Uint64())
		hasDiscrim = true
	}

	var tailBuf [8]byte
	tail := tailBuf[:0]
	if hasDiscrim {
		tail = append(tail,
			byte(discrimWord>>24),
			byte(discrimWord>>16),
			byte(discrimWord>>8),
			byte(discrimWord),
		)
	}
	tailLen := len(tail)
	// Reserve four bytes for the loop counter.
	tail = tail[:tailLen+4]

	zero := big.NewInt(0)
	key := new(big.Int)

	for i := range uint32(128) {
		tail[tailLen] = byte(i >> 24)
		tail[tailLen+1] = byte(i >> 16)
		tail[tailLen+2] = byte(i >> 8)
		tail[tailLen+3] = byte(i)

		hasher.Reset()
		hasher.Write(seed)
		hasher.Write(tail)
		sum = hasher.Sum(sum[:0])

		key.SetBytes(sum[:32])
		if key.Cmp(zero) > 0 && key.Cmp(order) < 0 {
			// Return a fresh allocation so callers can mutate the result freely.
			return new(big.Int).Set(key), nil
		}
	}
	// Practically unreachable: the odds of 128 consecutive candidates failing
	// the curve-order check are negligible. rippled likewise gives up here
	// (SecretKey.cpp deriveDeterministicRootKey / Generator::calculateTweak).
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
	curve := btcec.S256()
	order := curve.N

	// Derive the root private generator from the seed
	privateGen, err := c.deriveScalar(seed, nil)
	if err != nil {
		return nil, nil, err
	}

	var privateKey *big.Int
	if validator {
		// For validator keys, use the root generator directly
		privateKey = privateGen
	} else {
		// For regular keys, derive an additional scalar from the root public key
		privateGenBytes := privateGen.Bytes()
		defer rootcrypto.SecureErase(privateGenBytes)
		rootPrivateKey, _ := btcec.PrivKeyFromBytes(privateGenBytes)
		defer rootPrivateKey.Zero()
		derivatedScalar, err := c.deriveScalar(rootPrivateKey.PubKey().SerializeCompressed(), big.NewInt(0))
		if err != nil {
			return nil, nil, err
		}
		scalarWithPrivateGen := derivatedScalar.Add(derivatedScalar, privateGen)
		privateKey = scalarWithPrivateGen.Mod(scalarWithPrivateGen, order)
	}

	// Ensure private key is 32 bytes with leading zeros if needed
	privKeyBytes := make([]byte, 32)
	keyBytes := privateKey.Bytes()
	defer rootcrypto.SecureErase(keyBytes)
	copy(privKeyBytes[32-len(keyBytes):], keyBytes)

	privateKeyObject, pubKey := btcec.PrivKeyFromBytes(privKeyBytes)
	defer privateKeyObject.Zero()
	pubKeyBytes := pubKey.SerializeCompressed()

	return privKeyBytes, pubKeyBytes, nil
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
	secpPrivKey := secp256k1.PrivKeyFromBytes(privKey)
	defer secpPrivKey.Zero()
	hash := sha512half.Sum(msg)
	sig := ecdsa.Sign(secpPrivKey, hash[:])
	return derFromRS(sig.R(), sig.S()), nil
}

func validatePrivateKey(privKey []byte) error {
	if len(privKey) != 32 {
		return ErrInvalidPrivateKey
	}

	var scalar secp256k1.ModNScalar
	defer scalar.Zero()
	if scalar.SetByteSlice(privKey) || scalar.IsZero() {
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
	curve := btcec.S256()

	// Parse the input public key as a point
	rootPubKey, err := btcec.ParsePubKey(pubKey)
	if err != nil {
		return nil, err
	}

	// Derive scalar using existing function
	scalar, err := c.deriveScalar(pubKey, big.NewInt(0))
	if err != nil {
		return nil, err
	}

	// Multiply base point with scalar
	x, y := curve.ScalarBaseMult(scalar.Bytes())
	xField, yField := secp256k1.FieldVal{}, secp256k1.FieldVal{}

	xField.SetByteSlice(x.Bytes())
	yField.SetByteSlice(y.Bytes())

	scalarPoint := secp256k1.NewPublicKey(&xField, &yField)

	// Add the points
	resultX, resultY := curve.Add(
		rootPubKey.X(), rootPubKey.Y(),
		scalarPoint.X(), scalarPoint.Y(),
	)

	resultXField, resultYField := secp256k1.FieldVal{}, secp256k1.FieldVal{}
	resultXField.SetByteSlice(resultX.Bytes())
	resultYField.SetByteSlice(resultY.Bytes())

	// Create the final public key
	finalPubKey := secp256k1.NewPublicKey(&resultXField, &resultYField)

	return finalPubKey.SerializeCompressed(), nil
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
	privateKey, pubKey := btcec.PrivKeyFromBytes(secret)
	defer privateKey.Zero()
	return pubKey.SerializeCompressed(), nil
}

// derFromRS builds a DER-encoded signature directly from a decred ModNScalar
// r/s pair, avoiding a string→hex→bytes round-trip.
func derFromRS(r, s secp256k1.ModNScalar) []byte {
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	return rootcrypto.EncodeDERSignature(
		new(big.Int).SetBytes(rBytes[:]),
		new(big.Int).SetBytes(sBytes[:]),
	)
}

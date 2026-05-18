package secp256k1

import (
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"

	rootcrypto "github.com/LeJamon/goXRPLd/crypto"
	"github.com/LeJamon/goXRPLd/crypto/common"

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
	_ rootcrypto.Algorithm = SECP256K1CryptoAlgorithm{}

	// ErrInvalidPrivateKey is returned when a private key is invalid
	ErrInvalidPrivateKey = errors.New("invalid private key")
	// ErrInvalidMessage is returned when a message is required but not provided
	ErrInvalidMessage = errors.New("message is required")
	// ErrInvalidSignature is returned when a signature is invalid or not fully canonical
	ErrInvalidSignature = errors.New("invalid signature")
	// ErrSignatureNotCanonical is returned when a signature is not fully canonical
	ErrSignatureNotCanonical = errors.New("signature is not fully canonical")
)

// SECP256K1CryptoAlgorithm is the implementation of the SECP256K1 algorithm.
type SECP256K1CryptoAlgorithm struct {
	prefix           byte
	familySeedPrefix []byte
}

// SECP256K1 returns a new SECP256K1CryptoAlgorithm instance.
func SECP256K1() SECP256K1CryptoAlgorithm {
	return SECP256K1CryptoAlgorithm{
		prefix:           secp256K1Prefix,
		familySeedPrefix: secp256K1FamilySeedPrefixBytes,
	}
}

// Prefix returns the prefix for the SECP256K1 algorithm.
func (c SECP256K1CryptoAlgorithm) Prefix() byte {
	return c.prefix
}

// FamilySeedPrefix returns the family seed prefix for the SECP256K1 algorithm.
// The returned slice aliases shared package state; callers must not mutate it.
func (c SECP256K1CryptoAlgorithm) FamilySeedPrefix() []byte {
	return c.familySeedPrefix
}

// deriveScalar derives a scalar from a seed using the rippled "XRP Family
// Generator" construction: SHA512(seed | optional discrim | i++) truncated to
// 32 bytes, retrying until the result is in (0, n). The loop almost always
// exits on the first iteration.
func (c SECP256K1CryptoAlgorithm) deriveScalar(seed []byte, discrim *big.Int) *big.Int {
	order := btcec.S256().N
	hasher := sha512.New()
	sum := make([]byte, 0, sha512.Size)

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

	for i := uint32(0); i <= 0xffffffff; i++ {
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
			return new(big.Int).Set(key)
		}
	}
	// This error is practically impossible to reach.
	// The order of the curve describes the (finite) amount of points on the curve.
	panic("secp256k1.deriveScalar: exhausted all 2^32 candidates")
}

// DeriveKeypair derives a keypair from a seed.
// For regular (non-validator) keys, the derivation uses an additional scalar derived
// from the root public key. For validator keys, only the root generator is used.
func (c SECP256K1CryptoAlgorithm) DeriveKeypair(seed []byte, validator bool) (string, string, error) {
	curve := btcec.S256()
	order := curve.N

	// Derive the root private generator from the seed
	privateGen := c.deriveScalar(seed, nil)

	var privateKey *big.Int
	if validator {
		// For validator keys, use the root generator directly
		privateKey = privateGen
	} else {
		// For regular keys, derive an additional scalar from the root public key
		rootPrivateKey, _ := btcec.PrivKeyFromBytes(privateGen.Bytes())
		derivatedScalar := c.deriveScalar(rootPrivateKey.PubKey().SerializeCompressed(), big.NewInt(0))
		scalarWithPrivateGen := derivatedScalar.Add(derivatedScalar, privateGen)
		privateKey = scalarWithPrivateGen.Mod(scalarWithPrivateGen, order)
	}

	// Ensure private key is 32 bytes with leading zeros if needed
	privKeyBytes := make([]byte, 32)
	keyBytes := privateKey.Bytes()
	copy(privKeyBytes[32-len(keyBytes):], keyBytes)

	private := strings.ToUpper(hex.EncodeToString(privKeyBytes))

	_, pubKey := btcec.PrivKeyFromBytes(privKeyBytes)
	pubKeyBytes := pubKey.SerializeCompressed()

	return "00" + private, strings.ToUpper(hex.EncodeToString(pubKeyBytes)), nil
}

// SignBytes signs msg with a 32-byte raw secp256k1 private key and returns
// the DER-encoded signature in bytes.
func (c SECP256K1CryptoAlgorithm) SignBytes(msg, privKey []byte) ([]byte, error) {
	if len(privKey) != 32 {
		return nil, ErrInvalidPrivateKey
	}
	if len(msg) == 0 {
		return nil, ErrInvalidMessage
	}
	secpPrivKey := secp256k1.PrivKeyFromBytes(privKey)
	hash := common.Sha512Half(msg)
	sig := ecdsa.Sign(secpPrivKey, hash[:])
	return derFromRS(sig.R(), sig.S()), nil
}

// Sign signs a message with a private key (hex-encoded, optionally
// 0x00-prefixed). The returned signature is the uppercase hex form of the
// DER-encoded signature.
func (c SECP256K1CryptoAlgorithm) Sign(msg, privKey string) (string, error) {
	if len(privKey) != 64 && len(privKey) != 66 {
		return "", ErrInvalidPrivateKey
	}
	if len(privKey) == 66 {
		privKey = privKey[2:]
	}
	key, err := hex.DecodeString(privKey)
	if err != nil {
		return "", ErrInvalidPrivateKey
	}
	sig, err := c.SignBytes([]byte(msg), key)
	if err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(sig)), nil
}

// SignDigest signs a pre-computed digest (hash) directly without re-hashing.
// Matches rippled's signDigest() which passes the SHA-512Half hash directly
// to secp256k1 signing.
func (c SECP256K1CryptoAlgorithm) SignDigest(digest [32]byte, privKeyHex string) ([]byte, error) {
	if len(privKeyHex) == 66 {
		privKeyHex = privKeyHex[2:]
	}
	key, err := hex.DecodeString(privKeyHex)
	if err != nil {
		return nil, ErrInvalidPrivateKey
	}
	secpPrivKey := secp256k1.PrivKeyFromBytes(key)
	sig := ecdsa.Sign(secpPrivKey, digest[:])
	return derFromRS(sig.R(), sig.S()), nil
}

// Validate validates a signature for a message with a public key.
// It checks that the signature is fully canonical (low S) to prevent
// signature malleability attacks.
func (c SECP256K1CryptoAlgorithm) Validate(msg, pubkey, sig string) bool {
	return c.ValidateWithCanonicality(msg, pubkey, sig, true)
}

// ValidateWithCanonicality validates a signature with optional canonicality checking.
// If mustBeFullyCanonical is true, the signature must have S <= curve_order/2.
func (c SECP256K1CryptoAlgorithm) ValidateWithCanonicality(msg, pubkey, sig string, mustBeFullyCanonical bool) bool {
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
func (c SECP256K1CryptoAlgorithm) ValidateBytes(msg, pubkey, sig []byte) bool {
	return c.validateBytes(msg, pubkey, sig, true, true)
}

// validateBytes is the byte-level core used by Validate/ValidateBytes/ValidateDigest.
// When hashMsg is true the message is SHA-512Half-hashed before verification;
// otherwise msg is treated as a pre-computed 32-byte digest.
func (c SECP256K1CryptoAlgorithm) validateBytes(msg, pubkey, sig []byte, mustBeFullyCanonical, hashMsg bool) bool {
	canonicality := rootcrypto.ECDSACanonicality(sig)
	if canonicality == rootcrypto.CanonicityNone {
		return false
	}
	if mustBeFullyCanonical && canonicality != rootcrypto.CanonicityFullyCanonical {
		return false
	}
	var digest [32]byte
	if hashMsg {
		digest = common.Sha512Half(msg)
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
func (c SECP256K1CryptoAlgorithm) ValidateDigest(digest [32]byte, pubkeyBytes []byte, sigBytes []byte) bool {
	return c.validateBytes(digest[:], pubkeyBytes, sigBytes, false, false)
}

// DerivePublicKeyFromPublicGenerator derives a public key from a public generator.
func (c SECP256K1CryptoAlgorithm) DerivePublicKeyFromPublicGenerator(pubKey []byte) ([]byte, error) {
	curve := btcec.S256()

	// Parse the input public key as a point
	rootPubKey, err := btcec.ParsePubKey(pubKey)
	if err != nil {
		return nil, err
	}

	// Derive scalar using existing function
	scalar := c.deriveScalar(pubKey, big.NewInt(0))

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

// SignCanonical signs a message and ensures the signature is fully canonical.
// It automatically normalizes the S value if needed to produce a low-S signature.
func (c SECP256K1CryptoAlgorithm) SignCanonical(msg, privKey string) (string, error) {
	if len(privKey) != 64 && len(privKey) != 66 {
		return "", ErrInvalidPrivateKey
	}
	if len(privKey) == 66 {
		privKey = privKey[2:]
	}
	key, err := hex.DecodeString(privKey)
	if err != nil {
		return "", ErrInvalidPrivateKey
	}
	sigBytes, err := c.SignBytes([]byte(msg), key)
	if err != nil {
		return "", err
	}
	canonicality := rootcrypto.ECDSACanonicality(sigBytes)
	if canonicality == rootcrypto.CanonicityNone {
		return "", ErrInvalidSignature
	}
	if canonicality != rootcrypto.CanonicityFullyCanonical {
		sigBytes = rootcrypto.MakeSignatureCanonical(sigBytes)
		if sigBytes == nil {
			return "", ErrInvalidSignature
		}
	}
	return strings.ToUpper(hex.EncodeToString(sigBytes)), nil
}

// DeriveValidatorKeypair derives a validator keypair from a seed.
// This is a convenience function that calls DeriveKeypair with validator=true.
func (c SECP256K1CryptoAlgorithm) DeriveValidatorKeypair(seed []byte) (string, string, error) {
	return c.DeriveKeypair(seed, true)
}

// DerivePublicKeyFromSecret returns the 33-byte compressed secp256k1
// public key for a raw 32-byte secret. Mirrors rippled's
// derivePublicKey(KeyType::secp256k1, SecretKey) used by validator-token
// loading, where the JSON `validation_secret_key` already is the raw
// scalar (no seed expansion).
func (c SECP256K1CryptoAlgorithm) DerivePublicKeyFromSecret(secret []byte) ([]byte, error) {
	if len(secret) != 32 {
		return nil, ErrInvalidPrivateKey
	}
	_, pubKey := btcec.PrivKeyFromBytes(secret)
	return pubKey.SerializeCompressed(), nil
}

// DeriveAccountKeypair derives an account keypair from a seed.
// This is a convenience function that calls DeriveKeypair with validator=false.
func (c SECP256K1CryptoAlgorithm) DeriveAccountKeypair(seed []byte) (string, string, error) {
	return c.DeriveKeypair(seed, false)
}

// derFromRS builds a DER-encoded signature directly from a decred ModNScalar
// r/s pair. It avoids the string→hex→bytes round-trip done by DERHexFromSig.
func derFromRS(r, s secp256k1.ModNScalar) []byte {
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	return rootcrypto.EncodeDERSignature(
		new(big.Int).SetBytes(rBytes[:]),
		new(big.Int).SetBytes(sBytes[:]),
	)
}

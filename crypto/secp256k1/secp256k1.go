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
	familySeedPrefix byte
}

// SECP256K1 returns a new SECP256K1CryptoAlgorithm instance.
func SECP256K1() SECP256K1CryptoAlgorithm {
	return SECP256K1CryptoAlgorithm{
		prefix:           secp256K1Prefix,
		familySeedPrefix: secp256K1FamilySeedPrefix,
	}
}

// Prefix returns the prefix for the SECP256K1 algorithm.
func (c SECP256K1CryptoAlgorithm) Prefix() byte {
	return c.prefix
}

// FamilySeedPrefix returns the family seed prefix for the SECP256K1 algorithm.
func (c SECP256K1CryptoAlgorithm) FamilySeedPrefix() byte {
	return c.familySeedPrefix
}

// deriveScalar derives a scalar from a seed.
func (c SECP256K1CryptoAlgorithm) deriveScalar(bytes []byte, discrim *big.Int) *big.Int {
	order := btcec.S256().N
	for i := 0; i <= 0xffffffff; i++ {
		hash := sha512.New()

		hash.Write(bytes)

		if discrim != nil {
			discrimBytes := make([]byte, 4)
			discrimBytes[0] = byte(discrim.Uint64())
			discrimBytes[1] = byte(discrim.Uint64() >> 8)
			discrimBytes[2] = byte(discrim.Uint64() >> 16)
			discrimBytes[3] = byte(discrim.Uint64() >> 24)

			hash.Write(discrimBytes)
		}

		shiftBytes := make([]byte, 4)
		shiftBytes[0] = byte(i)
		shiftBytes[1] = byte(i >> 8)
		shiftBytes[2] = byte(i >> 16)
		shiftBytes[3] = byte(i >> 24)

		hash.Write(shiftBytes)

		key := new(big.Int).SetBytes(hash.Sum(nil)[:32])

		if key.Cmp(big.NewInt(0)) > 0 && key.Cmp(order) < 0 {
			return key
		}
	}
	// This error is practically impossible to reach.
	// The order of the curve describes the (finite) amount of points on the curve.
	panic("impossible unicorn ;)")
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

// Sign signs a message with a private key.
func (c SECP256K1CryptoAlgorithm) Sign(msg, privKey string) (string, error) {
	if len(privKey) != 64 && len(privKey) != 66 {
		return "", ErrInvalidPrivateKey
	}
	if len(msg) == 0 {
		return "", ErrInvalidMessage
	}

	if len(privKey) == 66 {
		privKey = privKey[2:]
	}
	key, err := hex.DecodeString(privKey)
	if err != nil {
		return "", ErrInvalidPrivateKey
	}

	secpPrivKey := secp256k1.PrivKeyFromBytes(key)
	hash := common.Sha512Half([]byte(msg))
	sig := ecdsa.Sign(secpPrivKey, hash[:])

	parsedSig, err := rootcrypto.DERHexFromSig(sig.R().String(), sig.S().String())
	if err != nil {
		return "", err
	}
	return strings.ToUpper(parsedSig), nil
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

	parsedSig, err := rootcrypto.DERHexFromSig(sig.R().String(), sig.S().String())
	if err != nil {
		return nil, err
	}
	sigBytes, err := hex.DecodeString(parsedSig)
	if err != nil {
		return nil, err
	}
	return sigBytes, nil
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
	// Decode the signature from hex
	sigBytes, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}

	// Check signature canonicality
	canonicality := rootcrypto.ECDSACanonicality(sigBytes)
	if canonicality == rootcrypto.CanonicityNone {
		return false
	}
	if mustBeFullyCanonical && canonicality != rootcrypto.CanonicityFullyCanonical {
		return false
	}

	// Decode the signature from DERHex to r and s
	r, s, err := rootcrypto.DERHexToSig(sig)
	if err != nil {
		return false
	}

	// Convert r and s slices to [32]byte arrays
	var rBytes, sBytes [32]byte

	copy(rBytes[32-len(r):], r)
	copy(sBytes[32-len(s):], s)

	ecdsaR := &secp256k1.ModNScalar{}
	ecdsaS := &secp256k1.ModNScalar{}

	ecdsaR.SetBytes(&rBytes)
	ecdsaS.SetBytes(&sBytes)

	parsedSig := ecdsa.NewSignature(ecdsaR, ecdsaS)
	// Hash the message
	hash := common.Sha512Half([]byte(msg))

	// Decode the pubkey from hex to a byte slice
	pubkeyBytes, err := hex.DecodeString(pubkey)
	if err != nil {
		return false
	}

	// Verify the signature
	pubKey, err := secp256k1.ParsePubKey(pubkeyBytes)
	if err != nil {
		return false
	}
	return parsedSig.Verify(hash[:], pubKey)
}

// ValidateDigest verifies a signature against a pre-computed digest (hash).
// Unlike Validate, this does NOT re-hash the data — it uses the digest directly.
// Matches rippled's verifyDigest() which passes the SHA-512Half hash directly
// to secp256k1_ecdsa_verify.
func (c SECP256K1CryptoAlgorithm) ValidateDigest(digest [32]byte, pubkeyBytes []byte, sigBytes []byte) bool {
	// Check signature canonicality
	canonicality := rootcrypto.ECDSACanonicality(sigBytes)
	if canonicality == rootcrypto.CanonicityNone {
		return false
	}

	// Decode the signature from DER to r and s
	sigHex := hex.EncodeToString(sigBytes)
	r, s, err := rootcrypto.DERHexToSig(sigHex)
	if err != nil {
		return false
	}

	var rBytes, sBytes [32]byte
	copy(rBytes[32-len(r):], r)
	copy(sBytes[32-len(s):], s)

	ecdsaR := &secp256k1.ModNScalar{}
	ecdsaS := &secp256k1.ModNScalar{}
	ecdsaR.SetBytes(&rBytes)
	ecdsaS.SetBytes(&sBytes)

	parsedSig := ecdsa.NewSignature(ecdsaR, ecdsaS)

	pubKey, err := secp256k1.ParsePubKey(pubkeyBytes)
	if err != nil {
		return false
	}
	return parsedSig.Verify(digest[:], pubKey)
}

// DerivePublicKeyFromPublicGenerator derives a public key from a public generator.
func (c SECP256K1CryptoAlgorithm) DerivePublicKeyFromPublicGenerator(pubKey []byte) ([]byte, error) {
	// Get the curve
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

	// Return compressed format
	return finalPubKey.SerializeCompressed(), nil
}

// SignCanonical signs a message and ensures the signature is fully canonical.
// It automatically normalizes the S value if needed to produce a low-S signature.
func (c SECP256K1CryptoAlgorithm) SignCanonical(msg, privKey string) (string, error) {
	sig, err := c.Sign(msg, privKey)
	if err != nil {
		return "", err
	}

	// Check if signature is already fully canonical
	sigBytes, err := hex.DecodeString(sig)
	if err != nil {
		return "", ErrInvalidSignature
	}

	canonicality := rootcrypto.ECDSACanonicality(sigBytes)
	if canonicality == rootcrypto.CanonicityNone {
		return "", ErrInvalidSignature
	}
	if canonicality == rootcrypto.CanonicityFullyCanonical {
		return sig, nil
	}

	// Make the signature canonical
	canonicalSig := rootcrypto.MakeSignatureCanonical(sigBytes)
	if canonicalSig == nil {
		return "", ErrInvalidSignature
	}

	return strings.ToUpper(hex.EncodeToString(canonicalSig)), nil
}

// DeriveValidatorKeypair derives a validator keypair from a seed.
// This is a convenience function that calls DeriveKeypair with validator=true.
func (c SECP256K1CryptoAlgorithm) DeriveValidatorKeypair(seed []byte) (string, string, error) {
	return c.DeriveKeypair(seed, true)
}

// DeriveAccountKeypair derives an account keypair from a seed.
// This is a convenience function that calls DeriveKeypair with validator=false.
func (c SECP256K1CryptoAlgorithm) DeriveAccountKeypair(seed []byte) (string, string, error) {
	return c.DeriveKeypair(seed, false)
}

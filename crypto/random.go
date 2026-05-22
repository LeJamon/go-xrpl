package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"

	"github.com/btcsuite/btcd/btcec/v2"
)

var (
	// ErrUnsupportedKeyType is returned when an unsupported key type is requested.
	ErrUnsupportedKeyType = errors.New("unsupported key type")
	// ErrRandomGeneration is returned when random number generation fails.
	ErrRandomGeneration = errors.New("failed to generate random bytes")
)

// RandomBytes generates n cryptographically secure random bytes.
// It uses crypto/rand which reads from the system's CSPRNG.
func RandomBytes(n int) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}

	b := make([]byte, n)
	_, err := io.ReadFull(rand.Reader, b)
	if err != nil {
		return nil, ErrRandomGeneration
	}
	return b, nil
}

// RandomSecretKey generates a random secret key for the specified key type.
// The returned SecretKey should be closed when no longer needed to securely
// erase the key material from memory.
func RandomSecretKey(keyType KeyType) (*SecretKey, error) {
	switch keyType {
	case KeyTypeSecp256k1:
		return randomSecp256k1SecretKey()
	case KeyTypeEd25519:
		return randomEd25519SecretKey()
	default:
		return nil, ErrUnsupportedKeyType
	}
}

// randomSecp256k1SecretKey generates a random secp256k1 secret key.
func randomSecp256k1SecretKey() (*SecretKey, error) {
	// Generate 32 random bytes
	key, err := RandomBytes(SecretKeySecp256k1Size)
	if err != nil {
		return nil, err
	}

	// Verify it's a valid private key (within curve order)
	// This also normalizes the key if needed
	privKey, _ := btcec.PrivKeyFromBytes(key)
	if privKey == nil {
		// Very unlikely, but regenerate if invalid
		SecureErase(key)
		return randomSecp256k1SecretKey()
	}

	normalizedKey := privKey.Serialize()
	SecureErase(key)

	return NewSecretKey(normalizedKey), nil
}

// randomEd25519SecretKey generates a random Ed25519 secret key seed.
func randomEd25519SecretKey() (*SecretKey, error) {
	seed, err := RandomBytes(SecretKeyEd25519Size)
	if err != nil {
		return nil, err
	}
	return NewSecretKey(seed), nil
}

// RandomKeyPair generates a random key pair for the specified key type.
// It returns the public key and private key as byte slices.
// The private key includes the appropriate prefix for XRPL compatibility.
//
// For secp256k1:
//   - Public key: 33 bytes (compressed format, 0x02 or 0x03 prefix)
//   - Private key: 33 bytes (0x00 prefix + 32 byte key)
//
// For Ed25519:
//   - Public key: 33 bytes (0xED prefix + 32 byte key)
//   - Private key: 33 bytes (0xED prefix + 32 byte seed)
func RandomKeyPair(keyType KeyType) (publicKey, privateKey []byte, err error) {
	switch keyType {
	case KeyTypeSecp256k1:
		return randomSecp256k1KeyPair()
	case KeyTypeEd25519:
		return randomEd25519KeyPair()
	default:
		return nil, nil, ErrUnsupportedKeyType
	}
}

// randomSecp256k1KeyPair generates a random secp256k1 key pair.
func randomSecp256k1KeyPair() (publicKey, privateKey []byte, err error) {
	sk, err := randomSecp256k1SecretKey()
	if err != nil {
		return nil, nil, err
	}
	defer sk.Close()

	privKey, pubKey := btcec.PrivKeyFromBytes(sk.Data())
	if privKey == nil {
		return nil, nil, ErrRandomGeneration
	}

	// Public key in compressed format
	publicKey = pubKey.SerializeCompressed()

	// Private key with 0x00 prefix for secp256k1
	privateKey = make([]byte, SecretKeySecp256k1WithPrefixSize)
	privateKey[0] = 0x00
	copy(privateKey[1:], privKey.Serialize())

	return publicKey, privateKey, nil
}

// randomEd25519KeyPair generates a random Ed25519 key pair.
func randomEd25519KeyPair() (publicKey, privateKey []byte, err error) {
	seed, err := RandomBytes(SecretKeyEd25519Size)
	if err != nil {
		return nil, nil, err
	}
	defer SecureErase(seed)

	// Generate the full Ed25519 key from the seed. The 64-byte expanded
	// private key carries the same secret material as the seed, so scrub
	// it before return rather than leaving it for the GC. Mirrors rippled
	// Seed.cpp:46-47 wiping secret buffers.
	fullPrivKey := ed25519.NewKeyFromSeed(seed)
	defer SecureErase(fullPrivKey)
	pubKey := fullPrivKey.Public().(ed25519.PublicKey)

	// Public key with 0xED prefix
	publicKey = make([]byte, 33)
	publicKey[0] = 0xED
	copy(publicKey[1:], pubKey)

	// Private key with 0xED prefix (just the seed)
	privateKey = make([]byte, SecretKeyEd25519WithPrefixSize)
	privateKey[0] = 0xED
	copy(privateKey[1:], seed)

	return publicKey, privateKey, nil
}

// RandomSeed generates a random 16-byte seed suitable for key derivation.
// This matches the standard XRPL seed size.
func RandomSeed() ([]byte, error) {
	return RandomBytes(16)
}

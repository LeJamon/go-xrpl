package peermanagement

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	btcecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
)

// Identity-related errors.
var (
	ErrInvalidPrivateKey = errors.New("invalid private key")
	ErrSignatureFailed   = errors.New("failed to sign message")
)

// Key encoding constants.
const (
	// NodePublicKeyPrefix is the prefix byte for XRPL node public keys (results in 'n' prefix).
	NodePublicKeyPrefix = 0x1C // 28 decimal

	// AccountPublicKeyPrefix is for account public keys (results in 'a' prefix).
	AccountPublicKeyPrefix = 0x23 // 35 decimal

	// CompressedPubKeyLen is the length of a compressed secp256k1 public key.
	CompressedPubKeyLen = 33

	// ChecksumLen is the length of the Base58Check checksum.
	ChecksumLen = 4
)

// Identity represents a node's cryptographic identity for peer authentication.
type Identity struct {
	privateKey *btcec.PrivateKey
	publicKey  *btcec.PublicKey
}

// NewIdentity creates a new random identity.
func NewIdentity() (*Identity, error) {
	privateKey, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	return &Identity{
		privateKey: privateKey,
		publicKey:  privateKey.PubKey(),
	}, nil
}

// NewIdentityFromSeed creates an identity from a 16-byte seed.
func NewIdentityFromSeed(seed []byte) (*Identity, error) {
	if len(seed) < 16 {
		return nil, errors.New("seed must be at least 16 bytes")
	}

	// Derive private key from seed using SHA-512
	h := sha512.New()
	h.Write(seed)
	hash := h.Sum(nil)

	privateKey, _ := btcec.PrivKeyFromBytes(hash[:32])

	return &Identity{
		privateKey: privateKey,
		publicKey:  privateKey.PubKey(),
	}, nil
}

// NewIdentityFromPrivateKey creates an identity from a hex-encoded private key.
func NewIdentityFromPrivateKey(privKeyHex string) (*Identity, error) {
	if len(privKeyHex) == 0 {
		return nil, ErrInvalidPrivateKey
	}

	// Remove 00 prefix if present
	if len(privKeyHex) == 66 && privKeyHex[:2] == "00" {
		privKeyHex = privKeyHex[2:]
	}

	if len(privKeyHex) != 64 {
		return nil, ErrInvalidPrivateKey
	}

	privKeyBytes, err := hex.DecodeString(privKeyHex)
	if err != nil {
		return nil, ErrInvalidPrivateKey
	}

	privateKey, _ := btcec.PrivKeyFromBytes(privKeyBytes)
	if privateKey == nil {
		return nil, ErrInvalidPrivateKey
	}

	return &Identity{
		privateKey: privateKey,
		publicKey:  privateKey.PubKey(),
	}, nil
}

// GenerateSeed generates a random 16-byte seed for identity creation.
func GenerateSeed() ([]byte, error) {
	seed := make([]byte, 16)
	_, err := rand.Read(seed)
	if err != nil {
		return nil, fmt.Errorf("failed to generate random seed: %w", err)
	}
	return seed, nil
}

// Sign signs a message with the identity's private key.
// The signature is in DER format suitable for XRPL peer authentication.
func (i *Identity) Sign(message []byte) ([]byte, error) {
	if i.privateKey == nil {
		return nil, ErrInvalidPrivateKey
	}

	// Hash the message with SHA-512 and take first 32 bytes (SHA-512 Half)
	h := sha512.New()
	h.Write(message)
	hash := h.Sum(nil)[:32]

	sig := btcecdsa.Sign(i.privateKey, hash)
	if sig == nil {
		return nil, ErrSignatureFailed
	}

	return sig.Serialize(), nil
}

// PublicKey returns the raw compressed public key bytes.
func (i *Identity) PublicKey() []byte {
	return i.publicKey.SerializeCompressed()
}

// PublicKeyHex returns the public key as a hex string.
func (i *Identity) PublicKeyHex() string {
	return hex.EncodeToString(i.publicKey.SerializeCompressed())
}

// EncodedPublicKey returns the base58-encoded public key with the node public prefix.
// This is the format used in XRPL peer handshakes (e.g., "n9K1Z...").
func (i *Identity) EncodedPublicKey() string {
	pubKeyBytes := i.publicKey.SerializeCompressed()

	payload := make([]byte, 1+len(pubKeyBytes))
	payload[0] = NodePublicKeyPrefix
	copy(payload[1:], pubKeyBytes)

	checksum := doubleSHA256Identity(payload)[:ChecksumLen]
	full := append(payload, checksum...)

	return addresscodec.EncodeBase58(full)
}

// PrivateKeyHex returns the private key as a hex string (with 00 prefix).
func (i *Identity) PrivateKeyHex() string {
	return "00" + hex.EncodeToString(i.privateKey.Serialize())
}

// BtcecPublicKey returns the underlying btcec public key.
func (i *Identity) BtcecPublicKey() *btcec.PublicKey {
	return i.publicKey
}

// GenerateIdentity creates a new random identity.
// This is an alias for NewIdentity for clarity.
func GenerateIdentity() (*Identity, error) {
	return NewIdentity()
}

// LoadIdentity loads an identity from disk.
func LoadIdentity(dataDir string) (*Identity, error) {
	keyPath := filepath.Join(dataDir, "node_identity.key")

	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load identity: %w", err)
	}

	return NewIdentityFromPrivateKey(string(data))
}

// Save saves the identity to disk.
func (i *Identity) Save(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	keyPath := filepath.Join(dataDir, "node_identity.key")
	return os.WriteFile(keyPath, []byte(i.PrivateKeyHex()), 0600)
}

// TLSCertificate generates a self-signed TLS certificate for this identity.
func (i *Identity) TLSCertificate() tls.Certificate {
	// Convert btcec private key to standard ecdsa key
	privKeyBytes := i.privateKey.Serialize()
	privKey := new(ecdsa.PrivateKey)
	privKey.Curve = elliptic.P256()
	privKey.D = new(big.Int).SetBytes(privKeyBytes)
	privKey.PublicKey.X, privKey.PublicKey.Y = privKey.Curve.ScalarBaseMult(privKeyBytes)

	// Create self-signed certificate template
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: i.EncodedPublicKey(),
		},
		NotBefore:             now,
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	// Self-sign the certificate
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privKey.PublicKey, privKey)
	if err != nil {
		// Return empty certificate on error (should not happen in practice)
		return tls.Certificate{}
	}

	// Encode to PEM
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privKeyBytes})

	cert, _ := tls.X509KeyPair(certPEM, keyPEM)
	return cert
}

// PublicKeyToken represents an XRPL node public key (from peer handshake).
type PublicKeyToken struct {
	key *btcec.PublicKey
}

// NewPublicKeyToken creates a PublicKeyToken from raw compressed bytes.
func NewPublicKeyToken(data []byte) (*PublicKeyToken, error) {
	if len(data) != CompressedPubKeyLen {
		return nil, ErrInvalidPublicKey
	}

	key, err := btcec.ParsePubKey(data)
	if err != nil {
		return nil, ErrInvalidPublicKey
	}

	return &PublicKeyToken{key: key}, nil
}

// NewPublicKeyTokenFromBtcec creates a PublicKeyToken from a btcec.PublicKey.
func NewPublicKeyTokenFromBtcec(key *btcec.PublicKey) *PublicKeyToken {
	return &PublicKeyToken{key: key}
}

// ParsePublicKeyToken decodes a Base58Check encoded node public key (e.g., "n9K1Z...").
func ParsePublicKeyToken(encoded string) (*PublicKeyToken, error) {
	data := addresscodec.DecodeBase58(encoded)
	if len(data) == 0 {
		return nil, ErrInvalidPublicKey
	}

	if len(data) < 1+CompressedPubKeyLen+ChecksumLen {
		return nil, ErrInvalidPublicKey
	}

	payloadLen := len(data) - ChecksumLen
	payload := data[:payloadLen]
	checksum := data[payloadLen:]

	expectedChecksum := doubleSHA256Identity(payload)[:ChecksumLen]
	if !equalBytesIdentity(checksum, expectedChecksum) {
		return nil, errors.New("invalid checksum")
	}

	if payload[0] != NodePublicKeyPrefix {
		return nil, errors.New("invalid key prefix")
	}

	keyBytes := payload[1:]
	return NewPublicKeyToken(keyBytes)
}

// Bytes returns the raw compressed public key bytes.
func (p *PublicKeyToken) Bytes() []byte {
	return p.key.SerializeCompressed()
}

// Encode returns the Base58Check encoded public key with node prefix.
func (p *PublicKeyToken) Encode() string {
	keyBytes := p.key.SerializeCompressed()

	payload := make([]byte, 1+len(keyBytes))
	payload[0] = NodePublicKeyPrefix
	copy(payload[1:], keyBytes)

	checksum := doubleSHA256Identity(payload)[:ChecksumLen]
	full := append(payload, checksum...)

	return addresscodec.EncodeBase58(full)
}

// BtcecKey returns the underlying btcec public key.
func (p *PublicKeyToken) BtcecKey() *btcec.PublicKey {
	return p.key
}

// Equal returns true if two public keys are equal.
func (p *PublicKeyToken) Equal(other *PublicKeyToken) bool {
	if p == nil || other == nil {
		return p == other
	}
	return p.key.IsEqual(other.key)
}

// doubleSHA256Identity computes SHA256(SHA256(data)).
func doubleSHA256Identity(data []byte) []byte {
	h := sha256.New()
	h.Write(data)
	first := h.Sum(nil)

	h.Reset()
	h.Write(first)
	return h.Sum(nil)
}

// equalBytesIdentity returns true if two byte slices are equal.
func equalBytesIdentity(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

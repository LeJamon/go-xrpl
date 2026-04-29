package peermanagement

import (
	"crypto/rand"
	"crypto/rsa"
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
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	btcecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/crypto/secp256k1"
)

var (
	ErrInvalidPrivateKey = errors.New("invalid private key")
	ErrSignatureFailed   = errors.New("failed to sign message")
)

const (
	NodePublicKeyPrefix    = 0x1C // base58 'n' prefix
	AccountPublicKeyPrefix = 0x23 // base58 'a' prefix
	CompressedPubKeyLen    = 33
	ChecksumLen            = 4
)

// Identity is the node's cryptographic identity used for peer
// authentication. The TLS keypair is generated lazily and cached —
// RSA-2048 keygen is 50–200 ms and we'd otherwise pay it on every dial.
type Identity struct {
	privateKey *btcec.PrivateKey
	publicKey  *btcec.PublicKey

	tlsOnce    sync.Once
	tlsCertPEM []byte
	tlsKeyPEM  []byte
	tlsErr     error
}

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

func NewIdentityFromSeed(seed []byte) (*Identity, error) {
	if len(seed) < 16 {
		return nil, errors.New("seed must be at least 16 bytes")
	}

	h := sha512.New()
	h.Write(seed)
	hash := h.Sum(nil)

	privateKey, _ := btcec.PrivKeyFromBytes(hash[:32])

	return &Identity{
		privateKey: privateKey,
		publicKey:  privateKey.PubKey(),
	}, nil
}

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

func GenerateSeed() ([]byte, error) {
	seed := make([]byte, 16)
	_, err := rand.Read(seed)
	if err != nil {
		return nil, fmt.Errorf("failed to generate random seed: %w", err)
	}
	return seed, nil
}

// Sign hashes message with sha512Half then signs (DER ECDSA).
func (i *Identity) Sign(message []byte) ([]byte, error) {
	if i.privateKey == nil {
		return nil, ErrInvalidPrivateKey
	}

	h := sha512.New()
	h.Write(message)
	hash := h.Sum(nil)[:32]

	sig := btcecdsa.Sign(i.privateKey, hash)
	if sig == nil {
		return nil, ErrSignatureFailed
	}

	return sig.Serialize(), nil
}

// SignDigest signs a pre-hashed 32-byte digest (used for session sigs).
func (i *Identity) SignDigest(digest []byte) ([]byte, error) {
	if i.privateKey == nil {
		return nil, ErrInvalidPrivateKey
	}
	return secp256k1.SignDigestBytes(digest, i.privateKey.Serialize())
}

// PublicKey returns the raw compressed public key bytes.
func (i *Identity) PublicKey() []byte {
	return i.publicKey.SerializeCompressed()
}

func (i *Identity) PublicKeyHex() string {
	return hex.EncodeToString(i.publicKey.SerializeCompressed())
}

// EncodedPublicKey returns the base58 'n...' form used in XRPL handshakes.
func (i *Identity) EncodedPublicKey() string {
	pubKeyBytes := i.publicKey.SerializeCompressed()

	payload := make([]byte, 1+len(pubKeyBytes))
	payload[0] = NodePublicKeyPrefix
	copy(payload[1:], pubKeyBytes)

	checksum := doubleSHA256Identity(payload)[:ChecksumLen]
	full := append(payload, checksum...)

	return addresscodec.EncodeBase58(full)
}

// PrivateKeyHex returns the private key as hex with the "00" prefix.
func (i *Identity) PrivateKeyHex() string {
	return "00" + hex.EncodeToString(i.privateKey.Serialize())
}

func (i *Identity) BtcecPublicKey() *btcec.PublicKey {
	return i.publicKey
}

// GenerateIdentity is an alias for NewIdentity.
func GenerateIdentity() (*Identity, error) {
	return NewIdentity()
}

func LoadIdentity(dataDir string) (*Identity, error) {
	keyPath := filepath.Join(dataDir, "node_identity.key")

	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load identity: %w", err)
	}

	return NewIdentityFromPrivateKey(string(data))
}

func (i *Identity) Save(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	keyPath := filepath.Join(dataDir, "node_identity.key")
	return os.WriteFile(keyPath, []byte(i.PrivateKeyHex()), 0600)
}

// TLSCertificatePEM returns a self-signed RSA-2048 TLS keypair (cached
// per Identity). The cert is unrelated to the secp256k1 node identity;
// peer trust comes from the Public-Key handshake header.
func (i *Identity) TLSCertificatePEM() (certPEM, keyPEM []byte, err error) {
	i.tlsOnce.Do(i.generateTLSCert)
	if i.tlsErr != nil {
		return nil, nil, i.tlsErr
	}
	return i.tlsCertPEM, i.tlsKeyPEM, nil
}

func (i *Identity) generateTLSCert() {
	tlsKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		i.tlsErr = fmt.Errorf("identity: generate TLS key: %w", err)
		return
	}

	// Back-date NotBefore so the cert doesn't leak the node's startup time.
	now := time.Now()
	notBefore := now.Add(-25 * time.Hour)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: i.EncodedPublicKey()},
		NotBefore:             notBefore,
		NotAfter:              now.Add(2 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &tlsKey.PublicKey, tlsKey)
	if err != nil {
		i.tlsErr = fmt.Errorf("identity: create TLS certificate: %w", err)
		return
	}
	i.tlsCertPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER := x509.MarshalPKCS1PrivateKey(tlsKey)
	i.tlsKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER})
}

// TLSCertificate is the crypto/tls form of TLSCertificatePEM, used by
// stdlib call sites (RPC HTTPS, WebSocket).
func (i *Identity) TLSCertificate() tls.Certificate {
	certPEM, keyPEM, err := i.TLSCertificatePEM()
	if err != nil {
		return tls.Certificate{}
	}
	cert, _ := tls.X509KeyPair(certPEM, keyPEM)
	return cert
}

// PublicKeyToken is a peer's secp256k1 node public key.
type PublicKeyToken struct {
	key *btcec.PublicKey
}

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

func NewPublicKeyTokenFromBtcec(key *btcec.PublicKey) *PublicKeyToken {
	return &PublicKeyToken{key: key}
}

// ParsePublicKeyToken decodes a base58 'n...' node public key.
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

	// Reject ed25519 keys explicitly so the error is clear instead of
	// an opaque btcec parse failure.
	if len(keyBytes) > 0 && keyBytes[0] == 0xED {
		return nil, errors.New("unsupported node public key type: ed25519 (rippled requires secp256k1)")
	}

	return NewPublicKeyToken(keyBytes)
}

func (p *PublicKeyToken) Bytes() []byte {
	return p.key.SerializeCompressed()
}

func (p *PublicKeyToken) Encode() string {
	keyBytes := p.key.SerializeCompressed()

	payload := make([]byte, 1+len(keyBytes))
	payload[0] = NodePublicKeyPrefix
	copy(payload[1:], keyBytes)

	checksum := doubleSHA256Identity(payload)[:ChecksumLen]
	full := append(payload, checksum...)

	return addresscodec.EncodeBase58(full)
}

func (p *PublicKeyToken) BtcecKey() *btcec.PublicKey {
	return p.key
}

func (p *PublicKeyToken) Equal(other *PublicKeyToken) bool {
	if p == nil || other == nil {
		return p == other
	}
	return p.key.IsEqual(other.key)
}

func doubleSHA256Identity(data []byte) []byte {
	h := sha256.New()
	h.Write(data)
	first := h.Sum(nil)

	h.Reset()
	h.Write(first)
	return h.Sum(nil)
}

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

package testing

import (
	"crypto/sha512"
	"encoding/hex"

	addresscodec "github.com/LeJamon/goXRPLd/codec/address-codec"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	ed25519 "github.com/LeJamon/goXRPLd/crypto/algorithms/ed25519"
	secp256k1 "github.com/LeJamon/goXRPLd/crypto/algorithms/secp256k1"
)

// KeyType constants for account key derivation.
const (
	KeyTypeSecp256k1 = "secp256k1"
	KeyTypeEd25519   = "ed25519"
)

// Account represents a test account with keypair and address information.
type Account struct {
	// Name is a human-readable identifier for the account (used for debugging).
	Name string

	// KeyType indicates the cryptographic algorithm used ("secp256k1" or "ed25519").
	KeyType string

	// Seed is the seed bytes used to derive the keypair.
	Seed []byte

	// Address is the classic XRPL address (e.g., "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh").
	Address string

	// PublicKey is the compressed public key bytes (33 bytes for secp256k1, 33 for ed25519 with prefix).
	PublicKey []byte

	// PrivateKey is the private key bytes (32 bytes).
	PrivateKey []byte

	// ID is the 20-byte account ID derived from the public key.
	ID [20]byte

	// Sequence tracks the account's sequence number (for test convenience).
	Sequence uint32
}

// NewAccount creates a new test account with a deterministic keypair derived from the name.
// Using the same name will always produce the same account, making tests reproducible.
// By default, uses secp256k1 key derivation.
func NewAccount(name string) *Account {
	return NewAccountWithKeyType(name, KeyTypeSecp256k1)
}

// NewAccountWithAddress creates a test account at a specific address.
// The account has no real keypair — it can only be used as a payment destination
// (to create/fund an account at that specific address) in test environments with
// signature verification disabled.
func NewAccountWithAddress(name string, address string) *Account {
	_, idBytes, _ := addresscodec.DecodeClassicAddressToAccountID(address)
	var id [20]byte
	copy(id[:], idBytes)
	return &Account{
		Name:    name,
		Address: address,
		ID:      id,
	}
}

// NewAccountWithKeyType creates a new test account with the specified key type.
// Supported key types: "secp256k1" and "ed25519".
func NewAccountWithKeyType(name string, keyType string) *Account {
	// Generate seed from name using SHA512-Half (first 16 bytes of SHA512)
	hash := sha512.Sum512([]byte(name))
	seed := hash[:16] // Use first 16 bytes as seed

	var privKeyHex, pubKeyHex string
	var err error

	switch keyType {
	case KeyTypeEd25519:
		algo := ed25519.ED25519()
		privKeyHex, pubKeyHex, err = algo.DeriveKeypair(seed, false)
		if err != nil {
			panic("failed to derive ed25519 keypair for account " + name + ": " + err.Error())
		}
	case KeyTypeSecp256k1:
		algo := secp256k1.SECP256K1()
		privKeyHex, pubKeyHex, err = algo.DeriveKeypair(seed, false)
		if err != nil {
			panic("failed to derive secp256k1 keypair for account " + name + ": " + err.Error())
		}
	default:
		panic("unsupported key type: " + keyType + " (must be 'secp256k1' or 'ed25519')")
	}

	// Decode private key (remove the leading prefix if present)
	privKey, err := hex.DecodeString(privKeyHex)
	if err != nil {
		panic("failed to decode private key: " + err.Error())
	}
	// The DeriveKeypair functions return prefixed private keys
	if len(privKey) == 33 {
		privKey = privKey[1:]
	}

	// Decode public key
	pubKey, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		panic("failed to decode public key: " + err.Error())
	}

	// Generate classic address from public key
	address, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(pubKeyHex)
	if err != nil {
		panic("failed to generate address: " + err.Error())
	}

	// Get the account ID (20 bytes)
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(address)
	if err != nil {
		panic("failed to decode account ID: " + err.Error())
	}

	var accountID [20]byte
	copy(accountID[:], accountIDBytes)

	return &Account{
		Name:       name,
		KeyType:    keyType,
		Seed:       seed,
		Address:    address,
		PublicKey:  pubKey,
		PrivateKey: privKey,
		ID:         accountID,
		Sequence:   1, // Default starting sequence
	}
}

// MasterAccount returns the well-known master account derived from "masterpassphrase".
// This is the genesis account that holds all XRP initially.
// Address: rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh
func MasterAccount() *Account {
	return NewAccountFromPassphrase("master", "masterpassphrase")
}

// NewAccountFromPassphrase creates a test account from a specific passphrase.
// This is useful for recreating well-known accounts.
// Uses secp256k1 by default.
func NewAccountFromPassphrase(name, passphrase string) *Account {
	return NewAccountFromPassphraseWithKeyType(name, passphrase, KeyTypeSecp256k1)
}

// NewAccountFromPassphraseWithKeyType creates a test account from a specific passphrase
// with the specified key type.
func NewAccountFromPassphraseWithKeyType(name, passphrase, keyType string) *Account {
	// Generate seed from passphrase using SHA512-Half
	hash := sha512.Sum512([]byte(passphrase))
	seed := hash[:16] // Use first 16 bytes as seed

	var privKeyHex, pubKeyHex string
	var err error

	switch keyType {
	case KeyTypeEd25519:
		algo := ed25519.ED25519()
		privKeyHex, pubKeyHex, err = algo.DeriveKeypair(seed, false)
		if err != nil {
			panic("failed to derive ed25519 keypair from passphrase: " + err.Error())
		}
	case KeyTypeSecp256k1:
		algo := secp256k1.SECP256K1()
		privKeyHex, pubKeyHex, err = algo.DeriveKeypair(seed, false)
		if err != nil {
			panic("failed to derive secp256k1 keypair from passphrase: " + err.Error())
		}
	default:
		panic("unsupported key type: " + keyType + " (must be 'secp256k1' or 'ed25519')")
	}

	// Decode private key (remove the leading prefix if present)
	privKey, err := hex.DecodeString(privKeyHex)
	if err != nil {
		panic("failed to decode private key: " + err.Error())
	}
	if len(privKey) == 33 {
		privKey = privKey[1:]
	}

	// Decode public key
	pubKey, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		panic("failed to decode public key: " + err.Error())
	}

	// Generate classic address from public key
	address, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(pubKeyHex)
	if err != nil {
		panic("failed to generate address: " + err.Error())
	}

	// Get the account ID (20 bytes)
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(address)
	if err != nil {
		panic("failed to decode account ID: " + err.Error())
	}

	var accountID [20]byte
	copy(accountID[:], accountIDBytes)

	return &Account{
		Name:       name,
		KeyType:    keyType,
		Seed:       seed,
		Address:    address,
		PublicKey:  pubKey,
		PrivateKey: privKey,
		ID:         accountID,
		Sequence:   1, // Default starting sequence
	}
}

// PublicKeyHex returns the public key as a hex string (uppercase).
func (a *Account) PublicKeyHex() string {
	return hex.EncodeToString(a.PublicKey)
}

// PrivateKeyHex returns the private key as a hex string (uppercase).
func (a *Account) PrivateKeyHex() string {
	return hex.EncodeToString(a.PrivateKey)
}

// AccountIDHex returns the account ID as a hex string.
func (a *Account) AccountIDHex() string {
	return hex.EncodeToString(a.ID[:])
}

// AccountID returns the 20-byte account ID.
// This is an alias for accessing the ID field directly.
func (a *Account) AccountID() [20]byte {
	return a.ID
}

// IsEd25519 returns true if this account uses Ed25519 cryptography.
func (a *Account) IsEd25519() bool {
	return a.KeyType == KeyTypeEd25519
}

// IsSecp256k1 returns true if this account uses secp256k1 cryptography.
func (a *Account) IsSecp256k1() bool {
	return a.KeyType == KeyTypeSecp256k1
}

// Human returns the human-readable address of the account.
// This is equivalent to accessing the Address field directly.
func (a *Account) Human() string {
	return a.Address
}

// String implements the Stringer interface for debugging.
func (a *Account) String() string {
	return a.Name + " (" + a.Address + ")"
}

// IOU returns a tx.Amount for this account as issuer of the given currency.
// Usage: gw.IOU("USD", 100) returns an issued amount of 100 USD from gw.
// Reference: rippled's Account::operator[]("USD")(100)
func (a *Account) IOU(currency string, value float64) tx.Amount {
	return state.NewIssuedAmountFromFloat64(value, currency, a.Address)
}

// NewAccountFromSeed creates a test account from a base58-encoded seed string.
// This is useful for recreating accounts from known seeds (e.g., from rippled test vectors).
func NewAccountFromSeed(name, base58Seed string) *Account {
	seedBytes, algo, err := addresscodec.DecodeSeed(base58Seed)
	if err != nil {
		panic("failed to decode base58 seed: " + err.Error())
	}

	// Determine key type from algorithm
	keyType := KeyTypeSecp256k1
	privKeyHex, pubKeyHex, err := algo.DeriveKeypair(seedBytes, false)
	if err != nil {
		panic("failed to derive keypair from seed: " + err.Error())
	}

	// Check if this is ed25519 by looking at the public key prefix
	pubKeyBytes, _ := hex.DecodeString(pubKeyHex)
	if len(pubKeyBytes) > 0 && pubKeyBytes[0] == 0xED {
		keyType = KeyTypeEd25519
	}

	// Decode private key (remove the leading prefix if present)
	privKey, err := hex.DecodeString(privKeyHex)
	if err != nil {
		panic("failed to decode private key: " + err.Error())
	}
	if len(privKey) == 33 {
		privKey = privKey[1:]
	}

	// Decode public key
	pubKey, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		panic("failed to decode public key: " + err.Error())
	}

	// Generate classic address from public key
	address, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(pubKeyHex)
	if err != nil {
		panic("failed to generate address: " + err.Error())
	}

	// Get the account ID (20 bytes)
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(address)
	if err != nil {
		panic("failed to decode account ID: " + err.Error())
	}

	var accountID [20]byte
	copy(accountID[:], accountIDBytes)

	return &Account{
		Name:       name,
		KeyType:    keyType,
		Seed:       seedBytes,
		Address:    address,
		PublicKey:  pubKey,
		PrivateKey: privKey,
		ID:         accountID,
		Sequence:   1,
	}
}

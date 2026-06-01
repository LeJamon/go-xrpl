// Package crypto provides cryptographic operations for the XRPL protocol.
package crypto

// KeyType represents the type of cryptographic key used in XRPL.
type KeyType int

const (
	// KeyTypeSecp256k1 indicates a secp256k1 (ECDSA) key. Matches rippled's
	// KeyType::secp256k1 = 0 (KeyType.h:29).
	KeyTypeSecp256k1 KeyType = 0
	// KeyTypeEd25519 indicates an Ed25519 key. Matches rippled's
	// KeyType::ed25519 = 1 (KeyType.h:30).
	KeyTypeEd25519 KeyType = 1
	// KeyTypeUnknown indicates an unknown or invalid key type. This is a
	// go-xrpl-only sentinel; rippled has no equivalent (parse failures are
	// signalled via std::optional). The enum is never serialised, so the
	// negative sentinel is purely internal.
	KeyTypeUnknown KeyType = -1
)

// String returns the string representation of the key type.
func (kt KeyType) String() string {
	switch kt {
	case KeyTypeSecp256k1:
		return "secp256k1"
	case KeyTypeEd25519:
		return "ed25519"
	default:
		return "unknown"
	}
}

// PublicKeyType determines the key type from a public key's raw bytes.
// It returns KeyTypeUnknown if the public key format is not recognized.
//
// Public key formats:
//   - Ed25519: 33 bytes, first byte is 0xED
//   - secp256k1: 33 bytes, first byte is 0x02 or 0x03 (compressed format)
func PublicKeyType(pubKey []byte) KeyType {
	if len(pubKey) != 33 {
		return KeyTypeUnknown
	}

	switch pubKey[0] {
	case 0xED:
		return KeyTypeEd25519
	case 0x02, 0x03:
		return KeyTypeSecp256k1
	default:
		return KeyTypeUnknown
	}
}

// IsValidPublicKey returns true if the public key has a valid format.
func IsValidPublicKey(pubKey []byte) bool {
	return PublicKeyType(pubKey) != KeyTypeUnknown
}

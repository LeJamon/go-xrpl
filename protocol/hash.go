package protocol

import (
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/go-xrpl/crypto/common"
)

// HashPrefix defines the prefix bytes used in XRPL hashing operations.
// These prefixes provide domain separation for different hash contexts.
type HashPrefix [4]byte

type Hash [32]byte

func NewHash(b []byte) (Hash, error) {
	if len(b) != 32 {
		return Hash{}, fmt.Errorf("expected 32 bytes, got %d", len(b))
	}

	var h Hash
	copy(h[:], b)

	return h, nil
}

func (h Hash) Bytes() []byte {
	return h[:]
}

func (h Hash) Hex() string {
	return hex.EncodeToString(h[:])
}

func (h Hash) String() string {
	return h.Hex()
}

func (h Hash) MarshalText() ([]byte, error) {
	return []byte(h.Hex()), nil
}

func (h *Hash) UnmarshalText(text []byte) error {
	b, err := hex.DecodeString(string(text))
	if err != nil {
		return err
	}

	if len(b) != 32 {
		return fmt.Errorf("invalid hash length")
	}

	copy(h[:], b)
	return nil
}

// Hash prefixes provide domain separation for the XRPL protocol's hashing
// contexts: each is the four-byte tag prepended to the payload before hashing,
// mirroring rippled's HashPrefix values.
var (
	HashPrefixLedgerMaster        = HashPrefix{'L', 'W', 'R', 0x00}
	HashPrefixInnerNode           = HashPrefix{'M', 'I', 'N', 0x00}
	HashPrefixLeafNode            = HashPrefix{'M', 'L', 'N', 0x00}
	HashPrefixTxNode              = HashPrefix{'S', 'N', 'D', 0x00}
	HashPrefixAccountStateEntry   = HashPrefix{'M', 'L', 'N', 0x00}
	HashPrefixTxSign              = HashPrefix{'S', 'T', 'X', 0x00}
	HashPrefixTxMultiSign         = HashPrefix{'S', 'M', 'T', 0x00}
	HashPrefixTransactionID       = HashPrefix{'T', 'X', 'N', 0x00}
	HashPrefixValidation          = HashPrefix{'V', 'A', 'L', 0x00}
	HashPrefixProposal            = HashPrefix{'P', 'R', 'P', 0x00}
	HashPrefixManifest            = HashPrefix{'M', 'A', 'N', 0x00}
	HashPrefixPaymentChannelClaim = HashPrefix{'C', 'L', 'M', 0x00}
	HashPrefixCredential          = HashPrefix{'C', 'R', 'D', 0x00}
	HashPrefixBatch               = HashPrefix{'B', 'C', 'H', 0x00}
)

// Bytes returns the prefix as a byte slice.
func (h HashPrefix) Bytes() []byte {
	return h[:]
}

// HashWithPrefix calcult the hash of a byte[] with a 4 byte prefix
func HashWithPrefix(prefix HashPrefix, payload []byte) Hash {
	data := make([]byte, 0, 4+len(payload))
	data = append(data, prefix[:]...)
	data = append(data, payload...)

	return common.Sha512Half(data)
}

// ComputeTxHash calculates the hash of a signed transaction
func ComputeTxHashBytes(txBytes []byte) Hash {
	return HashWithPrefix(HashPrefixTransactionID, txBytes)
}

// ComputeTxHashString calculates the hash of a signed transaction
func ComputeTxHashString(txBlobHex string) (Hash, error) {
	txBytes, err := hex.DecodeString(txBlobHex)
	if err != nil {
		return Hash{}, err
	}
	return ComputeTxHashBytes(txBytes), nil
}

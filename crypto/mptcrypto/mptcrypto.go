package mptcrypto

import "errors"

// PublicKeySize is the serialized size of a compressed secp256k1 public key.
// CiphertextSize is the serialized size of an ElGamal ciphertext.
// BlindingFactorSize is the serialized size of a ciphertext blinding factor.
// ConvertProofSize is the serialized size of a confidential convert proof.
// ConvertBackProofSize is the serialized size of a confidential convert-back proof.
// CommitmentSize is the serialized size of a Pedersen commitment.
const (
	PublicKeySize        = 33
	CiphertextSize       = 66
	BlindingFactorSize   = 32
	ConvertProofSize     = 64
	ConvertBackProofSize = 816
	SendProofSize        = 946
	ClawbackProofSize    = 64
	CommitmentSize       = 33
)

// ErrUnavailable reports that the optional native mpt-crypto backend is unavailable.
var ErrUnavailable = errors.New("mpt-crypto backend unavailable")

// Participant bundles the public key and encrypted amount used in a proof.
type Participant struct {
	PublicKey  []byte
	Ciphertext []byte
}

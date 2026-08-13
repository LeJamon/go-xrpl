// Package mptcrypto provides confidential MPT cryptographic operations.
package mptcrypto

// Protocol-defined serialized sizes for confidential MPT cryptographic values.
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

// Participant contains a public key and its encrypted balance.
type Participant struct {
	// PublicKey is the participant's compressed public key.
	PublicKey []byte
	// Ciphertext is the participant's encrypted balance.
	Ciphertext []byte
}

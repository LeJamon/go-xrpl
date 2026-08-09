package mptcrypto

import "errors"

const (
	PublicKeySize        = 33
	CiphertextSize       = 66
	BlindingFactorSize   = 32
	ConvertProofSize     = 64
	ConvertBackProofSize = 816
	CommitmentSize       = 33
)

var ErrUnavailable = errors.New("mpt-crypto backend unavailable")

type Participant struct {
	PublicKey  []byte
	Ciphertext []byte
}

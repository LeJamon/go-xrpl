//go:build !mptcrypto || !cgo

// Package mptcrypto exposes the optional native confidential-MPT cryptography backend.
package mptcrypto

// Available reports that the native mpt-crypto context is unavailable.
func Available() bool { return false }

// ValidPublicKey reports that the native backend is unavailable.
func ValidPublicKey([]byte) bool { return false }

// ValidCiphertext reports that the native backend is unavailable.
func ValidCiphertext([]byte) bool { return false }

// AddCiphertexts reports that the native backend is unavailable.
func AddCiphertexts([]byte, []byte) ([]byte, bool) { return nil, false }

// SubtractCiphertexts reports that the native backend is unavailable.
func SubtractCiphertexts([]byte, []byte) ([]byte, bool) { return nil, false }

// CanonicalZero reports that the native backend is unavailable.
func CanonicalZero([]byte, [20]byte, [24]byte) ([]byte, bool) { return nil, false }

// ConvertContext reports that the native backend is unavailable.
func ConvertContext([20]byte, [24]byte, uint32) ([32]byte, bool) { return [32]byte{}, false }

// ConvertBackContext reports that the native backend is unavailable.
func ConvertBackContext([20]byte, [24]byte, uint32, uint32) ([32]byte, bool) {
	return [32]byte{}, false
}

// SendContext derives the proof context for a confidential send transaction.
func SendContext([20]byte, [24]byte, uint32, [20]byte, uint32) ([32]byte, bool) {
	return [32]byte{}, false
}

// ClawbackContext derives the proof context for a confidential clawback transaction.
func ClawbackContext([20]byte, [24]byte, uint32, [20]byte) ([32]byte, bool) {
	return [32]byte{}, false
}

// VerifyRevealed reports that the native backend is unavailable.
func VerifyRevealed(uint64, [32]byte, Participant, Participant, *Participant) bool { return false }

// VerifyConvert reports that the native backend is unavailable.
func VerifyConvert([]byte, []byte, [32]byte) bool { return false }

// VerifyConvertBack reports that the native backend is unavailable.
func VerifyConvertBack([]byte, []byte, []byte, []byte, uint64, [32]byte) bool { return false }

// VerifySend verifies a confidential send proof.
func VerifySend([]byte, Participant, Participant, Participant, *Participant, []byte, []byte, []byte, [32]byte) bool {
	return false
}

// VerifyClawback verifies a confidential clawback proof.
func VerifyClawback([]byte, []byte, []byte, uint64, [32]byte) bool { return false }

// RerandomizeCiphertext rerandomizes a ciphertext with the supplied public key and randomness.
func RerandomizeCiphertext([]byte, []byte, [32]byte) ([]byte, bool) { return nil, false }

// GenerateKeyPair generates an MPT-crypto private and public key pair.
func GenerateKeyPair() ([32]byte, []byte, bool) { return [32]byte{}, nil, false }

// GenerateBlindingFactor generates a blinding factor for MPT-crypto commitments.
func GenerateBlindingFactor() ([32]byte, bool) { return [32]byte{}, false }

// EncryptAmount encrypts an amount for the supplied public key and blinding factor.
func EncryptAmount(uint64, []byte, [32]byte) ([]byte, bool) { return nil, false }

// GenerateConvertProof generates a confidential convert proof.
func GenerateConvertProof([]byte, [32]byte, [32]byte) ([]byte, bool) {
	return nil, false
}

// PedersenCommitment generates a Pedersen commitment for an amount and blinding factor.
func PedersenCommitment(uint64, [32]byte) ([]byte, bool) { return nil, false }

// GenerateConvertBackProof generates a confidential convert-back proof.
func GenerateConvertBackProof([32]byte, []byte, [32]byte, uint64, []byte, uint64, []byte, [32]byte) ([]byte, bool) {
	return nil, false
}

// GenerateSendProof generates a confidential send proof.
func GenerateSendProof([32]byte, uint64, []Participant, [32]byte, [32]byte, []byte, []byte, uint64, []byte, [32]byte) ([]byte, bool) {
	return nil, false
}

// GenerateClawbackProof generates a confidential clawback proof.
func GenerateClawbackProof([32]byte, []byte, [32]byte, uint64, []byte) ([]byte, bool) {
	return nil, false
}

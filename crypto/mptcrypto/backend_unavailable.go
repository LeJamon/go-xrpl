//go:build !mptcrypto || !cgo

package mptcrypto

// Available reports whether the native confidential MPT backend is available.
func Available() bool { return false }

// ValidPublicKey reports whether value is a valid confidential MPT public key.
func ValidPublicKey([]byte) bool { return false }

// ValidCommitment reports whether value is a valid Pedersen commitment.
func ValidCommitment([]byte) bool { return false }

// ValidCiphertext reports whether value is a valid encrypted balance.
func ValidCiphertext([]byte) bool { return false }

// AddCiphertexts adds two encrypted balances.
func AddCiphertexts([]byte, []byte) ([]byte, bool) { return nil, false }

// SubtractCiphertexts subtracts one encrypted balance from another.
func SubtractCiphertexts([]byte, []byte) ([]byte, bool) { return nil, false }

// CanonicalZero creates the canonical encrypted zero for an account and issuance.
func CanonicalZero([]byte, [20]byte, [24]byte) ([]byte, bool) { return nil, false }

// ConvertContext derives the proof context for a confidential conversion.
func ConvertContext([20]byte, [24]byte, uint32) ([32]byte, bool) { return [32]byte{}, false }

// ConvertBackContext derives the proof context for a conversion back to a public balance.
func ConvertBackContext([20]byte, [24]byte, uint32, uint32) ([32]byte, bool) {
	return [32]byte{}, false
}

// SendContext derives the proof context for a confidential transfer.
func SendContext([20]byte, [24]byte, uint32, [20]byte, uint32) ([32]byte, bool) {
	return [32]byte{}, false
}

// ClawbackContext derives the proof context for a confidential clawback.
func ClawbackContext([20]byte, [24]byte, uint32, [20]byte) ([32]byte, bool) {
	return [32]byte{}, false
}

// VerifyRevealed verifies that revealed balance data matches every participant ciphertext.
func VerifyRevealed(uint64, [32]byte, Participant, Participant, *Participant) bool { return false }

// VerifyConvert verifies a confidential conversion proof.
func VerifyConvert([]byte, []byte, [32]byte) bool { return false }

// VerifyConvertBack verifies a confidential conversion-back proof.
func VerifyConvertBack([]byte, []byte, []byte, []byte, uint64, [32]byte) bool { return false }

// VerifySend verifies a confidential transfer proof.
func VerifySend([]byte, Participant, Participant, Participant, *Participant, []byte, []byte, []byte, [32]byte) bool {
	return false
}

// VerifyClawback verifies a confidential clawback proof.
func VerifyClawback([]byte, []byte, []byte, uint64, [32]byte) bool { return false }

// RerandomizeCiphertext rerandomizes an encrypted balance.
func RerandomizeCiphertext([]byte, []byte, [32]byte) ([]byte, bool) { return nil, false }

// GenerateKeyPair creates a confidential MPT private and public key pair.
func GenerateKeyPair() ([32]byte, []byte, bool) { return [32]byte{}, nil, false }

// GenerateBlindingFactor creates a cryptographically secure blinding factor.
func GenerateBlindingFactor() ([32]byte, bool) { return [32]byte{}, false }

// EncryptAmount encrypts an amount for a public key with the supplied blinding factor.
func EncryptAmount(uint64, []byte, [32]byte) ([]byte, bool) { return nil, false }

// GenerateConvertProof creates a confidential conversion proof.
func GenerateConvertProof([]byte, [32]byte, [32]byte) ([]byte, bool) {
	return nil, false
}

// PedersenCommitment creates a commitment to an amount and blinding factor.
func PedersenCommitment(uint64, [32]byte) ([]byte, bool) { return nil, false }

// GenerateConvertBackProof creates a confidential conversion-back proof.
func GenerateConvertBackProof([32]byte, []byte, [32]byte, uint64, []byte, uint64, []byte, [32]byte) ([]byte, bool) {
	return nil, false
}

// GenerateSendProof creates a confidential transfer proof.
func GenerateSendProof([32]byte, uint64, []Participant, [32]byte, [32]byte, []byte, []byte, uint64, []byte, [32]byte) ([]byte, bool) {
	return nil, false
}

// GenerateClawbackProof creates a confidential clawback proof.
func GenerateClawbackProof([32]byte, []byte, [32]byte, uint64, []byte) ([]byte, bool) {
	return nil, false
}

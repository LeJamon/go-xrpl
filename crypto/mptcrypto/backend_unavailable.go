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
func SendContext([20]byte, [24]byte, uint32, [20]byte, uint32) ([32]byte, bool) {
	return [32]byte{}, false
}
func ClawbackContext([20]byte, [24]byte, uint32, [20]byte) ([32]byte, bool) {
	return [32]byte{}, false
}

// VerifyRevealed reports that the native backend is unavailable.
func VerifyRevealed(uint64, [32]byte, Participant, Participant, *Participant) bool { return false }

// VerifyConvert reports that the native backend is unavailable.
func VerifyConvert([]byte, []byte, [32]byte) bool { return false }

// VerifyConvertBack reports that the native backend is unavailable.
func VerifyConvertBack([]byte, []byte, []byte, []byte, uint64, [32]byte) bool { return false }
func VerifySend([]byte, Participant, Participant, Participant, *Participant, []byte, []byte, []byte, [32]byte) bool {
	return false
}
func VerifyClawback([]byte, []byte, []byte, uint64, [32]byte) bool  { return false }
func RerandomizeCiphertext([]byte, []byte, [32]byte) ([]byte, bool) { return nil, false }
func GenerateKeyPair() ([32]byte, []byte, bool)                     { return [32]byte{}, nil, false }
func GenerateBlindingFactor() ([32]byte, bool)                      { return [32]byte{}, false }
func EncryptAmount(uint64, []byte, [32]byte) ([]byte, bool)         { return nil, false }
func GenerateConvertProof([]byte, [32]byte, [32]byte) ([]byte, bool) {
	return nil, false
}
func PedersenCommitment(uint64, [32]byte) ([]byte, bool) { return nil, false }
func GenerateConvertBackProof([32]byte, []byte, [32]byte, uint64, []byte, uint64, []byte, [32]byte) ([]byte, bool) {
	return nil, false
}
func GenerateSendProof([32]byte, uint64, []Participant, [32]byte, [32]byte, []byte, []byte, uint64, []byte, [32]byte) ([]byte, bool) {
	return nil, false
}
func GenerateClawbackProof([32]byte, []byte, [32]byte, uint64, []byte) ([]byte, bool) {
	return nil, false
}

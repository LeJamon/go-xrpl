//go:build !mptcrypto || !cgo

package mptcrypto

func Available() bool { return false }

func ValidPublicKey([]byte) bool                                 { return false }
func ValidCiphertext([]byte) bool                                { return false }
func AddCiphertexts([]byte, []byte) ([]byte, bool)               { return nil, false }
func SubtractCiphertexts([]byte, []byte) ([]byte, bool)          { return nil, false }
func CanonicalZero([]byte, [20]byte, [24]byte) ([]byte, bool)    { return nil, false }
func ConvertContext([20]byte, [24]byte, uint32) ([32]byte, bool) { return [32]byte{}, false }
func ConvertBackContext([20]byte, [24]byte, uint32, uint32) ([32]byte, bool) {
	return [32]byte{}, false
}
func VerifyRevealed(uint64, [32]byte, Participant, Participant, *Participant) bool { return false }
func VerifyConvert([]byte, []byte, [32]byte) bool                                  { return false }
func VerifyConvertBack([]byte, []byte, []byte, []byte, uint64, [32]byte) bool      { return false }

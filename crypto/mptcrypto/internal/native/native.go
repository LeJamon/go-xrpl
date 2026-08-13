//go:build mptcrypto && cgo

package native

/*
#cgo pkg-config: goxrpl-mpt-crypto
#include "shim.h"
*/
import "C"

import (
	_ "github.com/LeJamon/go-xrpl/crypto/secp256k1/shim"
	"unsafe"
)

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

type Participant struct {
	PublicKey  []byte
	Ciphertext []byte
}

func bytePtr(value []byte) *C.uint8_t { return (*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(value))) }

func Available() bool { return C.go_mpt_available() == 1 }

func bytesResult(value []byte, ok bool) ([]byte, bool) {
	if !ok {
		return nil, false
	}
	return value, true
}

func ValidPublicKey(value []byte) bool {
	return len(value) == PublicKeySize && C.go_mpt_valid_pubkey(bytePtr(value)) == 1
}

func ValidCommitment(value []byte) bool { return ValidPublicKey(value) }

func ValidCiphertext(value []byte) bool {
	return len(value) == CiphertextSize && C.go_mpt_valid_ciphertext(bytePtr(value)) == 1
}

func combine(a, b []byte, subtract bool) ([]byte, bool) {
	if len(a) != CiphertextSize || len(b) != CiphertextSize {
		return nil, false
	}
	out := make([]byte, CiphertextSize)
	var ok C.int
	if subtract {
		ok = C.go_mpt_subtract(bytePtr(a), bytePtr(b), bytePtr(out))
	} else {
		ok = C.go_mpt_add(bytePtr(a), bytePtr(b), bytePtr(out))
	}
	return bytesResult(out, ok == 1)
}

func AddCiphertexts(a, b []byte) ([]byte, bool)      { return combine(a, b, false) }
func SubtractCiphertexts(a, b []byte) ([]byte, bool) { return combine(a, b, true) }

func CanonicalZero(pub []byte, account [20]byte, issuance [24]byte) ([]byte, bool) {
	if len(pub) != PublicKeySize {
		return nil, false
	}
	out := make([]byte, CiphertextSize)
	ok := C.go_mpt_canonical_zero(bytePtr(pub), bytePtr(account[:]), bytePtr(issuance[:]), bytePtr(out)) == 1
	return bytesResult(out, ok)
}

func ConvertContext(account [20]byte, issuance [24]byte, sequence uint32) ([32]byte, bool) {
	var out [32]byte
	ok := C.go_mpt_convert_context(bytePtr(account[:]), bytePtr(issuance[:]), C.uint32_t(sequence), bytePtr(out[:])) == 1
	if !ok {
		return [32]byte{}, false
	}
	return out, ok
}

func ConvertBackContext(account [20]byte, issuance [24]byte, sequence, version uint32) ([32]byte, bool) {
	var out [32]byte
	ok := C.go_mpt_convert_back_context(bytePtr(account[:]), bytePtr(issuance[:]), C.uint32_t(sequence), C.uint32_t(version), bytePtr(out[:])) == 1
	if !ok {
		return [32]byte{}, false
	}
	return out, ok
}

func SendContext(account [20]byte, issuance [24]byte, sequence uint32, destination [20]byte, version uint32) ([32]byte, bool) {
	var out [32]byte
	ok := C.go_mpt_send_context(bytePtr(account[:]), bytePtr(issuance[:]), C.uint32_t(sequence), bytePtr(destination[:]), C.uint32_t(version), bytePtr(out[:])) == 1
	if !ok {
		return [32]byte{}, false
	}
	return out, ok
}

func ClawbackContext(account [20]byte, issuance [24]byte, sequence uint32, holder [20]byte) ([32]byte, bool) {
	var out [32]byte
	ok := C.go_mpt_clawback_context(bytePtr(account[:]), bytePtr(issuance[:]), C.uint32_t(sequence), bytePtr(holder[:]), bytePtr(out[:])) == 1
	if !ok {
		return [32]byte{}, false
	}
	return out, ok
}

func validParticipant(p Participant) bool {
	return len(p.PublicKey) == PublicKeySize && len(p.Ciphertext) == CiphertextSize
}

func VerifyRevealed(amount uint64, blind [32]byte, holder, issuer Participant, auditor *Participant) bool {
	if !validParticipant(holder) || !validParticipant(issuer) {
		return false
	}
	zeroPub := make([]byte, PublicKeySize)
	zeroCT := make([]byte, CiphertextSize)
	hasAuditor, auditorPub, auditorCT := C.int(0), zeroPub, zeroCT
	if auditor != nil {
		if !validParticipant(*auditor) {
			return false
		}
		hasAuditor, auditorPub, auditorCT = 1, auditor.PublicKey, auditor.Ciphertext
	}
	return C.go_mpt_verify_revealed(C.uint64_t(amount), bytePtr(blind[:]), bytePtr(holder.PublicKey), bytePtr(holder.Ciphertext), bytePtr(issuer.PublicKey), bytePtr(issuer.Ciphertext), hasAuditor, bytePtr(auditorPub), bytePtr(auditorCT)) == 1
}

func VerifyConvert(proof, pub []byte, context [32]byte) bool {
	return len(proof) == ConvertProofSize && len(pub) == PublicKeySize && C.go_mpt_verify_convert(bytePtr(proof), bytePtr(pub), bytePtr(context[:])) == 1
}

func VerifyConvertBack(proof, pub, spending, commitment []byte, amount uint64, context [32]byte) bool {
	return len(proof) == ConvertBackProofSize && len(pub) == PublicKeySize && len(spending) == CiphertextSize && len(commitment) == CommitmentSize && C.go_mpt_verify_convert_back(bytePtr(proof), bytePtr(pub), bytePtr(spending), bytePtr(commitment), C.uint64_t(amount), bytePtr(context[:])) == 1
}

func VerifySend(proof []byte, sender, destination, issuer Participant, auditor *Participant, spending, amountCommitment, balanceCommitment []byte, context [32]byte) bool {
	if len(proof) != SendProofSize || !validParticipant(sender) || !validParticipant(destination) || !validParticipant(issuer) || len(spending) != CiphertextSize || len(amountCommitment) != CommitmentSize || len(balanceCommitment) != CommitmentSize {
		return false
	}
	participants := []Participant{sender, destination, issuer}
	if auditor != nil {
		if !validParticipant(*auditor) {
			return false
		}
		participants = append(participants, *auditor)
	}
	var pubs [4 * PublicKeySize]byte
	var ciphertexts [4 * CiphertextSize]byte
	for i, value := range participants {
		copy(pubs[i*PublicKeySize:], value.PublicKey)
		copy(ciphertexts[i*CiphertextSize:], value.Ciphertext)
	}
	return C.go_mpt_verify_send(bytePtr(proof), bytePtr(pubs[:]), bytePtr(ciphertexts[:]), C.uint8_t(len(participants)), bytePtr(spending), bytePtr(amountCommitment), bytePtr(balanceCommitment), bytePtr(context[:])) == 1
}

func VerifyClawback(proof, pub, ciphertext []byte, amount uint64, context [32]byte) bool {
	return len(proof) == ClawbackProofSize && len(pub) == PublicKeySize && len(ciphertext) == CiphertextSize && C.go_mpt_verify_clawback(bytePtr(proof), C.uint64_t(amount), bytePtr(pub), bytePtr(ciphertext), bytePtr(context[:])) == 1
}

func RerandomizeCiphertext(ciphertext, pub []byte, randomness [32]byte) ([]byte, bool) {
	if len(ciphertext) != CiphertextSize || len(pub) != PublicKeySize {
		return nil, false
	}
	out := make([]byte, CiphertextSize)
	return bytesResult(out, C.go_mpt_rerandomize(bytePtr(ciphertext), bytePtr(pub), bytePtr(randomness[:]), bytePtr(out)) == 1)
}

func GenerateKeyPair() ([32]byte, []byte, bool) {
	var private [32]byte
	public := make([]byte, PublicKeySize)
	ok := C.go_mpt_generate_keypair(bytePtr(private[:]), bytePtr(public)) == 1
	if !ok {
		return [32]byte{}, nil, false
	}
	return private, public, ok
}

func GenerateBlindingFactor() ([32]byte, bool) {
	var blind [32]byte
	ok := C.go_mpt_generate_blinding(bytePtr(blind[:])) == 1
	if !ok {
		return [32]byte{}, false
	}
	return blind, ok
}

func EncryptAmount(amount uint64, public []byte, blind [32]byte) ([]byte, bool) {
	if len(public) != PublicKeySize {
		return nil, false
	}
	out := make([]byte, CiphertextSize)
	return bytesResult(out, C.go_mpt_encrypt(C.uint64_t(amount), bytePtr(public), bytePtr(blind[:]), bytePtr(out)) == 1)
}

func GenerateConvertProof(public []byte, private [32]byte, context [32]byte) ([]byte, bool) {
	if len(public) != PublicKeySize {
		return nil, false
	}
	out := make([]byte, ConvertProofSize)
	return bytesResult(out, C.go_mpt_generate_convert_proof(bytePtr(public), bytePtr(private[:]), bytePtr(context[:]), bytePtr(out)) == 1)
}

func PedersenCommitment(amount uint64, blind [32]byte) ([]byte, bool) {
	out := make([]byte, CommitmentSize)
	return bytesResult(out, C.go_mpt_commitment(C.uint64_t(amount), bytePtr(blind[:]), bytePtr(out)) == 1)
}

func GenerateConvertBackProof(private [32]byte, public []byte, context [32]byte, amount uint64, balanceCommitment []byte, balance uint64, spending []byte, balanceBlind [32]byte) ([]byte, bool) {
	if len(public) != PublicKeySize || len(balanceCommitment) != CommitmentSize || len(spending) != CiphertextSize {
		return nil, false
	}
	out := make([]byte, ConvertBackProofSize)
	ok := C.go_mpt_generate_convert_back_proof(bytePtr(private[:]), bytePtr(public), bytePtr(context[:]), C.uint64_t(amount), bytePtr(balanceCommitment), C.uint64_t(balance), bytePtr(spending), bytePtr(balanceBlind[:]), bytePtr(out)) == 1
	return bytesResult(out, ok)
}

func GenerateSendProof(private [32]byte, amount uint64, participants []Participant, transactionBlind [32]byte, context [32]byte, amountCommitment, balanceCommitment []byte, balance uint64, spending []byte, balanceBlind [32]byte) ([]byte, bool) {
	if (len(participants) != 3 && len(participants) != 4) || len(amountCommitment) != CommitmentSize || len(balanceCommitment) != CommitmentSize || len(spending) != CiphertextSize {
		return nil, false
	}
	var pubs [4 * PublicKeySize]byte
	var ciphertexts [4 * CiphertextSize]byte
	for i, value := range participants {
		if !validParticipant(value) {
			return nil, false
		}
		copy(pubs[i*PublicKeySize:], value.PublicKey)
		copy(ciphertexts[i*CiphertextSize:], value.Ciphertext)
	}
	out := make([]byte, SendProofSize)
	ok := C.go_mpt_generate_send_proof(bytePtr(private[:]), C.uint64_t(amount), bytePtr(pubs[:]), bytePtr(ciphertexts[:]), C.uint8_t(len(participants)), bytePtr(transactionBlind[:]), bytePtr(context[:]), bytePtr(amountCommitment), bytePtr(balanceCommitment), C.uint64_t(balance), bytePtr(spending), bytePtr(balanceBlind[:]), bytePtr(out)) == 1
	return bytesResult(out, ok)
}

func GenerateClawbackProof(private [32]byte, public []byte, context [32]byte, amount uint64, ciphertext []byte) ([]byte, bool) {
	if len(public) != PublicKeySize || len(ciphertext) != CiphertextSize {
		return nil, false
	}
	out := make([]byte, ClawbackProofSize)
	ok := C.go_mpt_generate_clawback_proof(bytePtr(private[:]), bytePtr(public), bytePtr(context[:]), C.uint64_t(amount), bytePtr(ciphertext), bytePtr(out)) == 1
	return bytesResult(out, ok)
}

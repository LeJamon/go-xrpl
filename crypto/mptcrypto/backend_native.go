//go:build mptcrypto && cgo

package mptcrypto

/*
#cgo pkg-config: goxrpl-mpt-crypto
#include "shim.h"
*/
import "C"

import (
	_ "github.com/LeJamon/go-xrpl/crypto/secp256k1/shim"
	"unsafe"
)

func bytePtr(value []byte) *C.uint8_t { return (*C.uint8_t)(unsafe.Pointer(&value[0])) }

// Available reports whether the native mpt-crypto context initialized.
func Available() bool { return C.go_mpt_available() == 1 }

// ValidPublicKey reports whether value is a valid compressed secp256k1 public key.
func ValidPublicKey(value []byte) bool {
	return len(value) == PublicKeySize && C.go_mpt_valid_pubkey(bytePtr(value)) == 1
}

// ValidCiphertext reports whether value contains two valid compressed ciphertext points.
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
	return out, ok == 1
}

func AddCiphertexts(a, b []byte) ([]byte, bool)      { return combine(a, b, false) }
func SubtractCiphertexts(a, b []byte) ([]byte, bool) { return combine(a, b, true) }

// CanonicalZero returns the deterministic encryption of zero for an account and issuance.
func CanonicalZero(pub []byte, account [20]byte, issuance [24]byte) ([]byte, bool) {
	if len(pub) != PublicKeySize {
		return nil, false
	}
	out := make([]byte, CiphertextSize)
	ok := C.go_mpt_canonical_zero(bytePtr(pub), bytePtr(account[:]), bytePtr(issuance[:]), bytePtr(out)) == 1
	return out, ok
}

// ConvertContext returns the proof context for a confidential convert transaction.
func ConvertContext(account [20]byte, issuance [24]byte, sequence uint32) ([32]byte, bool) {
	var out [32]byte
	ok := C.go_mpt_convert_context(bytePtr(account[:]), bytePtr(issuance[:]), C.uint32_t(sequence), bytePtr(out[:])) == 1
	return out, ok
}

// ConvertBackContext returns the proof context for a confidential convert-back transaction.
func ConvertBackContext(account [20]byte, issuance [24]byte, sequence, version uint32) ([32]byte, bool) {
	var out [32]byte
	ok := C.go_mpt_convert_back_context(bytePtr(account[:]), bytePtr(issuance[:]), C.uint32_t(sequence), C.uint32_t(version), bytePtr(out[:])) == 1
	return out, ok
}

func validParticipant(p Participant) bool {
	return len(p.PublicKey) == PublicKeySize && len(p.Ciphertext) == CiphertextSize
}

// VerifyRevealed verifies that encrypted amounts reveal the claimed amount.
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

// VerifyConvert verifies a confidential convert proof.
func VerifyConvert(proof, pub []byte, context [32]byte) bool {
	return len(proof) == ConvertProofSize && len(pub) == PublicKeySize && C.go_mpt_verify_convert(bytePtr(proof), bytePtr(pub), bytePtr(context[:])) == 1
}

// VerifyConvertBack verifies a confidential convert-back proof.
func VerifyConvertBack(proof, pub, spending, commitment []byte, amount uint64, context [32]byte) bool {
	return len(proof) == ConvertBackProofSize && len(pub) == PublicKeySize && len(spending) == CiphertextSize && len(commitment) == CommitmentSize && C.go_mpt_verify_convert_back(bytePtr(proof), bytePtr(pub), bytePtr(spending), bytePtr(commitment), C.uint64_t(amount), bytePtr(context[:])) == 1
}

func generateKeyPair() ([32]byte, []byte, bool) {
	var private [32]byte
	public := make([]byte, PublicKeySize)
	ok := C.go_mpt_generate_keypair(bytePtr(private[:]), bytePtr(public)) == 1
	return private, public, ok
}

func generateBlinding() ([32]byte, bool) {
	var blind [32]byte
	ok := C.go_mpt_generate_blinding(bytePtr(blind[:])) == 1
	return blind, ok
}

func encrypt(amount uint64, public []byte, blind [32]byte) ([]byte, bool) {
	if len(public) != PublicKeySize {
		return nil, false
	}
	out := make([]byte, CiphertextSize)
	return out, C.go_mpt_encrypt(C.uint64_t(amount), bytePtr(public), bytePtr(blind[:]), bytePtr(out)) == 1
}

func generateConvertProof(public []byte, private [32]byte, context [32]byte) ([]byte, bool) {
	if len(public) != PublicKeySize {
		return nil, false
	}
	out := make([]byte, ConvertProofSize)
	return out, C.go_mpt_generate_convert_proof(bytePtr(public), bytePtr(private[:]), bytePtr(context[:]), bytePtr(out)) == 1
}

func commitment(amount uint64, blind [32]byte) ([]byte, bool) {
	out := make([]byte, CommitmentSize)
	return out, C.go_mpt_commitment(C.uint64_t(amount), bytePtr(blind[:]), bytePtr(out)) == 1
}

func generateConvertBackProof(private [32]byte, public []byte, context [32]byte, amount uint64, balanceCommitment []byte, balance uint64, spending []byte, balanceBlind [32]byte) ([]byte, bool) {
	if len(public) != PublicKeySize || len(balanceCommitment) != CommitmentSize || len(spending) != CiphertextSize {
		return nil, false
	}
	out := make([]byte, ConvertBackProofSize)
	ok := C.go_mpt_generate_convert_back_proof(bytePtr(private[:]), bytePtr(public), bytePtr(context[:]), C.uint64_t(amount), bytePtr(balanceCommitment), C.uint64_t(balance), bytePtr(spending), bytePtr(balanceBlind[:]), bytePtr(out)) == 1
	return out, ok
}

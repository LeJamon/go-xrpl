//go:build !mptcrypto || !cgo

package mptcrypto

import "testing"

func TestUnavailableBackendFailsClosed(t *testing.T) {
	if Available() {
		t.Fatal("unavailable backend reported available")
	}

	var account [20]byte
	var issuance [24]byte
	var scalar [32]byte
	participant := Participant{PublicKey: make([]byte, PublicKeySize), Ciphertext: make([]byte, CiphertextSize)}
	commitment := make([]byte, CommitmentSize)

	if _, ok := ConvertContext(account, issuance, 1); ok {
		t.Fatal("unavailable backend produced a context")
	}
	if VerifyRevealed(1, scalar, participant, participant, nil) ||
		VerifyConvert(make([]byte, ConvertProofSize), participant.PublicKey, scalar) ||
		VerifyConvertBack(make([]byte, ConvertBackProofSize), participant.PublicKey, participant.Ciphertext, commitment, 1, scalar) ||
		VerifySend(make([]byte, SendProofSize), participant, participant, participant, nil, participant.Ciphertext, commitment, commitment, scalar) ||
		VerifyClawback(make([]byte, ClawbackProofSize), participant.PublicKey, participant.Ciphertext, 1, scalar) {
		t.Fatal("unavailable backend accepted a proof")
	}
	if out, ok := AddCiphertexts(participant.Ciphertext, participant.Ciphertext); ok || out != nil {
		t.Fatal("unavailable backend produced a ciphertext")
	}
	if private, public, ok := GenerateKeyPair(); ok || private != [32]byte{} || public != nil {
		t.Fatal("unavailable backend produced key material")
	}
}

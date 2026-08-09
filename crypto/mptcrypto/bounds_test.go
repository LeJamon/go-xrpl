package mptcrypto

import "testing"

func TestRejectsMalformedInputSizes(t *testing.T) {
	var scalar [32]byte
	participant := Participant{
		PublicKey:  make([]byte, PublicKeySize),
		Ciphertext: make([]byte, CiphertextSize),
	}
	commitment := make([]byte, CommitmentSize)
	ciphertext := make([]byte, CiphertextSize)

	for _, size := range []int{0, PublicKeySize - 1, PublicKeySize + 1} {
		value := make([]byte, size)
		if ValidPublicKey(value) || ValidCommitment(value) {
			t.Fatalf("accepted %d-byte compressed point", size)
		}
		if _, ok := CanonicalZero(value, [20]byte{}, [24]byte{}); ok {
			t.Fatalf("canonical zero accepted %d-byte key", size)
		}
		if _, ok := EncryptAmount(1, value, scalar); ok {
			t.Fatalf("encryption accepted %d-byte key", size)
		}
	}

	for _, size := range []int{0, CiphertextSize - 1, CiphertextSize + 1} {
		value := make([]byte, size)
		if ValidCiphertext(value) {
			t.Fatalf("accepted %d-byte ciphertext", size)
		}
		if out, ok := AddCiphertexts(value, ciphertext); ok || out != nil {
			t.Fatalf("addition accepted %d-byte ciphertext", size)
		}
		if out, ok := SubtractCiphertexts(ciphertext, value); ok || out != nil {
			t.Fatalf("subtraction accepted %d-byte ciphertext", size)
		}
		if out, ok := RerandomizeCiphertext(value, participant.PublicKey, scalar); ok || out != nil {
			t.Fatalf("rerandomization accepted %d-byte ciphertext", size)
		}
	}

	for _, size := range []int{ConvertProofSize - 1, ConvertProofSize + 1} {
		if VerifyConvert(make([]byte, size), participant.PublicKey, scalar) {
			t.Fatalf("accepted %d-byte convert proof", size)
		}
	}
	for _, size := range []int{ConvertBackProofSize - 1, ConvertBackProofSize + 1} {
		if VerifyConvertBack(make([]byte, size), participant.PublicKey, ciphertext, commitment, 1, scalar) {
			t.Fatalf("accepted %d-byte convert-back proof", size)
		}
	}
	for _, size := range []int{SendProofSize - 1, SendProofSize + 1} {
		if VerifySend(make([]byte, size), participant, participant, participant, nil, ciphertext, commitment, commitment, scalar) {
			t.Fatalf("accepted %d-byte send proof", size)
		}
	}
	for _, size := range []int{ClawbackProofSize - 1, ClawbackProofSize + 1} {
		if VerifyClawback(make([]byte, size), participant.PublicKey, ciphertext, 1, scalar) {
			t.Fatalf("accepted %d-byte clawback proof", size)
		}
	}

	invalid := Participant{PublicKey: participant.PublicKey[:PublicKeySize-1], Ciphertext: ciphertext}
	if VerifyRevealed(1, scalar, invalid, participant, nil) {
		t.Fatal("accepted malformed revealed-amount participant")
	}
	if VerifySend(make([]byte, SendProofSize), invalid, participant, participant, nil, ciphertext, commitment, commitment, scalar) {
		t.Fatal("accepted malformed send participant")
	}

	if out, ok := GenerateConvertProof(participant.PublicKey[:PublicKeySize-1], scalar, scalar); ok || out != nil {
		t.Fatal("generated convert proof for malformed key")
	}
	if out, ok := GenerateConvertBackProof(scalar, participant.PublicKey, scalar, 1, commitment[:CommitmentSize-1], 1, ciphertext, scalar); ok || out != nil {
		t.Fatal("generated convert-back proof for malformed commitment")
	}
	if out, ok := GenerateSendProof(scalar, 1, []Participant{participant, participant}, scalar, scalar, commitment, commitment, 1, ciphertext, scalar); ok || out != nil {
		t.Fatal("generated send proof with invalid participant count")
	}
	if out, ok := GenerateClawbackProof(scalar, participant.PublicKey, scalar, 1, ciphertext[:CiphertextSize-1]); ok || out != nil {
		t.Fatal("generated clawback proof for malformed ciphertext")
	}
}

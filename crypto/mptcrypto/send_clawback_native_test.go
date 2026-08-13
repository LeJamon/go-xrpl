//go:build mptcrypto && cgo

package mptcrypto

import (
	"encoding/hex"
	"testing"
)

func TestContextHashVectors(t *testing.T) {
	var account, other [20]byte
	var issuance [24]byte
	for i := range account {
		account[i] = byte(i + 1)
		other[i] = byte(i + 31)
	}
	for i := range issuance {
		issuance[i] = byte(i + 61)
	}

	tests := []struct {
		name     string
		expected string
		context  func() ([32]byte, bool)
	}{
		{
			name:     "convert",
			expected: "74ad5b85b3e15e1b42552150ceccab860c7e887ba522f35e571333463c544270",
			context: func() ([32]byte, bool) {
				return ConvertContext(account, issuance, 0x01020304)
			},
		},
		{
			name:     "convert back",
			expected: "f96c41e43ae1e567b39aa2d648453d64cdfed08844d7eaedebdfa76801439542",
			context: func() ([32]byte, bool) {
				return ConvertBackContext(account, issuance, 0x01020304, 0x05060708)
			},
		},
		{
			name:     "send",
			expected: "bacac144a1d7d7836ea6d4badfe25f087803e1afdb6851dea4b1b11a080b4afd",
			context: func() ([32]byte, bool) {
				return SendContext(account, issuance, 0x01020304, other, 0x05060708)
			},
		},
		{
			name:     "clawback",
			expected: "913803539c8be30ad32fc54f6e2042382c2f6fab97437f70b34ce0e3a83adf50",
			context: func() ([32]byte, bool) {
				return ClawbackContext(account, issuance, 0x01020304, other)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expectedBytes, err := hex.DecodeString(test.expected)
			if err != nil {
				t.Fatal(err)
			}
			var expected [32]byte
			copy(expected[:], expectedBytes)
			context, ok := test.context()
			if !ok || context != expected {
				t.Fatalf("context = %x, want %x", context, expected)
			}
		})
	}
}

func TestNativeSendProof(t *testing.T) {
	for _, withAuditor := range []bool{false, true} {
		t.Run(map[bool]string{false: "three participants", true: "four participants"}[withAuditor], func(t *testing.T) {
			senderPrivate, senderPublic := mustKeyPair(t)
			_, destinationPublic := mustKeyPair(t)
			_, issuerPublic := mustKeyPair(t)
			_, auditorPublic := mustKeyPair(t)
			amountBlind := mustBlind(t)
			participants := []Participant{
				{PublicKey: senderPublic, Ciphertext: mustEncrypt(t, 40, senderPublic, amountBlind)},
				{PublicKey: destinationPublic, Ciphertext: mustEncrypt(t, 40, destinationPublic, amountBlind)},
				{PublicKey: issuerPublic, Ciphertext: mustEncrypt(t, 40, issuerPublic, amountBlind)},
			}
			if withAuditor {
				participants = append(participants, Participant{PublicKey: auditorPublic, Ciphertext: mustEncrypt(t, 40, auditorPublic, amountBlind)})
			}
			amountCommitment, ok := PedersenCommitment(40, amountBlind)
			if !ok {
				t.Fatal("amount commitment")
			}
			balanceBlind := mustBlind(t)
			spending := mustEncrypt(t, 100, senderPublic, balanceBlind)
			balanceCommitment, ok := PedersenCommitment(100, balanceBlind)
			if !ok {
				t.Fatal("balance commitment")
			}
			var account, destination [20]byte
			var issuance [24]byte
			account[0], destination[0], issuance[0] = 1, 2, 3
			context, ok := SendContext(account, issuance, 7, destination, 9)
			if !ok {
				t.Fatal("send context")
			}
			proof, ok := GenerateSendProof(senderPrivate, 40, participants, amountBlind, context, amountCommitment, balanceCommitment, 100, spending, balanceBlind)
			if !ok || len(proof) != SendProofSize {
				t.Fatal("generate send proof")
			}
			var auditor *Participant
			if withAuditor {
				auditor = &participants[3]
			}
			verify := func(candidate []byte, sender, destination, issuer Participant, auditor *Participant, spending, amountCommitment, balanceCommitment []byte, context [32]byte) bool {
				return VerifySend(candidate, sender, destination, issuer, auditor, spending, amountCommitment, balanceCommitment, context)
			}
			if !verify(proof, participants[0], participants[1], participants[2], auditor, spending, amountCommitment, balanceCommitment, context) {
				t.Fatal("verify send proof")
			}

			for _, offset := range []int{0, 192} {
				tampered := append([]byte(nil), proof...)
				tampered[offset] ^= 1
				if verify(tampered, participants[0], participants[1], participants[2], auditor, spending, amountCommitment, balanceCommitment, context) {
					t.Fatalf("accepted proof tampered at offset %d", offset)
				}
			}
			wrongContext := context
			wrongContext[0] ^= 1
			if verify(proof, participants[0], participants[1], participants[2], auditor, spending, amountCommitment, balanceCommitment, wrongContext) {
				t.Fatal("accepted wrong context")
			}
			wrongSpending := mustEncrypt(t, 100, senderPublic, mustBlind(t))
			if verify(proof, participants[0], participants[1], participants[2], auditor, wrongSpending, amountCommitment, balanceCommitment, context) {
				t.Fatal("accepted wrong spending ciphertext")
			}
			wrongAmountCommitment, _ := PedersenCommitment(41, amountBlind)
			if verify(proof, participants[0], participants[1], participants[2], auditor, spending, wrongAmountCommitment, balanceCommitment, context) {
				t.Fatal("accepted wrong amount commitment")
			}
			wrongBalanceCommitment, _ := PedersenCommitment(99, balanceBlind)
			if verify(proof, participants[0], participants[1], participants[2], auditor, spending, amountCommitment, wrongBalanceCommitment, context) {
				t.Fatal("accepted wrong balance commitment")
			}
			wrongSender := participants[0]
			wrongSender.PublicKey = destinationPublic
			if verify(proof, wrongSender, participants[1], participants[2], auditor, spending, amountCommitment, balanceCommitment, context) {
				t.Fatal("accepted wrong sender key")
			}
			commonC1Mismatch := participants[1]
			commonC1Mismatch.Ciphertext = mustEncrypt(t, 40, destinationPublic, mustBlind(t))
			if verify(proof, participants[0], commonC1Mismatch, participants[2], auditor, spending, amountCommitment, balanceCommitment, context) {
				t.Fatal("accepted non-common ciphertext C1")
			}

			var randomness [32]byte
			copy(randomness[:], proof[:32])
			rerandomized, ok := RerandomizeCiphertext(participants[1].Ciphertext, destinationPublic, randomness)
			if !ok || !ValidCiphertext(rerandomized) {
				t.Fatal("rerandomize ciphertext")
			}
			if string(rerandomized) == string(participants[1].Ciphertext) {
				t.Fatal("rerandomization did not change ciphertext")
			}
		})
	}
}

func TestNativeClawbackProof(t *testing.T) {
	private, public := mustKeyPair(t)
	blind := mustBlind(t)
	ciphertext := mustEncrypt(t, 75, public, blind)
	var issuer, holder [20]byte
	var issuance [24]byte
	issuer[0], holder[0], issuance[0] = 1, 2, 3
	context, ok := ClawbackContext(issuer, issuance, 11, holder)
	if !ok {
		t.Fatal("clawback context")
	}
	proof, ok := GenerateClawbackProof(private, public, context, 75, ciphertext)
	if !ok || len(proof) != ClawbackProofSize || !VerifyClawback(proof, public, ciphertext, 75, context) {
		t.Fatal("clawback proof")
	}

	tampered := append([]byte(nil), proof...)
	tampered[0] ^= 1
	if VerifyClawback(tampered, public, ciphertext, 75, context) {
		t.Fatal("accepted tampered clawback proof")
	}
	if VerifyClawback(proof, public, ciphertext, 74, context) {
		t.Fatal("accepted wrong clawback amount")
	}
	wrongContext := context
	wrongContext[0] ^= 1
	if VerifyClawback(proof, public, ciphertext, 75, wrongContext) {
		t.Fatal("accepted wrong clawback context")
	}
	_, wrongPublic := mustKeyPair(t)
	if VerifyClawback(proof, wrongPublic, ciphertext, 75, context) {
		t.Fatal("accepted wrong clawback public key")
	}
	wrongCiphertext := mustEncrypt(t, 75, public, mustBlind(t))
	if VerifyClawback(proof, public, wrongCiphertext, 75, context) {
		t.Fatal("accepted wrong clawback ciphertext")
	}
}

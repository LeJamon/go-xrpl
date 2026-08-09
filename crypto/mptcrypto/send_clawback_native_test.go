//go:build mptcrypto && cgo

package mptcrypto

import (
	"crypto/sha512"
	"encoding/binary"
	"testing"
)

func makeContextInput(transactionType uint16, account [20]byte, issuance [24]byte, sequence uint32, other [20]byte, version uint32) [32]byte {
	input := make([]byte, 0, 2+20+24+4+20+4)
	input = binary.BigEndian.AppendUint16(input, transactionType)
	input = append(input, account[:]...)
	input = append(input, issuance[:]...)
	input = binary.BigEndian.AppendUint32(input, sequence)
	input = append(input, other[:]...)
	input = binary.BigEndian.AppendUint32(input, version)
	digest := sha512.Sum512(input)
	var context [32]byte
	copy(context[:], digest[:32])
	return context
}

func TestSendAndClawbackContexts(t *testing.T) {
	var account, other [20]byte
	var issuance [24]byte
	for i := range account {
		account[i] = byte(i + 1)
		other[i] = byte(i + 31)
	}
	for i := range issuance {
		issuance[i] = byte(i + 61)
	}

	send, ok := SendContext(account, issuance, 0x01020304, other, 0x05060708)
	if !ok || send != makeContextInput(88, account, issuance, 0x01020304, other, 0x05060708) {
		t.Fatal("send context does not match the wire transcript")
	}
	clawback, ok := ClawbackContext(account, issuance, 0x01020304, other)
	if !ok || clawback != makeContextInput(89, account, issuance, 0x01020304, other, 0) {
		t.Fatal("clawback context does not match the wire transcript")
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
